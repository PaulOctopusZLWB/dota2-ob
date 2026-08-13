package history

import (
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// findCell returns the first cell matching the given key fields (empty fields ignored).
func findCell(cells []BaselineCell, metric, window, patch, playerID, sampleDef string) (BaselineCell, bool) {
	for _, c := range cells {
		if metric != "" && c.Key.Metric != metric {
			continue
		}
		if window != "" && c.Key.Window != window {
			continue
		}
		if patch != "" && c.Key.Patch != patch {
			continue
		}
		if playerID != "" && c.Key.PlayerID != playerID {
			continue
		}
		if sampleDef != "" && c.Key.SampleDefinition != sampleDef {
			continue
		}
		return c, true
	}
	return BaselineCell{}, false
}

// TestAggregateAdversarialNumericGoldens pins the corrected arithmetic:
//   - wins value == exact win count, sample/coverage denominator == games (not wins);
//   - kills mean is not inflated by the decimal fixed-point scaler;
//   - kill participation is a ratio in [0,1] ("0.26"), not a percent ("26.00").
func TestAggregateAdversarialNumericGoldens(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var facts []NormalizedMatchFacts
	for i := 0; i < 6; i++ {
		// team-a always radiant and always wins; person-a0 is always slot 0.
		facts = append(facts, buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", true, roster))
	}
	cells, err := Aggregate(AggregateInput{
		Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"},
		ActiveMatchID: "active", GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	// person-a0 is on team-a, which won all six matches; value must be the
	// exact win count and the sample size must be games (6), never wins-as-denominator.
	wins, ok := findCell(cells, MetricWins, string(WindowTrailing90), "60", "person-a0", "player.scalar.wins")
	if !ok {
		t.Fatalf("player wins trailing_90 cell not found")
	}
	if wins.Value.State != contracts.ValuePresent {
		t.Fatalf("wins cell should be present, got %s (sample=%d cov=%d)", wins.Value.State, wins.Value.SampleSize, wins.Value.SourceCoveragePPM)
	}
	if wins.Value.Value == nil || string(*wins.Value.Value) != "6" {
		t.Fatalf("wins value must be 6, got %v", wins.Value.Value)
	}
	if wins.Value.SampleSize != 6 {
		t.Fatalf("wins sample size must be games (6), got %d", wins.Value.SampleSize)
	}
	if wins.Value.SourceCoveragePPM != 1_000_000 {
		t.Fatalf("wins coverage must be full (all results known) = 1000000 ppm, got %d", wins.Value.SourceCoveragePPM)
	}

	// kills mean for person-a0: mkParticipant slot0 kills=1 every match -> mean "1.00".
	kills, ok := findCell(cells, MetricKills, string(WindowTrailing90), "60", "person-a0", "player.scalar.kills")
	if !ok {
		t.Fatalf("player kills trailing_90 cell not found")
	}
	if kills.Value.State != contracts.ValuePresent || kills.Value.Value == nil || string(*kills.Value.Value) != "1.00" {
		t.Fatalf("kills mean must be 1.00, got state=%s val=%v", kills.Value.State, kills.Value.Value)
	}

	// kill participation for person-a0: (1+2)/(1+2+3+4+5)=3/15=0.20 -> "0.20".
	kp, ok := findCell(cells, MetricKillParticipation, string(WindowTrailing90), "60", "person-a0", "player.scalar.kill_participation")
	if !ok {
		t.Fatalf("player KP trailing_90 cell not found")
	}
	if kp.Value.State != contracts.ValuePresent || kp.Value.Value == nil || string(*kp.Value.Value) != "0.20" {
		t.Fatalf("kill participation must be ratio 0.20, got state=%s val=%v (was it emitted as a percent?)", kp.Value.State, kp.Value.Value)
	}
}

// TestAggregateUnknownRadiantWinIsMissing proves an unknown win outcome is a
// missing observation: games stays present, wins becomes absent (never a
// fabricated Dire win), and the wins denominator stays games.
func TestAggregateUnknownRadiantWinIsMissing(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var facts []NormalizedMatchFacts
	for i := 0; i < 6; i++ {
		f := buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", i%2 == 0, roster)
		facts = append(facts, f)
	}
	// Make the last match's win outcome unknown.
	facts[5].RadiantWin = nil
	if err := SealNormalizedMatchFacts(&facts[5]); err != nil {
		t.Fatalf("seal: %v", err)
	}
	cells, err := Aggregate(AggregateInput{
		Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"},
		ActiveMatchID: "active", GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	// games cell: 6 games, still present (player draft cell, min 5).
	games, ok := findCell(cells, MetricGames, string(WindowTrailing90), "60", "person-a0", "player.scalar.games")
	if !ok {
		t.Fatalf("player games cell not found")
	}
	if games.Value.SampleSize != 6 || games.Value.State != contracts.ValuePresent {
		t.Fatalf("games sample must be 6 present, got sample=%d state=%s", games.Value.SampleSize, games.Value.State)
	}
	// wins cell: one game has an unknown result -> win total incomplete -> absent.
	wins, ok := findCell(cells, MetricWins, string(WindowTrailing90), "60", "person-a0", "player.scalar.wins")
	if !ok {
		t.Fatalf("player wins cell not found")
	}
	if wins.Value.State != contracts.ValueAbsent {
		t.Fatalf("wins must be absent when a win outcome is unknown, got state=%s val=%v", wins.Value.State, wins.Value.Value)
	}
	if wins.Value.SampleSize != 6 {
		t.Fatalf("wins denominator must stay games (6) even when incomplete, got %d", wins.Value.SampleSize)
	}
	if wins.Value.SourceCoveragePPM == 1_000_000 {
		t.Fatalf("wins coverage must reflect missingness, got full coverage")
	}
}

// TestAggregatePatchPerWindowKey proves trailing-window facts from an older
// patch are labeled with their own patch, never silently mixed under the
// current patch id.
func TestAggregatePatchPerWindowKey(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var facts []NormalizedMatchFacts
	for i := 0; i < 6; i++ {
		f := buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", true, roster)
		f.PatchID = "59" // older patch; not current-patch
		if err := SealNormalizedMatchFacts(&f); err != nil {
			t.Fatalf("seal: %v", err)
		}
		facts = append(facts, f)
	}
	cells, err := Aggregate(AggregateInput{
		Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"},
		ActiveMatchID: "active", GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	// A trailing_90 kills cell must be labeled patch "59" (the fact's patch).
	k59, ok := findCell(cells, MetricKills, string(WindowTrailing90), "59", "person-a0", "player.scalar.kills")
	if !ok {
		t.Fatalf("expected trailing_90 kills cell labeled patch 59")
	}
	if k59.Value.State != contracts.ValuePresent {
		t.Fatalf("patch-59 trailing_90 kills must be present (6>=5), got %s", k59.Value.State)
	}
	// No current_patch kills cell should exist for patch "59" (current filters non-current).
	if k60, ok := findCell(cells, MetricKills, string(WindowCurrentPatch), "59", "person-a0", "player.scalar.kills"); ok {
		t.Fatalf("current_patch must not include older-patch facts, found cell %v", k60.Key)
	}
}

// TestAggregateEffectiveMembershipExcludes proves a participant whose roster
// team membership is NOT effective at match time (player listed under a team
// they are not rostered for) is excluded from aggregation rather than counted.
func TestAggregateEffectiveMembershipExcludes(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var facts []NormalizedMatchFacts
	for i := 0; i < 6; i++ {
		f := buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", true, roster)
		// Mis-assign person-a0 (a team-a player) to team-b in the facts; this
		// is not an effective team-b membership, so it must be excluded.
		for j := range f.Participants {
			if f.Participants[j].PersonID == "person-a0" {
				f.Participants[j].TeamID = "team-b"
				break
			}
		}
		if err := SealNormalizedMatchFacts(&f); err != nil {
			t.Fatalf("seal: %v", err)
		}
		facts = append(facts, f)
	}
	cells, err := Aggregate(AggregateInput{
		Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"},
		ActiveMatchID: "active", GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	// person-a0 was listed under team-b but is rostered for team-a, so it must
	// NOT have a present player kills cell under the team-b roster.
	if c, ok := findCell(cells, MetricKills, string(WindowTrailing90), "60", "person-a0", "player.scalar.kills"); ok && c.Value.State == contracts.ValuePresent {
		t.Fatalf("mis-assigned person-a0 must be excluded, got present cell %v", c.Value)
	}
	// A legitimate team-a player (person-a1) still aggregates normally.
	if c, ok := findCell(cells, MetricKills, string(WindowTrailing90), "60", "person-a1", "player.scalar.kills"); !ok || c.Value.State != contracts.ValuePresent {
		t.Fatalf("legitimate team-a player person-a1 should have present kills cell")
	}
}

func TestAggregateTeamScalarIsPerMatchTeamTotal(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60"}, mustParseTime(t, "2026-03-24T00:00:00Z"))
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var facts []NormalizedMatchFacts
	for i := 0; i < 10; i++ {
		facts = append(facts, buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", true, roster))
	}
	cells, err := Aggregate(AggregateInput{Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"}, ActiveMatchID: "active", GeneratedAt: scope.HistoryCutoff.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cell, ok := findCell(cells, MetricKills, string(WindowTrailing90), "60", "", "team.scalar.kills")
	if !ok || cell.Key.RosterID != "roster-a" || cell.Value.Value == nil || string(*cell.Value.Value) != "15.00" {
		t.Fatalf("team kills must be mean per-match team total 15.00, got %#v", cell)
	}
}

func TestParseDecimalContractTokensExactly(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{{"0.5", 50}, {"1e3", 100000}, {"-2.345", -234}, {"1.234E-1", 12}} {
		got, ok := parseDecString(tc.in)
		if !ok || got != tc.want {
			t.Errorf("parseDecString(%q)=(%d,%v), want (%d,true)", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{"", "nope", "1e999999999999999999999"} {
		if _, ok := parseDecString(in); ok {
			t.Errorf("parseDecString(%q) unexpectedly succeeded", in)
		}
	}
}

func TestAggregateDecimalConversionAndBucketMissingness(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60"}, mustParseTime(t, "2026-03-24T00:00:00Z"))
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	var facts []NormalizedMatchFacts
	for i := 0; i < 8; i++ {
		f := buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", true, roster)
		for j := range f.Participants {
			if f.Participants[j].PersonID != "person-a0" {
				continue
			}
			if i == 0 {
				bad := "not-a-decimal"
				f.Participants[j].NetWorth = &bad
			} else {
				good := "1e3"
				f.Participants[j].NetWorth = &good
			}
			f.Participants[j].FarmCheckpoints = map[int64]string{10: "0.5"}
			if i < 4 {
				f.Participants[j].FarmCheckpoints[20] = "2.0"
			}
		}
		if err := SealNormalizedMatchFacts(&f); err != nil {
			t.Fatal(err)
		}
		facts = append(facts, f)
	}
	cells, err := Aggregate(AggregateInput{Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"}, ActiveMatchID: "active", GeneratedAt: scope.HistoryCutoff.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	nw, ok := findCell(cells, MetricNetWorth, string(WindowTrailing90), "60", "person-a0", "player.scalar.net_worth")
	if !ok || nw.Value.SampleSize != 7 || nw.Value.SourceCoveragePPM != 875000 || nw.Value.Value == nil || string(*nw.Value.Value) != "1000.00" {
		t.Fatalf("invalid conversion must be missing: %#v", nw)
	}
	b10, ok := findCell(cells, MetricFarmCheckpoint, string(WindowTrailing90), "60", "person-a0", "player.distribution.farm_checkpoint:10")
	if !ok || b10.Value.SourceCoveragePPM != 1000000 || b10.Value.SampleSize != 8 {
		t.Fatalf("bucket 10 coverage wrong: %#v", b10)
	}
	b20, ok := findCell(cells, MetricFarmCheckpoint, string(WindowTrailing90), "60", "person-a0", "player.distribution.farm_checkpoint:20")
	if !ok || b20.Value.SourceCoveragePPM != 500000 || b20.Value.SampleSize != 4 {
		t.Fatalf("bucket 20 missingness must be independent: %#v", b20)
	}
}

func TestAggregateRequiresOwningTeamEffectiveInterval(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	from := mustParseTime(t, "2026-07-01T00:00:00Z")
	until := from.Add(time.Hour)
	for i := range roster.Teams {
		if roster.Teams[i].TeamID == "team-a" {
			roster.Teams[i].EffectiveFrom, roster.Teams[i].EffectiveUntil = from, until
		}
	}
	if err := SealRosterManifestV1(&roster); err != nil {
		t.Fatal(err)
	}
	var facts []NormalizedMatchFacts
	for i := 0; i < 5; i++ {
		facts = append(facts, buildFacts(t, "at-from-"+pidSuffix(i), from, "team-a", "team-b", true, roster))
	}
	facts = append(facts, buildFacts(t, "before-team", from.Add(-time.Nanosecond), "team-a", "team-b", true, roster))
	facts = append(facts, buildFacts(t, "at-until", until, "team-a", "team-b", true, roster))
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60"}, mustParseTime(t, "2026-03-24T00:00:00Z"))
	cells, err := Aggregate(AggregateInput{Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60"}, GeneratedAt: scope.HistoryCutoff.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	games, ok := findCell(cells, MetricGames, string(WindowTrailing90), "60", "person-a0", "player.scalar.games")
	if !ok || games.Value.SampleSize != 5 {
		t.Fatalf("only [effective_from,effective_until) matches may count, got %#v", games)
	}
}
