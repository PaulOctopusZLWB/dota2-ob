package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestBroadcastRuntimeStartsWithAuthoritativeEmptyOperatorAndHiddenOverlay(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	sessionID := "product-session"
	runtime, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: t.TempDir(), SessionID: sessionID,
		RawPath: filepath.Join(t.TempDir(), "raw.jsonl"),
		Lineage: testLineage(sessionID), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	operatorState, err := runtime.operatorState(context.Background())
	if err != nil || operatorState.Validate() != nil {
		t.Fatalf("operator state=%#v err=%v", operatorState, err)
	}
	if operatorState.PolicyRevision != 0 || len(operatorState.Previews) != 0 || operatorState.EmergencyHidden {
		t.Fatalf("operator state is not the empty policy state: %#v", operatorState)
	}
	overlay, err := runtime.overlayState(context.Background())
	if err != nil || overlay.Validate() != nil || overlay.Visibility != "hidden" || overlay.Claim != nil {
		t.Fatalf("overlay=%#v err=%v", overlay, err)
	}
}

func TestBroadcastRuntimeCommandsMutateOnlyPolicyAndPublishCommittedOverlay(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	sessionID := "product-session"
	runtime, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: t.TempDir(), SessionID: sessionID,
		RawPath: filepath.Join(t.TempDir(), "raw.jsonl"),
		Lineage: testLineage(sessionID), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	observation := testObservation(sessionID, 1, now)
	candidate := testPresentationCandidate(observation)
	if err := runtime.commitCandidates(observation, []contracts.InsightCandidateV1{candidate}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	operatorState, err := runtime.operatorState(context.Background())
	if err != nil || operatorState.PolicyRevision != 1 || len(operatorState.Previews) != 1 || operatorState.Previews[0].Claim.Title != "天辉拿下肉山" {
		t.Fatalf("queued operator state=%#v err=%v", operatorState, err)
	}

	command := func(id, action, target string, revision uint64, at int64) contracts.OperatorCommandV1 {
		return contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: id, SessionID: sessionID, Action: action, TargetCandidateID: target, ExpectedPolicyRevision: revision, PolicyTimeMS: at}
	}
	approved, err := runtime.execute(context.Background(), command("approve", contracts.ActionApprove, candidate.CandidateID, 1, 10_001))
	if err != nil || approved.Status != contracts.CommandAccepted || approved.ResultingRevision != 2 {
		t.Fatalf("approve=%#v err=%v", approved, err)
	}
	overlay, err := runtime.overlayState(context.Background())
	if err != nil || overlay.Visibility != "visible" || overlay.Claim == nil || overlay.Claim.Title != "天辉拿下肉山" {
		t.Fatalf("published overlay=%#v err=%v", overlay, err)
	}
	activeState, err := runtime.operatorState(context.Background())
	if err != nil || len(activeState.Previews) != 1 || activeState.Previews[0].CandidateID != candidate.CandidateID {
		t.Fatalf("active candidate is not operator-addressable: state=%#v err=%v", activeState, err)
	}

	rejected, err := runtime.execute(context.Background(), command("conflict", contracts.ActionReject, candidate.CandidateID, 1, 10_002))
	if err != nil || rejected.Status != contracts.CommandRejected || rejected.Reason != "stale_revision" {
		t.Fatalf("revision conflict=%#v err=%v", rejected, err)
	}
	duplicate, err := runtime.execute(context.Background(), command("conflict", contracts.ActionReject, candidate.CandidateID, 1, 10_002))
	want, _ := contracts.MarshalCanonical(rejected)
	got, _ := contracts.MarshalCanonical(duplicate)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("duplicate=%#v err=%v", duplicate, err)
	}
	tooLong := command("bounded", contracts.ActionShow, strings.Repeat("x", contracts.MaxPolicyIdentifierBytes+1), 2, 10_003)
	if _, err := runtime.execute(context.Background(), tooLong); !errors.Is(err, contracts.ErrPolicyIdentifierLimit) {
		t.Fatalf("identifier admission error=%v", err)
	}
	stillVisible, _ := runtime.overlayState(context.Background())
	if stillVisible.Visibility != "visible" || stillVisible.Claim == nil {
		t.Fatalf("stateless admission rejection hid committed output: %#v", stillVisible)
	}

	pinned, err := runtime.execute(context.Background(), command("pin", contracts.ActionPin, candidate.CandidateID, 2, 10_004))
	if err != nil || pinned.Status != contracts.CommandAccepted || pinned.ResultingRevision != 3 {
		t.Fatalf("pin=%#v err=%v", pinned, err)
	}
	unpinned, err := runtime.execute(context.Background(), command("unpin", contracts.ActionUnpin, candidate.CandidateID, 3, 10_005))
	if err != nil || unpinned.Status != contracts.CommandAccepted || unpinned.ResultingRevision != 4 {
		t.Fatalf("unpin=%#v err=%v", unpinned, err)
	}

	hidden, err := runtime.execute(context.Background(), command("hide", contracts.ActionEmergencyHide, "", 4, 10_006))
	if err != nil || hidden.Status != contracts.CommandAccepted {
		t.Fatalf("hide=%#v err=%v", hidden, err)
	}
	overlay, _ = runtime.overlayState(context.Background())
	if overlay.Visibility != "hidden" || overlay.Claim != nil || overlay.HealthCode != "emergency_hide" {
		t.Fatalf("emergency overlay=%#v", overlay)
	}
	cleared, err := runtime.execute(context.Background(), command("clear", contracts.ActionClearEmergencyHide, "", 5, 10_007))
	if err != nil || cleared.Status != contracts.CommandAccepted {
		t.Fatalf("clear=%#v err=%v", cleared, err)
	}
	overlay, _ = runtime.overlayState(context.Background())
	if overlay.Visibility != "visible" || overlay.Claim == nil {
		t.Fatalf("cleared overlay=%#v", overlay)
	}
}

