package m4match

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func testReadiness(ready bool) RehearsalReadinessV1 {
	state := rehearsalRefusedConsoleState
	instruction := ""
	if ready {
		state, instruction = rehearsalReadyConsoleState, RehearsalHumanInstruction
	}
	return RehearsalReadinessV1{
		SchemaVersion: RehearsalReadinessSchemaVersion, Ready: ready, ConsoleState: state,
		Identity: acceptedRehearsalIdentity(), CandidateCommit: strings.Repeat("a", 40),
		EvidenceIndexSHA256: strings.Repeat("b", 64), HumanInstruction: instruction,
	}
}

func testAttemptRequest(sessionID string) RehearsalAttemptRequestV1 {
	return RehearsalAttemptRequestV1{
		SchemaVersion: RehearsalAttemptSchemaVersion, SessionID: sessionID,
		MatchID: "123", PreviewAccepted: true, EntryBeforeZero: true,
		ProcessStable: true, ProcessExecutableSHA256: strings.Repeat("c", 64), ProcessStartTicks: 1,
		PlausibleContinuous: true, NormalPostGame: true, OBSFinalized: true, RawReconciled: true,
		ClocksValid: true, ResourcesSampled: true, OperatorActions: 4, FailClosedWithin2S: true,
		ConfinementVerified: true, CleanExit: true,
	}
}

func testRawSession(t *testing.T, sessionID string, count int) []byte {
	t.Helper()
	var schedule struct {
		Updates []struct {
			Body json.RawMessage `json:"body"`
		} `json:"updates"`
	}
	payload, err := os.ReadFile(filepath.Join("..", "integration", "m4", "testdata", "captured_gsi_schedule.json"))
	if err != nil || json.Unmarshal(payload, &schedule) != nil || count > len(schedule.Updates) {
		t.Fatalf("load schedule: %v", err)
	}
	root := t.TempDir()
	clock := time.Unix(1_800_000_000, 0).UTC()
	store, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		if _, err := store.Append(schedule.Updates[index].Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.RawPath())
	if err != nil && count != 0 {
		t.Fatal(err)
	}
	return raw
}

