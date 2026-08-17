package insight

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

type liveOnlyExecutionError string

func (e liveOnlyExecutionError) Error() string { return string(e) }

// HistoricalUnavailableExecution is emitted synchronously by one actual
// production eligibleBaseline branch. It contains no historical value or
// identity fallback.
type HistoricalUnavailableExecution struct {
	Family   string
	Evidence contracts.EvidenceRefV1
	Reason   string
}

type HistoricalUnavailableSink interface {
	HistoricalUnavailable(HistoricalUnavailableExecution) error
}

// EvaluateLiveOnlyProductionBranches executes the accepted live-only evaluator
// and each of the eight immutable history-dependent production branches. Each
// sink call occurs inside executeUnavailableProductionBranch, immediately after
// that branch returns its typed invalid-history result; no registry walk
// after EvaluateLiveOnly can manufacture executions.
func EvaluateLiveOnlyProductionBranches(input LiveOnlyInput, config Config, sink HistoricalUnavailableSink) ([]contracts.InsightCandidateV1, error) {
	visible := EvaluateLiveOnly(input, config)
	if sink == nil || !validLiveOnlyExecutionBinding(input, config) {
		return visible, liveOnlyExecutionError("invalid live-only execution binding")
	}
	config = normalizeConfig(config)
	now := input.PolicyTimeMS
	o := input.Observation
	if err := executeUnavailableProductionBranch(sink, o, config, now, "hero", "draft_hero_performance", config.DraftMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "item", "item_timing_ms", config.DistributionMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "lane", "lane_10_net_worth", config.DistributionMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "patch", "patch_distribution", config.DistributionMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "player", "player_performance", config.DraftMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "player_hero", "player_hero_performance", config.DraftMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "role", "role_readiness", config.TeamMinimum); err != nil {
		return visible, err
	}
	if err := executeUnavailableProductionBranch(sink, o, config, now, "team", "teamfight_readiness", config.TeamMinimum); err != nil {
		return visible, err
	}
	return visible, nil
}

func executeUnavailableProductionBranch(sink HistoricalUnavailableSink, observation contracts.LiveObservationV1, config Config, now int64, family, metric string, minimum uint64) error {
	_, reason := eligibleBaseline(observation, nil, nil, nil, config, now, metric, minimum)
	if reason != "invalid_history_binding" {
		return liveOnlyExecutionError("history-dependent production branch did not fail closed")
	}
	return sink.HistoricalUnavailable(HistoricalUnavailableExecution{Family: family, Evidence: observation.Evidence, Reason: reason})
}

func validLiveOnlyExecutionBinding(input LiveOnlyInput, config Config) bool {
	bindingID, bindingErr := input.History.ContentID()
	_, lineageErr := input.Lineage.ContentID()
	return bindingErr == nil && lineageErr == nil && input.History.Mode == contracts.HistoryModeNoGo &&
		input.Lineage.SessionID == input.Observation.Evidence.SessionID && input.Lineage.HistoryAvailabilityBindingID == bindingID &&
		input.Lineage.HistoryAvailabilityBindingSHA256 == bindingID && input.Lineage.Config == ConfigArtifact(normalizeConfig(config)) &&
		input.Lineage.Rules == RulesArtifact()
}
