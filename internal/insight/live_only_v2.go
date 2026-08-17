package insight

import (
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const (
	LiveOnlyEvaluationSchemaV2 = "live_only_evaluation.v2"
	FamilySuppressionSchemaV1  = "family_suppression_audit.v1"
	FamilySitesNotEvaluated    = "family_sites_not_evaluated"
	HistoricalUnavailable      = "historical_unavailable"
	MaxFamilySuppressionAudits = 8
)

type liveOnlyValidationError string

func (e liveOnlyValidationError) Error() string { return string(e) }

// availabilityToken is deliberately private. The only way product or recovery
// wiring can obtain the eight typed values below is by validating the sealed
// history binding through ProductLiveOnlyAvailabilityV1.
type availabilityToken struct {
	family    string
	reason    string
	bindingID string
}

type HeroHistoryAvailabilityV1 struct{ token availabilityToken }
type ItemHistoryAvailabilityV1 struct{ token availabilityToken }
type LaneHistoryAvailabilityV1 struct{ token availabilityToken }
type PatchHistoryAvailabilityV1 struct{ token availabilityToken }
type PlayerHistoryAvailabilityV1 struct{ token availabilityToken }
type PlayerHeroHistoryAvailabilityV1 struct{ token availabilityToken }
type RoleHistoryAvailabilityV1 struct{ token availabilityToken }
type TeamHistoryAvailabilityV1 struct{ token availabilityToken }

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

// ProductLiveOnlyAvailabilityV1 derives all family inputs from one validated,
// content-addressed no-go binding. It accepts neither a family list nor caller
// supplied reasons.
func ProductLiveOnlyAvailabilityV1(history contracts.HistoryAvailabilityBindingV1) (LiveOnlyAvailabilityV1, error) {
	if history.Validate() != nil || history.Mode != contracts.HistoryModeNoGo {
		return LiveOnlyAvailabilityV1{}, liveOnlyValidationError("live-only history availability is not the accepted no-go binding")
	}
	bindingID, err := history.ContentID()
	if err != nil {
		return LiveOnlyAvailabilityV1{}, err
	}
	token := func(family string) availabilityToken {
		return availabilityToken{family: family, reason: HistoricalUnavailable, bindingID: bindingID}
	}
	return LiveOnlyAvailabilityV1{
		hero: HeroHistoryAvailabilityV1{token("hero")}, item: ItemHistoryAvailabilityV1{token("item")},
		lane: LaneHistoryAvailabilityV1{token("lane")}, patch: PatchHistoryAvailabilityV1{token("patch")},
		player: PlayerHistoryAvailabilityV1{token("player")}, playerHero: PlayerHeroHistoryAvailabilityV1{token("player_hero")},
		role: RoleHistoryAvailabilityV1{token("role")}, team: TeamHistoryAvailabilityV1{token("team")}, bindingID: bindingID,
	}, nil
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
	CandidateEmitted        bool   `json:"candidate_emitted"`
	DecisionEmitted         bool   `json:"decision_emitted"`
	OverlayOrDisplayEmitted bool   `json:"overlay_or_display_emitted"`
}

type LiveOnlyEvaluationV2 struct {
	SchemaVersion  string                         `json:"schema_version"`
	Candidates     []contracts.InsightCandidateV1 `json:"candidates"`
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

// EvaluateLiveOnlyV2 is the versioned pure production evaluator. The eight
// calls are intentionally explicit: each family owns a distinct typed branch;
// no registry enumeration constructs execution evidence.
func EvaluateLiveOnlyV2(input LiveOnlyInputV2, config Config) LiveOnlyEvaluationV2 {
	legacy := LiveOnlyInput{Observation: input.Observation, Previous: input.Previous, History: input.History, Lineage: input.Lineage, PolicyTimeMS: input.PolicyTimeMS}
	candidates := EvaluateLiveOnly(legacy, config)
	result := LiveOnlyEvaluationV2{SchemaVersion: LiveOnlyEvaluationSchemaV2, Candidates: candidates}
	if globalLiveOnlyFailure(candidates) || !inputAvailabilityMatches(input) {
		result.TerminalReason = FamilySitesNotEvaluated
		return result
	}

	result.Audits = make([]FamilySuppressionAuditV1, 0, MaxFamilySuppressionAudits)
	result.Audits = append(result.Audits, heroHistoryEligibility(input.Availability.hero, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, itemHistoryEligibility(input.Availability.item, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, laneHistoryEligibility(input.Availability.lane, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, patchHistoryEligibility(input.Availability.patch, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, playerHistoryEligibility(input.Availability.player, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, playerHeroHistoryEligibility(input.Availability.playerHero, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, roleHistoryEligibility(input.Availability.role, input.Observation, input.RawRecordSHA256))
	result.Audits = append(result.Audits, teamHistoryEligibility(input.Availability.team, input.Observation, input.RawRecordSHA256))
	return result
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

func familyAudit(token availabilityToken, site string, observation contracts.LiveObservationV1, rawRecordSHA256 string) FamilySuppressionAuditV1 {
	return FamilySuppressionAuditV1{
		SchemaVersion: FamilySuppressionSchemaV1, SiteVersion: site, Family: token.family, Reason: token.reason,
		SessionID: observation.Evidence.SessionID, RawSequence: observation.Evidence.Sequence, RawRecordSHA256: rawRecordSHA256,
		HistoryBindingSHA256: token.bindingID, CandidateEmitted: false, DecisionEmitted: false, OverlayOrDisplayEmitted: false,
	}
}

func heroHistoryEligibility(value HeroHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "hero") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "hero.history_eligibility.v1", observation, raw)
}
func itemHistoryEligibility(value ItemHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "item") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "item.history_eligibility.v1", observation, raw)
}
func laneHistoryEligibility(value LaneHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "lane") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "lane.history_eligibility.v1", observation, raw)
}
func patchHistoryEligibility(value PatchHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "patch") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "patch.history_eligibility.v1", observation, raw)
}
func playerHistoryEligibility(value PlayerHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "player") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "player.history_eligibility.v1", observation, raw)
}
func playerHeroHistoryEligibility(value PlayerHeroHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "player_hero") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "player_hero.history_eligibility.v1", observation, raw)
}
func roleHistoryEligibility(value RoleHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "role") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "role.history_eligibility.v1", observation, raw)
}
func teamHistoryEligibility(value TeamHistoryAvailabilityV1, observation contracts.LiveObservationV1, raw string) FamilySuppressionAuditV1 {
	if !closedFamilyDependency(value.token, "team") {
		return FamilySuppressionAuditV1{}
	}
	return familyAudit(value.token, "team.history_eligibility.v1", observation, raw)
}

