package contracts_test

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type semanticStepV2 struct {
	name string
	kind string
	time int64
}

type semanticOutcomeV2 struct {
	Name        string                             `json:"name"`
	Result      *contracts.OperatorCommandResultV1 `json:"result,omitempty"`
	Decisions   []contracts.BroadcastDecisionV1    `json:"decisions"`
	Audits      []contracts.AuditEventV1           `json:"audits"`
	Publication string                             `json:"publication"`
	StateHash   string                             `json:"state_hash"`
}

func TestPolicyStateV2RecoveredAndUninterruptedSemanticMatrixIsByteEquivalent(t *testing.T) {
	steps := []semanticStepV2{
		{name: "01-accepted-disable", kind: "accepted_disable", time: 10},
		{name: "02-queue-change", kind: "queue", time: 11},
		{name: "03-pin", kind: "pin", time: 12},
		{name: "04-active-primary", kind: "show", time: 13},
		{name: "05-emergency-hide", kind: "emergency_hide", time: 14},
		{name: "06-clear-emergency", kind: "clear_emergency", time: 15},
		{name: "07-unpin", kind: "unpin", time: 16},
		{name: "08-expiry", kind: "expiry", time: 20},
		{name: "09-rejected-enable-stale", kind: "rejected_enable_stale", time: 21},
		{name: "10-rejected-disable-out-of-order", kind: "rejected_disable_out_of_order", time: 19},
		{name: "11-accepted-enable", kind: "accepted_enable", time: 22},
		{name: "12-tombstone-expiry", kind: "tombstone_expiry", time: 40},
	}
	const recoveryAfter = 5

	uninterrupted := emptySemanticCheckpointV2()
	uninterruptedOutcomes := runSemanticStepsV2(t, &uninterrupted, steps)

	recovered := emptySemanticCheckpointV2()
	_ = runSemanticStepsV2(t, &recovered, steps[:recoveryAfter])
	recovered.StateHash, _ = recovered.ComputeStateHash()
	checkpointBytes, err := contracts.MarshalCanonical(recovered)
	if err != nil {
		t.Fatal(err)
	}
	var restarted contracts.PolicyCheckpointV2
	if err := contracts.DecodeStrict(checkpointBytes, &restarted); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Validate(); err != nil {
		t.Fatalf("restart checkpoint is not complete: %v", err)
	}
	recoveredOutcomes := runSemanticStepsV2(t, &restarted, steps[recoveryAfter:])

	for i := range recoveredOutcomes {
		want := uninterruptedOutcomes[recoveryAfter+i]
		if !bytes.Equal(recoveredOutcomes[i], want) {
			t.Fatalf("%s outcome differs after recovery\nwant %s\n got %s", steps[recoveryAfter+i].name, want, recoveredOutcomes[i])
		}
	}
	wantState, err := contracts.MarshalCanonical(uninterrupted.StateProjection())
	if err != nil {
		t.Fatal(err)
	}
	gotState, err := contracts.MarshalCanonical(restarted.StateProjection())
	if err != nil {
		t.Fatal(err)
	}
	wantHash, _ := uninterrupted.ComputeStateHash()
	gotHash, _ := restarted.ComputeStateHash()
	if !bytes.Equal(gotState, wantState) || gotHash != wantHash {
		t.Fatalf("final recovered semantic state differs\nwant_hash=%s\n got_hash=%s\nwant=%s\n got=%s", wantHash, gotHash, wantState, gotState)
	}
	if restarted.PolicyRevision != 7 || restarted.LastPolicyTimeMS != 40 || len(restarted.CommandResults) != 9 || len(restarted.CandidateTombstones) != 0 || restarted.EmergencyHide || restarted.ActivePrimary != nil || len(restarted.Pins) != 0 || len(restarted.DisabledRuleIDs) != 0 {
		t.Fatalf("matrix missed required terminal semantics: %#v", restarted.StateProjection())
	}
}

func emptySemanticCheckpointV2() contracts.PolicyCheckpointV2 {
	return contracts.PolicyCheckpointV2{
		SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: sha("1"), LineageManifestSHA256: sha("1"),
		SessionID: "session", CommitSequence: 1, ReferencedCommitSHA256: sha("2"), PolicyRevision: 0,
		Preview: []contracts.InsightCandidateV1{}, DisabledRuleIDs: []string{}, Cooldowns: []contracts.RuleCooldownV2{}, Pins: []contracts.PolicyPinV2{},
		CommandResults: []contracts.PolicyCommandResultRefV2{}, CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: []contracts.PolicyCandidateTombstoneV2{},
	}
}

