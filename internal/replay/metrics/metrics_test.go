package metrics

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
)

func rawReader(events []raw.Event) *raw.Reader {
	var buf bytes.Buffer
	w := raw.NewWriter(&buf)
	for i := range events {
		_ = w.Write(&events[i])
	}
	_ = w.Flush()
	return raw.NewReader(&buf)
}

// testRegistry returns a frozen 52-metric registry from the repo contract.
func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := LoadRegistry("../../../docs/specs/ti2026-role-phase-metrics-v1.json")
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestRegistryContract(t *testing.T) {
	reg := testRegistry(t)
	if len(reg.Metrics) != 52 {
		t.Fatalf("metrics=%d want 52", len(reg.Metrics))
	}
	for _, m := range reg.Metrics {
		if m.ID == "" || m.Name == "" || m.Unit == "" || m.AggregationRule == "" {
			t.Fatalf("incomplete metric: %+v", m)
		}
	}
	// Every official-eligible metric is V1 or V2.
	for _, m := range reg.Metrics {
		if m.OfficialScoreEligible && m.CapabilityLevel == CapabilityV3 {
			t.Fatalf("metric %s official-eligible but V3", m.ID)
		}
	}
}

func TestCalculatorAggregation(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("1000000001", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "1"})
	calc.SetTeamOfSide(map[string]string{"radiant": "T1", "dire": "T2"})
	feed := func(f *facts.Fact) { calc.Feed(f) }
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 10, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1", KillerAccount: "a2"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 20, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1", KillerAccount: "a2"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 30, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a2", KillerAccount: "a1"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 40, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "a1"})})
	v := int64(50)
	feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 15, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})
	feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 25, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})

	out := calc.Result(nil, nil)
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, v := range out.Values {
		if v.AccountID == "a1" {
			got[v.MetricID] = *v.Value
		}
	}
	// a1: 2 deaths (as victim), 1 kill (killed a2), 1 buyback, 100 hero damage.
	if got["death_count"] != 2 {
		t.Fatalf("a1 death_count=%v want 2", got["death_count"])
	}
	if got["kill_count"] != 1 {
		t.Fatalf("a1 kill_count=%v want 1", got["kill_count"])
	}
	if got["buyback_use_count"] != 1 {
		t.Fatalf("a1 buybacks=%v want 1", got["buybacks"])
	}
	if got["hero_damage_total"] != 100 {
		t.Fatalf("a1 hero_damage_total=%v want 100", got["hero_damage_total"])
	}
	// The V2/V3 opportunity metrics must resolve to explicit unavailable,
	// never fabricated zeros.
	for _, u := range out.Unavailable {
		if u.Value != nil || u.IntValue != nil {
			t.Fatalf("unavailable metric %s/%s has a value", u.AccountID, u.MetricID)
		}
		if u.UnavailableReason == "" {
			t.Fatalf("unavailable metric %s/%s has no reason", u.AccountID, u.MetricID)
		}
	}
}

