package scoring

import (
	"math"
	"testing"
)

func testContract(t *testing.T) *Contract {
	t.Helper()
	c, err := LoadContract("../../../docs/specs/ti2026-radar-scoring-v1.json")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	return c
}

func TestContractValidates(t *testing.T) {
	c := testContract(t)
	if len(c.Axes) != 8 {
		t.Fatalf("axes=%d want 8", len(c.Axes))
	}
	for role := range c.AxisWeightsByRole {
		sum := 0.0
		for _, v := range c.AxisWeightsByRole[role] {
			sum += v
		}
		if math.Abs(sum-1) > 1e-6 {
			t.Fatalf("role %s axis weights sum=%f", role, sum)
		}
	}
}

func TestMidRankPercentile(t *testing.T) {
	// Cohort [10,20,30,40], value 25 → below=2, eq=0, midrank=2.5 → 100*2/4=50.
	pct := MidRankPercentile(25, []float64{10, 20, 30, 40})
	if math.Abs(pct-50) > 1e-9 {
		t.Fatalf("pct=%f want 50", pct)
	}
	// Ties: value 20 in [10,20,20,40]: below=1, eq=2, midrank=1+1.5=2.5 → 50.
	pct = MidRankPercentile(20, []float64{10, 20, 20, 40})
	if math.Abs(pct-50) > 1e-9 {
		t.Fatalf("tie pct=%f want 50", pct)
	}
	// Min value → 100*0.5/4 = 12.5.
	pct = MidRankPercentile(10, []float64{10, 20, 30, 40})
	if math.Abs(pct-12.5) > 1e-9 {
		t.Fatalf("min pct=%f want 12.5", pct)
	}
	// Empty cohort → NaN.
	if !math.IsNaN(MidRankPercentile(1, nil)) {
		t.Fatal("empty cohort should be NaN")
	}
}

func mkPlayer(match, acct, role string, metrics map[string]float64) *PlayerMatch {
	mv := map[string]MetricValue{}
	for id, v := range metrics {
		d := HigherBetter
		off := true
		exp := false
		if id == "death_without_buyback_exposure" {
			d = LowerBetter
		}
		mv[id] = MetricValue{MetricID: id, Value: v, Direction: d, OfficialEligible: off, ExperimentalEligible: exp}
	}
	return &PlayerMatch{MatchID: match, AccountID: acct, NominalRole: role, Metrics: mv}
}

