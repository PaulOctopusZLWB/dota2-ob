package replay

import (
	"sort"
	"testing"
)

func factsFor(t *testing.T, c *Collected) *ReplayFactsV1 {
	t.Helper()
	return BuildFacts(c)
}

func TestBuildFactsDeterministic(t *testing.T) {
	c := &Collected{
		GameBuild:    6896,
		ServerName:   "Valve Dota 2 Europe Server",
		LastTick:     152134,
		MaxTimestamp: 3297.5,
		MessageCounts: map[string]uint64{
			"CDemoPacket":          76058,
			"CDemoFullPacket":      85,
			"CNETMsg_Tick":         76144,
			"CSVCMsg_PacketEntities": 76143,
		},
		Combat: []CombatEvent{
			{Type: "DOTA_COMBATLOG_FIRST_BLOOD", Timestamp: 120.5, Attacker: "npc_dota_hero_lion", Target: "npc_dota_hero_tusk", Inflictor: "lion_impale"},
			{Type: "DOTA_COMBATLOG_DEATH", Timestamp: 120.5, Attacker: "npc_dota_hero_lion", Target: "npc_dota_hero_tusk"},
			{Type: "DOTA_COMBATLOG_PURCHASE", Timestamp: 200.0, Target: "npc_dota_hero_lion", Inflictor: "item_blink"},
			{Type: "DOTA_COMBATLOG_PURCHASE", Timestamp: 210.0, Target: "npc_dota_hero_lion", Inflictor: "item_blink"},
			{Type: "DOTA_COMBATLOG_DEATH", Timestamp: 250.0, Attacker: "npc_dota_hero_tusk", Target: "npc_dota_hero_lion"},
			{Type: "DOTA_COMBATLOG_GOLD", Timestamp: 300.0, Attacker: "npc_dota_hero_lion", Value: 60},
			{Type: "DOTA_COMBATLOG_XP", Timestamp: 310.0, Attacker: "npc_dota_hero_lion", Value: 25},
			{Type: "DOTA_COMBATLOG_BUYBACK", Timestamp: 900.0, Attacker: "npc_dota_hero_tusk"},
			{Type: "DOTA_COMBATLOG_TEAM_BUILDING_KILL", Timestamp: 950.0, Attacker: "npc_dota_hero_lion", BuildingType: 2},
			{Type: "DOTA_COMBATLOG_KILLSTREAK", Timestamp: 950.0, Attacker: "npc_dota_hero_lion", Value: 3},
			{Type: "DOTA_COMBATLOG_DEATH", Timestamp: 999.0, Attacker: "npc_dota_creep_badguys_melee", Target: "npc_dota_hero_tusk"},
		},
	}

	f1 := factsFor(t, c)
	f2 := factsFor(t, c)
	j1, err := f1.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json 1: %v", err)
	}
	j2, err := f2.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json 2: %v", err)
	}
	if string(j1) != string(j2) {
		t.Fatalf("facts not byte-identical across builds")
	}
	h1, _ := f1.Hash()
	h2, _ := f2.Hash()
	if h1 != h2 {
		t.Fatalf("hash mismatch: %s != %s", h1, h2)
	}
	if h1 == "" {
		t.Fatalf("empty hash")
	}

	if f1.CombatLogTotal != uint64(len(c.Combat)) {
		t.Fatalf("combat total %d want %d", f1.CombatLogTotal, len(c.Combat))
	}
	if f1.Meta.GameBuild != 6896 || f1.Meta.MaxCombatLogTimestampSec != 3297.5 {
		t.Fatalf("meta mismatch: %+v", f1.Meta)
	}

	byName := map[string]HeroFacts{}
	for _, h := range f1.Heroes {
		byName[h.Name] = h
	}
	lion := byName["npc_dota_hero_lion"]
	if lion.HeroKills != 1 {
		t.Fatalf("lion hero kills %d want 1", lion.HeroKills)
	}
	if lion.Deaths != 1 {
		t.Fatalf("lion deaths %d want 1", lion.Deaths)
	}
	if lion.PurchaseEvents != 2 {
		t.Fatalf("lion purchase events %d want 2", lion.PurchaseEvents)
	}
	if lion.PurchaseGold != 0 {
		t.Fatalf("lion purchase gold %d want 0 (test events have value 0)", lion.PurchaseGold)
	}
	if lion.BuildingKills != 1 {
		t.Fatalf("lion building kills %d want 1", lion.BuildingKills)
	}
	if lion.GoldEvents != 1 || lion.XpEvents != 1 {
		t.Fatalf("lion gold/xp events %d/%d want 1/1", lion.GoldEvents, lion.XpEvents)
	}
	tusk := byName["npc_dota_hero_tusk"]
	if tusk.Deaths != 2 {
		t.Fatalf("tusk deaths %d want 2", tusk.Deaths)
	}
	if tusk.Buybacks != 1 {
		t.Fatalf("tusk buybacks %d want 1", tusk.Buybacks)
	}
	if tusk.HeroKills != 1 {
		t.Fatalf("tusk hero kills %d want 1", tusk.HeroKills)
	}

	if f1.Availability.Available[0] != "match_header" {
		t.Fatalf("availability order changed")
	}

	types := []string{}
	for ty := range f1.CombatLogTypeCounts {
		types = append(types, ty)
	}
	sort.Strings(types)
	wantTypes := []string{
		"DOTA_COMBATLOG_BUYBACK", "DOTA_COMBATLOG_DEATH",
		"DOTA_COMBATLOG_FIRST_BLOOD", "DOTA_COMBATLOG_GOLD",
		"DOTA_COMBATLOG_KILLSTREAK", "DOTA_COMBATLOG_PURCHASE",
		"DOTA_COMBATLOG_TEAM_BUILDING_KILL", "DOTA_COMBATLOG_XP",
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("type counts %v want %v", types, wantTypes)
	}
	for i, w := range wantTypes {
		if types[i] != w {
			t.Fatalf("type %d %q want %q", i, types[i], w)
		}
	}

	if len(f1.Timeline) != 4 {
		t.Fatalf("timeline len %d want 4", len(f1.Timeline))
	}
	wantTimeline := []struct {
		Type      string
		Timestamp float64
	}{
		{"first_blood", 120.5},
		{"buyback", 900.0},
		{"building_kill", 950.0},
		{"killstreak", 950.0},
	}
	for i, w := range wantTimeline {
		if f1.Timeline[i].Type != w.Type || f1.Timeline[i].Timestamp != w.Timestamp {
			t.Fatalf("timeline[%d] = %+v want %+v", i, f1.Timeline[i], w)
		}
	}
}

