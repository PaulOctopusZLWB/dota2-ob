package contracts_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func TestPolicyCommitV2RequiresExactlyOneCompleteCausalInput(t *testing.T) {
	command := validCommandV2("command-1")
	result := validCommandResultV2(command, contracts.CommandAccepted, 0, 1)
	commit := validPolicyCommitV2(command, result)
	if err := commit.Validate(); err != nil {
		t.Fatalf("valid command commit: %v", err)
	}

	missing := commit
	missing.CommandID = ""
	missing.Command = nil
	missing.CommandResult = nil
	if err := missing.Validate(); err == nil {
		t.Fatal("commit without causal input accepted")
	}

	mixed := commit
	mixed.ObservationSequence = 1
	mixed.ObservationEvidence = &contracts.EvidenceRefV1{
		RecordSchemaVersion: 2, SessionID: "session", Sequence: 1,
		ReceiveTime: testTime(), Source: "gsi", ProviderVersion: contracts.Absent[int64](),
		RawPayloadSHA256: sha("4"),
	}
	mixed.RawRecordSHA256 = sha("5")
	mixed.LiveObservationSHA256 = sha("6")
	if err := mixed.Validate(); err == nil {
		t.Fatal("mixed observation/command causal input accepted")
	}

	wrongTarget := commit
	copyCommand := *commit.Command
	copyCommand.CommandID = "other-command"
	wrongTarget.Command = &copyCommand
	if err := wrongTarget.Validate(); err == nil {
		t.Fatal("incoherent embedded command accepted")
	}

	missingAuditCause := commit
	missingAuditCause.AuditEvents = append([]contracts.AuditEventV1(nil), commit.AuditEvents...)
	missingAuditCause.AuditEvents[0].CommandID = ""
	missingAuditCause.AuditEvents[0].CandidateID = "candidate-1"
	if err := missingAuditCause.Validate(); err == nil {
		t.Fatal("command audit without matching causal command ID accepted")
	}
}

func TestPolicyLineageManifestV2IsContentAddressedAndBounded(t *testing.T) {
	manifest := validLineageManifestV2()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	id, err := manifest.ContentID()
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || id != manifest.MustContentID() {
		t.Fatalf("unstable manifest ID %q", id)
	}
	changed := manifest
	changed.Config.ContentSHA256 = sha("9")
	if changed.MustContentID() == id {
		t.Fatal("artifact substitution preserved manifest identity")
	}

	over := manifest
	over.EngineBuild.Version = strings.Repeat("x", contracts.MaxPolicyLineageManifestBytes)
	if err := over.Validate(); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("oversize manifest error = %v", err)
	}
}

func TestPolicyCheckpointV2StateHashExcludesLocatorsAndAnchor(t *testing.T) {
	cp := validCheckpointV2()
	hash, err := cp.ComputeStateHash()
	if err != nil {
		t.Fatal(err)
	}
	cp.StateHash = hash
	if err := cp.Validate(); err != nil {
		t.Fatalf("valid checkpoint: %v", err)
	}

	locatorChanged := cp
	locatorChanged.CommandLocators = append([]contracts.PolicyCommandLocatorV2(nil), cp.CommandLocators...)
	locatorChanged.CommandLocators[0].FrameOffset++
	locatorHash, err := locatorChanged.ComputeStateHash()
	if err != nil {
		t.Fatal(err)
	}
	if locatorHash != hash {
		t.Fatal("application locator entered policy_state.v2")
	}

	anchorChanged := cp
	anchorChanged.ReferencedCommitSHA256 = sha("8")
	anchorHash, err := anchorChanged.ComputeStateHash()
	if err != nil {
		t.Fatal(err)
	}
	if anchorHash != hash {
		t.Fatal("checkpoint anchor entered policy_state.v2")
	}

	semanticChanged := cp
	semanticChanged.CommandResults = append([]contracts.PolicyCommandResultRefV2(nil), cp.CommandResults...)
	semanticChanged.CommandResults[0].ResultSHA256 = sha("7")
	semanticHash, err := semanticChanged.ComputeStateHash()
	if err != nil {
		t.Fatal(err)
	}
	if semanticHash == hash {
		t.Fatal("semantic idempotency result omitted from policy_state.v2")
	}
}