func TestUnavailableNeverZero(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("1000000001", []string{"a1"}, map[string]string{"a1": "p1"}, map[string]string{"a1": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	out := calc.Result(nil, nil)
	for _, u := range out.Unavailable {
		if u.Value != nil || u.IntValue != nil {
			t.Fatalf("unavailable metric %s has a value", u.MetricID)
		}
		if u.UnavailableReason == "" {
			t.Fatalf("unavailable metric %s has no reason", u.MetricID)
		}
		if u.EpistemicClass != ClassUnavailable {
			t.Fatalf("unavailable metric %s class=%s", u.MetricID, u.EpistemicClass)
		}
	}
}

func TestFightDamageShare(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2", "b1"}, map[string]string{"a1": "p1", "a2": "p2", "b1": "p3"}, map[string]string{"a1": "T1", "a2": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, sec float64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: sec, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetAccount: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "b1", 100, 105))
	calc.Feed(dmg("a2", "b1", 100, 106))
	calc.Feed(dmg("b1", "a1", 100, 107))
	eps := &episodes.Output{
		Episodes: []episodes.Episode{
			{Kind: episodes.KindFight, StartGameSecond: 100, EndGameSecond: 120, Participants: []string{"a1", "a2", "b1"}, EvidenceIDs: []int64{101, 102, 103}},
		},
	}
	out := calc.Result(eps, nil)
	got := map[string]float64{}
	for _, v := range out.Values {
		if v.MetricID == "fight_damage_share" {
			got[v.AccountID] = *v.Value
		}
	}
	// T1 deals 200 inside the fight; a1 contributes 100 → share 0.5.
	if got["a1"] != 0.5 {
		t.Fatalf("a1 fight_damage_share=%v want 0.5", got["a1"])
	}
	if got["a2"] != 0.5 {
		t.Fatalf("a2 fight_damage_share=%v want 0.5", got["a2"])
	}
	// b1's own team damage inside fight is 100; b1 contributed 100 → 1.0.
	if got["b1"] != 1.0 {
		t.Fatalf("b1 fight_damage_share=%v want 1.0", got["b1"])
	}
}

func TestPhaseDuration(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1"}, map[string]string{"a1": "p1"}, map[string]string{"a1": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	ph := &phase.Output{Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 300},
		{GlobalPhase: phase.Midgame, StartGameSecond: 300, EndGameSecond: 600},
	}}
	out := calc.Result(nil, ph)
	found := false
	for _, v := range out.Values {
		if v.MetricID == "phase_duration_seconds" && v.ReportLevel == "match" {
			if *v.Value != 600 {
				t.Fatalf("phase_duration=%v want 600", *v.Value)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("phase_duration_seconds match-level value missing")
	}
}

// TestRateMetricsDenominatorReconciliation proves rate metrics publish
// numerator/denominator that reconcile: the published rate equals
// numerator/denominator and no denominator is fabricated.
// TestRegistryResolutionComplete proves every registry metric resolves to a
// published value or an explicit unavailable record — no missing branch.
func TestRegistryResolutionComplete(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "5"})
	v := int64(50)
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 15, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})
	out := calc.Result(nil, nil)
	resolved := map[string]bool{}
	for _, x := range out.Values {
		resolved[x.MetricID] = true
	}
	for _, x := range out.Unavailable {
		resolved[x.MetricID] = true
	}
	for _, m := range reg.Metrics {
		if !resolved[m.ID] {
			t.Fatalf("registry metric %s has no resolution branch", m.ID)
		}
	}
	if len(resolved) != 52 {
		t.Fatalf("resolved %d distinct metrics, want 52", len(resolved))
	}
}

// TestObjectiveDamageOnlyConfiguredTargets proves objective_damage_total only
// counts configured objective entities (tower/rax/ancient-fort/shrine/Roshan/
// Tormentor) per the explicit taxonomy; lane/neutral creeps are excluded
// attribution and excluded_count counts records, not damage magnitude.
func TestObjectiveDamageOnlyConfiguredTargets(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, seq int64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetName: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "npc_dota_goodguys_tower1_mid", 5000, 1))
	calc.Feed(dmg("a1", "npc_dota_badguys_melee_rax_mid", 3000, 2))
	calc.Feed(dmg("a1", "npc_dota_roshan", 2500, 3))
	// Neutral ancient-frog creeps MUST NOT count as objectives (the taxonomy
	// must not substring-match "ancient").
	calc.Feed(dmg("a1", "npc_dota_neutral_ancient_frog", 46511, 4))
	calc.Feed(dmg("a1", "npc_dota_neutral_ancient_frog_mage", 100, 5))
	// Lane/neutral creeps excluded.
	calc.Feed(dmg("a1", "npc_dota_creep_lane", 999999, 6))
	calc.Feed(dmg("a1", "npc_dota_neutral_centaur_khan", 888888, 7))
	// Roshan's banner is a summoned unit, not Roshan.
	calc.Feed(dmg("a1", "npc_dota_unit_roshans_banner", 1000, 8))
	out := calc.Result(nil, nil)
	var obj *Value
	for i := range out.Values {
		if out.Values[i].MetricID == "objective_damage_total" && out.Values[i].AccountID == "a1" {
			obj = &out.Values[i]
		}
	}
	if obj == nil {
		t.Fatal("objective_damage_total not published for a1")
	}
	// tower(5000)+rax(3000)+roshan(2500) = 10500; neutral ancient creeps,
	// lane/neutral creeps, and Roshan's banner are excluded.
	if *obj.Value != 10500 {
		t.Fatalf("objective_damage_total=%v want 10500", *obj.Value)
	}
	// excluded_count counts excluded RECORDS: ancient_frog, ancient_frog_mage,
	// creep_lane, centaur_khan, roshans_banner = 5 records.
	if obj.ExcludedCount != 5 {
		t.Fatalf("excluded_count=%d want 5 records", obj.ExcludedCount)
	}
	// excluded_damage carries the excluded magnitude separately.
	if obj.ExcludedDamage == nil || *obj.ExcludedDamage != 46511+100+999999+888888+1000 {
		t.Fatalf("excluded_damage=%v want 1936498", obj.ExcludedDamage)
	}
	if len(obj.EvidenceIDs) == 0 {
		t.Fatal("objective_damage_total has no evidence lineage")
	}
}

