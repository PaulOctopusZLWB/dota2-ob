// Package scoring implements the eight-axis role radar and the official and
// experimental total-score computation exactly from the frozen machine-readable
// scoring contract (docs/specs/ti2026-radar-scoring-v1.json). Metrics are
// normalized to same-role empirical mid-rank percentiles over the frozen TI
// 2026 corpus; axes and totals use versioned role-specific weights. Axes and
// totals are suppressed when mandatory inputs, match count, or coverage gates
// fail — an unavailable metric is never imputed as 0 or 50. Official scores
// never contain V3 inputs; the experimental V3 layer is a separately named
// dashed value.
package scoring

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
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
	// Count how many are strictly below v and how many equal v.
	below := 0
	eq := 0
	for _, x := range xs {
		if x < v {
			below++
		} else if x == v {
			eq++
		}
	}
	// Mid-rank: for the block of equal values, rank = below + (eq+1)/2.
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

// MetricValue is one published player-match metric input to scoring.
type MetricValue struct {
	MetricID             string    `json:"metric_id"`
	Value                float64   `json:"value"`
	Direction            Direction `json:"direction"`
	OfficialEligible     bool      `json:"official_eligible"`
	ExperimentalEligible bool      `json:"experimental_eligible"`
}

// PlayerMatch is one player in one match with its published metric values.
type PlayerMatch struct {
	MatchID     string                 `json:"match_id"`
	AccountID   string                 `json:"account_id"`
	TeamID      string                 `json:"team_id,omitempty"`
	NominalRole string                 `json:"nominal_role"`
	Metrics     map[string]MetricValue `json:"metrics"`
}

// AxisResult is one axis of a player's radar.
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