func TestPolicyCheckpointV2RejectsLocatorAndBoundViolations(t *testing.T) {
	cp := validCheckpointV2()
	cp.StateHash, _ = cp.ComputeStateHash()

	missing := cp
	missing.CommandLocators = nil
	if err := missing.Validate(); err == nil {
		t.Fatal("missing locator accepted")
	}

	duplicate := cp
	duplicate.CommandLocators = append(duplicate.CommandLocators, duplicate.CommandLocators[0])
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate locator accepted")
	}

	overID := cp
	overID.DisabledRuleIDs = []string{strings.Repeat("r", contracts.MaxPolicyIdentifierBytes+1)}
	overID.StateHash, _ = overID.ComputeStateHash()
	if err := overID.Validate(); err == nil {
		t.Fatal("over-limit policy identifier accepted")
	}

	tooMany := cp
	tooMany.CommandResults = make([]contracts.PolicyCommandResultRefV2, contracts.MaxCheckpointCommandResults+1)
	if err := tooMany.Validate(); err == nil {
		t.Fatal("over-limit semantic idempotency index accepted")
	}
}

func TestPolicyCheckpointV2ValidatesAnchorAgainstCommit(t *testing.T) {
	command := validCommandV2("command-1")
	result := validCommandResultV2(command, contracts.CommandAccepted, 0, 1)
	commit := validPolicyCommitV2(command, result)
	cp := validCheckpointV2()
	cp.StateHash, _ = cp.ComputeStateHash()
	commit.ResultingStateHash = cp.StateHash
	commitHash, err := contracts.CanonicalSHA256(commit)
	if err != nil {
		t.Fatal(err)
	}
	cp.ReferencedCommitSHA256 = commitHash
	if err := cp.ValidateAgainstCommit(commit, commitHash); err != nil {
		t.Fatalf("valid anchor: %v", err)
	}

	bad := cp
	bad.LastPolicyTimeMS++
	bad.StateHash, _ = bad.ComputeStateHash()
	if err := bad.ValidateAgainstCommit(commit, commitHash); err == nil {
		t.Fatal("policy-time anchor mismatch accepted")
	}
}

func TestAdmitOperatorCommandV2LooksUpDuplicateBeforeCapacityAndActionValidation(t *testing.T) {
	index := make([]contracts.PolicyCommandResultRefV2, contracts.MaxCheckpointCommandResults)
	for i := range index {
		index[i] = contracts.PolicyCommandResultRefV2{CommandID: formatCommandID(i), ResultSHA256: sha("1")}
	}
	duplicate := validCommandV2(index[0].CommandID)
	duplicate.Action = "unknown_action"
	duplicate.TargetRuleID = ""
	admission, err := contracts.AdmitOperatorCommandV2("session", duplicate, index)
	if err != nil || !admission.Duplicate || admission.ResultSHA256 != sha("1") {
		t.Fatalf("duplicate admission=%#v err=%v", admission, err)
	}

	unknown := validCommandV2("unknown-command")
	if _, err := contracts.AdmitOperatorCommandV2("session", unknown, index); !errors.Is(err, contracts.ErrSessionCommandLimit) {
		t.Fatalf("unknown over-limit error = %v", err)
	}

	overID := validCommandV2(strings.Repeat("x", contracts.MaxPolicyIdentifierBytes+1))
	if _, err := contracts.AdmitOperatorCommandV2("session", overID, nil); !errors.Is(err, contracts.ErrPolicyIdentifierLimit) {
		t.Fatalf("over-limit identifier error = %v", err)
	}
}

func TestPolicyTombstoneMaintenanceIsDeterministicAndBounded(t *testing.T) {
	tombstones := []contracts.PolicyCandidateTombstoneV2{
		{CandidateID: "c-old", RuleID: "r", TerminalDecisionID: "d-old", TerminalState: contracts.DecisionExpired, SuppressUntilPolicyTimeMS: 10},
		{CandidateID: "c-live", RuleID: "r", TerminalDecisionID: "d-live", TerminalState: contracts.DecisionRejected, SuppressUntilPolicyTimeMS: 20},
	}
	remaining := contracts.ExpirePolicyTombstonesV2(tombstones, 10)
	if len(remaining) != 1 || remaining[0].CandidateID != "c-live" {
		t.Fatalf("remaining tombstones = %#v", remaining)
	}
	full := make([]contracts.PolicyCandidateTombstoneV2, contracts.MaxCandidateTombstones)
	for i := range full {
		full[i] = contracts.PolicyCandidateTombstoneV2{CandidateID: formatCommandID(i), RuleID: "r", TerminalDecisionID: "d-" + formatCommandID(i), TerminalState: contracts.DecisionRejected, SuppressUntilPolicyTimeMS: int64(i + 1)}
	}
	if _, reason := contracts.InsertPolicyTombstoneV2(full, contracts.PolicyCandidateTombstoneV2{CandidateID: "new", RuleID: "r", TerminalDecisionID: "d-new", TerminalState: contracts.DecisionRejected, SuppressUntilPolicyTimeMS: 9999}); reason != "candidate_index_capacity" {
		t.Fatalf("capacity reason = %q", reason)
	}
}