// TestClassifyObjectiveTargetTaxonomy locks the explicit objective taxonomy:
// neutral ancient creeps and summoned units are never objectives.
func TestClassifyObjectiveTargetTaxonomy(t *testing.T) {
	cases := []struct {
		name string
		want ObjectiveEntityKind
	}{
		{"npc_dota_goodguys_tower2_mid", ObjectiveTower},
		{"npc_dota_badguys_melee_rax_bot", ObjectiveBarracks},
		{"npc_dota_goodguys_fort", ObjectiveAncientFort},
		{"npc_dota_badguys_ancient", ObjectiveAncientFort},
		{"npc_dota_goodguys_shrine", ObjectiveShrine},
		{"npc_dota_roshan", ObjectiveRoshan},
		{"npc_dota_badguys_tormentor", ObjectiveTormentor},
		{"npc_dota_neutral_ancient_frog", ObjectiveUnknown},
		{"npc_dota_neutral_ancient_frog_mage", ObjectiveUnknown},
		{"npc_dota_creep_goodguys_melee", ObjectiveUnknown},
		{"npc_dota_unit_roshans_banner", ObjectiveUnknown},
		{"npc_dota_underlord_portal", ObjectiveUnknown},
		{"", ObjectiveUnknown},
	}
	for _, c := range cases {
		if got := classifyObjectiveTarget(c.name); got != c.want {
			t.Fatalf("classify(%q)=%v want %v", c.name, got, c.want)
		}
	}
}

// TestOpportunityDurationCountsSecondsNotSamples proves opportunity seconds
// counts distinct calibrated second-grid bins, so ~0.5s-cadence samples in the
// same second contribute one eligible second, never more.
func TestOpportunityDurationCountsSecondsNotSamples(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1"}, map[string]string{"a1": "p1"}, map[string]string{"a1": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	pos := func(sec float64, seq int64) *facts.Fact {
		x, y := 1.0, 2.0
		return &facts.Fact{Family: facts.FamilyHeroState, GameSecond: sec, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.HeroStateSample{AccountID: "a1", PosX: &x, PosY: &y})}
	}
	// Two samples at 0.0, one at 0.5, one at 1.0 → only seconds 0 and 1.
	// The sample at the exclusive phase end is outside the eligible window.
	calc.Feed(pos(0.0, 1))
	calc.Feed(pos(0.4, 2))
	calc.Feed(pos(0.6, 3))
	calc.Feed(pos(1.0, 4))
	calc.Feed(pos(2.0, 5))
	ph := &phase.Output{Intervals: []phase.Interval{{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 2}}}
	out := calc.Result(nil, ph)
	var v *Value
	for i := range out.Values {
		if out.Values[i].MetricID == "opportunity_duration_seconds" && out.Values[i].AccountID == "a1" {
			v = &out.Values[i]
		}
	}
	if v == nil {
		t.Fatal("opportunity_duration_seconds not published")
	}
	if *v.Value != 2 {
		t.Fatalf("opportunity_duration_seconds=%v want 2 distinct eligible seconds", *v.Value)
	}
	if v.Denominator == nil || *v.Denominator != 2 {
		t.Fatalf("opportunity_duration_seconds denominator=%v want 2", v.Denominator)
	}
	if *v.Value > *v.Denominator {
		t.Fatalf("opportunity_duration_seconds value=%v exceeds denominator=%v", *v.Value, *v.Denominator)
	}
	if v.ExcludedCount != 1 {
		t.Fatalf("opportunity_duration_seconds excluded=%d want 1 out-of-window bin", v.ExcludedCount)
	}

	// Position samples without a validated official-phase denominator fail
	// closed instead of publishing sample-derived opportunity seconds.
	noPhase := NewCalculator("m2", []string{"a1"}, map[string]string{"a1": "p1"}, map[string]string{"a1": "T1"})
	noPhase.SetRegistry(reg)
	noPhase.SetRoles(map[string]string{"a1": "1"})
	noPhase.Feed(pos(0.0, 6))
	noPhaseOut := noPhase.Result(nil, nil)
	for _, u := range noPhaseOut.Unavailable {
		if u.AccountID == "a1" && u.MetricID == "opportunity_duration_seconds" {
			if u.UnavailableReason != "eligible_phase_denominator_missing" {
				t.Fatalf("opportunity_duration_seconds unavailable reason=%q", u.UnavailableReason)
			}
			return
		}
	}
	t.Fatal("opportunity_duration_seconds did not fail closed without phase denominator")
}

