package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
)

func TestRealReplayParseCheckpointRejectsReplacedIndex(t *testing.T) {
	root := t.TempDir()
	replaySHA := sha256Text("verified-replay")
	cfg := realReplayConfig{
		Root: root, MatchID: "8941092540", ReplayURL: "https://replay.invalid/8941092540.dem.bz2",
		SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
		RadiantTeamID: "zero-tenacity", DireTeamID: "rune-eaters", DurationSeconds: 3297,
	}
	manifest, _, _, err := realReplayManifest(cfg, replaySHA)
	if err != nil {
		t.Fatal(err)
	}
	c := &realReplayComposition{cfg: cfg, manifest: manifest}
	parsed := replay.BuildFacts(&replay.Collected{GameBuild: 6896, ServerName: "replacement", MessageCounts: map[string]uint64{}})
	payload, err := parsed.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	factsHash, err := parsed.Hash()
	if err != nil {
		t.Fatal(err)
	}
	runs := []realParseIndexRun{
		{ExecutionID: "manta-pass-a", PayloadPath: filepath.Base(c.parsePayloadPath("manta-pass-a")), PayloadSHA: bytesSHA256(payload), FactsHash: factsHash},
		{ExecutionID: "manta-pass-b", PayloadPath: filepath.Base(c.parsePayloadPath("manta-pass-b")), PayloadSHA: bytesSHA256(payload), FactsHash: factsHash},
	}
	for _, run := range runs {
		if err := os.MkdirAll(filepath.Dir(c.parsePayloadPath(run.ExecutionID)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.parsePayloadPath(run.ExecutionID), append(payload, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index := realParseIndex{SchemaVersion: "history.real-parse-index.v1", MatchID: c.cfg.MatchID, ReplaySHA256: replaySHA, Runs: runs}
	indexBytes, err := contracts.MarshalCanonical(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.parseIndexPath(), append(indexBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	entrySHA, err := history.DiscoveryEntrySHA256(manifest, manifest.Matches[0])
	if err != nil {
		t.Fatal(err)
	}
	entry := history.StageEntry{
		MatchID: "8941092540", Status: history.StageQueued, ReachedStage: history.StageParse, ReplaySHA256: replaySHA,
		ArtifactSHA256:      map[string]string{history.StageAcquisition: replaySHA, history.StageVerification: replaySHA, history.StageParse: sha256Text("checkpointed-original-index")},
		ArtifactInputSHA256: map[string]string{history.StageAcquisition: entrySHA, history.StageVerification: replaySHA, history.StageParse: replaySHA},
	}
	prior := history.NewStageBatch(manifest)
	prior.Entries[entry.MatchID] = entry
	effects := 0
	stages := map[string]history.StageFunc{}
	for _, stage := range []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate} {
		stages[stage] = func(e history.StageEntry, _ history.MatchContext) (history.StageEntry, *history.StageFailure) {
			effects++
			return e, nil
		}
	}
	pipeline := history.StagePipeline{
		Stages: stages, StageOrder: []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate}, MaxRetries: 1,
		ValidateCompleted: func(e history.StageEntry, ctx history.MatchContext) error {
			return c.validateStage(e, ctx, history.StageParse)
		},
	}
	if _, err := pipeline.Run(manifest, prior); err == nil {
		t.Fatal("externally replaced parse index was accepted despite checkpoint identity mismatch")
	}
	if effects != 0 {
		t.Fatalf("effects occurred before the replaced parse index was rejected: %d", effects)
	}
}

func TestRealReplayCompositionTraversesMantaOnPBDEMS2(t *testing.T) {
	dem := os.Getenv("DOTA2_OB_REAL_REPLAY")
	if dem == "" {
		t.Skip("set DOTA2_OB_REAL_REPLAY to the accepted task-local PBDEMS2 artifact")
	}
	result, err := runRealReplayComposition(realReplayConfig{
		SourcePath: dem, Root: t.TempDir(), MatchID: "8941092540",
		ReplayURL:       "http://replay273.valve.net/570/8941092540_1595018738.dem.bz2",
		SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
		RadiantTeamID: "zero-tenacity", DireTeamID: "rune-eaters", DurationSeconds: 3297,
	})
	if err != nil {
		t.Fatalf("real replay composition: %v", err)
	}
	if result.ReplayIdentity.Magic != "PBDEMS2\\x00" || result.ReplayIdentity.Bytes < 1_000_000 {
		t.Fatalf("input is not a genuine binary replay: %#v", result.ReplayIdentity)
	}
	if len(result.Processed.Passes) != 2 || len(result.ParseMetrics) != 2 {
		t.Fatalf("expected two independently recorded executions: %#v", result.Processed)
	}
	for _, pass := range result.Processed.Passes {
		if pass.ParserVersion != replay.ParserName+"/"+replay.ParserVersion || pass.AdapterVersion != replay.AdapterName+"/"+replay.AdapterVersion {
			t.Fatalf("execution did not bind real adapter versions: %#v", pass)
		}
		if err := validateParseExecutionArtifacts(result.ExecutionRoot, pass); err != nil {
			t.Fatalf("durable execution artifacts: %v", err)
		}
	}
	if result.Processed.Passes[0].FactsSHA256 != result.Processed.Passes[1].FactsSHA256 || result.Facts.ContentSHA256 != result.Processed.Passes[0].FactsSHA256 {
		t.Fatal("two real executions were not deterministic")
	}
	for _, stage := range []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate} {
		if result.StageCalls[stage] != 1 {
			t.Fatalf("stage %s effect count=%d, want 1", stage, result.StageCalls[stage])
		}
	}
}

func TestRealReplayNormalizeCheckpointRejectsUnrelatedFactsBeforeEffects(t *testing.T) {
	root := t.TempDir()
	replaySHA := sha256Text("verified-replay")
	replayPath := filepath.Join(root, "verified.dem")
	if err := os.WriteFile(replayPath, []byte("verified-replay"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := replay.BuildFacts(&replay.Collected{GameBuild: 6896, ServerName: "fixture", MessageCounts: map[string]uint64{}})
	duration := int64(3297)
	meta := replay.NormalizeMeta{
		MatchID: "8941092540", ReplaySHA256: replaySHA, SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC),
		PatchID: "60", RadiantTeamID: "zero-tenacity", DireTeamID: "rune-eaters", GameBuild: 6896, DurationSeconds: &duration,
	}
	normalized, err := replay.Normalize(parsed, meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := realReplayConfig{
		Root: root, MatchID: meta.MatchID, ReplayURL: "https://replay.invalid/8941092540.dem.bz2",
		SourceEventTime: meta.SourceEventTime, PatchID: meta.PatchID, GameBuild: meta.GameBuild,
		RadiantTeamID: meta.RadiantTeamID, DireTeamID: meta.DireTeamID, DurationSeconds: duration,
	}
	manifest, _, _, err := realReplayManifest(cfg, replaySHA)
	if err != nil {
		t.Fatal(err)
	}
	c := &realReplayComposition{cfg: cfg, manifest: manifest}
	c.facts = *normalized
	if err := os.MkdirAll(filepath.Dir(c.factsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	factsBytes, err := contracts.MarshalCanonical(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.factsPath(), append(factsBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	proof := history.ProcessedReplayEvidence{MatchID: meta.MatchID}
	for _, executionID := range []string{"manta-pass-a", "manta-pass-b"} {
		result, err := executeParseExecutionWith(c.executionRoot(), parseExecutionRequest{
			ExecutionID: executionID, ReplayPath: replayPath, ExpectedReplaySHA256: replaySHA, NormalizeMeta: meta,
		}, testParseExecutionDependencies(t, replayPath, *parsed))
		if err != nil {
			t.Fatal(err)
		}
		proof.Passes = append(proof.Passes, result.Evidence)
	}
	proofBytes, err := contracts.MarshalCanonical(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.proofPath(), append(proofBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	entrySHA, err := history.DiscoveryEntrySHA256(manifest, manifest.Matches[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []history.StageState{history.StageQueued, history.StageRunning, history.StageSucceeded} {
		t.Run(string(status), func(t *testing.T) {
			unrelated := sha256Text("unrelated-normalize")
			parseSHA := sha256Text("parse-index")
			entry := history.StageEntry{
				MatchID: meta.MatchID, Status: status, ReachedStage: history.StageNormalize, ReplaySHA256: replaySHA,
				FactsSHA256: unrelated,
				ArtifactSHA256: map[string]string{
					history.StageAcquisition: replaySHA, history.StageVerification: replaySHA,
					history.StageParse: parseSHA, history.StageNormalize: unrelated,
				},
				ArtifactInputSHA256: map[string]string{
					history.StageAcquisition: entrySHA, history.StageVerification: replaySHA,
					history.StageParse: replaySHA, history.StageNormalize: parseSHA,
				},
			}
			if status == history.StageSucceeded {
				entry.ReachedStage = history.StageAggregate
				entry.ArtifactSHA256[history.StageAggregate] = sha256Text("aggregate")
				entry.ArtifactInputSHA256[history.StageAggregate] = unrelated
			}
			prior := history.NewStageBatch(manifest)
			prior.Entries[entry.MatchID] = entry
			effects := 0
			stages := map[string]history.StageFunc{}
			for _, stage := range []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate} {
				stages[stage] = func(e history.StageEntry, _ history.MatchContext) (history.StageEntry, *history.StageFailure) {
					effects++
					return e, nil
				}
			}
			pipeline := history.StagePipeline{
				Stages: stages, StageOrder: []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate}, MaxRetries: 1,
				ValidateCompleted: func(e history.StageEntry, _ history.MatchContext) error {
					return c.validateStage(e, history.MatchContext{}, history.StageNormalize)
				},
			}
			if _, err := pipeline.Run(manifest, prior); err == nil {
				t.Fatal("valid normalized artifact set accepted against unrelated checkpoint identities")
			}
			if effects != 0 {
				t.Fatalf("effects occurred before unrelated normalize checkpoint was rejected: %d", effects)
			}
		})
	}
}
