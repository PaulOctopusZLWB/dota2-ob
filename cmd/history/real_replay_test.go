package main

import (
	"os"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
)

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
