package m4match

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture/v3fixture"
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

// testValidatedCaptureBinding is only a terminal-derivation unit fixture. The
// command-level producer tests below exercise creation and validation of the
// physical artifact manifest before this stage is reachable.
func testValidatedCaptureBinding(raw scannedRehearsalRaw) rehearsalCaptureBinding {
	h := RehearsalHarnessEvidenceV1{
		SchemaVersion: "public_match_rehearsal_harness_evidence.v2", SessionID: raw.Manifest.SessionID, RawSHA256: raw.Manifest.RawSHA256,
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
		for index, action := range []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide} {
			h.Operator = append(h.Operator, RehearsalOperatorObservationV1{Ordinal: uint8(index + 1), Action: action, SourceSequence: 1, AuditSHA256: strings.Repeat("6", 64)})
		}
	}
	binding := rehearsalCaptureBinding{Owner: RehearsalCaptureOwnerV1{SessionID: raw.Manifest.SessionID}, OwnerSHA256: strings.Repeat("7", 64), SealSHA256: strings.Repeat("8", 64), HarnessSHA256: strings.Repeat("9", 64), Facts: deriveRawFacts(raw), Harness: h}
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
	binding := testValidatedCaptureBinding(raw)
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
	maximumRaw, err := v3fixture.MaximumRelevantBody()
	if err != nil {
		t.Fatal(err)
	}
	maximumLine := encodeTestV3Frame("limits", 1, maximumRaw)
	if len(maximumLine)+1 > MaxRehearsalRawLineBytes {
		t.Fatalf("valid maximum payload line exceeds cap: %d", len(maximumLine)+1)
	}
	lineExact := scanRehearsalRaw(append(maximumLine, '\n'), "limits")
	if lineExact.Manifest.Accepted != 1 || lineExact.Manifest.FailureCode != "" {
		t.Fatalf("valid maximum-length line rejected: %#v", lineExact.Manifest)
	}
	totalExact := exactTotalRawFixture(t, "total-limit")
	if len(totalExact) != MaxRehearsalRawBytes {
		t.Fatalf("fixture bytes=%d", len(totalExact))
	}
	exact := scanRehearsalRaw(totalExact, "total-limit")
	if exact.Manifest.Accepted != MaxRehearsalRecords || exact.Manifest.RawBytes != MaxRehearsalRawBytes || exact.Manifest.FailureCode != "" {
		t.Fatalf("exact total boundary rejected: %#v", exact.Manifest)
	}
	plus := scanRehearsalRaw(make([]byte, MaxRehearsalRawBytes+1), "limits")
	if plus.Manifest.FailureCode != "raw_total_limit" || plus.Manifest.RawBytes != MaxRehearsalRawBytes+1 {
		t.Fatalf("limit+1 not closed: %#v", plus.Manifest)
	}
	linePlus := scanRehearsalRaw(append(bytes.Repeat([]byte{'x'}, MaxRehearsalRawLineBytes), '\n'), "limits")
	if linePlus.Manifest.FailureCode != "raw_line_limit" {
		t.Fatalf("line cap+1 not closed: %#v", linePlus.Manifest)
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

func paddedGSI(size int) []byte {
	prefix, suffix := []byte(`{"padding":"`), []byte(`"}`)
	if size < len(prefix)+len(suffix) {
		panic("padding size")
	}
	result := make([]byte, 0, size)
	result = append(result, prefix...)
	result = append(result, bytes.Repeat([]byte{'x'}, size-len(prefix)-len(suffix))...)
	return append(result, suffix...)
}

func encodeTestV3Frame(sessionID string, sequence uint64, raw []byte) []byte {
	return encodeTestV3FrameAt(sessionID, sequence, raw, "2027-01-15T08:00:00Z")
}

func encodeTestV3FrameAt(sessionID string, sequence uint64, raw []byte, receivedAt string) []byte {
	encoded := base64.StdEncoding.EncodeToString(raw)
	hash := sha256.Sum256(raw)
	return []byte(fmt.Sprintf(`{"schema_version":3,"session_id":%q,"sequence":%d,"received_at":%q,"source":"gsi","raw_encoding":"base64_std","raw_byte_length":%d,"raw_base64":%q,"raw_payload_sha256":"%x"}`, sessionID, sequence, receivedAt, len(raw), encoded, hash))
}

func exactTotalRawFixture(t *testing.T, sessionID string) []byte {
	t.Helper()
	frames := make([][]byte, MaxRehearsalRecords)
	total := 0
	for sequence := 1; sequence < MaxRehearsalRecords-1; sequence++ {
		frame := encodeTestV3Frame(sessionID, uint64(sequence), paddedGSI(11_000))
		frames[sequence-1] = append(frame, '\n')
		total += len(frame) + 1
	}
	penultimateSizes := []int{100, 999, 1_000, 9_999, 10_000, 10_970, 10_971, 10_972, 99_999, 100_000}
	timestamps := []string{"2027-01-15T08:00:00Z", "2027-01-15T08:00:00.1Z", "2027-01-15T08:00:00.12Z", "2027-01-15T08:00:00.123Z"}
	for _, penultimateSize := range penultimateSizes {
		penultimateLength := testV3FrameLength(sessionID, MaxRehearsalRecords-1, penultimateSize) + 1
		remaining := MaxRehearsalRawBytes - total - penultimateLength
		for _, timestamp := range timestamps {
			for lastSize := (remaining * 3 / 4) - 512; lastSize <= (remaining*3/4)+32 && lastSize <= 10<<20; lastSize++ {
				if lastSize < len(`{"padding":""}`) || testV3FrameLengthAt(sessionID, MaxRehearsalRecords, lastSize, timestamp)+1 != remaining {
					continue
				}
				penultimate := encodeTestV3Frame(sessionID, MaxRehearsalRecords-1, paddedGSI(penultimateSize))
				last := encodeTestV3FrameAt(sessionID, MaxRehearsalRecords, paddedGSI(lastSize), timestamp)
				frames[MaxRehearsalRecords-2] = append(penultimate, '\n')
				frames[MaxRehearsalRecords-1] = append(last, '\n')
				return bytes.Join(frames, nil)
			}
		}
	}
	t.Fatal("could not construct exact total raw fixture")
	return nil
}

func testV3FrameLength(sessionID string, sequence, rawSize int) int {
	return testV3FrameLengthAt(sessionID, sequence, rawSize, "2027-01-15T08:00:00Z")
}

func testV3FrameLengthAt(sessionID string, sequence, rawSize int, receivedAt string) int {
	emptyHash := strings.Repeat("0", 64)
	envelope := fmt.Sprintf(`{"schema_version":3,"session_id":%q,"sequence":%d,"received_at":%q,"source":"gsi","raw_encoding":"base64_std","raw_byte_length":%d,"raw_base64":"","raw_payload_sha256":%q}`, sessionID, sequence, receivedAt, rawSize, emptyHash)
	return len(envelope) + base64.StdEncoding.EncodedLen(rawSize)
}

func TestRawDescriptorGrowthAndReplacementFailClosed(t *testing.T) {
	makeGuarded := func() string {
		root := t.TempDir()
		store, err := session.NewStore(root, session.WithSessionID("growth"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append([]byte(`{"map":{"matchid":"1","clock_time":-1}}`)); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return store.RawPath()
	}
	growthPath := makeGuarded()
	rehearsalRawDescriptorBoundHook = func() {
		file, _ := os.OpenFile(growthPath, os.O_WRONLY|os.O_APPEND, 0)
		_, _ = file.Write([]byte("x"))
		_ = file.Close()
	}
	grown := scanRehearsalRawPath(growthPath, "growth")
	rehearsalRawDescriptorBoundHook = nil
	if grown.Manifest.FailureCode != "raw_changed_during_read" {
		t.Fatalf("concurrent growth admitted: %#v", grown.Manifest)
	}
	replacementPath := makeGuarded()
	rehearsalRawDescriptorBoundHook = func() {
		_ = os.Rename(replacementPath, replacementPath+".old")
		_ = os.WriteFile(replacementPath, []byte("replacement"), 0o600)
	}
	replaced := scanRehearsalRawPath(replacementPath, "growth")
	rehearsalRawDescriptorBoundHook = nil
	if replaced.Manifest.FailureCode != "raw_changed_during_read" {
		t.Fatalf("path replacement admitted: %#v", replaced.Manifest)
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
	binding := testValidatedCaptureBinding(raw)
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

func TestExecutableProducerOwnsAndSealsPhysicalArtifacts(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"capture", "data/sessions", "evidence/canonical", "recordings"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	owner := RehearsalCaptureOwnerV1{
		SchemaVersion: "public_match_rehearsal_capture_owner.v1", CandidateCommit: strings.Repeat("a", 40), SessionID: "producer",
		RawRelativePath: "data/sessions/producer/raw.jsonl", HarnessRelativePath: "data/sessions/producer/harness-evidence.json", SealRelativePath: "data/sessions/producer/capture-seal.json",
	}
	ownerPayload, _ := canonical(owner)
	if err := writePrivate(filepath.Join(root, "capture/owner.json"), ownerPayload); err != nil {
		t.Fatal(err)
	}
	original := rehearsalRunComponents
	t.Cleanup(func() { rehearsalRunComponents = original })
	rehearsalRunComponents = func(_ context.Context, runRoot, _ string, runOwner RehearsalCaptureOwnerV1, _ string) (rehearsalComponentResult, error) {
		source := scanRehearsalRaw(testCompleteRawSession(t, runOwner.SessionID), runOwner.SessionID)
		store, err := session.NewStore(filepath.Join(runRoot, "data/sessions"), session.WithSessionID(runOwner.SessionID))
		if err != nil {
			return rehearsalComponentResult{}, err
		}
		for _, record := range source.Records {
			if _, err := store.Append(record.Raw); err != nil {
				return rehearsalComponentResult{}, err
			}
		}
		if err := store.Close(); err != nil {
			return rehearsalComponentResult{}, err
		}
		files := map[string][]byte{
			"recordings/rehearsal.mkv":                {0x1a, 0x45, 0xdf, 0xa3, 1},
			"evidence/raw-operator-input.jsonl":       []byte("operator-journal\n"),
			"evidence/canonical/live-validation.json": []byte("validation\n"),
		}
		for relative, payload := range files {
			if err := writePrivate(filepath.Join(runRoot, relative), payload); err != nil {
				return rehearsalComponentResult{}, err
			}
		}
		sample := Sample{Sequence: 3, ProcessRSSBytes: 100, OBSProcessRSSBytes: 200, TelemetryComplete: true}
		visibility := VisibilitySample{RawSequence: 3, Visibility: "hidden", TelemetryComplete: true}
		for relative, value := range map[string]any{"evidence/samples.jsonl": sample, "evidence/visibility.jsonl": visibility} {
			payload, _ := json.Marshal(value)
			payload = append(payload, '\n')
			if err := writePrivate(filepath.Join(runRoot, relative), payload); err != nil {
				return rehearsalComponentResult{}, err
			}
		}
		identity, err := readProcessCorrelationIdentity("/proc", os.Getpid())
		if err != nil {
			return rehearsalComponentResult{}, err
		}
		validation := attemptValidation{RawCount: 3, CursorSequence: 3, ObservationCommits: 3}
		for index, action := range []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide} {
			validation.OperatorTerminals = append(validation.OperatorTerminals, operatorTerminal{CommandID: fmt.Sprintf("command-%d", index), Action: action})
		}
		proof := recoveryProof{StartTicks: 11, CursorSHA256: strings.Repeat("1", 64), PolicySHA256: strings.Repeat("2", 64), AuditSHA256: strings.Repeat("3", 64), OperatorSHA256: strings.Repeat("4", 64), OverlaySHA256: strings.Repeat("5", 64)}
		second := proof
		second.StartTicks = 12
		return rehearsalComponentResult{ProductIdentity: identity, Validation: validation, RecoveryFirst: proof, RecoverySecond: second, ProductClean: true, OBSClean: true}, nil
	}
	receipt := produceRehearsalEvidence(context.Background(), root, filepath.Join("..", ".."), owner, "derived-match")
	if receipt.Outcome != "sealed" || receipt.FailureCode != "" || !validSHA256(receipt.HarnessSHA256) || !validSHA256(receipt.SealSHA256) {
		t.Fatalf("producer did not seal: %#v", receipt)
	}
	raw := scanRehearsalRawPath(filepath.Join(root, filepath.FromSlash(owner.RawRelativePath)), owner.SessionID)
	harnessPayload, err := readBoundedFile(filepath.Join(root, filepath.FromSlash(owner.HarnessRelativePath)), MaxRehearsalArtifactBytes)
	var harness RehearsalHarnessEvidenceV1
	if err != nil || strictCanonicalRehearsalJSON(harnessPayload, &harness) != nil || validateHarnessEvidence(root, harness, raw.Manifest) != "" {
		t.Fatalf("producer-owned harness rejected: %v", err)
	}
	if missing := produceRehearsalEvidence(context.Background(), t.TempDir(), filepath.Join("..", ".."), owner, ""); missing.FailureCode != "expected_identity_required_for_physical_producer" {
		t.Fatalf("missing physical producer output not typed: %#v", missing)
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
			binding := testValidatedCaptureBinding(raw)
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
	binding := testValidatedCaptureBinding(raw)
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
