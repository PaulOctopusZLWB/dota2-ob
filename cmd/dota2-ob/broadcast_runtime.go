package main

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
)

const overlayFreshness = 2 * time.Second

type broadcastConfig struct {
	DataRoot     string
	SessionID    string
	RawPath      string
	Lineage      contracts.PolicyLineageManifestV2
	Now          func() time.Time
	StoreOptions []commitlog.V2Option
}

type broadcastRuntime struct {
	mu       sync.Mutex
	app      *policy.Application
	store    *commitlog.StoreV2
	now      func() time.Time
	lineage  contracts.PolicyLineageManifestV2
	previous *contracts.LiveObservationV1
	overlay  contracts.OverlayStateV1
}

func newBroadcastRuntime(config broadcastConfig) (*broadcastRuntime, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	if !matchesProductLineage(config.Lineage, config.SessionID) {
		return nil, errors.New("broadcast lineage configuration mismatch")
	}
	policyConfig := policy.DefaultConfig()
	policyConfig.LineageID = config.Lineage.MustContentID()
	policyConfig.CandidateConfigVersion = config.Lineage.Config.Version
	policyConfig.CandidateConfigArtifact = config.Lineage.Config
	policyConfig.CandidateRulesArtifact = config.Lineage.Rules
	replayVerifier, verificationResolver := newProductionReplayVerifier(config.RawPath, config.SessionID, config.Lineage, policyConfig)
	verifier := commitlog.WithV2ReplayVerifier(replayVerifier)
	storeOptions := append([]commitlog.V2Option(nil), config.StoreOptions...)
	storeOptions = append(storeOptions, verifier)
	store, _, err := commitlog.OpenV2(config.DataRoot, config.SessionID, config.Lineage, storeOptions...)
	verificationCloseErr := verificationResolver.Close()
	if err != nil {
		return nil, err
	}
	if verificationCloseErr != nil {
		_ = store.Close()
		return nil, verificationCloseErr
	}
	resolver := newObservationResolver(config.RawPath, config.SessionID, config.Lineage)
	app, err := recoverProductionApplication(store, config.SessionID, config.Lineage, policyConfig, resolver)
	resolverCloseErr := resolver.Close()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if resolverCloseErr != nil {
		_ = store.Close()
		return nil, resolverCloseErr
	}
	nowMS := config.Now().UTC().UnixMilli()
	hidden, err := presentation.Hidden(config.SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), "waiting_for_policy_decision")
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &broadcastRuntime{app: app, store: store, now: config.Now, lineage: config.Lineage, previous: resolver.previous, overlay: hidden}, nil
}

func (r *broadcastRuntime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *broadcastRuntime) commitCandidates(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commitCandidatesLocked(observation, candidates, policyTimeMS)
}

func (r *broadcastRuntime) applyObservation(ctx context.Context, observation contracts.LiveObservationV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if observation.Evidence.Sequence <= r.app.State().LastObservationSequence {
		copyObservation := observation
		r.previous = &copyObservation
		return nil
	}
	policyTimeMS := r.now().UTC().UnixMilli()
	candidates := insight.Evaluate(insight.Input{
		Observation: observation, Previous: r.previous, Lineage: &r.lineage, PolicyTimeMS: policyTimeMS,
	}, insight.DefaultConfig())
	return r.commitCandidatesLocked(observation, candidates, policyTimeMS)
}

func (r *broadcastRuntime) commitCandidatesLocked(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	liveHash, err := contracts.CanonicalSHA256(observation)
	if err != nil {
		return err
	}
	commit, err := r.app.EvaluateObservation(observation.Evidence.Sequence, observation.Evidence.RawPayloadSHA256, liveHash, observation.Evidence, candidates, policyTimeMS)
	if err != nil {
		r.hideLocked("policy_commit_failed")
		return err
	}
	copyObservation := observation
	r.previous = &copyObservation
	r.publishLocked(commit)
	return nil
}

