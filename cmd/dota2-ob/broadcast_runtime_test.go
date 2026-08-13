package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
		"../../internal/session/store.go":            sessionStoreSourceSHA256,
		"../../internal/session/live_projector.go":   liveProjectorSourceSHA256,
		"../../internal/gsi/server.go":               gsiServerSourceSHA256,
		"../../internal/contracts/contracts.go":      contractsSourceSHA256,
		"../../internal/capture/live_observation.go": liveMappingSourceSHA256,
		"../../internal/presentation/catalog.go":     presentationCatalogSHA256,
		"main.go":                                    productMainSourceSHA256,
		"broadcast_ports.go":                         productPortsSourceSHA256,
		"broadcast_recovery.go":                      productRecoverySourceSHA256,
		"broadcast_runtime.go":                       productRuntimeSourceSHA256,
		"broadcast_lineage.go":                       productLineageSourceSHA256,
		"../../internal/insight/engine.go":           insightEngineSourceSHA256,
		"../../internal/policy/engine.go":            policyEngineSourceSHA256,
		"../../internal/policy/application.go":       policyApplicationSourceSHA256,
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

func TestObservationResolverUnlinksIndexAndAcceptsMaximumPersistedCapture(t *testing.T) {
	const sessionID = "maximum-raw-record"
	if root := os.Getenv("DOTA2_OB_MAXIMUM_RECOVERY_ROOT"); root != "" {
		runtime, err := newBroadcastRuntime(broadcastConfig{DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"), Lineage: testLineage(sessionID)})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		if !raceEnabled && usage.Maxrss > 192<<10 {
			t.Fatalf("isolated maximum-record recovery RSS=%d KiB exceeds 192 MiB", usage.Maxrss)
		}
		return
	}
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	const maximumCaptureBody = 10 << 20
	prefix, suffix := `{"padding":"`, `"}`
	body := []byte(prefix + strings.Repeat("<", maximumCaptureBody-len(prefix)-len(suffix)) + suffix)
	record, err := store.Append(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, sessionID, "raw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= maximumCaptureBody || info.Size() > maximumPersistedRecordBytes {
		t.Fatalf("persisted maximum capture size=%d outside (%d,%d]", info.Size(), maximumCaptureBody, maximumPersistedRecordBytes)
	}
	rawPath := filepath.Join(root, sessionID, "raw.jsonl")
	lineage := testLineage(sessionID)
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newBroadcastRuntime(broadcastConfig{DataRoot: root, SessionID: sessionID, RawPath: rawPath, Lineage: lineage})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.commitCandidates(observation, insight.Evaluate(insight.Input{Observation: observation, Lineage: &lineage, PolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli()}, insight.DefaultConfig()), observation.Evidence.ReceiveTime.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	wantHash := runtime.app.StateHash()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestObservationResolverUnlinksIndexAndAcceptsMaximumPersistedCapture$", "-test.count=1")
	child.Env = append(os.Environ(), "DOTA2_OB_MAXIMUM_RECOVERY_ROOT="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated maximum-record recovery failed: %v\n%s", err, output)
	}
	restarted, err := newBroadcastRuntime(broadcastConfig{DataRoot: root, SessionID: sessionID, RawPath: rawPath, Lineage: lineage})
	if err != nil {
		t.Fatalf("maximum persisted capture failed real restart recovery: %v", err)
	}
	defer restarted.Close()
	if restarted.app.StateHash() != wantHash {
		t.Fatalf("maximum persisted capture recovered hash=%s want=%s", restarted.app.StateHash(), wantHash)
	}
	resolver := newObservationResolver(rawPath, sessionID, lineage)
	defer resolver.Close()
	resolved, err := resolver.readCommittedRecord(record.Sequence)
	if err != nil || resolved.Sequence != record.Sequence {
		t.Fatalf("maximum persisted capture was not recoverable: record=%#v err=%v", resolved, err)
	}
	if resolver.indexPath != "" {
		t.Fatalf("recovery index remains named and can leak across SIGKILL: %q", resolver.indexPath)
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
	firstLiveHash, _ := contracts.CanonicalSHA256(firstObservation)
	firstCommit, err := runtime.app.EvaluateObservation(firstObservation.Evidence.Sequence, firstObservation.Evidence.RawPayloadSHA256, firstLiveHash, firstObservation.Evidence, firstCandidates, firstObservation.Evidence.ReceiveTime.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	state := runtime.app.State()
	commitHash, _ := contracts.CanonicalSHA256(firstCommit)
	checkpoint := contracts.PolicyCheckpointV2{
		SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: lineage.MustContentID(), LineageManifestSHA256: lineage.MustContentID(),
		SessionID: sessionID, CommitSequence: firstCommit.CommitSequence, ReferencedCommitSHA256: commitHash,
		LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS,
		StateHash: runtime.app.StateHash(), CreatedTimeMS: state.LastPolicyTimeMS,
		Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins,
		EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults,
		CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: state.CandidateTombstones,
	}
	if err := runtime.store.WriteCheckpoint(checkpoint); err != nil {
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
	wantHash := runtime.app.StateHash()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newBroadcastRuntime(config)
	if err != nil {
		t.Fatalf("valid checkpoint continuation failed recovery: %v", err)
	}
	defer restarted.Close()
	if restarted.app.StateHash() != wantHash {
		t.Fatalf("checkpoint continuation hash=%s want=%s", restarted.app.StateHash(), wantHash)
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