func TestBroadcastRuntimeKeepsOperatorControlForUnrenderableCandidate(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	sessionID := "unrenderable-session"
	runtime, err := newBroadcastRuntime(broadcastConfig{DataRoot: t.TempDir(), SessionID: sessionID, RawPath: filepath.Join(t.TempDir(), "raw.jsonl"), Lineage: testLineage(sessionID), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	observation := testObservation(sessionID, 1, now)
	candidate := testPresentationCandidate(observation)
	candidate.Parameters = nil
	candidate.CandidateID = ""
	candidate.CandidateID, _ = contracts.InsightCandidateContentID(candidate)
	if err := runtime.commitCandidates(observation, []contracts.InsightCandidateV1{candidate}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.operatorState(context.Background())
	if err != nil || len(state.Previews) != 1 || state.Previews[0].Claim.Title != "分析暂不可展示" {
		t.Fatalf("operator fallback state=%#v err=%v", state, err)
	}
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "unsafe-show", SessionID: sessionID, Action: contracts.ActionShow, TargetCandidateID: candidate.CandidateID, ExpectedPolicyRevision: 1, PolicyTimeMS: now.UnixMilli()}
	if result, err := runtime.execute(context.Background(), command); err != nil || result.Status != contracts.CommandAccepted {
		t.Fatalf("show result=%#v err=%v", result, err)
	}
	overlay, _ := runtime.overlayState(context.Background())
	if overlay.Visibility != "hidden" || overlay.Claim != nil || overlay.HealthCode != "presentation_invalid" {
		t.Fatalf("unsafe candidate reached overlay: %#v", overlay)
	}
}

func TestBroadcastRuntimeRecoversCommittedPolicyAndExactDuplicate(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	sessionID := "restart-session"
	root := t.TempDir()
	config := broadcastConfig{
		DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"),
		Lineage: testLineage(sessionID), Now: func() time.Time { return now },
	}
	first, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	command := contracts.OperatorCommandV1{
		SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "restart-hide", SessionID: sessionID,
		Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: 0, PolicyTimeMS: now.UnixMilli(),
	}
	want, err := first.execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	state, err := restarted.operatorState(context.Background())
	if err != nil || state.PolicyRevision != 1 || !state.EmergencyHidden {
		t.Fatalf("recovered state=%#v err=%v", state, err)
	}
	duplicate, err := restarted.execute(context.Background(), command)
	wantBytes, _ := contracts.MarshalCanonical(want)
	gotBytes, _ := contracts.MarshalCanonical(duplicate)
	if err != nil || !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("recovered duplicate=%#v err=%v", duplicate, err)
	}
}

func TestBroadcastRuntimeRecoversLineageBoundObservationFromRawSession(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	sessionID := "observation-restart"
	root := t.TempDir()
	rawStore, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	record, err := rawStore.Append([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := rawStore.Close(); err != nil {
		t.Fatal(err)
	}
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	lineage := testLineage(sessionID)
	candidates := insight.Evaluate(insight.Input{Observation: observation, Lineage: &lineage, PolicyTimeMS: now.UnixMilli()}, insight.DefaultConfig())
	config := broadcastConfig{DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"), Lineage: lineage, Now: func() time.Time { return now }}
	first, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.commitCandidates(observation, candidates, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	wantHash := first.app.StateHash()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if state := restarted.app.State(); state.LastObservationSequence != 1 || restarted.app.StateHash() != wantHash {
		t.Fatalf("recovered observation state=%#v hash=%s want=%s", state, restarted.app.StateHash(), wantHash)
	}
}

func TestBroadcastRuntimeFailsClosedOnCommitFailureAndStaleOutput(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	sessionID := "failure-session"
	failSync := false
	runtime, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: t.TempDir(), SessionID: sessionID, RawPath: filepath.Join(t.TempDir(), "raw.jsonl"),
		Lineage: testLineage(sessionID), Now: func() time.Time { return now },
		StoreOptions: []commitlog.V2Option{commitlog.WithV2Hooks(commitlog.Hooks{Interrupt: func(stage string) error {
			if failSync && stage == "before_sync" {
				return errors.New("injected downstream sync failure")
			}
			return nil
		}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	observation := testObservation(sessionID, 1, now)
	candidate := testPresentationCandidate(observation)
	if err := runtime.commitCandidates(observation, []contracts.InsightCandidateV1{candidate}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	approved := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "approve", SessionID: sessionID, Action: contracts.ActionApprove, TargetCandidateID: candidate.CandidateID, ExpectedPolicyRevision: 1, PolicyTimeMS: now.UnixMilli()}
	if _, err := runtime.execute(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	now = now.Add(overlayFreshness)
	stale, err := runtime.overlayState(context.Background())
	if err != nil || stale.Visibility != "hidden" || stale.Claim != nil || stale.HealthCode != "stale_input" {
		t.Fatalf("stale overlay=%#v err=%v", stale, err)
	}

	failSync = true
	hide := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "failed-hide", SessionID: sessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: 2, PolicyTimeMS: now.UnixMilli()}
	if _, err := runtime.execute(context.Background(), hide); err == nil {
		t.Fatal("downstream commit failure was acknowledged")
	}
	state, _ := runtime.operatorState(context.Background())
	closed, _ := runtime.overlayState(context.Background())
	if state.PolicyRevision != 2 || state.EmergencyHidden || closed.Visibility != "hidden" || closed.HealthCode != "policy_commit_failed" {
		t.Fatalf("failed commit mutated state=%#v overlay=%#v", state, closed)
	}
}

func testObservation(sessionID string, sequence uint64, received time.Time) contracts.LiveObservationV1 {
	raw := json.RawMessage(`{}`)
	observation, err := capture.MapLiveObservationV1(&session.Record{
		SchemaVersion: 2, SessionID: sessionID, Sequence: sequence, ReceivedAt: received, Source: "gsi",
		Payload: map[string]any{}, Raw: raw,
	})
	if err != nil {
		panic(err)
	}
	return observation
}

func testPresentationCandidate(observation contracts.LiveObservationV1) contracts.InsightCandidateV1 {
	parameter := func(name, kind, value string) contracts.TypedParameterV1 {
		result := contracts.TypedParameterV1{Name: name, Type: kind}
		if kind == "decimal" {
			decimal := contracts.Decimal(value)
			result.DecimalValue = &decimal
		} else {
			result.StringValue = &value
		}
		return result
	}
	candidate := contracts.InsightCandidateV1{
		SchemaVersion: contracts.InsightCandidateSchemaV1, SessionID: observation.Evidence.SessionID,
		RuleVersion: insight.ObjectiveRule, ConfigVersion: insight.DefaultConfig().Version,
		LocalizationKey: "insight.objective_exchange",
		Parameters:      []contracts.TypedParameterV1{parameter("team", "team", "radiant"), parameter("objective", "objective", "roshan"), parameter("net_worth_delta", "decimal", "2200")},
		Evidence:        []contracts.EvidenceRefV1{observation.Evidence}, Confidence: "observed", SampleSize: 12, Priority: 90,
		CreatedTimeMS: observation.Evidence.ReceiveTime.UnixMilli(), ExpiryTimeMS: observation.Evidence.ReceiveTime.Add(30 * time.Second).UnixMilli(), Availability: "available",
	}
	candidate.CandidateID, _ = contracts.InsightCandidateContentID(candidate)
	return candidate
}

func testLineage(sessionID string) contracts.PolicyLineageManifestV2 {
	artifact := func(version string) contracts.PolicyArtifactIdentityV2 {
		digest := sha256.Sum256([]byte(version))
		return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: hex.EncodeToString(digest[:])}
	}
	config := insight.DefaultConfig()
	return contracts.PolicyLineageManifestV2{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: sessionID,
		RawRecordSchema: artifact("session_record.v2"), RawRecordFraming: artifact("jsonl.v1"),
		RawPayloadSchema: artifact("dota2_gsi.v1"), LiveObservationSchema: artifact(contracts.LiveObservationSchemaV1),
		ProjectionMapping: artifact("gsi_normalized.v1"),
		TournamentScopeID: artifact("live_only_scope.v1").ContentSHA256, TournamentScopeSHA256: artifact("live_only_scope.v1").ContentSHA256,
		HistoricalSnapshotID: artifact("history_unavailable.v1").ContentSHA256, HistoricalSnapshotSHA256: artifact("history_unavailable.v1").ContentSHA256,
		EligibleBaselineSHA256: []string{}, Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(config),
		Catalog: artifact(presentation.CatalogVersion()), Terminology: artifact(presentation.TerminologyVersion()),
		LocalizationParameterMapping: artifact("localization_parameter_mapping.v1"), EngineBuild: artifact("dota2-ob.test"),
	}
}

func writeTestLineage(t *testing.T, path string, lineage contracts.PolicyLineageManifestV2) {
	t.Helper()
	payload, err := contracts.MarshalCanonical(lineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
