package insight

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

const (
	LiveOnlyEvaluationSchemaV2 = "live_only_evaluation.v2"
	FamilyExecutionSchemaV1    = "family_site_execution.v1"
	FamilySuppressionSchemaV1  = "family_suppression_audit.v1"
	FamilySitesNotEvaluated    = "family_sites_not_evaluated"
	HistoricalUnavailable      = "historical_unavailable"
	MaxFamilySuppressionAudits = 8
)

type liveOnlyValidationError string

func (e liveOnlyValidationError) Error() string { return string(e) }

// Each dependency is a distinct closed product input. None has exported
// fields, so adapters, commands, and rehearsal code cannot assert eligibility.
// Package tests may perturb one value to prove the corresponding production
// site is causal.
type familyDependency struct {
	mode      string
	bindingID string
}

type HeroHistoryAvailabilityV1 struct{ dependency familyDependency }
type ItemHistoryAvailabilityV1 struct{ dependency familyDependency }
type LaneHistoryAvailabilityV1 struct{ dependency familyDependency }
type PatchHistoryAvailabilityV1 struct{ dependency familyDependency }
type PlayerHistoryAvailabilityV1 struct{ dependency familyDependency }
type PlayerHeroHistoryAvailabilityV1 struct{ dependency familyDependency }
type RoleHistoryAvailabilityV1 struct{ dependency familyDependency }
type TeamHistoryAvailabilityV1 struct{ dependency familyDependency }

type LiveOnlyAvailabilityV1 struct {
	hero       HeroHistoryAvailabilityV1
	item       ItemHistoryAvailabilityV1
	lane       LaneHistoryAvailabilityV1
	patch      PatchHistoryAvailabilityV1
	player     PlayerHistoryAvailabilityV1
	playerHero PlayerHeroHistoryAvailabilityV1
	role       RoleHistoryAvailabilityV1
	team       TeamHistoryAvailabilityV1
	bindingID  string
}

// ProductLiveOnlyAvailabilityV1 is the only non-test constructor. Validation
// of HistoryAvailabilityBindingV1 proves the immutable eight-family registry.
func ProductLiveOnlyAvailabilityV1(history contracts.HistoryAvailabilityBindingV1) (LiveOnlyAvailabilityV1, error) {
	if err := history.Validate(); err != nil || history.Mode != contracts.HistoryModeNoGo {
		return LiveOnlyAvailabilityV1{}, liveOnlyValidationError("live-only history availability is not the accepted no-go binding")
	}
	bindingID, err := history.ContentID()
	if err != nil {
		return LiveOnlyAvailabilityV1{}, err
	}
	dependency := familyDependency{mode: HistoricalUnavailable, bindingID: bindingID}
	return LiveOnlyAvailabilityV1{
		hero: HeroHistoryAvailabilityV1{dependency}, item: ItemHistoryAvailabilityV1{dependency},
		lane: LaneHistoryAvailabilityV1{dependency}, patch: PatchHistoryAvailabilityV1{dependency},
		player: PlayerHistoryAvailabilityV1{dependency}, playerHero: PlayerHeroHistoryAvailabilityV1{dependency},
		role: RoleHistoryAvailabilityV1{dependency}, team: TeamHistoryAvailabilityV1{dependency}, bindingID: bindingID,
	}, nil
}

type FamilySiteExecutionV1 struct {
	SchemaVersion         string `json:"schema_version"`
	SiteVersion           string `json:"site_version"`
	Family                string `json:"family"`
	SessionID             string `json:"session_id"`
	RawSequence           uint64 `json:"raw_sequence"`
	RawRecordSHA256       string `json:"raw_record_sha256"`
	HistoryBindingSHA256  string `json:"history_binding_sha256"`
	DependencyInputSHA256 string `json:"dependency_input_sha256"`
	Branch                string `json:"branch"`
}

