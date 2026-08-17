package m4match

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func coverageFrame(sequence uint64, body string) CoverageFrameV1 {
	payload := []byte(body)
	return CoverageFrameV1{Sequence: sequence, RawRecordSHA256: payloadSHA(payload), RawRecord: payload}
}

func completeSuppressionEvidence(t *testing.T) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
	t.Helper()
	runtime := NewSuppressionRuntime()
	adapter := NewRehearsalProductionAdapter(runtime)
	if err := adapter.EvaluateFrame(17, SuppressionInputV1{Availability: "unavailable_for_public_match"}); err != nil {
		t.Fatal(err)
	}
	return runtime.Evidence()
}

func validCoverageDelta(t *testing.T) FieldCoverageDeltaV1 {
	t.Helper()
	baseline := []CoverageFrameV1{
		coverageFrame(1, `{"map":{"clock_time":null,"players":{"100":{"health":1}}}}`),
		coverageFrame(2, `{"map":{"clock_time":1,"players":{"101":{"health":2}}}}`),
	}
	rehearsal := []CoverageFrameV1{
		coverageFrame(1, `{"map":{"clock_time":2,"players":{"200":{"health":3}}}}`),
		coverageFrame(2, `{"map":{"clock_time":3,"players":{"201":{"health":4}}},"provider":{"name":"x"}}`),
	}
	delta, err := GenerateFieldCoverageDelta(baseline, rehearsal, CapturedScheduleSHA256, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	return delta
}

func TestRehearsalParserIsClosedAndIdentityImmutable(t *testing.T) {
	classification, err := ParseRehearsalClassification("public_match_rehearsal", "public_match")
	if err != nil || classification.Purpose != PurposePublicMatchRehearsal || classification.Class != MatchClassPublicMatch {
		t.Fatalf("legal classification rejected: %#v %v", classification, err)
	}
	for _, pair := range [][2]string{{"p4_acceptance", "ti"}, {"public_match_rehearsal", "ti"}, {"public_match_rehearsal", "public_tournament"}, {"", ""}} {
		if _, err := ParseRehearsalClassification(pair[0], pair[1]); err == nil {
			t.Fatalf("illegal classification accepted: %q/%q", pair[0], pair[1])
		}
	}
	identity := acceptedRehearsalIdentity()
	if identity.AcceptedRehearsalSpec != "958c3f0d3fd464df4960905bc4bb7918521884e6" || identity.ClaimsP4 || identity.QualifyingMatch || identity.AcceptanceEligible || identity.AcceptanceGate != "none" {
		t.Fatalf("unsafe rehearsal identity: %#v", identity)
	}
	if UnavailablePublicMatchIdentity() != (PublicMatchUnavailableIdentityV1{
		Competition: "unavailable_for_public_match", League: "unavailable_for_public_match",
		Series: "unavailable_for_public_match", GameNumber: "unavailable_for_public_match",
		Teams: "unavailable_for_public_match", Roster: "unavailable_for_public_match",
		Organizer: "unavailable_for_public_match", TIIdentity: "unavailable_for_public_match",
	}) {
		t.Fatal("public match identities are not uniformly typed unavailable")
	}
}

func TestRuntimeSuppressionRequiresExecutedAuditedBranches(t *testing.T) {
	executions, audits := completeSuppressionEvidence(t)
	if err := validateSuppressionEvidence(executions, audits); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]SuppressionExecutionV1, []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1)
	}{
		{"real branch without audit", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			return e, a[1:]
		}},
		{"audit without execution", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			return e[1:], a
		}},
		{"fallback", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			e[0].FallbackUsed = true
			return e, a
		}},
		{"duplicate audit", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			a[1] = a[0]
			return e, a
		}},
		{"unexecuted static audit", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			a[0].RuntimeExecuted = false
			return e, a
		}},
		{"candidate leak", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			a[0].CandidateClaim = true
			return e, a
		}},
		{"decision leak", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			a[0].DecisionClaim = true
			return e, a
		}},
		{"overlay leak", func(e []SuppressionExecutionV1, a []SuppressionAuditV1) ([]SuppressionExecutionV1, []SuppressionAuditV1) {
			a[0].OverlayClaim = true
			return e, a
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := append([]SuppressionExecutionV1(nil), executions...)
			a := append([]SuppressionAuditV1(nil), audits...)
			e, a = test.mutate(e, a)
			if err := validateSuppressionEvidence(e, a); err == nil {
				t.Fatal("adversarial suppression evidence accepted")
			}
		})
	}
	if err := NewSuppressionRuntime().Evaluate(1, acceptedSuppressionBranches[0].ID, SuppressionInputV1{Availability: "display_name_fallback"}); err == nil {
		t.Fatal("display/account fallback was accepted")
	}
}

