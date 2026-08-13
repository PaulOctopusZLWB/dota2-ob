package policy_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
)

func TestCandidateOrderUsesRuleVersionThenCandidateID(t *testing.T) {
	candidates := []contracts.InsightCandidateV1{
		candidate("z", "rule.v2", "high", 5, 10), candidate("a", "rule.v1", "high", 5, 10),
		candidate("b", "rule.v1", "high", 5, 10), candidate("top", "rule.v9", "medium", 9, 20),
	}
	policy.SortCandidates(candidates)
	want := []string{"top", "a", "b", "z"}
	for i := range want {
		if candidates[i].CandidateID != want[i] {
			t.Fatalf("rank %d=%s", i, candidates[i].CandidateID)
		}
	}
}

func TestApprovalRequiredCommandsRevisionIdempotencyAndSafety(t *testing.T) {
	engine := policy.New("session", policy.DefaultConfig())
	commit := engine.EvaluateObservation(1, hash('c'), hash('d'), contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "session", Sequence: 1, ReceiveTime: mustTime(), Source: "gsi", RawPayloadSHA256: hash('e')}, []contracts.InsightCandidateV1{candidate("a", "draft.v1", "high", 1, 10)}, 10)
	if commit.Publication != contracts.PublicationSuppressedV2 || len(engine.State().Preview) != 1 || engine.State().ActivePrimary != nil {
		t.Fatal("startup did not remain approval_required and hidden")
	}
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "cmd-1", SessionID: "session", Action: contracts.ActionApprove, TargetCandidateID: "a", ExpectedPolicyRevision: commit.ResultingPolicyRevision, PolicyTimeMS: 11}
	approved := engine.ApplyCommand(command)
	if approved.CommandResult.Status != contracts.CommandAccepted || approved.Publication != contracts.PublicationPublish || engine.State().ActivePrimary == nil {
		t.Fatal("approve did not select primary")
	}
	before := engine.StateHash()
	duplicate := engine.ApplyCommand(command)
	wantResult, _ := contracts.MarshalCanonical(*approved.CommandResult)
	gotResult, _ := contracts.MarshalCanonical(*duplicate.CommandResult)
	if duplicate.CommandResult == nil || !bytes.Equal(gotResult, wantResult) || engine.StateHash() != before {
		t.Fatal("duplicate command did not return exact original result")
	}
	hide := command
	hide.CommandID = "cmd-2"
	hide.Action = contracts.ActionEmergencyHide
	hide.TargetCandidateID = ""
	hide.ExpectedPolicyRevision = approved.ResultingPolicyRevision
	hide.PolicyTimeMS = 12
	if got := engine.ApplyCommand(hide); got.Publication != contracts.PublicationHide || !engine.State().EmergencyHide {
		t.Fatal("emergency hide did not fail display closed")
	}
}

func TestQueueExpiryDuplicateDisabledAndOutOfOrder(t *testing.T) {
	engine := policy.New("session", policy.Config{QueueLimit: 1, TombstoneMS: 100, CooldownMS: 50})
	a := candidate("a", "draft.v1", "high", 1, 100)
	a.ExpiryTimeMS = 5
	engine.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{a, candidate("b", "lane.v1", "medium", 2, 5)}, 1)
	if len(engine.State().Preview) != 1 {
		t.Fatal("queue bound not enforced")
	}
	engine.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), []contracts.InsightCandidateV1{candidate("a", "draft.v1", "high", 1, 20)}, 2)
	if len(engine.State().Preview) != 1 {
		t.Fatal("duplicate candidate changed queue")
	}
	engine.EvaluateObservation(3, hash('c'), hash('d'), evidence(3), nil, 10)
	if len(engine.State().Preview) != 0 || len(engine.State().CandidateTombstones) == 0 {
		t.Fatal("expiry did not tombstone candidate")
	}
	prior := engine.StateHash()
	commit := engine.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), nil, 9)
	if commit.ResultingStateHash != prior || commit.AuditEvents[0].Reason != "out_of_order_observation" {
		t.Fatal("out-of-order observation mutated state")
	}
}

