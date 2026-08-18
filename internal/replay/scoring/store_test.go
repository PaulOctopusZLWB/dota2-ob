package scoring

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

func twoMatchTeamCorpus(t *testing.T) (*CorpusScores, *metrics.Registry) {
	t.Helper()
	reg := testMetricReg(t)
	p1 := mkPlayerWithLineage("8944525313", "a1", "9823272", "1", "hero_damage_total", 70000, 70000, 1, []EvidenceRef{{MatchID: "8944525313", Kind: "fact", ID: "fact:1"}})
	p2 := mkPlayerWithLineage("8946228107", "a2", "9823272", "2", "hero_damage_total", 102402, 102402, 1, []EvidenceRef{{MatchID: "8946228107", Kind: "fact", ID: "fact:2"}})
	cor := NewCorpusWithTeam(testContract(t), testTeamContract(t), reg, []*PlayerMatch{p1, p2})
	return &CorpusScores{
		SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion,
		ContractVersion: SchemaVersion, TeamScoringVersion: TeamSchemaVersion,
		CorpusMatches: 2,
		Matches: map[string]*MatchScores{
			p1.MatchID: {SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, MatchID: p1.MatchID, Players: []*PlayerMatch{p1}},
			p2.MatchID: {SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, MatchID: p2.MatchID, Players: []*PlayerMatch{p2}},
		},
		TeamMatches: cor.TeamMatches,
		Teams:       []*TeamScore{cor.ScoreTeam("9823272")},
		Players:     []*PlayerScore{},
	}, reg
}

