package policy_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
)

type recordingLog struct {
	commits []contracts.PolicyCommitV2
	err     error
	lookup  map[string]contracts.PolicyCommitV2
}

func (l *recordingLog) LookupPolicyCommand(id string) (contracts.PolicyCommitV2, bool, error) {
	commit, ok := l.lookup[id]
	return commit, ok, nil
}

func (l *recordingLog) AppendPolicyCommit(commit contracts.PolicyCommitV2) error {
	if l.err != nil {
		return l.err
	}
	l.commits = append(l.commits, commit)
	return nil
}

func TestApplicationReturnsCommittedDuplicateAfterRestart(t *testing.T) {
	config := policy.DefaultConfig()
	log := &recordingLog{lookup: map[string]contracts.PolicyCommitV2{}}
	app := policy.NewApplication(policy.New("session", config), log)
	_, _ = app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{candidate("a", "draft.v1", "high", 1, 10)}, 1)
	cmd := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "approve", SessionID: "session", Action: contracts.ActionApprove, TargetCandidateID: "a", ExpectedPolicyRevision: app.State().PolicyRevision, PolicyTimeMS: 2}
	original, err := app.ApplyCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	log.lookup[cmd.CommandID] = original
	state := app.State()
	cp := contracts.PolicyCheckpointV2{SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: config.LineageID, LineageManifestSHA256: config.LineageID, SessionID: "session", CommitSequence: original.CommitSequence, ReferencedCommitSHA256: hash('f'), LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: app.StateHash(), CreatedTimeMS: 2, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CommandLocators: []contracts.PolicyCommandLocatorV2{{CommandID: "approve", SegmentID: "segment-1", FrameOffset: 1, CommitSequence: original.CommitSequence, FrameSHA256: hash('f')}}, CandidateTombstones: state.CandidateTombstones}
	restored, err := policy.NewFromCheckpoint(cp, config)
	if err != nil {
		t.Fatal(err)
	}
	restarted := policy.NewApplication(restored, log)
	duplicate, err := restarted.ApplyCommand(cmd)
	a, _ := contracts.MarshalCanonical(original)
	b, _ := contracts.MarshalCanonical(duplicate)
	if err != nil || !bytes.Equal(a, b) || restarted.StateHash() != app.StateHash() {
		t.Fatalf("durable duplicate diverged: %v", err)
	}
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

func TestApplicationPersistsCompleteNormalTransitionSequence(t *testing.T) {
	log := &recordingLog{}
	app := policy.NewApplication(policy.New("session", policy.DefaultConfig()), log)
	queued, err := app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{candidate("a", "draft.v1", "high", 1, 20), candidate("b", "lane.v1", "high", 1, 10)}, 1)
	if err != nil || len(queued.Decisions) != 2 {
		t.Fatalf("queue transitions incomplete: decisions=%d err=%v", len(queued.Decisions), err)
	}
	command := func(id, action, target string, at int64) contracts.OperatorCommandV1 {
		return contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: id, SessionID: "session", Action: action, TargetCandidateID: target, ExpectedPolicyRevision: app.State().PolicyRevision, PolicyTimeMS: at}
	}
	if _, err = app.ApplyCommand(command("show-a", contracts.ActionShow, "a", 2)); err != nil {
		t.Fatal(err)
	}
	superseded, err := app.ApplyCommand(command("show-b", contracts.ActionShow, "b", 3))
	if err != nil || len(superseded.Decisions) != 2 || superseded.Decisions[0].ResultingState != contracts.DecisionSuperseded {
		t.Fatalf("primary supersede incomplete: %#v err=%v", superseded.Decisions, err)
	}
	hide := command("hide", contracts.ActionEmergencyHide, "", 4)
	hidden, err := app.ApplyCommand(hide)
	if err != nil || len(hidden.Decisions) != 1 || hidden.Decisions[0].ResultingState != contracts.DecisionEmergencyHidden {
		t.Fatalf("hide transition invalid: %#v %v", hidden.Decisions, err)
	}
	clear := command("clear", contracts.ActionClearEmergencyHide, "", 5)
	cleared, err := app.ApplyCommand(clear)
	if err != nil || len(cleared.Decisions) != 1 || cleared.Publication != contracts.PublicationPublish {
		t.Fatalf("clear transition invalid: %#v %v", cleared, err)
	}
	for _, commit := range log.commits {
		if err := commit.Validate(); err != nil {
			t.Fatalf("persisted invalid commit: %v", err)
		}
	}
}

func TestApplicationRejectsMismatchedObservationAndArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*contracts.InsightCandidateV1)
	}{
		{"observation", func(c *contracts.InsightCandidateV1) { c.Evidence[0] = evidence(99) }},
		{"rule", func(c *contracts.InsightCandidateV1) { c.RuleVersion = "draft.v999" }},
		{"config", func(c *contracts.InsightCandidateV1) { c.ConfigVersion = "config.v999" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &recordingLog{}
			app := policy.NewApplication(policy.New("session", policy.DefaultConfig()), log)
			c := candidate("bad", "draft.v1", "high", 1, 10)
			tc.mutate(&c)
			commit, err := app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
			if err != nil || len(app.State().Preview) != 0 || len(commit.AuditEvents) == 0 || commit.AuditEvents[0].EventType != "candidate_suppressed" {
				t.Fatalf("bad candidate admitted: %#v %v", commit, err)
			}
		})
	}
}
