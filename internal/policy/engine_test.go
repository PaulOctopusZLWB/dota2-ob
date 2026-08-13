package policy_test

import (
	"bytes"
	"fmt"
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
	if candidates[0].Priority != 20 || candidates[1].RuleVersion != "rule.v1" || candidates[2].RuleVersion != "rule.v1" || candidates[3].RuleVersion != "rule.v2" || candidates[1].CandidateID >= candidates[2].CandidateID {
		t.Fatalf("unexpected candidate rank: %#v", candidates)
	}
}

func TestApprovalRequiredCommandsRevisionIdempotencyAndSafety(t *testing.T) {
	engine := policy.New("session", policy.DefaultConfig())
	c := candidate("a", "draft.v1", "high", 1, 10)
	commit := engine.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 10)
	if commit.Publication != contracts.PublicationSuppressedV2 || len(engine.State().Preview) != 1 || engine.State().ActivePrimary != nil {
		t.Fatal("startup did not remain approval_required and hidden")
	}
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "cmd-1", SessionID: "session", Action: contracts.ActionApprove, TargetCandidateID: c.CandidateID, ExpectedPolicyRevision: commit.ResultingPolicyRevision, PolicyTimeMS: 11}
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
	sealTestCandidate(&a)
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
	b := candidate("b", "draft.v1", "high", 2, 10)
	engine.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), []contracts.InsightCandidateV1{b}, 4)
	approve := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "approve", SessionID: "session", Action: contracts.ActionApprove, TargetCandidateID: b.CandidateID, ExpectedPolicyRevision: engine.State().PolicyRevision, PolicyTimeMS: 5}
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

func TestActiveAndPinnedPreviewExpiryIsComplete(t *testing.T) {
	e := policy.New("session", policy.DefaultConfig())
	c := candidate("a", "draft.v1", "high", 1, 10)
	c.ExpiryTimeMS = 5
	sealTestCandidate(&c)
	queued := e.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	show := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "show", SessionID: "session", Action: contracts.ActionShow, TargetCandidateID: c.CandidateID, ExpectedPolicyRevision: queued.ResultingPolicyRevision, PolicyTimeMS: 2}
	e.ApplyCommand(show)
	expired := e.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), nil, 10)
	if expired.Publication != contracts.PublicationHide || e.State().ActivePrimary != nil || len(expired.Decisions) != 1 || expired.Decisions[0].ResultingState != contracts.DecisionExpired {
		t.Fatalf("active expiry incomplete: %#v", expired)
	}
	assertCheckpointStateValid(t, e, expired)

	e = policy.New("session", policy.DefaultConfig())
	c = candidate("p", "draft.v1", "high", 1, 10)
	c.ExpiryTimeMS = 5
	sealTestCandidate(&c)
	queued = e.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	pin := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "pin", SessionID: "session", Action: contracts.ActionPin, TargetCandidateID: c.CandidateID, ExpectedPolicyRevision: queued.ResultingPolicyRevision, PolicyTimeMS: 2}
	e.ApplyCommand(pin)
	e.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), nil, 10)
	if len(e.State().Pins) != 0 {
		t.Fatal("expired preview retained pin")
	}
	assertCheckpointStateValid(t, e, e.EvaluateObservation(3, hash('c'), hash('d'), evidence(3), nil, 11))
}

func TestOutOfOrderObservationReplayPreservesCausalAuditTime(t *testing.T) {
	config := policy.DefaultConfig()
	live := policy.New("session", config)
	first := live.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), nil, 10)
	second := live.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), nil, 20)
	replay := policy.New("session", config)
	if err := replay.ReplayCommit(first, nil); err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayCommit(second, nil); err != nil {
		t.Fatal(err)
	}
	if replay.StateHash() != live.StateHash() {
		t.Fatal("out-of-order replay state diverged")
	}
}