func runSemanticStepsV2(t *testing.T, checkpoint *contracts.PolicyCheckpointV2, steps []semanticStepV2) [][]byte {
	t.Helper()
	outcomes := make([][]byte, 0, len(steps))
	for _, step := range steps {
		outcome := applySemanticStepV2(t, checkpoint, step)
		data, err := contracts.MarshalCanonical(outcome)
		if err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, data)
	}
	return outcomes
}

func applySemanticStepV2(t *testing.T, checkpoint *contracts.PolicyCheckpointV2, step semanticStepV2) semanticOutcomeV2 {
	t.Helper()
	outcome := semanticOutcomeV2{Name: step.name, Decisions: []contracts.BroadcastDecisionV1{}, Audits: []contracts.AuditEventV1{}, Publication: contracts.PublicationUnchanged}
	candidate := matrixCandidateV2()

	switch step.kind {
	case "queue":
		checkpoint.LastObservationSequence++
		checkpoint.LastPolicyTimeMS = step.time
		checkpoint.Preview = []contracts.InsightCandidateV1{candidate}
		decision := matrixDecisionV2("decision-queue", candidate.CandidateID, "", checkpoint.PolicyRevision, contracts.DecisionRejected, contracts.DecisionQueued, step.time, "queued")
		outcome.Decisions = append(outcome.Decisions, decision)
		outcome.Audits = append(outcome.Audits, matrixAuditV2("audit-queue", "candidate_queued", candidate.CandidateID, "", decision.DecisionID, step.time, "queued"))
	case "expiry":
		checkpoint.LastObservationSequence++
		checkpoint.LastPolicyTimeMS = step.time
		checkpoint.Preview = []contracts.InsightCandidateV1{}
		checkpoint.ActivePrimary = nil
		decision := matrixDecisionV2("decision-expiry", candidate.CandidateID, "", checkpoint.PolicyRevision, contracts.DecisionShown, contracts.DecisionExpired, step.time, "expired")
		outcome.Decisions = append(outcome.Decisions, decision)
		outcome.Audits = append(outcome.Audits, matrixAuditV2("audit-expiry", "candidate_expired", candidate.CandidateID, "", decision.DecisionID, step.time, "expired"))
		checkpoint.CandidateTombstones = []contracts.PolicyCandidateTombstoneV2{{CandidateID: candidate.CandidateID, RuleID: candidate.RuleVersion, TerminalDecisionID: decision.DecisionID, TerminalState: contracts.DecisionExpired, SuppressUntilPolicyTimeMS: 30}}
	case "tombstone_expiry":
		checkpoint.LastObservationSequence++
		checkpoint.LastPolicyTimeMS = step.time
		checkpoint.CandidateTombstones = contracts.ExpirePolicyTombstonesV2(checkpoint.CandidateTombstones, step.time)
		outcome.Audits = append(outcome.Audits, matrixAuditV2("audit-tombstone-expiry", "tombstone_expired", candidate.CandidateID, "", "", step.time, "expired"))
	default:
		outcome.Result = applySemanticCommandV2(t, checkpoint, step, candidate, &outcome)
	}

	checkpoint.CommitSequence++
	checkpoint.CreatedTimeMS = checkpoint.LastPolicyTimeMS
	checkpoint.StateHash, _ = checkpoint.ComputeStateHash()
	outcome.StateHash = checkpoint.StateHash
	if outcome.Result != nil {
		if err := outcome.Result.Validate(); err != nil {
			t.Fatalf("%s result: %v", step.name, err)
		}
	}
	for _, decision := range outcome.Decisions {
		if err := decision.Validate(); err != nil {
			t.Fatalf("%s decision: %v", step.name, err)
		}
	}
	for _, audit := range outcome.Audits {
		if err := audit.Validate(); err != nil {
			t.Fatalf("%s audit: %v", step.name, err)
		}
	}
	return outcome
}

