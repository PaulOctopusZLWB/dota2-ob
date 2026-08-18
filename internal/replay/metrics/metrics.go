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

// EvidenceRef is a typed, match-qualified lineage reference. Identity is
// (MatchID, Kind, ID, RuleVersion): equal entity ids from different matches
// never collapse, and a ref always names the rule/algorithm version that
// produced the referenced entity. Kinds: fact, episode, phase,
// metric_observation, aggregation, algorithm.
type EvidenceRef struct {
	MatchID       string `json:"match_id,omitempty"`
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	RuleVersion   string `json:"rule_version,omitempty"`
	SourceFactSeq int64  `json:"source_fact_seq,omitempty"`
}

// Evidence ref kinds.
const (
	EvidenceFact              = "fact"
	EvidenceEpisode           = "episode"
	EvidencePhase             = "phase"
	EvidenceMetricObservation = "metric_observation"
	EvidenceAggregation       = "aggregation"
	EvidenceAlgorithm         = "algorithm"
)

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
	GapCount         int64    `json:"gap_count"`
	Coverage         float64  `json:"coverage"`
	// ExcludedDamage is the magnitude of excluded (non-objective) damage
	// preserved separately; it is distinct from excluded_count (record count).
	ExcludedDamage            *float64      `json:"excluded_damage,omitempty"`
	SampleCount               int64         `json:"sample_count"`
	EvidenceCount             int64         `json:"evidence_count"`
	Evidence                  []EvidenceRef `json:"evidence,omitempty"`
	UnavailableReason         string        `json:"unavailable_reason,omitempty"`
	Confidence                float64       `json:"confidence"`
	Direction                 string        `json:"direction"`
	OfficialScoreEligible     bool          `json:"official_score_eligible"`
	ExperimentalScoreEligible bool          `json:"experimental_score_eligible"`
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
	// EvaluatorID, EntryPoint, AcceptedSourceFields, PublicationGate, and
	// UnavailableGate are the closure-contract fields persisted for
	// independent audit: the stable algorithm/evaluator id, the concrete Go
	// entry point, the accepted source fields, and the definition-specific
	// publication/abstention gates.
	EvaluatorID          string `json:"evaluator_id"`
	EntryPoint           string `json:"entry_point"`
	AcceptedSourceFields string `json:"accepted_source_fields"`
	PublicationGate      string `json:"publication_gate"`
	UnavailableGate      string `json:"unavailable_gate,omitempty"`
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
	xpDeltas        map[string][]xpDeltaEvt // per-second non-negative XP award deltas
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

	// evidence: "account\x00metric" -> ordered typed evidence refs (facts,
	// episodes, phases) that produced the metric's observations.
	evidence map[string][]EvidenceRef
	// samples is the numerator contributor count, never a shortcut for the
	// metric's independently declared opportunity.
	samples map[string]int64
	// opportunities contains independently evaluated opportunity facts for
	// count metrics whose opportunity differs from their numerator.
	opportunities map[string][]EvidenceRef

	// factsCoverage is the per-family availability from the match's facts
	// summary, used to resolve per-metric field gates precisely.
	factsCoverage map[string]bool

	// closure is the frozen 52-row metric closure contract (definition-
	// specific evaluator/gate mapping). When nil, every metric resolves to
	// unavailable (the runner always supplies the frozen closure).
	closure *Closure

	// pendingFacts preserves normalized observations until Result receives the
	// authoritative official-phase stream. No metric is accumulated before the
	// calibrated-clock and causal-phase gates can be applied.
	pendingFacts []*facts.Fact
	excluded     map[string]int64 // account\x00metric -> rejected clock/phase facts
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

// xpDeltaEvt is one non-negative XP award delta at a calibrated game second.
type xpDeltaEvt struct {
	GameSecond float64
	Value      float64
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
		xpDelta: map[string]float64{}, xpDeltas: map[string][]xpDeltaEvt{}, netWorthDelta: map[string]float64{},
		goldEarned: map[string]float64{}, firstItemSec: map[string]float64{},
		heroDamage: map[string]float64{}, heroHealing: map[string]float64{},
		objectiveDamage: map[string]float64{}, objectiveExcl: map[string]float64{},
		objectiveExclN: map[string]int64{},
		controlSeconds: map[string]float64{},
		heroStateSecs:  map[string]map[int64]struct{}{},
		teams:          map[string]map[string]float64{},
		evidence:       map[string][]EvidenceRef{},
		samples:        map[string]int64{},
		opportunities:  map[string][]EvidenceRef{},
		factsCoverage:  map[string]bool{},
		healCasts:      map[string]int64{}, smokeParticles: map[string]int64{},
		buybackRoundPart: map[string]int64{}, buybackTotal: map[string]int64{},
		excluded: map[string]int64{},
	}
}

