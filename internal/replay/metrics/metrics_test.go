package metrics

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
)

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
// counts configured objective entities (tower/rax/ancient/fort/shrine/Roshan/
// Tormentor); lane/neutral creep damage is excluded attribution, never summed
// into the objective total.
func TestObjectiveDamageOnlyConfiguredTargets(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, seq int64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetName: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "npc_dota_goodguys_tower1_mid", 5000, 1))
	calc.Feed(dmg("a1", "npc_dota_goodguys_rax_melee_top", 3000, 2))
	calc.Feed(dmg("a1", "npc_dota_roshan", 2500, 3))
	// Lane/neutral creeps must NOT count as objective damage.
	calc.Feed(dmg("a1", "npc_dota_creep_lane", 999999, 4))
	calc.Feed(dmg("a1", "npc_dota_neutral", 888888, 5))
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
	if *obj.Value != 10500 {
		t.Fatalf("objective_damage_total=%v want 10500 (tower+rax+roshan only)", *obj.Value)
	}
	if obj.ExcludedCount != 1888887 {
		t.Fatalf("excluded=%d want 1888887 (creep+neutral damage preserved separately)", obj.ExcludedCount)
	}
	if len(obj.EvidenceIDs) == 0 {
		t.Fatal("objective_damage_total has no evidence lineage")
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
	calc.Feed(pos(0.0, 1))
	calc.Feed(pos(0.4, 2))
	calc.Feed(pos(0.6, 3))
	calc.Feed(pos(1.0, 4))
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