// TestPublishedEvidenceLineageNonEmpty proves every published value carries a
// non-empty evidence_ids chain (fact/episode/phase lineage).
func TestPublishedEvidenceLineageNonEmpty(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, seq int64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetAccount: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "b1", 100, 1))
	eps := &episodes.Output{Episodes: []episodes.Episode{
		{Kind: episodes.KindFight, StartGameSecond: 90, EndGameSecond: 120, Participants: []string{"a1", "b1"}, EvidenceIDs: []int64{1}},
	}}
	ph := &phase.Output{Intervals: []phase.Interval{{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 200, EvidenceSeqs: []int64{1}}}}
	out := calc.Result(eps, ph)
	for _, v := range out.Values {
		if v.ReportLevel == "player" && len(v.EvidenceIDs) == 0 {
			t.Fatalf("published metric %s/%s has empty evidence_ids", v.AccountID, v.MetricID)
		}
	}
}

// TestResolutionTableComplete proves the output carries a 52-row resolution
// table (one per registry metric) distinguishing published from unavailable.
func TestResolutionTableComplete(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	calc.SetFactsCoverage([]string{"combat_event"})
	v := int64(50)
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 15, Seq: 1, SourceSeq: 1, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "b1", Value: &v})})
	out := calc.Result(nil, nil)
	if len(out.ResolutionTable) != 52 {
		t.Fatalf("resolution table rows=%d want 52", len(out.ResolutionTable))
	}
	pub := 0
	for _, r := range out.ResolutionTable {
		if r.MetricID == "" {
			t.Fatal("resolution row missing metric id")
		}
		if r.Published {
			pub++
		} else if r.UnavailableReason == "" {
			t.Fatalf("unavailable metric %s missing reason", r.MetricID)
		}
	}
	if pub == 0 {
		t.Fatal("no published metrics")
	}
}

func TestRateMetricsDenominatorReconciliation(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, sec float64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: sec, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetAccount: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "b1", 100, 105))
	calc.Feed(dmg("a1", "b1", 100, 108))
	calc.Feed(dmg("b1", "a1", 50, 106))
	eps := &episodes.Output{Episodes: []episodes.Episode{
		{Kind: episodes.KindFight, StartGameSecond: 100, EndGameSecond: 120, Participants: []string{"a1", "b1"}, EvidenceIDs: []int64{101, 102}},
	}}
	out := calc.Result(eps, nil)
	byAcct := map[string]map[string]Value{}
	for _, v := range out.Values {
		if byAcct[v.AccountID] == nil {
			byAcct[v.AccountID] = map[string]Value{}
		}
		byAcct[v.AccountID][v.MetricID] = v
	}
	// a1: 200 damage in fight, T1 team total 200 → share 1.0 with
	// numerator=200 denominator=200.
	share := byAcct["a1"]["fight_damage_share"]
	if share.Numerator == nil || share.Denominator == nil {
		t.Fatalf("fight_damage_share missing numerator/denominator: %+v", share)
	}
	if *share.Denominator <= 0 || *share.Numerator > *share.Denominator {
		t.Fatalf("fight_damage_share numerator exceeds denominator: %+v", share)
	}
	if math.Abs(*share.Value-(*share.Numerator / *share.Denominator)) > 1e-9 {
		t.Fatalf("share %f != numerator/denominator %f", *share.Value, *share.Numerator / *share.Denominator)
	}
	// An account with no fight participation must NOT get a fabricated 0.
	if v, ok := byAcct["a1"]["lane_pressure_damage_per_contact"]; ok {
		t.Fatalf("lane_pressure_damage_per_contact published without geometry inputs: %+v", v)
	}
}

