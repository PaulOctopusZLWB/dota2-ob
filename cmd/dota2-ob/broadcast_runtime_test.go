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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
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

func TestBroadcastRuntimeRejectsCommandsUntilProjectionRestoreCompletes(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	const sessionID = "restore-command-gate"
	runtime, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: t.TempDir(), SessionID: sessionID, RawPath: filepath.Join(t.TempDir(), "raw.jsonl"),
		Lineage: testLineage(sessionID), Now: func() time.Time { return now }, ProjectionRestoreRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "restore-hide", SessionID: sessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: 0, PolicyTimeMS: now.UnixMilli()}
	if _, err := runtime.execute(context.Background(), command); err == nil {
		t.Fatal("command was admitted before projection restore completed")
	}
	if state, _ := runtime.operatorState(context.Background()); state.PolicyRevision != 0 {
		t.Fatalf("restore-time command mutated policy: %#v", state)
	}
	assertOverlayHealth(t, runtime, "projection_restoring", "hidden")
	if err := runtime.CompleteRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.execute(context.Background(), command); err != nil || result.Status != contracts.CommandAccepted {
		t.Fatalf("post-restore command=%#v err=%v", result, err)
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
	duplicateHide, err := runtime.execute(context.Background(), command("hide", contracts.ActionEmergencyHide, "", 4, 10_006))
	if err != nil || duplicateHide.Status != contracts.CommandAccepted {
		t.Fatalf("duplicate hide=%#v err=%v", duplicateHide, err)
	}
	overlay, _ = runtime.overlayState(context.Background())
	if overlay.Visibility != "visible" || overlay.Claim == nil {
		t.Fatalf("old duplicate command changed current publication: %#v", overlay)
	}
}

func TestBroadcastRuntimeRejectsSubstitutedLocalLineageArtifact(t *testing.T) {
	lineage := testLineage("substituted-lineage")
	lineage.Catalog.ContentSHA256 = strings.Repeat("f", 64)
	if _, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: t.TempDir(), SessionID: lineage.SessionID, RawPath: filepath.Join(t.TempDir(), "raw.jsonl"), Lineage: lineage,
	}); err == nil {
		t.Fatal("substituted local catalog content hash was accepted")
	}
}

func TestProductLineageSourceFingerprintsMatchCompiledIdentities(t *testing.T) {
	files := map[string]string{
		"../../internal/contracts/contracts.go":                      contractsSourceSHA256,
		"../../internal/capture/live_observation.go":                 liveMappingSourceSHA256,
		"product_selector.go":                                        productSelectorSourceSHA256,
		"../../internal/presentation/catalog.go":                     presentationCatalogSHA256,
		"main.go":                                                    productMainSourceSHA256,
		"broadcast_ports.go":                                         productPortsSourceSHA256,
		"broadcast_recovery.go":                                      productRecoverySourceSHA256,
		"../../internal/snapshotv2/reference/product_runtime.go.src": productRuntimeSourceSHA256,
		"broadcast_lineage.go":                                       productLineageSourceSHA256,
		"broadcast_live_only.go":                                     productLiveOnlySourceSHA256,
		"broadcast_recovery_v3.go":                                   productRecoveryV3SourceSHA256,
		"broadcast_runtime_v3.go":                                    productRuntimeV3SourceSHA256,
		"../../internal/session/highwater.go":                        sessionHighWaterSourceSHA256,
		"../../internal/session/live_projector.go":                   sessionFollowerSourceSHA256,
		"../../internal/insight/engine.go":                           insightEngineSourceSHA256,
		"../../internal/policy/engine.go":                            policyEngineSourceSHA256,
		"../../internal/policy/application.go":                       policyApplicationSourceSHA256,
	}
	for path, want := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		if got := hex.EncodeToString(digest[:]); got != want {
			t.Errorf("lineage source fingerprint for %s = %s want %s", path, got, want)
		}
	}
}

