package main

import (
	"errors"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policyapp"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type liveOnlyObservationResolver struct {
	raw          *observationResolver
	history      contracts.HistoryAvailabilityBindingV1
	lineage      contracts.PolicyLineageManifestV3
	availability insight.LiveOnlyAvailabilityV1
	previous     *contracts.LiveObservationV1
	evaluate     func(insight.LiveOnlyInputV2, insight.Config) insight.LiveOnlyEvaluationV2
}

func newLiveOnlyObservationResolver(rawPath, sessionID string, artifacts liveOnlyPolicyArtifacts, mapObservation func(*session.Record) (contracts.LiveObservationV1, error), evaluate func(insight.LiveOnlyInputV2, insight.Config) insight.LiveOnlyEvaluationV2) *liveOnlyObservationResolver {
	raw := newObservationResolver(rawPath, sessionID, contracts.PolicyLineageManifestV2{})
	if mapObservation != nil {
		raw.mapObservation = mapObservation
	}
	if evaluate == nil {
		evaluate = insight.EvaluateLiveOnlyV2
	}
	availability, _ := insight.ProductLiveOnlyAvailabilityV1(artifacts.History)
	return &liveOnlyObservationResolver{
		raw:     raw,
		history: artifacts.History, lineage: artifacts.Lineage, availability: availability, evaluate: evaluate,
	}
}

func (r *liveOnlyObservationResolver) Close() error { return r.raw.Close() }

func (r *liveOnlyObservationResolver) resolve(commit contracts.PolicyCommitV3) ([]contracts.InsightCandidateV1, error) {
	observation, rawRecordSHA256, err := r.raw.readCommittedObservationWithRecordSHA(commit.ObservationSequence)
	if err != nil {
		return nil, err
	}
	if commit.ObservationEvidence == nil || !canonicalEqual(observation.Evidence, *commit.ObservationEvidence) ||
		commit.RawRecordSHA256 != observation.Evidence.RawPayloadSHA256 {
		return nil, errors.New("committed live-only observation evidence mismatch")
	}
	liveHash, err := contracts.CanonicalSHA256(observation)
	if err != nil || liveHash != commit.LiveObservationSHA256 {
		return nil, errors.New("committed live-only observation hash mismatch")
	}
	policyTimeMS := commit.ResultingPolicyTimeMS
	if len(commit.AuditEvents) > 0 {
		policyTimeMS = commit.AuditEvents[0].PolicyTimeMS
	}
	input := insight.LiveOnlyInputV2{
		Observation: *observation, Previous: r.previous, History: r.history,
		Lineage: r.lineage, Availability: r.availability, RawRecordSHA256: rawRecordSHA256, PolicyTimeMS: policyTimeMS,
	}
	result := r.evaluate(input, insight.DefaultConfig())
	if err := insight.ValidateLiveOnlyEvaluationV2(result, input); err != nil {
		return nil, err
	}
	copyObservation := *observation
	r.previous = &copyObservation
	return result.Candidates, nil
}

func newProductionReplayVerifierV3(resolver *liveOnlyObservationResolver, sessionID string, config policy.Config) commitlog.ReplayVerifierV3 {
	engine := policy.New(sessionID, config)
	staged := make(map[uint64][]contracts.InsightCandidateV1)
	return commitlog.ReplayVerifierV3{
		VerifyObservation: func(commit contracts.PolicyCommitV3) error {
			candidates, err := resolver.resolve(commit)
			if err == nil {
				staged[commit.CommitSequence] = candidates
			}
			return err
		},
		VerifyCommand: func(commit contracts.PolicyCommitV3) error {
			if commit.Command == nil || commit.CommandResult == nil || commit.CommandID != commit.Command.CommandID || commit.CommandID != commit.CommandResult.CommandID {
				return errors.New("incomplete committed live-only command")
			}
			return nil
		},
		Reevaluate: func(commit contracts.PolicyCommitV3) error {
			candidates := staged[commit.CommitSequence]
			delete(staged, commit.CommitSequence)
			return engine.ReplayCommitV3(commit, candidates)
		},
	}
}

func recoverProductionApplicationV3(store *commitlog.StoreV3, sessionID string, artifacts liveOnlyPolicyArtifacts, config policy.Config, resolver *liveOnlyObservationResolver) (*policy.ApplicationV3, error) {
	return policyapp.RecoverV3(store, sessionID, artifacts.History, artifacts.Lineage, config, resolver.resolve)
}
