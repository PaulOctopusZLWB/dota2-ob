package facts

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
)

// testClock returns a calibrated clock with anchor at combat TS 120, tick 1200.
func testClock(t *testing.T) *clock.Clock {
	c := clock.Build([]clock.Transition{
		{State: clock.StateGameInProgress, CombatTS: 120, Tick: 1200},
		{State: clock.StatePostgame, CombatTS: 1200, Tick: 12000},
	}, clock.BuildOptions{PublicDurationSeconds: 1080})
	if c.State != clock.StateCalibrated {
		t.Fatalf("clock not calibrated: %s %s", c.State, c.Reason)
	}
	return c
}

func testIdentity(t *testing.T) *identity.Identity {
	return &identity.Identity{
		SchemaVersion: "replay.identity.v1",
		MatchID:       "1000000001",
		State:         identity.StateVerified,
		GameMode:      22,
		GameBuild:     6902,
		Participants: []identity.Participant{
			{Slot: 0, AccountID: "1000", PlayerName: "p0", HeroName: "npc_dota_hero_antimage", HeroID: 1, Side: "radiant", Team: 2},
			{Slot: 1, AccountID: "1001", PlayerName: "p1", HeroName: "npc_dota_hero_axe", HeroID: 2, Side: "radiant", Team: 2},
			{Slot: 2, AccountID: "1002", PlayerName: "p2", HeroName: "npc_dota_hero_bane", HeroID: 3, Side: "radiant", Team: 2},
			{Slot: 3, AccountID: "1003", PlayerName: "p3", HeroName: "npc_dota_hero_crystal_maiden", HeroID: 4, Side: "radiant", Team: 2},
			{Slot: 4, AccountID: "1004", PlayerName: "p4", HeroName: "npc_dota_hero_drow_ranger", HeroID: 5, Side: "radiant", Team: 2},
			{Slot: 5, AccountID: "2000", PlayerName: "p5", HeroName: "npc_dota_hero_earthshaker", HeroID: 6, Side: "dire", Team: 3},
			{Slot: 6, AccountID: "2001", PlayerName: "p6", HeroName: "npc_dota_hero_juggernaut", HeroID: 7, Side: "dire", Team: 3},
			{Slot: 7, AccountID: "2002", PlayerName: "p7", HeroName: "npc_dota_hero_nevermore", HeroID: 11, Side: "dire", Team: 3},
			{Slot: 8, AccountID: "2003", PlayerName: "p8", HeroName: "npc_dota_hero_mirana", HeroID: 9, Side: "dire", Team: 3},
			{Slot: 9, AccountID: "2004", PlayerName: "p9", HeroName: "npc_dota_hero_pudge", HeroID: 14, Side: "dire", Team: 3},
		},
	}
}

// rawCombatEvent builds a combat raw event line.
func rawCombat(seq int64, ts float64, typ, attacker, target string) *raw.Event {
	a := uint32(1)
	t := uint32(2)
	e := &raw.Event{Kind: raw.KindCombat, Combat: &raw.Combat{
		Seq: seq, Type: typ, TypeID: 1, TS: ts, TSRaw: ts, Tick: uint32(ts * 10),
	}}
	if attacker != "" {
		e.Combat.AttackerIdx = &a
		e.Combat.AttackerName = attacker
	}
	if target != "" {
		e.Combat.TargetIdx = &t
		e.Combat.TargetName = target
	}
	return e
}

func TestBuildFactsBasic(t *testing.T) {
	clk := testClock(t)
	idn := testIdentity(t)
	b := NewBuilder(clk, idn)

var buf bytes.Buffer
	writer := raw.NewWriter(&buf)
	_ = writer.Write(rawCombat(1, 130, "DOTA_COMBATLOG_DEATH", "npc_dota_hero_antimage", "npc_dota_hero_axe"))
	_ = writer.Write(rawCombat(2, 131, "DOTA_COMBATLOG_GOLD", "npc_dota_hero_antimage", ""))
	_ = writer.Write(rawCombat(3, 132, "DOTA_COMBATLOG_PURCHASE", "", "npc_dota_hero_antimage"))
	writer.Flush()

	sum, err := b.Build(raw.NewReader(&buf), func(f *Fact) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if sum.MatchID != "1000000001" {
		t.Fatalf("match id %s", sum.MatchID)
	}
	// The summary should include participant + match_state families.
	fams := map[string]bool{}
	for _, c := range sum.Families {
		fams[c.Family] = true
	}
	if !fams[FamilyMatchState] || !fams[FamilyParticipant] {
		t.Fatalf("missing base families: %v", fams)
	}
	if !fams[FamilyDeathRespawn] || !fams[FamilyEconomy] || !fams[FamilyItem] {
		t.Fatalf("missing event families: %v", fams)
	}
}

func TestFactReaderRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.Encode(&Fact{Seq: 1, Family: FamilyCombat, MatchID: "1000000001", Payload: json.RawMessage(`{"kind":"damage"}`)})
	enc.Encode(&Fact{Seq: 2, Family: FamilyHeroState, MatchID: "1000000001"})
	r := NewReader(&buf)
	f1, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if f1.Seq != 1 || f1.Family != FamilyCombat {
		t.Fatalf("f1=%+v", f1)
	}
	f2, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if f2.Family != FamilyHeroState {
		t.Fatalf("f2=%+v", f2)
	}
	if _, err := r.Next(); err == nil || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestClassifyBuilding(t *testing.T) {
	kind, tier, lane, team := classifyBuilding("badguys_tower1_mid", ptr(3), ptr(2))
	if kind != "tower" || team != "dire" || lane != "mid" || tier == nil || *tier != 1 {
		t.Fatalf("got kind=%s tier=%v lane=%s team=%s", kind, tier, lane, team)
	}
	kind, tier, lane, team = classifyBuilding("goodguys_rax_melee_top", nil, nil)
	if kind != "barracks" || team != "radiant" || lane != "top" {
		t.Fatalf("got kind=%s lane=%s team=%s", kind, lane, team)
	}
	kind, _, _, team = classifyBuilding("ancient", nil, nil)
	if kind != "ancient" {
		t.Fatalf("kind=%s", kind)
	}
}

func ptr(v uint32) *uint32 { return &v }