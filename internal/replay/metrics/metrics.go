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
	MetricID         string   `json:"metric_id"`
	Name             string   `json:"name"`
	ReportLevel      string   `json:"report_level"`
	AccountID        string   `json:"account_id,omitempty"`
	TeamID           string   `json:"team_id,omitempty"`
	NominalRole      string   `json:"nominal_role,omitempty"`
	OfficialPhase    string   `json:"official_phase,omitempty"`
	Value            *float64 `json:"value"`
	IntValue         *int64   `json:"int_value,omitempty"`
	Unit             string   `json:"unit"`
	EpistemicClass   string   `json:"epistemic_class"`
	CapabilityLevel  string   `json:"capability_level"`
	MetricVersion    string   `json:"metric_version"`
	Numerator        *float64 `json:"numerator"`
	Denominator      *float64 `json:"denominator"`
	OpportunityCount int64    `json:"opportunity_count"`
	ExcludedCount    int64    `json:"excluded_count"`
	// ExcludedDamage is the magnitude of excluded (non-objective) damage
	// preserved separately; it is distinct from excluded_count (record count).
	ExcludedDamage            *float64 `json:"excluded_damage,omitempty"`
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
	// ResolutionTable reports, per registry metric, whether it published or
	// is unavailable and the precise reason, so the reviewer can distinguish a
	// true source gap from a missing implementation branch.
	ResolutionTable []Resolution `json:"resolution_table,omitempty"`
}