func TestProductLineageUsesAcceptedCaptureV3Identities(t *testing.T) {
	artifacts := expectedProductLineageArtifacts()
	wants := map[string]struct {
		got      contracts.PolicyArtifactIdentityV2
		version  string
		identity string
	}{
		"raw record schema":  {artifacts.rawRecordSchema, "raw_record.v3", session.RawRecordSchemaV3Identity},
		"raw record framing": {artifacts.rawRecordFraming, "raw_record_framing.v3", session.RawRecordFramingV3Identity},
		"raw payload schema": {artifacts.rawPayloadSchema, "dota2_gsi.v3", session.RawPayloadSchemaV3Identity},
	}
	for name, want := range wants {
		if want.got.Version != want.version || "sha256:"+want.got.ContentSHA256 != want.identity {
			t.Errorf("%s identity=%#v want version=%q identity=%q", name, want.got, want.version, want.identity)
		}
	}
	wantProjection := sourceArtifact("gsi_projection.v3+live_observation.v1", strings.TrimPrefix(session.GSIProjectionMappingV3Identity, "sha256:"), liveMappingSourceSHA256)
	if artifacts.projectionMapping != wantProjection {
		t.Errorf("projection mapping identity=%#v want=%#v", artifacts.projectionMapping, wantProjection)
	}
}

