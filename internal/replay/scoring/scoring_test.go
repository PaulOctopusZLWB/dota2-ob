package scoring

import (
	"fmt"
	"math"
	"sort"
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

// TestAggregationDedupMatchQualified proves equal entity ids from different
// matches never collapse during aggregation: the dedup key includes match id,
// so two identical fact/episode ids from distinct matches both survive.
func TestAggregationDedupMatchQualified(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	players := []*PlayerMatch{
		// Same account/role, same metric, SAME entity id (fact:7) and SAME
		// episode id across two different matches.
		mkPlayerWithLineage("m1", "a1", "T1", "1", "hero_damage_total", 500, 500, 1, []EvidenceRef{{MatchID: "m1", Kind: "fact", ID: "fact:7", SourceFactSeq: 7}}),
		mkPlayerWithLineage("m2", "a1", "T1", "1", "hero_damage_total", 800, 800, 1, []EvidenceRef{{MatchID: "m2", Kind: "fact", ID: "fact:7", SourceFactSeq: 7}}),
	}
	cor := NewCorpus(c, mreg, players)
	pt := cor.Tournament("a1", "1")
	if pt == nil {
		t.Fatal("tournament nil")
	}
	agg := pt.Metrics["hero_damage_total"]
	if len(agg.Lineage) != 2 {
		t.Fatalf("lineage len=%d want 2 (equal entity ids from different matches must stay distinct)", len(agg.Lineage))
	}
	seen := map[string]bool{}
	for _, ref := range agg.Lineage {
		if ref.MatchID == "" || ref.Kind == "" || ref.ID == "" {
			t.Fatalf("lineage ref malformed: %+v", ref)
		}
		k := ref.MatchID + "\x00" + ref.Kind + "\x00" + ref.ID
		if seen[k] {
			t.Fatalf("lineage dedup collapsed a match-qualified ref: %+v", ref)
		}
		seen[k] = true
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

// teamPlayer builds a PlayerMatch row carrying the full official-eligible team
// metric set for a team, all with the same raw value so the arithmetic is
// deterministic.
func teamPlayer(match, acct, team, role string, val float64, mids []string) *PlayerMatch {
	mv := map[string]MetricValue{}
	for _, id := range mids {
		d := HigherBetter
		if id == "death_without_buyback_exposure" {
			d = LowerBetter
		}
		n := val * 10
		den := 100.0
		mv[id] = MetricValue{MetricID: id, Value: val, Direction: d, OfficialEligible: true, ExperimentalEligible: false, Numerator: &n, Denominator: &den}
	}
	return &PlayerMatch{MatchID: match, AccountID: acct, TeamID: team, NominalRole: role, Metrics: mv}
}

// allTeamMetricIDs is the union of every metric referenced by the frozen team
// official axis components (all V2 official-eligible).
var allTeamMetricIDs = []string{
	"lane_pressure_damage_per_contact",
	"item_timing_opportunity_percentile", "stack_attempt_success_rate",
	"roam_conversion_rate", "rune_control_contribution", "key_ability_window_conversion",
	"observed_map_exchange_outcome_rate",
	"ward_lifetime_share",
	"fight_damage_share", "control_duration_per_opportunity",
	"resource_to_objective_conversion", "highground_building_conversion",
	"death_without_buyback_exposure", "buyback_round_participation",
}

// mandatoryOnlyTeamMetricIDs is allTeamMetricIDs minus the optional vision
// metric (ward_lifetime_share).
func mandatoryOnlyTeamMetricIDs() []string {
	out := []string{}
	for _, id := range allTeamMetricIDs {
		if id == "ward_lifetime_share" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// buildTeamCorpus builds a corpus with nTeams teams, each playing matches in
// rounds; each team has per-metric raw value valByTeam. Returns the corpus and
// a team id list.
func buildTeamCorpus(t *testing.T, teamVals map[string]float64, matchesPerTeam int, mids []string) *Corpus {
	t.Helper()
	c := testContract(t)
	tc := testTeamContract(t)
	mreg := testMetricReg(t)
	teamIDs := make([]string, 0, len(teamVals))
	for tid := range teamVals {
		teamIDs = append(teamIDs, tid)
	}
	sort.Strings(teamIDs)
	var players []*PlayerMatch
	acct := 0
	for mi := 0; mi < matchesPerTeam; mi++ {
		matchID := fmt.Sprintf("m%d", mi)
		for _, tid := range teamIDs {
			for role := 1; role <= 5; role++ {
				acct++
				players = append(players, teamPlayer(matchID, fmt.Sprintf("a%d", acct), tid, fmt.Sprintf("%d", role), teamVals[tid], mids))
			}
		}
	}
	return NewCorpusWithTeam(c, tc, mreg, players)
}

// TestTeamContractValidateWithRegistry proves the frozen team registry is
// valid against the metric registry and that unknown / cross-ineligible /
// duplicate / malformed variants fail closed.
func TestTeamContractValidateWithRegistry(t *testing.T) {
	tc := testTeamContract(t)
	mreg := testMetricReg(t)
	if err := tc.ValidateWithRegistry(mreg); err != nil {
		t.Fatalf("frozen team registry failed cross-registry validation: %v", err)
	}
	// Unknown metric fails.
	bad := *tc
	bad.OfficialAxisComponents = map[string]map[string]float64{}
	for k, v := range tc.OfficialAxisComponents {
		bad.OfficialAxisComponents[k] = v
	}
	bad.OfficialAxisComponents["laning"] = map[string]float64{"not_a_metric": 1.0}
	if err := bad.ValidateWithRegistry(mreg); err == nil {
		t.Fatal("unknown team metric accepted")
	}
	// V3 metric must not enter the official team layer.
	bad2 := *tc
	bad2.OfficialAxisComponents = map[string]map[string]float64{}
	for k, v := range tc.OfficialAxisComponents {
		bad2.OfficialAxisComponents[k] = v
	}
	bad2.OfficialAxisComponents["laning"] = map[string]float64{"core_partner_protection_uptime": 1.0}
	if err := bad2.ValidateWithRegistry(mreg); err == nil {
		t.Fatal("V3 metric accepted into official team layer")
	}
	// Duplicate axis fails.
	bad3 := *tc
	bad3.Axes = append(append([]AxisDef{}, tc.Axes...), AxisDef{ID: "laning"})
	if err := bad3.Validate(); err == nil {
		t.Fatal("duplicate axis accepted")
	}
	// Axis both mandatory and optional fails.
	bad4 := *tc
	bad4.OptionalAxes = append(append([]string{}, tc.OptionalAxes...), "laning")
	if err := bad4.Validate(); err == nil {
		t.Fatal("axis both mandatory and optional accepted")
	}
	// Missing canonical axis fails.
	bad5 := *tc
	bad5.Axes = bad5.Axes[:7]
	if err := bad5.Validate(); err == nil {
		t.Fatal("missing canonical axis accepted")
	}
}

// TestTeamOfficialTotalPublishesWithRenormalization proves: seven mandatory
// axes publish, vision (optional) is unavailable, and the official total
// publishes with the vision omission disclosed and the remaining weights
// renormalized exactly.
func TestTeamOfficialTotalPublishesWithRenormalization(t *testing.T) {
	teamVals := map[string]float64{"TA": 0.3, "TB": 0.5, "TC": 0.7}
	cor := buildTeamCorpus(t, teamVals, 3, mandatoryOnlyTeamMetricIDs())
	tc := testTeamContract(t)
	ts := cor.ScoreTeam("TA")
	if ts == nil {
		t.Fatal("team score nil")
	}
	// The optional vision axis is unavailable but disclosed.
	if !ts.OfficialAxes["vision"].Published {
		t.Logf("vision unavailable reason: %s", ts.OfficialAxes["vision"].Reason)
	}
	// Seven mandatory axes publish.
	mandatory := map[string]bool{}
	for _, a := range tc.MandatoryAxes {
		mandatory[a] = true
	}
	for ax := range mandatory {
		if !ts.OfficialAxes[ax].Published {
			t.Fatalf("mandatory axis %s not published: %s", ax, ts.OfficialAxes[ax].Reason)
		}
	}
	if ts.OfficialTotal == nil || !ts.OfficialTotal.Published {
		t.Fatalf("official total must publish with all mandatory axes; reasons=%v", ts.OfficialTotal.Reasons)
	}
	// Renormalization: total == sum(w*a)/sum(w for published axes), and the
	// optional vision weight must NOT be in the denominator.
	sum, wsum := 0.0, 0.0
	for _, ax := range ts.OfficialTotal.AxesIncluded {
		w := tc.AxisWeights[ax]
		sum += w * *ts.OfficialAxes[ax].Value
		wsum += w
	}
	want := sum / wsum
	if math.Abs(*ts.OfficialTotal.Value-want) > 1e-9 {
		t.Fatalf("official total=%v want renormalized %v", *ts.OfficialTotal.Value, want)
	}
	// Vision must be excluded from AxesIncluded and its weight excluded.
	for _, ax := range ts.OfficialTotal.AxesIncluded {
		if ax == "vision" {
			t.Fatal("optional vision axis wrongly included in total")
		}
	}
	if wsum >= 1.0-1e-9 {
		t.Fatalf("renormalized denominator must exclude vision; got %v (full weight sum would be 1.0)", wsum)
	}
}

// TestTeamOfficialTotalWithVisionUsesOriginalWeight proves available vision
// uses its original registry weight and changes the denominator exactly.
func TestTeamOfficialTotalWithVisionUsesOriginalWeight(t *testing.T) {
	teamVals := map[string]float64{"TA": 0.3, "TB": 0.5, "TC": 0.7}
	cor := buildTeamCorpus(t, teamVals, 3, allTeamMetricIDs)
	tc := testTeamContract(t)
	ts := cor.ScoreTeam("TA")
	if ts.OfficialTotal == nil || !ts.OfficialTotal.Published {
		t.Fatalf("official total must publish with all 8 axes; reasons=%v", ts.OfficialTotal.Reasons)
	}
	if !ts.OfficialAxes["vision"].Published {
		t.Fatalf("vision axis should publish with ward_lifetime_share present: %s", ts.OfficialAxes["vision"].Reason)
	}
	if got := ts.OfficialTotal.Weights["vision"]; math.Abs(got-tc.AxisWeights["vision"]) > 1e-9 {
		t.Fatalf("vision weight in total=%v want original %v", got, tc.AxisWeights["vision"])
	}
	// Denominator now includes the full weight set (vision weight).
	sum, wsum := 0.0, 0.0
	for _, ax := range ts.OfficialTotal.AxesIncluded {
		w := tc.AxisWeights[ax]
		sum += w * *ts.OfficialAxes[ax].Value
		wsum += w
	}
	if math.Abs(wsum-1.0) > 1e-9 {
		t.Fatalf("full weight denominator=%v want 1.0", wsum)
	}
	if math.Abs(*ts.OfficialTotal.Value-(sum/wsum)) > 1e-9 {
		t.Fatalf("official total=%v want %v", *ts.OfficialTotal.Value, sum/wsum)
	}
}

// TestTeamOfficialTotalMandatoryAxisSuppresses proves any one missing
// mandatory axis suppresses the total with a precise reason.
func TestTeamOfficialTotalMandatoryAxisSuppresses(t *testing.T) {
	// Omit lane_pressure_damage_per_contact => laning (mandatory) unavailable.
	partial := []string{}
	for _, id := range allTeamMetricIDs {
		if id == "lane_pressure_damage_per_contact" {
			continue
		}
		partial = append(partial, id)
	}
	teamVals := map[string]float64{"TA": 0.3, "TB": 0.5, "TC": 0.7}
	cor := buildTeamCorpus(t, teamVals, 3, partial)
	ts := cor.ScoreTeam("TA")
	if ts.OfficialTotal == nil || ts.OfficialTotal.Published {
		t.Fatal("official total must be suppressed when a mandatory axis is unavailable")
	}
	found := false
	for _, r := range ts.OfficialTotal.Reasons {
		if r == "mandatory_axis_unavailable:laning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons=%v want mandatory_axis_unavailable:laning", ts.OfficialTotal.Reasons)
	}
}

// TestTeamOfficialTotalFewerThanSixAxesSuppresses proves fewer than six
// published axes suppresses the total.
func TestTeamOfficialTotalFewerThanSixAxesSuppresses(t *testing.T) {
	// Only fight + objectives + endgame metrics present => 3 axes.
	partial := []string{"fight_damage_share", "resource_to_objective_conversion", "buyback_round_participation"}
	teamVals := map[string]float64{"TA": 0.3, "TB": 0.5, "TC": 0.7}
	cor := buildTeamCorpus(t, teamVals, 3, partial)
	ts := cor.ScoreTeam("TA")
	if ts.OfficialTotal == nil || ts.OfficialTotal.Published {
		t.Fatal("official total must be suppressed with fewer than six axes")
	}
	found := false
	for _, r := range ts.OfficialTotal.Reasons {
		if len(r) >= len("publishable_axes=") && r[:len("publishable_axes=")] == "publishable_axes=" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons=%v want publishable_axes reason", ts.OfficialTotal.Reasons)
	}
}

// TestTeamOfficialTotalFewerThanThreeMatchesSuppresses proves fewer than three
// eligible team matches suppresses the total.
func TestTeamOfficialTotalFewerThanThreeMatchesSuppresses(t *testing.T) {
	teamVals := map[string]float64{"TA": 0.3, "TB": 0.5, "TC": 0.7}
	cor := buildTeamCorpus(t, teamVals, 2, allTeamMetricIDs)
	ts := cor.ScoreTeam("TA")
	if ts.OfficialTotal == nil || ts.OfficialTotal.Published {
		t.Fatal("official total must be suppressed with fewer than three matches")
	}
	found := false
	for _, r := range ts.OfficialTotal.Reasons {
		if len(r) >= len("team_matches=") && r[:len("team_matches=")] == "team_matches=" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons=%v want team_matches reason", ts.OfficialTotal.Reasons)
	}
}

// TestTeamExperimentalLayerSuppressedAndIsolated proves the experimental team
// layer is exposed suppressed (no machine-readable recipe in the frozen
// registry), is separately named, and V3-only inputs never change official
// axes or totals.
func TestTeamExperimentalLayerSuppressedAndIsolated(t *testing.T) {
	c := testContract(t)
	tc := testTeamContract(t)
	mreg := testMetricReg(t)
	// Add a V3 experimental-only metric to the players; it must never enter
	// the official team layer.
	mv := map[string]MetricValue{}
	for _, id := range allTeamMetricIDs {
		n := 5.0
		mv[id] = MetricValue{MetricID: id, Value: 0.5, Direction: HigherBetter, OfficialEligible: true, ExperimentalEligible: false, Numerator: &n, Denominator: ptrF(10)}
	}
	mv["core_partner_protection_uptime"] = MetricValue{MetricID: "core_partner_protection_uptime", Value: 0.9, Direction: HigherBetter, OfficialEligible: false, ExperimentalEligible: true, Numerator: ptrF(9), Denominator: ptrF(10)}
	var players []*PlayerMatch
	acct := 0
	for mi := 0; mi < 3; mi++ {
		for _, tid := range []string{"TA", "TB", "TC"} {
			for role := 1; role <= 5; role++ {
				acct++
				players = append(players, &PlayerMatch{MatchID: fmt.Sprintf("m%d", mi), AccountID: fmt.Sprintf("a%d", acct), TeamID: tid, NominalRole: fmt.Sprintf("%d", role), Metrics: mv})
			}
		}
	}
	cor := NewCorpusWithTeam(c, tc, mreg, players)
	ts := cor.ScoreTeam("TA")
	if ts == nil {
		t.Fatal("team score nil")
	}
	if ts.ExperimentalAxes == nil || len(ts.ExperimentalAxes) == 0 {
		t.Fatal("experimental_axes must be present (suppressed interface)")
	}
	if ts.ExperimentalTotal == nil || ts.ExperimentalTotal.Published {
		t.Fatal("experimental team total must be suppressed (no frozen recipe)")
	}
	for _, ax := range tc.TeamAxisNames() {
		if ts.ExperimentalAxes[ax].Published {
			t.Fatalf("experimental axis %s must be suppressed", ax)
		}
	}
	// The V3 metric never appears in the official team axes or the official
	// total decomposition.
	officialVals := ts.OfficialAxes["fight"]
	for mid := range officialVals.Components {
		if mid == "core_partner_protection_uptime" {
			t.Fatal("V3 metric leaked into official team axis components")
		}
	}
	// Official total unaffected (all mandatory + optional present) and has no
	// V3 inputs.
	if ts.OfficialTotal == nil || !ts.OfficialTotal.Published {
		t.Fatalf("official total should publish; reasons=%v", ts.OfficialTotal.Reasons)
	}
}

func ptrF(v float64) *float64 { return &v }

// TestTeamAbsentStableShape proves absent-registry and absent-team API
// fallbacks use the same stable TeamScore shape with both layers.
func TestTeamAbsentStableShape(t *testing.T) {
	c := testContract(t)
	mreg := testMetricReg(t)
	cor := NewCorpusWithTeam(c, nil, mreg, nil)
	ts := cor.ScoreTeam("TA")
	if ts == nil {
		t.Fatal("absent-registry team score must not be nil")
	}
	if ts.OfficialTotal == nil || ts.ExperimentalTotal == nil {
		t.Fatal("absent-registry shape must include official and experimental totals")
	}
	if !ts.OfficialTotal.Suppressed || !ts.ExperimentalTotal.Suppressed {
		t.Fatal("absent-registry layers must be suppressed")
	}
	// Absent team with valid registry.
	tc := testTeamContract(t)
	cor2 := NewCorpusWithTeam(c, tc, mreg, nil)
	ts2 := cor2.ScoreTeam("NO_TEAM")
	if ts2 == nil {
		t.Fatal("absent-team score must not be nil")
	}
	if ts2.OfficialTotal == nil || ts2.ExperimentalTotal == nil {
		t.Fatal("absent-team shape must include both layers")
	}
	found := false
	for _, r := range ts2.OfficialTotal.Reasons {
		if r == "team_not_in_corpus" {
			found = true
		}
	}
	if !found {
		t.Fatalf("absent-team reasons=%v want team_not_in_corpus", ts2.OfficialTotal.Reasons)
	}
}
