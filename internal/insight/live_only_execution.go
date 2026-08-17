package insight

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

type liveOnlyExecutionError string

func (err liveOnlyExecutionError) Error() string { return string(err) }

// HistoricalUnavailableExecution is the value-free result of an eligibility
// site reached by the production live-only evaluator. The evidence reference
// is the only observation identity carried across the package boundary.
type HistoricalUnavailableExecution struct {
	Family          string
	EligibilitySite string
	Evidence        contracts.EvidenceRefV1
	Reason          string
}

// LiveOnlyProductionResult keeps visible candidates and history suppression
// separate. Suppression is returned by the evaluator; it is not reconstructed
// by an audit wrapper.
type LiveOnlyProductionResult struct {
	Candidates   []contracts.InsightCandidateV1
	Suppressions []HistoricalUnavailableExecution
}

// EvaluateLiveOnlyProduction is the production-shaped live-only evaluation.
// Every family in the already-validated immutable V3 history binding reaches
// its closed eligibility site before visible non-history rules are evaluated.
func EvaluateLiveOnlyProduction(input LiveOnlyInput, config Config) (LiveOnlyProductionResult, error) {
	config = normalizeConfig(config)
	if !validLiveOnlyExecutionBinding(input, config) {
		return LiveOnlyProductionResult{Candidates: []contracts.InsightCandidateV1{suppress(input.Observation, config, input.PolicyTimeMS, "live.suppressed.v1", "invalid_live_only_binding")}}, liveOnlyExecutionError("invalid live-only execution binding")
	}
	result := LiveOnlyProductionResult{Suppressions: make([]HistoricalUnavailableExecution, 0, 8)}
	hero, err := heroHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	item, err := itemHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	lane, err := laneHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	patch, err := patchHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	player, err := playerHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	playerHero, err := playerHeroHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	role, err := roleHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	team, err := teamHistoricalEligibility(input)
	if err != nil {
		return LiveOnlyProductionResult{}, err
	}
	result.Suppressions = append(result.Suppressions, hero, item, lane, patch, player, playerHero, role, team)
	o := input.Observation
	if config.MaximumLiveAgeMS > 0 && input.PolicyTimeMS-o.Evidence.ReceiveTime.UnixMilli() > config.MaximumLiveAgeMS {
		result.Candidates = []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", "stale_live_input")}
		return result, nil
	}
	if reason := unsafeReason(o); reason != "" {
		result.Candidates = []contracts.InsightCandidateV1{suppress(o, config, input.PolicyTimeMS, "live.suppressed.v1", reason)}
		return result, nil
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
	result.Candidates = []contracts.InsightCandidateV1{candidate}
	Sort(result.Candidates)
	return result, nil
}

// historicalUnavailableEligibility is the actual production eligibility site
// for a V3 family. It consumes the typed no-go binding directly; it never calls
// the historical baseline evaluator with fabricated nil inputs.
func closedHistoricalEligibility(input LiveOnlyInput, family, site string) (HistoricalUnavailableExecution, error) {
	if input.History.Mode != contracts.HistoryModeNoGo {
		return HistoricalUnavailableExecution{}, liveOnlyExecutionError("history is not typed unavailable")
	}
	return HistoricalUnavailableExecution{Family: family, EligibilitySite: site, Evidence: input.Observation.Evidence, Reason: "historical_unavailable"}, nil
}

// These are deliberately separate production eligibility sites. Each family
// owns the branch that suppresses its unavailable historical input; no registry
// walk can manufacture branch execution after evaluation.
func heroHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "hero", "hero.history_unavailable.v1")
}
func itemHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "item", "item.history_unavailable.v1")
}
func laneHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "lane", "lane.history_unavailable.v1")
}
func patchHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "patch", "patch.history_unavailable.v1")
}
func playerHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "player", "player.history_unavailable.v1")
}
func playerHeroHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "player_hero", "player_hero.history_unavailable.v1")
}
func roleHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "role", "role.history_unavailable.v1")
}
func teamHistoricalEligibility(input LiveOnlyInput) (HistoricalUnavailableExecution, error) {
	return closedHistoricalEligibility(input, "team", "team.history_unavailable.v1")
}

func validLiveOnlyExecutionBinding(input LiveOnlyInput, config Config) bool {
	bindingID, bindingErr := input.History.ContentID()
	_, lineageErr := input.Lineage.ContentID()
	return bindingErr == nil && lineageErr == nil && input.History.Mode == contracts.HistoryModeNoGo &&
		input.Lineage.SessionID == input.Observation.Evidence.SessionID && input.Lineage.HistoryAvailabilityBindingID == bindingID &&
		input.Lineage.HistoryAvailabilityBindingSHA256 == bindingID && input.Lineage.Config == ConfigArtifact(normalizeConfig(config)) &&
		input.Lineage.Rules == RulesArtifact()
}
