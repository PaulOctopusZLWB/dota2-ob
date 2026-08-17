package policy

import (
	"errors"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// CommitAppenderV3 is the explicit synchronized persistence port for a
// live-only lineage. It cannot be satisfied by a V2 store.
type CommitAppenderV3 interface {
	AppendPolicyCommit(contracts.PolicyCommitV3) error
}

type CommandCommitLookupV3 interface {
	LookupPolicyCommand(string) (contracts.PolicyCommitV3, bool, error)
}

// ApplicationV3 exposes state only after the one V3 terminal commit for a
// causal input has synchronously reached durable storage.
type ApplicationV3 struct {
	engine *Engine
	log    CommitAppenderV3
}

func NewApplicationV3(engine *Engine, log CommitAppenderV3) *ApplicationV3 {
	return &ApplicationV3{engine: engine, log: log}
}

func NewBoundApplicationV3(engine *Engine, log CommitAppenderV3, binding contracts.HistoryAvailabilityBindingV1, lineage contracts.PolicyLineageManifestV3) (*ApplicationV3, error) {
	bindingID, bindingErr := binding.ContentID()
	rules, rulesErr := contracts.RuleVersionsArtifact(lineage.Rules.Version, engine.config.AllowedRuleVersions)
	if bindingErr != nil || binding.Mode != contracts.HistoryModeNoGo || lineage.Validate() != nil || rulesErr != nil ||
		lineage.HistoryAvailabilityBindingID != bindingID || lineage.HistoryAvailabilityBindingSHA256 != bindingID ||
		lineage.MustContentID() != engine.config.LineageID || lineage.Config != engine.config.CandidateConfigArtifact ||
		lineage.Rules != engine.config.CandidateRulesArtifact || engine.config.CandidateConfigVersion != lineage.Config.Version || rules != lineage.Rules || log == nil {
		return nil, ErrCommitFailedHidden
	}
	application := NewApplicationV3(engine, log)
	publishRuntimeStatus(engine.state.SessionID, engine.config.QueueLimit, true)
	return application, nil
}

func (a *ApplicationV3) State() contracts.PolicyStateV2 { return a.engine.State() }
func (a *ApplicationV3) StateHash() string              { return a.engine.StateHash() }

func (a *ApplicationV3) EvaluateObservation(sequence uint64, rawHash, liveHash string, evidence contracts.EvidenceRefV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) (contracts.PolicyCommitV3, error) {
	next := a.engine.clone()
	commit := next.EvaluateObservationV3(sequence, rawHash, liveHash, evidence, candidates, policyTimeMS)
	return a.accept(next, commit)
}

func (a *ApplicationV3) ApplyCommand(command contracts.OperatorCommandV1) (contracts.PolicyCommitV3, error) {
	if existing, ok := a.engine.commandCommits[command.CommandID]; ok {
		v3 := contracts.PolicyCommitV3(existing)
		v3.SchemaVersion = contracts.PolicyCommitSchemaV3
		return v3, nil
	}
	admission, err := contracts.AdmitOperatorCommandV2(a.engine.state.SessionID, command, a.engine.state.CommandResults)
	if err != nil {
		return contracts.PolicyCommitV3{}, err
	}
	if admission.Duplicate {
		lookup, ok := a.log.(CommandCommitLookupV3)
		if !ok {
			return contracts.PolicyCommitV3{}, ErrCommitFailedHidden
		}
		commit, found, lookupErr := lookup.LookupPolicyCommand(command.CommandID)
		if lookupErr != nil || !found || commit.CommandResult == nil {
			if lookupErr == nil {
				lookupErr = ErrCommitFailedHidden
			}
			return contracts.PolicyCommitV3{}, errors.Join(ErrCommitFailedHidden, lookupErr)
		}
		resultHash, hashErr := contracts.CanonicalSHA256(*commit.CommandResult)
		if hashErr != nil || resultHash != admission.ResultSHA256 {
			return contracts.PolicyCommitV3{}, ErrCommitFailedHidden
		}
		return commit, nil
	}
	next := a.engine.clone()
	commit := next.ApplyCommandV3(command)
	return a.accept(next, commit)
}

func (a *ApplicationV3) accept(next *Engine, commit contracts.PolicyCommitV3) (contracts.PolicyCommitV3, error) {
	if err := commit.Validate(); err != nil {
		return failClosedV3(a.engine, commit, err)
	}
	if a.log == nil {
		return failClosedV3(a.engine, commit, ErrCommitFailedHidden)
	}
	if err := a.log.AppendPolicyCommit(commit); err != nil {
		return failClosedV3(a.engine, commit, err)
	}
	a.engine = next
	return commit, nil
}

func failClosedV3(engine *Engine, commit contracts.PolicyCommitV3, cause error) (contracts.PolicyCommitV3, error) {
	commit.Publication = contracts.PublicationHide
	commit.ResultingPolicyRevision = engine.state.PolicyRevision
	commit.ResultingObservationSequence = engine.state.LastObservationSequence
	commit.ResultingPolicyTimeMS = engine.state.LastPolicyTimeMS
	commit.ResultingStateHash = engine.hash
	return commit, errors.Join(ErrCommitFailedHidden, cause)
}
