// Package insight evaluates bounded semantic broadcast rules without adapters.
package insight

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const (
	DraftRule     = "draft.v1"
	LaneRule      = "lane.v1"
	ItemRule      = "item.v1"
	ObjectiveRule = "objective.v1"
	ReadinessRule = "readiness.v1"
)

type Config struct {
	Version             string
	Patch               string
	DraftMinimum        uint64
	DistributionMinimum uint64
	TeamMinimum         uint64
	ExpiryMS            int64
	MaximumLiveAgeMS    int64
}

func DefaultConfig() Config {
	return Config{Version: "config.v1", Patch: "7.41", DraftMinimum: 5, DistributionMinimum: 8, TeamMinimum: 10, ExpiryMS: 30_000, MaximumLiveAgeMS: 5_000}
}

type Input struct {
	Observation  contracts.LiveObservationV1
	Previous     *contracts.LiveObservationV1
	Baselines    []contracts.HistoricalBaselineV1
	PolicyTimeMS int64
}

func Family(ruleVersion string) string {
	if i := strings.IndexByte(ruleVersion, '.'); i >= 0 {
		return ruleVersion[:i]
	}
	return ruleVersion
}

func Evaluate(input Input, config Config) []contracts.InsightCandidateV1 {
	if config.Version == "" {
		config = DefaultConfig()
	}
	o := input.Observation
	if reason := unsafeReason(o); reason != "" {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", reason)}
	}
	results := make([]contracts.InsightCandidateV1, 0, 5)
	results = append(results, historyCandidate(o, input.Baselines, config, input.PolicyTimeMS, DraftRule, "draft_hero_performance", config.DraftMinimum, 50))
	minuteMetric := "lane_10_net_worth"
	if present(o.Map.ClockTime) >= 900 {
		minuteMetric = "lane_15_net_worth"
	}
	results = append(results, historyCandidate(o, input.Baselines, config, input.PolicyTimeMS, LaneRule, minuteMetric, config.DistributionMinimum, 70))
	results = append(results, itemCandidate(o, input.Previous, input.Baselines, config, input.PolicyTimeMS))
	results = append(results, objectiveCandidate(o, input.Previous, config, input.PolicyTimeMS))
	results = append(results, readinessCandidate(o, input.Baselines, config, input.PolicyTimeMS))
	Sort(results)
	return results
}

func Sort(values []contracts.InsightCandidateV1) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Priority != values[j].Priority {
			return values[i].Priority > values[j].Priority
		}
		if confidence(values[i].Confidence) != confidence(values[j].Confidence) {
			return confidence(values[i].Confidence) > confidence(values[j].Confidence)
		}
		ti, tj := evidenceTime(values[i]), evidenceTime(values[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if values[i].RuleVersion != values[j].RuleVersion {
			return values[i].RuleVersion < values[j].RuleVersion
		}
		return values[i].CandidateID < values[j].CandidateID
	})
}

func historyCandidate(o contracts.LiveObservationV1, baselines []contracts.HistoricalBaselineV1, config Config, now int64, rule, metric string, minimum uint64, priority int) contracts.InsightCandidateV1 {
	baseline, reason := eligibleBaseline(o, baselines, config, metric, minimum)
	c := base(o, config, now, rule, priority)
	c.LocalizationKey = "insight." + Family(rule)
	if reason != "" {
		c.Availability, c.Reason = "suppressed", reason
		seal(&c)
		return c
	}
	c.SnapshotID, c.SampleSize, c.Availability = baseline.SnapshotID, baseline.Value.SampleSize, "available"
	c.SourceRequirements = []contracts.SourceRequirementV1{{Source: "gsi", MinimumConfidence: "medium"}, {Source: "historical_baseline", MinimumConfidence: "verified"}}
	c.ObservedValues = []contracts.ObservedMetricV1{{Name: metric, Value: *baseline.Value.Value, Unit: "baseline"}}
	seal(&c)
	return c
}

func itemCandidate(o contracts.LiveObservationV1, previous *contracts.LiveObservationV1, baselines []contracts.HistoricalBaselineV1, config Config, now int64) contracts.InsightCandidateV1 {
	name := newlyObservedItem(o, previous)
	if name == "" {
		c := base(o, config, now, ItemRule, 75)
		c.LocalizationKey = "insight.item"
		c.Availability, c.Reason = "suppressed", "no_key_item_completion"
		seal(&c)
		return c
	}
	return historyCandidate(o, baselines, config, now, ItemRule, "item_"+name+"_timing_ms", config.DistributionMinimum, 75)
}

func objectiveCandidate(o contracts.LiveObservationV1, previous *contracts.LiveObservationV1, config Config, now int64) contracts.InsightCandidateV1 {
	c := base(o, config, now, ObjectiveRule, 90)
	c.LocalizationKey = "insight.objective_exchange"
	changed := previous != nil && (present(o.Map.RadiantScore) != present(previous.Map.RadiantScore) || present(o.Map.DireScore) != present(previous.Map.DireScore) || buildingChanged(o.Buildings, previous.Buildings) || state(o.Roshan.State) != state(previous.Roshan.State) || state(o.Tormentor.State) != state(previous.Tormentor.State))
	if !changed {
		c.Availability, c.Reason = "suppressed", "objective_non_event"
	} else {
		c.Availability = "available"
		c.SourceRequirements = []contracts.SourceRequirementV1{{Source: "gsi", MinimumConfidence: "medium"}}
	}
	seal(&c)
	return c
}

