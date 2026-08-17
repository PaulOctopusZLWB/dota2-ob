package scoring

import (
	"math"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
)

func testContract(t *testing.T) *Contract {
	t.Helper()
	c, err := LoadContract("../../../docs/specs/ti2026-radar-scoring-v1.json")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	return c
}

func testMetricReg(t *testing.T) *metrics.Registry {
	t.Helper()
	reg, err := metrics.LoadRegistry("../../../docs/specs/ti2026-role-phase-metrics-v1.json")
	if err != nil {
		t.Fatalf("load metric registry: %v", err)
	}
	return reg
}

func testTeamContract(t *testing.T) *TeamContract {
	t.Helper()
	tc, err := LoadTeamContract("../../../docs/specs/ti2026-team-scoring-v1.json")
	if err != nil {
		t.Fatalf("load team registry: %v", err)
	}
	return tc
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
	pct := MidRankPercentile(25, []float64{10, 20, 30, 40})
	if math.Abs(pct-50) > 1e-9 {
		t.Fatalf("pct=%f want 50", pct)
	}
	pct = MidRankPercentile(20, []float64{10, 20, 20, 40})
	if math.Abs(pct-50) > 1e-9 {
		t.Fatalf("tie pct=%f want 50", pct)
	}
	pct = MidRankPercentile(10, []float64{10, 20, 30, 40})
	if math.Abs(pct-12.5) > 1e-9 {
		t.Fatalf("min pct=%f want 12.5", pct)
	}
	if !math.IsNaN(MidRankPercentile(1, nil)) {
		t.Fatal("empty cohort should be NaN")
	}
}

func fptr(v float64) *float64 { return &v }

func mkPlayer(match, acct, team, role string, metrics map[string]float64, nums, dens map[string]float64) *PlayerMatch {
	mv := map[string]MetricValue{}
	for id, v := range metrics {
		d := HigherBetter
		off := true
		exp := false
		if id == "death_without_buyback_exposure" {
			d = LowerBetter
		}
		m := MetricValue{MetricID: id, Value: v, Direction: d, OfficialEligible: off, ExperimentalEligible: exp}
		if n, ok := nums[id]; ok {
			m.Numerator = fptr(n)
		}
		if dn, ok := dens[id]; ok {
			m.Denominator = fptr(dn)
		}
		mv[id] = m
	}
	return &PlayerMatch{MatchID: match, AccountID: acct, TeamID: team, NominalRole: role, Metrics: mv}
}

// rateMetrics returns a full official-eligible metric set with numerator and
// denominator so rate aggregation can pool them.
func rateMetrics(vals map[string]float64) (map[string]float64, map[string]float64, map[string]float64) {
	nums := map[string]float64{}
	dens := map[string]float64{}
	for id, v := range vals {
		nums[id] = v * 10
		dens[id] = 100
	}
	return vals, nums, dens
}

