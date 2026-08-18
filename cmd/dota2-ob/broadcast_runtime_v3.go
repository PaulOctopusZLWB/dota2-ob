package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
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
	PolicyNow                 func() time.Time
	DisplayNow                func() time.Time
	StoreOptions              []commitlog.V3Option
	ProjectionRestoreRequired bool
	PolicyConfig              *policy.Config
	BuildOverlay              func(presentation.BuildInput) (contracts.OverlayStateV1, error)
	EvaluateLiveOnlyV2        func(insight.LiveOnlyInputV2, insight.Config) insight.LiveOnlyEvaluationV2
	MapObservation            func(*session.Record) (contracts.LiveObservationV1, error)
}

// broadcastRuntimeV3 is intentionally separate from broadcastRuntime. This
// prevents a live-only session from manufacturing or accepting a V2 commit.
type broadcastRuntimeV3 struct {
	mu                   sync.Mutex
	app                  *policy.ApplicationV3
	store                *commitlog.StoreV3
	policyNow            func() time.Time
	displayNow           func() time.Time
	artifacts            liveOnlyPolicyArtifacts
	availability         insight.LiveOnlyAvailabilityV1
	previous             *contracts.LiveObservationV1
	overlay              contracts.OverlayStateV1
	restoringProjection  bool
	restoreReady         chan struct{}
	restoreReadyOnce     sync.Once
	observationReady     chan struct{}
	committedObservation atomic.Uint64
	projectionRejected   bool
	projectionHealthCode string
	candidateSaturated   bool
	buildOverlay         func(presentation.BuildInput) (contracts.OverlayStateV1, error)
	evaluateLiveOnlyV2   func(insight.LiveOnlyInputV2, insight.Config) insight.LiveOnlyEvaluationV2
	mapObservation       func(*session.Record) (contracts.LiveObservationV1, error)
}

func newBroadcastRuntimeV3(config broadcastConfigV3) (*broadcastRuntimeV3, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.PolicyNow == nil {
		config.PolicyNow = config.Now
	}
	if config.DisplayNow == nil {
		config.DisplayNow = config.Now
	}
	if config.BuildOverlay == nil {
		config.BuildOverlay = presentation.Build
	}
	if config.EvaluateLiveOnlyV2 == nil {
		config.EvaluateLiveOnlyV2 = insight.EvaluateLiveOnlyV2
	}
	if config.Artifacts.Release.ValidateAgainst(config.Artifacts.History, config.Artifacts.Lineage) != nil ||
		config.Artifacts.Release.SourceCommit != acceptedLiveOnlyScopeCommit ||
		!matchesProductLineageV3(config.Artifacts.Lineage, config.Artifacts.History, config.SessionID) {
		return nil, errors.New("live-only broadcast lineage configuration mismatch")
	}
	availability, err := insight.ProductLiveOnlyAvailabilityV1(config.Artifacts.History)
	if err != nil {
		return nil, errors.New("live-only product availability configuration mismatch")
	}
	policyConfig := policy.DefaultConfig()
	if config.PolicyConfig != nil {
		policyConfig = *config.PolicyConfig
	}
	policyConfig.LineageID = config.Artifacts.Lineage.MustContentID()
	policyConfig.CandidateConfigVersion = config.Artifacts.Lineage.Config.Version
	policyConfig.CandidateConfigArtifact = config.Artifacts.Lineage.Config
	policyConfig.CandidateRulesArtifact = config.Artifacts.Lineage.Rules
	resolver := newLiveOnlyObservationResolver(config.RawPath, config.SessionID, config.Artifacts, config.MapObservation, config.EvaluateLiveOnlyV2)
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
	saturated := false
	if err := store.VisitAll(func(committed commitlog.CommittedV3) error {
		if commitHasQueueFull(committed.Commit) {
			saturated = true
		}
		return nil
	}); err != nil {
		_ = store.Close()
		return nil, err
	}
	nowMS := config.DisplayNow().UTC().UnixMilli()
	healthCode := "waiting_for_policy_decision"
	if saturated {
		healthCode = "candidate_queue_saturated"
	} else if config.ProjectionRestoreRequired {
		healthCode = "projection_restoring"
	}
	hidden, err := presentation.Hidden(config.SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), healthCode)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	runtime := &broadcastRuntimeV3{
		app: app, store: store, policyNow: config.PolicyNow, displayNow: config.DisplayNow, artifacts: config.Artifacts, availability: availability,
		previous: previous, overlay: hidden, restoringProjection: config.ProjectionRestoreRequired, candidateSaturated: saturated,
		restoreReady:     make(chan struct{}),
		observationReady: make(chan struct{}, 1),
		buildOverlay:     config.BuildOverlay, evaluateLiveOnlyV2: config.EvaluateLiveOnlyV2, mapObservation: config.MapObservation,
	}
	if !config.ProjectionRestoreRequired {
		runtime.restoreReadyOnce.Do(func() { close(runtime.restoreReady) })
	}
	runtime.committedObservation.Store(app.State().LastObservationSequence)
	return runtime, nil
}