func TestPolicyCheckpointV2RoundTripsCompleteContinuationState(t *testing.T) {
	preview := validCandidateV2("candidate-preview", 100)
	active := validCandidateV2("candidate-active", 90)
	decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: "decision-active", SessionID: "session", PolicyRevision: 1, CandidateID: active.CandidateID, PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionShown, PolicyTimeMS: 10, Reason: "approved"}
	cp := validCheckpointV2()
	cp.Preview = []contracts.InsightCandidateV1{preview}
	cp.Cooldowns = []contracts.RuleCooldownV2{{RuleID: "rule-1", UntilPolicyTimeMS: 20}}
	cp.Pins = []contracts.PolicyPinV2{{CandidateID: preview.CandidateID, DecisionID: "decision-pin"}}
	cp.EmergencyHide = true
	cp.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: active, Decision: decision}
	cp.CandidateTombstones = []contracts.PolicyCandidateTombstoneV2{{CandidateID: "candidate-old", RuleID: "rule-1", TerminalDecisionID: "decision-old", TerminalState: contracts.DecisionExpired, SuppressUntilPolicyTimeMS: 30}}
	cp.StateHash, _ = cp.ComputeStateHash()
	if err := cp.Validate(); err != nil {
		t.Fatalf("complete checkpoint: %v", err)
	}
	first, err := contracts.MarshalCanonical(cp)
	if err != nil {
		t.Fatal(err)
	}
	var recovered contracts.PolicyCheckpointV2
	if err := contracts.DecodeStrict(first, &recovered); err != nil {
		t.Fatal(err)
	}
	second, err := contracts.MarshalCanonical(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || recovered.ActivePrimary == nil || !recovered.EmergencyHide || len(recovered.Preview) != 1 || len(recovered.Pins) != 1 || len(recovered.CandidateTombstones) != 1 {
		t.Fatal("complete policy continuation state was not byte-equivalent")
	}
}

func TestRejectedAdmittedCommandChangesOnlySemanticIndexHash(t *testing.T) {
	cp := validCheckpointV2()
	cp.PolicyRevision = 7
	cp.LastPolicyTimeMS = 100
	cp.CommandResults = nil
	cp.CommandLocators = nil
	before, err := cp.ComputeStateHash()
	if err != nil {
		t.Fatal(err)
	}
	cp.CommandResults = []contracts.PolicyCommandResultRefV2{{CommandID: "rejected-command", ResultSHA256: sha("9")}}
	cp.CommandLocators = []contracts.PolicyCommandLocatorV2{{CommandID: "rejected-command", SegmentID: "00000000000000000001.pcl2", FrameOffset: 0, CommitSequence: 1, FrameSHA256: sha("8")}}
	after, err := cp.ComputeStateHash()
	if err != nil {
		t.Fatal(err)
	}
	if before == after || cp.PolicyRevision != 7 || cp.LastPolicyTimeMS != 100 {
		t.Fatalf("before=%s after=%s revision=%d time=%d", before, after, cp.PolicyRevision, cp.LastPolicyTimeMS)
	}
}

func validPolicyCommitV2(command contracts.OperatorCommandV1, result contracts.OperatorCommandResultV1) contracts.PolicyCommitV2 {
	return contracts.PolicyCommitV2{
		SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: sha("1"),
		LineageManifestSHA256: sha("1"), SessionID: "session", CommitSequence: 1,
		CommandID: command.CommandID, Command: &command, PriorPolicyRevision: 0,
		ResultingPolicyRevision: result.ResultingRevision, ResultingStateHash: sha("2"),
		ResultingObservationSequence: 0, ResultingPolicyTimeMS: command.PolicyTimeMS,
		Decisions: []contracts.BroadcastDecisionV1{}, CommandResult: &result,
		AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "audit-1", SessionID: "session", EventType: "command", PolicyTimeMS: command.PolicyTimeMS, CommandID: command.CommandID, Reason: result.Reason}},
		Publication: contracts.PublicationUnchanged,
	}
}

