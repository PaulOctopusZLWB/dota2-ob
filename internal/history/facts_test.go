package history

import (
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// buildFacts constructs a sealed, verified NormalizedMatchFacts for one match
// with two teams of five participants drawn from the scope roster.
func buildFacts(t *testing.T, matchID string, eventTime time.Time, radiantTeam, direTeam string, radiantWin bool, roster RosterManifestV1) NormalizedMatchFacts {
	t.Helper()
	radiantPlayers := teamPlayers(roster, radiantTeam, 5)
	direPlayers := teamPlayers(roster, direTeam, 5)
	f := NormalizedMatchFacts{
		SchemaVersion:   FactsSchema,
		MatchID:         matchID,
		ReplaySHA256:    testSHA(matchID),
		SourceEventTime: eventTime,
		PatchID:         "60",
		GameBuild:       6896,
		RadiantTeamID:   radiantTeam,
		DireTeamID:      direTeam,
		RadiantWin:      &radiantWin,
		IdentityStatus:  contracts.IdentityVerified,
		Availability: FactsAvailability{
			Available:   []string{MetricGames, MetricKills, MetricDeaths, MetricAssists, MetricGPM, MetricXPM, MetricLevel, MetricNetWorth, MetricKillParticipation},
			Deferred:    []string{MetricFarmCheckpoint, MetricKeyItemTiming},
			Unavailable: []string{"replay_salt_or_gc_credentials", "hidden_fog_of_war_state"},
		},
	}
	slot := 0
	for i, p := range radiantPlayers {
		f.Participants = append(f.Participants, mkParticipant(p.PersonID, radiantTeam, "npc_dota_hero_"+heroFor(i), p.Role, slot))
		slot++
	}
	for i, p := range direPlayers {
		f.Participants = append(f.Participants, mkParticipant(p.PersonID, direTeam, "npc_dota_hero_"+heroFor(5+i), p.Role, slot))
		slot++
	}
	f.Participants = sortParticipantsByPersonID(f.Participants)
	if err := SealNormalizedMatchFacts(&f); err != nil {
		t.Fatalf("seal facts %s: %v", matchID, err)
	}
	return f
}

func sortParticipantsByPersonID(in []ParticipantFacts) []ParticipantFacts {
	out := append([]ParticipantFacts(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].PersonID > out[j].PersonID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func teamPlayers(roster RosterManifestV1, teamID string, n int) []RosterPlayer {
	out := []RosterPlayer{}
	for _, p := range roster.Players {
		if p.TeamID == teamID && p.Role == "player" {
			out = append(out, p)
			if len(out) == n {
				return out
			}
		}
	}
	return out
}

func mkParticipant(personID, teamID, hero, role string, slot int) ParticipantFacts {
	k := int64(slot + 1)
	d := int64(slot)
	a := int64(slot + 2)
	gpm := int64(500 + slot*10)
	xpm := int64(550 + slot*10)
	lh := int64(40 + slot*5)
	den := int64(5 + slot)
	lvl := int64(20)
	return ParticipantFacts{
		PersonID: personID, TeamID: teamID, HeroName: hero, Role: role, Slot: slot,
		Kills: &k, Deaths: &d, Assists: &a, GPM: &gpm, XPM: &xpm,
		LastHits: &lh, Denies: &den, Level: &lvl,
	}
}

func heroFor(i int) string {
	heroes := []string{"invoker", "juggernaut", "crystal_maiden", "lina", "axe", "phantom_assassin", "earthshaker", "lion", "sniper", "drow_ranger"}
	return heroes[i%len(heroes)]
}

func TestNormalizedFactsSealDeterministic(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	eventTime := mustParseTime(t, "2026-07-01T00:00:00Z")
	f1 := buildFacts(t, "m1", eventTime, "team-a", "team-b", true, roster)
	f2 := buildFacts(t, "m1", eventTime, "team-a", "team-b", true, roster)
	if f1.ContentSHA256 != f2.ContentSHA256 {
		t.Fatalf("deterministic facts hash mismatch: %s != %s", f1.ContentSHA256, f2.ContentSHA256)
	}
	f2.Participants[0].Kills = int64Ptr(99)
	if err := SealNormalizedMatchFacts(&f2); err != nil {
		t.Fatalf("re-seal: %v", err)
	}
	if f1.ContentSHA256 == f2.ContentSHA256 {
		t.Fatalf("expected changed facts to change content hash")
	}
}

func TestNormalizedFactsQuarantinePreserved(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	eventTime := mustParseTime(t, "2026-07-01T00:00:00Z")
	f := buildFacts(t, "m1", eventTime, "team-a", "team-b", true, roster)
	f.IdentityStatus = contracts.IdentityQuarantined
	if err := SealNormalizedMatchFacts(&f); err != nil {
		t.Fatalf("seal quarantined: %v", err)
	}
	if f.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("quarantine not preserved through sealing")
	}
}

func TestWindowsHalfOpenBounds(t *testing.T) {
	cutoff := mustParseTime(t, "2026-08-12T00:00:00Z")
	patchRelease := mustParseTime(t, "2026-03-24T00:00:00Z")
	w := NewCutoffWindow(cutoff, PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)
	if !w.Includes(WindowTrailing90, cutoff) {
		t.Fatalf("cutoff itself must be included")
	}
	if w.Includes(WindowTrailing90, cutoff.Add(time.Second)) {
		t.Fatalf("after cutoff must be excluded")
	}
	if w.Includes(WindowCurrentPatch, patchRelease.Add(-time.Second)) {
		t.Fatalf("before patch release must not be current patch")
	}
	if !w.Includes(WindowCurrentPatch, patchRelease) {
		t.Fatalf("patch release time itself must be current patch")
	}
}