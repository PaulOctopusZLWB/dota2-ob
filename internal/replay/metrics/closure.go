package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ClosureRow is one metric's definition-specific closure entry: the stable
// evaluator/algorithm id, the concrete Go entry point, the accepted source
// fact/episode/phase fields, the definition-specific publication and
// abstention/unavailable gates, and the persisted/API resolution fields. It is
// the reviewer-grade mapping that the frozen 52-row registry requires: no
// metric resolves through a generic default branch.
type ClosureRow struct {
	MetricID             string `json:"metric_id"`
	EvaluatorID          string `json:"evaluator_id"`
	EntryPoint           string `json:"entry_point"`
	CapabilityLevel      string `json:"capability_level"`
	ReportLevel          string `json:"report_level"`
	Resolved             string `json:"resolved"` // published|unavailable
	AcceptedSourceFields string `json:"accepted_source_fields"`
	PublicationGate      string `json:"publication_gate"`
	UnavailableGate      string `json:"unavailable_gate"`
}

// Closure is the validated 52-row metric closure contract keyed by metric ID.
type Closure struct {
	SchemaVersion string       `json:"schema_version"`
	Issue         string       `json:"issue"`
	Count         int          `json:"count"`
	Rules         []string     `json:"rules"`
	Metrics       []ClosureRow `json:"metrics"`
}

// LoadClosure reads and validates the frozen metric closure JSON.
func LoadClosure(path string) (*Closure, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("metrics: read closure: %w", err)
	}
	return ParseClosure(b)
}

// ParseClosure decodes a closure from bytes (used by tests).
func ParseClosure(b []byte) (*Closure, error) {
	var c Closure
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("metrics: decode closure: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate enforces the closure contract: exactly 52 rows, unique metric ids,
// non-empty evaluator/entry-point/gate fields, and every row either published
// with a concrete entry point or unavailable with a precise gate reason.
func (c *Closure) Validate() error {
	if c.SchemaVersion != ClosureSchemaVersion {
		return fmt.Errorf("metrics: closure schema %q != %q", c.SchemaVersion, ClosureSchemaVersion)
	}
	if len(c.Metrics) != 52 {
		return fmt.Errorf("metrics: closure has %d rows, want 52", len(c.Metrics))
	}
	seen := map[string]bool{}
	for i := range c.Metrics {
		r := &c.Metrics[i]
		if r.MetricID == "" || r.EvaluatorID == "" || r.EntryPoint == "" {
			return fmt.Errorf("metrics: closure row %d missing metric/evaluator/entry point", i)
		}
		if r.CapabilityLevel == "" || r.ReportLevel == "" || r.AcceptedSourceFields == "" || r.PublicationGate == "" {
			return fmt.Errorf("metrics: closure row %s incomplete gate/source fields", r.MetricID)
		}
		switch r.Resolved {
		case "published":
			if r.UnavailableGate == "" {
				return fmt.Errorf("metrics: closure %s published row missing unavailable gate", r.MetricID)
			}
			if !isComputeEntryPoint(r.EntryPoint) {
				return fmt.Errorf("metrics: closure %s published row entry point %q is not a computeMetric dispatch", r.MetricID, r.EntryPoint)
			}
		case "unavailable":
			if r.UnavailableGate == "" {
				return fmt.Errorf("metrics: closure %s unavailable row missing gate reason", r.MetricID)
			}
		default:
			return fmt.Errorf("metrics: closure %s unknown resolved state %q", r.MetricID, r.Resolved)
		}
		if seen[r.MetricID] {
			return fmt.Errorf("metrics: closure duplicate metric id %q", r.MetricID)
		}
		seen[r.MetricID] = true
	}
	if c.Count != len(c.Metrics) {
		return fmt.Errorf("metrics: closure count %d != rows %d", c.Count, len(c.Metrics))
	}
	return nil
}

// isComputeEntryPoint reports whether an entry point names a concrete
// evaluator dispatch (computeMetric for player metrics, or the match-level
// phaseDurationValue evaluator).
func isComputeEntryPoint(ep string) bool {
	return len(ep) >= len("computeMetric:") && ep[:len("computeMetric:")] == "computeMetric:" ||
		ep == "phaseDurationValue"
}

// Lookup returns the closure row for a metric id, or nil when unmapped.
func (c *Closure) Lookup(metricID string) *ClosureRow {
	for i := range c.Metrics {
		if c.Metrics[i].MetricID == metricID {
			return &c.Metrics[i]
		}
	}
	return nil
}

// ValidateAgainstRegistry proves the closure and the frozen registry agree
// exactly: every registry id maps to exactly one closure row, and no closure
// row references an unknown metric id. Unknown, duplicate, or unmapped ids
// fail hard so a definition can never silently resolve through a default.
func (c *Closure) ValidateAgainstRegistry(reg *Registry) error {
	if reg == nil {
		return fmt.Errorf("metrics: closure validation requires a registry")
	}
	regIDs := reg.SortedIDs()
	closureIDs := make([]string, 0, len(c.Metrics))
	for i := range c.Metrics {
		closureIDs = append(closureIDs, c.Metrics[i].MetricID)
	}
	sort.Strings(closureIDs)
	if len(regIDs) != len(closureIDs) {
		return fmt.Errorf("metrics: closure ids %d != registry ids %d", len(closureIDs), len(regIDs))
	}
	for i := range regIDs {
		if regIDs[i] != closureIDs[i] {
			return fmt.Errorf("metrics: closure/registry id mismatch at %d: %q vs %q", i, closureIDs[i], regIDs[i])
		}
	}
	// Every registry id has a closure row (guaranteed by the sorted equality)
	// and every closure row has a registry definition.
	for i := range c.Metrics {
		if reg.Find(c.Metrics[i].MetricID) == nil {
			return fmt.Errorf("metrics: closure references unknown registry id %q", c.Metrics[i].MetricID)
		}
	}
	return nil
}