func TestDisableEnablePinStaleRevisionAndCooldown(t *testing.T) {
	engine := policy.New("session", policy.Config{QueueLimit: 4, TombstoneMS: 100, CooldownMS: 50})
	queued := engine.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{candidate("a", "draft.v1", "high", 1, 10)}, 1)
	disable := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "disable", SessionID: "session", Action: contracts.ActionDisableRule, TargetRuleID: "draft.v1", ExpectedPolicyRevision: queued.ResultingPolicyRevision, PolicyTimeMS: 2}
	if got := engine.ApplyCommand(disable); got.CommandResult.Status != contracts.CommandAccepted || len(engine.State().Preview) != 0 {
		t.Fatal("disable rule did not remove queued rule")
	}
	stale := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "stale", SessionID: "session", Action: contracts.ActionEnableRule, TargetRuleID: "draft.v1", ExpectedPolicyRevision: 0, PolicyTimeMS: 3}
	if got := engine.ApplyCommand(stale); got.CommandResult.Status != contracts.CommandRejected || got.CommandResult.Reason != "stale_revision" {
		t.Fatal("stale revision accepted")
	}
	enable := stale
	enable.CommandID = "enable"
	enable.ExpectedPolicyRevision = engine.State().PolicyRevision
	if got := engine.ApplyCommand(enable); got.CommandResult.Status != contracts.CommandAccepted {
		t.Fatal("enable rule rejected")
	}
	engine.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), []contracts.InsightCandidateV1{candidate("b", "draft.v1", "high", 2, 10)}, 4)
	approve := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "approve", SessionID: "session", Action: contracts.ActionApprove, TargetCandidateID: "b", ExpectedPolicyRevision: engine.State().PolicyRevision, PolicyTimeMS: 5}
	engine.ApplyCommand(approve)
	engine.EvaluateObservation(3, hash('c'), hash('d'), evidence(3), []contracts.InsightCandidateV1{candidate("c", "draft.v1", "high", 3, 10)}, 6)
	if len(engine.State().Preview) != 0 {
		t.Fatalf("rule cooldown admitted candidate: %#v", engine.State())
	}
}

func TestRestoreCheckpointContinuesIdentically(t *testing.T) {
	config := policy.DefaultConfig()
	uninterrupted := policy.New("session", config)
	first := uninterrupted.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{candidate("a", "draft.v1", "high", 1, 10)}, 1)
	state := uninterrupted.State()
	cp := contracts.PolicyCheckpointV2{SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: config.LineageID, LineageManifestSHA256: config.LineageID, SessionID: "session", CommitSequence: first.CommitSequence, ReferencedCommitSHA256: hash('f'), LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: uninterrupted.StateHash(), CreatedTimeMS: 1, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CandidateTombstones: state.CandidateTombstones}
	recovered, err := policy.NewFromCheckpoint(cp, config)
	if err != nil {
		t.Fatal(err)
	}
	nextA := uninterrupted.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), []contracts.InsightCandidateV1{candidate("b", "lane.v1", "high", 2, 11)}, 2)
	nextB := recovered.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), []contracts.InsightCandidateV1{candidate("b", "lane.v1", "high", 2, 11)}, 2)
	a, _ := contracts.MarshalCanonical(nextA)
	b, _ := contracts.MarshalCanonical(nextB)
	if !bytes.Equal(a, b) || uninterrupted.StateHash() != recovered.StateHash() {
		t.Fatal("checkpoint continuation diverged")
	}
}

func candidate(id, rule, confidence string, evidenceMS int64, priority int) contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{SchemaVersion: contracts.InsightCandidateSchemaV1, CandidateID: id, SessionID: "session", RuleVersion: rule, ConfigVersion: "config.v1", LocalizationKey: "insight." + rule, Evidence: []contracts.EvidenceRefV1{evidence(uint64(evidenceMS))}, Confidence: confidence, Priority: priority, CreatedTimeMS: 1, ExpiryTimeMS: 100, Availability: "available"}
}
func evidence(seq uint64) contracts.EvidenceRefV1 {
	return contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "session", Sequence: seq, ReceiveTime: mustTime(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: hash('e')}
}
func mustTime() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
func hash(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
