package history

import (
	"testing"
	"time"
)

func buildReadinessInput(t *testing.T, pairs [][2]string, parsePasses uint32) ReadinessInput {
	t.Helper()
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var matches []DiscoveryMatch
	var facts []NormalizedMatchFacts
	var processed []ProcessedReplayEvidence
	for i, p := range pairs {
		mid := "m" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		f := buildFacts(t, mid, base.Add(time.Duration(i)*time.Hour), p[0], p[1], i%2 == 0, roster)
		facts = append(facts, f)
		m := makeMatch(mid, f.SourceEventTime, MatchReplayAccessible, p[0], p[1])
		m.ReplaySHA256 = f.ReplaySHA256
		m.GameBuild = f.GameBuild
		matches = append(matches, m)
		proof := ProcessedReplayEvidence{MatchID: mid}
		for pass := uint32(0); pass < parsePasses; pass++ {
			receipt := ParseExecutionEvidence{SchemaVersion: "history.parse-execution.v1", ExecutionID: mid + "-pass-" + string(rune('a'+pass)), MatchID: mid, ReplaySHA256: f.ReplaySHA256, FactsSHA256: f.ContentSHA256, ParserVersion: "manta/v1.5.0", AdapterVersion: AdapterName + "/" + AdapterVersion, ConfigSHA256: testSHA("config"), Deterministic: true}
			if err := SealParseExecutionEvidence(&receipt); err != nil {
				t.Fatal(err)
			}
			proof.Passes = append(proof.Passes, receipt)
		}
		processed = append(processed, proof)
	}
	dm := buildDiscovery(t, scope, roster, matches)
	return ReadinessInput{Scope: scope, Roster: roster, Manifest: dm, Facts: facts, Processed: processed, Windows: NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60"}, mustParseTime(t, "2026-03-24T00:00:00Z")), Patch: PatchWindow{PatchID: "60"}, GeneratedAt: scope.HistoryCutoff.Add(-time.Hour)}
}

func fullPairs(scopeTeamIDs []string) [][2]string {
	var pairs [][2]string
	for i, team := range scopeTeamIDs {
		for j := 0; j < 7; j++ {
			pairs = append(pairs, [2]string{team, scopeTeamIDs[(i+1)%len(scopeTeamIDs)]})
		}
	}
	return pairs
}

func TestReadinessGateFullHistoryGoFromProcessedFactsAndCells(t *testing.T) {
	scope := buildScope(t)
	ids := make([]string, len(scope.Teams))
	for i := range scope.Teams {
		ids[i] = scope.Teams[i].TeamID
	}
	in := buildReadinessInput(t, fullPairs(ids), 2)
	e := ReadinessGate(in)
	if e.Outcome != ReadinessFullHistoryGo || e.RepeatablyProcessedTotal < 100 || e.EnabledCellTotal == 0 {
		t.Fatalf("expected evidence-backed full go, got %#v", e)
	}
}

func TestReadinessGateAccessibleManifestWithoutRepeatableParseCannotPass(t *testing.T) {
	scope := buildScope(t)
	ids := make([]string, len(scope.Teams))
	for i := range scope.Teams {
		ids[i] = scope.Teams[i].TeamID
	}
	in := buildReadinessInput(t, fullPairs(ids), 1)
	e := ReadinessGate(in)
	if e.Outcome == ReadinessFullHistoryGo || e.RestrictedReason != "no_repeatably_processed_replays" {
		t.Fatalf("accessible-only manifest passed: %#v", e)
	}
}

func TestReadinessGateRestrictedHistoryGo(t *testing.T) {
	scope := buildScope(t)
	var pairs [][2]string
	for i := 0; i < 8; i++ {
		for j := 0; j < 5; j++ {
			pairs = append(pairs, [2]string{scope.Teams[i].TeamID, scope.Teams[15].TeamID})
		}
	}
	e := ReadinessGate(buildReadinessInput(t, pairs, 2))
	if e.Outcome != ReadinessRestrictedGo || len(e.DisabledFamilies) == 0 {
		t.Fatalf("expected restricted with disabled teams, got %#v", e)
	}
}

func TestReadinessGateHistoricalNoGoWithoutEnabledCells(t *testing.T) {
	scope := buildScope(t)
	in := buildReadinessInput(t, [][2]string{{scope.Teams[0].TeamID, scope.Teams[1].TeamID}}, 2)
	e := ReadinessGate(in)
	if e.Outcome != ReadinessHistoricalNoGo || e.RestrictedReason != "no_enabled_baseline_cells" {
		t.Fatalf("expected no-go for insufficient cells, got %#v", e)
	}
}

func TestReadinessGateRejectsUnboundManifest(t *testing.T) {
	in := buildReadinessInput(t, nil, 2)
	in.Manifest.TournamentScopeID = "not-the-scope"
	e := ReadinessGate(in)
	if e.Outcome != ReadinessHistoricalNoGo {
		t.Fatalf("expected no-go for unbound manifest, got %#v", e)
	}
}

func TestReadinessRejectsCopiedParseReceipt(t *testing.T) {
	scope := buildScope(t)
	ids := make([]string, len(scope.Teams))
	for i := range scope.Teams {
		ids[i] = scope.Teams[i].TeamID
	}
	in := buildReadinessInput(t, fullPairs(ids), 1)
	for i := range in.Processed {
		in.Processed[i].Passes = append(in.Processed[i].Passes, in.Processed[i].Passes[0])
	}
	if got := ReadinessGate(in); got.Outcome == ReadinessFullHistoryGo {
		t.Fatalf("copied receipt passed readiness: %#v", got)
	}
}