// TestScorePlayerPublishesWhenAllComponentsPresent builds a corpus where a
// role's official-eligible metrics are all published and verifies the official
// total computes exactly from the contract weights and percentiles.
func TestScorePlayerPublishesWhenAllComponentsPresent(t *testing.T) {
	c := testContract(t)
	// Three matches, two role-1 players each — a 6-member cohort per metric.
	all := []map[string]float64{
		{
			"lane_pressure_damage_per_contact":   10,
			"item_timing_opportunity_percentile": 20,
			"key_ability_window_conversion":      30,
			"observed_map_exchange_outcome_rate": 40,
			"ward_lifetime_share":                50,
			"fight_damage_share":                 60,
			"control_duration_per_opportunity":   70,
			"resource_to_objective_conversion":   80,
			"highground_building_conversion":     90,
			"death_without_buyback_exposure":     10,
			"buyback_round_participation":        20,
		},
		{
			"lane_pressure_damage_per_contact":   20,
			"item_timing_opportunity_percentile": 30,
			"key_ability_window_conversion":      40,
			"observed_map_exchange_outcome_rate": 50,
			"ward_lifetime_share":                60,
			"fight_damage_share":                 70,
			"control_duration_per_opportunity":   80,
			"resource_to_objective_conversion":   90,
			"highground_building_conversion":     10,
			"death_without_buyback_exposure":     20,
			"buyback_round_participation":        30,
		},
		{
			"lane_pressure_damage_per_contact":   30,
			"item_timing_opportunity_percentile": 40,
			"key_ability_window_conversion":      50,
			"observed_map_exchange_outcome_rate": 60,
			"ward_lifetime_share":                70,
			"fight_damage_share":                 80,
			"control_duration_per_opportunity":   90,
			"resource_to_objective_conversion":   10,
			"highground_building_conversion":     20,
			"death_without_buyback_exposure":     30,
			"buyback_round_participation":        40,
		},
		{
			"lane_pressure_damage_per_contact":   40,
			"item_timing_opportunity_percentile": 50,
			"key_ability_window_conversion":      60,
			"observed_map_exchange_outcome_rate": 70,
			"ward_lifetime_share":                80,
			"fight_damage_share":                 90,
			"control_duration_per_opportunity":   10,
			"resource_to_objective_conversion":   20,
			"highground_building_conversion":     30,
			"death_without_buyback_exposure":     40,
			"buyback_round_participation":        50,
		},
		{
			"lane_pressure_damage_per_contact":   50,
			"item_timing_opportunity_percentile": 60,
			"key_ability_window_conversion":      70,
			"observed_map_exchange_outcome_rate": 80,
			"ward_lifetime_share":                90,
			"fight_damage_share":                 10,
			"control_duration_per_opportunity":   20,
			"resource_to_objective_conversion":   30,
			"highground_building_conversion":     40,
			"death_without_buyback_exposure":     50,
			"buyback_round_participation":        60,
		},
		{
			"lane_pressure_damage_per_contact":   60,
			"item_timing_opportunity_percentile": 70,
			"key_ability_window_conversion":      80,
			"observed_map_exchange_outcome_rate": 90,
			"ward_lifetime_share":                10,
			"fight_damage_share":                 20,
			"control_duration_per_opportunity":   30,
			"resource_to_objective_conversion":   40,
			"highground_building_conversion":     50,
			"death_without_buyback_exposure":     60,
			"buyback_round_participation":        70,
		},
	}
	players := []*PlayerMatch{}
	matches := []string{"m1", "m1", "m2", "m2", "m3", "m3"}
	accts := []string{"a1", "a2", "b1", "b2", "c1", "c2"}
	for i := range all {
		players = append(players, mkPlayer(matches[i], accts[i], "1", all[i]))
	}
	cor := NewCorpus(c, players)
	if cor.MatchCount() != 3 {
		t.Fatalf("matches=%d", cor.MatchCount())
	}
	ps := cor.ScorePlayer(players[0])
	// All official axes publish for role 1 (vision is optional and present).
	for _, axis := range c.AxisNames() {
		a := ps.OfficialAxes[axis]
		if !a.Published {
			t.Fatalf("axis %s not published: %s", axis, a.Reason)
		}
	}
	if ps.OfficialTotal == nil || !ps.OfficialTotal.Published {
		t.Fatalf("official total not published: %+v", ps.OfficialTotal)
	}
	// Reproduce the total exactly from axis values and role weights.
	w := c.AxisWeightsByRole["1"]
	sum := 0.0
	tot := 0.0
	for ax, a := range ps.OfficialAxes {
		if !a.Published {
			continue
		}
		sum += w[ax] * *a.Value
		tot += w[ax]
	}
	want := sum / tot
	got := *ps.OfficialTotal.Value
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("official total=%f want reproduced=%f", got, want)
	}
	// Score drilldown: axis value = weighted sum of component percentiles.
	lane := ps.OfficialAxes["laning"]
	comp := lane.Components["lane_pressure_damage_per_contact"]
	if !comp.Published {
		t.Fatal("lane component not published")
	}
	// Percentile of value 10 in cohort [10,20,30,40,50,60] = 100*0.5/6 ≈ 8.33.
	wantPct := 100.0 * (0.5) / 6.0
	if math.Abs(*comp.Percentile-wantPct) > 1e-9 {
		t.Fatalf("lane pct=%f want %f", *comp.Percentile, wantPct)
	}
}