// PlayerScore is the full scoring snapshot for one player in one match.
type PlayerScore struct {
	MatchID              string                      `json:"match_id"`
	AccountID            string                      `json:"account_id"`
	TeamID               string                      `json:"team_id,omitempty"`
	NominalRole          string                      `json:"nominal_role"`
	ScoringVersion       string                      `json:"scoring_version"`
	MetricPercentiles    map[string]PercentileResult `json:"metric_percentiles"`
	OfficialAxes         map[string]AxisResult       `json:"official_axes"`
	ExperimentalAxes     map[string]AxisResult       `json:"experimental_axes"`
	OfficialTotal        *TotalResult                `json:"official_total"`
	ExperimentalTotal    *TotalResult                `json:"experimental_total"`
	CorpusMatches        int                         `json:"corpus_matches"`
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

// Corpus holds the collection of player-match inputs across all matches and
// the computed per-role per-metric cohorts for percentile normalization.
type Corpus struct {
	Contract *Contract
	Matches  map[string][]*PlayerMatch // match_id -> players
	Players  []*PlayerMatch            // flat, all matches
	Cohorts  map[string][]float64      // "role\x00metric" -> published raw values
}

// NewCorpus builds a corpus from the contract and player-match inputs.
func NewCorpus(c *Contract, players []*PlayerMatch) *Corpus {
	cor := &Corpus{
		Contract: c,
		Matches:  map[string][]*PlayerMatch{},
		Players:  players,
		Cohorts:  map[string][]float64{},
	}
	for _, p := range players {
		cor.Matches[p.MatchID] = append(cor.Matches[p.MatchID], p)
		for mid, mv := range p.Metrics {
			key := p.NominalRole + "\x00" + mid
			cor.Cohorts[key] = append(cor.Cohorts[key], mv.Value)
		}
	}
	return cor
}

// MatchCount returns the number of distinct matches in the corpus.
func (c *Corpus) MatchCount() int {
	return len(c.Matches)
}

// Cohort returns the raw values for a role+metric cohort (may be empty).
func (c *Corpus) Cohort(role, metricID string) []float64 {
	return c.Cohorts[role+"\x00"+metricID]
}

// ScorePlayer computes the full scoring snapshot for one player given the
// corpus. It applies every suppression gate in the contract and never imputes.
func (c *Corpus) ScorePlayer(p *PlayerMatch) *PlayerScore {
	cv := c.Contract
	ps := &PlayerScore{
		MatchID: p.MatchID, AccountID: p.AccountID, TeamID: p.TeamID, NominalRole: p.NominalRole,
		ScoringVersion:       cv.SchemaVersion,
		MetricPercentiles:    map[string]PercentileResult{},
		OfficialAxes:         map[string]AxisResult{},
		ExperimentalAxes:     map[string]AxisResult{},
		CorpusMatches:        c.MatchCount(),
		ComparisonPopulation: cv.ComparisonPopulation,
	}

	// Step 1: normalize every published metric to a same-role percentile.
	for mid, mv := range p.Metrics {
		cohort := c.Cohort(p.NominalRole, mid)
		if len(cohort) < cv.MinimumMatches {
			continue // not enough cohort; metric cannot normalize
		}
		pct := MidRankPercentile(mv.Value, cohort)
		if mv.Direction == LowerBetter {
			pct = Invert(pct)
		}
		ps.MetricPercentiles[mid] = PercentileResult{
			MetricID: mid, RawValue: mv.Value, Percentile: pct, Direction: mv.Direction,
			OfficialEligible: mv.OfficialEligible, ExperimentalEligible: mv.ExperimentalEligible,
			CohortSize: len(cohort),
		}
	}

	// Step 2: official axes from official_axis_components_by_role.
	ps.OfficialAxes = c.computeOfficialAxes(p)
	ps.OfficialTotal = c.computeOfficialTotal(ps)

	// Step 3: experimental V3 dashed layer.
	ps.ExperimentalAxes = c.computeExperimentalAxes(ps)
	ps.ExperimentalTotal = c.computeExperimentalTotal(ps)

	return ps
}

func (c *Corpus) computeOfficialAxes(p *PlayerMatch) map[string]AxisResult {
	cv := c.Contract
	out := map[string]AxisResult{}
	comps := cv.OfficialAxisComponentsByRole[p.NominalRole]
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
			pr, ok := p.Metrics[mid]
			pct, ok2 := c.percentileOf(p, mid)
			cr := ComponentResult{MetricID: mid, Weight: w}
			if !ok || !pr.OfficialEligible || !ok2 {
				cr.UnavailableReason = c.missingReason(p, mid, pr, ok, ok2, "official")
				mandatoryMissing = true
			} else {
				v := pr.Value
				pv := pct
				cr.RawValue = &v
				cr.Percentile = &pv
				cr.Published = true
				weightedSum += w * pct
				weightTotal += w
				ar.Coverage.PublishedCount++
			}
			ar.Components[mid] = cr
		}
		if mandatoryMissing || weightTotal == 0 {
			ar.Reason = c.missingMetricPolicyReason(p, cm, ar)
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
func (c *Corpus) percentileOf(p *PlayerMatch, mid string) (float64, bool) {
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
func (c *Corpus) missingReason(p *PlayerMatch, mid string, pr MetricValue, ok, ok2 bool, layer string) string {
	if !ok {
		return "metric_not_published:" + mid
	}
	if !pr.OfficialEligible && layer == "official" {
		return "metric_not_official_eligible:" + mid
	}
	if !ok2 {
		return "insufficient_cohort:" + mid
	}
	return "unknown"
}

// missingMetricPolicyReason summarizes which components failed.
func (c *Corpus) missingMetricPolicyReason(p *PlayerMatch, cm map[string]float64, ar AxisResult) string {
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

// computeOfficialTotal applies the official total gate.
func (c *Corpus) computeOfficialTotal(ps *PlayerScore) *TotalResult {
	cv := c.Contract
	role := ps.NominalRole
	tr := &TotalResult{
		ID: cv.OfficialTotalScore.ID, Name: "官方总分", Weights: map[string]float64{},
		Suppressed: true,
	}
	reasons := []string{}
	if c.MatchCount() < cv.MinimumMatches {
		reasons = append(reasons, fmt.Sprintf("corpus_matches=%d_less_than_%d", c.MatchCount(), cv.MinimumMatches))
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

// computeExperimentalAxes builds the dashed V3 axes.
func (c *Corpus) computeExperimentalAxes(ps *PlayerScore) map[string]AxisResult {
	cv := c.Contract
	out := map[string]AxisResult{}
	for _, axis := range cv.AxisNames() {
		ar := AxisResult{Axis: axis, DisplayZh: cv.AxisDisplayNameZh(axis), Components: map[string]ComponentResult{}, Suppressed: true}
		official := ps.OfficialAxes[axis]
		// Official base required; dashed layer never fabricates from nothing.
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
