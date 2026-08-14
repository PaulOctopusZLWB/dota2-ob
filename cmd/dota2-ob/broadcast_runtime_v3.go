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
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type broadcastConfigV3 struct {
	DataRoot                  string
	SessionID                 string
	RawPath                   string
	Artifacts                 liveOnlyPolicyArtifacts
	Now                       func() time.Time
	StoreOptions              []commitlog.V3Option
	ProjectionRestoreRequired bool
}

// broadcastRuntimeV3 is intentionally separate from broadcastRuntime. This
// prevents a live-only session from manufacturing or accepting a V2 commit.
type broadcastRuntimeV3 struct {
	mu                   sync.Mutex
	app                  *policy.ApplicationV3
	store                *commitlog.StoreV3
	now                  func() time.Time
	artifacts            liveOnlyPolicyArtifacts
	previous             *contracts.LiveObservationV1
	overlay              contracts.OverlayStateV1
	restoringProjection  bool
	projectionRejected   bool
	projectionHealthCode string
}

func newBroadcastRuntimeV3(config broadcastConfigV3) (*broadcastRuntimeV3, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Artifacts.Release.ValidateAgainst(config.Artifacts.History, config.Artifacts.Lineage) != nil ||
		config.Artifacts.Release.SourceCommit != acceptedLiveOnlyScopeCommit ||
		!matchesProductLineageV3(config.Artifacts.Lineage, config.Artifacts.History, config.SessionID) {
		return nil, errors.New("live-only broadcast lineage configuration mismatch")
	}
	policyConfig := policy.DefaultConfig()
	policyConfig.LineageID = config.Artifacts.Lineage.MustContentID()
	policyConfig.CandidateConfigVersion = config.Artifacts.Lineage.Config.Version
	policyConfig.CandidateConfigArtifact = config.Artifacts.Lineage.Config
	policyConfig.CandidateRulesArtifact = config.Artifacts.Lineage.Rules
	resolver := newLiveOnlyObservationResolver(config.RawPath, config.SessionID, config.Artifacts)
	storeOptions := append([]commitlog.V3Option(nil), config.StoreOptions...)
	storeOptions = append(storeOptions, commitlog.WithV3ReplayVerifier(newProductionReplayVerifierV3(resolver, config.SessionID, policyConfig)))
	store, _, err := commitlog.OpenV3(config.DataRoot, config.SessionID, config.Artifacts.History, config.Artifacts.Lineage, storeOptions...)
	if err != nil {
		_ = resolver.Close()
		return nil, err
	}
	resolver.previous = nil
	app, err := recoverProductionApplicationV3(store, config.SessionID, config.Artifacts, policyConfig, resolver)
	previous := resolver.previous
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
	healthCode := "waiting_for_policy_decision"
	if config.ProjectionRestoreRequired {
		healthCode = "projection_restoring"
	}
	hidden, err := presentation.Hidden(config.SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), healthCode)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &broadcastRuntimeV3{
		app: app, store: store, now: config.Now, artifacts: config.Artifacts,
		previous: previous, overlay: hidden, restoringProjection: config.ProjectionRestoreRequired,
	}, nil
}

func (r *broadcastRuntimeV3) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *broadcastRuntimeV3) applyObservation(ctx context.Context, observation contracts.LiveObservationV1) error {
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
	candidates := insight.EvaluateLiveOnly(insight.LiveOnlyInput{
		Observation: observation, Previous: r.previous, History: r.artifacts.History,
		Lineage: r.artifacts.Lineage, PolicyTimeMS: policyTimeMS,
	}, insight.DefaultConfig())
	return r.commitCandidatesLocked(observation, candidates, policyTimeMS)
}

func (r *broadcastRuntimeV3) commitCandidates(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commitCandidatesLocked(observation, candidates, policyTimeMS)
}

func (r *broadcastRuntimeV3) commitCandidatesLocked(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) error {
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

func (r *broadcastRuntimeV3) execute(ctx context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OperatorCommandResultV1{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.restoringProjection {
		return contracts.OperatorCommandResultV1{}, errors.New("projection restore in progress")
	}
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
	if !duplicate {
		r.publishLocked(commit)
	}
	return *commit.CommandResult, nil
}

func (r *broadcastRuntimeV3) publishLocked(commit contracts.PolicyCommitV3) {
	if r.restoringProjection {
		r.hideLocked("projection_restoring")
		return
	}
	if r.projectionRejected {
		r.hideLocked(r.projectionHealthCode)
		return
	}
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

func (r *broadcastRuntimeV3) republishLocked() {
	if r.restoringProjection {
		r.hideLocked("projection_restoring")
		return
	}
	if r.projectionRejected {
		r.hideLocked(r.projectionHealthCode)
		return
	}
	state := r.app.State()
	if state.EmergencyHide {
		r.hideLocked("emergency_hide")
		return
	}
	if state.ActivePrimary == nil {
		r.hideLocked("no_active_decision")
		return
	}
	r.publishLocked(contracts.PolicyCommitV3{Publication: contracts.PublicationPublish})
}

func (r *broadcastRuntimeV3) BeginRestore(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restoringProjection = true
	r.hideLocked("projection_restoring")
	return nil
}

func (r *broadcastRuntimeV3) CompleteRestore(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restoringProjection = false
	r.republishLocked()
	return nil
}

func (r *broadcastRuntimeV3) ProjectionHealth(ctx context.Context, transition session.RejectionTransition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if transition.Active {
		if transition.Code != "gsi_projection_non_object" && transition.Code != "gsi_projection_bounds_exceeded" {
			return errors.New("invalid projection rejection transition")
		}
		r.projectionRejected = true
		r.projectionHealthCode = transition.Code
		r.hideLocked(transition.Code)
		return nil
	}
	r.projectionRejected = false
	r.projectionHealthCode = ""
	r.republishLocked()
	return nil
}

func (r *broadcastRuntimeV3) hideLocked(code string) {
	nowMS := r.now().UTC().UnixMilli()
	if hidden, err := presentation.Hidden(r.app.State().SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), code); err == nil {
		r.overlay = hidden
	}
}

func (r *broadcastRuntimeV3) operatorState(context.Context) (delivery.OperatorState, error) {
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

func (r *broadcastRuntimeV3) overlayState(context.Context) (contracts.OverlayStateV1, error) {
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