// TestAdditionalV1Metrics proves raw heal ticks and smoke modifier additions do
// not publish the registry's opportunity-normalized cast/ratio metrics, while
// buyback round participation still publishes from accepted facts.
func TestAdditionalV1Metrics(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	calc.SetFactsCoverage([]string{"combat_event", "modifier_event", "death_respawn_buyback_event"})
	h := int64(100)
	d := int64(50)
	// A raw heal fact may be a regen tick; it does not prove a ready-source cast.
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 10, Seq: 1, SourceSeq: 1, Payload: mustJSON(&facts.CombatFact{Kind: "heal", ActorAccount: "a1", TargetAccount: "b1", Value: &h})})
	// Smoke modifier additions do not provide the charge/member denominator
	// required by the registry's ratio definition.
	m := "modifier_smoke_of_deceit"
	calc.Feed(&facts.Fact{Family: facts.FamilyModifier, GameSecond: 20, Seq: 2, SourceSeq: 2, Payload: mustJSON(&facts.ModifierFact{Kind: "add", Modifier: m, AccountID: "a1"})})
	calc.Feed(&facts.Fact{Family: facts.FamilyModifier, GameSecond: 20, Seq: 3, SourceSeq: 3, Payload: mustJSON(&facts.ModifierFact{Kind: "add", Modifier: m, AccountID: "b1"})})
	// buyback on a1 at 100, post-buyback damage at 130 (within 60s)
	calc.Feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 100, Seq: 4, SourceSeq: 4, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "a1"})})
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 130, Seq: 5, SourceSeq: 5, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "b1", Value: &d})})
	out := calc.Result(nil, nil)
	got := map[string]map[string]Value{}
	for _, v := range out.Values {
		if got[v.AccountID] == nil {
			got[v.AccountID] = map[string]Value{}
		}
		got[v.AccountID][v.MetricID] = v
	}
	unavailable := map[string]map[string]Value{}
	for _, u := range out.Unavailable {
		if unavailable[u.AccountID] == nil {
			unavailable[u.AccountID] = map[string]Value{}
		}
		unavailable[u.AccountID][u.MetricID] = u
	}
	if v, ok := got["a1"]["heal_dispel_save_casts"]; ok {
		t.Fatalf("raw heal fact published as heal_dispel_save_casts: %+v", v)
	}
	if u := unavailable["a1"]["heal_dispel_save_casts"]; u.UnavailableReason != "save_cast_opportunity_gate_not_met:requires_ready_source_cooldown_target_need_evidence_not_in_accepted_adapter" {
		t.Fatalf("heal_dispel_save_casts unavailable=%+v", u)
	}
	for _, acct := range []string{"a1", "b1"} {
		if v, ok := got[acct]["smoke_activation_participation"]; ok {
			t.Fatalf("raw smoke modifier published as ratio for %s: %+v", acct, v)
		}
		if u := unavailable[acct]["smoke_activation_participation"]; u.UnavailableReason != "smoke_charge_opportunity_gate_not_met:requires_smoke_charge_inventory_and_eligible_member_denominator_not_in_accepted_adapter" {
			t.Fatalf("smoke_activation_participation unavailable for %s=%+v", acct, u)
		}
	}
	bb := got["a1"]["buyback_round_participation"]
	if bb.Value == nil || *bb.Value != 1.0 {
		t.Fatalf("buyback_round_participation a1=%+v want 1.0 (1/1 participated)", bb)
	}
}

// TestEconomyAttributionFromTarget proves GOLD/XP economy facts attribute to
// the hero carried in TargetName (the accepted adapter's shape).
func TestEconomyAttributionFromTarget(t *testing.T) {
	clk := &clock.Clock{State: clock.StateCalibrated, AnchorCombatTS: 0, GameDurationSeconds: f64ptr(300)}
	idn := &identity.Identity{MatchID: "m1", State: identity.StateVerified, Participants: []identity.Participant{
		{AccountID: "a1", HeroName: "npc_dota_hero_kez", Side: "radiant", Slot: 0},
	}}
	rawEv := []raw.Event{
		{Kind: raw.KindCombat, Combat: &raw.Combat{Type: "DOTA_COMBATLOG_GOLD", TargetName: "npc_dota_hero_kez", Value: int64p(500), Networth: uint32p(1000), LastHits: uint32p(42)}},
	}
	b := facts.NewBuilder(clk, idn)
	var got []facts.Fact
	_, err := b.Build(rawReader(rawEv), func(f *facts.Fact) error { got = append(got, *f); return nil })
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range got {
		if f.Family != facts.FamilyEconomy {
			continue
		}
		var es facts.EconomySample
		if err := json.Unmarshal(f.Payload, &es); err != nil {
			t.Fatal(err)
		}
		if es.AccountID == "a1" && es.Gold != nil && *es.Gold == 500 && es.LastHits != nil && *es.LastHits == 42 {
			found = true
		}
	}
	if !found {
		t.Fatal("economy fact not attributed to target hero account")
	}
}

func f64ptr(v float64) *float64 { return &v }
func int64p(v int64) *int64     { return &v }
func uint32p(v uint32) *uint32  { return &v }
