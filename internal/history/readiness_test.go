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
	for i, t := range scope.Teams {
		for j := 0; j < 7; j++ {
			pairs = append(pairs, [2]string{t.TeamID, scope.Teams[(i+1)%len(scope.Teams)].TeamID})
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
			pairs = append(pairs, [2]string{scope.Teams[i].TeamID, scope.Teams[len(scope.Teams)-1].TeamID})
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

// TestReadinessGateOneMatchIsNoGo proves a single accessible match (or any
// case where no team reaches its minimum-match coverage) is no_go, not
// restricted_history_go — one match cannot satisfy any five-sample baseline.
func TestReadinessGateOneMatchIsNoGo(t *testing.T) {
	scope := buildScope(t)
	dm := buildReadinessManifest(t, [][2]string{{scope.Teams[0].TeamID, scope.Teams[1].TeamID}})
	e := ReadinessGate(scope, dm)
	if e.Outcome != ReadinessHistoricalNoGo {
		t.Fatalf("expected historical_no_go for one match, got %s (accessible=%d)", e.Outcome, e.ReplayAccessibleTotal)
	}
}

func TestReadinessGateRejectsUnboundManifest(t *testing.T) {
	scope := buildScope(t)
	dm := buildReadinessManifest(t, nil)
	// Tamper the scope binding so the manifest no longer binds to the scope.
	dm.TournamentScopeID = "not-the-scope"
	// Re-seal is intentionally skipped: Validate must reject before the gate
	// evaluates coverage.
	e := ReadinessGate(scope, dm)
	if e.Outcome != ReadinessHistoricalNoGo {
		t.Fatalf("expected no_go for unbound manifest, got %s", e.Outcome)
	}
}