func cloneCorpusScores(t *testing.T, in *CorpusScores) *CorpusScores {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out CorpusScores
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestTeamMatchAggregationsAreMatchQualifiedPersistedAndReconciled(t *testing.T) {
	cs, reg := twoMatchTeamCorpus(t)
	if err := ValidateCorpusScores(cs, reg); err != nil {
		t.Fatal(err)
	}
	tournament := cs.Teams[0].AggregatedMetrics["hero_damage_total"]
	if tournament.Value != 172402 || tournament.EligibleMatches != 2 {
		t.Fatalf("tournament=%+v want value=172402 eligible=2", tournament)
	}
	seen := map[string]bool{}
	for _, matchID := range []string{"8944525313", "8946228107"} {
		child := cs.TeamMatches["9823272"][matchID].Metrics["hero_damage_total"]
		wantID := teamMatchAggregationID("9823272", matchID, "hero_damage_total", reg.Find("hero_damage_total").MetricVersion)
		found := false
		for _, ref := range child.Lineage {
			if ref.Kind == "aggregation" && ref.ID == wantID && ref.MatchID == matchID {
				found = true
				seen[ref.ID] = true
			}
		}
		if !found {
			t.Fatalf("match %s missing canonical ref %s: %+v", matchID, wantID, child.Lineage)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("distinct team-match refs=%d want 2: %v", len(seen), seen)
	}
}

func TestValidateCorpusScoresRejectsTeamMatchChildSetCorruption(t *testing.T) {
	base, reg := twoMatchTeamCorpus(t)
	if err := ValidateCorpusScores(base, reg); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	childRefs := func(c *CorpusScores) (*AggregatedMetric, []int) {
		v := c.Teams[0].AggregatedMetrics["hero_damage_total"]
		idx := []int{}
		for i, ref := range v.Lineage {
			if ref.Kind == "aggregation" && strings.HasPrefix(ref.ID, "aggregation:team_match:") {
				idx = append(idx, i)
			}
		}
		return &v, idx
	}
	tests := []struct {
		name string
		edit func(*CorpusScores)
		want string
	}{
		{"missing", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage = append(v.Lineage[:ix[0]], v.Lineage[ix[0]+1:]...)
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "team_match_child_count"},
		{"duplicate", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage = append(v.Lineage, v.Lineage[ix[0]])
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "duplicate="},
		{"extra", func(c *CorpusScores) {
			v, _ := childRefs(c)
			v.Lineage = append(v.Lineage, EvidenceRef{Kind: "aggregation", ID: teamMatchAggregationID("9823272", "extra", v.MetricID, v.MetricVersion), MatchID: "extra", RuleVersion: version.ScoreRuleVersion, ContractVersion: TeamSchemaVersion})
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "unexpected="},
		{"matchless", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage[ix[0]].MatchID = ""
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "team_match_child.match_id"},
		{"wrong_team", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage[ix[0]].ID = strings.Replace(v.Lineage[ix[0]].ID, ":9823272:", ":wrong:", 1)
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "unexpected="},
		{"wrong_match", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage[ix[0]].MatchID = "8946228107"
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "team_match_child.match_id"},
		{"wrong_metric_version", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage[ix[0]].ID = strings.Replace(v.Lineage[ix[0]].ID, ":1.0.0:", ":0.9.0:", 1)
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "unexpected="},
		{"wrong_score_rule", func(c *CorpusScores) {
			v, ix := childRefs(c)
			v.Lineage[ix[0]].RuleVersion = "ti2026.scoring.v4"
			c.Teams[0].AggregatedMetrics[v.MetricID] = *v
		}, "team_match_child.provenance"},
		{"non_resolving", func(c *CorpusScores) { delete(c.TeamMatches["9823272"], "8944525313") }, "team_match_entity"},
		{"wrong_entity_team", func(c *CorpusScores) { c.TeamMatches["9823272"]["8944525313"].TeamID = "wrong" }, "field=team_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := cloneCorpusScores(t, base)
			tc.edit(corrupt)
			if err := ValidateCorpusScores(corrupt, reg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func currentStoredMetric(t *testing.T) (*store.Store, *metrics.Registry, *metrics.Output) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := testMetricReg(t)
	def := reg.Find("hero_damage_total")
	value := 100.0
	met := &metrics.Output{
		SchemaVersion: version.MetricsSchema, RuleVersion: version.MetricsRuleVersion, MatchID: "m1",
		Values:      []metrics.Value{{MetricID: def.ID, MetricVersion: def.MetricVersion, AccountID: "a1", OfficialPhase: "whole_match", Value: &value}},
		Unavailable: []metrics.Value{},
	}
	if err := st.WriteJSON("m1", store.ArtifactMetrics, met); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("m1", store.ArtifactReport, map[string]interface{}{"participants": []map[string]interface{}{{"account_id": "a1", "team_id": "T1", "nominal_role": "1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteRootJSON("catalog.json", &store.Catalog{Matches: []store.CatalogRow{{MatchID: "m1", Status: store.StatusVerified}}}); err != nil {
		t.Fatal(err)
	}
	return st, reg, met
}

func TestBuildCorpusRejectsStaleMetricArtifacts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*metrics.Output)
		want string
	}{
		{"wrong_schema", func(m *metrics.Output) { m.SchemaVersion = "replay.metrics.v4" }, "field=schema_version expected=" + version.MetricsSchema + " actual=replay.metrics.v4"},
		{"wrong_rule", func(m *metrics.Output) { m.RuleVersion = "ti2026.metrics.v10" }, "field=rule_version expected=" + version.MetricsRuleVersion + " actual=ti2026.metrics.v10"},
		{"missing_metric_version", func(m *metrics.Output) { m.Values[0].MetricVersion = "" }, "metric_version expected=1.0.0 actual=<missing>"},
		{"wrong_metric_version", func(m *metrics.Output) { m.Values[0].MetricVersion = "0.9.0" }, "metric_version expected=1.0.0 actual=0.9.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, reg, met := currentStoredMetric(t)
			tc.edit(met)
			if err := st.WriteJSON("m1", store.ArtifactMetrics, met); err != nil {
				t.Fatal(err)
			}
			_, err := BuildCorpusFromStore(st, testContract(t), testTeamContract(t), reg, &roles.Registry{}, nil)
			if err == nil || !strings.Contains(err.Error(), "match=m1") || !strings.Contains(err.Error(), "metrics.json") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want auditable stale-version error containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildCorpusAcceptsFullyCurrentMetricArtifact(t *testing.T) {
	st, reg, _ := currentStoredMetric(t)
	cor, err := BuildCorpusFromStore(st, testContract(t), testTeamContract(t), reg, &roles.Registry{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cor.Players) != 1 {
		t.Fatalf("players=%d want 1", len(cor.Players))
	}
	mv := cor.Players[0].Metrics["hero_damage_total"]
	if mv.MetricVersion != "1.0.0" {
		t.Fatalf("metric version lost at load: %+v", mv)
	}
}

func TestAggregationRejectsMixedMetricVersions(t *testing.T) {
	reg := testMetricReg(t)
	p1 := mkPlayer("m1", "a1", "T1", "1", map[string]float64{"hero_damage_total": 10}, nil, nil)
	p2 := mkPlayer("m2", "a1", "T1", "1", map[string]float64{"hero_damage_total": 20}, nil, nil)
	mv1 := p1.Metrics["hero_damage_total"]
	mv1.MetricVersion = "1.0.0"
	p1.Metrics["hero_damage_total"] = mv1
	mv2 := p2.Metrics["hero_damage_total"]
	mv2.MetricVersion = "0.9.0"
	p2.Metrics["hero_damage_total"] = mv2
	cor := NewCorpus(testContract(t), reg, []*PlayerMatch{p1, p2})
	if pt := cor.Tournament("a1", "1"); pt != nil {
		if _, ok := pt.Metrics["hero_damage_total"]; ok {
			t.Fatal("mixed metric versions were aggregated under one metric id")
		}
	}
}

func TestMixedV4V5RootFailsClosedWithoutResurrectingDeathZeroes(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	matchID := "8946228107"
	if err := st.WriteRootJSON("catalog.json", &store.Catalog{Matches: []store.CatalogRow{{MatchID: matchID, Status: store.StatusVerified}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON(matchID, store.ArtifactReport, map[string]interface{}{"participants": []map[string]interface{}{
		{"account_id": "312436974", "team_id": "T1", "nominal_role": "2"},
		{"account_id": "56351509", "team_id": "T1", "nominal_role": "3"},
	}}); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	stale := &metrics.Output{
		SchemaVersion: "replay.metrics.v4", RuleVersion: "ti2026.metrics.v10", MatchID: matchID,
		Values: []metrics.Value{
			{MetricID: "death_count", MetricVersion: "1.0.0", AccountID: "312436974", OfficialPhase: "whole_match", Value: &zero, OpportunityCount: 1},
			{MetricID: "death_count", MetricVersion: "1.0.0", AccountID: "56351509", OfficialPhase: "whole_match", Value: &zero, OpportunityCount: 1},
		},
	}
	if err := st.WriteJSON(matchID, store.ArtifactMetrics, stale); err != nil {
		t.Fatal(err)
	}
	current := &CorpusScores{SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, Matches: map[string]*MatchScores{}, Players: []*PlayerScore{}, Teams: []*TeamScore{}}
	if err := st.WriteRootJSON("scores-corpus.json", current); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Root, "scores-corpus.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ComputeAndPersist(st, testContract(t), testTeamContract(t), testMetricReg(t), &roles.Registry{}, nil)
	if err == nil || !strings.Contains(err.Error(), "match=8946228107") || !strings.Contains(err.Error(), "expected="+version.MetricsSchema+" actual=replay.metrics.v4") {
		t.Fatalf("mixed-root err=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte(`"opportunity_count":1`)) || bytes.Contains(after, []byte(`"death_count"`)) {
		t.Fatal("stale death zero rows entered or replaced the current score corpus")
	}
}

func TestUnavailableComponentRetainsRegistryVersionAndPreciseReason(t *testing.T) {
	reg := testMetricReg(t)
	def := reg.Find("buyback_round_participation")
	p := mkPlayer("m1", "a1", "T1", "1", map[string]float64{"hero_damage_total": 10}, nil, nil)
	p.UnavailableMetrics = map[string]UnavailableMetric{
		def.ID: {MetricID: def.ID, MetricVersion: def.MetricVersion, UnavailableReason: "eligible_death_buyback_state_denominator_not_in_accepted_adapter"},
	}
	cor := NewCorpusWithTeam(testContract(t), testTeamContract(t), reg, []*PlayerMatch{p})
	ps := cor.ScorePlayer("a1", "1")
	if ps == nil {
		t.Fatal("player score missing")
	}
	found := false
	for _, axis := range ps.OfficialAxes {
		if component, ok := axis.Components[def.ID]; ok {
			found = true
			if component.MetricVersion != def.MetricVersion || component.UnavailableReason != p.UnavailableMetrics[def.ID].UnavailableReason || component.Published {
				t.Fatalf("unavailable component lost identity/reason: %+v", component)
			}
		}
	}
	if !found {
		t.Fatalf("%s is not present in role-1 official decomposition", def.ID)
	}
}

func TestCanonicalAggregationIDsUseScoreRuleAndRetainContractProvenance(t *testing.T) {
	reg := testMetricReg(t)
	p := mkPlayer("m1", "a1", "T1", "1", map[string]float64{"hero_damage_total": 10}, nil, nil)
	cor := NewCorpusWithTeam(testContract(t), testTeamContract(t), reg, []*PlayerMatch{p})
	assertRef := func(t *testing.T, value AggregatedMetric, wantID, wantContract string) {
		t.Helper()
		for _, ref := range value.Lineage {
			if ref.Kind == "aggregation" && ref.ID == wantID {
				if ref.RuleVersion != version.ScoreRuleVersion || ref.ContractVersion != wantContract {
					t.Fatalf("aggregation provenance=%+v", ref)
				}
				return
			}
		}
		t.Fatalf("aggregation ref %q missing from %+v", wantID, value.Lineage)
	}
	def := reg.Find("hero_damage_total")
	assertRef(t, cor.Tournament("a1", "1").Metrics[def.ID], "aggregation:player_tournament:a1:1:hero_damage_total:"+def.MetricVersion+":"+version.ScoreRuleVersion, testContract(t).SchemaVersion)
	assertRef(t, cor.Teams["T1"].Metrics[def.ID], "aggregation:team_tournament:T1::hero_damage_total:"+def.MetricVersion+":"+version.ScoreRuleVersion, testTeamContract(t).SchemaVersion)
}

func TestValidateCorpusScoresRejectsNestedMetricVersionAndAggregationCorruption(t *testing.T) {
	reg := testMetricReg(t)
	p := mkPlayer("m1", "a1", "T1", "1", map[string]float64{"hero_damage_total": 10}, nil, nil)
	cor := NewCorpusWithTeam(testContract(t), testTeamContract(t), reg, []*PlayerMatch{p})
	base := func() *CorpusScores {
		return &CorpusScores{
			SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion,
			ContractVersion: SchemaVersion, TeamScoringVersion: TeamSchemaVersion,
			Players: []*PlayerScore{cor.ScorePlayer("a1", "1")}, Teams: []*TeamScore{cor.ScoreTeam("T1")},
			Matches: map[string]*MatchScores{
				"m1": {SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, MatchID: "m1", Players: cor.Matches["m1"]},
			},
			TeamMatches: cor.TeamMatches,
		}
	}
	cs := base()
	if err := ValidateCorpusScores(cs, reg); err != nil {
		t.Fatalf("valid corpus: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*CorpusScores)
		want string
	}{
		{"missing_component_version", func(c *CorpusScores) {
			for axisKey, axis := range c.Players[0].OfficialAxes {
				for mid, component := range axis.Components {
					component.MetricVersion = ""
					axis.Components[mid] = component
					c.Players[0].OfficialAxes[axisKey] = axis
					return
				}
			}
		}, "field=metric_version"},
		{"wrong_component_version", func(c *CorpusScores) {
			for axisKey, axis := range c.Players[0].OfficialAxes {
				for mid, component := range axis.Components {
					component.MetricVersion = "0.9.0"
					axis.Components[mid] = component
					c.Players[0].OfficialAxes[axisKey] = axis
					return
				}
			}
		}, "actual=0.9.0"},
		{"wrong_aggregate_version", func(c *CorpusScores) {
			value := c.Players[0].AggregatedMetrics["hero_damage_total"]
			value.MetricVersion = "0.9.0"
			c.Players[0].AggregatedMetrics["hero_damage_total"] = value
		}, "expected=1.0.0 actual=0.9.0"},
		{"aggregate_map_key_identity", func(c *CorpusScores) {
			value := c.Players[0].AggregatedMetrics["hero_damage_total"]
			delete(c.Players[0].AggregatedMetrics, "hero_damage_total")
			c.Players[0].AggregatedMetrics["wrong_key"] = value
		}, "field=metric_id expected=wrong_key actual=hero_damage_total"},
		{"wrong_aggregation_id", func(c *CorpusScores) {
			value := c.Players[0].AggregatedMetrics["hero_damage_total"]
			value.Lineage = append([]EvidenceRef(nil), value.Lineage...)
			for i := range value.Lineage {
				if value.Lineage[i].Kind == "aggregation" {
					value.Lineage[i].ID = strings.TrimSuffix(value.Lineage[i].ID, version.ScoreRuleVersion) + "ti2026.radar-score.v1"
				}
			}
			c.Players[0].AggregatedMetrics["hero_damage_total"] = value
		}, "aggregation_ref"},
		{"wrong_aggregation_contract", func(c *CorpusScores) {
			value := c.Players[0].AggregatedMetrics["hero_damage_total"]
			value.Lineage = append([]EvidenceRef(nil), value.Lineage...)
			for i := range value.Lineage {
				if value.Lineage[i].Kind == "aggregation" {
					value.Lineage[i].ContractVersion = TeamSchemaVersion
				}
			}
			c.Players[0].AggregatedMetrics["hero_damage_total"] = value
		}, "aggregation_ref.contract_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := base()
			tc.edit(corrupt)
			if err := ValidateCorpusScores(corrupt, reg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}