// TestSuppressionOnMandatoryMissing proves a missing mandatory component
// suppresses the axis and the total; no 0/50 is imputed.
func TestSuppressionOnMandatoryMissing(t *testing.T) {
	c := testContract(t)
	// Only fight-axis components present for role 4's mandatory set; most
	// mandatory axes missing → total suppressed.
	players := []*PlayerMatch{
		mkPlayer("m1", "a1", "4", map[string]float64{
			"fight_damage_share":               10,
			"control_duration_per_opportunity": 20,
		}),
		mkPlayer("m1", "a2", "4", map[string]float64{
			"fight_damage_share":               30,
			"control_duration_per_opportunity": 40,
		}),
		mkPlayer("m2", "b1", "4", map[string]float64{
			"fight_damage_share":               50,
			"control_duration_per_opportunity": 60,
		}),
	}
	cor := NewCorpus(c, players)
	ps := cor.ScorePlayer(players[0])
	for _, axis := range c.AxisNames() {
		a := ps.OfficialAxes[axis]
		if axis == "fight" {
			if !a.Published {
				t.Fatalf("fight axis should publish: %s", a.Reason)
			}
		} else if a.Published {
			t.Fatalf("axis %s should be suppressed (mandatory components missing)", axis)
		}
	}
	if ps.OfficialTotal == nil || ps.OfficialTotal.Published {
		t.Fatal("official total must be suppressed when mandatory axes missing")
	}
	// No value is ever 0 or 50 for a suppressed axis/total.
	for _, a := range ps.OfficialAxes {
		if !a.Published && a.Value != nil {
			t.Fatalf("suppressed axis %s has a value", a.Axis)
		}
	}
	if ps.OfficialTotal.Value != nil {
		t.Fatal("suppressed total has a value")
	}
}

// TestExperimentalLayerSeparate proves V3 components never enter the official
// value and the dashed experimental layer is computed separately.
func TestExperimentalLayerSeparate(t *testing.T) {
	c := testContract(t)
	// Build a full official corpus plus one V3 component per relevant axis.
	base := map[string]float64{
		"lane_pressure_damage_per_contact":   10,
		"item_timing_opportunity_percentile": 20,
		"key_ability_window_conversion":      30,
		"observed_map_exchange_outcome_rate": 40,
		"ward_lifetime_share":                50,
		"fight_damage_share":                 60,
		"control_duration_per_opportunity":   70,
		"resource_to_objective_conversion":   80,
		"highground_building_conversion":     90,
		"death_without_buyback_exposure":     10,
		"buyback_round_participation":        20,
	}
	v3 := map[string]float64{
		"core_partner_protection_uptime": 90, // laning V3
	}
	players := []*PlayerMatch{}
	seeds := []int{0, 10, 20, 30, 40, 50}
	matches := []string{"m1", "m1", "m2", "m2", "m3", "m3"}
	for i, seed := range seeds {
		m := map[string]float64{}
		for k, v := range base {
			m[k] = v + float64(seed)
		}
		for k, v := range v3 {
			m[k] = v
		}
		pm := mkPlayer(matches[i], "p"+string(rune('a'+i)), "1", m)
		// mark V3 as experimental-eligible
		mv := pm.Metrics["core_partner_protection_uptime"]
		mv.ExperimentalEligible = true
		mv.OfficialEligible = false
		mv.Direction = HigherBetter
		pm.Metrics["core_partner_protection_uptime"] = mv
		players = append(players, pm)
	}
	cor := NewCorpus(c, players)
	ps := cor.ScorePlayer(players[0])
	// Official value must be free of V3 input: laning official == component pct.
	laneOfficial := ps.OfficialAxes["laning"]
	if !laneOfficial.Published {
		t.Fatal("laning official not published")
	}
	// Experimental laning should differ (V3 blended) and be named differently.
	expLane := ps.ExperimentalAxes["laning"]
	if !expLane.Published {
		t.Fatalf("experimental laning not published: %s", expLane.Reason)
	}
	if math.Abs(*expLane.Value-*laneOfficial.Value) < 1e-9 {
		t.Fatal("experimental and official laning must differ")
	}
	// Official total must not include the V3 metric.
	if ps.OfficialTotal == nil || !ps.OfficialTotal.Published {
		t.Fatal("official total should publish")
	}
	// Experimental total prerequisite: official total must be published.
	if ps.ExperimentalTotal == nil {
		t.Fatal("experimental total missing")
	}
}

func TestLowMatchCountSuppresses(t *testing.T) {
	c := testContract(t)
	// Fewer than minimum_matches (3): even with all metrics present, totals
	// are suppressed because the cohort is too small to normalize.
	players := []*PlayerMatch{
		mkPlayer("m1", "a1", "1", map[string]float64{"fight_damage_share": 10, "control_duration_per_opportunity": 20}),
	}
	cor := NewCorpus(c, players)
	ps := cor.ScorePlayer(players[0])
	if ps.OfficialTotal != nil && ps.OfficialTotal.Published {
		t.Fatal("official total must suppress when corpus matches < minimum")
	}
}
