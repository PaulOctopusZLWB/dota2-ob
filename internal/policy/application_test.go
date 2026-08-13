package policy_test

import (
	"errors"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
)

type recordingLog struct {
	commits []contracts.PolicyCommitV2
	err     error
}

func (l *recordingLog) AppendPolicyCommit(commit contracts.PolicyCommitV2) error {
	if l.err != nil {
		return l.err
	}
	l.commits = append(l.commits, commit)
	return nil
}

func TestApplicationAcceptsStateOnlyAfterCommit(t *testing.T) {
	engine := policy.New("session", policy.DefaultConfig())
	log := &recordingLog{}
	app := policy.NewApplication(engine, log)
	c := candidate("a", "draft.v1", "high", 1, 10)
	commit, err := app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	if err != nil || len(log.commits) != 1 || commit.Publication != contracts.PublicationSuppressedV2 || len(app.State().Preview) != 1 {
		t.Fatalf("commit was not accepted: %v", err)
	}
	log.err = errors.New("disk sync failed")
	prior := app.StateHash()
	failed, err := app.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), nil, 2)
	if !errors.Is(err, policy.ErrCommitFailedHidden) || failed.Publication != contracts.PublicationHide || app.StateHash() != prior {
		t.Fatal("persistence failure did not hide without accepting state")
	}
}
