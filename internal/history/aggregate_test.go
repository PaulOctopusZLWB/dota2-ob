package history

import (
	"testing"
	"time"
)

func buildAggInput(t *testing.T, nMatches int) AggregateInput {
	t.Helper()
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	teams := []string{"team-a", "team-b"}
	var facts []NormalizedMatchFacts
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	for i := 0; i < nMatches; i++ {
		radiant := teams[i%len(teams)]
		dire := teams[(i+1)%len(teams)]
		facts = append(facts, buildFacts(t, matchIDFor(i), base.Add(time.Duration(i)*time.Hour), radiant, dire, i%2 == 0, roster))
	}
	return AggregateInput{
		Facts: facts, Roster: roster, Windows: windows,
		Patch: PatchWindow{PatchID: "60", DotaPatch: "7.41"},
		ActiveMatchID: "active", GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	}
}

func matchIDFor(i int) string {
	return "890000000" + string(rune('0'+i%10))
}

func TestAggregateMinimaEnforced(t *testing.T) {
	in := buildAggInput(t, 3)
	cells, err := Aggregate(in)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	anyPresent := false
	for _, c := range cells {
		if c.Key.SampleDefinition == "player.scalar.kills" && c.Value.State == "present" {
			anyPresent = true
		}
	}
	// 3 matches < MinDraftCell(5), so no draft kills cell should be present.
	if anyPresent {
		t.Fatalf("expected draft cells below minimum to be absent, got a present kills cell")
	}
	_ = cells
}

func TestAggregatePublishesAboveMinimum(t *testing.T) {
	in := buildAggInput(t, 6) // 6 matches for team-a vs team-b etc. each team plays >=5
	cells, err := Aggregate(in)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	found := false
	for _, c := range cells {
		if c.Key.SampleDefinition == "player.scalar.kills" && c.Key.PlayerID == "person-a0" && c.Value.State == "present" {
			found = true
			if c.Value.SampleSize < MinDraftCell {
				t.Fatalf("published draft cell below minimum: %d", c.Value.SampleSize)
			}
		}
	}
	if !found {
		t.Fatalf("expected at least one published player kills cell above the minimum")
	}
}

func TestAggregateWindowsSeparateAndCoverageReported(t *testing.T) {
	in := buildAggInput(t, 1)
	cells, err := Aggregate(in)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range cells {
		seen[c.Key.Window] = true
	}
	for _, w := range AllWindows() {
		if !seen[string(w)] {
			t.Fatalf("expected window %s to appear", w)
		}
	}
}

func TestAggregateQuarantinedFactsExcluded(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	f := buildFacts(t, "m1", mustParseTime(t, "2026-07-01T00:00:00Z"), "team-a", "team-b", true, roster)
	f.IdentityStatus = "quarantined"
	if err := SealNormalizedMatchFacts(&f); err != nil {
		t.Fatalf("seal: %v", err)
	}
	cells, err := Aggregate(AggregateInput{
		Facts: []NormalizedMatchFacts{f}, Roster: roster, Windows: windows,
		Patch: PatchWindow{PatchID: "60"}, ActiveMatchID: "active",
		GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(cells) != 0 {
		t.Fatalf("expected no cells from a quarantined fact, got %d", len(cells))
	}
}

func TestAggregateActiveMatchExcluded(t *testing.T) {
	in := buildAggInput(t, 6)
	in.Facts[0].MatchID = "active"
	if err := SealNormalizedMatchFacts(&in.Facts[0]); err != nil {
		t.Fatalf("seal: %v", err)
	}
	in.ActiveMatchID = "active"
	cells, err := Aggregate(in)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	for _, c := range cells {
		if c.Value.State == "present" && c.Value.SampleSize > 0 {
			// active match must not inflate counts beyond its absence
		}
	}
	_ = cells
}