package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestLiveOnlyArtifactLoaderRequiresCanonicalExactProductCoherence(t *testing.T) {
	artifacts := testLiveOnlyArtifacts("live-loader")
	dir := t.TempDir()
	historyPath := writeCanonicalTestContract(t, dir, "history.json", artifacts.History)
	lineagePath := writeCanonicalTestContract(t, dir, "lineage.json", artifacts.Lineage)
	releasePath := writeCanonicalTestContract(t, dir, "release.json", artifacts.Release)
	got, err := loadLiveOnlyPolicyArtifacts(historyPath, lineagePath, releasePath, "live-loader")
	if err != nil || got.Lineage.MustContentID() != artifacts.Lineage.MustContentID() {
		t.Fatalf("load exact artifacts: %#v %v", got, err)
	}
	if err := os.WriteFile(historyPath, append([]byte(" "), mustCanonicalTest(t, artifacts.History)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLiveOnlyPolicyArtifacts(historyPath, lineagePath, releasePath, "live-loader"); err == nil {
		t.Fatal("noncanonical binding accepted")
	}
	writeCanonicalTestContract(t, dir, "history.json", artifacts.History)
	substituted := artifacts.Release
	substituted.SourceCommit = strings.Repeat("c", 40)
	writeCanonicalTestContract(t, dir, "release.json", substituted)
	if _, err := loadLiveOnlyPolicyArtifacts(historyPath, lineagePath, releasePath, "live-loader"); err == nil {
		t.Fatal("unaccepted live-only scope identity accepted")
	}
}

func TestBroadcastRuntimeV3ProducesOnlyDurableV3TerminalCommit(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	root := t.TempDir()
	const sessionID = "live-runtime"
	runtime, err := newBroadcastRuntimeV3(broadcastConfigV3{
		DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"),
		Artifacts: testLiveOnlyArtifacts(sessionID), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := testObservation(sessionID, 1, now)
	candidate := testPresentationCandidate(observation)
	if err := runtime.commitCandidates(observation, []contracts.InsightCandidateV1{candidate}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	commits := 0
	if err := runtime.store.VisitAll(func(commitlog.CommittedV3) error { commits++; return nil }); err != nil || commits != 1 {
		t.Fatalf("durable commits=%d err=%v", commits, err)
	}
	if _, err := runtime.execute(context.Background(), contracts.OperatorCommandV1{
		SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "show-live", SessionID: sessionID,
		Action: contracts.ActionShow, TargetCandidateID: candidate.CandidateID,
		ExpectedPolicyRevision: runtime.app.State().PolicyRevision, PolicyTimeMS: now.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, sessionID))
	if err != nil {
		t.Fatal(err)
	}
	var v2, v3 int
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pcl2") {
			v2++
		}
		if strings.HasSuffix(entry.Name(), ".pcl3") {
			v3++
		}
	}
	if v2 != 0 || v3 != 1 {
		t.Fatalf("policy frame versions: v2=%d v3=%d", v2, v3)
	}
}

func TestBroadcastRuntimeV3RecoversAndReturnsExactDurableDuplicate(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	root := t.TempDir()
	const sessionID = "live-restart"
	raw, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	record, err := raw.Append([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts := testLiveOnlyArtifacts(sessionID)
	config := broadcastConfigV3{DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"), Artifacts: artifacts, Now: func() time.Time { return now }}
	runtime, err := newBroadcastRuntimeV3(config)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	candidates := insight.EvaluateLiveOnly(insight.LiveOnlyInput{Observation: observation, History: artifacts.History, Lineage: artifacts.Lineage, PolicyTimeMS: now.UnixMilli()}, insight.DefaultConfig())
	if err := runtime.commitCandidates(observation, candidates, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "restart-hide", SessionID: sessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: runtime.app.State().PolicyRevision, PolicyTimeMS: now.UnixMilli() + 1}
	original, err := runtime.execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := runtime.app.StateHash()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newBroadcastRuntimeV3(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.app.StateHash() != wantHash {
		t.Fatalf("recovered state hash=%s want=%s", restarted.app.StateHash(), wantHash)
	}
	duplicate, err := restarted.execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := contracts.MarshalCanonical(original)
	b, _ := contracts.MarshalCanonical(duplicate)
	if !bytes.Equal(a, b) {
		t.Fatal("recovered duplicate changed")
	}
	commits := 0
	if err := restarted.store.VisitAll(func(commitlog.CommittedV3) error { commits++; return nil }); err != nil || commits != 2 {
		t.Fatalf("duplicate appended: commits=%d err=%v", commits, err)
	}
}

func TestBroadcastRuntimeV3DoesNotRewindCausalBaselineAndRestartsAtNewest(t *testing.T) {
	root := t.TempDir()
	const sessionID = "live-causal-baseline"
	now := time.UnixMilli(20_000).UTC()
	raw, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]contracts.LiveObservationV1, 3)
	for i := range observations {
		now = now.Add(time.Millisecond)
		record, appendErr := raw.Append([]byte(`{}`))
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		observations[i], err = capture.MapLiveObservationV1(record)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts := testLiveOnlyArtifacts(sessionID)
	config := broadcastConfigV3{DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"), Artifacts: artifacts, Now: func() time.Time { return now }}
	r, err := newBroadcastRuntimeV3(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range observations[:2] {
		if err := r.applyObservation(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	for _, stale := range []contracts.LiveObservationV1{observations[1], observations[0], observations[0]} {
		if err := r.applyObservation(context.Background(), stale); err != nil {
			t.Fatal(err)
		}
		if r.previous == nil || r.previous.Evidence.Sequence != 2 {
			t.Fatalf("stale delivery rewound baseline: %#v", r.previous)
		}
	}
	if err := r.applyObservation(context.Background(), observations[2]); err != nil {
		t.Fatal(err)
	}
	if r.previous == nil || r.previous.Evidence.Sequence != 3 || r.app.State().LastObservationSequence != 3 {
		t.Fatalf("next-newer observation did not advance: %#v", r.previous)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newBroadcastRuntimeV3(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.previous == nil || restarted.previous.Evidence.Sequence != 3 {
		t.Fatalf("restart baseline=%#v", restarted.previous)
	}
}

func TestBroadcastRuntimeV3QueueSaturationLatchesHealthHideAndRecovers(t *testing.T) {
	fixture := readM4Schedule(t)
	root := t.TempDir()
	const sessionID = "live-saturation"
	now := time.UnixMilli(30_000).UTC()
	raw, err := session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(fixture.Updates[2].Body, &body); err != nil {
		t.Fatal(err)
	}
	records := make([]*session.Record, 0, 67)
	for i := 0; i < 67; i++ {
		players := body["player"].(map[string]any)["team2"].(map[string]any)
		players["player0"].(map[string]any)["net_worth"] = float64(10_000 + i*i)
		payload, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		now = now.Add(time.Millisecond)
		record, appendErr := raw.Append(payload)
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		records = append(records, record)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts := testLiveOnlyArtifacts(sessionID)
	policyConfig := policy.DefaultConfig()
	policyConfig.CooldownMS = 1
	config := broadcastConfigV3{DataRoot: root, SessionID: sessionID, RawPath: filepath.Join(root, sessionID, "raw.jsonl"), Artifacts: artifacts, Now: func() time.Time { return now }, PolicyConfig: &policyConfig}
	r, err := newBroadcastRuntimeV3(config)
	if err != nil {
		t.Fatal(err)
	}
	for i, record := range records {
		observation, mapErr := capture.MapLiveObservationV1(record)
		if mapErr != nil {
			t.Fatal(mapErr)
		}
		now = record.ReceivedAt
		if err := r.applyObservation(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			state := r.app.State()
			if len(state.Preview) != 1 {
				t.Fatalf("initial preview=%d", len(state.Preview))
			}
			command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "saturation-visible", SessionID: sessionID, Action: contracts.ActionApprove, TargetCandidateID: state.Preview[0].CandidateID, ExpectedPolicyRevision: state.PolicyRevision, PolicyTimeMS: now.UnixMilli()}
			if _, err := r.execute(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			visible, _ := r.overlayState(context.Background())
			if visible.Visibility != "visible" {
				t.Fatalf("pre-saturation overlay=%#v", visible)
			}
		}
	}
	if !r.candidateSaturated {
		last := ""
		_ = r.store.VisitAll(func(value commitlog.CommittedV3) error {
			if len(value.Commit.AuditEvents) > 0 {
				last = value.Commit.AuditEvents[0].Reason
			}
			return nil
		})
		t.Fatalf("queue saturation did not latch: previews=%d last_audit=%s", len(r.app.State().Preview), last)
	}
	hidden, _ := r.overlayState(context.Background())
	if hidden.Visibility != "hidden" || hidden.HealthCode != "candidate_queue_saturated" || hidden.PublicationTimeMS-records[66].ReceivedAt.UnixMilli() > 2_000 {
		t.Fatalf("saturation hide=%#v", hidden)
	}
	if _, err := r.execute(context.Background(), contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "saturation-command", SessionID: sessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: r.app.State().PolicyRevision, PolicyTimeMS: now.UnixMilli()}); err == nil {
		t.Fatal("saturated runtime accepted command")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newBroadcastRuntimeV3(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, _ := restarted.overlayState(context.Background())
	if !restarted.candidateSaturated || recovered.Visibility != "hidden" || recovered.HealthCode != "candidate_queue_saturated" {
		t.Fatalf("saturation recovery=%#v", recovered)
	}
}

func testLiveOnlyArtifacts(sessionID string) liveOnlyPolicyArtifacts {
	history := contracts.HistoryAvailabilityBindingV1{
		SchemaVersion: contracts.HistoryAvailabilityBindingSchemaV1, Mode: contracts.HistoryModeNoGo,
		TerminalOutcome: contracts.HistoricalNoGoOutcome, CodeFoundationCommit: contracts.AcceptedHistoryCodeCommit,
		EvidenceCommit: contracts.AcceptedHistoryEvidenceCommit, EvidenceIndexSHA256: contracts.AcceptedEvidenceIndexSHA256,
		ArtifactTreeSHA256: contracts.AcceptedArtifactTreeSHA256, ReplayGateAuditSHA256: contracts.AcceptedReplayGateAuditSHA256,
		SourceProvenanceSHA256: contracts.AcceptedSourceProvenanceSHA256, DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(),
		TournamentScopeID: contracts.AcceptedTournamentScopeID, TournamentScopeSHA256: contracts.AcceptedTournamentScopeSHA256,
		Cutoff: "2026-08-12T00:00:00Z", Trailing90Start: "2026-05-14T00:00:00Z", Trailing180Start: "2026-02-13T00:00:00Z",
		PatchID: "60", DotaPatch: "7.41",
	}
	local := expectedProductLineageArtifactsV3()
	bindingID := history.MustContentID()
	lineage := contracts.PolicyLineageManifestV3{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: sessionID,
		RawRecordSchema: local.rawRecordSchema, RawRecordFraming: local.rawRecordFraming, RawPayloadSchema: local.rawPayloadSchema,
		LiveObservationSchema: local.liveObservationSchema, ProjectionMapping: local.projectionMapping,
		TournamentScopeID: strings.Repeat("a", 64), TournamentScopeSHA256: strings.Repeat("a", 64),
		HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID,
		Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(insight.DefaultConfig()), Catalog: local.catalog,
		Terminology: local.terminology, LocalizationParameterMapping: local.localizationMapping, EngineBuild: local.engineBuild,
	}
	lineageID := lineage.MustContentID()
	release := contracts.LiveOnlyReleaseBindingV1{
		SchemaVersion: contracts.LiveOnlyReleaseBindingSchemaV1, SourceCommit: acceptedLiveOnlyScopeCommit,
		LineageSchema: contracts.PolicyLineageManifestSchemaV3, LineageManifestID: lineageID, LineageManifestSHA256: lineageID,
		HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID,
		CodeFoundationCommit: history.CodeFoundationCommit, EvidenceCommit: history.EvidenceCommit,
		EvidenceIndexSHA256: history.EvidenceIndexSHA256, ArtifactTreeSHA256: history.ArtifactTreeSHA256,
		ReplayGateAuditSHA256: history.ReplayGateAuditSHA256, SourceProvenanceSHA256: history.SourceProvenanceSHA256,
		TournamentScopeID: history.TournamentScopeID, TournamentScopeSHA256: history.TournamentScopeSHA256,
		Cutoff: history.Cutoff, Trailing90Start: history.Trailing90Start, Trailing180Start: history.Trailing180Start,
		PatchID: history.PatchID, DotaPatch: history.DotaPatch, DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(),
		Rules: lineage.Rules, Config: lineage.Config, Catalog: lineage.Catalog, Terminology: lineage.Terminology,
		LocalizationParameterMapping: lineage.LocalizationParameterMapping,
	}
	return liveOnlyPolicyArtifacts{History: history, Lineage: lineage, Release: release}
}

func writeCanonicalTestContract(t *testing.T, dir, name string, value any) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, mustCanonicalTest(t, value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustCanonicalTest(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := contracts.MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
