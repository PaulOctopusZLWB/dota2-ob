// Package insight evaluates bounded semantic broadcast rules without adapters.
package insight

import (
	"math/big"
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

func normalizeConfig(config Config) Config {
	d := DefaultConfig()
	if config.Version == "" {
		config.Version = d.Version
	}
	if config.Patch == "" {
		config.Patch = d.Patch
	}
	if config.DraftMinimum < 5 {
		config.DraftMinimum = 5
	}
	if config.DistributionMinimum < 8 {
		config.DistributionMinimum = 8
	}
	if config.TeamMinimum < 10 {
		config.TeamMinimum = 10
	}
	if config.ExpiryMS <= 0 {
		config.ExpiryMS = d.ExpiryMS
	}
	if config.MaximumLiveAgeMS <= 0 {
		config.MaximumLiveAgeMS = d.MaximumLiveAgeMS
	}
	if config.MaximumHistoryAgeMS <= 0 {
		config.MaximumHistoryAgeMS = d.MaximumHistoryAgeMS
	}
	if len(config.KeyItems) == 0 {
		config.KeyItems = append([]string(nil), d.KeyItems...)
	}
	sort.Strings(config.KeyItems)
	return config
}

func ConfigArtifact(config Config) contracts.PolicyArtifactIdentityV2 {
	config = normalizeConfig(config)
	hash, _ := contracts.CanonicalSHA256(config)
	return contracts.PolicyArtifactIdentityV2{Version: config.Version, ContentSHA256: hash}
}

func RulesArtifact() contracts.PolicyArtifactIdentityV2 {
	rules := []string{DraftRule, ItemRule, LaneRule, ObjectiveRule, ReadinessRule}
	artifact, _ := contracts.RuleVersionsArtifact("rules.v1", rules)
	return artifact
}

type Input struct {
	Observation  contracts.LiveObservationV1
	Previous     *contracts.LiveObservationV1
	Manifest     *contracts.HistoricalSnapshotManifestV1
	Lineage      *contracts.PolicyLineageManifestV2
	Baselines    []contracts.HistoricalBaselineV1
	PolicyTimeMS int64
}

// LiveOnlyInput makes unavailable history a typed, validated condition. It
// deliberately has no snapshot or baseline field.
type LiveOnlyInput struct {
	Observation  contracts.LiveObservationV1
	Previous     *contracts.LiveObservationV1
	History      contracts.HistoryAvailabilityBindingV1
	Lineage      contracts.PolicyLineageManifestV3
	PolicyTimeMS int64
}

// EvaluateLiveOnly emits only source-faithful non-history rules. Historical
// families are absent, rather than represented by fabricated zero baselines or
// history-dependent suppressed candidates.
func EvaluateLiveOnly(input LiveOnlyInput, config Config) []contracts.InsightCandidateV1 {
	config = normalizeConfig(config)
	o := input.Observation
	bindingID, historyErr := input.History.ContentID()
	lineageID, lineageErr := input.Lineage.ContentID()
	_ = lineageID
	if historyErr != nil || lineageErr != nil || input.History.Mode != contracts.HistoryModeNoGo ||
		input.Lineage.SessionID != o.Evidence.SessionID || input.Lineage.HistoryAvailabilityBindingID != bindingID ||
		input.Lineage.HistoryAvailabilityBindingSHA256 != bindingID || input.Lineage.Config != ConfigArtifact(config) ||
		input.Lineage.Rules != RulesArtifact() {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "invalid_live_only_binding")}
	}
	if config.MaximumLiveAgeMS > 0 && input.PolicyTimeMS-o.Evidence.ReceiveTime.UnixMilli() > config.MaximumLiveAgeMS {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "stale_live_input")}
	}
	if reason := unsafeReason(o); reason != "" {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", reason)}
	}
	results := []contracts.InsightCandidateV1{objectiveCandidate(o, input.Previous, config, input.PolicyTimeMS)}
	Sort(results)
	return results
}

func Family(ruleVersion string) string {
	if i := strings.IndexByte(ruleVersion, '.'); i >= 0 {
		return ruleVersion[:i]
	}
	return ruleVersion
}

