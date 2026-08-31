package history

import (
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func buildSnapshotInput(t *testing.T, nMatches int) (SnapshotInput, RosterManifestV1) {
	t.Helper()
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	windows := NewCutoffWindow(scope.HistoryCutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	var facts []NormalizedMatchFacts
	var matches []DiscoveryMatch
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	for i := 0; i < nMatches; i++ {
		mid := matchIDFor(i)
		facts = append(facts, buildFacts(t, mid, base.Add(time.Duration(i)*time.Hour), "team-a", "team-b", i%2 == 0, roster))
		m := makeMatch(mid, base.Add(time.Duration(i)*time.Hour), MatchReplayAccessible, "team-a", "team-b")
		m.ReplaySHA256 = testSHA(mid)
		m.GameBuild = facts[len(facts)-1].GameBuild
		m.IdentityStatus = contracts.IdentityVerified
		matches = append(matches, m)
	}
	dm := buildDiscovery(t, scope, roster, matches)
	aggIn := AggregateInput{
		Facts: facts, Roster: roster, Windows: windows, Patch: PatchWindow{PatchID: "60", DotaPatch: "7.41"},
		ActiveMatchID: "active", GeneratedAt: mustParseTime(t, "2026-08-11T12:00:00Z"),
	}
	cellsUnused, err := Aggregate(aggIn)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	_ = cellsUnused // baselines are recomputed inside BuildSnapshot from included facts
	return SnapshotInput{
		Scope: scope, Roster: roster, Discovery: dm, Facts: facts, Windows: windows, Patch: PatchWindow{PatchID: "60", DotaPatch: "7.41"},
		Binding: contracts.LiveSessionBindingV1{
			SessionID: "sess-1", ActiveMatchID: "active",
			SessionStartTime:  mustParseTime(t, "2026-08-11T23:00:00Z"),
			TournamentScopeID: scope.ScopeID, TournamentScopeSHA256: scope.ContentSHA256,
		},
		SealedAt:         mustParseTime(t, "2026-08-11T22:00:00Z"),
		GeneratedAt:      mustParseTime(t, "2026-08-11T12:00:00Z"),
		ParserVersion:    "dotabuff/manta v1.5.0",
		AggregateVersion: AdapterName + "/" + AdapterVersion,
	}, roster
}

func TestSnapshotSealsAndBindsBaselines(t *testing.T) {
	in, _ := buildSnapshotInput(t, 6)
	res, err := BuildSnapshot(in)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if err := res.Snapshot.ValidateAgainstScope(in.Scope); err != nil {
		t.Fatalf("snapshot against scope: %v", err)
	}
	if len(res.Baselines) == 0 {
		t.Fatalf("expected baselines")
	}
	for _, b := range res.Baselines {
		if err := b.ValidateAgainst(res.Snapshot); err != nil {
			t.Fatalf("baseline against snapshot: %v", err)
		}
	}
	activeExcluded := false
	for _, x := range res.Snapshot.ExcludedMatches {
		if x.MatchID == "active" && x.Reason == contracts.ExclusionActiveMatch {
			activeExcluded = true
		}
	}
	if !activeExcluded {
		t.Fatalf("active match not excluded")
	}
}

func TestSnapshotActiveMatchFactsRejected(t *testing.T) {
	in, _ := buildSnapshotInput(t, 6)
	active := buildFacts(t, "active", mustParseTime(t, "2026-07-15T00:00:00Z"), "team-a", "team-b", true, in.Roster)
	active.MatchID = "active"
	if err := SealNormalizedMatchFacts(&active); err != nil {
		t.Fatalf("seal active: %v", err)
	}
	in.Facts = append(in.Facts, active)
	_, err := BuildSnapshot(in)
	if err != ErrActiveMatchIncluded {
		t.Fatalf("expected ErrActiveMatchIncluded, got %v", err)
	}
}

func TestSnapshotLateFactRejected(t *testing.T) {
	in, _ := buildSnapshotInput(t, 6)
	late := buildFacts(t, "late", in.Scope.HistoryCutoff.Add(time.Hour), "team-a", "team-b", true, in.Roster)
	late.MatchID = "late"
	if err := SealNormalizedMatchFacts(&late); err != nil {
		t.Fatalf("seal late: %v", err)
	}
	in.Facts = append(in.Facts, late)
	res, err := BuildSnapshot(in)
	if err != nil {
		t.Fatalf("build snapshot with late fact: %v", err)
	}
	foundLate := false
	for _, x := range res.Snapshot.ExcludedMatches {
		if x.MatchID == "late" && x.Reason == "late_fact" {
			foundLate = true
		}
	}
	if !foundLate {
		t.Fatalf("late fact not excluded with late_fact reason: %v", res.Snapshot.ExcludedMatches)
	}
	for _, inc := range res.Snapshot.IncludedMatches {
		if inc.MatchID == "late" {
			t.Fatalf("late fact must not be included")
		}
	}
}

func TestSnapshotSealedAfterSessionStartRejected(t *testing.T) {
	in, _ := buildSnapshotInput(t, 6)
	in.SealedAt = in.Binding.SessionStartTime.Add(time.Second)
	if _, err := BuildSnapshot(in); err == nil {
		t.Fatalf("expected snapshot sealed after session start to be rejected")
	}
}

// TestSnapshotCorrelationMismatchExcludes proves a fact from the wrong replay
// (mismatched SHA, time, patch, or teams) is quarantined out of the snapshot
// rather than joined by MatchID alone.
func TestSnapshotCorrelationMismatchExcludes(t *testing.T) {
	in, _ := buildSnapshotInput(t, 6)
	// Tamper the first fact's replay SHA so it no longer correlates with its
	// discovery record; it must be excluded with a correlation reason.
	in.Facts[0].ReplaySHA256 = sha256HexSeed("wrong-replay")
	if err := SealNormalizedMatchFacts(&in.Facts[0]); err != nil {
		t.Fatalf("seal: %v", err)
	}
	res, err := BuildSnapshot(in)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	excluded := false
	for _, x := range res.Snapshot.ExcludedMatches {
		if x.MatchID == in.Facts[0].MatchID && (x.Reason == "replay_identity_mismatch" || x.Reason == "identity_not_verified") {
			excluded = true
		}
	}
	if !excluded {
		t.Fatalf("expected mismatched-replay fact excluded, got excluded=%v", res.Snapshot.ExcludedMatches)
	}
	for _, inc := range res.Snapshot.IncludedMatches {
		if inc.MatchID == in.Facts[0].MatchID {
			t.Fatalf("mismatched-replay fact must not be included")
		}
	}
}

func TestSnapshotGameBuildMismatchExcludes(t *testing.T) {
	in, _ := buildSnapshotInput(t, 6)
	in.Discovery.Matches[0].GameBuild++
	if err := SealDiscoveryManifestV1(&in.Discovery); err != nil {
		t.Fatalf("reseal discovery: %v", err)
	}
	res, err := BuildSnapshot(in)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	wantID := in.Facts[0].MatchID
	for _, inc := range res.Snapshot.IncludedMatches {
		if inc.MatchID == wantID {
			t.Fatalf("game-build-mismatched fact must not be included")
		}
	}
	found := false
	for _, x := range res.Snapshot.ExcludedMatches {
		if x.MatchID == wantID && x.Reason == "game_build_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected game_build_mismatch exclusion, got %v", res.Snapshot.ExcludedMatches)
	}
}