func buildTestAttempt(t *testing.T, rawPayload []byte, request RehearsalAttemptRequestV1, ready bool) builtRehearsalAttempt {
	t.Helper()
	raw := scanRehearsalRaw(rawPayload, request.SessionID)
	built, err := buildRehearsalAttempt(filepath.Join("..", ".."), testReadiness(ready), request, raw)
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestClosedRehearsalIdentityAndParser(t *testing.T) {
	classification, err := ParseRehearsalClassification("public_match_rehearsal", "public_match")
	if err != nil || classification.Purpose != PurposePublicMatchRehearsal || classification.Class != MatchClassPublicMatch {
		t.Fatalf("legal classification rejected: %#v %v", classification, err)
	}
	for _, pair := range [][2]string{{"p4_acceptance", "ti"}, {"public_match_rehearsal", "ti"}, {"public_match_rehearsal", "public_tournament"}, {"", ""}} {
		if _, err := ParseRehearsalClassification(pair[0], pair[1]); err == nil {
			t.Fatalf("illegal pair accepted: %q/%q", pair[0], pair[1])
		}
	}
	identity := acceptedRehearsalIdentity()
	if identity.AcceptedRehearsalSpec != "958c3f0d3fd464df4960905bc4bb7918521884e6" || identity.ClaimsP4 || identity.QualifyingMatch || identity.AcceptanceEligible || identity.AcceptanceGate != "none" {
		t.Fatalf("unsafe immutable identity: %#v", identity)
	}
}

func TestEveryPreflightRefusalIsClosedAndNeverReady(t *testing.T) {
	ids := []string{"candidate_commit", "candidate_sole_parent", "candidate_tree", "clean_tree", "expected_remote", "remote_branch_head", "draft_pr_head", "exclusive_localhost_gsi_listener"}
	for _, failed := range ids {
		t.Run(failed, func(t *testing.T) {
			checks := make([]RehearsalCheckV1, 0, len(ids)+2)
			for _, id := range ids {
				state := "passed"
				if id == failed {
					state = "failed"
				}
				checks = append(checks, RehearsalCheckV1{ID: id, State: state})
			}
			checks = append(checks, RehearsalCheckV1{ID: "p4_acceptance", State: "not_applicable_rehearsal"})
			evidence := RehearsalEvidenceV1{CandidateCommit: strings.Repeat("a", 40), Checks: checks}
			payload, _ := canonical(evidence)
			readiness := buildRehearsalReadiness(evidence, payload)
			if readiness.Ready || readiness.ConsoleState != rehearsalRefusedConsoleState || readiness.HumanInstruction != "" || !containsString(readiness.Failures, failed) {
				t.Fatalf("refusal leaked readiness: %#v", readiness)
			}
		})
	}
	occupied := func(string) (io.Closer, error) { return nil, errors.New("occupied") }
	if rehearsalListenerAvailable(occupied, CaptureAddress) {
		t.Fatal("occupied listener reported available")
	}
}

func TestRawScanBindsExactV3OrderHashesAndPartialReceipt(t *testing.T) {
	rawPayload := testRawSession(t, "scan", 3)
	scanned := scanRehearsalRaw(rawPayload, "scan")
	if scanned.Manifest.Accepted != 3 || scanned.Manifest.Observed != 3 || scanned.Manifest.Rejected != 0 || scanned.Manifest.RawSHA256 != payloadSHA(rawPayload) || len(scanned.Manifest.Records) != 3 {
		t.Fatalf("scan mismatch: %#v", scanned.Manifest)
	}
	partial := scanRehearsalRaw(append(append([]byte(nil), rawPayload...), []byte(`{"schema_version":3`)...), "scan")
	if partial.Manifest.Accepted != 3 || partial.Manifest.Rejected != 1 || partial.Manifest.Observed != 4 || partial.Manifest.FailureCode != "partial_frame" {
		t.Fatalf("partial receipt mismatch: %#v", partial.Manifest)
	}
	mutated := append([]byte(nil), rawPayload...)
	mutated[bytes.IndexByte(mutated, '\n')+1] = '!'
	malformed := scanRehearsalRaw(mutated, "scan")
	if malformed.Manifest.Accepted != 1 || malformed.Manifest.Rejected != 1 || malformed.Manifest.FailureCode != "malformed_frame" {
		t.Fatalf("malformed receipt mismatch: %#v", malformed.Manifest)
	}
}

func TestExecutableAttemptRegeneratesCoverageAndEightFamilyProductionSuppression(t *testing.T) {
	rawPayload := testRawSession(t, "normal", 3)
	request := testAttemptRequest("normal")
	built := buildTestAttempt(t, rawPayload, request, true)
	rebuilt := buildTestAttempt(t, rawPayload, request, true)
	if built.Terminal.Outcome != "rehearsal_completed" || built.Terminal.FailureCode != "" || !bytes.Equal(built.TerminalPayload, rebuilt.TerminalPayload) || !bytes.Equal(built.CoveragePayload, rebuilt.CoveragePayload) || !bytes.Equal(built.SuppressionPayload, rebuilt.SuppressionPayload) {
		t.Fatalf("normal attempt not deterministic/complete: %#v", built.Terminal)
	}
	var suppression RehearsalSuppressionEvidenceV1
	if err := strictCanonicalRehearsalJSON(built.SuppressionPayload, &suppression); err != nil {
		t.Fatal(err)
	}
	families := contracts.HistoricalDisabledFamiliesV1()
	if len(families) != 8 || suppression.FrameCount != 3 || len(suppression.Executions) != 24 || len(suppression.Audits) != 24 {
		t.Fatalf("production suppression population mismatch: families=%v evidence=%#v", families, suppression)
	}
	if built.Terminal.Coverage.State != "complete" || built.Terminal.Coverage.Delta == nil || built.Terminal.Coverage.Delta.RehearsalSourceFrameCount != 3 {
		t.Fatalf("coverage not regenerated from exact population: %#v", built.Terminal.Coverage)
	}
}

func TestZeroPartialAndOperationalFailuresSealDerivedReceipts(t *testing.T) {
	normalRaw := testRawSession(t, "failure", 3)
	oneRaw := testRawSession(t, "failure", 1)
	partialRaw := append(append([]byte(nil), normalRaw...), []byte(`{"schema_version":3`)...)
	tests := []struct {
		name, want string
		raw        []byte
		ready      bool
		mutate     func(*RehearsalAttemptRequestV1)
	}{
		{"preflight refusal", "preflight_refused", nil, false, nil},
		{"preview refusal", "preview_refused", nil, true, func(r *RehearsalAttemptRequestV1) { r.PreviewAccepted = false }},
		{"zero", "zero_frames", nil, true, nil},
		{"one", "insufficient_frames", oneRaw, true, nil},
		{"partial", "partial_frame", partialRaw, true, nil},
		{"late join", "late_join", normalRaw, true, func(r *RehearsalAttemptRequestV1) { r.LateJoin = true }},
		{"process loss", "process_lost", normalRaw, true, func(r *RehearsalAttemptRequestV1) { r.ProcessStable = false }},
		{"process replacement", "process_replaced", normalRaw, true, func(r *RehearsalAttemptRequestV1) { r.ProcessReplaced = true }},
		{"obs", "obs_finalization_failure", normalRaw, true, func(r *RehearsalAttemptRequestV1) { r.OBSFinalized = false }},
		{"unclean", "unclean_exit", normalRaw, true, func(r *RehearsalAttemptRequestV1) { r.CleanExit = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testAttemptRequest("failure")
			if test.mutate != nil {
				test.mutate(&request)
			}
			built := buildTestAttempt(t, test.raw, request, test.ready)
			if built.Terminal.Outcome != "rehearsal_failed" || built.Terminal.FailureCode != test.want || !built.Receipt.CleanupAdmissible || built.Receipt.TerminalSHA256 != payloadSHA(built.TerminalPayload) {
				t.Fatalf("derived failure mismatch: %#v / %#v", built.Terminal, built.Receipt)
			}
		})
	}
}

func TestSuppressionRejectsEveryStaticSplicedFallbackAndLeakCase(t *testing.T) {
	raw := scanRehearsalRaw(testRawSession(t, "audit", 2), "audit")
	valid, failure := evaluateRehearsalProduction(raw)
	if failure != "" || validateSuppressionEvidence(valid, raw.Manifest) != nil {
		t.Fatalf("valid evidence failed: %s", failure)
	}
	tests := []struct {
		name   string
		mutate func(*RehearsalSuppressionEvidenceV1)
	}{
		{"missing execution", func(v *RehearsalSuppressionEvidenceV1) { v.Executions = v.Executions[1:] }},
		{"audit without execution", func(v *RehearsalSuppressionEvidenceV1) { v.Executions = v.Executions[1:]; v.FrameCount = 1 }},
		{"duplicate", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[1] = v.Audits[0] }},
		{"fallback", func(v *RehearsalSuppressionEvidenceV1) { v.Executions[0].FallbackUsed = true }},
		{"unexecuted", func(v *RehearsalSuppressionEvidenceV1) { v.Executions[0].Executed = false }},
		{"sequence splice", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].SourceSequence = 2 }},
		{"raw splice", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].RawRecordSHA256 = strings.Repeat("f", 64) }},
		{"candidate claim", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].CandidateClaim = true }},
		{"decision claim", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].DecisionClaim = true }},
		{"overlay claim", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].OverlayClaim = true }},
		{"display", func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].DisplayProduced = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, _ := canonical(valid)
			var mutated RehearsalSuppressionEvidenceV1
			_ = json.Unmarshal(payload, &mutated)
			test.mutate(&mutated)
			if err := validateSuppressionEvidence(mutated, raw.Manifest); err == nil {
				t.Fatal("adversarial suppression accepted")
			}
		})
	}
}

