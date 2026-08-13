package policy

import (
	"errors"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

var ErrCommitFailedHidden = errors.New("policy_commit_failed_hidden")

// CommitAppender is the narrow synchronized persistence port. Production
// commit-log adapters return only after the complete V2 frame has been synced.
type CommitAppender interface {
	AppendPolicyCommit(contracts.PolicyCommitV2) error
}

// Application never exposes tentative policy state. A failed append retains
// the prior engine and requests a deterministic hide publication.
type Application struct {
	engine *Engine
	log    CommitAppender
}

func NewApplication(engine *Engine, log CommitAppender) *Application {
	return &Application{engine: engine, log: log}
}
func (a *Application) State() contracts.PolicyStateV2 { return a.engine.State() }
func (a *Application) StateHash() string              { return a.engine.StateHash() }

func (a *Application) EvaluateObservation(sequence uint64, rawHash, liveHash string, evidence contracts.EvidenceRefV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) (contracts.PolicyCommitV2, error) {
	next := a.engine.clone()
	commit := next.EvaluateObservation(sequence, rawHash, liveHash, evidence, candidates, policyTimeMS)
	return a.accept(next, commit)
}
func (a *Application) ApplyCommand(command contracts.OperatorCommandV1) (contracts.PolicyCommitV2, error) {
	if existing, ok := a.engine.commandCommits[command.CommandID]; ok {
		return existing, nil
	}
	if _, err := contracts.AdmitOperatorCommandV2(a.engine.state.SessionID, command, a.engine.state.CommandResults); err != nil {
		return contracts.PolicyCommitV2{}, err
	}
	next := a.engine.clone()
	commit := next.ApplyCommand(command)
	return a.accept(next, commit)
}
func (a *Application) accept(next *Engine, commit contracts.PolicyCommitV2) (contracts.PolicyCommitV2, error) {
	if err := commit.Validate(); err != nil {
		return failClosed(a.engine, commit, err)
	}
	if a.log == nil {
		return failClosed(a.engine, commit, ErrCommitFailedHidden)
	}
	if err := a.log.AppendPolicyCommit(commit); err != nil {
		return failClosed(a.engine, commit, err)
	}
	a.engine = next
	return commit, nil
}
func failClosed(engine *Engine, commit contracts.PolicyCommitV2, cause error) (contracts.PolicyCommitV2, error) {
	commit.Publication = contracts.PublicationHide
	commit.ResultingPolicyRevision = engine.state.PolicyRevision
	commit.ResultingObservationSequence = engine.state.LastObservationSequence
	commit.ResultingPolicyTimeMS = engine.state.LastPolicyTimeMS
	commit.ResultingStateHash = engine.hash
	return commit, errors.Join(ErrCommitFailedHidden, cause)
}
func (e *Engine) clone() *Engine {
	copyEngine := *e
	copyEngine.state = cloneState(e.state)
	copyEngine.commandCommits = make(map[string]contracts.PolicyCommitV2, len(e.commandCommits))
	for key, value := range e.commandCommits {
		copyEngine.commandCommits[key] = value
	}
	return &copyEngine
}