type FamilySuppressionAuditV1 struct {
	SchemaVersion           string `json:"schema_version"`
	SiteVersion             string `json:"site_version"`
	Family                  string `json:"family"`
	Reason                  string `json:"reason"`
	SessionID               string `json:"session_id"`
	RawSequence             uint64 `json:"raw_sequence"`
	RawRecordSHA256         string `json:"raw_record_sha256"`
	HistoryBindingSHA256    string `json:"history_binding_sha256"`
	ExecutionSHA256         string `json:"execution_sha256"`
	CandidateEmitted        bool   `json:"candidate_emitted"`
	DecisionEmitted         bool   `json:"decision_emitted"`
	OverlayOrDisplayEmitted bool   `json:"overlay_or_display_emitted"`
}

type LiveOnlyEvaluationV2 struct {
	SchemaVersion  string                         `json:"schema_version"`
	Candidates     []contracts.InsightCandidateV1 `json:"candidates"`
	Executions     []FamilySiteExecutionV1        `json:"executions"`
	Audits         []FamilySuppressionAuditV1     `json:"audits"`
	TerminalReason string                         `json:"terminal_reason,omitempty"`
}

type LiveOnlyInputV2 struct {
	Observation     contracts.LiveObservationV1
	Previous        *contracts.LiveObservationV1
	History         contracts.HistoryAvailabilityBindingV1
	Lineage         contracts.PolicyLineageManifestV3
	Availability    LiveOnlyAvailabilityV1
	RawRecordSHA256 string
	PolicyTimeMS    int64
}

type liveOnlyProductionEvaluation struct {
	input  LiveOnlyInputV2
	config Config
	result LiveOnlyEvaluationV2
}

// EvaluateLiveOnlyV2 is the ordinary production evaluator. Eligibility is
// evaluated before the visible objective rule, at eight separate family-owned
// sites. There is no post-evaluation audit pass and no registry iteration.
func EvaluateLiveOnlyV2(input LiveOnlyInputV2, config Config) LiveOnlyEvaluationV2 {
	config = normalizeConfig(config)
	legacy := LiveOnlyInput{Observation: input.Observation, Previous: input.Previous, History: input.History, Lineage: input.Lineage, PolicyTimeMS: input.PolicyTimeMS}
	global := EvaluateLiveOnly(legacy, config)
	result := LiveOnlyEvaluationV2{SchemaVersion: LiveOnlyEvaluationSchemaV2}
	if globalLiveOnlyFailure(global) || !inputAvailabilityMatches(input) {
		result.Candidates = global
		result.TerminalReason = FamilySitesNotEvaluated
		return result
	}

	evaluation := liveOnlyProductionEvaluation{input: input, config: config, result: result}
	if err := evaluation.evaluateHeroHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluateItemHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluateLaneHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluatePatchHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluatePlayerHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluatePlayerHeroHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluateRoleHistory(); err != nil {
		return evaluation.closedFailure(err)
	}
	if err := evaluation.evaluateTeamHistory(); err != nil {
		return evaluation.closedFailure(err)
	}

	// The visible, non-history rule runs only after every historical dependency
	// branch has closed. Its bytes remain those of the accepted evaluator.
	evaluation.result.Candidates = global
	return evaluation.result
}

func (e *liveOnlyProductionEvaluation) closedFailure(_ error) LiveOnlyEvaluationV2 {
	legacy := LiveOnlyInput{Observation: e.input.Observation, Previous: e.input.Previous, History: e.input.History, Lineage: e.input.Lineage, PolicyTimeMS: e.input.PolicyTimeMS}
	e.result.Candidates = EvaluateLiveOnly(legacy, e.config)
	e.result.Executions = nil
	e.result.Audits = nil
	e.result.TerminalReason = FamilySitesNotEvaluated
	return e.result
}