func (r *broadcastRuntime) execute(ctx context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OperatorCommandResultV1{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	duplicate := hasCommandResult(r.app.State().CommandResults, command.CommandID)
	commit, err := r.app.ApplyCommand(command)
	if err != nil {
		if !errors.Is(err, contracts.ErrSessionCommandLimit) && !errors.Is(err, contracts.ErrPolicyIdentifierLimit) && !errors.Is(err, contracts.ErrMalformedCommand) {
			r.hideLocked("policy_commit_failed")
		}
		return contracts.OperatorCommandResultV1{}, err
	}
	if commit.CommandResult == nil {
		r.hideLocked("invalid_policy_result")
		return contracts.OperatorCommandResultV1{}, errors.New("committed command result missing")
	}
	if duplicate {
		return *commit.CommandResult, nil
	}
	r.publishLocked(commit)
	return *commit.CommandResult, nil
}

func hasCommandResult(results []contracts.PolicyCommandResultRefV2, commandID string) bool {
	for _, result := range results {
		if result.CommandID == commandID {
			return true
		}
	}
	return false
}

func (r *broadcastRuntime) publishLocked(commit contracts.PolicyCommitV2) {
	state := r.app.State()
	if state.EmergencyHide {
		r.hideLocked("emergency_hide")
		return
	}
	if commit.Publication == contracts.PublicationHide {
		r.hideLocked("policy_hidden")
		return
	}
	if state.ActivePrimary == nil {
		if r.overlay.Visibility != "hidden" {
			r.hideLocked("no_active_decision")
		}
		return
	}
	publicationMS := r.now().UTC().UnixMilli()
	if state.ActivePrimary.Decision.PolicyTimeMS > publicationMS {
		publicationMS = state.ActivePrimary.Decision.PolicyTimeMS
	}
	deadlineMS := state.ActivePrimary.Candidate.Evidence[len(state.ActivePrimary.Candidate.Evidence)-1].ReceiveTime.Add(overlayFreshness).UnixMilli()
	visible, err := presentation.Build(presentation.BuildInput{
		Locale: "zh-CN", Candidate: state.ActivePrimary.Candidate, Decision: state.ActivePrimary.Decision,
		PublicationTimeMS: publicationMS, StaleDeadlineMS: deadlineMS,
	})
	if err != nil {
		r.hideLocked("presentation_invalid")
		return
	}
	r.overlay = visible
}

func (r *broadcastRuntime) hideLocked(code string) {
	nowMS := r.now().UTC().UnixMilli()
	if hidden, err := presentation.Hidden(r.app.State().SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), code); err == nil {
		r.overlay = hidden
	}
}

func (r *broadcastRuntime) operatorState(context.Context) (delivery.OperatorState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.app.State()
	candidates := append([]contracts.InsightCandidateV1(nil), state.Preview...)
	if state.ActivePrimary != nil {
		candidates = append(candidates, state.ActivePrimary.Candidate)
	}
	previews := make([]delivery.OperatorPreview, 0, len(candidates))
	for _, candidate := range candidates {
		claim, err := presentation.Preview("zh-CN", candidate)
		if err != nil {
			claim = presentation.UnavailablePreview("zh-CN")
		}
		previews = append(previews, delivery.OperatorPreview{
			CandidateID: candidate.CandidateID, RuleID: insight.Family(candidate.RuleVersion), RuleVersion: candidate.RuleVersion,
			Confidence: candidate.Confidence, SampleSize: candidate.SampleSize, ExpiresAtMS: candidate.ExpiryTimeMS,
			Pinned: policyPinned(state.Pins, candidate.CandidateID), Claim: claim,
		})
	}
	sort.Slice(previews, func(i, j int) bool { return previews[i].CandidateID < previews[j].CandidateID })
	return delivery.OperatorState{SchemaVersion: "operator_state.v1", SessionID: state.SessionID, PolicyRevision: state.PolicyRevision, EmergencyHidden: state.EmergencyHide, Previews: previews}, nil
}

func (r *broadcastRuntime) overlayState(context.Context) (contracts.OverlayStateV1, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nowMS := r.now().UTC().UnixMilli()
	if r.overlay.Visibility == "visible" && nowMS >= r.overlay.StaleDeadlineMS {
		hidden, err := presentation.Hidden(r.overlay.SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), "stale_input")
		if err != nil {
			return contracts.OverlayStateV1{}, err
		}
		r.overlay = hidden
	}
	return cloneOverlay(r.overlay)
}

func cloneOverlay(state contracts.OverlayStateV1) (contracts.OverlayStateV1, error) {
	payload, err := contracts.MarshalCanonical(state)
	if err != nil {
		return contracts.OverlayStateV1{}, err
	}
	var clone contracts.OverlayStateV1
	if err := contracts.DecodeStrict(payload, &clone); err != nil {
		return contracts.OverlayStateV1{}, err
	}
	return clone, nil
}

func policyPinned(pins []contracts.PolicyPinV2, candidateID string) bool {
	for _, pin := range pins {
		if pin.CandidateID == candidateID {
			return true
		}
	}
	return false
}
