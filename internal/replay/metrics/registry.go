// Package metrics implements the full V1/V2/V3 metric registry and
// deterministic calculators over the normalized fact stream. Every metric
// definition comes from the frozen machine-readable registry
// (docs/specs/ti2026-role-phase-metrics-v1.json); a metric either publishes a
// value with numerator/denominator/opportunity/sample/confidence/evidence or
// resolves to an explicit unavailable state with a precise reason. Unavailable
// is null plus a reason code — never a fabricated zero.
package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Capability and epistemic class constants.
const (
	CapabilityV1 = "V1"
	CapabilityV2 = "V2"
	CapabilityV3 = "V3"

	ClassDirect         = "direct"
	ClassDerived        = "derived"
	ClassModelled       = "modelled"
	ClassCounterfactual = "counterfactual"
	ClassUnavailable    = "unavailable"

	// RegistrySchemaVersion is the version of the frozen metric registry file.
	RegistrySchemaVersion = "ti2026.role-phase-metrics.v1"
)

// Metric is the full machine-readable contract for one registry entry.
// Field names mirror the frozen JSON so the registry is the single source of
// truth and no field is silently downgraded by the Go definition.
type Metric struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	BusinessMeaning           string   `json:"business_meaning"`
	Description               string   `json:"description"`
	Domain                    string   `json:"domain"`
	Roles                     []string `json:"roles"`
	Phases                    []string `json:"phases"`
	CurrentClass              string   `json:"current_class"`
	TargetClass               string   `json:"target_class"`
	CapabilityLevel           string   `json:"capability_level"`
	EpistemicClass            string   `json:"epistemic_class"`
	MetricVersion             string   `json:"metric_version"`
	ReportLevel               string   `json:"report_level"` // player|team|match
	Unit                      string   `json:"unit"`
	Direction                 string   `json:"direction"` // higher_better|lower_better|context_only
	ScoreDirection            string   `json:"score_direction"`
	AggregationRule           string   `json:"aggregation_rule"`
	Numerator                 string   `json:"numerator"`
	Denominator               string   `json:"denominator"`
	Opportunity               string   `json:"opportunity"`
	Algorithm                 string   `json:"algorithm"`
	TimeWindow                string   `json:"time_window"`
	SpaceWindow               string   `json:"space_window"`
	Exclusions                []string `json:"exclusions"`
	RawInputs                 []string `json:"raw_inputs"`
	Confidence                string   `json:"confidence"`
	KnownBiases               []string `json:"known_biases"`
	TestCase                  string   `json:"test_case"`
	RadarAxis                 string   `json:"radar_axis"`
	OfficialScoreEligible     bool     `json:"official_score_eligible"`
	ExperimentalScoreEligible bool     `json:"experimental_score_eligible"`
	RequiredFactFamilies      []string `json:"required_fact_families"`
	FieldQualityGates         []string `json:"field_quality_gates"`
	EvidenceRecordShape       struct {
		Required []string `json:"required"`
		Lineage  string   `json:"lineage"`
	} `json:"evidence_record_shape"`
	AbstentionRule string   `json:"abstention_rule"`
	FailureModes   []string `json:"failure_modes"`
	Tests          struct {
		Unit      string `json:"unit"`
		Fixture   string `json:"fixture"`
		Gold      string `json:"gold"`
		Property  string `json:"property"`
		Invariant string `json:"invariant"`
	} `json:"tests"`
	ScorePublicationGate struct {
		OfficialEligible     bool   `json:"official_eligible"`
		ExperimentalEligible bool   `json:"experimental_eligible"`
		Rule                 string `json:"rule"`
		FailureResult        string `json:"failure_result"`
	} `json:"score_publication_gate"`
}

// Registry is the parsed frozen metric registry document.
type Registry struct {
	SchemaVersion    string   `json:"schema_version"`
	Issue            string   `json:"issue"`
	EpistemicClasses []string `json:"epistemic_classes"`
	Conventions      struct {
		CurrentClass     string            `json:"current_class"`
		TargetClass      string            `json:"target_class"`
		Missing          string            `json:"missing"`
		Intervals        string            `json:"intervals"`
		Positions        string            `json:"positions"`
		Roles            string            `json:"roles"`
		Publication      string            `json:"publication"`
		CapabilityLevels map[string]string `json:"capability_levels"`
		RadarAxes        []string          `json:"radar_axes"`
		Scoring          string            `json:"scoring"`
		MetricContract   string            `json:"metric_contract"`
		AtomicBaseline   string            `json:"atomic_baseline"`
	} `json:"conventions"`
	Metrics []Metric `json:"metrics"`
}

// LoadRegistry reads and validates the frozen metric registry JSON.
func LoadRegistry(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("metrics: read registry: %w", err)
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("metrics: decode registry: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// ParseRegistry decodes a registry from bytes (used by tests).
func ParseRegistry(b []byte) (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("metrics: decode registry: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate enforces the registry contract: 52 explicit metric objects each
// carrying the full machine-readable contract. A missing contract field is a
// hard failure so a definition can never be silently downgraded.
func (r *Registry) Validate() error {
	if r.SchemaVersion != RegistrySchemaVersion {
		return fmt.Errorf("metrics: registry schema version %q != %q", r.SchemaVersion, RegistrySchemaVersion)
	}
	if len(r.Metrics) != 52 {
		return fmt.Errorf("metrics: registry has %d metrics, want 52", len(r.Metrics))
	}
	seen := map[string]bool{}
	for i := range r.Metrics {
		m := &r.Metrics[i]
		if m.ID == "" || m.Name == "" || m.Unit == "" || m.AggregationRule == "" {
			return fmt.Errorf("metrics: metric %d incomplete identity/unit/aggregation", i)
		}
		if m.CapabilityLevel == "" || m.EpistemicClass == "" || m.MetricVersion == "" || m.ReportLevel == "" {
			return fmt.Errorf("metrics: %s missing capability/class/version/level", m.ID)
		}
		if len(m.RequiredFactFamilies) == 0 {
			return fmt.Errorf("metrics: %s missing required_fact_families", m.ID)
		}
		if len(m.FieldQualityGates) == 0 {
			return fmt.Errorf("metrics: %s missing field_quality_gates", m.ID)
		}
		if len(m.EvidenceRecordShape.Required) == 0 {
			return fmt.Errorf("metrics: %s missing evidence_record_shape", m.ID)
		}
		if m.AbstentionRule == "" {
			return fmt.Errorf("metrics: %s missing abstention_rule", m.ID)
		}
		if m.ScorePublicationGate.Rule == "" {
			return fmt.Errorf("metrics: %s missing score_publication_gate", m.ID)
		}
		if seen[m.ID] {
			return fmt.Errorf("metrics: duplicate metric id %q", m.ID)
		}
		seen[m.ID] = true
	}
	return nil
}

// Find returns the metric definition by id.
func (r *Registry) Find(id string) *Metric {
	for i := range r.Metrics {
		if r.Metrics[i].ID == id {
			return &r.Metrics[i]
		}
	}
	return nil
}

// AppliesToRole reports whether the metric is defined for a nominal role.
func (m *Metric) AppliesToRole(role string) bool {
	for _, r := range m.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// SortedIDs returns all registry metric ids in deterministic order.
func (r *Registry) SortedIDs() []string {
	out := make([]string, 0, len(r.Metrics))
	for _, m := range r.Metrics {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out
}

// UnavailableReasonFor returns the precise reason string when a metric's
// required input is absent from the accepted adapter.
func (m *Metric) UnavailableReasonFor(missing string) string {
	if missing == "" {
		return "required_input_missing"
	}
	return missing
}
