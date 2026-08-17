// Package metrics implements the full V1/V2/V3 metric registry and
// deterministic calculators over the normalized fact stream. Every metric
// definition comes from the frozen machine-readable registry
// (docs/specs/ti2026-role-phase-metrics-v1.json). A metric either publishes a
// value with numerator/denominator/opportunity/sample/confidence/evidence or
// resolves to an explicit unavailable state with a precise reason. Unavailable
// is null plus a reason code — never a fabricated zero.
package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Definition is one metric's machine-readable contract (view model). The
// calculator and API derive definitions from the frozen registry so the
// versioned contract is the single source of truth.
type Definition struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	Description               string   `json:"description"`
	Domain                    string   `json:"domain"`
	CapabilityLevel           string   `json:"capability_level"`
	EpistemicClass            string   `json:"epistemic_class"`
	MetricVersion             string   `json:"metric_version"`
	ReportLevel               string   `json:"report_level"` // player|team|match
	Unit                      string   `json:"unit"`
	Direction                 string   `json:"direction"`
	ScoreDirection            string   `json:"score_direction"`
	AggregationRule           string   `json:"aggregation_rule"`
	Numerator                 string   `json:"numerator"`
	Denominator               string   `json:"denominator"`
	Opportunity               string   `json:"opportunity"`
	RequiredFactFamilies      []string `json:"required_fact_families"`
	RadarAxis                 string   `json:"radar_axis"`
	OfficialScoreEligible     bool     `json:"official_score_eligible"`
	ExperimentalScoreEligible bool     `json:"experimental_score_eligible"`
	Roles                     []string `json:"roles"`
	Phases                    []string `json:"phases"`
	ScorePublicationGate      string   `json:"score_publication_gate"`
}

