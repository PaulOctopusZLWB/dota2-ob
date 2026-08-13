package history

import (
	"testing"
	"time"
)

func buildReadinessManifest(t *testing.T, pairs [][2]string) DiscoveryManifestV1 {
	t.Helper()
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	var matches []DiscoveryMatch
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	for i, p := range pairs {
		mid := "m" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		m := makeMatch(mid, base.Add(time.Duration(i)*time.Hour), MatchReplayAccessible, p[0], p[1])
		matches = append(matches, m)
	}
	return buildDiscovery(t, scope, roster, matches)
}

func TestReadinessGateFullHistoryGo(t *testing.T) {
	scope := buildScope(t)
	var pairs [][2]string
	for _, t := range scope.Teams {
		for j := 0; j < 7; j++ {
			pairs = append(pairs, [2]string{t.TeamID, scope.Teams[0].TeamID})
		}
	}
	dm := buildReadinessManifest(t, pairs)
	e := ReadinessGate(scope, dm)
	if e.Outcome != ReadinessFullHistoryGo {
		t.Fatalf("expected full_history_go, got %s (accessible=%d)", e.Outcome, e.ReplayAccessibleTotal)
	}
}

func TestReadinessGateRestrictedHistoryGo(t *testing.T) {
	scope := buildScope(t)
	var pairs [][2]string
	// First 8 teams get five matches each; remaining 8 teams get none.
	for i := 0; i < 8; i++ {
		for j := 0; j < 5; j++ {
			pairs = append(pairs, [2]string{scope.Teams[i].TeamID, scope.Teams[0].TeamID})
		}
	}
	dm := buildReadinessManifest(t, pairs)
	e := ReadinessGate(scope, dm)
	if e.Outcome != ReadinessRestrictedGo {
		t.Fatalf("expected restricted_history_go, got %s", e.Outcome)
	}
	if len(e.DisabledFamilies) == 0 {
		t.Fatalf("expected disabled families for under-represented teams")
	}
}

func TestReadinessGateHistoricalNoGo(t *testing.T) {
	scope := buildScope(t)
	dm := buildReadinessManifest(t, nil)
	e := ReadinessGate(scope, dm)
	if e.Outcome != ReadinessHistoricalNoGo {
		t.Fatalf("expected historical_no_go, got %s", e.Outcome)
	}
}