func TestRepeatedSemanticCandidateRemainsSparse(t *testing.T) {
	e := policy.New("session", policy.DefaultConfig())
	a := candidate("a", "draft.v1", "high", 1, 10)
	b := a
	b.Evidence = []contracts.EvidenceRefV1{evidence(2)}
	b.CreatedTimeMS, b.ExpiryTimeMS = 2, 101
	sealTestCandidate(&b)
	e.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{a}, 1)
	e.EvaluateObservation(2, hash('c'), hash('d'), evidence(2), []contracts.InsightCandidateV1{b}, 2)
	if len(e.State().Preview) != 1 {
		t.Fatalf("repeated claim grew queue: %d", len(e.State().Preview))
	}
}

func TestCommandExpiresTargetBeforeLookupAndReplaysExactly(t *testing.T) {
	config := policy.DefaultConfig()
	live := policy.New("session", config)
	c := sealedCandidate("draft.v1", "high", 1, 10)
	c.ExpiryTimeMS = 5
	sealTestCandidate(&c)
	queued := live.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{c}, 1)
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "expired-show", SessionID: "session", Action: contracts.ActionShow, TargetCandidateID: c.CandidateID, ExpectedPolicyRevision: queued.ResultingPolicyRevision, PolicyTimeMS: 5}
	staleCommand := command
	staleCommand.CommandID = "stale-before-expiry"
	staleCommand.ExpectedPolicyRevision = 0
	stale := live.ApplyCommand(staleCommand)
	if stale.CommandResult.Reason != "stale_revision" || len(live.State().Preview) != 1 {
		t.Fatalf("stale command performed expiry: %#v", stale)
	}
	expired := live.ApplyCommand(command)
	if err := expired.Validate(); err != nil || live.State().ActivePrimary != nil || len(live.State().Preview) != 0 || expired.Publication == contracts.PublicationPublish || len(expired.Decisions) != 1 || expired.Decisions[0].ResultingState != contracts.DecisionExpired || expired.CommandResult.Reason != "candidate_expired" {
		t.Fatalf("expired target was not suppressed before show: %#v", expired)
	}
	replayed := policy.New("session", config)
	if err := replayed.ReplayCommit(queued, []contracts.InsightCandidateV1{c}); err != nil {
		t.Fatal(err)
	}
	if err := replayed.ReplayCommit(stale, nil); err != nil {
		t.Fatal(err)
	}
	if err := replayed.ReplayCommit(expired, nil); err != nil || replayed.StateHash() != live.StateHash() {
		t.Fatalf("expired command replay diverged: %v", err)
	}
}

func TestAdmissionRequiresCanonicalCandidateContentID(t *testing.T) {
	valid := sealedCandidate("draft.v1", "high", 1, 10)
	engine := policy.New("session", policy.DefaultConfig())
	engine.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{valid}, 1)
	if len(engine.State().Preview) != 1 {
		t.Fatal("valid content-derived candidate ID rejected")
	}
	for _, mutate := range []func(*contracts.InsightCandidateV1){
		func(c *contracts.InsightCandidateV1) { c.Priority++ },
		func(c *contracts.InsightCandidateV1) { c.Evidence[0].RawPayloadSHA256 = hash('9') },
		func(c *contracts.InsightCandidateV1) { c.CandidateID = "arbitrary" },
	} {
		engine = policy.New("session", policy.DefaultConfig())
		bad := valid
		bad.Evidence = append([]contracts.EvidenceRefV1(nil), valid.Evidence...)
		mutate(&bad)
		commit := engine.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{bad}, 1)
		if len(engine.State().Preview) != 0 || commit.AuditEvents[0].Reason != "candidate_identity_mismatch" {
			t.Fatalf("tampered candidate admitted: %#v", commit)
		}
	}
}

func TestRuntimeConfidenceOrderProducesValidCheckpoint(t *testing.T) {
	engine := policy.New("session", policy.DefaultConfig())
	high := sealedCandidate("draft.v1", "high", 1, 10)
	medium := sealedCandidate("lane.v1", "medium", 1, 10)
	commit := engine.EvaluateObservation(1, hash('c'), hash('d'), evidence(1), []contracts.InsightCandidateV1{medium, high}, 1)
	if got := engine.State().Preview; len(got) != 2 || got[0].Confidence != "high" || got[1].Confidence != "medium" {
		t.Fatalf("runtime confidence order wrong: %#v", got)
	}
	assertCheckpointStateValid(t, engine, commit)
}