func globalLiveOnlyFailure(candidates []contracts.InsightCandidateV1) bool {
	if len(candidates) != 1 || candidates[0].RuleVersion != "live.suppressed.v1" {
		return false
	}
	switch candidates[0].Reason {
	case "invalid_live_only_binding", "stale_live_input", "inconsistent_live_input", "paused_live_input", "unsafe_live_input", "out_of_order_live_input":
		return true
	default:
		return false
	}
}

func inputAvailabilityMatches(input LiveOnlyInputV2) bool {
	bindingID, err := input.History.ContentID()
	return err == nil && bindingID == input.Availability.bindingID && validLiveOnlySHA256(input.RawRecordSHA256)
}

func allFamilyDependenciesValid(input LiveOnlyInputV2) bool {
	binding := input.Availability.bindingID
	values := []familyDependency{input.Availability.hero.dependency, input.Availability.item.dependency, input.Availability.lane.dependency, input.Availability.patch.dependency, input.Availability.player.dependency, input.Availability.playerHero.dependency, input.Availability.role.dependency, input.Availability.team.dependency}
	for _, value := range values {
		if validateDependency(value, binding) != nil {
			return false
		}
	}
	return true
}

func dependencyInputSHA(site, binding string, values ...any) (string, error) {
	return contracts.CanonicalSHA256(struct {
		Site    string `json:"site"`
		Binding string `json:"binding"`
		Values  []any  `json:"values"`
	}{site, binding, values})
}

func executionSHA(execution FamilySiteExecutionV1) (string, error) {
	return contracts.CanonicalSHA256(execution)
}

func (e *liveOnlyProductionEvaluation) appendSite(execution FamilySiteExecutionV1, audit FamilySuppressionAuditV1) {
	e.result.Executions = append(e.result.Executions, execution)
	e.result.Audits = append(e.result.Audits, audit)
}