func TestTerminalRejectsCallerSuppliedDeltaFailureAndPopulation(t *testing.T) {
	rawPayload := testRawSession(t, "splice", 3)
	request := testAttemptRequest("splice")
	raw := scanRehearsalRaw(rawPayload, "splice")
	built := buildTestAttempt(t, rawPayload, request, true)
	var suppression RehearsalSuppressionEvidenceV1
	if err := json.Unmarshal(built.SuppressionPayload, &suppression); err != nil {
		t.Fatal(err)
	}
	terminal := built.Terminal
	terminal.FailureCode = "late_join"
	terminal.Outcome = "rehearsal_failed"
	if err := validateTerminalStructure(terminal, request, raw.Manifest, suppression, ""); err == nil {
		t.Fatal("unrelated failure code accepted")
	}
	terminal = built.Terminal
	terminal.Coverage.Delta.RehearsalRawSessionSHA256 = strings.Repeat("f", 64)
	if err := validateTerminalStructure(terminal, request, raw.Manifest, suppression, ""); err == nil {
		t.Fatal("caller-supplied unrelated delta accepted")
	}
	terminal = built.Terminal
	suppression.FrameCount--
	if err := validateTerminalStructure(terminal, request, raw.Manifest, suppression, ""); err == nil {
		t.Fatal("population-spliced suppression accepted")
	}
}

