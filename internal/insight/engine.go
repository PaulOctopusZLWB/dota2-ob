// Package insight evaluates bounded semantic broadcast rules without adapters.
package insight

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
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
	MaximumHistoryAgeMS int64
	KeyItems            []string
}

func DefaultConfig() Config {
	return Config{Version: "config.v1", Patch: "7.41", DraftMinimum: 5, DistributionMinimum: 8, TeamMinimum: 10, ExpiryMS: 30_000, MaximumLiveAgeMS: 5_000, MaximumHistoryAgeMS: int64((180 * 24 * time.Hour) / time.Millisecond), KeyItems: []string{"item_blink"}}
}

type Input struct {
	Observation  contracts.LiveObservationV1
	Previous     *contracts.LiveObservationV1
	Manifest     *contracts.HistoricalSnapshotManifestV1
	Lineage      *contracts.PolicyLineageManifestV2
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
	if config.MaximumLiveAgeMS > 0 && input.PolicyTimeMS-o.Evidence.ReceiveTime.UnixMilli() > config.MaximumLiveAgeMS {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "stale_live_input")}
	}
	if reason := unsafeReason(o); reason != "" {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", reason)}
	}
	results := make([]contracts.InsightCandidateV1, 0, 5)
	results = append(results, historyCandidate(o, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS, DraftRule, "draft_hero_performance", config.DraftMinimum, 50))
	minuteMetric := "lane_10_net_worth"
	if present(o.Map.ClockTime) >= 900 {
		minuteMetric = "lane_15_net_worth"
	}
	results = append(results, historyCandidate(o, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS, LaneRule, minuteMetric, config.DistributionMinimum, 70))
	results = append(results, itemCandidate(o, input.Previous, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS))
	results = append(results, objectiveCandidate(o, input.Previous, config, input.PolicyTimeMS))
	results = append(results, readinessCandidate(o, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS))
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

func historyCandidate(o contracts.LiveObservationV1, manifest *contracts.HistoricalSnapshotManifestV1, lineage *contracts.PolicyLineageManifestV2, baselines []contracts.HistoricalBaselineV1, config Config, now int64, rule, metric string, minimum uint64, priority int) contracts.InsightCandidateV1 {
	baseline, reason := eligibleBaseline(o, manifest, lineage, baselines, config, now, metric, minimum)
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
	live, parameters, ok := familyObservation(rule, o, baseline)
	if !ok {
		c.Availability, c.Reason, c.ObservedValues = "suppressed", "missing_family_telemetry", nil
	} else {
		c.ObservedValues = append(c.ObservedValues, live...)
		c.Parameters = parameters
	}
	seal(&c)
	return c
}

func itemCandidate(o contracts.LiveObservationV1, previous *contracts.LiveObservationV1, manifest *contracts.HistoricalSnapshotManifestV1, lineage *contracts.PolicyLineageManifestV2, baselines []contracts.HistoricalBaselineV1, config Config, now int64) contracts.InsightCandidateV1 {
	name := newlyObservedItem(o, previous, config.KeyItems)
	if name == "" {
		c := base(o, config, now, ItemRule, 75)
		c.LocalizationKey = "insight.item"
		c.Availability, c.Reason = "suppressed", "no_key_item_completion"
		seal(&c)
		return c
	}
	return historyCandidate(o, manifest, lineage, baselines, config, now, ItemRule, "item_"+name+"_timing_ms", config.DistributionMinimum, 75)
}

func objectiveCandidate(o contracts.LiveObservationV1, previous *contracts.LiveObservationV1, config Config, now int64) contracts.InsightCandidateV1 {
	c := base(o, config, now, ObjectiveRule, 90)
	c.LocalizationKey = "insight.objective_exchange"
	changed, complete := objectiveChanged(o, previous)
	if !complete {
		c.Availability, c.Reason = "suppressed", "missing_objective_telemetry"
		seal(&c)
		return c
	}
	if !changed {
		c.Availability, c.Reason = "suppressed", "objective_non_event"
	} else {
		c.Availability = "available"
		c.SourceRequirements = []contracts.SourceRequirementV1{{Source: "gsi", MinimumConfidence: "medium"}}
		c.ObservedValues = objectiveMetrics(o, *previous)
	}
	seal(&c)
	return c
}

func readinessCandidate(o contracts.LiveObservationV1, manifest *contracts.HistoricalSnapshotManifestV1, lineage *contracts.PolicyLineageManifestV2, baselines []contracts.HistoricalBaselineV1, config Config, now int64) contracts.InsightCandidateV1 {
	c := historyCandidate(o, manifest, lineage, baselines, config, now, ReadinessRule, "teamfight_readiness", config.TeamMinimum, 80)
	if len(o.Participants) != 10 || !readinessComplete(o.Participants) {
		c.Availability, c.Reason = "suppressed", "missing_team_observation"
		c.ObservedValues = nil
		seal(&c)
	}
	return c
}