func readinessCandidate(o contracts.LiveObservationV1, baselines []contracts.HistoricalBaselineV1, config Config, now int64) contracts.InsightCandidateV1 {
	c := historyCandidate(o, baselines, config, now, ReadinessRule, "teamfight_readiness", config.TeamMinimum, 80)
	if len(o.Participants) != 10 {
		c.Availability, c.Reason = "suppressed", "missing_team_observation"
		c.ObservedValues = nil
		seal(&c)
	}
	return c
}

func eligibleBaseline(o contracts.LiveObservationV1, values []contracts.HistoricalBaselineV1, config Config, metric string, minimum uint64) (contracts.HistoricalBaselineV1, string) {
	for _, value := range values {
		if value.SessionID != o.Evidence.SessionID {
			continue
		}
		if value.Key.Metric != metric {
			continue
		}
		if config.Patch != "" && value.Key.Patch != config.Patch {
			return contracts.HistoricalBaselineV1{}, "patch_boundary"
		}
		if value.Value.State != contracts.ValuePresent || value.Value.Value == nil {
			return contracts.HistoricalBaselineV1{}, "missing_history"
		}
		if value.Value.SampleSize < minimum {
			return contracts.HistoricalBaselineV1{}, "low_sample_history"
		}
		return value, ""
	}
	return contracts.HistoricalBaselineV1{}, "missing_history"
}

func unsafeReason(o contracts.LiveObservationV1) string {
	if o.SchemaVersion != contracts.LiveObservationSchemaV1 || o.Evidence.SessionID == "" || o.Evidence.Sequence == 0 {
		return "inconsistent_live_input"
	}
	if v := o.Map.Paused; v.State == contracts.ValuePresent && v.Value != nil && *v.Value {
		return "paused_live_input"
	}
	for _, flag := range o.Quality.Flags {
		switch flag {
		case "stale":
			return "stale_live_input"
		case "inconsistent", "unsafe", "out_of_order":
			return flag + "_live_input"
		}
	}
	return ""
}

func base(o contracts.LiveObservationV1, config Config, now int64, rule string, priority int) contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{SchemaVersion: contracts.InsightCandidateSchemaV1, SessionID: o.Evidence.SessionID, RuleVersion: rule, ConfigVersion: config.Version, Evidence: []contracts.EvidenceRefV1{o.Evidence}, Confidence: o.Quality.Confidence, Priority: priority, CreatedTimeMS: now, ExpiryTimeMS: now + config.ExpiryMS}
}
func suppress(o contracts.LiveObservationV1, config Config, now int64, rule, reason string) contracts.InsightCandidateV1 {
	c := base(o, config, now, rule, 100)
	c.LocalizationKey = "insight.suppressed"
	c.Availability, c.Reason = "suppressed", reason
	seal(&c)
	return c
}
func seal(c *contracts.InsightCandidateV1) {
	copy := *c
	copy.CandidateID = ""
	payload, _ := contracts.MarshalCanonical(copy)
	sum := sha256.Sum256(payload)
	c.CandidateID = hex.EncodeToString(sum[:])
}
func present[T ~int64](v contracts.ObservedV1[T]) T {
	if v.State == contracts.ValuePresent && v.Value != nil {
		return *v.Value
	}
	return 0
}
func state(v contracts.ObservedV1[string]) string {
	if v.State == contracts.ValuePresent && v.Value != nil {
		return *v.Value
	}
	return ""
}
func evidenceTime(c contracts.InsightCandidateV1) (zeroTime time.Time) {
	if len(c.Evidence) > 0 {
		return c.Evidence[0].ReceiveTime
	}
	return zeroTime
}
func confidence(v string) int {
	switch v {
	case "high", "verified":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}
func newlyObservedItem(o contracts.LiveObservationV1, previous *contracts.LiveObservationV1) string {
	if previous == nil {
		return ""
	}
	prior := map[string]bool{}
	for _, p := range previous.Participants {
		for _, item := range p.Items {
			prior[state(item.Name)] = true
		}
	}
	for _, p := range o.Participants {
		for _, item := range p.Items {
			name := state(item.Name)
			if name != "" && !prior[name] {
				return name
			}
		}
	}
	return ""
}
func buildingChanged(current, previous []contracts.BuildingObservationV1) bool {
	for i := range current {
		if i >= len(previous) || current[i].Team != previous[i].Team || current[i].Name != previous[i].Name || presentDecimal(current[i].Health) != presentDecimal(previous[i].Health) {
			return true
		}
	}
	return false
}
func presentDecimal(v contracts.ObservedV1[contracts.Decimal]) contracts.Decimal {
	if v.State == contracts.ValuePresent && v.Value != nil {
		return *v.Value
	}
	return ""
}