func (r *broadcastRuntimeV3) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *broadcastRuntimeV3) applyObservation(ctx context.Context, observation contracts.LiveObservationV1) error {
	return r.applyRecord(ctx, observation, observation.Evidence.RawPayloadSHA256)
}

func (r *broadcastRuntimeV3) applyRecord(ctx context.Context, observation contracts.LiveObservationV1, rawRecordSHA256 string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := observation.Validate(); err != nil {
		r.hideLocked("observation_contract_exceeded")
		return nil
	}
	if observation.Evidence.Sequence <= r.app.State().LastObservationSequence {
		return nil
	}
	policyTimeMS := r.policyNow().UTC().UnixMilli()
	input := insight.LiveOnlyInputV2{
		Observation: observation, Previous: r.previous, History: r.artifacts.History,
		Lineage: r.artifacts.Lineage, Availability: r.availability, RawRecordSHA256: rawRecordSHA256, PolicyTimeMS: policyTimeMS,
	}
	result := r.evaluateLiveOnlyV2(input, insight.DefaultConfig())
	if err := insight.ValidateLiveOnlyEvaluationV2(result, input); err != nil {
		r.hideLocked("live_only_family_evidence_invalid")
		return err
	}
	return r.commitCandidatesLocked(observation, result.Candidates, policyTimeMS)
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
	r.committedObservation.Store(observation.Evidence.Sequence)
	r.signalObservationReadyLocked()
	if commitHasQueueFull(commit) {
		r.candidateSaturated = true
		r.hideLocked("candidate_queue_saturated")
		return nil
	}
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
	if r.candidateSaturated {
		r.hideLocked("candidate_queue_saturated")
		return contracts.OperatorCommandResultV1{}, errors.New("candidate queue saturated")
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
	if r.candidateSaturated {
		r.hideLocked("candidate_queue_saturated")
		return
	}
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
	publicationMS := r.displayNow().UTC().UnixMilli()
	if state.ActivePrimary.Decision.PolicyTimeMS > publicationMS {
		publicationMS = state.ActivePrimary.Decision.PolicyTimeMS
	}
	deadlineMS := state.ActivePrimary.Candidate.Evidence[len(state.ActivePrimary.Candidate.Evidence)-1].ReceiveTime.Add(overlayFreshness).UnixMilli()
	visible, err := r.buildOverlay(presentation.BuildInput{
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
	if r.candidateSaturated {
		r.hideLocked("candidate_queue_saturated")
		return
	}
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

func commitHasQueueFull(commit contracts.PolicyCommitV3) bool {
	for _, event := range commit.AuditEvents {
		if event.EventType == "candidate_suppressed" && event.Reason == "queue_full" {
			return true
		}
	}
	return false
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
	r.restoreReadyOnce.Do(func() { close(r.restoreReady) })
	return nil
}

// RestoreReady closes exactly when the startup follower has rebuilt through
// its retained high-water mark and publication has reopened. It replaces
// observer-side mutex polling with a causal production readiness signal.
func (r *broadcastRuntimeV3) RestoreReady() <-chan struct{} { return r.restoreReady }

func (r *broadcastRuntimeV3) WaitRestore(ctx context.Context) error {
	select {
	case <-r.restoreReady:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("phase=restore: %w", ctx.Err())
	}
}

// WaitObservation blocks on a coalescing causal notification instead of
// contending with projection on the runtime mutex. The application state is
// authoritative, so a notification is only a wakeup and cannot skip progress.
func (r *broadcastRuntimeV3) WaitObservation(ctx context.Context, sequence uint64) error {
	for {
		observed := r.committedObservation.Load()
		if observed >= sequence {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("phase=observation sequence=%d committed=%d: %w", sequence, r.committedObservation.Load(), ctx.Err())
		case <-r.observationReady:
		}
	}
}

func (r *broadcastRuntimeV3) signalObservationReadyLocked() {
	select {
	case r.observationReady <- struct{}{}:
	default:
	}
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
	nowMS := r.displayNow().UTC().UnixMilli()
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
	nowMS := r.displayNow().UTC().UnixMilli()
	if r.overlay.Visibility == "visible" && nowMS >= r.overlay.StaleDeadlineMS {
		hidden, err := presentation.Hidden(r.overlay.SessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), "stale_input")
		if err != nil {
			return contracts.OverlayStateV1{}, err
		}
		r.overlay = hidden
	}
	return cloneOverlay(r.overlay)
}
