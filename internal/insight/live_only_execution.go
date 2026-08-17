package insight

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

// HistoricalUnavailableObserver receives value-free execution evidence for
// every immutable historical family disabled by a live-only evaluation.
type HistoricalUnavailableObserver interface {
	HistoricalUnavailable(sequence uint64, rawPayloadSHA256, family string)
}

// LiveOnlyExecutionInput is the production-shaped executable adapter for an
// accepted live-only evaluation. It deliberately carries no historical value,
// snapshot, replay, or fallback input.
type LiveOnlyExecutionInput struct {
	LiveOnlyInput
	HistoricalUnavailableObserver HistoricalUnavailableObserver
}

// ExecuteLiveOnly evaluates the accepted production engine first, then records
// the typed historical-unavailable branches bound to that same observation.
// Invalid bindings never produce branch-execution evidence.
func ExecuteLiveOnly(input LiveOnlyExecutionInput, config Config) []contracts.InsightCandidateV1 {
	candidates := EvaluateLiveOnly(input.LiveOnlyInput, config)
	if input.HistoricalUnavailableObserver == nil || !validLiveOnlyBinding(input.LiveOnlyInput, config) {
		return candidates
	}
	for _, family := range input.History.DisabledFamilies {
		input.HistoricalUnavailableObserver.HistoricalUnavailable(
			input.Observation.Evidence.Sequence,
			input.Observation.Evidence.RawPayloadSHA256,
			family,
		)
	}
	return candidates
}

func validLiveOnlyBinding(input LiveOnlyInput, config Config) bool {
	bindingID, bindingErr := input.History.ContentID()
	_, lineageErr := input.Lineage.ContentID()
	return bindingErr == nil && lineageErr == nil &&
		input.History.Mode == contracts.HistoryModeNoGo &&
		input.Lineage.SessionID == input.Observation.Evidence.SessionID &&
		input.Lineage.HistoryAvailabilityBindingID == bindingID &&
		input.Lineage.HistoryAvailabilityBindingSHA256 == bindingID &&
		input.Lineage.Config == ConfigArtifact(normalizeConfig(config)) &&
		input.Lineage.Rules == RulesArtifact()
}