func TestSnapshotV2MigratedCanonicalBytesAndLineageRemainPinned(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	const sessionID = "v2-regression"
	lineage := testLineage(sessionID)
	runtime, err := newBroadcastRuntime(broadcastConfig{DataRoot: t.TempDir(), SessionID: sessionID, RawPath: filepath.Join(t.TempDir(), "raw.jsonl"), Lineage: lineage, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	observation := testObservation(sessionID, 1, now)
	candidate := testPresentationCandidate(observation)
	if err := runtime.commitCandidates(observation, []contracts.InsightCandidateV1{candidate}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	state := runtime.state()
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "v2-regression-approve", SessionID: sessionID, Action: contracts.ActionApprove, TargetCandidateID: candidate.CandidateID, ExpectedPolicyRevision: state.PolicyRevision, PolicyTimeMS: now.UnixMilli() + 1}
	result, err := runtime.execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	commitHashes := []string{}
	if err := runtime.visitAll(func(value commitlog.CommittedV2) error { commitHashes = append(commitHashes, value.Hash); return nil }); err != nil {
		t.Fatal(err)
	}
	candidateHash, _ := contracts.CanonicalSHA256(candidate)
	resultHash, _ := contracts.CanonicalSHA256(result)
	wantCommits := []string{"921b64e821b094b20337753137e1c28e39623b973173c92e10e6f90ee5aaaf8b", "8c0adf5a594ba2013d7115715c01f05a75c88e258e3369fdc4f7b3b36248f61a"}
	if lineage.MustContentID() != "8292e6a8deff171829f1a4a443826b54a6b8ffc38219d3d71e1e0f4daf4e4198" || candidate.CandidateID != "596c8f5153b9f8a9d71ab3da1ba19dff53bba2426b2cc378ad99258931f337fb" || candidateHash != "15732db121932440f9c4b6384c68695e5bb25e6f5f933ed9d49ecad78853efef" || resultHash != "7c22fbcced7e36cf6505c2adf7aae31ad53b6eaaf22e2b7d7711fa6d574d2f6e" || !slices.Equal(commitHashes, wantCommits) {
		t.Fatalf("accepted V2 bytes changed: lineage=%s candidate_id=%s candidate=%s result=%s commits=%v", lineage.MustContentID(), candidate.CandidateID, candidateHash, resultHash, commitHashes)
	}
}

func TestBroadcastRuntimeProjectionRecoveryBarrierFailsClosedAndRepublishes(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	const sessionID = "projection-health"
	runtime, err := newBroadcastRuntime(broadcastConfig{
		DataRoot: t.TempDir(), SessionID: sessionID, RawPath: filepath.Join(t.TempDir(), "raw.jsonl"),
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
	show := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "projection-show", SessionID: sessionID, Action: contracts.ActionShow, TargetCandidateID: candidate.CandidateID, ExpectedPolicyRevision: 1, PolicyTimeMS: now.UnixMilli()}
	if result, err := runtime.execute(context.Background(), show); err != nil || result.Status != contracts.CommandAccepted {
		t.Fatalf("show result=%#v err=%v", result, err)
	}

	type projectionControl interface {
		BeginRestore(context.Context) error
		CompleteRestore(context.Context) error
		ProjectionHealth(context.Context, session.RejectionTransition) error
	}
	control, ok := any(runtime).(projectionControl)
	if !ok {
		t.Fatal("broadcast runtime does not implement the accepted projection recovery boundary")
	}
	if err := control.BeginRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertOverlayHealth(t, runtime, "projection_restoring", "hidden")
	if err := control.ProjectionHealth(context.Background(), session.RejectionTransition{Active: true, Sequence: 2, Count: 1, Code: "gsi_projection_non_object", Reason: "top_level_non_object"}); err != nil {
		t.Fatal(err)
	}
	if err := control.CompleteRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertOverlayHealth(t, runtime, "gsi_projection_non_object", "hidden")
	if err := control.ProjectionHealth(context.Background(), session.RejectionTransition{Active: false, Sequence: 3, Count: 1, Code: "gsi_projection_non_object", Reason: "top_level_non_object"}); err != nil {
		t.Fatal(err)
	}
	assertOverlayHealth(t, runtime, "", "visible")
}

func assertOverlayHealth(t *testing.T, runtime *broadcastRuntime, health, visibility string) {
	t.Helper()
	overlay, err := runtime.overlayState(context.Background())
	if err != nil || overlay.Visibility != visibility || overlay.HealthCode != health {
		t.Fatalf("overlay=%#v err=%v want visibility=%q health=%q", overlay, err, visibility, health)
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
	wantHash := first.stateHash()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if state := restarted.state(); state.LastObservationSequence != 1 || restarted.stateHash() != wantHash {
		t.Fatalf("recovered observation state=%#v hash=%s want=%s", state, restarted.stateHash(), wantHash)
	}
	before := policyLogBytes(t, filepath.Join(root, sessionID))
	if err := restarted.applyObservation(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if after := policyLogBytes(t, filepath.Join(root, sessionID)); after != before {
		t.Fatalf("replayed raw observation appended policy bytes: before=%d after=%d", before, after)
	}
}

func TestObservationResolverStreamsRawSessionOnce(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	root := t.TempDir()
	const sessionID = "streaming-recovery"
	store, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	records := make([]*session.Record, 0, 2)
	for range 2 {
		record, appendErr := store.Append([]byte(`{}`))
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		records = append(records, record)
		now = now.Add(time.Millisecond)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(root, sessionID, "raw.jsonl")
	resolver := newObservationResolver(rawPath, sessionID, testLineage(sessionID))
	defer resolver.Close()
	commits := make([]contracts.PolicyCommitV2, 0, 2)
	for index, record := range records {
		observation, mapErr := capture.MapLiveObservationV1(record)
		if mapErr != nil {
			t.Fatal(mapErr)
		}
		liveHash, hashErr := contracts.CanonicalSHA256(observation)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		commits = append(commits, contracts.PolicyCommitV2{
			ObservationSequence: uint64(index + 1), ObservationEvidence: &observation.Evidence,
			RawRecordSHA256: observation.Evidence.RawPayloadSHA256, LiveObservationSHA256: liveHash,
			ResultingPolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli(),
		})
	}
	if _, err := resolver.resolve(commits[1]); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rawPath, rawPath+".moved"); err != nil {
		t.Fatal(err)
	}
	for _, commit := range []contracts.PolicyCommitV2{commits[0], commits[1]} {
		if _, err := resolver.resolve(commit); err != nil {
			t.Fatalf("indexed resolver rejected out-of-order or repeated observation: %v", err)
		}
	}
}

func TestObservationResolverUnlinksCachesAndAcceptsMaximumPersistedCapture(t *testing.T) {
	root := t.TempDir()
	const sessionID = "maximum-raw-record"
	store, err := session.NewStore(root, session.WithSessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	const maximumCaptureBody = 10 << 20
	prefix, suffix := `{"padding":"`, `"}`
	body := []byte(prefix + strings.Repeat("x", maximumCaptureBody-len(prefix)-len(suffix)) + suffix)
	record, err := store.Append(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	resolver := newObservationResolver(filepath.Join(root, sessionID, "raw.jsonl"), sessionID, testLineage(sessionID))
	defer resolver.Close()
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	liveHash, err := contracts.CanonicalSHA256(observation)
	if err != nil {
		t.Fatal(err)
	}
	commit := contracts.PolicyCommitV2{ObservationSequence: record.Sequence, ObservationEvidence: &observation.Evidence, RawRecordSHA256: observation.Evidence.RawPayloadSHA256, LiveObservationSHA256: liveHash, ResultingPolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli()}
	if _, err := resolver.resolve(commit); err != nil {
		t.Fatalf("maximum persisted capture was not recoverable: %v", err)
	}
	if resolver.indexPath != "" || resolver.dataPath != "" {
		t.Fatalf("recovery caches remain named and can leak across SIGKILL: index=%q data=%q", resolver.indexPath, resolver.dataPath)
	}
}

func TestBroadcastRuntimeRecoversDeltaObservationAfterValidCheckpoint(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	root := t.TempDir()
	const sessionID = "checkpoint-delta"
	rawStore, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	firstRecord, err := rawStore.Append([]byte(`{"items":{"radiant":{"player0":{"slot0":{"name":"item_branches"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	secondRecord, err := rawStore.Append([]byte(`{"items":{"radiant":{"player0":{"slot0":{"name":"item_blink"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := rawStore.Close(); err != nil {
		t.Fatal(err)
	}
	lineage := testLineage(sessionID)
	config := broadcastConfig{DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"), Lineage: lineage, Now: func() time.Time { return now }}
	runtime, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	firstObservation, err := capture.MapLiveObservationV1(firstRecord)
	if err != nil {
		t.Fatal(err)
	}
	firstCandidates := insight.Evaluate(insight.Input{Observation: firstObservation, Lineage: &lineage, PolicyTimeMS: firstObservation.Evidence.ReceiveTime.UnixMilli()}, insight.DefaultConfig())
	firstCommit, err := runtime.evaluateObservation(firstObservation, firstCandidates, firstObservation.Evidence.ReceiveTime.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	state := runtime.state()
	commitHash, _ := contracts.CanonicalSHA256(firstCommit)
	checkpoint := contracts.PolicyCheckpointV2{
		SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: lineage.MustContentID(), LineageManifestSHA256: lineage.MustContentID(),
		SessionID: sessionID, CommitSequence: firstCommit.CommitSequence, ReferencedCommitSHA256: commitHash,
		LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS,
		StateHash: runtime.stateHash(), CreatedTimeMS: state.LastPolicyTimeMS,
		Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins,
		EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults,
		CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: state.CandidateTombstones,
	}
	if err := runtime.writeCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	secondObservation, err := capture.MapLiveObservationV1(secondRecord)
	if err != nil {
		t.Fatal(err)
	}
	secondCandidates := insight.Evaluate(insight.Input{Observation: secondObservation, Previous: &firstObservation, Lineage: &lineage, PolicyTimeMS: secondObservation.Evidence.ReceiveTime.UnixMilli()}, insight.DefaultConfig())
	if err := runtime.commitCandidates(secondObservation, secondCandidates, secondObservation.Evidence.ReceiveTime.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	wantHash := runtime.stateHash()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatalf("valid checkpoint continuation failed recovery: %v", err)
	}
	defer restarted.Close()
	if restarted.stateHash() != wantHash {
		t.Fatalf("checkpoint continuation hash=%s want=%s", restarted.stateHash(), wantHash)
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
	local := expectedProductLineageArtifacts()
	return contracts.PolicyLineageManifestV2{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: sessionID,
		RawRecordSchema: local.rawRecordSchema, RawRecordFraming: local.rawRecordFraming,
		RawPayloadSchema: local.rawPayloadSchema, LiveObservationSchema: local.liveObservationSchema,
		ProjectionMapping: local.projectionMapping,
		TournamentScopeID: artifact("live_only_scope.v1").ContentSHA256, TournamentScopeSHA256: artifact("live_only_scope.v1").ContentSHA256,
		HistoricalSnapshotID: artifact("history_unavailable.v1").ContentSHA256, HistoricalSnapshotSHA256: artifact("history_unavailable.v1").ContentSHA256,
		EligibleBaselineSHA256: []string{}, Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(config),
		Catalog: local.catalog, Terminology: local.terminology,
		LocalizationParameterMapping: local.localizationMapping, EngineBuild: local.engineBuild,
	}
}

func policyLogBytes(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".pcl2") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		total += info.Size()
	}
	return total
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