func validCommandV2(id string) contracts.OperatorCommandV1 {
	return contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: id, SessionID: "session", Action: contracts.ActionDisableRule, TargetRuleID: "rule-1", ExpectedPolicyRevision: 0, PolicyTimeMS: 10}
}

func validCommandResultV2(command contracts.OperatorCommandV1, status string, previous, resulting uint64) contracts.OperatorCommandResultV1 {
	return contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: command.CommandID, SessionID: command.SessionID, Status: status, PreviousRevision: previous, ResultingRevision: resulting, DecisionIDs: []string{}, Reason: "accepted"}
}

func validLineageManifestV2() contracts.PolicyLineageManifestV2 {
	artifact := func(version, digit string) contracts.PolicyArtifactIdentityV2 {
		return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: sha(digit)}
	}
	return contracts.PolicyLineageManifestV2{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: "session",
		RawRecordSchema: artifact("raw_record.v2", "1"), RawRecordFraming: artifact("jsonl.v1", "2"),
		RawPayloadSchema: artifact("dota2_gsi.v1", "3"), LiveObservationSchema: artifact(contracts.LiveObservationSchemaV1, "4"),
		ProjectionMapping: artifact("gsi_mapping.v1", "5"), TournamentScopeID: sha("6"), TournamentScopeSHA256: sha("6"),
		HistoricalSnapshotID: sha("7"), HistoricalSnapshotSHA256: sha("7"), EligibleBaselineSHA256: []string{sha("8")},
		Rules: artifact("rules.v1", "a"), Config: artifact("config.v1", "b"), Catalog: artifact("catalog.v1", "c"),
		Terminology: artifact("terminology.v1", "d"), LocalizationParameterMapping: artifact("localization_params.v1", "e"),
		EngineBuild: artifact("engine.v1", "f"),
	}
}

func validCheckpointV2() contracts.PolicyCheckpointV2 {
	resultRef := contracts.PolicyCommandResultRefV2{CommandID: "command-1", ResultSHA256: sha("3")}
	locator := contracts.PolicyCommandLocatorV2{CommandID: "command-1", SegmentID: "00000000000000000001.pcl2", FrameOffset: 0, CommitSequence: 1, FrameSHA256: sha("4")}
	return contracts.PolicyCheckpointV2{
		SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: sha("1"), LineageManifestSHA256: sha("1"),
		SessionID: "session", CommitSequence: 1, ReferencedCommitSHA256: sha("5"), LastObservationSequence: 0,
		PolicyRevision: 1, LastPolicyTimeMS: 10, StateHash: sha("2"), CreatedTimeMS: 10,
		Preview: []contracts.InsightCandidateV1{}, DisabledRuleIDs: []string{"rule-1"}, Cooldowns: []contracts.RuleCooldownV2{},
		Pins: []contracts.PolicyPinV2{}, CommandResults: []contracts.PolicyCommandResultRefV2{resultRef},
		CommandLocators: []contracts.PolicyCommandLocatorV2{locator}, CandidateTombstones: []contracts.PolicyCandidateTombstoneV2{},
	}
}

func validCandidateV2(id string, priority int) contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{
		SchemaVersion: contracts.InsightCandidateSchemaV1, CandidateID: id, SessionID: "session",
		RuleVersion: "rule.v1", ConfigVersion: "config.v1", LocalizationKey: "insight.test",
		Parameters: []contracts.TypedParameterV1{}, Evidence: []contracts.EvidenceRefV1{}, ObservedValues: []contracts.ObservedMetricV1{},
		Confidence: "observed", Priority: priority, CreatedTimeMS: 1, ExpiryTimeMS: 100,
		SourceRequirements: []contracts.SourceRequirementV1{}, Availability: "available",
	}
}

func sha(digit string) string { return strings.Repeat(digit, 64) }

func testTime() time.Time { return time.Unix(1, 0).UTC() }

func formatCommandID(i int) string { return fmt.Sprintf("command-%04d", i) }
