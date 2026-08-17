package product

import (
	"context"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	snapshotinsight "github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/insight"
)

// Config is the narrow product-entry configuration for the generated,
// immutable snapshot-V2 implementation.
type Config struct {
	DataRoot                  string
	SessionID                 string
	RawPath                   string
	Lineage                   contracts.PolicyLineageManifestV2
	Now                       func() time.Time
	StoreOptions              []commitlog.V2Option
	ProjectionRestoreRequired bool
}

// Runtime is an exported facade over the implementation mechanically produced
// from the identity-bearing snapshot-V2 sources.
type Runtime struct{ runtime *broadcastRuntime }

// SemanticDigests returns the generator-input identities compiled into this
// V2 implementation. A structural guard regenerates implementation and
// constants together and rejects any byte-level divergence.
func SemanticDigests() map[string]string {
	return map[string]string{
		"contracts.go":            contractsSourceSHA256,
		"live_mapping.go":         liveMappingSourceSHA256,
		"product_selector.go":     productSelectorSourceSHA256,
		"presentation_catalog.go": presentationCatalogSHA256,
		"product_main.go":         productMainSourceSHA256,
		"product_ports.go":        productPortsSourceSHA256,
		"product_recovery.go":     productRecoverySourceSHA256,
		"product_runtime.go":      productRuntimeSourceSHA256,
		"product_lineage.go":      productLineageSourceSHA256,
		"session_highwater.go":    sessionHighWaterSourceSHA256,
		"session_follower.go":     sessionFollowerSourceSHA256,
		"insight_engine.go":       insightEngineSourceSHA256,
		"policy_engine.go":        policyEngineSourceSHA256,
		"policy_application.go":   policyApplicationSourceSHA256,
	}
}

func RulesArtifact() contracts.PolicyArtifactIdentityV2 { return snapshotinsight.RulesArtifact() }

func ConfigArtifact() contracts.PolicyArtifactIdentityV2 {
	return snapshotinsight.ConfigArtifact(snapshotinsight.DefaultConfig())
}

func NewRuntime(config Config) (*Runtime, error) {
	runtime, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: config.DataRoot, SessionID: config.SessionID, RawPath: config.RawPath,
		Lineage: config.Lineage, Now: config.Now, StoreOptions: config.StoreOptions,
		ProjectionRestoreRequired: config.ProjectionRestoreRequired,
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{runtime: runtime}, nil
}

func LoadPolicyLineage(path, sessionID string) (contracts.PolicyLineageManifestV2, error) {
	return loadPolicyLineage(path, sessionID)
}

func AssembleProductLineage(lineage contracts.PolicyLineageManifestV2, sessionID string) contracts.PolicyLineageManifestV2 {
	return assembleProductLineage(lineage, sessionID)
}

func (r *Runtime) Close() error { return r.runtime.Close() }

func (r *Runtime) ApplyObservation(ctx context.Context, observation contracts.LiveObservationV1) error {
	return r.runtime.applyObservation(ctx, observation)
}

func (r *Runtime) CommitCandidates(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) error {
	return r.runtime.commitCandidates(observation, candidates, policyTimeMS)
}

func (r *Runtime) State() contracts.PolicyStateV2 { return r.runtime.app.State() }

func (r *Runtime) StateHash() string { return r.runtime.app.StateHash() }

func (r *Runtime) VisitAll(visit func(commitlog.CommittedV2) error) error {
	return r.runtime.store.VisitAll(visit)
}

func (r *Runtime) EvaluateObservation(observation contracts.LiveObservationV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) (contracts.PolicyCommitV2, error) {
	liveHash, err := contracts.CanonicalSHA256(observation)
	if err != nil {
		return contracts.PolicyCommitV2{}, err
	}
	return r.runtime.app.EvaluateObservation(observation.Evidence.Sequence, observation.Evidence.RawPayloadSHA256, liveHash, observation.Evidence, candidates, policyTimeMS)
}

func (r *Runtime) WriteCheckpoint(checkpoint contracts.PolicyCheckpointV2) error {
	return r.runtime.store.WriteCheckpoint(checkpoint)
}

func (r *Runtime) Execute(ctx context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	return r.runtime.execute(ctx, command)
}

func (r *Runtime) OperatorState(ctx context.Context) (delivery.OperatorState, error) {
	return r.runtime.operatorState(ctx)
}

func (r *Runtime) OverlayState(ctx context.Context) (contracts.OverlayStateV1, error) {
	return r.runtime.overlayState(ctx)
}

func (r *Runtime) BeginRestore(ctx context.Context) error { return r.runtime.BeginRestore(ctx) }

func (r *Runtime) CompleteRestore(ctx context.Context) error { return r.runtime.CompleteRestore(ctx) }

func (r *Runtime) ProjectionHealth(ctx context.Context, transition session.RejectionTransition) error {
	return r.runtime.ProjectionHealth(ctx, transition)
}