func eligibleBaseline(o contracts.LiveObservationV1, manifest *contracts.HistoricalSnapshotManifestV1, lineage *contracts.PolicyLineageManifestV2, values []contracts.HistoricalBaselineV1, config Config, now int64, metric string, minimum uint64) (contracts.HistoricalBaselineV1, string) {
	if manifest == nil || lineage == nil || manifest.Validate() != nil || lineage.Validate() != nil || lineage.SessionID != o.Evidence.SessionID || lineage.HistoricalSnapshotID != manifest.SnapshotID || lineage.HistoricalSnapshotSHA256 != manifest.ContentSHA256 || lineage.TournamentScopeID != manifest.TournamentScopeID || lineage.TournamentScopeSHA256 != manifest.TournamentScopeSHA256 {
		return contracts.HistoricalBaselineV1{}, "invalid_history_binding"
	}
	if config.MaximumHistoryAgeMS > 0 && now-manifest.CutoffTime.UnixMilli() > config.MaximumHistoryAgeMS {
		return contracts.HistoricalBaselineV1{}, "stale_history"
	}
	for _, value := range values {
		if value.SessionID != o.Evidence.SessionID {
			continue
		}
		if value.Key.Metric != metric {
			continue
		}
		if value.ValidateAgainst(*manifest) != nil || !contains(lineage.EligibleBaselineSHA256, value.BaselineID) || value.ActiveMatchID != observedString(o.MatchID) {
			continue
		}
		if value.Key.Window != "current_patch" || value.Key.SampleDefinition != "completed_matches" || value.Value.PeriodEnd.After(manifest.CutoffTime) || value.GeneratedTime.After(manifest.SealedAt) {
			continue
		}
		if !baselineIdentityMatches(o, value) {
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
func newlyObservedItem(o contracts.LiveObservationV1, previous *contracts.LiveObservationV1, keyItems []string) string {
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
			if name != "" && contains(keyItems, name) && !prior[name] {
				return name
			}
		}
	}
	return ""
}

func objectiveChanged(current contracts.LiveObservationV1, previous *contracts.LiveObservationV1) (bool, bool) {
	if previous == nil || !observed(current.Map.RadiantScore) || !observed(previous.Map.RadiantScore) || !observed(current.Map.DireScore) || !observed(previous.Map.DireScore) || !observed(current.Roshan.State) || !observed(previous.Roshan.State) || !observed(current.Tormentor.State) || !observed(previous.Tormentor.State) || len(current.Participants) != len(previous.Participants) {
		return false, false
	}
	for i := range current.Participants {
		if !observed(current.Participants[i].NetWorth) || !observed(previous.Participants[i].NetWorth) {
			return false, false
		}
	}
	if len(current.Buildings) != len(previous.Buildings) {
		return false, false
	}
	for i := range current.Buildings {
		if current.Buildings[i].Team != previous.Buildings[i].Team || current.Buildings[i].Name != previous.Buildings[i].Name || !observed(current.Buildings[i].Health) || !observed(previous.Buildings[i].Health) {
			return false, false
		}
	}
	return *current.Map.RadiantScore.Value != *previous.Map.RadiantScore.Value || *current.Map.DireScore.Value != *previous.Map.DireScore.Value || buildingChanged(current.Buildings, previous.Buildings) || *current.Roshan.State.Value != *previous.Roshan.State.Value || *current.Tormentor.State.Value != *previous.Tormentor.State.Value, true
}

func objectiveMetrics(current, previous contracts.LiveObservationV1) []contracts.ObservedMetricV1 {
	radiant, dire := 0.0, 0.0
	for i := range current.Participants {
		delta := decimalFloat(*current.Participants[i].NetWorth.Value) - decimalFloat(*previous.Participants[i].NetWorth.Value)
		if current.Participants[i].TeamKey == "radiant" {
			radiant += delta
		} else if current.Participants[i].TeamKey == "dire" {
			dire += delta
		}
	}
	return []contracts.ObservedMetricV1{
		{Name: "radiant_kill_delta", Value: contracts.Decimal(strconv.FormatInt(*current.Map.RadiantScore.Value-*previous.Map.RadiantScore.Value, 10)), Unit: "kills"},
		{Name: "dire_kill_delta", Value: contracts.Decimal(strconv.FormatInt(*current.Map.DireScore.Value-*previous.Map.DireScore.Value, 10)), Unit: "kills"},
		{Name: "radiant_net_worth_delta", Value: decimal(radiant), Unit: "gold"},
		{Name: "dire_net_worth_delta", Value: decimal(dire), Unit: "gold"},
	}
}

func readinessComplete(parts []contracts.ParticipantObservationV1) bool {
	for _, p := range parts {
		if !observed(p.Alive) || !observed(p.HealthPercent) || !observed(p.ManaPercent) || (!*p.Alive.Value && !observed(p.RespawnSeconds)) {
			return false
		}
		for _, a := range p.Abilities {
			if !observed(a.Cooldown) || !observed(a.CanCast) {
				return false
			}
		}
		for _, item := range p.Items {
			if !observed(item.Cooldown) || !observed(item.CanCast) {
				return false
			}
		}
	}
	return true
}

func familyObservation(rule string, o contracts.LiveObservationV1, baseline contracts.HistoricalBaselineV1) ([]contracts.ObservedMetricV1, []contracts.TypedParameterV1, bool) {
	p := matchingParticipant(o, baseline)
	switch Family(rule) {
	case "draft":
		if p == nil || !observed(p.HeroID) {
			return nil, nil, false
		}
		hero := strconv.FormatInt(*p.HeroID.Value, 10)
		return []contracts.ObservedMetricV1{{Name: "draft_hero_id", Value: contracts.Decimal(hero), Unit: "hero_id"}}, []contracts.TypedParameterV1{stringParameter("player_id", baseline.Key.PlayerID), stringParameter("role", baseline.Key.Role), stringParameter("hero_id", hero)}, true
	case "lane":
		if p == nil || !observed(p.NetWorth) || !observed(p.Level) || !observed(o.Map.ClockTime) {
			return nil, nil, false
		}
		return []contracts.ObservedMetricV1{{Name: "observed_net_worth", Value: *p.NetWorth.Value, Unit: "gold"}, {Name: "observed_level", Value: contracts.Decimal(strconv.FormatInt(*p.Level.Value, 10)), Unit: "level"}, {Name: "checkpoint_time", Value: contracts.Decimal(strconv.FormatInt(*o.Map.ClockTime.Value, 10)), Unit: "seconds"}}, nil, true
	case "item":
		if p == nil || !observed(o.Map.ClockTime) {
			return nil, nil, false
		}
		return []contracts.ObservedMetricV1{{Name: "observed_item_time", Value: contracts.Decimal(strconv.FormatInt(*o.Map.ClockTime.Value*1000, 10)), Unit: "milliseconds"}}, []contracts.TypedParameterV1{stringParameter("player_id", baseline.Key.PlayerID), stringParameter("hero_id", baseline.Key.HeroID)}, true
	case "readiness":
		if !readinessComplete(o.Participants) {
			return nil, nil, false
		}
		dead, respawn, cooldown, health, mana := int64(0), 0.0, 0.0, 0.0, 0.0
		for _, participant := range o.Participants {
			health += decimalFloat(*participant.HealthPercent.Value)
			mana += decimalFloat(*participant.ManaPercent.Value)
			if !*participant.Alive.Value {
				dead++
				respawn += decimalFloat(*participant.RespawnSeconds.Value)
			}
			for _, ability := range participant.Abilities {
				cooldown += decimalFloat(*ability.Cooldown.Value)
			}
			for _, item := range participant.Items {
				cooldown += decimalFloat(*item.Cooldown.Value)
			}
		}
		return []contracts.ObservedMetricV1{{Name: "dead_players", Value: contracts.Decimal(strconv.FormatInt(dead, 10)), Unit: "players"}, {Name: "respawn_seconds", Value: decimal(respawn), Unit: "seconds"}, {Name: "cooldown_seconds", Value: decimal(cooldown), Unit: "seconds"}, {Name: "mean_health_percent", Value: decimal(health / 10), Unit: "percent"}, {Name: "mean_mana_percent", Value: decimal(mana / 10), Unit: "percent"}}, nil, true
	}
	return nil, nil, false
}

func matchingParticipant(o contracts.LiveObservationV1, baseline contracts.HistoricalBaselineV1) *contracts.ParticipantObservationV1 {
	for i := range o.Participants {
		p := &o.Participants[i]
		if p.VerifiedIdentity == nil || (baseline.Key.RosterID != "" && p.VerifiedIdentity.RosterID != baseline.Key.RosterID) || (baseline.Key.PlayerID != "" && p.VerifiedIdentity.PlayerID != baseline.Key.PlayerID) {
			continue
		}
		if baseline.Key.HeroID != "" && (!observed(p.HeroID) || strconv.FormatInt(*p.HeroID.Value, 10) != baseline.Key.HeroID) {
			continue
		}
		return p
	}
	return nil
}

func baselineIdentityMatches(o contracts.LiveObservationV1, baseline contracts.HistoricalBaselineV1) bool {
	return matchingParticipant(o, baseline) != nil
}

func stringParameter(name, value string) contracts.TypedParameterV1 {
	copy := value
	return contracts.TypedParameterV1{Name: name, Type: "string", StringValue: &copy}
}

func observed[T any](v contracts.ObservedV1[T]) bool {
	return v.State == contracts.ValuePresent && v.Value != nil
}
func observedString(v contracts.ObservedV1[string]) string {
	if observed(v) {
		return *v.Value
	}
	return ""
}
func contains(values []string, value string) bool {
	for _, x := range values {
		if x == value {
			return true
		}
	}
	return false
}
func decimalFloat(v contracts.Decimal) float64 { n, _ := strconv.ParseFloat(string(v), 64); return n }
func decimal(v float64) contracts.Decimal {
	return contracts.Decimal(strconv.FormatFloat(v, 'f', -1, 64))
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