func TestCoverageValueFreePathsAndIdentityAttacks(t *testing.T) {
	frame := func(sequence uint64, body string) CoverageFrameV1 {
		payload := []byte(body)
		return CoverageFrameV1{Sequence: sequence, RawRecordSHA256: payloadSHA(payload), RawRecord: payload}
	}
	baseline := []CoverageFrameV1{frame(1, `{"x":null,"players":{"100":{"health":1}},"a":[{"v":1}]}`), frame(2, `{"players":{"101":{"health":2}},"a":[]}`)}
	rehearsal := []CoverageFrameV1{frame(1, `{"x":"changed","players":{"200":{"health":3}},"a":[{"v":null}]}`), frame(2, `{"x":"present","players":{"201":{"health":4}},"a":[{"v":2}]}`)}
	delta, err := GenerateFieldCoverageDelta(baseline, rehearsal, CapturedScheduleSHA256, strings.Repeat("d", 64), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := canonical(delta)
	for _, forbidden := range []string{"100", "101", "200", "201", `"changed"`, `"present"`} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("coverage leaked scalar/dynamic value: %s", forbidden)
		}
	}
	bad := append([]CoverageFrameV1(nil), rehearsal...)
	bad[1].Sequence = 1
	if _, err := GenerateFieldCoverageDelta(baseline, bad, CapturedScheduleSHA256, strings.Repeat("d", 64), strings.Repeat("e", 64)); err == nil {
		t.Fatal("duplicate/out-of-order accepted")
	}
	bad = append([]CoverageFrameV1(nil), rehearsal...)
	bad[0].RawRecordSHA256 = strings.Repeat("f", 64)
	if _, err := GenerateFieldCoverageDelta(baseline, bad, CapturedScheduleSHA256, strings.Repeat("d", 64), strings.Repeat("e", 64)); err == nil {
		t.Fatal("hash mismatch accepted")
	}
}

func TestP4AndRehearsalWireContractsRejectEachOther(t *testing.T) {
	p4, _ := canonical(Readiness{SchemaVersion: ReadinessSchemaVersion, Mode: "preflight", HumanInstruction: HumanInstruction})
	var rehearsal RehearsalReadinessV1
	if strictCanonicalRehearsalJSON(p4, &rehearsal) == nil {
		t.Fatal("rehearsal decoder accepted P4")
	}
	rehearsalPayload, _ := canonical(testReadiness(true))
	var p4Readiness Readiness
	if json.Unmarshal(rehearsalPayload, &p4Readiness) == nil && p4Readiness.SchemaVersion == ReadinessSchemaVersion {
		t.Fatal("P4 accepted rehearsal")
	}
}
