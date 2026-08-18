package main

import (
	"context"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	snapshotproduct "github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/product"
)

const overlayFreshness = 2 * time.Second

type broadcastConfig struct {
	DataRoot                  string
	SessionID                 string
	RawPath                   string
	Lineage                   contracts.PolicyLineageManifestV2
	Now                       func() time.Time
	StoreOptions              []commitlog.V2Option
	ProjectionRestoreRequired bool
}

// broadcastRuntime is the deliberately narrow product seam between the mixed
// V2/V3 command and the generated, immutable snapshot-V2 implementation. The
// wrapped implementation is mechanically generated from the identity-bearing
// snapshotv2 reference artifacts; no current insight, policy, presentation, or
// recovery implementation is reachable through this V2 facade.
type broadcastRuntime struct {
	selected *snapshotproduct.Runtime
}

func newBroadcastRuntime(config broadcastConfig) (*broadcastRuntime, error) {
	selected, err := snapshotproduct.NewRuntime(snapshotproduct.Config{
		DataRoot:                  config.DataRoot,
		SessionID:                 config.SessionID,
		RawPath:                   config.RawPath,
		Lineage:                   config.Lineage,
		Now:                       config.Now,
		StoreOptions:              config.StoreOptions,
		ProjectionRestoreRequired: config.ProjectionRestoreRequired,
	})
	if err != nil {
		return nil, err
	}
	return &broadcastRuntime{selected: selected}, nil
}

func (r *broadcastRuntime) Close() error {
	if r == nil || r.selected == nil {
		return nil
	}
	return r.selected.Close()
}

func (r *broadcastRuntime) commitCandidates(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) error {
	return r.selected.CommitCandidates(observation, candidates, policyTimeMS)
}

func (r *broadcastRuntime) state() contracts.PolicyStateV2 { return r.selected.State() }

func (r *broadcastRuntime) stateHash() string { return r.selected.StateHash() }

func (r *broadcastRuntime) visitAll(visit func(commitlog.CommittedV2) error) error {
	return r.selected.VisitAll(visit)
}

func (r *broadcastRuntime) evaluateObservation(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) (contracts.PolicyCommitV2, error) {
	return r.selected.EvaluateObservation(observation, candidates, policyTimeMS)
}

func (r *broadcastRuntime) writeCheckpoint(checkpoint contracts.PolicyCheckpointV2) error {
	return r.selected.WriteCheckpoint(checkpoint)
}

func (r *broadcastRuntime) applyObservation(ctx context.Context, observation contracts.LiveObservationV1) error {
	return r.selected.ApplyObservation(ctx, observation)
}

// V2 semantics remain unchanged; the V3-only raw-record identity is ignored by
// the immutable snapshot-backed runtime.
func (r *broadcastRuntime) applyRecord(ctx context.Context, observation contracts.LiveObservationV1, _ string) error {
	return r.applyObservation(ctx, observation)
}

func (r *broadcastRuntime) execute(ctx context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	return r.selected.Execute(ctx, command)
}

func (r *broadcastRuntime) operatorState(ctx context.Context) (delivery.OperatorState, error) {
	return r.selected.OperatorState(ctx)
}

func (r *broadcastRuntime) overlayState(ctx context.Context) (contracts.OverlayStateV1, error) {
	return r.selected.OverlayState(ctx)
}

func (r *broadcastRuntime) BeginRestore(ctx context.Context) error {
	return r.selected.BeginRestore(ctx)
}

func (r *broadcastRuntime) CompleteRestore(ctx context.Context) error {
	return r.selected.CompleteRestore(ctx)
}

func (r *broadcastRuntime) ProjectionHealth(ctx context.Context, transition session.RejectionTransition) error {
	return r.selected.ProjectionHealth(ctx, transition)
}
