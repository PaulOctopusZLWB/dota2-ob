package scoring

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

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