// Value is a computed metric value with provenance.
type Value struct {
	MetricID                  string   `json:"metric_id"`
	Name                      string   `json:"name"`
	ReportLevel               string   `json:"report_level"`
	AccountID                 string   `json:"account_id,omitempty"`
	TeamID                    string   `json:"team_id,omitempty"`
	NominalRole               string   `json:"nominal_role,omitempty"`
	OfficialPhase             string   `json:"official_phase,omitempty"`
	Value                     *float64 `json:"value"`
	IntValue                  *int64   `json:"int_value,omitempty"`
	Unit                      string   `json:"unit"`
	EpistemicClass            string   `json:"epistemic_class"`
	CapabilityLevel           string   `json:"capability_level"`
	MetricVersion             string   `json:"metric_version"`
	Numerator                 *float64 `json:"numerator"`
	Denominator               *float64 `json:"denominator"`
	OpportunityCount          int64    `json:"opportunity_count"`
	ExcludedCount             int64    `json:"excluded_count"`
	SampleCount               int64    `json:"sample_count"`
	EvidenceCount             int64    `json:"evidence_count"`
	EvidenceIDs               []int64  `json:"evidence_ids,omitempty"`
	UnavailableReason         string   `json:"unavailable_reason,omitempty"`
	Confidence                float64  `json:"confidence"`
	Direction                 string   `json:"direction"`
	OfficialScoreEligible     bool     `json:"official_score_eligible"`
	ExperimentalScoreEligible bool     `json:"experimental_score_eligible"`
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

// RegistryOutput returns the full registry definitions for the API.
func RegistryOutput(reg *Registry) []Definition {
	if reg == nil {
		return []Definition{}
	}
	out := make([]Definition, 0, len(reg.Metrics))
	for i := range reg.Metrics {
		m := &reg.Metrics[i]
		out = append(out, Definition{
			ID: m.ID, Name: m.Name, Description: m.Description, Domain: m.Domain,
			CapabilityLevel: m.CapabilityLevel, EpistemicClass: m.EpistemicClass,
			MetricVersion: m.MetricVersion, ReportLevel: m.ReportLevel, Unit: m.Unit,
			Direction: m.Direction, ScoreDirection: m.ScoreDirection, AggregationRule: m.AggregationRule,
			Numerator: m.Numerator, Denominator: m.Denominator, Opportunity: m.Opportunity,
			RequiredFactFamilies: m.RequiredFactFamilies, RadarAxis: m.RadarAxis,
			OfficialScoreEligible: m.OfficialScoreEligible, ExperimentalScoreEligible: m.ExperimentalScoreEligible,
			Roles: m.Roles, Phases: m.Phases,
			ScorePublicationGate: m.ScorePublicationGate.Rule,
		})
	}
	return out
}

// Calculator accumulates metric values from a facts stream. It is
// registry-aware: every registry metric resolves to either a published value
// or an explicit unavailable record with a precise reason.
type Calculator struct {
	matchID     string
	accounts    []string
	accountName map[string]string
	teamByAcct  map[string]string
	roleByAcct  map[string]string
	teamOfSide  map[string]string
	registry    *Registry

	// per-account accumulators
	kills           map[string]int64
	assists         map[string]int64
	deaths          map[string]int64
	buybacks        map[string]int64
	buybackSeconds  map[string][]float64
	lastHits        map[string]int64
	denies          map[string]int64
	xpDelta         map[string]float64
	netWorthDelta   map[string]float64
	goldEarned      map[string]float64
	firstItemSec    map[string]float64
	heroDamage      map[string]float64
	heroHealing     map[string]float64
	objectiveDamage map[string]float64
	controlSeconds  map[string]float64
	heroStateSecs   map[string]int64              // eligible hero-state seconds
	damageEvents    []damageEvent                 // for fight_damage_share
	teams           map[string]map[string]float64 // team_id -> account -> networth (final)
}

type damageEvent struct {
	GameSecond float64
	Actor      string
	Team       string
	Value      float64
	HeroTarget bool
	Building   bool
}

// NewCalculator creates a metric calculator bound to the participant list and
// an optional role map. When registry is nil, all metrics resolve to
// unavailable (the runner always supplies the frozen registry).
func NewCalculator(matchID string, accounts []string, accountName map[string]string, teamByAcct map[string]string) *Calculator {
	return &Calculator{
		matchID: matchID, accounts: accounts, accountName: accountName, teamByAcct: teamByAcct,
		roleByAcct: map[string]string{}, teamOfSide: map[string]string{},
		kills: map[string]int64{}, assists: map[string]int64{}, deaths: map[string]int64{},
		buybacks: map[string]int64{}, buybackSeconds: map[string][]float64{},
		lastHits: map[string]int64{}, denies: map[string]int64{},
		xpDelta: map[string]float64{}, netWorthDelta: map[string]float64{},
		goldEarned: map[string]float64{}, firstItemSec: map[string]float64{},
		heroDamage: map[string]float64{}, heroHealing: map[string]float64{},
		objectiveDamage: map[string]float64{}, controlSeconds: map[string]float64{},
		heroStateSecs: map[string]int64{},
		teams:         map[string]map[string]float64{},
	}
}

// SetRoles binds nominal roles (publication-gate input) to accounts.
func (c *Calculator) SetRoles(roleByAcct map[string]string) {
	for k, v := range roleByAcct {
		c.roleByAcct[k] = v
	}
}

// SetTeamOfSide maps a side to a team id (radiant/dire).
func (c *Calculator) SetTeamOfSide(m map[string]string) {
	for k, v := range m {
		c.teamOfSide[k] = v
	}
}

// SetRegistry binds the frozen metric registry.
func (c *Calculator) SetRegistry(reg *Registry) { c.registry = reg }

// Feed processes one fact line.
func (c *Calculator) Feed(f *facts.Fact) {
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
			c.deaths[drb.AccountID]++
			if drb.KillerAccount != "" {
				c.kills[drb.KillerAccount]++
			}
			for _, a := range drb.AssistAccounts {
				c.assists[a]++
			}
		case "buyback":
			c.buybacks[drb.AccountID]++
			c.buybackSeconds[drb.AccountID] = append(c.buybackSeconds[drb.AccountID], f.GameSecond)
		}
	case facts.FamilyCombat:
		var cf facts.CombatFact
		if err := json.Unmarshal(f.Payload, &cf); err != nil {
			return
		}
		if cf.Value == nil || cf.ActorAccount == "" {
			return
		}
		v := float64(*cf.Value)
		isHero := cf.TargetAccount != ""
		isBuilding := !isHero && isBuildingTargetName(cf.TargetName)
		team := c.teamByAcct[cf.ActorAccount]
		switch cf.Kind {
		case "damage":
			if isHero {
				c.heroDamage[cf.ActorAccount] += v
			} else {
				c.objectiveDamage[cf.ActorAccount] += v
			}
			c.damageEvents = append(c.damageEvents, damageEvent{
				GameSecond: f.GameSecond, Actor: cf.ActorAccount, Team: team,
				Value: v, HeroTarget: isHero, Building: isBuilding,
			})
		case "heal":
			c.heroHealing[cf.ActorAccount] += v
		}
	case facts.FamilyEconomy:
		var es facts.EconomySample
		if err := json.Unmarshal(f.Payload, &es); err != nil {
			return
		}
		if es.AccountID == "" {
			return
		}
		if es.LastHits != nil && int64(*es.LastHits) > c.lastHits[es.AccountID] {
			c.lastHits[es.AccountID] = int64(*es.LastHits)
		}
		if es.Xp != nil && float64(*es.Xp) > c.xpDelta[es.AccountID] {
			c.xpDelta[es.AccountID] = float64(*es.Xp)
		}
		if es.Networth != nil {
			nw := float64(*es.Networth)
			if nw > c.netWorthDelta[es.AccountID] {
				c.netWorthDelta[es.AccountID] = nw
			}
			if c.teams[c.teamByAcct[es.AccountID]] == nil {
				c.teams[c.teamByAcct[es.AccountID]] = map[string]float64{}
			}
			c.teams[c.teamByAcct[es.AccountID]][es.AccountID] = nw
		}
		if es.Gold != nil {
			c.goldEarned[es.AccountID] += float64(*es.Gold)
		}
	case facts.FamilyItem:
		var it facts.ItemFact
		if err := json.Unmarshal(f.Payload, &it); err != nil {
			return
		}
		if it.AccountID != "" && it.Kind == "purchase" {
			if _, ok := c.firstItemSec[it.AccountID]; !ok {
				c.firstItemSec[it.AccountID] = f.GameSecond
			}
		}
	case facts.FamilyHeroState:
		var hs facts.HeroStateSample
		if err := json.Unmarshal(f.Payload, &hs); err != nil {
			return
		}
		if hs.AccountID != "" && hs.PosX != nil && hs.PosY != nil {
			c.heroStateSecs[hs.AccountID]++
		}
	}
}

