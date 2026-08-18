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
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
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

// CanonicalTeamAxes is the frozen canonical eight-axis set. It is fixed so an
// axis rename/removal is a versioned registry change, never a silent drift.
var CanonicalTeamAxes = []string{"laning", "resources", "tempo", "map", "vision", "fight", "objectives", "endgame"}

// Validate enforces the team registry invariants: the canonical eight-axis
// set, non-negative weights, per-axis component-weight sums, mandatory/
// optional membership and disjointness covering the full set, minimum gates,
// and a declared total id. A malformed registry fails closed.
func (c *TeamContract) Validate() error {
	if c.SchemaVersion != TeamSchemaVersion {
		return fmt.Errorf("scoring: team registry schema %q != %q", c.SchemaVersion, TeamSchemaVersion)
	}
	if len(c.Axes) != len(CanonicalTeamAxes) {
		return fmt.Errorf("scoring: team registry %d axes, want %d", len(c.Axes), len(CanonicalTeamAxes))
	}
	axisSet := map[string]bool{}
	for _, a := range c.Axes {
		if a.ID == "" {
			return fmt.Errorf("scoring: team axis with empty id")
		}
		if axisSet[a.ID] {
			return fmt.Errorf("scoring: duplicate team axis %q", a.ID)
		}
		axisSet[a.ID] = true
	}
	for _, want := range CanonicalTeamAxes {
		if !axisSet[want] {
			return fmt.Errorf("scoring: team registry missing canonical axis %q", want)
		}
	}
	// Axis weights: exactly one non-negative weight per canonical axis.
	if len(c.AxisWeights) != len(CanonicalTeamAxes) {
		return fmt.Errorf("scoring: team registry %d axis weights, want %d", len(c.AxisWeights), len(CanonicalTeamAxes))
	}
	for _, want := range CanonicalTeamAxes {
		w, ok := c.AxisWeights[want]
		if !ok {
			return fmt.Errorf("scoring: team registry missing axis weight %q", want)
		}
		if w < 0 {
			return fmt.Errorf("scoring: team axis %q weight %f is negative", want, w)
		}
	}
	// Per-axis component weights sum to 1 (each axis must define components).
	for _, want := range CanonicalTeamAxes {
		cm, ok := c.OfficialAxisComponents[want]
		if !ok || len(cm) == 0 {
			return fmt.Errorf("scoring: team axis %q has no components", want)
		}
		s := 0.0
		for mid, v := range cm {
			if v < 0 {
				return fmt.Errorf("scoring: team axis %q component %s weight %f is negative", want, mid, v)
			}
			s += v
		}
		if math.Abs(s-1.0) > 1e-6 {
			return fmt.Errorf("scoring: team axis %s component weights sum %f != 1", want, s)
		}
	}
	// Mandatory/optional membership: disjoint, cover the full canonical set.
	if len(c.MandatoryAxes) == 0 || len(c.OptionalAxes) == 0 {
		return fmt.Errorf("scoring: team registry missing mandatory/optional axis lists")
	}
	mandSet, optSet := map[string]bool{}, map[string]bool{}
	for _, a := range c.MandatoryAxes {
		if !axisSet[a] {
			return fmt.Errorf("scoring: team mandatory axis %q not in axes", a)
		}
		if mandSet[a] {
			return fmt.Errorf("scoring: duplicate mandatory axis %q", a)
		}
		mandSet[a] = true
	}
	for _, a := range c.OptionalAxes {
		if !axisSet[a] {
			return fmt.Errorf("scoring: team optional axis %q not in axes", a)
		}
		if optSet[a] {
			return fmt.Errorf("scoring: duplicate optional axis %q", a)
		}
		if mandSet[a] {
			return fmt.Errorf("scoring: team axis %q is both mandatory and optional", a)
		}
		optSet[a] = true
	}
	for _, want := range CanonicalTeamAxes {
		if !mandSet[want] && !optSet[want] {
			return fmt.Errorf("scoring: team axis %q neither mandatory nor optional", want)
		}
	}
	// Minimum gates: at least three eligible matches, at least six axes.
	if c.MinimumMatches < 3 {
		return fmt.Errorf("scoring: team minimum_matches %d < 3", c.MinimumMatches)
	}
	if c.MinimumPublishableAxes < 6 {
		return fmt.Errorf("scoring: team minimum_publishable_axes %d < 6", c.MinimumPublishableAxes)
	}
	if c.OfficialTotalScore.ID == "" {
		return fmt.Errorf("scoring: team registry missing official_total_score id")
	}
	if c.OfficialTotalScore.Formula == "" || c.OfficialTotalScore.Gate == "" {
		return fmt.Errorf("scoring: team registry missing official_total_score gate/formula")
	}
	return nil
}

