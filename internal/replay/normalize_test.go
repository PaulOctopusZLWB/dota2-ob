package replay

import (
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func sampleFacts() *ReplayFactsV1 {
	c := &Collected{
		GameBuild:    6896,
		ServerName:   "prod",
		LastTick:     100000,
		MaxTimestamp: 3297.0,
		Combat: []CombatEvent{
			{Type: "DOTA_COMBATLOG_DEATH", Attacker: "npc_dota_hero_invoker", Target: "npc_dota_hero_juggernaut"},
			{Type: "DOTA_COMBATLOG_DEATH", Attacker: "npc_dota_hero_juggernaut", Target: "npc_dota_hero_invoker"},
			{Type: "DOTA_COMBATLOG_DEATH", Attacker: "npc_dota_hero_invoker", Target: "npc_dota_hero_juggernaut"},
		},
	}
	return BuildFacts(c)
}

func TestNormalizeQuarantinedWithoutMapping(t *testing.T) {
	f := sampleFacts()
	meta := NormalizeMeta{
		MatchID: "m1", SourceEventTime: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PatchID: "60", ReplaySHA256: "1111111111111111111111111111111111111111111111111111111111111111", RadiantTeamID: "team-a", DireTeamID: "team-b",
	}
	nf, err := Normalize(f, meta, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantined without mapping, got %s", nf.IdentityStatus)
	}
	for _, p := range nf.Participants {
		if p.PersonID != "" {
			t.Fatalf("quarantined participant must have empty person id, got %q", p.PersonID)
		}
	}
}

func TestNormalizeVerifiedWithFullMapping(t *testing.T) {
	f := sampleFacts() // 2 heroes: juggernaut, invoker
	if len(f.Heroes) != 2 {
		t.Fatalf("expected 2 heroes in sample facts, got %d", len(f.Heroes))
	}
	meta := NormalizeMeta{
		MatchID: "m1", SourceEventTime: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PatchID: "60", ReplaySHA256: "1111111111111111111111111111111111111111111111111111111111111111", RadiantTeamID: "team-a", DireTeamID: "team-b",
	}
	mapping := []ParticipantMapping{
		{PersonID: "person-a0", TeamID: "team-a", HeroName: "npc_dota_hero_invoker", Role: "player", Slot: 0},
		{PersonID: "person-b0", TeamID: "team-b", HeroName: "npc_dota_hero_juggernaut", Role: "player", Slot: 5},
	}
	nf, err := Normalize(f, meta, mapping)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityVerified {
		t.Fatalf("expected verified with full mapping, got %s", nf.IdentityStatus)
	}
	// Re-run identically must yield a byte-identical content hash (determinism).
	nf2, err := Normalize(sampleFacts(), meta, mapping)
	if err != nil {
		t.Fatalf("normalize 2: %v", err)
	}
	if nf.ContentSHA256 != nf2.ContentSHA256 {
		t.Fatalf("normalize not deterministic: %s != %s", nf.ContentSHA256, nf2.ContentSHA256)
	}
}

func TestNormalizeMismappedHeroQuarantines(t *testing.T) {
	f := sampleFacts()
	meta := NormalizeMeta{
		MatchID: "m1", SourceEventTime: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PatchID: "60", ReplaySHA256: "2222222222222222222222222222222222222222222222222222222222222222",
	}
	// mapping references a hero not present in the parsed facts -> quarantine.
	mapping := []ParticipantMapping{
		{PersonID: "person-a0", TeamID: "team-a", HeroName: "npc_dota_hero_axe"},
		{PersonID: "person-b0", TeamID: "team-b", HeroName: "npc_dota_hero_juggernaut"},
	}
	nf, err := Normalize(f, meta, mapping)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine on hero mismatch, got %s", nf.IdentityStatus)
	}
}