// Result builds the Output artifact. It resolves every registry metric for
// every participant: published when the calculator has the required
// observations, otherwise an explicit unavailable record with a reason.
// eps/phases supply fight participation, fight damage share, and phase
// duration inputs.
func (c *Calculator) Result(eps *episodes.Output, ph *phase.Output) *Output {
	out := &Output{
		SchemaVersion: version.MetricsSchema,
		RuleVersion:   version.MetricsRuleVersion,
		MatchID:       c.matchID,
		Definitions:   RegistryOutput(c.registry),
		Values:        []Value{},
		Unavailable:   []Value{},
	}
	reg := c.registry
	if reg == nil {
		// No registry: fail closed — every metric resolves to unavailable.
		out.Unavailable = append(out.Unavailable, Value{
			MetricID: "registry_unavailable", Name: "registry unavailable",
			ReportLevel: "match", EpistemicClass: ClassUnavailable,
			UnavailableReason: "metric_registry_not_loaded",
		})
		return out
	}

	teamNetWorth := map[string]float64{}
	for tid, m := range c.teams {
		for _, nw := range m {
			teamNetWorth[tid] += nw
		}
	}

	fightParticipation := c.computeFightParticipation(eps)
	fightDamageShare := c.computeFightDamageShare(eps)
	phaseDuration := c.computePhaseDuration(ph)

	for _, acct := range c.accounts {
		role := c.roleByAcct[acct]
		team := c.teamByAcct[acct]
		for i := range reg.Metrics {
			m := &reg.Metrics[i]
			if m.ReportLevel != "player" {
				continue
			}
			if len(m.Roles) > 0 && !m.AppliesToRole(role) {
				continue
			}
			v := c.computeMetric(m, acct, team, role, teamNetWorth, fightParticipation, fightDamageShare, phaseDuration)
			v.MetricID = m.ID
			v.Name = m.Name
			v.ReportLevel = "player"
			v.AccountID = acct
			v.TeamID = team
			v.NominalRole = role
			v.Unit = m.Unit
			v.EpistemicClass = m.EpistemicClass
			v.CapabilityLevel = m.CapabilityLevel
			v.MetricVersion = m.MetricVersion
			v.Direction = m.Direction
			v.OfficialScoreEligible = m.OfficialScoreEligible
			v.ExperimentalScoreEligible = m.ExperimentalScoreEligible
			if v.UnavailableReason != "" {
				v.EpistemicClass = ClassUnavailable
				out.Unavailable = append(out.Unavailable, v)
			} else {
				out.Values = append(out.Values, v)
			}
		}
	}

	// Match-level metrics.
	if phaseDuration != nil {
		out.Values = append(out.Values, phaseDurationValue(reg, c.matchID, phaseDuration))
	} else {
		out.Unavailable = append(out.Unavailable, phaseDurationUnavailable(reg, c.matchID))
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

// computeMetric resolves one registry metric for one player. Published metrics
// are deterministic atomic facts or derived transforms supported by the
// accepted parser; everything else is explicit unavailable.
func (c *Calculator) computeMetric(m *Metric, acct, team, role string, teamNetWorth map[string]float64, fightParticipation map[string]int64, fightDamageShare map[string]*shareVal, phaseDuration *float64) Value {
	v := Value{}

	switch m.ID {
	case "kill_count":
		v = c.countValue(c.kills[acct], m)
	case "assist_count":
		v = c.countValue(c.assists[acct], m)
	case "death_count":
		v = c.countValue(c.deaths[acct], m)
	case "buyback_use_count":
		v = c.countValue(c.buybacks[acct], m)
	case "last_hit_count":
		if !c.sawEconomy(acct) {
			v.UnavailableReason = "last_hit_state_missing"
		} else {
			v = c.countValue(c.lastHits[acct], m)
		}
	case "deny_count":
		v.UnavailableReason = "deny_counter_not_in_accepted_adapter"
	case "xp_delta":
		if !c.sawEconomy(acct) {
			v.UnavailableReason = "xp_state_missing"
		} else {
			v = c.floatValue(c.xpDelta[acct], m)
		}
	case "net_worth_delta":
		if !c.sawEconomy(acct) {
			v.UnavailableReason = "networth_state_missing"
		} else {
			v = c.floatValue(c.netWorthDelta[acct], m)
		}
	case "gold_earned":
		if !c.sawEconomy(acct) {
			v.UnavailableReason = "gold_state_missing"
		} else {
			v = c.floatValue(c.goldEarned[acct], m)
		}
	case "team_resource_share":
		if !c.sawEconomy(acct) {
			v.UnavailableReason = "networth_state_missing"
		} else if teamNetWorth[team] <= 0 {
			v.UnavailableReason = "team_networth_missing"
		} else {
			share := c.netWorthDelta[acct] / teamNetWorth[team]
			v = c.floatValue(share, m)
		}
	case "item_completion_timing_seconds":
		// The accepted adapter emits raw purchase events without a complete
		// inventory lifecycle / combine record, so "first possession second"
		// is interval-ambiguous (starting items yield pregame negatives).
		v.UnavailableReason = "inventory_lifecycle_not_in_accepted_adapter"
	case "objective_damage_total":
		v = c.floatValue(c.objectiveDamage[acct], m)
	case "fight_participation_count":
		v = c.countValue(fightParticipation[acct], m)
	case "opportunity_duration_seconds":
		v = c.countValue(c.heroStateSecs[acct], m)
	case "hero_damage_total":
		v = c.floatValue(c.heroDamage[acct], m)
	case "hero_healing_total":
		v = c.floatValue(c.heroHealing[acct], m)
	case "control_duration_seconds":
		v.UnavailableReason = "control_modifier_registry_not_in_accepted_adapter"
	case "phase_duration_seconds":
		// player-level aggregate view is unavailable; the match-level metric
		// is emitted separately. Keep the player row unavailable to avoid
		// double counting.
		v.UnavailableReason = "reported_at_match_level"
	case "fight_damage_share":
		sv := fightDamageShare[acct]
		if sv == nil || sv.denom <= 0 {
			v.UnavailableReason = "fight_damage_denominator_missing"
		} else {
			share := sv.num / sv.denom
			v = c.floatValue(share, m)
			v.Numerator = &sv.num
			v.Denominator = &sv.denom
			v.OpportunityCount = sv.fights
			v.SampleCount = sv.fights
		}
	case "buyback_round_participation":
		// Post-buyback participation facts are not resolvable from the
		// accepted adapter (no round context/participation ledger).
		v.UnavailableReason = "buyback_round_context_not_in_accepted_adapter"
	default:
		// V2/V3 modelled and opportunity metrics requiring map geometry,
		// creep lifecycle, rune state, ward entities, or team shape.
		v.UnavailableReason = c.contractReason(m)
	}
	if v.UnavailableReason != "" {
		v.EpistemicClass = ClassUnavailable
		v.Confidence = 0
		return v
	}
	if v.Confidence == 0 {
		v.Confidence = 1.0
	}
	return v
}

// contractReason returns the precise reason for a metric whose required
// inputs are not produced by the accepted parser, derived from its registry
// contract rather than a fabricated denominator.
func (c *Calculator) contractReason(m *Metric) string {
	switch m.CapabilityLevel {
	case CapabilityV3:
		return "v3_modelled_inputs_not_in_accepted_adapter:" + strings.Join(m.RequiredFactFamilies, "|")
	case CapabilityV2:
		return "v2_opportunity_inputs_not_in_accepted_adapter:" + strings.Join(m.RequiredFactFamilies, "|")
	default:
		return "required_fact_families_not_available:" + strings.Join(m.RequiredFactFamilies, "|")
	}
}

func (c *Calculator) countValue(n int64, m *Metric) Value {
	v := Value{}
	f := float64(n)
	v.Value = &f
	iv := n
	v.IntValue = &iv
	v.Numerator = &f
	v.SampleCount = n
	v.EvidenceCount = n
	v.Confidence = 1.0
	return v
}

func (c *Calculator) floatValue(f float64, m *Metric) Value {
	v := Value{}
	v.Value = &f
	iv := int64(f)
	v.IntValue = &iv
	v.Numerator = &f
	v.SampleCount = 1
	v.EvidenceCount = 1
	v.Confidence = 1.0
	return v
}

func (c *Calculator) sawEconomy(acct string) bool {
	return c.lastHits[acct] > 0 || c.netWorthDelta[acct] > 0 || c.xpDelta[acct] > 0
}

type shareVal struct {
	num    float64
	denom  float64
	fights int64
}

// computeFightParticipation counts fight episodes containing each player.
func (c *Calculator) computeFightParticipation(eps *episodes.Output) map[string]int64 {
	out := map[string]int64{}
	if eps == nil {
		return out
	}
	for i := range eps.Episodes {
		e := &eps.Episodes[i]
		if e.Kind != episodes.KindFight {
			continue
		}
		for _, p := range e.Participants {
			out[p]++
		}
	}
	return out
}

// computeFightDamageShare computes per-player hero damage inside fight
// intervals divided by team hero damage in those intervals.
func (c *Calculator) computeFightDamageShare(eps *episodes.Output) map[string]*shareVal {
	out := map[string]*shareVal{}
	if eps == nil {
		return out
	}
	for i := range eps.Episodes {
		e := &eps.Episodes[i]
		if e.Kind != episodes.KindFight {
			continue
		}
		byActor := map[string]float64{}
		teamTotal := map[string]float64{}
		for _, de := range c.damageEvents {
			if de.GameSecond < e.StartGameSecond || de.GameSecond >= e.EndGameSecond {
				continue
			}
			if !de.HeroTarget {
				continue
			}
			byActor[de.Actor] += de.Value
			teamTotal[de.Team] += de.Value
		}
		for actor, dmg := range byActor {
			sv := out[actor]
			if sv == nil {
				sv = &shareVal{}
				out[actor] = sv
			}
			sv.num += dmg
			sv.denom += teamTotal[teamOf(c.teamByAcct, actor)]
			sv.fights++
		}
	}
	return out
}

func teamOf(teamByAcct map[string]string, acct string) string {
	return teamByAcct[acct]
}

// isBuildingTargetName reports whether a damage target name is a real
// structure (tower/rax/ancient/fort). Temporary structures and summoned units
// never count as objective entities.
func isBuildingTargetName(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	for _, marker := range []string{"tower", "rax", "ancient", "fort", "shrine"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// computePhaseDuration sums the official phase interval durations.
func (c *Calculator) computePhaseDuration(ph *phase.Output) *float64 {
	if ph == nil {
		return nil
	}
	var total float64
	for _, iv := range ph.Intervals {
		if iv.GlobalPhase.Official() {
			total += float64(iv.EndGameSecond - iv.StartGameSecond)
		}
	}
	return &total
}

func phaseDurationValue(reg *Registry, matchID string, dur *float64) Value {
	v := Value{
		MetricID: "phase_duration_seconds", Name: "Phase duration (match)",
		ReportLevel: "match", Unit: "seconds", EpistemicClass: ClassDerived,
		CapabilityLevel: CapabilityV1, MetricVersion: "1.0.0",
		Value: dur, SampleCount: 1, Confidence: 1.0, Direction: "context_only",
	}
	return v
}

func phaseDurationUnavailable(reg *Registry, matchID string) Value {
	return Value{
		MetricID: "phase_duration_seconds", Name: "Phase duration (match)",
		ReportLevel: "match", Unit: "seconds", EpistemicClass: ClassUnavailable,
		CapabilityLevel: CapabilityV1, MetricVersion: "1.0.0",
		UnavailableReason: "official_phase_intervals_missing",
	}
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