// TestScorePlayerPublishesWhenAllComponentsPresent builds a corpus where a
// role's official-eligible metrics are all published and the subject plays
// three eligible matches; the player-tournament official total must publish
// exactly from the contract weights and aggregated percentiles.
func TestScorePlayerPublishesWhenAllComponentsPresent(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	// Subject a1 plays three matches; other players give a multi-member
	// same-role cohort.
	all := []map[string]float64{
		{"lane_pressure_damage_per_contact": 10, "item_timing_opportunity_percentile": 20, "key_ability_window_conversion": 30,
			"observed_map_exchange_outcome_rate": 40, "ward_lifetime_share": 50, "fight_damage_share": 60,
			"control_duration_per_opportunity": 70, "resource_to_objective_conversion": 80, "highground_building_conversion": 90,
			"death_without_buyback_exposure": 10, "buyback_round_participation": 20},
		{"lane_pressure_damage_per_contact": 20, "item_timing_opportunity_percentile": 30, "key_ability_window_conversion": 40,
			"observed_map_exchange_outcome_rate": 50, "ward_lifetime_share": 60, "fight_damage_share": 70,
			"control_duration_per_opportunity": 80, "resource_to_objective_conversion": 90, "highground_building_conversion": 10,
			"death_without_buyback_exposure": 20, "buyback_round_participation": 30},
		{"lane_pressure_damage_per_contact": 30, "item_timing_opportunity_percentile": 40, "key_ability_window_conversion": 50,
			"observed_map_exchange_outcome_rate": 60, "ward_lifetime_share": 70, "fight_damage_share": 80,
			"control_duration_per_opportunity": 90, "resource_to_objective_conversion": 10, "highground_building_conversion": 20,
			"death_without_buyback_exposure": 30, "buyback_round_participation": 40},
		{"lane_pressure_damage_per_contact": 40, "item_timing_opportunity_percentile": 50, "key_ability_window_conversion": 60,
			"observed_map_exchange_outcome_rate": 70, "ward_lifetime_share": 80, "fight_damage_share": 90,
			"control_duration_per_opportunity": 10, "resource_to_objective_conversion": 20, "highground_building_conversion": 30,
			"death_without_buyback_exposure": 40, "buyback_round_participation": 50},
		{"lane_pressure_damage_per_contact": 50, "item_timing_opportunity_percentile": 60, "key_ability_window_conversion": 70,
			"observed_map_exchange_outcome_rate": 80, "ward_lifetime_share": 90, "fight_damage_share": 10,
			"control_duration_per_opportunity": 20, "resource_to_objective_conversion": 30, "highground_building_conversion": 40,
			"death_without_buyback_exposure": 50, "buyback_round_participation": 60},
		{"lane_pressure_damage_per_contact": 60, "item_timing_opportunity_percentile": 70, "key_ability_window_conversion": 80,
			"observed_map_exchange_outcome_rate": 90, "ward_lifetime_share": 10, "fight_damage_share": 20,
			"control_duration_per_opportunity": 30, "resource_to_objective_conversion": 40, "highground_building_conversion": 50,
			"death_without_buyback_exposure": 60, "buyback_round_participation": 70},
	}
	// a1 plays m1/m2/m3 (3 eligible matches); cohort peers cover other roles.
	rows := []struct {
		match, acct string
		idx         int
	}{
		{"m1", "a1", 0},
		{"m2", "a1", 1},
		{"m3", "a1", 2},
		{"m1", "b1", 3},
		{"m2", "b1", 4},
		{"m3", "b1", 5},
		{"m1", "c1", 0},
		{"m2", "c1", 1},
		{"m3", "c1", 2},
	}
	players := []*PlayerMatch{}
	for _, r := range rows {
		m, n, d := rateMetrics(all[r.idx])
		players = append(players, mkPlayer(r.match, r.acct, "T1", "1", m, n, d))
	}
	cor := NewCorpus(c, mreg, players)
	if cor.MatchCount() != 3 {
		t.Fatalf("matches=%d", cor.MatchCount())
	}
	ps := cor.ScorePlayer("a1", "1")
	if ps == nil {
		t.Fatal("player score nil")
	}
	// Subject coverage reports the subject's own eligible matches.
	if ps.SubjectCoverage.EligibleMatches != 3 {
		t.Fatalf("subject matches=%d want 3", ps.SubjectCoverage.EligibleMatches)
	}
	if ps.SubjectCoverage.CorpusMatches != 3 {
		t.Fatalf("corpus matches=%d want 3", ps.SubjectCoverage.CorpusMatches)
	}
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
}

// TestAggregationUsesDeclaredRule proves fight_damage_share (rate) aggregates
// by sum numerator / sum denominator across the subject's matches.
func TestAggregationUsesDeclaredRule(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	// a1 plays two matches: 30/100 and 30/300 → pooled 60/400 = 0.15.
	players := []*PlayerMatch{
		mkPlayer("m1", "a1", "T1", "1", map[string]float64{"fight_damage_share": 0.3}, map[string]float64{"fight_damage_share": 30}, map[string]float64{"fight_damage_share": 100}),
		mkPlayer("m2", "a1", "T1", "1", map[string]float64{"fight_damage_share": 0.1}, map[string]float64{"fight_damage_share": 30}, map[string]float64{"fight_damage_share": 300}),
	}
	cor := NewCorpus(c, mreg, players)
	pt := cor.Tournament("a1", "1")
	if pt == nil {
		t.Fatal("tournament nil")
	}
	if pt.EligibleMatches != 2 {
		t.Fatalf("eligible matches=%d want 2", pt.EligibleMatches)
	}
	agg := pt.Metrics["fight_damage_share"]
	if math.Abs(agg.Value-0.15) > 1e-9 {
		t.Fatalf("aggregated fight_damage_share=%f want 0.15 (sum num/sum den)", agg.Value)
	}
	if math.Abs(agg.Numerator-60) > 1e-9 || math.Abs(agg.Denominator-400) > 1e-9 {
		t.Fatalf("aggregated num/den=%f/%f want 60/400", agg.Numerator, agg.Denominator)
	}
}