// ValidateWithRegistry additionally proves every metric referenced by the team
// official axis components exists in the frozen metric registry and is
// official_score_eligible, and that no referenced metric is V3 (the official
// team layer never contains V3 inputs). Unknown, duplicate, missing, or
// cross-ineligible entries fail closed.
func (c *TeamContract) ValidateWithRegistry(mreg *metrics.Registry) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if mreg == nil {
		return fmt.Errorf("scoring: team registry validation requires the metric registry")
	}
	for axis, cm := range c.OfficialAxisComponents {
		for mid := range cm {
			m := mreg.Find(mid)
			if m == nil {
				return fmt.Errorf("scoring: team axis %q references unknown metric %q", axis, mid)
			}
			if !m.OfficialScoreEligible {
				return fmt.Errorf("scoring: team axis %q metric %q is not official_score_eligible", axis, mid)
			}
			if m.CapabilityLevel == metrics.CapabilityV3 {
				return fmt.Errorf("scoring: team axis %q metric %q is V3 and may not enter the official team layer", axis, mid)
			}
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
	MatchID         string `json:"match_id,omitempty"`
	Kind            string `json:"kind"` // fact|episode|phase|metric
	ID              string `json:"id"`
	RuleVersion     string `json:"rule_version,omitempty"`
	ContractVersion string `json:"contract_version,omitempty"`
	SourceFactSeq   int64  `json:"source_fact_seq,omitempty"`
}

// UnavailableMetric preserves the validated registry identity and the exact
// metrics-layer abstention reason when an observation cannot be published.
// It prevents score decomposition from manufacturing an anonymous zero-value
// aggregate for a known-but-unavailable component.
type UnavailableMetric struct {
	MetricID          string        `json:"metric_id"`
	MetricVersion     string        `json:"metric_version"`
	UnavailableReason string        `json:"unavailable_reason"`
	Lineage           []EvidenceRef `json:"lineage,omitempty"`
}

// MetricValue is one published metric observation with the numerator/
// denominator/opportunity fields needed for declared-rule aggregation and the
// typed lineage references that produced it.
type MetricValue struct {
	MetricID             string        `json:"metric_id"`
	MetricVersion        string        `json:"metric_version"`
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
	MetricVersion        string        `json:"metric_version"`
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
	MatchID            string                       `json:"match_id"`
	AccountID          string                       `json:"account_id"`
	TeamID             string                       `json:"team_id,omitempty"`
	SourceNominalRole  string                       `json:"source_nominal_role,omitempty"`
	NominalRole        string                       `json:"nominal_role"`
	RoleRecordVersion  string                       `json:"role_record_version,omitempty"`
	OverrideApplied    bool                         `json:"override_applied,omitempty"`
	OverrideAuthor     string                       `json:"override_author,omitempty"`
	OverrideVersion    string                       `json:"override_version,omitempty"`
	Metrics            map[string]MetricValue       `json:"metrics"`
	UnavailableMetrics map[string]UnavailableMetric `json:"unavailable_metrics,omitempty"`
}

type RoleProvenance struct {
	MatchID           string `json:"match_id"`
	SourceNominalRole string `json:"source_nominal_role"`
	NominalRole       string `json:"nominal_role"`
	RoleRecordVersion string `json:"role_record_version"`
	OverrideApplied   bool   `json:"override_applied"`
	OverrideAuthor    string `json:"override_author,omitempty"`
	OverrideVersion   string `json:"override_version,omitempty"`
}

// PlayerTournament is one player's records aggregated across its eligible
// matches within a fixed nominal role (the scoring subject grain).
type PlayerTournament struct {
	AccountID          string                       `json:"account_id"`
	TeamID             string                       `json:"team_id,omitempty"`
	NominalRole        string                       `json:"nominal_role"`
	EligibleMatches    int                          `json:"eligible_matches"`
	MatchIDs           []string                     `json:"match_ids"`
	Metrics            map[string]AggregatedMetric  `json:"metrics"`
	UnavailableMetrics map[string]UnavailableMetric `json:"unavailable_metrics,omitempty"`
	RoleProvenance     map[string]RoleProvenance    `json:"role_provenance"`
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
	TeamID             string                       `json:"team_id"`
	EligibleMatches    int                          `json:"eligible_matches"`
	MatchIDs           []string                     `json:"match_ids"`
	Metrics            map[string]AggregatedMetric  `json:"metrics"`
	UnavailableMetrics map[string]UnavailableMetric `json:"unavailable_metrics,omitempty"`
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
	MetricVersion     string   `json:"metric_version,omitempty"`
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
	AggregatedMetrics    map[string]AggregatedMetric  `json:"aggregated_metrics,omitempty"`
	UnavailableMetrics   map[string]UnavailableMetric `json:"unavailable_metrics,omitempty"`
	OfficialAxes         map[string]AxisResult        `json:"official_axes"`
	ExperimentalAxes     map[string]AxisResult        `json:"experimental_axes"`
	OfficialTotal        *TotalResult                 `json:"official_total"`
	ExperimentalTotal    *TotalResult                 `json:"experimental_total"`
	SubjectCoverage      SubjectCoverage              `json:"subject_coverage"`
	ComparisonPopulation string                       `json:"comparison_population"`
	RoleProvenance       map[string]RoleProvenance    `json:"role_provenance"`
}

// PercentileResult is one metric's normalized value with provenance.
type PercentileResult struct {
	MetricID             string    `json:"metric_id"`
	MetricVersion        string    `json:"metric_version"`
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

// TeamScore is the team-tournament scoring snapshot. It carries both the
// official layer (solid, V1/V2 official-eligible team-pooled inputs) and the
// separately named experimental layer (dashed, V3/team-model inputs). The
// experimental layer follows the frozen team registry: if the registry has no
// machine-readable experimental recipe (or the adapter supplies no eligible V3
// observations), the complete dashed interface is exposed as suppressed with a
// precise contract/gate reason — never a fabricated value or a borrowed
// formula.
type TeamScore struct {
	TeamID            string                      `json:"team_id"`
	ScoringVersion    string                      `json:"scoring_version"`
	MetricPercentiles map[string]PercentileResult `json:"metric_percentiles"`
	// AggregatedMetrics carries the team-tournament aggregated raw values with
	// typed lineage (fact/episode/phase refs + the aggregation entity) for
	// navigable drilldown, mirroring the player score.
	AggregatedMetrics    map[string]AggregatedMetric  `json:"aggregated_metrics,omitempty"`
	UnavailableMetrics   map[string]UnavailableMetric `json:"unavailable_metrics,omitempty"`
	OfficialAxes         map[string]AxisResult        `json:"official_axes"`
	OfficialTotal        *TotalResult                 `json:"official_total"`
	ExperimentalAxes     map[string]AxisResult        `json:"experimental_axes"`
	ExperimentalTotal    *TotalResult                 `json:"experimental_total"`
	SubjectCoverage      SubjectCoverage              `json:"subject_coverage"`
	ComparisonPopulation string                       `json:"comparison_population"`
}

// TeamExperimentalRecipeUnavailableReason is the precise reason exposed when
// the frozen team registry carries no machine-readable experimental recipe.
const TeamExperimentalRecipeUnavailableReason = "team_experimental_recipe_unavailable_in_frozen_registry"

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
	// Programmatic callers/tests may construct current observations directly;
	// normalize an omitted version from the loaded registry before aggregation.
	// Persisted artifacts never take this path unchecked: BuildCorpusFromStore
	// rejects missing or stale versions first.
	if mreg != nil {
		for _, p := range players {
			for mid, mv := range p.Metrics {
				if mv.MetricVersion == "" {
					if def := mreg.Find(mid); def != nil {
						mv.MetricVersion = def.MetricVersion
						p.Metrics[mid] = mv
					}
				}
			}
		}
	}
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
	metricVersion := vals[0].MetricVersion
	if metricVersion == "" {
		return AggregatedMetric{}, false
	}
	for _, v := range vals[1:] {
		if v.MetricID != mid || v.MetricVersion != metricVersion {
			return AggregatedMetric{}, false
		}
	}
	agg := AggregatedMetric{MetricID: mid, MetricVersion: metricVersion, Direction: vals[0].Direction, OfficialEligible: vals[0].OfficialEligible, ExperimentalEligible: vals[0].ExperimentalEligible}
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
		sort.Slice(rows, func(i, j int) bool { return rows[i].MatchID < rows[j].MatchID })
		pt := &PlayerTournament{
			AccountID: rows[0].AccountID, TeamID: rows[0].TeamID, NominalRole: rows[0].NominalRole,
			EligibleMatches: len(rows), Metrics: map[string]AggregatedMetric{}, UnavailableMetrics: map[string]UnavailableMetric{}, RoleProvenance: map[string]RoleProvenance{},
		}
		for _, r := range rows {
			pt.MatchIDs = append(pt.MatchIDs, r.MatchID)
			pt.RoleProvenance[r.MatchID] = RoleProvenance{
				MatchID: r.MatchID, SourceNominalRole: r.SourceNominalRole, NominalRole: r.NominalRole,
				RoleRecordVersion: r.RoleRecordVersion, OverrideApplied: r.OverrideApplied,
				OverrideAuthor: r.OverrideAuthor, OverrideVersion: r.OverrideVersion,
			}
			for mid, unavailable := range r.UnavailableMetrics {
				if _, published := pt.Metrics[mid]; !published {
					pt.UnavailableMetrics[mid] = unavailable
				}
			}
		}
		byMetric := map[string][]MetricValue{}
		for _, r := range rows {
			for mid, mv := range r.Metrics {
				byMetric[mid] = append(byMetric[mid], mv)
			}
		}
		for mid, vals := range byMetric {
			if agg, ok := c.aggregateValues(mid, vals); ok {
				pt.Metrics[mid] = withAggregationRef(agg, "player_tournament", pt.AccountID, pt.NominalRole, c.Contract.SchemaVersion)
				delete(pt.UnavailableMetrics, mid)
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
	teamIDs := make([]string, 0, len(rows))
	for tid := range rows {
		teamIDs = append(teamIDs, tid)
	}
	sort.Strings(teamIDs)
	for _, tid := range teamIDs {
		byMatch := rows[tid]
		c.TeamMatches[tid] = map[string]*TeamMatch{}
		tt := &TeamTournament{TeamID: tid, Metrics: map[string]AggregatedMetric{}, UnavailableMetrics: map[string]UnavailableMetric{}}
		teamAgg := map[string][]MetricValue{}
		matchIDs := make([]string, 0, len(byMatch))
		for matchID := range byMatch {
			matchIDs = append(matchIDs, matchID)
		}
		sort.Strings(matchIDs)
		for _, matchID := range matchIDs {
			players := byMatch[matchID]
			sort.Slice(players, func(i, j int) bool { return players[i].AccountID < players[j].AccountID })
			tm := &TeamMatch{MatchID: matchID, TeamID: tid, Metrics: map[string]AggregatedMetric{}, PlayerCount: len(players)}
			byMetric := map[string][]MetricValue{}
			for _, p := range players {
				for mid, mv := range p.Metrics {
					byMetric[mid] = append(byMetric[mid], mv)
				}
				for mid, unavailable := range p.UnavailableMetrics {
					if _, exists := tt.UnavailableMetrics[mid]; !exists {
						tt.UnavailableMetrics[mid] = unavailable
					}
				}
			}
			for mid, vals := range byMetric {
				if agg, ok := c.aggregateValues(mid, vals); ok {
					agg = withAggregationRef(agg, "team_match", tid, "", c.teamRuleVersion())
					tm.Metrics[mid] = agg
					teamAgg[mid] = append(teamAgg[mid], MetricValue{
						MetricID: mid, MetricVersion: agg.MetricVersion, Value: agg.Value,
						Numerator: &agg.Numerator, Denominator: &agg.Denominator,
						OpportunityCount: agg.OpportunityCount,
						Direction:        agg.Direction, OfficialEligible: agg.OfficialEligible,
						ExperimentalEligible: agg.ExperimentalEligible,
						Lineage:              append([]EvidenceRef(nil), agg.Lineage...),
					})
				}
			}
			c.TeamMatches[tid][matchID] = tm
		}
		tt.EligibleMatches = len(byMatch)
		tt.MatchIDs = append([]string(nil), matchIDs...)
		for mid, vals := range teamAgg {
			if agg, ok := c.aggregateValues(mid, vals); ok {
				tt.Metrics[mid] = withAggregationRef(agg, "team_tournament", tid, "", c.teamRuleVersion())
				delete(tt.UnavailableMetrics, mid)
			}
		}
		c.Teams[tid] = tt
	}
}

// teamRuleVersion returns the team registry schema version, or "" when the
// registry is absent (withAggregationRef falls back to the scoring version).
func (c *Corpus) teamRuleVersion() string {
	if c.TeamC == nil {
		return ""
	}
	return c.TeamC.SchemaVersion
}

// withAggregationRef appends the canonical aggregation entity ref to an
// aggregated metric's lineage. The id is scope/subject/role/metric/algorithm-
// metric-version and score-rule-version qualified so each aggregation entity
// is stable and match/scope distinct; child refs are retained underneath. The
// radar/team contract version remains separate provenance on the reference.
func withAggregationRef(agg AggregatedMetric, scope, subject, role, contractVersion string) AggregatedMetric {
	agg.Lineage = append(agg.Lineage, EvidenceRef{
		MatchID:         "", // aggregation is corpus/scope-qualified, not match-scoped
		Kind:            "aggregation",
		ID:              fmt.Sprintf("aggregation:%s:%s:%s:%s:%s:%s", scope, subject, role, agg.MetricID, agg.MetricVersion, version.ScoreRuleVersion),
		RuleVersion:     version.ScoreRuleVersion,
		ContractVersion: contractVersion,
	})
	return agg
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
		ScoringVersion:     cv.SchemaVersion,
		MetricPercentiles:  map[string]PercentileResult{},
		AggregatedMetrics:  map[string]AggregatedMetric{},
		UnavailableMetrics: map[string]UnavailableMetric{},
		OfficialAxes:       map[string]AxisResult{},
		ExperimentalAxes:   map[string]AxisResult{},
		SubjectCoverage: SubjectCoverage{
			EligibleMatches: pt.EligibleMatches, MatchIDs: pt.MatchIDs,
			PublishedMetrics: len(pt.Metrics), CorpusMatches: c.MatchCount(),
		},
		ComparisonPopulation: cv.ComparisonPopulation,
		RoleProvenance:       pt.RoleProvenance,
	}
	// Carry the aggregated raw values + typed lineage for drilldown.
	for mid, am := range pt.Metrics {
		ps.AggregatedMetrics[mid] = am
	}
	for mid, unavailable := range pt.UnavailableMetrics {
		ps.UnavailableMetrics[mid] = unavailable
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
			MetricID: mid, MetricVersion: am.MetricVersion, RawValue: am.Value, Percentile: pct, Direction: am.Direction,
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
// frozen team scoring registry. It always returns the stable TeamScore shape
// with both official and experimental layers: when the registry is absent team
// scoring fails closed with explicit reasons, and when the team is absent the
// layers are exposed suppressed (never a stale unrelated shape).
func (c *Corpus) ScoreTeam(teamID string) *TeamScore {
	tc := c.TeamC
	if tc == nil {
		return &TeamScore{
			TeamID: teamID, ScoringVersion: "",
			MetricPercentiles: map[string]PercentileResult{},
			OfficialAxes:      map[string]AxisResult{},
			OfficialTotal:     &TotalResult{ID: "official_team_total_v1", Name: "队伍官方总分", Suppressed: true, Reasons: []string{"team_scoring_registry_unavailable"}},
			ExperimentalAxes:  map[string]AxisResult{},
			ExperimentalTotal: &TotalResult{ID: "experimental_team_total_v1", Name: "队伍实验总分（虚线层）", Suppressed: true, Reasons: []string{"team_scoring_registry_unavailable"}},
			SubjectCoverage:   SubjectCoverage{CorpusMatches: c.MatchCount()},
		}
	}
	tt := c.Teams[teamID]
	if tt == nil {
		// Absent team: stable shape, both layers suppressed with a precise
		// reason.
		return &TeamScore{
			TeamID: teamID, ScoringVersion: tc.SchemaVersion,
			MetricPercentiles: map[string]PercentileResult{},
			OfficialAxes:      c.suppressedTeamAxes(tc, "team_not_in_corpus"),
			OfficialTotal:     &TotalResult{ID: tc.OfficialTotalScore.ID, Name: "队伍官方总分", Suppressed: true, Reasons: []string{"team_not_in_corpus"}},
			ExperimentalAxes:  c.suppressedTeamAxes(tc, TeamExperimentalRecipeUnavailableReason),
			ExperimentalTotal: &TotalResult{ID: "experimental_team_total_v1", Name: "队伍实验总分（虚线层）", Suppressed: true, Reasons: []string{TeamExperimentalRecipeUnavailableReason}},
			SubjectCoverage:   SubjectCoverage{CorpusMatches: c.MatchCount()},
		}
	}
	ts := &TeamScore{
		TeamID: teamID, ScoringVersion: tc.SchemaVersion,
		MetricPercentiles:  map[string]PercentileResult{},
		AggregatedMetrics:  map[string]AggregatedMetric{},
		UnavailableMetrics: map[string]UnavailableMetric{},
		OfficialAxes:       map[string]AxisResult{},
		SubjectCoverage: SubjectCoverage{
			EligibleMatches: tt.EligibleMatches, MatchIDs: tt.MatchIDs,
			PublishedMetrics: len(tt.Metrics), CorpusMatches: c.MatchCount(),
		},
		ComparisonPopulation: tc.ComparisonPopulation,
	}
	// Carry the team-tournament aggregated raw values + typed lineage (incl.
	// the aggregation entity) for drilldown.
	for mid, am := range tt.Metrics {
		ts.AggregatedMetrics[mid] = am
	}
	for mid, unavailable := range tt.UnavailableMetrics {
		ts.UnavailableMetrics[mid] = unavailable
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
			MetricID: mid, MetricVersion: am.MetricVersion, RawValue: am.Value, Percentile: pct, Direction: am.Direction,
			OfficialEligible: am.OfficialEligible, ExperimentalEligible: am.ExperimentalEligible,
			CohortSize: len(cohort),
		}
	}
	ts.OfficialAxes = c.computeTeamAxes(tt, ts.MetricPercentiles)
	ts.OfficialTotal = c.computeTeamTotal(ts, tt)
	// Experimental layer: the frozen team registry (ti2026.team-score.v1) has
	// NO machine-readable experimental recipe and no experimental axis
	// components. Expose the complete dashed interface as suppressed with the
	// precise contract reason; never invent a formula or borrow the player V3
	// layer.
	ts.ExperimentalAxes = c.suppressedTeamAxes(tc, TeamExperimentalRecipeUnavailableReason)
	ts.ExperimentalTotal = &TotalResult{
		ID: "experimental_team_total_v1", Name: "队伍实验总分（虚线层）",
		Weights: map[string]float64{}, Suppressed: true,
		Reasons:  []string{TeamExperimentalRecipeUnavailableReason},
		Coverage: Coverage{MetricCount: 0, PublishedCount: 0, MatchCount: tt.EligibleMatches},
	}
	return ts
}

// suppressedTeamAxes returns all canonical team axes in the suppressed state
// with a single explicit reason (absent team or unavailable experimental
// recipe).
func (c *Corpus) suppressedTeamAxes(tc *TeamContract, reason string) map[string]AxisResult {
	out := map[string]AxisResult{}
	if tc == nil {
		return out
	}
	for _, ax := range tc.TeamAxisNames() {
		out[ax] = AxisResult{Axis: ax, DisplayZh: tc.TeamAxisDisplayNameZh(ax), Components: map[string]ComponentResult{}, Suppressed: true, Reason: reason}
	}
	return out
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
			cr := ComponentResult{MetricID: mid, MetricVersion: c.metricVersion(mid), Weight: w}
			pr, ok := pt.Metrics[mid]
			if ok {
				cr.MetricVersion = pr.MetricVersion
			}
			pct, ok2 := pcts[mid]
			if !ok || !pr.OfficialEligible || !ok2 {
				cr.UnavailableReason = c.playerMissingReason(pt, mid, pr, ok, ok2, "official")
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
			cr := ComponentResult{MetricID: mid, MetricVersion: c.metricVersion(mid), Weight: w}
			am, ok := tt.Metrics[mid]
			if ok {
				cr.MetricVersion = am.MetricVersion
			}
			pct, ok2 := pcts[mid]
			if !ok || !am.OfficialEligible || !ok2 {
				cr.UnavailableReason = c.teamMissingReason(tt, mid, am, ok, ok2, "official")
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
func (c *Corpus) missingReason(mid string, am AggregatedMetric, ok, ok2 bool, layer string) string {
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

func (c *Corpus) metricVersion(mid string) string {
	if c.MetricReg != nil {
		if def := c.MetricReg.Find(mid); def != nil {
			return def.MetricVersion
		}
	}
	return ""
}

func (c *Corpus) playerMissingReason(pt *PlayerTournament, mid string, am AggregatedMetric, ok, ok2 bool, layer string) string {
	if !ok {
		if unavailable, found := pt.UnavailableMetrics[mid]; found && unavailable.UnavailableReason != "" {
			return unavailable.UnavailableReason
		}
	}
	return c.missingReason(mid, am, ok, ok2, layer)
}

func (c *Corpus) teamMissingReason(tt *TeamTournament, mid string, am AggregatedMetric, ok, ok2 bool, layer string) string {
	if !ok {
		if unavailable, found := tt.UnavailableMetrics[mid]; found && unavailable.UnavailableReason != "" {
			return unavailable.UnavailableReason
		}
	}
	return c.missingReason(mid, am, ok, ok2, layer)
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
// team scoring registry's axis weights:
//   - a missing MANDATORY axis suppresses the total with a precise reason;
//   - a missing OPTIONAL axis (vision) is disclosed but does NOT suppress;
//   - the remaining published team axis weights are renormalized exactly
//     (sum(w*a)/sum(w for published axes));
//   - at least minimum_publishable_axes published axes, every mandatory axis,
//     at least minimum_matches eligible team matches, and all coverage gates
//     remain required;
//   - a missing value is never imputed as 0 or 50.
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
	mandatory := map[string]bool{}
	for _, a := range tc.MandatoryAxes {
		mandatory[a] = true
	}
	optional := map[string]bool{}
	for _, a := range tc.OptionalAxes {
		optional[a] = true
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
			if mandatory[ax] {
				// Missing mandatory axis suppresses the total.
				reasons = append(reasons, "mandatory_axis_unavailable:"+ax)
			} else if optional[ax] {
				// Missing optional axis is disclosed but never suppresses.
				tr.Weights[ax] = 0 // disclosed omission; not part of A
			} else {
				reasons = append(reasons, "axis_unavailable:"+ax)
			}
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
	// Coverage gate: every declared coverage gate must pass. The registry
	// declares minimum_matches and minimum_publishable_axes; reflect any
	// metric-level coverage shortfall.
	if tc.MinimumPublishableAxes > 0 && len(published) > 0 && len(published) < tc.MinimumPublishableAxes {
		// already recorded above
	}
	if len(reasons) > 0 || totalW == 0 {
		tr.Reasons = reasons
		if len(reasons) == 0 {
			tr.Reasons = []string{"team_total_suppressed_no_publishable_axes"}
		}
		return tr
	}
	// Renormalized weighted mean over the published axis set A exactly.
	val := sum / totalW
	tr.Published = true
	tr.Suppressed = false
	tr.Value = &val
	tr.AxesIncluded = published
	tr.Coverage = Coverage{MetricCount: len(published), PublishedCount: len(published), MatchCount: tt.EligibleMatches}
	return tr
}

// computeExperimentalAxes builds the dashed V3 axes.
func (c *Corpus) computeExperimentalAxes(ps *PlayerScore) map[string]AxisResult {
	cv := c.Contract
	out := map[string]AxisResult{}
	for _, axis := range cv.AxisNames() {
		ar := AxisResult{Axis: axis, DisplayZh: cv.AxisDisplayNameZh(axis), Components: map[string]ComponentResult{}, Suppressed: true}
		official := ps.OfficialAxes[axis]
		v3Comps := cv.ExperimentalComponentsByAxis[axis]
		if !official.Published {
			for _, mid := range v3Comps {
				reason := "v3_component_not_published"
				if unavailable, found := ps.UnavailableMetrics[mid]; found && unavailable.UnavailableReason != "" {
					reason = unavailable.UnavailableReason
				}
				ar.Components[mid] = ComponentResult{MetricID: mid, MetricVersion: c.metricVersion(mid), UnavailableReason: reason}
			}
			ar.Reason = "official_axis_unavailable"
			out[axis] = ar
			continue
		}
		var v3Pcts []float64
		for _, mid := range v3Comps {
			if pr, ok := ps.MetricPercentiles[mid]; ok && pr.ExperimentalEligible {
				v3Pcts = append(v3Pcts, pr.Percentile)
				cr := ComponentResult{MetricID: mid, MetricVersion: pr.MetricVersion, Published: true, Percentile: &pr.Percentile, Weight: 0.25}
				ar.Components[mid] = cr
			} else {
				reason := "v3_component_not_published"
				if unavailable, found := ps.UnavailableMetrics[mid]; found && unavailable.UnavailableReason != "" {
					reason = unavailable.UnavailableReason
				}
				ar.Components[mid] = ComponentResult{MetricID: mid, MetricVersion: c.metricVersion(mid), UnavailableReason: reason}
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