// SetClosure binds the frozen 52-row metric closure contract.
func (c *Calculator) SetClosure(cl *Closure) { c.closure = cl }

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

// Feed buffers one fact line. Publication is intentionally deferred until
// Result has the authoritative phase stream needed for the calibrated-clock,
// non-pregame, and official-causal-phase gates.
func (c *Calculator) Feed(f *facts.Fact) {
	if f == nil {
		return
	}
	// Receiving a normalized fact is itself positive evidence that the
	// accepted adapter emitted this family. The runner additionally supplies
	// summary coverage, which is needed to publish observed zeroes when a
	// family is covered but has no subject numerator event.
	c.factsCoverage[f.Family] = true
	cp := *f
	cp.Payload = append(json.RawMessage(nil), f.Payload...)
	c.pendingFacts = append(c.pendingFacts, &cp)
}

// feedAccepted processes a fact that has already passed the calibrated-clock
// and official-phase gates.
func (c *Calculator) feedAccepted(f *facts.Fact) {
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
			victimTeam := c.teamByAcct[drb.AccountID]
			killerTeam := c.teamByAcct[drb.KillerAccount]
			// Opportunities are every verified opposing-hero death, evaluated
			// for all players on the killer's team independently of numerator
			// credit. The normalized death fact always carries the parser's
			// complete credited-assistant list (including an empty list).
			if victimTeam != "" && killerTeam != "" && victimTeam != killerTeam {
				for _, account := range c.accounts {
					if c.teamByAcct[account] == killerTeam {
						c.addOpportunity(account, "kill_count", f.Seq)
						c.addOpportunity(account, "assist_count", f.Seq)
					}
				}
			}
			c.deaths[drb.AccountID]++
			c.addEvidence(drb.AccountID, "death_count", f.Seq)
			c.addSample(drb.AccountID, "death_count")
			if drb.KillerAccount != "" {
				c.kills[drb.KillerAccount]++
				c.addEvidence(drb.KillerAccount, "kill_count", f.Seq)
				c.addSample(drb.KillerAccount, "kill_count")
			}
			for _, a := range drb.AssistAccounts {
				c.assists[a]++
				c.addEvidence(a, "assist_count", f.Seq)
				c.addSample(a, "assist_count")
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
			// hero_damage_total: sum resolved post-mitigation damage to bound
			// real ENEMY heroes. A target account that is not a bound
			// participant of the opposing team is excluded (self/friendly,
			// unbound summons), matching the registry's real-enemy-hero gate.
			enemyHero := isHero && cf.TargetAccount != cf.ActorAccount &&
				c.teamByAcct[cf.TargetAccount] != "" && c.teamByAcct[cf.TargetAccount] != team
			if enemyHero {
				c.heroDamage[cf.ActorAccount] += v
				c.addEvidence(cf.ActorAccount, "hero_damage_total", f.Seq)
				c.addSample(cf.ActorAccount, "hero_damage_total")
			} else if obj {
				// Only configured objective entities (tower/rax/ancient/
				// fort/shrine/Roshan/Tormentor) count as objective damage.
				c.objectiveDamage[cf.ActorAccount] += v
				c.addEvidence(cf.ActorAccount, "objective_damage_total", f.Seq)
				c.addSample(cf.ActorAccount, "objective_damage_total")
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
		if es.Xp != nil && *es.Xp >= 0 {
			// The accepted adapter emits per-award XP deltas (non-negative).
			// The exact metric is the SUM of non-negative deltas across valid
			// contiguous calibrated segments, not the maximum award.
			c.xpDelta[es.AccountID] += float64(*es.Xp)
			c.xpDeltas[es.AccountID] = append(c.xpDeltas[es.AccountID], xpDeltaEvt{GameSecond: f.GameSecond, Value: float64(*es.Xp)})
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

// officialPhaseAt returns the single official causal phase containing sec.
func officialPhaseAt(ph *phase.Output, sec float64) (string, bool) {
	if ph == nil || sec < 0 {
		return "", false
	}
	for i := range ph.Intervals {
		iv := &ph.Intervals[i]
		if !iv.GlobalPhase.Official() {
			continue
		}
		if sec >= float64(iv.StartGameSecond) && sec < float64(iv.EndGameSecond) {
			return string(iv.GlobalPhase), true
		}
	}
	return "", false
}

func directPhaseMetric(id string) bool {
	switch id {
	case "kill_count", "assist_count", "death_count", "hero_damage_total", "objective_damage_total":
		return true
	default:
		return false
	}
}

func (c *Calculator) excludedKey(account, metric string) string { return account + "\x00" + metric }

func (c *Calculator) addSample(account, metric string) {
	if account != "" {
		c.samples[c.excludedKey(account, metric)]++
	}
}

func (c *Calculator) addOpportunity(account, metric string, seq int64) {
	if account == "" {
		return
	}
	key := c.excludedKey(account, metric)
	c.opportunities[key] = append(c.opportunities[key], EvidenceRef{
		MatchID: c.matchID, Kind: EvidenceFact, ID: fmt.Sprintf("fact:%d", seq),
		RuleVersion: version.FactsSchema, SourceFactSeq: seq,
	})
}

func (c *Calculator) addExcluded(account, metric string) {
	if account != "" {
		c.excluded[c.excludedKey(account, metric)]++
	}
}

// recordRejected records definition-specific exclusions for facts rejected by
// clock/phase gates. Rejected values never enter numerators.
func (c *Calculator) recordRejected(f *facts.Fact) {
	switch f.Family {
	case facts.FamilyDeathRespawn:
		var d facts.DeathRespawnBuyback
		if json.Unmarshal(f.Payload, &d) != nil {
			return
		}
		if d.Kind == "death" {
			c.addExcluded(d.AccountID, "death_count")
			c.addExcluded(d.KillerAccount, "kill_count")
			for _, a := range d.AssistAccounts {
				c.addExcluded(a, "assist_count")
			}
		} else if d.Kind == "buyback" {
			c.addExcluded(d.AccountID, "buyback_use_count")
		}
	case facts.FamilyCombat:
		var cf facts.CombatFact
		if json.Unmarshal(f.Payload, &cf) != nil || cf.Kind != "damage" || cf.ActorAccount == "" {
			return
		}
		team := c.teamByAcct[cf.ActorAccount]
		enemyHero := cf.TargetAccount != "" && cf.TargetAccount != cf.ActorAccount &&
			c.teamByAcct[cf.TargetAccount] != "" && c.teamByAcct[cf.TargetAccount] != team
		if enemyHero {
			c.addExcluded(cf.ActorAccount, "hero_damage_total")
		} else if isObjectiveTarget(cf.TargetName) {
			c.addExcluded(cf.ActorAccount, "objective_damage_total")
		}
	}
}

func (c *Calculator) scopedCalculator() *Calculator {
	pc := NewCalculator(c.matchID, c.accounts, c.accountName, c.teamByAcct)
	pc.registry, pc.closure = c.registry, c.closure
	pc.SetRoles(c.roleByAcct)
	pc.SetTeamOfSide(c.teamOfSide)
	for family, ok := range c.factsCoverage {
		if ok {
			pc.factsCoverage[family] = true
		}
	}
	return pc
}

// prepareFacts applies the immutable clock/phase gate once and builds one
// isolated calculator per official phase plus this calculator's whole-match
// reconciliation accumulators.
func (c *Calculator) prepareFacts(ph *phase.Output) map[string]*Calculator {
	pcs := map[string]*Calculator{
		"laning": c.scopedCalculator(), "midgame": c.scopedCalculator(), "decisive": c.scopedCalculator(),
	}
	if !validOfficialPhaseCoverage(ph) {
		for _, f := range c.pendingFacts {
			c.recordRejected(f)
		}
		return pcs
	}
	for _, f := range c.pendingFacts {
		phaseName, ok := officialPhaseAt(ph, f.GameSecond)
		if !f.GameSecondOK || !ok {
			c.recordRejected(f)
			continue
		}
		c.feedAccepted(f)
		pcs[phaseName].feedAccepted(f)
	}
	return pcs
}

// addEvidence appends every contributing typed fact ref. Published numerators
// must be exactly reconstructible from canonical lineage; sampling belongs in
// presentation/query layers, never in the authoritative artifact.
func (c *Calculator) addEvidence(account, metric string, seq int64) {
	key := account + "\x00" + metric
	c.evidence[key] = append(c.evidence[key], EvidenceRef{
		MatchID:       c.matchID,
		Kind:          EvidenceFact,
		ID:            fmt.Sprintf("fact:%d", seq),
		RuleVersion:   version.FactsSchema,
		SourceFactSeq: seq,
	})
}

// dedupeEvidence removes duplicate evidence refs by match+kind+id, preserving
// order.
func dedupeEvidence(refs []EvidenceRef) []EvidenceRef {
	seen := map[string]bool{}
	out := make([]EvidenceRef, 0, len(refs))
	for _, r := range refs {
		key := r.MatchID + "\x00" + r.Kind + "\x00" + r.ID
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}

// evidenceFor returns the collected typed evidence refs for an account+metric.
func (c *Calculator) evidenceFor(account, metric string) []EvidenceRef {
	return c.evidence[account+"\x00"+metric]
}

// derivedEvidence resolves episode/phase evidence for metrics computed from
// the episodes/phases artifacts (e.g. fight_damage_share, fight
// participation, buyback round participation), so the lineage chain carries
// typed episode/phase identities instead of flattened fact sequences. It is
// pure: it returns the derived refs without mutating calculator state.
func (c *Calculator) derivedEvidence(metricID, acct string, fightParticipation map[string]int64, fightDamageShare map[string]*shareVal, eps *episodes.Output, ph *phase.Output) []EvidenceRef {
	var out []EvidenceRef
	switch metricID {
	case "fight_participation_count", "fight_damage_share":
		if eps != nil {
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
					out = append(out, EvidenceRef{MatchID: c.matchID, Kind: EvidenceEpisode, ID: e.ID, RuleVersion: e.RuleVersion})
					for _, fs := range e.EvidenceIDs {
						out = append(out, EvidenceRef{MatchID: c.matchID, Kind: EvidenceFact, ID: fmt.Sprintf("fact:%d", fs), SourceFactSeq: fs, RuleVersion: version.FactsSchema})
					}
				}
			}
		}
	case "opportunity_duration_seconds":
		if ph != nil {
			for i := range ph.Intervals {
				iv := &ph.Intervals[i]
				if !iv.GlobalPhase.Official() {
					continue
				}
				out = append(out, EvidenceRef{
					MatchID: c.matchID, Kind: EvidencePhase,
					ID:          fmt.Sprintf("interval@%d-%d", iv.StartGameSecond, iv.EndGameSecond),
					RuleVersion: iv.RuleVersion,
				})
			}
		}
	case "buyback_round_participation":
		// Evidence = post-buyback combat events by the same account within the
		// participation window (typed fact refs from the damage stream).
		for _, bb := range c.buybackSeconds[acct] {
			for _, de := range c.damageEvents {
				if de.Actor != acct {
					continue
				}
				if de.GameSecond >= bb && de.GameSecond <= bb+60 {
					out = append(out, EvidenceRef{MatchID: c.matchID, Kind: EvidenceFact, ID: fmt.Sprintf("fact:%d", de.FactSeq), SourceFactSeq: de.FactSeq, RuleVersion: version.FactsSchema})
				}
			}
		}
	}
	return out
}

// algorithmRef returns the algorithm/evaluator identity ref for a metric from
// the closure contract.
func (c *Calculator) algorithmRef(metricID string) EvidenceRef {
	evaluator := "unassigned"
	if c.closure != nil {
		if row := c.closure.Lookup(metricID); row != nil {
			evaluator = row.EvaluatorID
		}
	}
	return EvidenceRef{
		MatchID:     c.matchID,
		Kind:        EvidenceAlgorithm,
		ID:          evaluator,
		RuleVersion: version.MetricsRuleVersion,
	}
}

// observationRef returns the metric-observation identity ref for a value.
func (c *Calculator) observationRef(v Value) EvidenceRef {
	id := v.MetricID
	if v.AccountID != "" {
		id = v.MetricID + ":" + v.AccountID
	} else if v.TeamID != "" {
		id = v.MetricID + ":" + v.TeamID
	} else {
		id = v.MetricID + ":match"
	}
	return EvidenceRef{
		MatchID:     c.matchID,
		Kind:        EvidenceMetricObservation,
		ID:          id,
		RuleVersion: v.MetricVersion,
	}
}

func decorateValue(v Value, m *Metric, acct, team, role string) Value {
	v.MetricID = m.ID
	v.Name = m.Name
	v.ReportLevel = m.ReportLevel
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
	return v
}

func phaseDurationMap(ph *phase.Output) map[string]float64 {
	out := map[string]float64{"laning": 0, "midgame": 0, "decisive": 0}
	if ph == nil {
		return out
	}
	for _, iv := range ph.Intervals {
		if iv.GlobalPhase.Official() {
			out[string(iv.GlobalPhase)] += float64(iv.EndGameSecond - iv.StartGameSecond)
		}
	}
	return out
}

func validOfficialPhaseCoverage(ph *phase.Output) bool {
	if ph == nil || ph.EligibleSeconds <= 0 || len(ph.Intervals) == 0 {
		return false
	}
	prev := 0
	for i := range ph.Intervals {
		iv := &ph.Intervals[i]
		if !iv.GlobalPhase.Official() || iv.StartGameSecond != prev || iv.EndGameSecond <= iv.StartGameSecond {
			return false
		}
		prev = iv.EndGameSecond
	}
	return prev == ph.EligibleSeconds
}

func phaseEvidence(matchID, phaseName string, ph *phase.Output) []EvidenceRef {
	var out []EvidenceRef
	if ph == nil {
		return out
	}
	for i := range ph.Intervals {
		iv := &ph.Intervals[i]
		if phaseName != "whole_match" && string(iv.GlobalPhase) != phaseName {
			continue
		}
		if !iv.GlobalPhase.Official() {
			continue
		}
		out = append(out, EvidenceRef{MatchID: matchID, Kind: EvidencePhase,
			ID: fmt.Sprintf("interval@%d-%d", iv.StartGameSecond, iv.EndGameSecond), RuleVersion: iv.RuleVersion})
	}
	return out
}

func finalizeDirectValue(calc *Calculator, v Value, m *Metric, acct, team, role, phaseName string, duration float64, ph *phase.Output, excluded int64) (Value, bool, string) {
	v = decorateValue(v, m, acct, team, role)
	contributors := dedupeEvidence(calc.evidenceFor(acct, m.ID))
	samples := calc.samples[calc.excludedKey(acct, m.ID)]
	if v.UnavailableReason != "" {
		return v, false, v.UnavailableReason
	}
	if duration <= 0 {
		return v, false, "eligible_official_phase_duration_not_proven"
	}
	var opportunity int64
	var opportunityEvidence []EvidenceRef
	switch m.ID {
	case "kill_count", "assist_count":
		opportunityEvidence = dedupeEvidence(calc.opportunities[calc.excludedKey(acct, m.ID)])
		opportunity = int64(len(opportunityEvidence))
		if opportunity == 0 {
			return v, false, "eligible_opposing_hero_death_opportunity_not_proven"
		}
	case "death_count":
		if !calc.factsCoverage[facts.FamilyDeathRespawn] {
			return v, false, "bound_real_hero_life_interval_not_proven"
		}
		// Positive duration plus participant binding prove one intersecting
		// bound-real-hero life interval even when its death numerator is zero.
		// Each verified death proves its own completed life interval; without
		// an emitted respawn transition we do not fabricate a post-death one.
		opportunity = calc.deaths[acct]
		if opportunity == 0 {
			opportunity = 1
		}
		opportunityEvidence = contributors
	case "hero_damage_total", "objective_damage_total":
		opportunity = samples
		if opportunity == 0 {
			return v, false, "eligible_damage_event_opportunity_not_proven"
		}
	default:
		return v, false, "declared_opportunity_evaluator_missing"
	}
	v.OfficialPhase = phaseName
	v.Denominator = &duration
	v.OpportunityCount = opportunity
	v.SampleCount = samples
	v.EvidenceCount = samples
	v.ExcludedCount += excluded
	v.Coverage = 1.0
	chain := append([]EvidenceRef(nil), contributors...)
	chain = append(chain, opportunityEvidence...)
	chain = append(chain, phaseEvidence(calc.matchID, phaseName, ph)...)
	chain = append(chain, calc.observationRef(v), calc.algorithmRef(m.ID))
	v.Evidence = dedupeEvidence(chain)
	return v, true, ""
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
	phaseCalcs := c.prepareFacts(ph)
	phaseDurations := phaseDurationMap(ph)

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
			if directPhaseMetric(m.ID) {
				published := false
				for _, phaseName := range []string{"laning", "midgame", "decisive"} {
					pc := phaseCalcs[phaseName]
					pv := pc.computeMetric(m, acct, team, role, nil, nil, nil, nil)
					if pv, ok, _ := finalizeDirectValue(pc, pv, m, acct, team, role, phaseName, phaseDurations[phaseName], ph, 0); ok {
						out.Values = append(out.Values, pv)
						published = true
					}
				}
				whole := c.computeMetric(m, acct, team, role, teamNetWorth, fightParticipation, fightDamageShare, phaseDuration)
				totalDuration := phaseDurations["laning"] + phaseDurations["midgame"] + phaseDurations["decisive"]
				wholeReason := ""
				if wholeValue, ok, reason := finalizeDirectValue(c, whole, m, acct, team, role, "whole_match", totalDuration, ph, c.excluded[c.excludedKey(acct, m.ID)]); ok {
					out.Values = append(out.Values, wholeValue)
					published = true
				} else {
					wholeReason = reason
				}
				if !published {
					if wholeReason == "" {
						wholeReason = "declared_opportunity_or_duration_not_proven"
					}
					u := decorateValue(Value{UnavailableReason: wholeReason}, m, acct, team, role)
					u.EpistemicClass = ClassUnavailable
					u.ExcludedCount = c.excluded[c.excludedKey(acct, m.ID)]
					out.Unavailable = append(out.Unavailable, u)
				}
				continue
			}
			v := c.computeMetric(m, acct, team, role, teamNetWorth, fightParticipation, fightDamageShare, phaseDuration)
			v = decorateValue(v, m, acct, team, role)
			if v.UnavailableReason != "" {
				v.EpistemicClass = ClassUnavailable
				out.Unavailable = append(out.Unavailable, v)
			} else {
				// Fact -> episode/phase/opportunity -> metric evidence lineage.
				ev := c.evidenceFor(acct, m.ID)
				ev = append(ev, c.derivedEvidence(m.ID, acct, fightParticipation, fightDamageShare, eps, ph)...)
				ev = dedupeEvidence(ev)
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
				// Complete typed chain: evidence entities + the metric
				// observation identity + the algorithm/evaluator identity.
				chain := append([]EvidenceRef(nil), ev...)
				chain = append(chain, c.observationRef(v), c.algorithmRef(m.ID))
				v.Evidence = chain
				if v.EvidenceCount == 0 {
					v.EvidenceCount = int64(len(ev))
				}
				out.Values = append(out.Values, v)
			}
		}
	}

	// Match-level phase-duration observations: one row per official phase plus
	// one explicitly labelled whole-match reconciliation.
	if phaseDuration != nil {
		out.Values = append(out.Values, c.phaseDurationValues(reg, c.matchID, ph)...)
	} else {
		out.Unavailable = append(out.Unavailable, phaseDurationUnavailable(reg, c.matchID))
	}

	sort.Slice(out.Values, func(i, j int) bool {
		if out.Values[i].AccountID != out.Values[j].AccountID {
			return out.Values[i].AccountID < out.Values[j].AccountID
		}
		if out.Values[i].MetricID != out.Values[j].MetricID {
			return out.Values[i].MetricID < out.Values[j].MetricID
		}
		return out.Values[i].OfficialPhase < out.Values[j].OfficialPhase
	})
	sort.Slice(out.Unavailable, func(i, j int) bool {
		if out.Unavailable[i].AccountID != out.Unavailable[j].AccountID {
			return out.Unavailable[i].AccountID < out.Unavailable[j].AccountID
		}
		return out.Unavailable[i].MetricID < out.Unavailable[j].MetricID
	})
	out.ResolutionTable = c.buildResolutionTable(out)
	return out
}

// buildResolutionTable derives the per-metric resolution summary from the
// closure contract plus the computed values/unavailable rows.
func (c *Calculator) buildResolutionTable(o *Output) []Resolution {
	pubSubjects := map[string]map[string]struct{}{}
	evByMetric := map[string]int64{}
	for _, v := range o.Values {
		if pubSubjects[v.MetricID] == nil {
			pubSubjects[v.MetricID] = map[string]struct{}{}
		}
		subject := v.AccountID
		if subject == "" {
			subject = "match"
		}
		pubSubjects[v.MetricID][subject] = struct{}{}
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
			PublishedPlayers: len(pubSubjects[d.ID]),
		}
		if c.closure != nil {
			if row := c.closure.Lookup(d.ID); row != nil {
				r.EvaluatorID = row.EvaluatorID
				r.EntryPoint = row.EntryPoint
				r.AcceptedSourceFields = row.AcceptedSourceFields
				r.PublicationGate = row.PublicationGate
				r.UnavailableGate = row.UnavailableGate
			}
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
		// The accepted adapter emits buyback transitions but not the buyback
		// availability/cost state for every eligible death. Consequently the
		// frozen denominator cannot be proven exactly; fail closed.
		v.UnavailableReason = "eligible_death_buyback_state_denominator_not_in_accepted_adapter"
	case "last_hit_count":
		// The registry requires validated monotonic increments with explicit
		// reset/gap reconciliation. The adapter exposes occasional absolute
		// counters only, so taking the maximum is not the defined quantity.
		v.UnavailableReason = "last_hit_counter_reset_gap_reconciliation_not_in_accepted_adapter"
	case "deny_count":
		v.UnavailableReason = "deny_counter_not_in_accepted_adapter"
	case "xp_delta":
		// The normalized XP facts are combat-log awards, not experience-state
		// endpoints. They cannot prove valid contiguous monotonic segments or
		// disclose state gaps/resets, so summing awards is not publishable.
		v.UnavailableReason = "experience_state_endpoint_segment_gap_gates_not_in_accepted_adapter"
	case "net_worth_delta":
		// A maximum sampled net worth is not the signed end-minus-start delta
		// across valid segments required by the registry.
		v.UnavailableReason = "networth_endpoint_segment_gap_reset_gates_not_in_accepted_adapter"
	case "gold_earned":
		if len(c.evidenceFor(acct, "gold_earned")) == 0 {
			v.UnavailableReason = "gold_state_missing"
		} else {
			v = c.floatValue(c.goldEarned[acct], m)
		}
	case "team_resource_share":
		// Latest/maxima from independently timed samples are not complete
		// synchronized five-player frames and cannot be duration weighted.
		v.UnavailableReason = "complete_synchronized_team_resource_frames_not_in_accepted_adapter"
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
		// Registry: count each eligible fight window at most once per player
		// when at least one configured direct action is present AND the window
		// has sufficient actor/target coverage. The accepted adapter's fight
		// intervals lack a per-window coverage gate, so the count cannot be
		// verified against the eligibility predicate. Suppressed at the gate.
		v.UnavailableReason = "fight_window_coverage_gate_not_in_accepted_adapter"
	case "opportunity_duration_seconds":
		// Registry: numerator = seconds where every required field and the
		// configured opportunity predicate are valid (a per-opportunity-key,
		// per-official-phase field-quality mask); denominator = candidate
		// seconds before exclusions; exclusions by reason. Distinct
		// position-sample seconds are NOT the registry's opportunity-key
		// field-quality mask: the accepted adapter emits position samples
		// without the configured opportunity predicate, per-phase field-quality
		// masks, or per-reason exclusion bookkeeping, so an exact value cannot
		// be produced. Suppressed at the exact gate (never a null-phase,
		// zero-opportunity proxy).
		v.UnavailableReason = "opportunity_key_field_quality_mask_not_in_accepted_adapter"
	case "hero_damage_total":
		v = c.floatValue(c.heroDamage[acct], m)
	case "hero_healing_total":
		// Registry: numerator = resolved EFFECTIVE healing to bound real ALLIED
		// heroes after overheal exclusion, target/owner gates, and the
		// calibrated clock. The accepted adapter emits raw heal values without
		// overheal classification or a verified allied-real-hero target gate,
		// so a raw sum is not the defined effective-healing quantity.
		v.UnavailableReason = "effective_healing_overheal_target_gates_not_in_accepted_adapter"
	case "heal_dispel_save_casts":
		// Registry: opportunity-normalized save/dispel casts keyed on ready
		// ability/item sources with a valid ally target (heal, dispel,
		// health/status, cooldown, position evidence). The accepted adapter
		// only emits raw heal facts (including regen ticks) with no ready-
		// source / cooldown / target-need evidence, so the registry numerator
		// and opportunity denominator cannot be resolved. Publishing raw heal
		// counts as "save casts" would violate the definition, so this metric
		// is explicitly unavailable at its exact field gate.
		v.UnavailableReason = "save_cast_opportunity_gate_not_met:requires_ready_source_cooldown_target_need_evidence_not_in_accepted_adapter"
	case "smoke_activation_participation":
		// Registry: ratio_0_1 = typed smoke events/participations (numerator)
		// over verified smoke charges available to the team and eligible alive
		// team members (denominator), joined to the consumed charge and
		// modifier lifecycle. The accepted adapter emits smoke modifier
		// applications but no charge inventory or eligible-member denominator,
		// so the registry ratio cannot be resolved; a raw modifier count is
		// not the defined ratio. Explicitly unavailable at its exact gate.
		v.UnavailableReason = "smoke_charge_opportunity_gate_not_met:requires_smoke_charge_inventory_and_eligible_member_denominator_not_in_accepted_adapter"
	case "control_duration_seconds":
		v.UnavailableReason = "control_modifier_registry_not_in_accepted_adapter"
	case "phase_duration_seconds":
		// player-level aggregate view is unavailable; the match-level metric
		// is emitted separately. Keep the player row unavailable to avoid
		// double counting.
		v.UnavailableReason = "reported_at_match_level"
	case "fight_damage_share":
		// Registry: qualifying player-attributed real-hero damage inside a
		// VERIFIED fight interval restricted to the declared midgame/decisive
		// phase, active/alive opportunities, and >=98% actor/target resolution
		// for the interval. The accepted adapter emits fight intervals but no
		// per-second alive/active state, no phase-scoped interval attribution,
		// and no actor/target resolution metric, so the all-fight pooled share
		// is not the defined quantity. Suppressed at the exact gate.
		v.UnavailableReason = "fight_share_phase_alive_resolution_gates_not_in_accepted_adapter"
	case "buyback_round_participation":
		// The registry publishes a round-scoped outcome vector, not a scalar
		// "any action within 60 seconds" proxy. Round identity, position,
		// availability/cost, and complete outcome/censor fields are unavailable.
		v.UnavailableReason = "buyback_round_identity_position_outcome_vector_gates_not_in_accepted_adapter"
	default:
		// Definition-specific closure dispatch: the frozen 52-row closure
		// contract maps every registry metric to a stable evaluator and a
		// precise unavailable gate. There is no generic default resolution
		// path; an unmapped id is a hard failure.
		row := c.closureRow(m.ID)
		if row == nil {
			v.UnavailableReason = "no_closure_entry:" + m.ID
			break
		}
		if row.Resolved == "published" {
			v.UnavailableReason = "closure_published_but_no_entry_point:" + m.ID
			break
		}
		v.UnavailableReason = c.definitionGateReason(m, row)
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

// closureRow returns the closure entry for a metric, or nil.
func (c *Calculator) closureRow(id string) *ClosureRow {
	if c.closure == nil {
		return nil
	}
	return c.closure.Lookup(id)
}

// definitionGateReason produces the precise unavailable reason for a metric
// whose closure row marks it unavailable. It names the specific required
// fact families/field gates that the accepted adapter does not emit (from the
// actual facts coverage) rather than a blanket capability-level string, so a
// true source gap is distinguishable from a missing implementation branch.
func (c *Calculator) definitionGateReason(m *Metric, row *ClosureRow) string {
	if row != nil && row.UnavailableGate != "" {
		// The closure contract already pins the definition-specific gate
		// reason; keep it verbatim so the persisted reason is auditable.
		return row.UnavailableGate
	}
	var missing []string
	for _, fam := range m.RequiredFactFamilies {
		if !c.familyAvailable(fam) {
			missing = append(missing, fam)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("%s_input_missing_for_%s:%s", strings.ToLower(m.CapabilityLevel), m.ID, strings.Join(missing, "|"))
	}
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
	if !validOfficialPhaseCoverage(ph) {
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

func (c *Calculator) phaseDurationValues(reg *Registry, matchID string, ph *phase.Output) []Value {
	durations := phaseDurationMap(ph)
	total := durations["laning"] + durations["midgame"] + durations["decisive"]
	if total <= 0 {
		return nil
	}
	var out []Value
	for _, phaseName := range []string{"laning", "midgame", "decisive", "whole_match"} {
		dur := total
		if phaseName != "whole_match" {
			dur = durations[phaseName]
		}
		if dur <= 0 {
			continue
		}
		v := Value{
			MetricID: "phase_duration_seconds", Name: "Phase duration",
			ReportLevel: "match", OfficialPhase: phaseName,
			Unit: "seconds", EpistemicClass: ClassDerived,
			CapabilityLevel: CapabilityV1, MetricVersion: "1.0.0",
			Value: &dur, Numerator: &dur, Denominator: &total,
			OpportunityCount: int64(dur), SampleCount: int64(dur),
			Coverage: 1.0, Confidence: 1.0, Direction: "context_only",
		}
		ev := phaseEvidence(c.matchID, phaseName, ph)
		v.Evidence = append(ev, c.observationRef(v), c.algorithmRef("phase_duration_seconds"))
		v.EvidenceCount = int64(len(ev))
		out = append(out, v)
	}
	return out
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
