// Package scoring implements the eight-axis role radar and the official and
// experimental total-score computation exactly from the frozen machine-readable
// scoring contract (docs/specs/ti2026-radar-scoring-v1.json). Metrics are
// aggregated by their declared numerator/denominator rule first, then player
// tournament records are normalized to same-role empirical mid-rank
// percentiles over the frozen TI 2026 corpus. Axes and totals use versioned
// role-specific weights. Axes and totals are suppressed when mandatory inputs,
// the subject's eligible match/opportunity coverage, or the coverage gates
// fail — an unavailable metric is never imputed as 0 or 50. Official scores
// never contain V3 inputs; the experimental V3 layer is a separately named
// dashed value. Team-match and team-tournament scores are computed under a
// documented derived team registry.
package scoring

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
)

// Contract is the frozen radar/score contract.
type Contract struct {
	SchemaVersion            string `json:"schema_version"`
	Issue                    string `json:"issue"`
	ComparisonPopulation     string `json:"comparison_population"`
	ScoreRange               []int  `json:"score_range"`
	MetricNormalization      string `json:"metric_normalization"`
	OfficialMetricSource     string `json:"official_metric_source"`
	ExperimentalMetricSource string `json:"experimental_metric_source"`
	MinimumMatches           int    `json:"minimum_matches"`
	MinimumPublishableAxes   int    `json:"minimum_publishable_axes"`
	MissingMetricPolicy      string `json:"missing_metric_policy"`
	OptionalAxisPolicy       string `json:"optional_axis_policy"`
	MandatoryAxisPolicy      string `json:"mandatory_axis_policy"`
	ExperimentalAxisFormula  struct {
		OfficialBaseWeight float64 `json:"official_base_weight"`
		V3ComponentWeight  float64 `json:"v3_component_weight"`
		Rule               string  `json:"rule"`
	} `json:"experimental_axis_formula"`
	OfficialTotalScore struct {
		ID              string `json:"id"`
		InputAxes       string `json:"input_axes"`
		Gate            string `json:"gate"`
		Formula         string `json:"formula"`
		MissingAxisRule string `json:"missing_axis_rule"`
		ComparisonScope string `json:"comparison_scope"`
	} `json:"official_total_score"`
	ExperimentalTotalScore struct {
		ID                                string  `json:"id"`
		OfficialTotalPrerequisite         string  `json:"official_total_prerequisite"`
		EligibleAxisSet                   string  `json:"eligible_axis_set"`
		MinimumExperimentalAxes           int     `json:"minimum_experimental_axes"`
		MinimumOriginalAxisWeightCoverage float64 `json:"minimum_original_axis_weight_coverage"`
		Formula                           string  `json:"formula"`
		MissingAxisRule                   string  `json:"missing_axis_rule"`
		V3Gate                            string  `json:"v3_gate"`
		DisplayRule                       string  `json:"display_rule"`
	} `json:"experimental_total_score"`
	Axes                         []AxisDef                                `json:"axes"`
	OfficialAxisComponentsByRole map[string]map[string]map[string]float64 `json:"official_axis_components_by_role"`
	AxisWeightsByRole            map[string]map[string]float64            `json:"axis_weights_by_role"`
	MandatoryAxesByRole          map[string][]string                      `json:"mandatory_axes_by_role"`
	OptionalAxesByRole           map[string][]string                      `json:"optional_axes_by_role"`
	ExperimentalComponentsByAxis map[string][]string                      `json:"experimental_components_by_axis"`
	DisplayRequirements          []string                                 `json:"display_requirements"`
}

// AxisDef is one radar axis definition.
type AxisDef struct {
	ID            string `json:"id"`
	DisplayNameZh string `json:"display_name_zh"`
	Description   string `json:"description"`
}

// SchemaVersion is the frozen contract version.
const SchemaVersion = "ti2026.radar-score.v1"

// TeamSchemaVersion is the frozen team scoring registry version.
const TeamSchemaVersion = "ti2026.team-score.v1"

// TeamContract is the frozen team scoring registry
// (docs/specs/ti2026-team-scoring-v1.json). The master spec requires team
// radar/total under a separate versioned registry; team weights and component
// weights are frozen here and never derived from the player contract.
type TeamContract struct {
	SchemaVersion          string                        `json:"schema_version"`
	Issue                  string                        `json:"issue"`
	ComparisonPopulation   string                        `json:"comparison_population"`
	ScoreRange             []int                         `json:"score_range"`
	MetricNormalization    string                        `json:"metric_normalization"`
	OfficialMetricSource   string                        `json:"official_metric_source"`
	MinimumMatches         int                           `json:"minimum_matches"`
	MinimumPublishableAxes int                           `json:"minimum_publishable_axes"`
	MissingMetricPolicy    string                        `json:"missing_metric_policy"`
	MandatoryAxisPolicy    string                        `json:"mandatory_axis_policy"`
	OptionalAxisPolicy     string                        `json:"optional_axis_policy"`
	Axes                   []AxisDef                     `json:"axes"`
	OfficialAxisComponents map[string]map[string]float64 `json:"official_axis_components"`
	AxisWeights            map[string]float64            `json:"axis_weights"`
	MandatoryAxes          []string                      `json:"mandatory_axes"`
	OptionalAxes           []string                      `json:"optional_axes"`
	OfficialTotalScore     struct {
		ID              string `json:"id"`
		Gate            string `json:"gate"`
		Formula         string `json:"formula"`
		MissingAxisRule string `json:"missing_axis_rule"`
	} `json:"official_total_score"`
	DisplayRequirements []string `json:"display_requirements"`
}

// LoadTeamContract reads and validates the frozen team scoring registry.
func LoadTeamContract(path string) (*TeamContract, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scoring: read team registry: %w", err)
	}
	return ParseTeamContract(b)
}

