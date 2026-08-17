package insight

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

type liveOnlyExecutionError string

func (err liveOnlyExecutionError) Error() string { return string(err) }

// HistoricalUnavailableExecution is the value-free result of an eligibility
// site reached by the production live-only evaluator. The evidence reference
// is the only observation identity carried across the package boundary.
type HistoricalUnavailableExecution struct {
	Family   string
	Evidence contracts.EvidenceRefV1
	Reason   string
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
	result := LiveOnlyProductionResult{Suppressions: make([]HistoricalUnavailableExecution, 0, len(input.History.DisabledFamilies))}
	for _, family := range input.History.DisabledFamilies {
		execution, err := historicalUnavailableEligibility(input, family)
		if err != nil {
			return LiveOnlyProductionResult{}, err
		}
		result.Suppressions = append(result.Suppressions, execution)
	}
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
func historicalUnavailableEligibility(input LiveOnlyInput, family string) (HistoricalUnavailableExecution, error) {
	if input.History.Mode != contracts.HistoryModeNoGo {
		return HistoricalUnavailableExecution{}, liveOnlyExecutionError("history is not typed unavailable")
	}
	found := false
	for _, disabled := range input.History.DisabledFamilies {
		if disabled == family {
			if found {
				return HistoricalUnavailableExecution{}, liveOnlyExecutionError("duplicate history family eligibility")
			}
			found = true
		}
	}
	if !found {
		return HistoricalUnavailableExecution{}, liveOnlyExecutionError("history family is not disabled by immutable binding")
	}
	return HistoricalUnavailableExecution{Family: family, Evidence: input.Observation.Evidence, Reason: "historical_unavailable"}, nil
}

func validLiveOnlyExecutionBinding(input LiveOnlyInput, config Config) bool {
	bindingID, bindingErr := input.History.ContentID()
	_, lineageErr := input.Lineage.ContentID()
	return bindingErr == nil && lineageErr == nil && input.History.Mode == contracts.HistoryModeNoGo &&
		input.Lineage.SessionID == input.Observation.Evidence.SessionID && input.Lineage.HistoryAvailabilityBindingID == bindingID &&
		input.Lineage.HistoryAvailabilityBindingSHA256 == bindingID && input.Lineage.Config == ConfigArtifact(normalizeConfig(config)) &&
		input.Lineage.Rules == RulesArtifact()
}