func TestBuildFactsEmpty(t *testing.T) {
	f := factsFor(t, &Collected{MessageCounts: map[string]uint64{}})
	h, err := f.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == "" {
		t.Fatalf("empty hash")
	}
	if len(f.Heroes) != 0 || f.CombatLogTotal != 0 {
		t.Fatalf("empty facts not empty")
	}
}

func TestHeroPrefixFiltering(t *testing.T) {
	c := &Collected{Combat: []CombatEvent{
		{Type: "DOTA_COMBATLOG_DEATH", Attacker: "npc_dota_creep_badguys_melee", Target: "npc_dota_hero_lion"},
	}}
	f := factsFor(t, c)
	if len(f.Heroes) != 1 {
		t.Fatalf("expected 1 hero, got %d", len(f.Heroes))
	}
	if f.Heroes[0].Name != "npc_dota_hero_lion" {
		t.Fatalf("unexpected hero %s", f.Heroes[0].Name)
	}
}

func TestTimelineSortedStable(t *testing.T) {
	c := &Collected{Combat: []CombatEvent{
		{Type: "DOTA_COMBATLOG_KILLSTREAK", Timestamp: 100, Attacker: "npc_dota_hero_a", Value: 2},
		{Type: "DOTA_COMBATLOG_BUYBACK", Timestamp: 50, Attacker: "npc_dota_hero_b"},
		{Type: "DOTA_COMBATLOG_TEAM_BUILDING_KILL", Timestamp: 50, Attacker: "npc_dota_hero_a"},
	}}
	f := factsFor(t, c)
	if len(f.Timeline) != 3 {
		t.Fatalf("timeline len %d", len(f.Timeline))
	}
	if f.Timeline[0].Timestamp != 50 || f.Timeline[0].Type != "building_kill" {
		t.Fatalf("timeline not sorted: %+v", f.Timeline)
	}
	if f.Timeline[1].Timestamp != 50 || f.Timeline[1].Type != "buyback" {
		t.Fatalf("timeline not sorted tie: %+v", f.Timeline)
	}
	if f.Timeline[2].Timestamp != 100 {
		t.Fatalf("timeline not sorted last: %+v", f.Timeline)
	}
}