// ParseTeamContract decodes a team registry from bytes.
func ParseTeamContract(b []byte) (*TeamContract, error) {
	var c TeamContract
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("scoring: decode team registry: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate enforces the team registry invariants: eight axes, axis weights
// summing to 1, and per-axis component weights summing to 1.
func (c *TeamContract) Validate() error {
	if c.SchemaVersion != TeamSchemaVersion {
		return fmt.Errorf("scoring: team registry schema %q != %q", c.SchemaVersion, TeamSchemaVersion)
	}
	if len(c.Axes) != 8 {
		return fmt.Errorf("scoring: team registry %d axes, want 8", len(c.Axes))
	}
	sum := 0.0
	for _, v := range c.AxisWeights {
		sum += v
	}
	if math.Abs(sum-1.0) > 1e-6 {
		return fmt.Errorf("scoring: team axis weights sum %f != 1", sum)
	}
	for axis, m := range c.OfficialAxisComponents {
		s := 0.0
		for _, v := range m {
			s += v
		}
		if math.Abs(s-1.0) > 1e-6 {
			return fmt.Errorf("scoring: team axis %s component weights sum %f != 1", axis, s)
		}
	}
	return nil
}

// TeamAxisNames returns the canonical team axis order.
func (c *TeamContract) TeamAxisNames() []string {
	out := make([]string, 0, len(c.Axes))
	for _, a := range c.Axes {
		out = append(out, a.ID)
	}
	return out
}

// TeamAxisDisplayNameZh returns the Chinese display name for a team axis.
func (c *TeamContract) TeamAxisDisplayNameZh(id string) string {
	for _, a := range c.Axes {
		if a.ID == id {
			return a.DisplayNameZh
		}
	}
	return id
}

// LoadContract reads and validates the frozen scoring contract.
func LoadContract(path string) (*Contract, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scoring: read contract: %w", err)
	}
	return ParseContract(b)
}

// ParseContract decodes a contract from bytes (used by tests).
func ParseContract(b []byte) (*Contract, error) {
	var c Contract
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("scoring: decode contract: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate enforces the contract invariants: all eight axes, weights summing
// to 1 per role, and component weights summing to 1 per role/axis.
func (c *Contract) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("scoring: contract schema %q != %q", c.SchemaVersion, SchemaVersion)
	}
	if len(c.Axes) != 8 {
		return fmt.Errorf("scoring: %d axes, want 8", len(c.Axes))
	}
	for _, role := range []string{"1", "2", "3", "4", "5"} {
		w := c.AxisWeightsByRole[role]
		if w == nil {
			return fmt.Errorf("scoring: missing axis weights for role %s", role)
		}
		sum := 0.0
		for _, v := range w {
			sum += v
		}
		if math.Abs(sum-1.0) > 1e-6 {
			return fmt.Errorf("scoring: role %s axis weights sum %f != 1", role, sum)
		}
		comps := c.OfficialAxisComponentsByRole[role]
		if comps == nil {
			return fmt.Errorf("scoring: missing official axis components for role %s", role)
		}
		for axis, m := range comps {
			s := 0.0
			for _, v := range m {
				s += v
			}
			if math.Abs(s-1.0) > 1e-6 {
				return fmt.Errorf("scoring: role %s axis %s component weights sum %f != 1", role, axis, s)
			}
		}
	}
	return nil
}

// AxisNames returns the canonical axis order.
func (c *Contract) AxisNames() []string {
	out := make([]string, 0, len(c.Axes))
	for _, a := range c.Axes {
		out = append(out, a.ID)
	}
	return out
}

// AxisDisplayNameZh returns the Chinese display name for an axis id.
func (c *Contract) AxisDisplayNameZh(id string) string {
	for _, a := range c.Axes {
		if a.ID == id {
			return a.DisplayNameZh
		}
	}
	return id
}

// MidRankPercentile computes the empirical mid-rank percentile (0..100) of v
// within cohort using ties-as-midrank. An empty cohort is NaN (callers must
// gate on MinimumMatches before using it).
func MidRankPercentile(v float64, cohort []float64) float64 {
	if len(cohort) == 0 {
		return math.NaN()
	}
	xs := append([]float64(nil), cohort...)
	sort.Float64s(xs)
	below := 0
	eq := 0
	for _, x := range xs {
		if x < v {
			below++
		} else if x == v {
			eq++
		}
	}
	midrank := float64(below) + float64(eq+1)/2.0
	return 100.0 * (midrank - 0.5) / float64(len(xs))
}

// Direction is the declared metric direction.
type Direction string

const (
	HigherBetter Direction = "higher_better"
	LowerBetter  Direction = "lower_better"
	ContextOnly  Direction = "context_only"
)

// Invert converts a raw percentile so lower-better values score high.
func Invert(pct float64) float64 {
	return 100.0 - pct
}

// EvidenceRef is a typed, versioned lineage reference: the source fact,
// derived episode, or applicable phase that produced a metric observation.
// It is preserved through aggregation so drilldown can navigate to evidence.
type EvidenceRef struct {
	MatchID       string `json:"match_id,omitempty"`
	Kind          string `json:"kind"` // fact|episode|phase|metric
	ID            string `json:"id"`
	RuleVersion   string `json:"rule_version,omitempty"`
	SourceFactSeq int64  `json:"source_fact_seq,omitempty"`
}

// MetricValue is one published metric observation with the numerator/
// denominator/opportunity fields needed for declared-rule aggregation and the
// typed lineage references that produced it.
type MetricValue struct {
	MetricID             string        `json:"metric_id"`
	Value                float64       `json:"value"`
	Numerator            *float64      `json:"numerator,omitempty"`
	Denominator          *float64      `json:"denominator,omitempty"`
	OpportunityCount     int64         `json:"opportunity_count,omitempty"`
	Direction            Direction     `json:"direction"`
	OfficialEligible     bool          `json:"official_eligible"`
	ExperimentalEligible bool          `json:"experimental_eligible"`
	Lineage              []EvidenceRef `json:"lineage,omitempty"`
}

// AggregatedMetric is one metric aggregated over a subject's eligible records
// using its declared rule, retaining the merged typed lineage.
type AggregatedMetric struct {
	MetricID             string        `json:"metric_id"`
	Value                float64       `json:"value"`
	Numerator            float64       `json:"numerator,omitempty"`
	Denominator          float64       `json:"denominator,omitempty"`
	OpportunityCount     int64         `json:"opportunity_count,omitempty"`
	EligibleMatches      int           `json:"eligible_matches"`
	Direction            Direction     `json:"direction"`
	OfficialEligible     bool          `json:"official_eligible"`
	ExperimentalEligible bool          `json:"experimental_eligible"`
	Lineage              []EvidenceRef `json:"lineage,omitempty"`
}

// PlayerMatch is one player in one match with its published metric values.
type PlayerMatch struct {
	MatchID     string                 `json:"match_id"`
	AccountID   string                 `json:"account_id"`
	TeamID      string                 `json:"team_id,omitempty"`
	NominalRole string                 `json:"nominal_role"`
	Metrics     map[string]MetricValue `json:"metrics"`
}

// PlayerTournament is one player's records aggregated across its eligible
// matches within a fixed nominal role (the scoring subject grain).
type PlayerTournament struct {
	AccountID       string                      `json:"account_id"`
	TeamID          string                      `json:"team_id,omitempty"`
	NominalRole     string                      `json:"nominal_role"`
	EligibleMatches int                         `json:"eligible_matches"`
	MatchIDs        []string                    `json:"match_ids"`
	Metrics         map[string]AggregatedMetric `json:"metrics"`
}

// TeamMatch is one team's metrics pooled across its five players in one match.
type TeamMatch struct {
	MatchID     string                      `json:"match_id"`
	TeamID      string                      `json:"team_id"`
	Metrics     map[string]AggregatedMetric `json:"metrics"`
	PlayerCount int                         `json:"player_count"`
}

// TeamTournament is one team's metrics aggregated across its eligible matches.
type TeamTournament struct {
	TeamID          string                      `json:"team_id"`
	EligibleMatches int                         `json:"eligible_matches"`
	MatchIDs        []string                    `json:"match_ids"`
	Metrics         map[string]AggregatedMetric `json:"metrics"`
}

// AxisResult is one axis of a subject's radar.
type AxisResult struct {
	Axis       string                     `json:"axis"`
	DisplayZh  string                     `json:"display_name_zh"`
	Published  bool                       `json:"published"`
	Value      *float64                   `json:"value,omitempty"`
	Components map[string]ComponentResult `json:"components"`
	Suppressed bool                       `json:"suppressed,omitempty"`
	Reason     string                     `json:"reason,omitempty"`
	Coverage   Coverage                   `json:"coverage"`
}

// ComponentResult is one metric component within an axis.
type ComponentResult struct {
	MetricID          string   `json:"metric_id"`
	RawValue          *float64 `json:"raw_value,omitempty"`
	Percentile        *float64 `json:"percentile,omitempty"`
	Weight            float64  `json:"weight"`
	Published         bool     `json:"published"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

// Coverage is the per-axis/per-total coverage summary.
type Coverage struct {
	MetricCount      int   `json:"metric_count"`
	PublishedCount   int   `json:"published_count"`
	MatchCount       int   `json:"match_count"`
	OpportunityCount int64 `json:"opportunity_count"`
}

// TotalResult is the official or experimental total score.
type TotalResult struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Published    bool               `json:"published"`
	Value        *float64           `json:"value,omitempty"`
	AxesIncluded []string           `json:"axes_included"`
	Weights      map[string]float64 `json:"weights"`
	Suppressed   bool               `json:"suppressed,omitempty"`
	Reasons      []string           `json:"reasons,omitempty"`
	Coverage     Coverage           `json:"coverage"`
}

// PlayerScore is the full scoring snapshot for one player (player-tournament
// grain). SubjectCoverage reports the subject's own eligible match count, never
// the global corpus count.
type PlayerScore struct {
	AccountID         string                      `json:"account_id"`
	TeamID            string                      `json:"team_id,omitempty"`
	NominalRole       string                      `json:"nominal_role"`
	ScoringVersion    string                      `json:"scoring_version"`
	MetricPercentiles map[string]PercentileResult `json:"metric_percentiles"`
	// AggregatedMetrics carries the aggregated raw values with typed lineage
	// (fact/episode/phase refs) for navigable drilldown.
	AggregatedMetrics    map[string]AggregatedMetric `json:"aggregated_metrics,omitempty"`
	OfficialAxes         map[string]AxisResult       `json:"official_axes"`
	ExperimentalAxes     map[string]AxisResult       `json:"experimental_axes"`
	OfficialTotal        *TotalResult                `json:"official_total"`
	ExperimentalTotal    *TotalResult                `json:"experimental_total"`
	SubjectCoverage      SubjectCoverage             `json:"subject_coverage"`
	ComparisonPopulation string                      `json:"comparison_population"`
}

// PercentileResult is one metric's normalized value with provenance.
type PercentileResult struct {
	MetricID             string    `json:"metric_id"`
	RawValue             float64   `json:"raw_value"`
	Percentile           float64   `json:"percentile"`
	Direction            Direction `json:"direction"`
	OfficialEligible     bool      `json:"official_eligible"`
	ExperimentalEligible bool      `json:"experimental_eligible"`
	CohortSize           int       `json:"cohort_size"`
}

// SubjectCoverage reports the scoring subject's own coverage.
type SubjectCoverage struct {
	EligibleMatches  int      `json:"eligible_matches"`
	MatchIDs         []string `json:"match_ids"`
	PublishedMetrics int      `json:"published_metrics"`
	CorpusMatches    int      `json:"corpus_matches"`
}

// TeamScore is the team-tournament scoring snapshot.
type TeamScore struct {
	TeamID               string                      `json:"team_id"`
	ScoringVersion       string                      `json:"scoring_version"`
	MetricPercentiles    map[string]PercentileResult `json:"metric_percentiles"`
	OfficialAxes         map[string]AxisResult       `json:"official_axes"`
	OfficialTotal        *TotalResult                `json:"official_total"`
	SubjectCoverage      SubjectCoverage             `json:"subject_coverage"`
	ComparisonPopulation string                      `json:"comparison_population"`
}

// Corpus holds the player-match inputs, the player-tournament aggregation,
// the team aggregation, and the percentile cohorts.
type Corpus struct {
	Contract    *Contract
	TeamC       *TeamContract
	MetricReg   *metrics.Registry
	Matches     map[string][]*PlayerMatch        // match_id -> players
	Players     []*PlayerMatch                   // flat per-match rows
	Tournaments map[string]*PlayerTournament     // account\x00role -> aggregate
	TeamMatches map[string]map[string]*TeamMatch // team_id -> match_id -> team
	Teams       map[string]*TeamTournament       // team_id -> aggregate
	Cohorts     map[string][]float64             // "role\x00metric" -> tournament aggregate values
	TeamCohorts map[string][]float64             // "team\x00metric" -> team aggregate values
}

// NewCorpus builds a corpus from the contract, metric registry, and
// player-match inputs. Aggregation happens here using each metric's declared
// rule from the frozen registry.
func NewCorpus(c *Contract, mreg *metrics.Registry, players []*PlayerMatch) *Corpus {
	return NewCorpusWithTeam(c, nil, mreg, players)
}

// NewCorpusWithTeam builds a corpus with an optional frozen team scoring
// registry. When tc is nil, team scoring fails closed (no team axes publish).
func NewCorpusWithTeam(c *Contract, tc *TeamContract, mreg *metrics.Registry, players []*PlayerMatch) *Corpus {
	cor := &Corpus{
		Contract: c, TeamC: tc, MetricReg: mreg,
		Matches:     map[string][]*PlayerMatch{},
		Players:     players,
		Tournaments: map[string]*PlayerTournament{},
		TeamMatches: map[string]map[string]*TeamMatch{},
		Teams:       map[string]*TeamTournament{},
		Cohorts:     map[string][]float64{},
		TeamCohorts: map[string][]float64{},
	}
	for _, p := range players {
		cor.Matches[p.MatchID] = append(cor.Matches[p.MatchID], p)
	}
	cor.buildTournaments()
	cor.buildTeams()
	cor.buildCohorts()
	return cor
}

// AggregateRuleOf returns the declared aggregation rule for a metric id.
func (c *Corpus) AggregateRuleOf(id string) string {
	if c.MetricReg == nil {
		return ""
	}
	if m := c.MetricReg.Find(id); m != nil {
		return m.AggregationRule
	}
	return ""
}

// aggregateValues combines per-match metric observations using the metric's
// declared rule. Supported rule families: rate (sum numerator / sum
// denominator), sum, max, and percentile-recompute (aggregate raw then
// recompute — handled by the caller on the pooled value).
func (c *Corpus) aggregateValues(mid string, vals []MetricValue) (AggregatedMetric, bool) {
	if len(vals) == 0 {
		return AggregatedMetric{}, false
	}
	rule := c.AggregateRuleOf(mid)
	agg := AggregatedMetric{MetricID: mid, Direction: vals[0].Direction, OfficialEligible: vals[0].OfficialEligible, ExperimentalEligible: vals[0].ExperimentalEligible}
	var num, den float64
	var numOK, denOK bool
	maxV := math.Inf(-1)
	sumV := 0.0
	seenLineage := map[string]bool{}
	for _, v := range vals {
		agg.EligibleMatches++
		agg.OpportunityCount += v.OpportunityCount
		if v.Numerator != nil {
			num += *v.Numerator
			numOK = true
		}
		if v.Denominator != nil {
			den += *v.Denominator
			denOK = true
		}
		if v.Value > maxV {
			maxV = v.Value
		}
		sumV += v.Value
		// Preserve typed lineage through aggregation (deduplicated by
		// match+kind+id so equal entity ids from different matches never
		// collapse).
		for _, ref := range v.Lineage {
			key := ref.MatchID + "\x00" + ref.Kind + "\x00" + ref.ID
			if !seenLineage[key] {
				seenLineage[key] = true
				agg.Lineage = append(agg.Lineage, ref)
			}
		}
	}
	low := strings.ToLower(rule)
	switch {
	case strings.Contains(low, "sum numerator divided by sum denominator"):
		if denOK && den > 0 {
			agg.Value = num / den
			agg.Numerator = num
			agg.Denominator = den
			return agg, true
		}
		return agg, false
	case strings.Contains(low, "never average stored percentiles"):
		// Aggregate raw values first; recompute percentile in caller.
		agg.Numerator = num
		agg.Denominator = den
		if denOK && den > 0 {
			agg.Value = num / den
		} else {
			agg.Value = sumV / float64(len(vals))
		}
		return agg, true
	case strings.Contains(low, "max"):
		agg.Value = maxV
		agg.Numerator = maxV
		return agg, true
	case strings.Contains(low, "sum eligible non-overlapping seconds"):
		agg.Value = sumV
		agg.Numerator = sumV
		agg.Denominator = den
		return agg, true
	default: // sum counts / sum signed raw values
		agg.Value = sumV
		if numOK {
			agg.Numerator = num
		} else {
			agg.Numerator = sumV
		}
		return agg, true
	}
}

// buildTournaments aggregates each player's metrics across its matches within
// its fixed nominal role.
func (c *Corpus) buildTournaments() {
	byKey := map[string][]*PlayerMatch{}
	for _, p := range c.Players {
		key := p.AccountID + "\x00" + p.NominalRole
		byKey[key] = append(byKey[key], p)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rows := byKey[key]
		pt := &PlayerTournament{
			AccountID: rows[0].AccountID, TeamID: rows[0].TeamID, NominalRole: rows[0].NominalRole,
			EligibleMatches: len(rows), Metrics: map[string]AggregatedMetric{},
		}
		for _, r := range rows {
			pt.MatchIDs = append(pt.MatchIDs, r.MatchID)
		}
		byMetric := map[string][]MetricValue{}
		for _, r := range rows {
			for mid, mv := range r.Metrics {
				byMetric[mid] = append(byMetric[mid], mv)
			}
		}
		for mid, vals := range byMetric {
			if agg, ok := c.aggregateValues(mid, vals); ok {
				pt.Metrics[mid] = agg
			}
		}
		sort.Strings(pt.MatchIDs)
		c.Tournaments[key] = pt
	}
}

// buildTeams pools player metrics to team-match and team-tournament level.
func (c *Corpus) buildTeams() {
	// team_id -> match_id -> []player match rows
	rows := map[string]map[string][]*PlayerMatch{}
	for _, p := range c.Players {
		if p.TeamID == "" {
			continue
		}
		if rows[p.TeamID] == nil {
			rows[p.TeamID] = map[string][]*PlayerMatch{}
		}
		rows[p.TeamID][p.MatchID] = append(rows[p.TeamID][p.MatchID], p)
	}
	for tid, byMatch := range rows {
		c.TeamMatches[tid] = map[string]*TeamMatch{}
		tt := &TeamTournament{TeamID: tid, Metrics: map[string]AggregatedMetric{}}
		teamAgg := map[string][]MetricValue{}
		for matchID, players := range byMatch {
			tm := &TeamMatch{MatchID: matchID, TeamID: tid, Metrics: map[string]AggregatedMetric{}, PlayerCount: len(players)}
			byMetric := map[string][]MetricValue{}
			for _, p := range players {
				for mid, mv := range p.Metrics {
					byMetric[mid] = append(byMetric[mid], mv)
				}
			}
			for mid, vals := range byMetric {
				if agg, ok := c.aggregateValues(mid, vals); ok {
					tm.Metrics[mid] = agg
					teamAgg[mid] = append(teamAgg[mid], MetricValue{
						MetricID: mid, Value: agg.Value,
						Numerator: &agg.Numerator, Denominator: &agg.Denominator,
						OpportunityCount: agg.OpportunityCount,
						Direction:        agg.Direction, OfficialEligible: agg.OfficialEligible,
						ExperimentalEligible: agg.ExperimentalEligible,
					})
				}
			}
			c.TeamMatches[tid][matchID] = tm
		}
		tt.EligibleMatches = len(byMatch)
		mids := make([]string, 0, len(byMatch))
		for m := range byMatch {
			mids = append(mids, m)
		}
		sort.Strings(mids)
		tt.MatchIDs = mids
		for mid, vals := range teamAgg {
			if agg, ok := c.aggregateValues(mid, vals); ok {
				tt.Metrics[mid] = agg
			}
		}
		c.Teams[tid] = tt
	}
}

// buildCohorts derives the percentile populations: one value per subject
// (player tournament / team tournament) per metric.
func (c *Corpus) buildCohorts() {
	for _, pt := range c.Tournaments {
		for mid, am := range pt.Metrics {
			key := pt.NominalRole + "\x00" + mid
			c.Cohorts[key] = append(c.Cohorts[key], am.Value)
		}
	}
	for _, tt := range c.Teams {
		for mid, am := range tt.Metrics {
			key := "team\x00" + mid
			c.TeamCohorts[key] = append(c.TeamCohorts[key], am.Value)
		}
	}
}

// MatchCount returns the number of distinct matches in the corpus.
func (c *Corpus) MatchCount() int {
	return len(c.Matches)
}

// Cohort returns the tournament values for a role+metric cohort (may be empty).
func (c *Corpus) Cohort(role, metricID string) []float64 {
	return c.Cohorts[role+"\x00"+metricID]
}

// TeamCohort returns the team-tournament values for a metric cohort.
func (c *Corpus) TeamCohort(metricID string) []float64 {
	return c.TeamCohorts["team\x00"+metricID]
}

// Tournament returns the player-tournament aggregate for account+role.
func (c *Corpus) Tournament(account, role string) *PlayerTournament {
	return c.Tournaments[account+"\x00"+role]
}

// ScorePlayer computes the player-tournament scoring snapshot. The subject's
// own eligible match count is reported in SubjectCoverage; the minimum-match
// gate uses that subject coverage, not the global corpus count.
func (c *Corpus) ScorePlayer(account, role string) *PlayerScore {
	cv := c.Contract
	pt := c.Tournament(account, role)
	if pt == nil {
		return nil
	}
	ps := &PlayerScore{
		AccountID: pt.AccountID, TeamID: pt.TeamID, NominalRole: pt.NominalRole,
		ScoringVersion:    cv.SchemaVersion,
		MetricPercentiles: map[string]PercentileResult{},
		AggregatedMetrics: map[string]AggregatedMetric{},
		OfficialAxes:      map[string]AxisResult{},
		ExperimentalAxes:  map[string]AxisResult{},
		SubjectCoverage: SubjectCoverage{
			EligibleMatches: pt.EligibleMatches, MatchIDs: pt.MatchIDs,
			PublishedMetrics: len(pt.Metrics), CorpusMatches: c.MatchCount(),
		},
		ComparisonPopulation: cv.ComparisonPopulation,
	}
	// Carry the aggregated raw values + typed lineage for drilldown.
	for mid, am := range pt.Metrics {
		ps.AggregatedMetrics[mid] = am
	}

	// Step 1: normalize every published tournament metric to a same-role
	// percentile computed from the aggregated tournament cohort.
	for mid, am := range pt.Metrics {
		cohort := c.Cohort(pt.NominalRole, mid)
		if len(cohort) < cv.MinimumMatches {
			continue // not enough cohort; metric cannot normalize
		}
		pct := MidRankPercentile(am.Value, cohort)
		if am.Direction == LowerBetter {
			pct = Invert(pct)
		}
		ps.MetricPercentiles[mid] = PercentileResult{
			MetricID: mid, RawValue: am.Value, Percentile: pct, Direction: am.Direction,
			OfficialEligible: am.OfficialEligible, ExperimentalEligible: am.ExperimentalEligible,
			CohortSize: len(cohort),
		}
	}

	// Step 2: official axes from official_axis_components_by_role.
	ps.OfficialAxes = c.computeOfficialAxes(pt, ps.MetricPercentiles, pt.NominalRole)
	ps.OfficialTotal = c.computeOfficialTotal(ps, pt)

	// Step 3: experimental V3 dashed layer.
	ps.ExperimentalAxes = c.computeExperimentalAxes(ps)
	ps.ExperimentalTotal = c.computeExperimentalTotal(ps)

	return ps
}

// ScoreTeam computes the team-tournament scoring snapshot exactly from the
// frozen team scoring registry. When the registry is absent, team scoring
// fails closed (no axes publish).
func (c *Corpus) ScoreTeam(teamID string) *TeamScore {
	tc := c.TeamC
	if tc == nil {
		return &TeamScore{
			TeamID: teamID, ScoringVersion: "",
			MetricPercentiles: map[string]PercentileResult{},
			OfficialAxes:      map[string]AxisResult{},
			OfficialTotal:     &TotalResult{ID: "official_team_total_v1", Name: "队伍官方总分", Suppressed: true, Reasons: []string{"team_scoring_registry_unavailable"}},
			SubjectCoverage:   SubjectCoverage{CorpusMatches: c.MatchCount()},
		}
	}
	tt := c.Teams[teamID]
	if tt == nil {
		return nil
	}
	ts := &TeamScore{
		TeamID: teamID, ScoringVersion: tc.SchemaVersion,
		MetricPercentiles: map[string]PercentileResult{},
		OfficialAxes:      map[string]AxisResult{},
		SubjectCoverage: SubjectCoverage{
			EligibleMatches: tt.EligibleMatches, MatchIDs: tt.MatchIDs,
			PublishedMetrics: len(tt.Metrics), CorpusMatches: c.MatchCount(),
		},
		ComparisonPopulation: tc.ComparisonPopulation,
	}
	for mid, am := range tt.Metrics {
		cohort := c.TeamCohort(mid)
		if len(cohort) < tc.MinimumMatches {
			continue
		}
		pct := MidRankPercentile(am.Value, cohort)
		if am.Direction == LowerBetter {
			pct = Invert(pct)
		}
		ts.MetricPercentiles[mid] = PercentileResult{
			MetricID: mid, RawValue: am.Value, Percentile: pct, Direction: am.Direction,
			OfficialEligible: am.OfficialEligible, ExperimentalEligible: am.ExperimentalEligible,
			CohortSize: len(cohort),
		}
	}
	ts.OfficialAxes = c.computeTeamAxes(tt, ts.MetricPercentiles)
	ts.OfficialTotal = c.computeTeamTotal(ts, tt)
	return ts
}

// computeOfficialAxes builds axes from the tournament metric percentiles.
func (c *Corpus) computeOfficialAxes(pt *PlayerTournament, pcts map[string]PercentileResult, role string) map[string]AxisResult {
	cv := c.Contract
	out := map[string]AxisResult{}
	comps := cv.OfficialAxisComponentsByRole[role]
	for _, axis := range cv.AxisNames() {
		ar := AxisResult{Axis: axis, DisplayZh: cv.AxisDisplayNameZh(axis), Components: map[string]ComponentResult{}, Suppressed: true}
		cm := comps[axis]
		if len(cm) == 0 {
			ar.Reason = "no_components_defined_for_role"
			out[axis] = ar
			continue
		}
		weightedSum := 0.0
		weightTotal := 0.0
		ar.Coverage.MetricCount = len(cm)
		mandatoryMissing := false
		for mid, w := range cm {
			cr := ComponentResult{MetricID: mid, Weight: w}
			pr, ok := pt.Metrics[mid]
			pct, ok2 := pcts[mid]
			if !ok || !pr.OfficialEligible || !ok2 {
				cr.UnavailableReason = c.missingReason(pt, mid, pr, ok, ok2, "official")
				mandatoryMissing = true
			} else {
				v := pr.Value
				pv := pct.Percentile
				cr.RawValue = &v
				cr.Percentile = &pv
				cr.Published = true
				weightedSum += w * pv
				weightTotal += w
				ar.Coverage.PublishedCount++
			}
			ar.Components[mid] = cr
		}
		if mandatoryMissing || weightTotal == 0 {
			ar.Reason = c.missingMetricPolicyReason(cm, ar)
			out[axis] = ar
			continue
		}
		val := weightedSum / weightTotal
		ar.Published = true
		ar.Suppressed = false
		ar.Value = &val
		out[axis] = ar
	}
	return out
}

// computeTeamAxes builds team axes exactly from the frozen team scoring
// registry's per-axis component weights (never derived from player roles).
func (c *Corpus) computeTeamAxes(tt *TeamTournament, pcts map[string]PercentileResult) map[string]AxisResult {
	tc := c.TeamC
	out := map[string]AxisResult{}
	if tc == nil {
		return out
	}
	for _, axis := range tc.TeamAxisNames() {
		ar := AxisResult{Axis: axis, DisplayZh: tc.TeamAxisDisplayNameZh(axis), Components: map[string]ComponentResult{}, Suppressed: true}
		cm := tc.OfficialAxisComponents[axis]
		if len(cm) == 0 {
			ar.Reason = "no_team_components_defined"
			out[axis] = ar
			continue
		}
		weightedSum := 0.0
		weightTotal := 0.0
		ar.Coverage.MetricCount = len(cm)
		mandatoryMissing := false
		for mid, w := range cm {
			cr := ComponentResult{MetricID: mid, Weight: w}
			am, ok := tt.Metrics[mid]
			pct, ok2 := pcts[mid]
			if !ok || !am.OfficialEligible || !ok2 {
				cr.UnavailableReason = c.missingReason(tt, mid, am, ok, ok2, "official")
				mandatoryMissing = true
			} else {
				v := am.Value
				pv := pct.Percentile
				cr.RawValue = &v
				cr.Percentile = &pv
				cr.Published = true
				weightedSum += w * pv
				weightTotal += w
				ar.Coverage.PublishedCount++
			}
			ar.Components[mid] = cr
		}
		if mandatoryMissing || weightTotal == 0 {
			ar.Reason = c.missingMetricPolicyReason(cm, ar)
			out[axis] = ar
			continue
		}
		val := weightedSum / weightTotal
		ar.Published = true
		ar.Suppressed = false
		ar.Value = &val
		out[axis] = ar
	}
	return out
}

// percentileOf returns the normalized percentile for a metric (if it exists).
func (c *Corpus) percentileOf(p *PlayerTournament, mid string) (float64, bool) {
	pr, ok := p.Metrics[mid]
	if !ok {
		return 0, false
	}
	cohort := c.Cohort(p.NominalRole, mid)
	if len(cohort) < c.Contract.MinimumMatches {
		return 0, false
	}
	pct := MidRankPercentile(pr.Value, cohort)
	if pr.Direction == LowerBetter {
		pct = Invert(pct)
	}
	return pct, true
}

// missingReason builds a precise reason for a missing component.
func (c *Corpus) missingReason(subject interface {
}, mid string, am AggregatedMetric, ok, ok2 bool, layer string) string {
	if !ok {
		return "metric_not_published:" + mid
	}
	if !am.OfficialEligible && layer == "official" {
		return "metric_not_official_eligible:" + mid
	}
	if !ok2 {
		return "insufficient_cohort:" + mid
	}
	return "unknown"
}

// missingMetricPolicyReason summarizes which components failed.
func (c *Corpus) missingMetricPolicyReason(cm map[string]float64, ar AxisResult) string {
	var fails []string
	for mid := range cm {
		cr := ar.Components[mid]
		if !cr.Published {
			fails = append(fails, mid+"("+cr.UnavailableReason+")")
		}
	}
	sort.Strings(fails)
	return "axis_suppressed_required_metric_unavailable: " + joinComma(fails)
}

func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// computeOfficialTotal applies the official total gate. The subject's own
// eligible match count drives the minimum-match gate.
func (c *Corpus) computeOfficialTotal(ps *PlayerScore, pt *PlayerTournament) *TotalResult {
	cv := c.Contract
	role := ps.NominalRole
	tr := &TotalResult{
		ID: cv.OfficialTotalScore.ID, Name: "官方总分", Weights: map[string]float64{},
		Suppressed: true,
	}
	reasons := []string{}
	if pt.EligibleMatches < cv.MinimumMatches {
		reasons = append(reasons, fmt.Sprintf("subject_matches=%d_less_than_%d", pt.EligibleMatches, cv.MinimumMatches))
	}
	mandatory := cv.MandatoryAxesByRole[role]
	mandatoryAll := true
	for _, ax := range mandatory {
		a := ps.OfficialAxes[ax]
		if !a.Published {
			mandatoryAll = false
			reasons = append(reasons, "mandatory_axis_unavailable:"+ax)
		}
	}
	published := []string{}
	totalW := 0.0
	sum := 0.0
	for ax, a := range ps.OfficialAxes {
		if !a.Published {
			continue
		}
		w := cv.AxisWeightsByRole[role][ax]
		published = append(published, ax)
		sum += w * *a.Value
		totalW += w
		tr.Weights[ax] = w
	}
	if len(published) < cv.MinimumPublishableAxes {
		reasons = append(reasons, fmt.Sprintf("publishable_axes=%d_less_than_%d", len(published), cv.MinimumPublishableAxes))
	}
	if !mandatoryAll || len(reasons) > 0 || totalW == 0 {
		tr.Suppressed = true
		tr.Reasons = reasons
		if len(reasons) == 0 {
			tr.Reasons = []string{"official_total_suppressed_no_publishable_axes"}
		}
		return tr
	}
	val := sum / totalW
	tr.Published = true
	tr.Suppressed = false
	tr.Value = &val
	tr.AxesIncluded = published
	tr.Reasons = []string{}
	return tr
}

// computeTeamTotal applies the official total gate exactly from the frozen
// team scoring registry's axis weights.
func (c *Corpus) computeTeamTotal(ts *TeamScore, tt *TeamTournament) *TotalResult {
	tc := c.TeamC
	tr := &TotalResult{
		ID: "official_team_total_v1", Name: "队伍官方总分", Weights: map[string]float64{},
		Suppressed: true,
	}
	if tc == nil {
		tr.Reasons = []string{"team_scoring_registry_unavailable"}
		return tr
	}
	reasons := []string{}
	if tt.EligibleMatches < tc.MinimumMatches {
		reasons = append(reasons, fmt.Sprintf("team_matches=%d_less_than_%d", tt.EligibleMatches, tc.MinimumMatches))
	}
	published := []string{}
	totalW := 0.0
	sum := 0.0
	for _, ax := range tc.TeamAxisNames() {
		a := ts.OfficialAxes[ax]
		if !a.Published {
			reasons = append(reasons, "mandatory_axis_unavailable:"+ax)
			continue
		}
		w := tc.AxisWeights[ax]
		published = append(published, ax)
		sum += w * *a.Value
		totalW += w
		tr.Weights[ax] = w
	}
	if len(published) < tc.MinimumPublishableAxes {
		reasons = append(reasons, fmt.Sprintf("publishable_axes=%d_less_than_%d", len(published), tc.MinimumPublishableAxes))
	}
	if len(reasons) > 0 || totalW == 0 {
		tr.Reasons = reasons
		if len(reasons) == 0 {
			tr.Reasons = []string{"team_total_suppressed_no_publishable_axes"}
		}
		return tr
	}
	val := sum / totalW
	tr.Published = true
	tr.Suppressed = false
	tr.Value = &val
	tr.AxesIncluded = published
	return tr
}

// computeExperimentalAxes builds the dashed V3 axes.
func (c *Corpus) computeExperimentalAxes(ps *PlayerScore) map[string]AxisResult {
	cv := c.Contract
	out := map[string]AxisResult{}
	for _, axis := range cv.AxisNames() {
		ar := AxisResult{Axis: axis, DisplayZh: cv.AxisDisplayNameZh(axis), Components: map[string]ComponentResult{}, Suppressed: true}
		official := ps.OfficialAxes[axis]
		if !official.Published {
			ar.Reason = "official_axis_unavailable"
			out[axis] = ar
			continue
		}
		v3Comps := cv.ExperimentalComponentsByAxis[axis]
		var v3Pcts []float64
		for _, mid := range v3Comps {
			if pr, ok := ps.MetricPercentiles[mid]; ok && pr.ExperimentalEligible {
				v3Pcts = append(v3Pcts, pr.Percentile)
				cr := ComponentResult{MetricID: mid, Published: true, Percentile: &pr.Percentile, Weight: 0.25}
				ar.Components[mid] = cr
			} else {
				ar.Components[mid] = ComponentResult{MetricID: mid, UnavailableReason: "v3_component_not_published"}
			}
		}
		if len(v3Pcts) == 0 {
			ar.Reason = "no_calibrated_v3_component"
			out[axis] = ar
			continue
		}
		v3Mean := 0.0
		for _, v := range v3Pcts {
			v3Mean += v
		}
		v3Mean /= float64(len(v3Pcts))
		val := cv.ExperimentalAxisFormula.OfficialBaseWeight*(*official.Value) + cv.ExperimentalAxisFormula.V3ComponentWeight*v3Mean
		ar.Published = true
		ar.Suppressed = false
		ar.Value = &val
		ar.Reason = "experimental_dashed_layer"
		out[axis] = ar
	}
	return out
}

// computeExperimentalTotal applies the experimental total gates.
func (c *Corpus) computeExperimentalTotal(ps *PlayerScore) *TotalResult {
	cv := c.Contract
	role := ps.NominalRole
	tr := &TotalResult{
		ID: cv.ExperimentalTotalScore.ID, Name: "实验总分（V3 虚线层）", Weights: map[string]float64{},
		Suppressed: true,
	}
	reasons := []string{}
	if ps.OfficialTotal == nil || !ps.OfficialTotal.Published {
		reasons = append(reasons, "official_total_not_published_prerequisite")
	}
	eligible := []string{}
	origW := 0.0
	weighted := 0.0
	totalW := 0.0
	for ax, a := range ps.ExperimentalAxes {
		if !a.Published {
			continue
		}
		w := cv.AxisWeightsByRole[role][ax]
		eligible = append(eligible, ax)
		origW += w
		weighted += w * *a.Value
		totalW += w
		tr.Weights[ax] = w
	}
	if len(eligible) < cv.ExperimentalTotalScore.MinimumExperimentalAxes {
		reasons = append(reasons, fmt.Sprintf("experimental_axes=%d_less_than_%d", len(eligible), cv.ExperimentalTotalScore.MinimumExperimentalAxes))
	}
	if origW < cv.ExperimentalTotalScore.MinimumOriginalAxisWeightCoverage {
		reasons = append(reasons, fmt.Sprintf("original_weight_coverage=%.3f_less_than_%.2f", origW, cv.ExperimentalTotalScore.MinimumOriginalAxisWeightCoverage))
	}
	if len(reasons) > 0 || totalW == 0 {
		tr.Reasons = reasons
		if len(reasons) == 0 {
			tr.Reasons = []string{"experimental_total_suppressed_no_eligible_axes"}
		}
		return tr
	}
	val := weighted / totalW
	tr.Published = true
	tr.Suppressed = false
	tr.Value = &val
	tr.AxesIncluded = eligible
	return tr
}