// TestSuppressionOnMandatoryMissing proves a missing mandatory component
// suppresses the axis and the total; no 0/50 is imputed.
func TestSuppressionOnMandatoryMissing(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	players := []*PlayerMatch{}
	rows := []struct {
		match, acct string
		share, ctrl float64
	}{
		{"m1", "a1", 10, 20},
		{"m2", "a2", 30, 40},
		{"m3", "b1", 50, 60},
	}
	for _, r := range rows {
		m, n, d := rateMetrics(map[string]float64{"fight_damage_share": r.share, "control_duration_per_opportunity": r.ctrl})
		players = append(players, mkPlayer(r.match, r.acct, "T1", "4", m, n, d))
	}
	cor := NewCorpus(c, mreg, players)
	ps := cor.ScorePlayer("a1", "4")
	for _, axis := range c.AxisNames() {
		a := ps.OfficialAxes[axis]
		if axis == "fight" {
			if !a.Published {
				t.Fatalf("fight axis should publish: %s", a.Reason)
			}
		} else if a.Published {
			t.Fatalf("axis %s should be suppressed", axis)
		}
	}
	if ps.OfficialTotal == nil || ps.OfficialTotal.Published {
		t.Fatal("official total must be suppressed when mandatory axes missing")
	}
	for _, a := range ps.OfficialAxes {
		if !a.Published && a.Value != nil {
			t.Fatalf("suppressed axis %s has a value", a.Axis)
		}
	}
	if ps.OfficialTotal.Value != nil {
		t.Fatal("suppressed total has a value")
	}
}

// TestLineagePreservedThroughAggregation proves typed evidence references are
// retained and deduplicated through player-tournament aggregation.
func TestLineagePreservedThroughAggregation(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	players := []*PlayerMatch{
		mkPlayerWithLineage("m1", "a1", "T1", "1", "fight_damage_share", 0.3, 30, 100, []EvidenceRef{{MatchID: "m1", Kind: "episode", ID: "fight@100", SourceFactSeq: 7}}),
		mkPlayerWithLineage("m2", "a1", "T1", "1", "fight_damage_share", 0.1, 30, 300, []EvidenceRef{{MatchID: "m2", Kind: "episode", ID: "fight@200", SourceFactSeq: 8}}),
	}
	cor := NewCorpus(c, mreg, players)
	pt := cor.Tournament("a1", "1")
	if pt == nil {
		t.Fatal("tournament nil")
	}
	agg := pt.Metrics["fight_damage_share"]
	if len(agg.Lineage) != 2 {
		t.Fatalf("lineage len=%d want 2 (deduplicated across matches)", len(agg.Lineage))
	}
	if agg.Lineage[0].Kind != "episode" || agg.Lineage[0].MatchID == "" {
		t.Fatalf("lineage ref malformed: %+v", agg.Lineage[0])
	}
}

func mkPlayerWithLineage(match, acct, team, role, mid string, val, num, den float64, lineage []EvidenceRef) *PlayerMatch {
	pm := mkPlayer(match, acct, team, role,
		map[string]float64{mid: val},
		map[string]float64{mid: num},
		map[string]float64{mid: den})
	mv := pm.Metrics[mid]
	mv.Lineage = lineage
	pm.Metrics[mid] = mv
	return pm
}

// TestTeamScoreFromFrozenRegistry proves team-tournament scores are computed
// exactly from the frozen team scoring registry: the team snapshot exists, the
// fight axis publishes from team-pooled metrics using the registry's frozen
// component weights, and the team total is suppressed with explicit
// mandatory-axis reasons.
func TestTeamScoreFromFrozenRegistry(t *testing.T) {
	c := testContract(t)
	tc := testTeamContract(t)
	mreg := testMetricReg(t)
	rows := []struct {
		tid, match, acct, role string
		share, num, den        float64
	}{
		{"TA", "m1", "p1", "1", 0.5, 50, 100},
		{"TA", "m2", "p2", "1", 0.25, 25, 100},
		{"TA", "m3", "p3", "1", 0.75, 75, 100},
		{"TB", "m1", "p4", "1", 0.75, 75, 100},
		{"TB", "m2", "p5", "1", 0.5, 50, 100},
		{"TB", "m3", "p6", "1", 0.6, 60, 100},
		{"TC", "m1", "p7", "1", 0.4, 40, 100},
		{"TC", "m2", "p8", "1", 0.6, 60, 100},
		{"TC", "m3", "p9", "1", 0.5, 50, 100},
	}
	var players []*PlayerMatch
	for _, r := range rows {
		m, n, d := rateMetrics(map[string]float64{"fight_damage_share": r.share, "control_duration_per_opportunity": 0.5})
		players = append(players, mkPlayer(r.match, r.acct, r.tid, r.role, m, n, d))
	}
	cor := NewCorpusWithTeam(c, tc, mreg, players)
	ts := cor.ScoreTeam("TA")
	if ts == nil {
		t.Fatal("team score nil")
	}
	if ts.ScoringVersion != TeamSchemaVersion {
		t.Fatalf("team scoring version=%q want %q", ts.ScoringVersion, TeamSchemaVersion)
	}
	if ts.SubjectCoverage.EligibleMatches != 3 {
		t.Fatalf("team eligible matches=%d want 3", ts.SubjectCoverage.EligibleMatches)
	}
	// The fight axis publishes using the frozen registry's component weights.
	a := ts.OfficialAxes["fight"]
	if !a.Published {
		t.Fatalf("team fight axis not published: %s", a.Reason)
	}
	wantW := tc.OfficialAxisComponents["fight"]["fight_damage_share"]
	if gotW := a.Components["fight_damage_share"].Weight; math.Abs(gotW-wantW) > 1e-9 {
		t.Fatalf("fight_damage_share team weight=%f want frozen %f", gotW, wantW)
	}
	// The team total is suppressed with explicit mandatory-axis reasons.
	if ts.OfficialTotal == nil || ts.OfficialTotal.Published {
		t.Fatal("team total must be suppressed when mandatory team axes are unavailable")
	}
	found := false
	for _, reason := range ts.OfficialTotal.Reasons {
		if reason == "mandatory_axis_unavailable:laning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("team total reasons=%v want explicit mandatory_axis_unavailable", ts.OfficialTotal.Reasons)
	}
}

