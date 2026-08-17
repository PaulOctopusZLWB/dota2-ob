package policyapp

import (
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

type CandidateResolverV3 func(contracts.PolicyCommitV3) ([]contracts.InsightCandidateV1, error)

// RecoverV3 restores only the exact V3 checkpoint/log lineage already opened
// and verified by StoreV3. It never accepts a V2 store or searches for history.
func RecoverV3(store *commitlog.StoreV3, sessionID string, binding contracts.HistoryAvailabilityBindingV1, lineage contracts.PolicyLineageManifestV3, config policy.Config, resolve CandidateResolverV3) (*policy.ApplicationV3, error) {
	checkpoint, err := store.LoadCheckpoint(func(commitlog.CommittedV3) error { return nil })
	if err != nil {
		return nil, err
	}
	var engine *policy.Engine
	if checkpoint != nil {
		engine, err = policy.NewFromCheckpointV3(*checkpoint, config)
	} else {
		engine = policy.New(sessionID, config)
	}
	if err != nil {
		return nil, err
	}
	replay := func(committed commitlog.CommittedV3) error {
		var candidates []contracts.InsightCandidateV1
		if committed.Commit.ObservationEvidence != nil {
			if resolve == nil {
				return commitlog.ErrInvalidCommit
			}
			candidates, err = resolve(committed.Commit)
			if err != nil {
				return err
			}
		}
		return engine.ReplayCommitV3(committed.Commit, candidates)
	}
	if checkpoint != nil {
		_, err = store.LoadCheckpoint(replay)
	} else {
		err = store.VisitAll(replay)
	}
	if err != nil {
		return nil, err
	}
	return policy.NewBoundApplicationV3(engine, store, binding, lineage)
}