func applySemanticCommandV2(t *testing.T, checkpoint *contracts.PolicyCheckpointV2, step semanticStepV2, candidate contracts.InsightCandidateV1, outcome *semanticOutcomeV2) *contracts.OperatorCommandResultV1 {
	t.Helper()
	accepted := step.kind != "rejected_enable_stale" && step.kind != "rejected_disable_out_of_order"
	previous := checkpoint.PolicyRevision
	resulting := previous
	status, reason := contracts.CommandRejected, "stale_revision"
	if accepted {
		status, reason = contracts.CommandAccepted, "accepted"
		resulting++
		checkpoint.PolicyRevision = resulting
		checkpoint.LastPolicyTimeMS = step.time
	}
	if step.kind == "rejected_disable_out_of_order" {
		reason = "out_of_order_policy_time"
	}
	commandID := "command-" + step.name
	result := &contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: commandID, SessionID: "session", Status: status, PreviousRevision: previous, ResultingRevision: resulting, DecisionIDs: []string{}, Reason: reason}

	if accepted {
		switch step.kind {
		case "accepted_disable":
			checkpoint.DisabledRuleIDs = []string{"rule.v1"}
		case "accepted_enable":
			checkpoint.DisabledRuleIDs = []string{}
		case "pin":
			checkpoint.Pins = []contracts.PolicyPinV2{{CandidateID: candidate.CandidateID, DecisionID: "decision-pin"}}
		case "unpin":
			checkpoint.Pins = []contracts.PolicyPinV2{}
		case "show":
			checkpoint.Preview = []contracts.InsightCandidateV1{}
			decision := matrixDecisionV2("decision-show", candidate.CandidateID, commandID, resulting, contracts.DecisionQueued, contracts.DecisionShown, step.time, "shown")
			checkpoint.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: candidate, Decision: decision}
			outcome.Decisions = append(outcome.Decisions, decision)
			result.DecisionIDs = []string{decision.DecisionID}
			outcome.Publication = contracts.PublicationPublish
		case "emergency_hide":
			checkpoint.EmergencyHide = true
			outcome.Publication = contracts.PublicationHide
		case "clear_emergency":
			checkpoint.EmergencyHide = false
			outcome.Publication = contracts.PublicationPublish
		}
	}
	resultHash, err := contracts.CanonicalSHA256(*result)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.CommandResults = append(checkpoint.CommandResults, contracts.PolicyCommandResultRefV2{CommandID: commandID, ResultSHA256: resultHash})
	checkpoint.CommandLocators = append(checkpoint.CommandLocators, contracts.PolicyCommandLocatorV2{CommandID: commandID, SegmentID: "segment.pcl2", FrameOffset: int64(len(checkpoint.CommandLocators)), CommitSequence: checkpoint.CommitSequence, FrameSHA256: fmt.Sprintf("%064x", len(checkpoint.CommandLocators)+1)})
	sort.Slice(checkpoint.CommandResults, func(i, j int) bool {
		return checkpoint.CommandResults[i].CommandID < checkpoint.CommandResults[j].CommandID
	})
	sort.Slice(checkpoint.CommandLocators, func(i, j int) bool {
		return checkpoint.CommandLocators[i].CommandID < checkpoint.CommandLocators[j].CommandID
	})
	outcome.Audits = append(outcome.Audits, matrixAuditV2("audit-"+step.name, "operator_command", "", commandID, "", step.time, reason))
	return result
}

func matrixCandidateV2() contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{
		SchemaVersion: contracts.InsightCandidateSchemaV1, CandidateID: "candidate-1", SessionID: "session", RuleVersion: "rule.v1", ConfigVersion: "config.v1",
		LocalizationKey: "insight.matrix", Parameters: []contracts.TypedParameterV1{}, Evidence: []contracts.EvidenceRefV1{}, ObservedValues: []contracts.ObservedMetricV1{},
		Confidence: "observed", Priority: 100, CreatedTimeMS: 11, ExpiryTimeMS: 20, SourceRequirements: []contracts.SourceRequirementV1{}, Availability: "available",
	}
}

func matrixDecisionV2(id, candidateID, commandID string, revision uint64, prior, resulting string, policyTime int64, reason string) contracts.BroadcastDecisionV1 {
	return contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id, SessionID: "session", PolicyRevision: revision, CandidateID: candidateID, CommandID: commandID, PriorState: prior, ResultingState: resulting, PolicyTimeMS: policyTime, Reason: reason}
}

func matrixAuditV2(id, eventType, candidateID, commandID, decisionID string, policyTime int64, reason string) contracts.AuditEventV1 {
	return contracts.AuditEventV1{SchemaVersion: contracts.AuditEventSchemaV1, EventID: id, SessionID: "session", EventType: eventType, PolicyTimeMS: policyTime, CandidateID: candidateID, CommandID: commandID, DecisionID: decisionID, Reason: reason}
}