// TestExperimentalLayerSeparate proves V3 components never enter the official
// value and the dashed experimental layer is computed separately.
func TestExperimentalLayerSeparate(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	base := map[string]float64{
		"lane_pressure_damage_per_contact": 10, "item_timing_opportunity_percentile": 20, "key_ability_window_conversion": 30,
		"observed_map_exchange_outcome_rate": 40, "ward_lifetime_share": 50, "fight_damage_share": 60,
		"control_duration_per_opportunity": 70, "resource_to_objective_conversion": 80, "highground_building_conversion": 90,
		"death_without_buyback_exposure": 10, "buyback_round_participation": 20,
	}
	v3 := map[string]float64{"core_partner_protection_uptime": 90}
	var players []*PlayerMatch
	seeds := []int{0, 10, 20, 30, 40, 50}
	// pa plays three matches so its official total can publish; others give
	// cohort peers.
	rows := []struct {
		match, acct string
		idx         int
	}{
		{"m1", "pa", 0},
		{"m2", "pa", 1},
		{"m3", "pa", 2},
		{"m1", "pb", 3},
		{"m2", "pb", 4},
		{"m3", "pb", 5},
		{"m1", "pc", 0},
		{"m2", "pc", 1},
		{"m3", "pc", 2},
	}
	for _, r := range rows {
		m := map[string]float64{}
		for k, v := range base {
			m[k] = v + float64(seeds[r.idx])
		}
		for k, v := range v3 {
			m[k] = v
		}
		vm, n, d := rateMetrics(m)
		pm := mkPlayer(r.match, r.acct, "T1", "1", vm, n, d)
		mv := pm.Metrics["core_partner_protection_uptime"]
		mv.ExperimentalEligible = true
		mv.OfficialEligible = false
		pm.Metrics["core_partner_protection_uptime"] = mv
		players = append(players, pm)
	}
	cor := NewCorpus(c, mreg, players)
	ps := cor.ScorePlayer("pa", "1")
	laneOfficial := ps.OfficialAxes["laning"]
	if !laneOfficial.Published {
		t.Fatal("laning official not published")
	}
	expLane := ps.ExperimentalAxes["laning"]
	if !expLane.Published {
		t.Fatalf("experimental laning not published: %s", expLane.Reason)
	}
	if math.Abs(*expLane.Value-*laneOfficial.Value) < 1e-9 {
		t.Fatal("experimental and official laning must differ")
	}
	if ps.OfficialTotal == nil || !ps.OfficialTotal.Published {
		t.Fatal("official total should publish")
	}
	if ps.ExperimentalTotal == nil {
		t.Fatal("experimental total missing")
	}
}

func TestLowMatchCountSuppresses(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	players := []*PlayerMatch{
		mkPlayer("m1", "a1", "T1", "1", map[string]float64{"fight_damage_share": 10, "control_duration_per_opportunity": 20}, nil, nil),
	}
	cor := NewCorpus(c, mreg, players)
	ps := cor.ScorePlayer("a1", "1")
	if ps == nil {
		t.Fatal("player score nil")
	}
	if ps.OfficialTotal != nil && ps.OfficialTotal.Published {
		t.Fatal("official total must suppress when subject matches < minimum")
	}
}