func validateDependency(value familyDependency, binding string) error {
	if value.mode != HistoricalUnavailable || value.bindingID != binding || !validLiveOnlySHA256(binding) {
		return liveOnlyValidationError("family dependency is not product-derived historical-unavailable")
	}
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluateHeroHistory() error {
	dependency := e.input.Availability.hero.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	present := 0
	for _, participant := range e.input.Observation.Participants {
		if participant.HeroID.State == contracts.ValuePresent || participant.HeroName.State == contracts.ValuePresent {
			present++
		}
	}
	inputSHA, err := dependencyInputSHA("hero.history_eligibility.v2", dependency.bindingID, present, len(e.input.Observation.Participants))
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "hero.history_eligibility.v2", "hero", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluateItemHistory() error {
	dependency := e.input.Availability.item.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	items := 0
	for _, participant := range e.input.Observation.Participants {
		items += len(participant.Items)
	}
	inputSHA, err := dependencyInputSHA("item.history_eligibility.v2", dependency.bindingID, items, len(e.config.KeyItems))
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "item.history_eligibility.v2", "item", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluateLaneHistory() error {
	dependency := e.input.Availability.lane.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	inputSHA, err := dependencyInputSHA("lane.history_eligibility.v2", dependency.bindingID, e.input.Observation.Map.ClockTime.State, e.input.Observation.ClockBasis)
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "lane.history_eligibility.v2", "lane", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluatePatchHistory() error {
	dependency := e.input.Availability.patch.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	inputSHA, err := dependencyInputSHA("patch.history_eligibility.v2", dependency.bindingID, e.input.Observation.Evidence.ProviderVersion.State, e.config.Patch)
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "patch.history_eligibility.v2", "patch", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluatePlayerHistory() error {
	dependency := e.input.Availability.player.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	verified := 0
	for _, participant := range e.input.Observation.Participants {
		if participant.VerifiedIdentity != nil {
			verified++
		}
	}
	inputSHA, err := dependencyInputSHA("player.history_eligibility.v2", dependency.bindingID, verified, len(e.input.Observation.Participants))
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "player.history_eligibility.v2", "player", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluatePlayerHeroHistory() error {
	dependency := e.input.Availability.playerHero.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	pairs := 0
	for _, participant := range e.input.Observation.Participants {
		if participant.VerifiedIdentity != nil && participant.HeroID.State == contracts.ValuePresent {
			pairs++
		}
	}
	inputSHA, err := dependencyInputSHA("player_hero.history_eligibility.v2", dependency.bindingID, pairs)
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "player_hero.history_eligibility.v2", "player_hero", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluateRoleHistory() error {
	dependency := e.input.Availability.role.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	positions := 0
	for _, participant := range e.input.Observation.Participants {
		if participant.XPos.State == contracts.ValuePresent && participant.YPos.State == contracts.ValuePresent {
			positions++
		}
	}
	inputSHA, err := dependencyInputSHA("role.history_eligibility.v2", dependency.bindingID, positions)
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "role.history_eligibility.v2", "role", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

func (e *liveOnlyProductionEvaluation) evaluateTeamHistory() error {
	dependency := e.input.Availability.team.dependency
	if err := validateDependency(dependency, e.input.Availability.bindingID); err != nil {
		return err
	}
	teams := map[string]struct{}{}
	for _, participant := range e.input.Observation.Participants {
		if participant.TeamKey != "" {
			teams[participant.TeamKey] = struct{}{}
		}
	}
	inputSHA, err := dependencyInputSHA("team.history_eligibility.v2", dependency.bindingID, len(teams), len(e.input.Observation.Buildings))
	if err != nil {
		return err
	}
	execution := FamilySiteExecutionV1{FamilyExecutionSchemaV1, "team.history_eligibility.v2", "team", e.input.Observation.Evidence.SessionID, e.input.Observation.Evidence.Sequence, e.input.RawRecordSHA256, dependency.bindingID, inputSHA, HistoricalUnavailable}
	hash, err := executionSHA(execution)
	if err != nil {
		return err
	}
	audit := FamilySuppressionAuditV1{FamilySuppressionSchemaV1, execution.SiteVersion, execution.Family, HistoricalUnavailable, execution.SessionID, execution.RawSequence, execution.RawRecordSHA256, execution.HistoryBindingSHA256, hash, false, false, false}
	e.appendSite(execution, audit)
	return nil
}

// ValidateLiveOnlyEvaluationV2 is the publication barrier. The immutable
// registry defines the expected order, while independently hashed execution
// records prove the corresponding site ran before its audit was emitted.
func ValidateLiveOnlyEvaluationV2(result LiveOnlyEvaluationV2, input LiveOnlyInputV2) error {
	if result.SchemaVersion != LiveOnlyEvaluationSchemaV2 || len(result.Candidates) == 0 {
		return liveOnlyValidationError("invalid live-only evaluation envelope")
	}
	if result.TerminalReason != "" {
		if result.TerminalReason != FamilySitesNotEvaluated || len(result.Audits) != 0 || len(result.Executions) != 0 || (!globalLiveOnlyFailure(result.Candidates) && inputAvailabilityMatches(input) && allFamilyDependenciesValid(input)) {
			return liveOnlyValidationError("invalid family-sites terminal")
		}
		return nil
	}
	expected := contracts.HistoricalDisabledFamiliesV1()
	if len(result.Executions) != len(expected) || len(result.Audits) != len(expected) || len(result.Audits) > MaxFamilySuppressionAudits || !inputAvailabilityMatches(input) {
		return liveOnlyValidationError("family site population mismatch")
	}
	bindingID, _ := input.History.ContentID()
	for index, family := range expected {
		execution, audit := result.Executions[index], result.Audits[index]
		expectedInputSHA, expectedInputErr := expectedFamilyDependencyInputSHA(family, input, normalizeConfig(DefaultConfig()))
		if execution.SchemaVersion != FamilyExecutionSchemaV1 || execution.Family != family || execution.SiteVersion != family+".history_eligibility.v2" || execution.SessionID != input.Observation.Evidence.SessionID || execution.RawSequence != input.Observation.Evidence.Sequence || execution.RawRecordSHA256 != input.RawRecordSHA256 || execution.HistoryBindingSHA256 != bindingID || execution.Branch != HistoricalUnavailable || !validLiveOnlySHA256(execution.DependencyInputSHA256) {
			return liveOnlyValidationError("invalid family execution at " + family)
		}
		if expectedInputErr != nil || execution.DependencyInputSHA256 != expectedInputSHA {
			return liveOnlyValidationError("family dependency execution did not reconcile at " + family)
		}
		executionID, err := executionSHA(execution)
		if err != nil || audit.SchemaVersion != FamilySuppressionSchemaV1 || audit.Family != family || audit.SiteVersion != execution.SiteVersion || audit.Reason != HistoricalUnavailable || audit.SessionID != execution.SessionID || audit.RawSequence != execution.RawSequence || audit.RawRecordSHA256 != execution.RawRecordSHA256 || audit.HistoryBindingSHA256 != bindingID || audit.ExecutionSHA256 != executionID || audit.CandidateEmitted || audit.DecisionEmitted || audit.OverlayOrDisplayEmitted {
			return liveOnlyValidationError("invalid family audit at " + family)
		}
	}
	for _, candidate := range result.Candidates {
		for _, family := range expected {
			if Family(candidate.RuleVersion) == family {
				return liveOnlyValidationError("history-dependent candidate escaped live-only evaluation")
			}
		}
	}
	return nil
}

func expectedFamilyDependencyInputSHA(family string, input LiveOnlyInputV2, config Config) (string, error) {
	binding := input.Availability.bindingID
	switch family {
	case "hero":
		present := 0
		for _, participant := range input.Observation.Participants {
			if participant.HeroID.State == contracts.ValuePresent || participant.HeroName.State == contracts.ValuePresent {
				present++
			}
		}
		return dependencyInputSHA("hero.history_eligibility.v2", binding, present, len(input.Observation.Participants))
	case "item":
		items := 0
		for _, participant := range input.Observation.Participants {
			items += len(participant.Items)
		}
		return dependencyInputSHA("item.history_eligibility.v2", binding, items, len(config.KeyItems))
	case "lane":
		return dependencyInputSHA("lane.history_eligibility.v2", binding, input.Observation.Map.ClockTime.State, input.Observation.ClockBasis)
	case "patch":
		return dependencyInputSHA("patch.history_eligibility.v2", binding, input.Observation.Evidence.ProviderVersion.State, config.Patch)
	case "player":
		verified := 0
		for _, participant := range input.Observation.Participants {
			if participant.VerifiedIdentity != nil {
				verified++
			}
		}
		return dependencyInputSHA("player.history_eligibility.v2", binding, verified, len(input.Observation.Participants))
	case "player_hero":
		pairs := 0
		for _, participant := range input.Observation.Participants {
			if participant.VerifiedIdentity != nil && participant.HeroID.State == contracts.ValuePresent {
				pairs++
			}
		}
		return dependencyInputSHA("player_hero.history_eligibility.v2", binding, pairs)
	case "role":
		positions := 0
		for _, participant := range input.Observation.Participants {
			if participant.XPos.State == contracts.ValuePresent && participant.YPos.State == contracts.ValuePresent {
				positions++
			}
		}
		return dependencyInputSHA("role.history_eligibility.v2", binding, positions)
	case "team":
		teams := map[string]struct{}{}
		for _, participant := range input.Observation.Participants {
			if participant.TeamKey != "" {
				teams[participant.TeamKey] = struct{}{}
			}
		}
		return dependencyInputSHA("team.history_eligibility.v2", binding, len(teams), len(input.Observation.Buildings))
	default:
		return "", liveOnlyValidationError("unknown family dependency")
	}
}

func validLiveOnlySHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