// Resolution is one registry metric's resolution for a match.
type Resolution struct {
	MetricID          string `json:"metric_id"`
	CapabilityLevel   string `json:"capability_level"`
	EpistemicClass    string `json:"epistemic_class"`
	ReportLevel       string `json:"report_level"`
	Published         bool   `json:"published"`
	PublishedPlayers  int    `json:"published_players"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	EvidenceCount     int64  `json:"evidence_count"`
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
	objectiveExcl   map[string]float64 // excluded non-objective damage magnitude
	objectiveExclN  map[string]int64   // excluded non-objective damage record count
	controlSeconds  map[string]float64
	heroStateSecs   map[string]map[int64]struct{} // distinct eligible second-grid bins
	// heal casts (for heal_dispel_save_casts) and smoke participations (for
	// smoke_activation_participation) per account.
	healCasts      map[string]int64
	smokeParticles map[string]int64
	// buyback post-participation: account -> seconds of buybacks that had a
	// follow-up combat/objective event within 60s.
	buybackRoundPart map[string]int64
	buybackTotal     map[string]int64
	damageEvents     []damageEvent                 // for fight_damage_share
	teams            map[string]map[string]float64 // team_id -> account -> networth (final)

	// evidence: "account\x00metric" -> ordered fact seq ids that produced the
	// metric's observations (fact_ids -> episode/phase/opportunity -> metric).
	evidence map[string][]int64

	// factsCoverage is the per-family availability from the match's facts
	// summary, used to resolve per-metric field gates precisely.
	factsCoverage map[string]bool
}

type damageEvent struct {
	GameSecond float64
	Actor      string
	Team       string
	Value      float64
	HeroTarget bool
	Building   bool
	FactSeq    int64
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
		objectiveDamage: map[string]float64{}, objectiveExcl: map[string]float64{},
		objectiveExclN: map[string]int64{},
		controlSeconds: map[string]float64{},
		heroStateSecs:  map[string]map[int64]struct{}{},
		teams:          map[string]map[string]float64{},
		evidence:       map[string][]int64{},
		factsCoverage:  map[string]bool{},
		healCasts:      map[string]int64{}, smokeParticles: map[string]int64{},
		buybackRoundPart: map[string]int64{}, buybackTotal: map[string]int64{},
	}
}

// SetFactsCoverage records which fact families the accepted adapter actually
// emitted for this match, so per-metric field gates resolve precisely instead
// of a capability-level blanket reason.
func (c *Calculator) SetFactsCoverage(covered []string) {
	c.factsCoverage = map[string]bool{}
	for _, f := range covered {
		c.factsCoverage[f] = true
	}
}

// MarkDerivedAvailable records a derived artifact (e.g. fight episodes or the
// phase stream) as available for per-metric field-gate resolution.
func (c *Calculator) MarkDerivedAvailable(key string) {
	c.factsCoverage[key] = true
}

// familyAvailable reports whether a fact family (registry human-readable name
// or adapter family constant) was emitted for the match. Families the adapter
// always emits on the gated path (participant binding, calibrated clock) are
// implicit requirements; adapter families not in the emitted set are absent.
func (c *Calculator) familyAvailable(family string) bool {
	for _, f := range adapterFamily(family) {
		if f == "always" {
			return true
		}
		if c.factsCoverage[f] {
			return true
		}
	}
	return false
}

// adapterFamily maps a registry required-fact-family name to the normalized
// fact-family constants (or "always" for gates the pipeline guarantees). An
// empty mapping means the family is not produced by the accepted adapter.
func adapterFamily(name string) []string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "participant binding", "participant_binding", "binding", "owner binding", "ownership", "hero ownership", "player binding":
		return []string{"always"}
	case "game clock", "calibrated clock", "calibrated game clock", "game_clock":
		return []string{"always"}
	case "hero position", "positions", "position", "hero positions", "enemy positions":
		return []string{facts.FamilyHeroState}
	case "alive state", "alive/status", "alive/positions", "health", "status":
		return []string{facts.FamilyHeroState}
	case "damage events", "damage/control events", "combat", "combat/control", "damage/control", "hero damage events", "damage":
		return []string{facts.FamilyCombat}
	case "heal", "healing event", "healing events":
		return []string{facts.FamilyCombat}
	case "item use", "item consumption", "purchase/combine events", "inventory lifecycle", "item", "items":
		return []string{facts.FamilyItem}
	case "ability", "ability/item definitions", "casts", "cast/impact", "ability casts", "cooldowns":
		return []string{facts.FamilyAbility}
	case "death/respawn", "death_respawn_buyback", "buyback", "buyback event", "death event", "hero death event", "buyback availability":
		return []string{facts.FamilyDeathRespawn}
	case "modifier lifecycle", "modifiers", "smoke modifiers", "smoke lifecycle", "modifier":
		return []string{facts.FamilyModifier}
	case "objective events", "objectives", "objective", "real building entities", "tower", "buildings":
		return []string{facts.FamilyObjective}
	case "ward entity spawn", "ward coordinate/lifecycle", "ward polygon/lifecycle", "ward spawn/death/expiry", "vision":
		return []string{facts.FamilyVision}
	case "fight intervals", "fight interval", "fight windows", "fight window", "team fight windows":
		return []string{"episodes_fight"}
	case "phase rounds", "phase", "official phase", "phase/threat":
		return []string{"phases"}
	default:
		// Geometry, creep lifecycle, rune state, team shapes, and other
		// families the accepted adapter does not emit.
		return nil
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
			c.addEvidence(drb.AccountID, "death_count", f.Seq)
			if drb.KillerAccount != "" {
				c.kills[drb.KillerAccount]++
				c.addEvidence(drb.KillerAccount, "kill_count", f.Seq)
			}
			for _, a := range drb.AssistAccounts {
				c.assists[a]++
				c.addEvidence(a, "assist_count", f.Seq)
			}
		case "buyback":
			c.buybacks[drb.AccountID]++
			c.buybackSeconds[drb.AccountID] = append(c.buybackSeconds[drb.AccountID], f.GameSecond)
			c.addEvidence(drb.AccountID, "buyback_use_count", f.Seq)
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
		obj := isObjectiveTarget(cf.TargetName)
		team := c.teamByAcct[cf.ActorAccount]
		switch cf.Kind {
		case "damage":
			if isHero {
				c.heroDamage[cf.ActorAccount] += v
				c.addEvidence(cf.ActorAccount, "hero_damage_total", f.Seq)
			} else if obj {
				// Only configured objective entities (tower/rax/ancient/
				// fort/shrine/Roshan/Tormentor) count as objective damage.
				c.objectiveDamage[cf.ActorAccount] += v
				c.addEvidence(cf.ActorAccount, "objective_damage_total", f.Seq)
			} else {
				// Lane/neutral creep damage and other non-objective targets
				// are excluded attribution: record count and magnitude are
				// preserved separately (never mixed into the objective total).
				c.objectiveExcl[cf.ActorAccount] += v
				c.objectiveExclN[cf.ActorAccount]++
			}
			c.damageEvents = append(c.damageEvents, damageEvent{
				GameSecond: f.GameSecond, Actor: cf.ActorAccount, Team: team,
				Value: v, HeroTarget: isHero, Building: obj, FactSeq: f.Seq,
			})
		case "heal":
			c.heroHealing[cf.ActorAccount] += v
			c.healCasts[cf.ActorAccount]++
			c.addEvidence(cf.ActorAccount, "hero_healing_total", f.Seq)
			c.addEvidence(cf.ActorAccount, "heal_dispel_save_casts", f.Seq)
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
			c.addEvidence(es.AccountID, "last_hit_count", f.Seq)
		}
		if es.Xp != nil && float64(*es.Xp) > c.xpDelta[es.AccountID] {
			c.xpDelta[es.AccountID] = float64(*es.Xp)
			c.addEvidence(es.AccountID, "xp_delta", f.Seq)
		}
		if es.Networth != nil {
			nw := float64(*es.Networth)
			if nw > c.netWorthDelta[es.AccountID] {
				c.netWorthDelta[es.AccountID] = nw
				c.addEvidence(es.AccountID, "net_worth_delta", f.Seq)
			}
			if c.teams[c.teamByAcct[es.AccountID]] == nil {
				c.teams[c.teamByAcct[es.AccountID]] = map[string]float64{}
			}
			c.teams[c.teamByAcct[es.AccountID]][es.AccountID] = nw
		}
		if es.Gold != nil {
			c.goldEarned[es.AccountID] += float64(*es.Gold)
			c.addEvidence(es.AccountID, "gold_earned", f.Seq)
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
			// Count distinct calibrated second-grid bins, not raw samples:
			// samples arrive at ~0.5s cadence but the metric is seconds.
			bin := int64(f.GameSecond)
			if bin >= 0 {
				if c.heroStateSecs[hs.AccountID] == nil {
					c.heroStateSecs[hs.AccountID] = map[int64]struct{}{}
				}
				c.heroStateSecs[hs.AccountID][bin] = struct{}{}
				c.addEvidence(hs.AccountID, "opportunity_duration_seconds", f.Seq)
			}
		}
	case facts.FamilyModifier:
		var mf facts.ModifierFact
		if err := json.Unmarshal(f.Payload, &mf); err != nil {
			return
		}
		// Smoke activation participation: the smoke-of-deceit modifier is
		// applied to each participant; count each account's smoke applications.
		if strings.Contains(mf.Modifier, "smoke_of_deceit") && mf.Kind == "add" && mf.AccountID != "" {
			c.smokeParticles[mf.AccountID]++
			c.addEvidence(mf.AccountID, "smoke_activation_participation", f.Seq)
		}
	}
}

// addEvidence appends a fact seq to the metric's evidence lineage, avoiding
// unbounded growth for high-frequency facts (bounded to a representative
// sample of the earliest observations).
func (c *Calculator) addEvidence(account, metric string, seq int64) {
	key := account + "\x00" + metric
	ev := c.evidence[key]
	if len(ev) < 64 {
		c.evidence[key] = append(ev, seq)
	}
}

// evidenceFor returns the collected fact seq ids for an account+metric.
func (c *Calculator) evidenceFor(account, metric string) []int64 {
	return c.evidence[account+"\x00"+metric]
}

// derivedEvidence resolves episode/phase evidence for metrics computed from
// the episodes/phases artifacts (e.g. fight_damage_share, fight
// participation), so the lineage chain is not empty for derived rows.
func (c *Calculator) derivedEvidence(metricID, acct string, fightParticipation map[string]int64, fightDamageShare map[string]*shareVal, eps *episodes.Output, ph *phase.Output) []int64 {
	switch metricID {
	case "fight_participation_count", "fight_damage_share":
		if eps != nil {
			var out []int64
			for i := range eps.Episodes {
				e := &eps.Episodes[i]
				if e.Kind != episodes.KindFight {
					continue
				}
				participates := false
				for _, p := range e.Participants {
					if p == acct {
						participates = true
						break
					}
				}
				if participates {
					out = append(out, e.EvidenceIDs...)
				}
			}
			return out
		}
	case "buyback_round_participation":
		// Evidence = post-buyback combat events by the same account within the
		// participation window (fact seq ids from the damage stream).
		var out []int64
		for _, bb := range c.buybackSeconds[acct] {
			for _, de := range c.damageEvents {
				if de.Actor != acct {
					continue
				}
				if de.GameSecond >= bb && de.GameSecond <= bb+60 {
					out = append(out, de.FactSeq)
				}
			}
		}
		return out
	}
	return nil
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
				// Fact -> episode/phase/opportunity -> metric evidence lineage.
				ev := c.evidenceFor(acct, m.ID)
				if len(ev) == 0 {
					ev = c.derivedEvidence(m.ID, acct, fightParticipation, fightDamageShare, eps, ph)
				}
				if len(ev) == 0 {
					// No evidence chain means the metric cannot be audited;
					// fail closed rather than publish an untraceable value.
					v.EpistemicClass = ClassUnavailable
					v.UnavailableReason = "no_evidence_lineage"
					v.Value = nil
					v.IntValue = nil
					v.Numerator = nil
					v.Denominator = nil
					v.Confidence = 0
					out.Unavailable = append(out.Unavailable, v)
					continue
				}
				v.EvidenceIDs = append([]int64(nil), ev...)
				if v.EvidenceCount == 0 {
					v.EvidenceCount = int64(len(ev))
				}
				out.Values = append(out.Values, v)
			}
		}
	}

	// Match-level metrics.
	if phaseDuration != nil {
		out.Values = append(out.Values, phaseDurationValue(reg, c.matchID, ph, phaseDuration))
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
	out.ResolutionTable = buildResolutionTable(out)
	return out
}

// buildResolutionTable derives the per-metric resolution summary.
func buildResolutionTable(o *Output) []Resolution {
	pubByMetric := map[string]int{}
	evByMetric := map[string]int64{}
	for _, v := range o.Values {
		pubByMetric[v.MetricID]++
		evByMetric[v.MetricID] += v.EvidenceCount
	}
	unavByMetric := map[string]string{}
	classByMetric := map[string]string{}
	for _, v := range o.Unavailable {
		if _, ok := unavByMetric[v.MetricID]; !ok {
			unavByMetric[v.MetricID] = v.UnavailableReason
		}
		classByMetric[v.MetricID] = v.EpistemicClass
	}
	table := []Resolution{}
	for i := range o.Definitions {
		d := &o.Definitions[i]
		r := Resolution{
			MetricID: d.ID, CapabilityLevel: d.CapabilityLevel,
			EpistemicClass: d.EpistemicClass, ReportLevel: d.ReportLevel,
			PublishedPlayers: pubByMetric[d.ID],
		}
		if r.PublishedPlayers > 0 {
			r.Published = true
			r.EvidenceCount = evByMetric[d.ID]
		} else {
			r.UnavailableReason = unavByMetric[d.ID]
			if r.UnavailableReason == "" {
				r.UnavailableReason = "not_applicable_for_any_participant"
			}
			if cls, ok := classByMetric[d.ID]; ok {
				r.EpistemicClass = cls
			}
		}
		table = append(table, r)
	}
	sort.Slice(table, func(i, j int) bool { return table[i].MetricID < table[j].MetricID })
	return table
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
		if len(c.evidenceFor(acct, "last_hit_count")) == 0 {
			v.UnavailableReason = "last_hit_state_missing"
		} else {
			v = c.countValue(c.lastHits[acct], m)
		}
	case "deny_count":
		v.UnavailableReason = "deny_counter_not_in_accepted_adapter"
	case "xp_delta":
		if len(c.evidenceFor(acct, "xp_delta")) == 0 {
			v.UnavailableReason = "xp_state_missing"
		} else {
			v = c.floatValue(c.xpDelta[acct], m)
		}
	case "net_worth_delta":
		if len(c.evidenceFor(acct, "net_worth_delta")) == 0 {
			v.UnavailableReason = "networth_state_missing"
		} else {
			v = c.floatValue(c.netWorthDelta[acct], m)
		}
	case "gold_earned":
		if len(c.evidenceFor(acct, "gold_earned")) == 0 {
			v.UnavailableReason = "gold_state_missing"
		} else {
			v = c.floatValue(c.goldEarned[acct], m)
		}
	case "team_resource_share":
		if len(c.evidenceFor(acct, "net_worth_delta")) == 0 {
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
		if c.objectiveDamage[acct] <= 0 && len(c.evidenceFor(acct, "objective_damage_total")) == 0 {
			v.UnavailableReason = "objective_damage_missing"
		} else {
			v = c.floatValue(c.objectiveDamage[acct], m)
			// excluded_count counts excluded damage RECORDS; the excluded
			// damage magnitude is a separate typed field.
			v.ExcludedCount = c.objectiveExclN[acct]
			if c.objectiveExcl[acct] > 0 {
				ed := c.objectiveExcl[acct]
				v.ExcludedDamage = &ed
			}
			v.EvidenceCount = int64(len(c.evidenceFor(acct, "objective_damage_total")))
		}
	case "fight_participation_count":
		v = c.countValue(fightParticipation[acct], m)
	case "opportunity_duration_seconds":
		bins := c.heroStateSecs[acct]
		if len(bins) == 0 {
			v.UnavailableReason = "position_state_missing"
		} else {
			n := int64(len(bins))
			v = c.countValue(n, m)
			if phaseDuration != nil {
				v.Denominator = phaseDuration
			}
			v.EvidenceCount = int64(len(c.evidenceFor(acct, "opportunity_duration_seconds")))
		}
	case "hero_damage_total":
		v = c.floatValue(c.heroDamage[acct], m)
	case "hero_healing_total":
		v = c.floatValue(c.heroHealing[acct], m)
	case "heal_dispel_save_casts":
		if c.healCasts[acct] == 0 && len(c.evidenceFor(acct, "heal_dispel_save_casts")) == 0 {
			v.UnavailableReason = "heal_cast_events_missing"
		} else {
			v = c.countValue(c.healCasts[acct], m)
		}
	case "smoke_activation_participation":
		if c.smokeParticles[acct] == 0 && len(c.evidenceFor(acct, "smoke_activation_participation")) == 0 {
			v.UnavailableReason = "smoke_modifier_events_missing"
		} else {
			v = c.countValue(c.smokeParticles[acct], m)
		}
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
		// Derived from verified buyback facts plus post-buyback combat within
		// a 60-second window: participation = buyback uses followed by at
		// least one hero damage/heal/objective event by the same player.
		num, den, ev := c.computeBuybackParticipation(acct)
		if den <= 0 {
			v.UnavailableReason = "buyback_events_missing"
		} else {
			rate := float64(num) / float64(den)
			v = c.floatValue(rate, m)
			nf := float64(num)
			df := float64(den)
			v.Numerator = &nf
			v.Denominator = &df
			v.OpportunityCount = den
			v.SampleCount = den
			v.EvidenceCount = ev
		}
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
// inputs are not produced by the accepted adapter. It names the specific
// required fact families/field gates that are missing for this match (from the
// actual facts coverage) rather than a blanket capability-level string, so a
// true source gap is distinguishable from a missing implementation branch.
func (c *Calculator) contractReason(m *Metric) string {
	var missing []string
	for _, fam := range m.RequiredFactFamilies {
		if !c.familyAvailable(fam) {
			missing = append(missing, fam)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("%s_input_missing_for_%s:%s", strings.ToLower(m.CapabilityLevel), m.ID, strings.Join(missing, "|"))
	}
	// All declared families present but the metric is still unsupported by
	// this adapter's field gates (e.g. lane geometry / creep lifecycle that
	// the registry lists but the adapter does not emit).
	return fmt.Sprintf("%s_field_gates_not_met:%s", strings.ToLower(m.CapabilityLevel), strings.Join(m.RequiredFactFamilies, "|"))
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
	return len(c.evidenceFor(acct, "net_worth_delta")) > 0 || len(c.evidenceFor(acct, "xp_delta")) > 0 || len(c.evidenceFor(acct, "gold_earned")) > 0
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

// computeBuybackParticipation derives buyback round participation from
// verified buyback facts and post-buyback combat within a 60-second window:
// numerator = buybacks followed by a player damage/heal/objective event,
// denominator = verified buyback uses. Evidence count is the number of
// qualifying post-buyback damage events.
func (c *Calculator) computeBuybackParticipation(acct string) (num, den, ev int64) {
	den = c.buybacks[acct]
	if den == 0 {
		return 0, 0, 0
	}
	for _, bb := range c.buybackSeconds[acct] {
		for _, de := range c.damageEvents {
			if de.Actor != acct {
				continue
			}
			if de.GameSecond >= bb && de.GameSecond <= bb+60 {
				num++
				ev++
				break
			}
		}
	}
	return num, den, ev
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

// ObjectiveEntityKind is the classified objective-entity family for a damage
// target, used by objective_damage_total's field gate.
type ObjectiveEntityKind int

const (
	ObjectiveUnknown ObjectiveEntityKind = iota
	ObjectiveTower
	ObjectiveBarracks
	ObjectiveAncientFort
	ObjectiveShrine
	ObjectiveRoshan
	ObjectiveTormentor
)

// classifyObjectiveTarget returns the objective-entity family of a damage
// target name. It uses an explicit taxonomy: a real structure is only a
// Radiant/Dire entity (`npc_dota_goodguys_*` / `npc_dota_badguys_*`) of kind
// tower / rax / fort-ancient / shrine, or the Roshan / Tormentor bosses.
// Neutral creeps — including `npc_dota_neutral_ancient_frog*` — are NEVER
// objectives, and summoned units (e.g. Roshan's banner) are excluded.
func classifyObjectiveTarget(name string) ObjectiveEntityKind {
	if name == "" {
		return ObjectiveUnknown
	}
	lower := strings.ToLower(name)
	side := strings.HasPrefix(lower, "npc_dota_goodguys_") || strings.HasPrefix(lower, "npc_dota_badguys_")
	switch {
	case strings.Contains(lower, "npc_dota_roshan") && !strings.Contains(lower, "roshans_banner"):
		return ObjectiveRoshan
	case strings.Contains(lower, "tormentor"):
		return ObjectiveTormentor
	}
	if !side {
		// Neutral/lane creeps and summons are not objectives even when their
		// name contains a structural word (e.g. neutral_ancient_frog).
		return ObjectiveUnknown
	}
	switch {
	case strings.Contains(lower, "tower"):
		return ObjectiveTower
	case strings.Contains(lower, "_rax_") || strings.Contains(lower, "rax"):
		return ObjectiveBarracks
	case strings.Contains(lower, "fort") || strings.Contains(lower, "ancient"):
		return ObjectiveAncientFort
	case strings.Contains(lower, "shrine"):
		return ObjectiveShrine
	}
	return ObjectiveUnknown
}

// isObjectiveTarget reports whether a damage target name is a configured
// objective entity (tower, barracks, ancient/fort, shrine, Roshan, or
// Tormentor) per the explicit taxonomy.
func isObjectiveTarget(name string) bool {
	return classifyObjectiveTarget(name) != ObjectiveUnknown
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

func phaseDurationValue(reg *Registry, matchID string, ph *phase.Output, dur *float64) Value {
	v := Value{
		MetricID: "phase_duration_seconds", Name: "Phase duration (match)",
		ReportLevel: "match", Unit: "seconds", EpistemicClass: ClassDerived,
		CapabilityLevel: CapabilityV1, MetricVersion: "1.0.0",
		Value: dur, SampleCount: 1, Confidence: 1.0, Direction: "context_only",
	}
	if ph != nil {
		var ev []int64
		for i := range ph.Intervals {
			ev = append(ev, ph.Intervals[i].EvidenceSeqs...)
		}
		v.EvidenceIDs = ev
		v.EvidenceCount = int64(len(ev))
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
