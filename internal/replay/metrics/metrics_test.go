package metrics

import (
	"encoding/json"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
)

func TestCalculatorAggregation(t *testing.T) {
	calc := NewCalculator("1000000001", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T1"})
	feed := func(f *facts.Fact) { calc.Feed(f) }
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a2"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "a1"})})
	v := int64(50)
	feed(&facts.Fact{Family: facts.FamilyCombat, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})
	feed(&facts.Fact{Family: facts.FamilyCombat, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})

	out := calc.Result()
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
	// a1: 2 deaths, 1 buyback, 100 hero damage.
	got := map[string]float64{}
	for _, v := range out.Values {
		if v.AccountID == "a1" {
			got[v.MetricID] = *v.Value
		}
	}
	if got["deaths"] != 2 {
		t.Fatalf("a1 deaths=%v want 2", got["deaths"])
	}
	if got["buybacks"] != 1 {
		t.Fatalf("a1 buybacks=%v want 1", got["buybacks"])
	}
	if got["hero_damage"] != 100 {
		t.Fatalf("a1 hero_damage=%v want 100", got["hero_damage"])
	}
}

func TestUnavailableNeverZero(t *testing.T) {
	calc := NewCalculator("1000000001", []string{"a1"}, map[string]string{"a1": "p1"}, map[string]string{"a1": "T1"})
	// No observations at all.
	out := calc.Result()
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
	if len(out.Values) != 0 {
		t.Fatalf("no observations should produce no published values")
	}
}

func TestDefinitionsComplete(t *testing.T) {
	defs := Definitions()
	if len(defs) == 0 {
		t.Fatal("no definitions")
	}
	for _, d := range defs {
		if d.ID == "" || d.Name == "" || d.Unit == "" || d.AggregationRule == "" {
			t.Fatalf("incomplete definition: %+v", d)
		}
		if d.CapabilityLevel != CapabilityV1 || d.EpistemicClass != ClassDirect {
			t.Fatalf("definition %s not V1/direct: %s/%s", d.ID, d.CapabilityLevel, d.EpistemicClass)
		}
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
