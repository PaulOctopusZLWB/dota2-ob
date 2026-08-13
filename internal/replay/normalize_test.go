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

// sampleFacts10 builds a replay whose combat log references exactly ten
// distinct heroes, so a full ten-player mapping can verify identity.
func sampleFacts10() *ReplayFactsV1 {
	heroes := []string{
		"npc_dota_hero_invoker", "npc_dota_hero_juggernaut", "npc_dota_hero_lina",
		"npc_dota_hero_axe", "npc_dota_hero_crystal_maiden", "npc_dota_hero_phantom_assassin",
		"npc_dota_hero_earthshaker", "npc_dota_hero_lion", "npc_dota_hero_sniper",
		"npc_dota_hero_drow_ranger",
	}
	c := &Collected{GameBuild: 6896, ServerName: "prod", LastTick: 100000, MaxTimestamp: 3297.0}
	for i := 0; i < len(heroes); i += 2 {
		c.Combat = append(c.Combat, CombatEvent{
			Type: "DOTA_COMBATLOG_DEATH", Attacker: heroes[i], Target: heroes[i+1],
		})
	}
	return BuildFacts(c)
}

func tenMapping() []ParticipantMapping {
	heroes := []string{
		"npc_dota_hero_invoker", "npc_dota_hero_juggernaut", "npc_dota_hero_lina",
		"npc_dota_hero_axe", "npc_dota_hero_crystal_maiden", "npc_dota_hero_phantom_assassin",
		"npc_dota_hero_earthshaker", "npc_dota_hero_lion", "npc_dota_hero_sniper",
		"npc_dota_hero_drow_ranger",
	}
	m := make([]ParticipantMapping, 10)
	for i, h := range heroes {
		team := "team-a"
		if i >= 5 {
			team = "team-b"
		}
		m[i] = ParticipantMapping{PersonID: "person-x" + string(rune('0'+i%10)), TeamID: team, HeroName: h, Role: "player", Slot: i}
	}
	return m
}

func baseMeta(build uint32) NormalizeMeta {
	return NormalizeMeta{
		MatchID: "m1", SourceEventTime: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PatchID: "60", ReplaySHA256: "1111111111111111111111111111111111111111111111111111111111111111",
		RadiantTeamID: "team-a", DireTeamID: "team-b", GameBuild: build,
	}
}

func TestNormalizeQuarantinedWithoutMapping(t *testing.T) {
	f := sampleFacts()
	nf, err := Normalize(f, baseMeta(6896), nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantined without mapping, got %s", nf.IdentityStatus)
	}
	if len(nf.Participants) != 0 {
		t.Fatalf("quarantined facts must have no participants, got %d", len(nf.Participants))
	}
}

func TestNormalizeVerifiedWithFullTenMapping(t *testing.T) {
	f := sampleFacts10()
	if len(f.Heroes) != 10 {
		t.Fatalf("expected 10 heroes in sample facts, got %d", len(f.Heroes))
	}
	nf, err := Normalize(f, baseMeta(6896), tenMapping())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityVerified {
		t.Fatalf("expected verified with full ten-player mapping, got %s", nf.IdentityStatus)
	}
	if len(nf.Participants) != 10 {
		t.Fatalf("expected 10 verified participants, got %d", len(nf.Participants))
	}
	// Determinism: identical input yields byte-identical content hash.
	nf2, err := Normalize(sampleFacts10(), baseMeta(6896), tenMapping())
	if err != nil {
		t.Fatalf("normalize 2: %v", err)
	}
	if nf.ContentSHA256 != nf2.ContentSHA256 {
		t.Fatalf("normalize not deterministic: %s != %s", nf.ContentSHA256, nf2.ContentSHA256)
	}
}

func TestNormalizeTwoHeroMappingQuarantines(t *testing.T) {
	f := sampleFacts() // two heroes
	// A two-player mapping is not a complete ten-player binding -> quarantine.
	mapping := []ParticipantMapping{
		{PersonID: "person-a0", TeamID: "team-a", HeroName: "npc_dota_hero_invoker", Role: "player", Slot: 0},
		{PersonID: "person-b0", TeamID: "team-b", HeroName: "npc_dota_hero_juggernaut", Role: "player", Slot: 5},
	}
	nf, err := Normalize(f, baseMeta(6896), mapping)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine for incomplete mapping, got %s", nf.IdentityStatus)
	}
}

func TestNormalizeDuplicateHeroQuarantines(t *testing.T) {
	f := sampleFacts10()
	m := tenMapping()
	// Duplicate a hero binding (re-use an already-bound hero) while keeping
	// length 10 — duplicate heroes must quarantine, not silently dedupe.
	m[9] = ParticipantMapping{PersonID: "person-x9dup", TeamID: "team-b", HeroName: "npc_dota_hero_invoker", Role: "player", Slot: 9}
	nf, err := Normalize(f, baseMeta(6896), m)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine on duplicate hero, got %s", nf.IdentityStatus)
	}
}

func TestNormalizeBuildMismatchQuarantines(t *testing.T) {
	f := sampleFacts10()   // build 6896
	meta := baseMeta(7000) // different build
	nf, err := Normalize(f, meta, tenMapping())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine on GameBuild mismatch, got %s", nf.IdentityStatus)
	}
}

func TestNormalizeMissingBuildQuarantines(t *testing.T) {
	f := sampleFacts10()
	nf, err := Normalize(f, baseMeta(0), tenMapping())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine when public game build is absent, got %s", nf.IdentityStatus)
	}
}

func TestNormalizeExtraParsedHeroQuarantines(t *testing.T) {
	f := sampleFacts10()
	f.Heroes = append(f.Heroes, HeroFacts{Name: "npc_dota_hero_pudge"})
	nf, err := Normalize(f, baseMeta(6896), tenMapping())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine when replay has more than ten parsed heroes, got %s", nf.IdentityStatus)
	}
}

func TestNormalizeInvalidSlotOrTeamQuarantines(t *testing.T) {
	f := sampleFacts10()
	m := tenMapping()
	m[0].Slot = 10
	nf, err := Normalize(f, baseMeta(6896), m)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine for invalid participant slot, got %s", nf.IdentityStatus)
	}

	m = tenMapping()
	m[0].TeamID = "team-b"
	nf, err = Normalize(f, baseMeta(6896), m)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine for slot/team mismatch, got %s", nf.IdentityStatus)
	}
}

func TestNormalizeMismappedHeroQuarantines(t *testing.T) {
	f := sampleFacts()
	mapping := []ParticipantMapping{
		{PersonID: "person-a0", TeamID: "team-a", HeroName: "npc_dota_hero_axe", Role: "player", Slot: 0},
		{PersonID: "person-b0", TeamID: "team-b", HeroName: "npc_dota_hero_juggernaut", Role: "player", Slot: 5},
	}
	nf, err := Normalize(f, baseMeta(6896), mapping)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if nf.IdentityStatus != contracts.IdentityQuarantined {
		t.Fatalf("expected quarantine on hero mismatch, got %s", nf.IdentityStatus)
	}
}
