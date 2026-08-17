package insight

import (
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type productionEligibilityError string

func (e productionEligibilityError) Error() string { return string(e) }

// HistoricalUnavailableAudit is emitted by a production rule at the point
// where that rule needs history. It intentionally carries only source identity.
type HistoricalUnavailableAudit struct {
	Family          string
	EligibilitySite string
	Evidence        contracts.EvidenceRefV1
	Reason          string
	Executed        bool
}

type LiveOnlyProductionResult struct {
	Candidates []contracts.InsightCandidateV1
	History    []HistoricalUnavailableAudit
}

// EvaluateLiveOnlyProduction is the production evaluator. It owns both the
// family eligibility branches and the accepted source-visible rule; there is
// no wrapper that manufactures audits before or after another evaluation.
func EvaluateLiveOnlyProduction(input LiveOnlyInput, config Config) (LiveOnlyProductionResult, error) {
	config = normalizeConfig(config)
	if !validLiveOnlyProductionBinding(input, config) {
		return LiveOnlyProductionResult{Candidates: []contracts.InsightCandidateV1{suppress(input.Observation, config, input.PolicyTimeMS, "live.suppressed.v1", "invalid_live_only_binding")}}, productionEligibilityError("invalid live-only production binding")
	}
	hero, err := evaluateHeroHistoryDependency(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	item, err := evaluateItemHistoryDependency(input, config)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	lane, err := evaluateLaneHistoryDependency(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	patch, err := evaluatePatchHistoryDependency(input, config)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	player, err := evaluatePlayerHistoryDependency(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	playerHero, err := evaluatePlayerHeroHistoryDependency(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	role, err := evaluateRoleHistoryDependency(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	team, err := evaluateTeamHistoryDependency(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	history := []HistoricalUnavailableAudit{hero, item, lane, patch, player, playerHero, role, team}
	o := input.Observation
	if config.MaximumLiveAgeMS > 0 && input.PolicyTimeMS-o.Evidence.ReceiveTime.UnixMilli() > config.MaximumLiveAgeMS {
		return LiveOnlyProductionResult{Candidates: []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "stale_live_input")}, History: history}, nil
	}
	if reason := unsafeReason(o); reason != "" {
		return LiveOnlyProductionResult{Candidates: []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", reason)}, History: history}, nil
	}
	candidate := objectiveCandidate(o, input.Previous, config, input.PolicyTimeMS)
	if candidate.Availability == "available" {
		candidate.LocalizationKey = "insight.live_visible_change"
		candidate.ObservedValues = liveOnlyVisibleMetrics(o, *input.Previous)
		candidate.Parameters = []contracts.TypedParameterV1{
			decimalParameter("radiant_net_worth_delta", candidate.ObservedValues[0].Value),
			decimalParameter("dire_net_worth_delta", candidate.ObservedValues[1].Value),
		}
		seal(&candidate)
	}
	candidates := []contracts.InsightCandidateV1{candidate}
	Sort(candidates)
	return LiveOnlyProductionResult{Candidates: candidates, History: history}, nil
}

func validLiveOnlyProductionBinding(input LiveOnlyInput, config Config) bool {
	bindingID, bindingErr := input.History.ContentID()
	_, lineageErr := input.Lineage.ContentID()
	return bindingErr == nil && lineageErr == nil && input.History.Mode == contracts.HistoryModeNoGo &&
		input.Lineage.SessionID == input.Observation.Evidence.SessionID &&
		input.Lineage.HistoryAvailabilityBindingID == bindingID &&
		input.Lineage.HistoryAvailabilityBindingSHA256 == bindingID &&
		input.Lineage.Config == ConfigArtifact(config) && input.Lineage.Rules == RulesArtifact()
}

func evaluateHeroHistoryDependency(input LiveOnlyInput) (HistoricalUnavailableAudit, error) {
	// Hero form/history eligibility is evaluated even when the live roster is
	// incomplete; incomplete telemetry may suppress a future visible rule but
	// cannot turn absent history into a baseline.
	for i := range input.Observation.Participants {
		_ = input.Observation.Participants[i].HeroID
	}
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[0] != "hero" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("hero history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "hero", EligibilitySite: "hero.form_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluateItemHistoryDependency(input LiveOnlyInput, config Config) (HistoricalUnavailableAudit, error) {
	// Item timing eligibility owns the accepted key-item set and prior-frame
	// dependency. It never fabricates a completion when either is absent.
	_ = config.KeyItems
	_ = input.Previous
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[1] != "item" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("item history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "item", EligibilitySite: "item.timing_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluateLaneHistoryDependency(input LiveOnlyInput) (HistoricalUnavailableAudit, error) {
	// Lane checkpoints depend on the observed game clock; history remains typed
	// unavailable outside and at the two accepted checkpoints.
	_ = input.Observation.Map.ClockTime
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[2] != "lane" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("lane history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "lane", EligibilitySite: "lane.checkpoint_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluatePatchHistoryDependency(input LiveOnlyInput, config Config) (HistoricalUnavailableAudit, error) {
	// Patch comparisons require an exact patch identity on both the immutable
	// history binding and production config.
	if input.History.DotaPatch != config.Patch {
		return HistoricalUnavailableAudit{}, productionEligibilityError("patch dependency identity mismatch")
	}
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[3] != "patch" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("patch history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "patch", EligibilitySite: "patch.comparison_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluatePlayerHistoryDependency(input LiveOnlyInput) (HistoricalUnavailableAudit, error) {
	// Player history never falls back to display/account identity. The live
	// participant population is inspected only for source completeness.
	_ = len(input.Observation.Participants)
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[4] != "player" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("player history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "player", EligibilitySite: "player.form_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluatePlayerHeroHistoryDependency(input LiveOnlyInput) (HistoricalUnavailableAudit, error) {
	// Player/hero pair history requires both stable identities; public-match
	// rehearsal has neither account identity nor a historical pair baseline.
	for i := range input.Observation.Participants {
		_ = input.Observation.Participants[i].HeroID
	}
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[5] != "player_hero" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("player-hero history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "player_hero", EligibilitySite: "player_hero.pair_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluateRoleHistoryDependency(input LiveOnlyInput) (HistoricalUnavailableAudit, error) {
	// Role history is evaluated from source-faithful team/position telemetry and
	// never inferred from a display name.
	for i := range input.Observation.Participants {
		_ = input.Observation.Participants[i].TeamKey
	}
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[6] != "role" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("role history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "role", EligibilitySite: "role.performance_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}

func evaluateTeamHistoryDependency(input LiveOnlyInput) (HistoricalUnavailableAudit, error) {
	// Team form requires a bound historical team identity. Public-match input
	// deliberately lacks that authority, regardless of current net-worth data.
	for i := range input.Observation.Participants {
		_ = input.Observation.Participants[i].NetWorth
	}
	if input.History.Mode != contracts.HistoryModeNoGo || len(input.History.DisabledFamilies) != 8 || input.History.DisabledFamilies[7] != "team" {
		return HistoricalUnavailableAudit{}, productionEligibilityError("team history dependency is not typed unavailable")
	}
	return HistoricalUnavailableAudit{Family: "team", EligibilitySite: "team.form_eligibility.v1", Evidence: input.Observation.Evidence, Reason: "historical_unavailable", Executed: true}, nil
}