func TestDisableRuleCapacityRejects257thWithoutInvalidState(t *testing.T) {
	engine := policy.New("session", policy.DefaultConfig())
	commits := make([]contracts.PolicyCommitV2, 0, contracts.MaxDisabledRules+1)
	for i := 0; i <= contracts.MaxDisabledRules; i++ {
		command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: fmt.Sprintf("disable-%03d", i), SessionID: "session", Action: contracts.ActionDisableRule, TargetRuleID: fmt.Sprintf("rule-%03d", i), ExpectedPolicyRevision: engine.State().PolicyRevision, PolicyTimeMS: int64(i + 1)}
		commits = append(commits, engine.ApplyCommand(command))
	}
	last := commits[len(commits)-1]
	if last.CommandResult.Status != contracts.CommandRejected || last.CommandResult.Reason != "disabled_rule_capacity" || len(engine.State().DisabledRuleIDs) != contracts.MaxDisabledRules {
		t.Fatalf("257th disable was not bounded: %#v", last.CommandResult)
	}
	assertCheckpointStateValid(t, engine, last)
	replay := policy.New("session", policy.DefaultConfig())
	for _, commit := range commits {
		if err := replay.ReplayCommit(commit, nil); err != nil {
			t.Fatal(err)
		}
	}
	if replay.StateHash() != engine.StateHash() {
		t.Fatal("disable capacity replay diverged")
	}
}

func assertCheckpointStateValid(t *testing.T, e *policy.Engine, commit contracts.PolicyCommitV2) {
	t.Helper()
	s := e.State()
	locators := make([]contracts.PolicyCommandLocatorV2, len(s.CommandResults))
	for i, result := range s.CommandResults {
		locators[i] = contracts.PolicyCommandLocatorV2{CommandID: result.CommandID, SegmentID: "segment-1", FrameOffset: int64(i), CommitSequence: 1, FrameSHA256: hash('f')}
	}
	cp := contracts.PolicyCheckpointV2{SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: policy.DefaultConfig().LineageID, LineageManifestSHA256: policy.DefaultConfig().LineageID, SessionID: "session", CommitSequence: commit.CommitSequence, ReferencedCommitSHA256: hash('f'), LastObservationSequence: s.LastObservationSequence, PolicyRevision: s.PolicyRevision, LastPolicyTimeMS: s.LastPolicyTimeMS, CreatedTimeMS: s.LastPolicyTimeMS, Preview: s.Preview, DisabledRuleIDs: s.DisabledRuleIDs, Cooldowns: s.Cooldowns, Pins: s.Pins, EmergencyHide: s.EmergencyHide, ActivePrimary: s.ActivePrimary, CommandResults: s.CommandResults, CommandLocators: locators, CandidateTombstones: s.CandidateTombstones}
	cp.StateHash, _ = cp.ComputeStateHash()
	if err := cp.Validate(); err != nil {
		t.Fatalf("invalid checkpoint state: %v", err)
	}
}

func candidate(id, rule, confidence string, evidenceMS int64, priority int) contracts.InsightCandidateV1 {
	c := contracts.InsightCandidateV1{SchemaVersion: contracts.InsightCandidateSchemaV1, SessionID: "session", RuleVersion: rule, ConfigVersion: "config.v1", LocalizationKey: "insight." + rule + "." + id, Evidence: []contracts.EvidenceRefV1{evidence(uint64(evidenceMS))}, Confidence: confidence, Priority: priority, CreatedTimeMS: 1, ExpiryTimeMS: 100, Availability: "available"}
	sealTestCandidate(&c)
	return c
}
func sealedCandidate(rule, confidence string, evidenceMS int64, priority int) contracts.InsightCandidateV1 {
	c := candidate("", rule, confidence, evidenceMS, priority)
	sealTestCandidate(&c)
	return c
}
func sealTestCandidate(c *contracts.InsightCandidateV1) {
	c.CandidateID, _ = contracts.InsightCandidateContentID(*c)
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