func closedFamilyDependency(token availabilityToken, family string) bool {
	return token.family == family && token.reason == HistoricalUnavailable && validLiveOnlySHA256(token.bindingID)
}

// ValidateLiveOnlyEvaluationV2 is the publication barrier. The expected order
// is registry-derived, while execution is produced only by the eight sites.
func ValidateLiveOnlyEvaluationV2(result LiveOnlyEvaluationV2, input LiveOnlyInputV2) error {
	if result.SchemaVersion != LiveOnlyEvaluationSchemaV2 || len(result.Candidates) == 0 {
		return liveOnlyValidationError("invalid live-only evaluation envelope")
	}
	if result.TerminalReason != "" {
		if result.TerminalReason != FamilySitesNotEvaluated || len(result.Audits) != 0 || !globalLiveOnlyFailure(result.Candidates) && inputAvailabilityMatches(input) {
			return liveOnlyValidationError("invalid family-sites terminal")
		}
		return nil
	}
	expected := contracts.HistoricalDisabledFamiliesV1()
	if len(result.Audits) != len(expected) || len(result.Audits) > MaxFamilySuppressionAudits || !inputAvailabilityMatches(input) {
		return liveOnlyValidationError("family audit population mismatch")
	}
	bindingID, _ := input.History.ContentID()
	for index, audit := range result.Audits {
		family := expected[index]
		if audit.SchemaVersion != FamilySuppressionSchemaV1 || audit.Family != family || audit.SiteVersion != family+".history_eligibility.v1" ||
			audit.Reason != HistoricalUnavailable || audit.SessionID != input.Observation.Evidence.SessionID ||
			audit.RawSequence != input.Observation.Evidence.Sequence || audit.RawRecordSHA256 != input.RawRecordSHA256 ||
			audit.HistoryBindingSHA256 != bindingID || audit.CandidateEmitted || audit.DecisionEmitted || audit.OverlayOrDisplayEmitted {
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
