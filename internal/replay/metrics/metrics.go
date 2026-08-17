// Package metrics implements the V1 metric registry and deterministic
// calculators over the normalized fact stream. V1 publishes direct facts and
// deterministic atomic transforms with no quality judgement. A metric that
// cannot be computed from the available facts is published as unavailable
// (null value plus a reason code) — never as a fabricated zero.
package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Capability and epistemic class constants (V1 subset).
const (
	CapabilityV1     = "V1"
	ClassDirect      = "direct"
	ClassUnavailable = "unavailable"
)

// Definition is one metric's machine-readable contract (V1 fields).
type Definition struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	Domain               string   `json:"domain"`
	CapabilityLevel      string   `json:"capability_level"`
	EpistemicClass       string   `json:"epistemic_class"`
	MetricVersion        string   `json:"metric_version"`
	ReportLevel          string   `json:"report_level"` // player|team|match
	Unit                 string   `json:"unit"`
	AggregationRule      string   `json:"aggregation_rule"`
	RequiredFactFamilies []string `json:"required_fact_families"`
	ScorePublicationGate string   `json:"score_publication_gate"`
}

// Value is a computed metric value with provenance.
type Value struct {
	MetricID          string   `json:"metric_id"`
	Name              string   `json:"name"`
	ReportLevel       string   `json:"report_level"`
	AccountID         string   `json:"account_id,omitempty"`
	TeamID            string   `json:"team_id,omitempty"`
	Value             *float64 `json:"value"`
	IntValue          *int64   `json:"int_value,omitempty"`
	Unit              string   `json:"unit"`
	EpistemicClass    string   `json:"epistemic_class"`
	CapabilityLevel   string   `json:"capability_level"`
	MetricVersion     string   `json:"metric_version"`
	Numerator         *float64 `json:"numerator"`
	Denominator       *float64 `json:"denominator"`
	SampleCount       int64    `json:"sample_count"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	EvidenceCount     int64    `json:"evidence_count"`
	Confidence        float64  `json:"confidence"`
}

// Output is the metrics artifact for one match.
type Output struct {
	SchemaVersion string       `json:"schema_version"`
	RuleVersion   string       `json:"rule_version"`
	MatchID       string       `json:"match_id"`
	Definitions   []Definition `json:"definitions"`
	Values        []Value      `json:"values"`
	Unavailable   []Value      `json:"unavailable"`
}

// CanonicalJSON returns the deterministic encoding.
func (o *Output) CanonicalJSON() ([]byte, error) { return json.Marshal(o) }

// Definitions returns the frozen V1 metric definitions this stage computes or
// explicitly marks unavailable. Deterministic order by id.
func Definitions() []Definition {
	return []Definition{
		{ID: "kills", Name: "击杀", Description: "Total hero kills scored", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyDeathRespawn}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "deaths", Name: "阵亡", Description: "Total deaths suffered", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyDeathRespawn}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "assists", Name: "助攻", Description: "Total assists on kills", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyDeathRespawn}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "buybacks", Name: "买活次数", Description: "Total buybacks used", Domain: "round", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyDeathRespawn}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "last_hits", Name: "正补", Description: "Last hits (final value)", Domain: "economy", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "max", RequiredFactFamilies: []string{facts.FamilyEconomy}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "net_worth", Name: "净资产", Description: "Net worth (final value)", Domain: "economy", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "gold", AggregationRule: "max", RequiredFactFamilies: []string{facts.FamilyEconomy}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "gold_earned", Name: "获得金钱", Description: "Gold earned", Domain: "economy", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "gold", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyEconomy}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "xp_earned", Name: "获得经验", Description: "XP earned", Domain: "economy", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "xp", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyEconomy}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "hero_damage", Name: "英雄伤害", Description: "Total hero damage dealt", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "damage", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyCombat}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "damage_received", Name: "承受伤害", Description: "Total hero damage received", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "damage", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyCombat}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "healing", Name: "治疗量", Description: "Total hero healing", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "heal", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyCombat}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "tower_damage", Name: "建筑伤害", Description: "Total tower/building damage", Domain: "objectives", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "damage", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyCombat}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "ability_casts", Name: "技能施放", Description: "Total ability casts", Domain: "combat", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyAbility}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "item_purchases", Name: "购买物品", Description: "Total item purchases", Domain: "economy", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyItem}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "wards_placed", Name: "守卫放置", Description: "Observer+sentry wards placed", Domain: "vision", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "player", Unit: "count", AggregationRule: "sum", RequiredFactFamilies: []string{facts.FamilyVision}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "team_score", Name: "队伍击杀", Description: "Team kill score", Domain: "objectives", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "team", Unit: "count", AggregationRule: "max", RequiredFactFamilies: []string{facts.FamilyMatchState}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "towers_killed", Name: "摧毁防御塔", Description: "Team towers killed", Domain: "objectives", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "team", Unit: "count", AggregationRule: "max", RequiredFactFamilies: []string{facts.FamilyObjective}, ScorePublicationGate: "identity+clock+facts"},
		{ID: "match_duration", Name: "比赛时长", Description: "Calibrated game duration", Domain: "match", CapabilityLevel: CapabilityV1, EpistemicClass: ClassDirect, MetricVersion: "v1", ReportLevel: "match", Unit: "seconds", AggregationRule: "value", RequiredFactFamilies: []string{facts.FamilyMatchState}, ScorePublicationGate: "identity+clock+facts"},
	}
}

// Calculator accumulates V1 metric values from a facts stream.
type Calculator struct {
	matchID     string
	accounts    []string
	accountName map[string]string
	teamByAcct  map[string]string
	totals      map[string]*agg
}

type agg struct {
	count int64
	sum   float64
	max   float64
}

// NewCalculator creates a metric calculator bound to the participant list.
func NewCalculator(matchID string, accounts []string, accountName map[string]string, teamByAcct map[string]string) *Calculator {
	return &Calculator{matchID: matchID, accounts: accounts, accountName: accountName, teamByAcct: teamByAcct, totals: map[string]*agg{}}
}

// Feed processes one fact line.
func (c *Calculator) Feed(f *facts.Fact) {
	var key, id string
	switch f.Family {
	case facts.FamilyDeathRespawn:
		var drb facts.DeathRespawnBuyback
		if err := json.Unmarshal(f.Payload, &drb); err != nil {
			return
		}
		if drb.AccountID == "" {
			return
		}
		switch drb.Kind {
		case "death":
			key, id = drb.AccountID, "deaths"
		case "buyback":
			key, id = drb.AccountID, "buybacks"
		default:
			return
		}
		c.inc(key, id, 1)
	case facts.FamilyCombat:
		var cf facts.CombatFact
		if err := json.Unmarshal(f.Payload, &cf); err != nil {
			return
		}
		if cf.Value != nil {
			if cf.ActorAccount != "" && cf.Kind == "damage" {
				if cf.TargetAccount != "" {
					c.inc(cf.ActorAccount, "hero_damage", float64(*cf.Value))
					c.inc(cf.TargetAccount, "damage_received", float64(*cf.Value))
				} else if cf.IsUltimate != nil && *cf.IsUltimate {
					c.inc(cf.ActorAccount, "hero_damage", float64(*cf.Value))
				}
			}
			if cf.ActorAccount != "" && cf.Kind == "heal" {
				c.inc(cf.ActorAccount, "healing", float64(*cf.Value))
			}
			if cf.ActorAccount != "" && cf.TargetAccount == "" && cf.Kind == "damage" {
				c.inc(cf.ActorAccount, "tower_damage", float64(*cf.Value))
			}
		}
	case facts.FamilyEconomy:
		var es facts.EconomySample
		if err := json.Unmarshal(f.Payload, &es); err != nil {
			return
		}
		if es.AccountID == "" {
			return
		}
		if es.LastHits != nil {
			c.max(es.AccountID, "last_hits", float64(*es.LastHits))
		}
		if es.Networth != nil {
			c.max(es.AccountID, "net_worth", float64(*es.Networth))
		}
		if es.Gold != nil {
			c.inc(es.AccountID, "gold_earned", float64(*es.Gold))
		}
		if es.Xp != nil {
			c.inc(es.AccountID, "xp_earned", float64(*es.Xp))
		}
	case facts.FamilyAbility:
		var af facts.AbilityFact
		if err := json.Unmarshal(f.Payload, &af); err != nil {
			return
		}
		if af.AccountID != "" {
			c.inc(af.AccountID, "ability_casts", 1)
		}
	case facts.FamilyItem:
		var it facts.ItemFact
		if err := json.Unmarshal(f.Payload, &it); err != nil {
			return
		}
		if it.AccountID != "" && it.Kind == "purchase" {
			c.inc(it.AccountID, "item_purchases", 1)
		}
	case facts.FamilyVision:
		var vf facts.VisionFact
		if err := json.Unmarshal(f.Payload, &vf); err != nil {
			return
		}
		if vf.AccountID != "" {
			c.inc(vf.AccountID, "wards_placed", 1)
		}
	}
}

func (c *Calculator) inc(key, id string, v float64) {
	a := c.aggFor(key, id)
	a.sum += v
	a.count++
}

func (c *Calculator) max(key, id string, v float64) {
	a := c.aggFor(key, id)
	if v > a.max {
		a.max = v
	}
	a.count++
}

func (c *Calculator) aggFor(key, id string) *agg {
	k := key + "\x00" + id
	a := c.totals[k]
	if a == nil {
		a = &agg{}
		c.totals[k] = a
	}
	return a
}

// Result builds the Output artifact.
func (c *Calculator) Result() *Output {
	out := &Output{
		SchemaVersion: version.MetricsSchema,
		RuleVersion:   version.MetricsRuleVersion,
		MatchID:       c.matchID,
		Definitions:   Definitions(),
		Values:        []Value{},
		Unavailable:   []Value{},
	}
	defs := map[string]Definition{}
	for _, d := range Definitions() {
		defs[d.ID] = d
	}
	// Player-level metrics for every participant.
	for _, acct := range c.accounts {
		for id, d := range defs {
			if d.ReportLevel != "player" {
				continue
			}
			v := Value{
				MetricID: id, Name: d.Name, ReportLevel: d.ReportLevel,
				AccountID: acct, TeamID: c.teamByAcct[acct],
				Unit: d.Unit, EpistemicClass: d.EpistemicClass,
				CapabilityLevel: d.CapabilityLevel, MetricVersion: d.MetricVersion,
			}
			a := c.totals[acct+"\x00"+id]
			if a == nil || a.count == 0 {
				v.UnavailableReason = "no_observations_for_metric"
				v.EpistemicClass = ClassUnavailable
				out.Unavailable = append(out.Unavailable, v)
				continue
			}
			switch d.AggregationRule {
			case "sum":
				f := a.sum
				v.Value = &f
				iv := int64(a.sum)
				v.IntValue = &iv
			case "max":
				f := a.max
				v.Value = &f
				iv := int64(a.max)
				v.IntValue = &iv
			}
			v.SampleCount = a.count
			v.EvidenceCount = a.count
			v.Confidence = 1.0
			out.Values = append(out.Values, v)
		}
	}
	sort.Slice(out.Values, func(i, j int) bool {
		if out.Values[i].AccountID != out.Values[j].AccountID {
			return out.Values[i].AccountID < out.Values[j].AccountID
		}
		return out.Values[i].MetricID < out.Values[j].MetricID
	})
	sort.Slice(out.Unavailable, func(i, j int) bool {
		if out.Unavailable[i].AccountID != out.Unavailable[j].AccountID {
			return out.Unavailable[i].AccountID < out.Unavailable[j].AccountID
		}
		return out.Unavailable[i].MetricID < out.Unavailable[j].MetricID
	})
	return out
}

// Validate ensures the deterministic output invariant: no value row is empty.
func (o *Output) Validate() error {
	for _, v := range o.Values {
		if v.Value == nil && v.IntValue == nil {
			return fmt.Errorf("metrics: value row %s/%s has no value", v.AccountID, v.MetricID)
		}
		if v.UnavailableReason != "" {
			return fmt.Errorf("metrics: value row %s/%s has unavailable reason", v.AccountID, v.MetricID)
		}
	}
	return nil
}

// Summary returns a compact text summary for progress output.
func (o *Output) Summary() string {
	byAcct := map[string]int{}
	for _, v := range o.Values {
		if v.AccountID != "" {
			byAcct[v.AccountID]++
		}
	}
	unavail := len(o.Unavailable)
	var b strings.Builder
	fmt.Fprintf(&b, "metrics: %d player values, %d unavailable rows, %d players", len(o.Values), unavail, len(byAcct))
	return b.String()
}
