// Package policyapp composes the pure policy engine with durable V2 recovery.
package policyapp

import (
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

type CandidateResolver func(contracts.PolicyCommitV2) ([]contracts.InsightCandidateV1, error)

// Recover streams checkpoint continuation (or the full log when the cache is
// missing/corrupt) through exact pure-engine re-evaluation.
func Recover(store *commitlog.StoreV2, sessionID string, config policy.Config, resolve CandidateResolver) (*policy.Application, error) {
	checkpoint, err := store.LoadCheckpoint(func(commitlog.CommittedV2) error { return nil })
	if err != nil {
		return nil, err
	}
	var engine *policy.Engine
	if checkpoint != nil {
		engine, err = policy.NewFromCheckpoint(*checkpoint, config)
	} else {
		engine = policy.New(sessionID, config)
	}
	if err != nil {
		return nil, err
	}
	replay := func(committed commitlog.CommittedV2) error {
		var candidates []contracts.InsightCandidateV1
		if committed.Commit.ObservationEvidence != nil {
			if resolve == nil {
				return commitlog.ErrInvalidCommit
			}
			var err error
			candidates, err = resolve(committed.Commit)
			if err != nil {
				return err
			}
		}
		return engine.ReplayCommit(committed.Commit, candidates)
	}
	if checkpoint != nil {
		_, err = store.LoadCheckpoint(replay)
	} else {
		err = store.VisitAll(replay)
	}
	if err != nil {
		return nil, err
	}
	return policy.NewApplication(engine, store), nil
}
