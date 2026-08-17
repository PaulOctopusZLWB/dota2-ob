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
		EvidenceIndexSHA256: strings.Repeat("b", 64), CaptureOwnerSHA256: strings.Repeat("d", 64), HumanInstruction: instruction,
	}
}

func testAttemptRequest(sessionID string) RehearsalAttemptRequestV1 {
	return RehearsalAttemptRequestV1{SchemaVersion: RehearsalAttemptSchemaVersion}
}

func testCompleteRawSession(t *testing.T, sessionID string) []byte {
	t.Helper()
	var schedule struct {
		Updates []struct {
			Body json.RawMessage `json:"body"`
		} `json:"updates"`
	}
	payload, err := os.ReadFile(filepath.Join("..", "integration", "m4", "testdata", "captured_gsi_schedule.json"))
	if err != nil || json.Unmarshal(payload, &schedule) != nil {
		t.Fatal(err)
	}
	bodies := make([]json.RawMessage, 3)
	for index, clock := range []int64{-10, 0, 1} {
		var body map[string]any
		if json.Unmarshal(schedule.Updates[index].Body, &body) != nil {
			t.Fatal("fixture")
		}
		m := body["map"].(map[string]any)
		m["matchid"], m["clock_time"] = "derived-match", clock
		if index == 2 {
			m["game_state"], m["win_team"] = "DOTA_GAMERULES_STATE_POST_GAME", "radiant"
		}
		bodies[index], _ = json.Marshal(body)
	}
	root := t.TempDir()
	clock := time.Unix(1_800_000_000, 0).UTC()
	store, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { clock = clock.Add(time.Second); return clock }))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range bodies {
		if _, err := store.Append(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.RawPath())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testCaptureBinding(raw scannedRehearsalRaw) rehearsalCaptureBinding {
	h := RehearsalHarnessEvidenceV1{
		SchemaVersion: "public_match_rehearsal_harness_evidence.v1", SessionID: raw.Manifest.SessionID, RawSHA256: raw.Manifest.RawSHA256,
		RecordingSHA256: strings.Repeat("1", 64), RecordingBytes: 1,
		ReconciliationSHA256: strings.Repeat("2", 64), ConfinementSHA256: strings.Repeat("3", 64),
		RecoveryInputSHA256: raw.Manifest.RawSHA256, RecoveryFirstSHA256: strings.Repeat("4", 64), RecoverySecondSHA256: strings.Repeat("4", 64), RecoveryStartTicks1: 10, RecoveryStartTicks2: 20,
		ProjectedCount: raw.Manifest.Accepted, PolicyCount: raw.Manifest.Accepted, AuditCount: raw.Manifest.Accepted, OverlayCount: raw.Manifest.Accepted,
	}
	for sequence := uint64(1); sequence <= raw.Manifest.Accepted; sequence++ {
		h.Process = append(h.Process, RehearsalProcessObservationV1{SourceSequence: sequence, ExecutableSHA256: strings.Repeat("5", 64), StartTicks: 9, State: "running"})
	}
	if raw.Manifest.Accepted > 0 {
		h.FailClosed = []RehearsalFailClosedObservationV1{{SourceSequence: 1, ElapsedMS: 2000}}
		h.Resources = []RehearsalResourceObservationV1{{SourceSequence: 1, IntervalMS: 5000, ProductRSS: 1, OBSRSS: 1}}
		for index, action := range []string{"preview", "approve", "reject", "emergency_hide"} {
			h.Operator = append(h.Operator, RehearsalOperatorObservationV1{Ordinal: uint8(index + 1), Action: action, SourceSequence: 1, AuditSHA256: strings.Repeat("6", 64)})
		}
	}
	binding := rehearsalCaptureBinding{Owner: RehearsalCaptureOwnerV1{SessionID: raw.Manifest.SessionID}, OwnerSHA256: strings.Repeat("7", 64), SealSHA256: strings.Repeat("8", 64), HarnessSHA256: strings.Repeat("9", 64), Facts: deriveRawFacts(raw), Harness: h}
	binding.Failure = validateHarnessEvidence(h, raw.Manifest)
	return binding
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
	sessionID := "empty"
	if first := bytes.SplitN(rawPayload, []byte{'\n'}, 2)[0]; len(first) > 0 {
		var header struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(first, &header)
		if header.SessionID != "" {
			sessionID = header.SessionID
		}
	}
	raw := scanRehearsalRaw(rawPayload, sessionID)
	binding := testCaptureBinding(raw)
	built, err := buildRehearsalAttempt(filepath.Join("..", ".."), testReadiness(ready), request, raw, binding)
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
	for _, registryEntry := range rehearsalCheckRegistry {
		if registryEntry.State != "dynamic" {
			continue
		}
		failed := registryEntry.ID
		t.Run(failed, func(t *testing.T) {
			checks := make([]RehearsalCheckV1, 0, len(rehearsalCheckRegistry))
			for _, entry := range rehearsalCheckRegistry {
				state := entry.State
				if state == "dynamic" {
					state = "passed"
				}
				if entry.ID == failed {
					state = "failed"
				}
				checks = append(checks, RehearsalCheckV1{ID: entry.ID, State: state})
			}
			evidence := RehearsalEvidenceV1{CandidateCommit: strings.Repeat("a", 40), Checks: checks}
			payload, _ := canonical(evidence)
			readiness := buildRehearsalReadiness(evidence, payload)
			if readiness.Ready || readiness.ConsoleState != rehearsalRefusedConsoleState || readiness.HumanInstruction != "" || !containsString(readiness.Failures, failed) {
				t.Fatalf("refusal leaked readiness: %#v", readiness)
			}
		})
	}
	valid := make([]RehearsalCheckV1, len(rehearsalCheckRegistry))
	for i, entry := range rehearsalCheckRegistry {
		state := entry.State
		if state == "dynamic" {
			state = "passed"
		}
		valid[i] = RehearsalCheckV1{ID: entry.ID, State: state}
	}
	for _, mutate := range []func([]RehearsalCheckV1) []RehearsalCheckV1{
		func(v []RehearsalCheckV1) []RehearsalCheckV1 { return v[1:] },
		func(v []RehearsalCheckV1) []RehearsalCheckV1 {
			return append(v, RehearsalCheckV1{ID: "invented", State: "passed"})
		},
		func(v []RehearsalCheckV1) []RehearsalCheckV1 { v[0], v[1] = v[1], v[0]; return v },
	} {
		copied := append([]RehearsalCheckV1(nil), valid...)
		if got := rehearsalCheckFailures(mutate(copied)); !equalStrings(got, []string{"invalid_check_contract"}) {
			t.Fatalf("forged registry accepted: %v", got)
		}
	}
	occupied := func(string) (io.Closer, error) { return nil, errors.New("occupied") }
	if rehearsalListenerAvailable(occupied, CaptureAddress) {
		t.Fatal("occupied listener reported available")
	}
	available := func(string) (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	if volatileListenerMatches(true, occupied) || volatileListenerMatches(false, available) {
		t.Fatal("listener mutation matched sealed preflight")
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

func TestStreamingResourceEnvelopeExactAndLimitPlusOne(t *testing.T) {
	exactPath := filepath.Join(t.TempDir(), "exact.raw")
	if err := os.WriteFile(exactPath, bytes.Repeat([]byte{'x'}, MaxRehearsalRawLineBytes-1), 0o600); err != nil {
		t.Fatal(err)
	}
	exact := scanRehearsalRawPath(exactPath, "limits")
	if exact.Manifest.FailureCode != "partial_frame" && exact.Manifest.FailureCode != "raw_line_limit" {
		t.Fatalf("exact bounded input escaped typed failure: %#v", exact.Manifest)
	}
	plusPath := filepath.Join(t.TempDir(), "plus.raw")
	file, err := os.Create(plusPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxRehearsalRawBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	plus := scanRehearsalRawPath(plusPath, "limits")
	if plus.Manifest.FailureCode != "raw_total_limit" || plus.Manifest.RawBytes != MaxRehearsalRawBytes+1 {
		t.Fatalf("limit+1 not closed: %#v", plus.Manifest)
	}
	unterminated := scanRehearsalRaw([]byte(`{"schema_version":3}`), "limits")
	if unterminated.Manifest.FailureCode != "partial_frame" {
		t.Fatalf("unterminated input not typed: %#v", unterminated.Manifest)
	}
	frames := make([]CoverageFrameV1, MaxRehearsalCoverageFrames+1)
	if _, err := GenerateFieldCoverageDelta(frames, frames, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)); err == nil {
		t.Fatal("coverage cap+1 accepted")
	}
	manifest := RehearsalRawManifestV1{Accepted: MaxRehearsalRecords}
	over := RehearsalSuppressionEvidenceV1{SchemaVersion: RehearsalSuppressionSchema, Executions: make([]SuppressionExecutionV1, MaxRehearsalExecutions+1), Audits: make([]SuppressionAuditV1, MaxRehearsalAudits+1)}
	if validateSuppressionEvidence(over, manifest) == nil {
		t.Fatal("suppression cap+1 accepted")
	}
}

func TestForgedSuccessDefaultsAndFixtureCannotComplete(t *testing.T) {
	forged := []byte(`{"schema_version":"public_match_rehearsal_attempt.v2","session_id":"foreign","match_id":"forged","preview_accepted":true,"obs_finalized":true}`)
	var request RehearsalAttemptRequestV1
	if strictCanonicalRehearsalJSON(forged, &request) == nil {
		t.Fatal("removed success assertions remain accepted")
	}
	rawPayload := testRawSession(t, "fixture", 3)
	raw := scanRehearsalRaw(rawPayload, "fixture")
	binding := testCaptureBinding(raw)
	built, err := buildRehearsalAttempt(filepath.Join("..", ".."), testReadiness(true), testAttemptRequest("fixture"), raw, binding)
	if err != nil {
		t.Fatal(err)
	}
	if built.Terminal.Outcome != "rehearsal_failed" || built.Terminal.FailureCode != "late_join" {
		t.Fatalf("fixture upgraded to success: %#v", built.Terminal)
	}
	request = testAttemptRequest("fixture")
	request.ExpectedMatchID = "not-in-raw"
	built, err = buildRehearsalAttempt(filepath.Join("..", ".."), testReadiness(true), request, raw, binding)
	if err != nil {
		t.Fatal(err)
	}
	if built.Terminal.FailureCode != "expected_match_mismatch" {
		t.Fatalf("match mismatch not derived: %#v", built.Terminal)
	}
}

func TestExecutableAttemptRegeneratesCoverageAndEightFamilyProductionSuppression(t *testing.T) {
	rawPayload := testCompleteRawSession(t, "normal")
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
	normalRaw := testCompleteRawSession(t, "failure")
	oneRaw := testRawSession(t, "failure", 1)
	partialRaw := append(append([]byte(nil), normalRaw...), []byte(`{"schema_version":3`)...)
	tests := []struct {
		name, want string
		raw        []byte
		ready      bool
		mutate     func(*RehearsalAttemptRequestV1, *rehearsalCaptureBinding)
	}{
		{"preflight refusal", "preflight_refused", nil, false, nil},
		{"zero", "zero_frames", nil, true, nil},
		{"one", "insufficient_frames", oneRaw, true, nil},
		{"partial", "partial_frame", partialRaw, true, nil},
		{"expected mismatch", "expected_match_mismatch", normalRaw, true, func(r *RehearsalAttemptRequestV1, _ *rehearsalCaptureBinding) { r.ExpectedMatchID = "forged" }},
		{"process loss", "process_lost", normalRaw, true, func(_ *RehearsalAttemptRequestV1, b *rehearsalCaptureBinding) { b.Failure = "process_lost" }},
		{"process replacement", "process_replaced", normalRaw, true, func(_ *RehearsalAttemptRequestV1, b *rehearsalCaptureBinding) { b.Failure = "process_replaced" }},
		{"obs", "obs_finalization_failure", normalRaw, true, func(_ *RehearsalAttemptRequestV1, b *rehearsalCaptureBinding) { b.Failure = "obs_finalization_failure" }},
		{"unclean", "unclean_exit", normalRaw, true, func(_ *RehearsalAttemptRequestV1, b *rehearsalCaptureBinding) { b.Failure = "unclean_exit" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testAttemptRequest("failure")
			sessionID := "empty"
			if len(test.raw) > 0 {
				var h struct {
					SessionID string `json:"session_id"`
				}
				_ = json.Unmarshal(bytes.SplitN(test.raw, []byte{'\n'}, 2)[0], &h)
				sessionID = h.SessionID
			}
			raw := scanRehearsalRaw(test.raw, sessionID)
			binding := testCaptureBinding(raw)
			if test.mutate != nil {
				test.mutate(&request, &binding)
			}
			built, err := buildRehearsalAttempt(filepath.Join("..", ".."), testReadiness(test.ready), request, raw, binding)
			if err != nil {
				t.Fatal(err)
			}
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
	rawPayload := testCompleteRawSession(t, "splice")
	request := testAttemptRequest("splice")
	raw := scanRehearsalRaw(rawPayload, "splice")
	binding := testCaptureBinding(raw)
	built := buildTestAttempt(t, rawPayload, request, true)
	var suppression RehearsalSuppressionEvidenceV1
	if err := json.Unmarshal(built.SuppressionPayload, &suppression); err != nil {
		t.Fatal(err)
	}
	terminal := built.Terminal
	terminal.FailureCode = "late_join"
	terminal.Outcome = "rehearsal_failed"
	if err := validateTerminalStructure(terminal, request, raw.Manifest, binding, suppression, ""); err == nil {
		t.Fatal("unrelated failure code accepted")
	}
	terminal = built.Terminal
	terminal.Coverage.Delta.RehearsalRawSessionSHA256 = strings.Repeat("f", 64)
	if err := validateTerminalStructure(terminal, request, raw.Manifest, binding, suppression, ""); err == nil {
		t.Fatal("caller-supplied unrelated delta accepted")
	}
	terminal = built.Terminal
	suppression.FrameCount--
	if err := validateTerminalStructure(terminal, request, raw.Manifest, binding, suppression, ""); err == nil {
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