func TestFieldCoverageIsValueFreeDeterministicAndComplete(t *testing.T) {
	delta := validCoverageDelta(t)
	payloadA, _ := canonical(delta)
	payloadB, _ := canonical(validCoverageDelta(t))
	if !bytes.Equal(payloadA, payloadB) {
		t.Fatal("coverage generation is nondeterministic")
	}
	for _, forbidden := range []string{"100", "101", "200", "201", `"x"`} {
		if bytes.Contains(payloadA, []byte(forbidden)) {
			t.Fatalf("coverage retained a concrete dynamic key or scalar sample: %s", forbidden)
		}
	}
	if err := ValidateFieldCoverageDelta(delta); err != nil {
		t.Fatal(err)
	}
	seenAdditional := false
	seenDifference := false
	for _, path := range delta.Paths {
		if path.Classification == "additional_in_rehearsal" {
			seenAdditional = true
		}
		if path.Classification == "different_type_or_nullability" {
			seenDifference = true
		}
	}
	if !seenAdditional || !seenDifference {
		t.Fatalf("coverage union/classification incomplete: %#v", delta.Paths)
	}
}

func TestFieldCoverageRejectsIdentityOrderHashAndPathAttacks(t *testing.T) {
	valid := []CoverageFrameV1{coverageFrame(1, `{"map":{"x":1}}`), coverageFrame(2, `{"map":{"x":2}}`)}
	tests := []struct {
		name   string
		frames []CoverageFrameV1
	}{
		{"duplicate sequence", []CoverageFrameV1{valid[0], valid[0]}},
		{"out of order", []CoverageFrameV1{valid[1], valid[0]}},
		{"missing identity", []CoverageFrameV1{{RawRecord: []byte(`{}`)}}},
		{"hash mismatch", []CoverageFrameV1{{Sequence: 1, RawRecordSHA256: strings.Repeat("a", 64), RawRecord: []byte(`{}`)}}},
		{"malformed JSON", []CoverageFrameV1{coverageFrame(1, `{`)}},
		{"malformed segment", []CoverageFrameV1{coverageFrame(1, `{"bad key":1}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := GenerateFieldCoverageDelta(valid, test.frames, CapturedScheduleSHA256, strings.Repeat("b", 64), strings.Repeat("c", 64)); err == nil {
				t.Fatal("invalid coverage population accepted")
			}
		})
	}
	if _, err := GenerateFieldCoverageDelta(valid, valid, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)); err == nil {
		t.Fatal("baseline identity mismatch accepted")
	}
}

func TestFieldCoverageProfilesNullAbsentArraysTypesAndDynamicCollisions(t *testing.T) {
	baseline := []CoverageFrameV1{
		coverageFrame(1, `{"nullable":null,"array":[{"value":1}],"dynamic":{"1":true,"2":false}}`),
		coverageFrame(2, `{"array":[],"dynamic":{"3":true}}`),
	}
	rehearsal := []CoverageFrameV1{
		coverageFrame(1, `{"nullable":"now-string","array":[{"value":null}],"dynamic":{"4":true,"5":false}}`),
		coverageFrame(2, `{"nullable":"always-present","array":[{"value":2}],"dynamic":{"6":true}}`),
	}
	delta, err := GenerateFieldCoverageDelta(baseline, rehearsal, CapturedScheduleSHA256, strings.Repeat("d", 64), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	var nullable, collision, arrayValue bool
	for _, path := range delta.Paths {
		switch path.Path {
		case "/k:nullable":
			nullable = path.Classification == "different_type_or_nullability" && path.Baseline.Presence == "sometimes" && path.Baseline.Nullable
		case "/k:dynamic/d:decimal":
			collision = path.Baseline.HasDynamicCollision && path.Rehearsal.HasDynamicCollision
		case "/k:array/a:[]/k:value":
			arrayValue = path.Classification == "different_type_or_nullability"
		}
	}
	if !nullable || !collision || !arrayValue {
		t.Fatalf("missing structural coverage cases: nullable=%v collision=%v array=%v", nullable, collision, arrayValue)
	}
}

func TestFieldCoverageStatusClosedUnion(t *testing.T) {
	delta := validCoverageDelta(t)
	if err := ValidateFieldCoverageStatus(FieldCoverageStatusV1{State: "complete", Delta: &delta}); err != nil {
		t.Fatal(err)
	}
	reasons := []struct {
		reason                       string
		observed, accepted, rejected uint64
	}{
		{"no_accepted_frames", 0, 0, 0}, {"insufficient_frames", 1, 1, 0},
		{"baseline_identity_failure", 3, 2, 1}, {"rehearsal_identity_failure", 2, 2, 0},
		{"coverage_generation_failure", 3, 2, 1},
	}
	for _, reason := range reasons {
		status := FieldCoverageStatusV1{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{Reason: reason.reason, ObservedFrames: reason.observed, AcceptedFrames: reason.accepted, RejectedFrames: reason.rejected}}
		if err := ValidateFieldCoverageStatus(status); err != nil {
			t.Fatalf("%s: %v", reason.reason, err)
		}
	}
	bad := []FieldCoverageStatusV1{
		{State: "complete"}, {State: "complete", Delta: &delta, Unavailable: &FieldCoverageUnavailableV1{}},
		{State: "unavailable"}, {State: "unavailable", Delta: &delta, Unavailable: &FieldCoverageUnavailableV1{Reason: "no_accepted_frames"}},
		{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{Reason: "partial_delta", ObservedFrames: 1, AcceptedFrames: 1}},
		{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{Reason: "no_accepted_frames", ObservedFrames: 1, AcceptedFrames: 1}},
	}
	for index, status := range bad {
		if err := ValidateFieldCoverageStatus(status); err == nil {
			t.Fatalf("bad union %d accepted", index)
		}
	}
}

func TestEveryFailureSealsVerifiesAndRemainsCleanupAdmissible(t *testing.T) {
	executions, audits := completeSuppressionEvidence(t)
	cases := []struct {
		name, failure, reason        string
		observed, accepted, rejected uint64
		late                         bool
		stable                       bool
	}{
		{"preview refusal", "preview_refused", "no_accepted_frames", 0, 0, 0, false, true},
		{"zero frames", "zero_frames", "no_accepted_frames", 0, 0, 0, false, true},
		{"one frame", "insufficient_frames", "insufficient_frames", 1, 1, 0, false, true},
		{"partial frame", "partial_frame", "coverage_generation_failure", 2, 1, 1, false, true},
		{"late join", "late_join", "coverage_generation_failure", 2, 2, 0, true, true},
		{"malformed frame", "malformed_frame", "rehearsal_identity_failure", 2, 1, 1, false, true},
		{"process loss", "process_lost", "coverage_generation_failure", 2, 2, 0, false, false},
		{"process replacement", "process_replaced", "coverage_generation_failure", 2, 2, 0, false, false},
		{"baseline failure", "baseline_identity_failure", "baseline_identity_failure", 2, 2, 0, false, true},
		{"rehearsal identity failure", "rehearsal_identity_failure", "rehearsal_identity_failure", 2, 2, 0, false, true},
		{"coverage generation failure", "coverage_generation_failure", "coverage_generation_failure", 2, 2, 0, false, true},
		{"verifier restart", "verifier_restart", "coverage_generation_failure", 2, 2, 0, false, true},
		{"cleanup path", "operator_abort", "no_accepted_frames", 0, 0, 0, false, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			terminal := RehearsalTerminalV1{
				SchemaVersion: RehearsalTerminalSchemaVersion, Identity: acceptedRehearsalIdentity(), Outcome: "rehearsal_failed", FailureCode: test.failure,
				Attempt:    RehearsalAttemptV1{ObservedFrames: test.observed, AcceptedFrames: test.accepted, RejectedFrames: test.rejected, ProcessStable: test.stable},
				Coverage:   FieldCoverageStatusV1{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{Reason: test.reason, ObservedFrames: test.observed, AcceptedFrames: test.accepted, RejectedFrames: test.rejected}},
				Executions: executions, Audits: audits, CleanupAdmissible: true, NonResumable: true,
			}
			terminal.Attempt.LateJoin = test.late
			if err := ValidateRehearsalTerminal(terminal); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompletedRehearsalRequiresExactCompletePopulation(t *testing.T) {
	delta := validCoverageDelta(t)
	executions, audits := completeSuppressionEvidence(t)
	result := RehearsalTerminalV1{
		SchemaVersion: RehearsalTerminalSchemaVersion, Identity: acceptedRehearsalIdentity(), Outcome: "rehearsal_completed",
		Attempt:  RehearsalAttemptV1{ObservedFrames: delta.RehearsalSourceFrameCount, AcceptedFrames: delta.RehearsalSourceFrameCount, ProcessStable: true, OBSFinalized: true, CleanExit: true},
		Coverage: FieldCoverageStatusV1{State: "complete", Delta: &delta}, Executions: executions, Audits: audits,
		CleanupAdmissible: true, NonResumable: true,
	}
	if err := ValidateRehearsalTerminal(result); err != nil {
		t.Fatal(err)
	}
	result.Attempt.AcceptedFrames = 0
	result.Attempt.ObservedFrames = 0
	if err := ValidateRehearsalTerminal(result); err == nil {
		t.Fatal("zero-frame rehearsal completed")
	}
}

func TestUnknownFailureAndPopulationSpliceAreRejected(t *testing.T) {
	executions, audits := completeSuppressionEvidence(t)
	result := RehearsalTerminalV1{
		SchemaVersion: RehearsalTerminalSchemaVersion, Identity: acceptedRehearsalIdentity(), Outcome: "rehearsal_failed", FailureCode: "invented_failure",
		Attempt:    RehearsalAttemptV1{ObservedFrames: 1, AcceptedFrames: 1},
		Coverage:   FieldCoverageStatusV1{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{Reason: "insufficient_frames", ObservedFrames: 1, AcceptedFrames: 1}},
		Executions: executions, Audits: audits, CleanupAdmissible: true, NonResumable: true,
	}
	if err := ValidateRehearsalTerminal(result); err == nil {
		t.Fatal("unknown terminal failure code accepted")
	}
	result.FailureCode = "insufficient_frames"
	result.Attempt.ObservedFrames = 2
	result.Attempt.RejectedFrames = 1
	if err := ValidateRehearsalTerminal(result); err == nil {
		t.Fatal("spliced terminal/coverage populations accepted")
	}
}

func TestP4AndRehearsalWireContractsMutuallyReject(t *testing.T) {
	p4, _ := canonical(Readiness{SchemaVersion: ReadinessSchemaVersion, Mode: "preflight", ClaimsP4: false, HumanInstruction: HumanInstruction})
	var rehearsal RehearsalReadinessV1
	if strictCanonicalRehearsalJSON(p4, &rehearsal) == nil {
		t.Fatal("rehearsal decoder accepted P4 readiness")
	}
	rehearsalPayload, _ := canonical(RehearsalReadinessV1{SchemaVersion: RehearsalReadinessSchemaVersion, ConsoleState: "REHEARSAL_READY", Identity: acceptedRehearsalIdentity()})
	var p4Readiness Readiness
	if json.Unmarshal(rehearsalPayload, &p4Readiness) == nil && p4Readiness.SchemaVersion == ReadinessSchemaVersion {
		t.Fatal("P4 accepted rehearsal readiness")
	}
}

func TestCanonicalRehearsalEvidenceHasNoAcceptanceCapability(t *testing.T) {
	executions, audits := completeSuppressionEvidence(t)
	value := RehearsalEvidenceV1{SchemaVersion: RehearsalSchemaVersion, Mode: "preflight", Identity: acceptedRehearsalIdentity(), UnavailableIdentity: UnavailablePublicMatchIdentity(), Executions: executions, Audits: audits, NonResumable: true}
	if err := AssertNoScalarOrIdentityLeak(value); err != nil {
		t.Fatal(err)
	}
	payload, _ := canonical(value)
	for _, forbidden := range []string{"ARMED", "DOT-64", "DOT-62", "DOT-70", "acceptance_transition", "public_tournament"} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("found forbidden capability %s", forbidden)
		}
	}
}
