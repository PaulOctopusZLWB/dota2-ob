package policy_test

import (
	"errors"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
)

type recordingLogV3 struct {
	commits []contracts.PolicyCommitV3
	err     error
	lookup  map[string]contracts.PolicyCommitV3
}

func (l *recordingLogV3) AppendPolicyCommit(c contracts.PolicyCommitV3) error {
	if l.err != nil {
		return l.err
	}
	l.commits = append(l.commits, c)
	return nil
}
func (l *recordingLogV3) LookupPolicyCommand(id string) (contracts.PolicyCommitV3, bool, error) {
	c, ok := l.lookup[id]
	return c, ok, nil
}

func TestApplicationV3PublishesOnlyAfterDurableAppendAndRollsBackHidden(t *testing.T) {
	config := policy.DefaultConfig()
	log := &recordingLogV3{}
	app := policy.NewApplicationV3(policy.New("session", config), log)
	c := candidate("v3-a", "draft.v1", "high", 1, 10)
	commit, err := app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	if err != nil || len(log.commits) != 1 || commit.SchemaVersion != contracts.PolicyCommitSchemaV3 || app.State().LastObservationSequence != 1 {
		t.Fatalf("V3 durable acceptance: %#v %v", commit, err)
	}
	prior := app.StateHash()
	log.err = errors.New("sync failed")
	failed, err := app.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), nil, 2)
	if !errors.Is(err, policy.ErrCommitFailedHidden) || failed.Publication != contracts.PublicationHide || app.StateHash() != prior || len(log.commits) != 1 {
		t.Fatal("tentative V3 state escaped append failure")
	}
}

func TestApplicationV3ReturnsDurableDuplicateWithoutAppending(t *testing.T) {
	config := policy.DefaultConfig()
	log := &recordingLogV3{lookup: map[string]contracts.PolicyCommitV3{}}
	app := policy.NewApplicationV3(policy.New("session", config), log)
	c := candidate("v3-command-target", "draft.v1", "high", 1, 10)
	_, _ = app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	cmd := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "v3-command", SessionID: "session", Action: contracts.ActionApprove, TargetCandidateID: c.CandidateID, ExpectedPolicyRevision: app.State().PolicyRevision, PolicyTimeMS: 2}
	original, err := app.ApplyCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	log.lookup[cmd.CommandID] = original
	state := app.State()
	cp := contracts.PolicyCheckpointV3{SchemaVersion: contracts.PolicyCheckpointSchemaV3, LineageManifestID: config.LineageID, LineageManifestSHA256: config.LineageID, SessionID: "session", CommitSequence: original.CommitSequence, ReferencedCommitSHA256: hash('f'), LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: app.StateHash(), CreatedTimeMS: 2, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CommandLocators: []contracts.PolicyCommandLocatorV2{{CommandID: cmd.CommandID, SegmentID: "segment.pcl3", FrameOffset: 0, CommitSequence: original.CommitSequence, FrameSHA256: hash('f')}}, CandidateTombstones: state.CandidateTombstones}
	restored, err := policy.NewFromCheckpointV3(cp, config)
	if err != nil {
		t.Fatal(err)
	}
	restarted := policy.NewApplicationV3(restored, log)
	before := len(log.commits)
	duplicate, err := restarted.ApplyCommand(cmd)
	if err != nil || len(log.commits) != before {
		t.Fatalf("durable duplicate appended: %v", err)
	}
	a, _ := contracts.MarshalCanonical(original)
	b, _ := contracts.MarshalCanonical(duplicate)
	if string(a) != string(b) {
		t.Fatal("durable V3 duplicate changed")
	}
}

func TestApplicationV3PersistsOutOfOrderInputsWithoutSemanticAdvance(t *testing.T) {
	log := &recordingLogV3{}
	app := policy.NewApplicationV3(policy.New("session", policy.DefaultConfig()), log)
	if _, err := app.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), nil, 10); err != nil {
		t.Fatal(err)
	}
	prior := app.StateHash()
	late, err := app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), nil, 9)
	if err != nil {
		t.Fatal(err)
	}
	if late.Publication != contracts.PublicationSuppressedV2 || len(late.AuditEvents) != 1 || late.AuditEvents[0].Reason != "out_of_order_observation" || app.StateHash() != prior || len(log.commits) != 2 {
		t.Fatalf("late observation was not durably suppressed: %#v", late)
	}
	command := contracts.OperatorCommandV1{
		SchemaVersion:          contracts.OperatorCommandSchemaV1,
		CommandID:              "late-policy-time",
		SessionID:              "session",
		Action:                 contracts.ActionDisableRule,
		TargetRuleID:           "draft.v1",
		ExpectedPolicyRevision: app.State().PolicyRevision,
		PolicyTimeMS:           9,
	}
	priorRevision := app.State().PolicyRevision
	priorObservation := app.State().LastObservationSequence
	rejected, err := app.ApplyCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.CommandResult == nil || rejected.CommandResult.Status != contracts.CommandRejected || rejected.Publication != contracts.PublicationSuppressedV2 || app.State().PolicyRevision != priorRevision || app.State().LastObservationSequence != priorObservation || len(log.commits) != 3 {
		t.Fatalf("late command was not durably rejected: %#v", rejected)
	}
}

func TestApplicationV3PersistsObjectiveNonEventAsUnchanged(t *testing.T) {
	log := &recordingLogV3{}
	app := policy.NewApplicationV3(policy.New("session", policy.DefaultConfig()), log)
	c := candidate("v3-non-event", "objective.v1", "observed", 1, 10)
	c.Availability, c.Reason, c.CandidateID = "suppressed", "objective_non_event", ""
	c.CandidateID, _ = contracts.InsightCandidateContentID(c)
	commit, err := app.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	if err != nil || commit.Publication != contracts.PublicationUnchanged || len(log.commits) != 1 {
		t.Fatalf("V3 unchanged terminal: %#v %v", commit, err)
	}
	replay := policy.New("session", policy.DefaultConfig())
	if err := replay.ReplayCommitV3(commit, []contracts.InsightCandidateV1{c}); err != nil {
		t.Fatalf("V3 unchanged replay: %v", err)
	}
}