func Evaluate(input Input, config Config) []contracts.InsightCandidateV1 {
	config = normalizeConfig(config)
	o := input.Observation
	if config.MaximumLiveAgeMS > 0 && input.PolicyTimeMS-o.Evidence.ReceiveTime.UnixMilli() > config.MaximumLiveAgeMS {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "stale_live_input")}
	}
	if reason := unsafeReason(o); reason != "" {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", reason)}
	}
	if input.Lineage != nil && (input.Lineage.Config != ConfigArtifact(config) || input.Lineage.Rules != RulesArtifact()) {
		return []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "lineage_artifact_mismatch")}
	}
	results := make([]contracts.InsightCandidateV1, 0, 5)
	results = append(results, historyCandidate(o, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS, DraftRule, "draft_hero_performance", config.DraftMinimum, 50))
	minuteMetric := ""
	if observed(o.Map.ClockTime) && *o.Map.ClockTime.Value == 600 {
		minuteMetric = "lane_10_net_worth"
	}
	if observed(o.Map.ClockTime) && *o.Map.ClockTime.Value == 900 {
		minuteMetric = "lane_15_net_worth"
	}
	if minuteMetric == "" {
		c := base(o, config, input.PolicyTimeMS, LaneRule, 70)
		c.LocalizationKey = "insight.lane"
		c.Availability, c.Reason = "suppressed", "not_lane_checkpoint"
		seal(&c)
		results = append(results, c)
	} else {
		results = append(results, historyCandidate(o, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS, LaneRule, minuteMetric, config.DistributionMinimum, 70))
	}
	results = append(results, itemCandidate(o, input.Previous, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS))
	results = append(results, objectiveCandidate(o, input.Previous, config, input.PolicyTimeMS))
	results = append(results, readinessCandidate(o, input.Manifest, input.Lineage, input.Baselines, config, input.PolicyTimeMS))
	Sort(results)
	return results
}

func Sort(values []contracts.InsightCandidateV1) {
	contracts.SortInsightCandidates(values)
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
	c.CandidateID, _ = contracts.InsightCandidateContentID(*c)
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
	netWorthChanged := false
	for i := range current.Participants {
		if compareDecimal(*current.Participants[i].NetWorth.Value, *previous.Participants[i].NetWorth.Value) != 0 {
			netWorthChanged = true
			break
		}
	}
	return *current.Map.RadiantScore.Value != *previous.Map.RadiantScore.Value || *current.Map.DireScore.Value != *previous.Map.DireScore.Value || buildingChanged(current.Buildings, previous.Buildings) || *current.Roshan.State.Value != *previous.Roshan.State.Value || *current.Tormentor.State.Value != *previous.Tormentor.State.Value || netWorthChanged, true
}

func objectiveMetrics(current, previous contracts.LiveObservationV1) []contracts.ObservedMetricV1 {
	radiant, dire := new(big.Rat), new(big.Rat)
	for i := range current.Participants {
		delta := new(big.Rat).Sub(decimalRat(*current.Participants[i].NetWorth.Value), decimalRat(*previous.Participants[i].NetWorth.Value))
		if current.Participants[i].TeamKey == "radiant" {
			radiant.Add(radiant, delta)
		} else if current.Participants[i].TeamKey == "dire" {
			dire.Add(dire, delta)
		}
	}
	return []contracts.ObservedMetricV1{
		{Name: "radiant_kill_delta", Value: contracts.Decimal(strconv.FormatInt(*current.Map.RadiantScore.Value-*previous.Map.RadiantScore.Value, 10)), Unit: "kills"},
		{Name: "dire_kill_delta", Value: contracts.Decimal(strconv.FormatInt(*current.Map.DireScore.Value-*previous.Map.DireScore.Value, 10)), Unit: "kills"},
		{Name: "radiant_net_worth_delta", Value: ratDecimal(radiant), Unit: "gold"},
		{Name: "dire_net_worth_delta", Value: ratDecimal(dire), Unit: "gold"},
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
		dead := int64(0)
		respawn, cooldown, health, mana := new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)
		for _, participant := range o.Participants {
			health.Add(health, decimalRat(*participant.HealthPercent.Value))
			mana.Add(mana, decimalRat(*participant.ManaPercent.Value))
			if !*participant.Alive.Value {
				dead++
				respawn.Add(respawn, decimalRat(*participant.RespawnSeconds.Value))
			}
			for _, ability := range participant.Abilities {
				cooldown.Add(cooldown, decimalRat(*ability.Cooldown.Value))
			}
			for _, item := range participant.Items {
				cooldown.Add(cooldown, decimalRat(*item.Cooldown.Value))
			}
		}
		return []contracts.ObservedMetricV1{{Name: "dead_players", Value: contracts.Decimal(strconv.FormatInt(dead, 10)), Unit: "players"}, {Name: "respawn_seconds", Value: ratDecimal(respawn), Unit: "seconds"}, {Name: "cooldown_seconds", Value: ratDecimal(cooldown), Unit: "seconds"}, {Name: "mean_health_percent", Value: ratDecimal(new(big.Rat).Quo(health, big.NewRat(10, 1))), Unit: "percent"}, {Name: "mean_mana_percent", Value: ratDecimal(new(big.Rat).Quo(mana, big.NewRat(10, 1))), Unit: "percent"}}, nil, true
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
func decimalRat(v contracts.Decimal) *big.Rat {
	n, ok := new(big.Rat).SetString(string(v))
	if !ok {
		return new(big.Rat)
	}
	return n
}
func compareDecimal(a, b contracts.Decimal) int { return decimalRat(a).Cmp(decimalRat(b)) }
func ratDecimal(v *big.Rat) contracts.Decimal {
	s := v.FloatString(9)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		s = "0"
	}
	return contracts.Decimal(s)
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
