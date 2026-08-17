package m4match

import (
	"errors"
	"fmt"
)

type RunPurposeV1 string

const (
	PurposeP4Acceptance         RunPurposeV1 = "p4_acceptance"
	PurposePublicMatchRehearsal RunPurposeV1 = "public_match_rehearsal"
)

type MatchClassV1 string

const (
	MatchClassTI               MatchClassV1 = "ti"
	MatchClassPublicTournament MatchClassV1 = "public_tournament"
	MatchClassPublicMatch      MatchClassV1 = "public_match"
)

const (
	p4RootDomain        = "dota2-ob.m4.p4-acceptance.v4"
	rehearsalRootDomain = "dota2-ob.m4.public-match-rehearsal.v1"
	rehearsalContractV1 = "public_match_rehearsal_contract.v1"
)

type RunClassificationV1 struct {
	Purpose RunPurposeV1 `json:"run_purpose"`
	Class   MatchClassV1 `json:"match_class"`
}

func ParseRunClassification(purpose, class string) (RunClassificationV1, error) {
	result := RunClassificationV1{Purpose: RunPurposeV1(purpose), Class: MatchClassV1(class)}
	if purpose == "" || class == "" {
		return RunClassificationV1{}, errors.New("run purpose and match class are required exactly once")
	}
	if err := result.Validate(); err != nil {
		return RunClassificationV1{}, err
	}
	return result, nil
}

func (classification RunClassificationV1) Validate() error {
	switch classification.Purpose {
	case PurposeP4Acceptance:
		if classification.Class != MatchClassTI && classification.Class != MatchClassPublicTournament {
			return fmt.Errorf("illegal purpose/class pair: %s/%s", classification.Purpose, classification.Class)
		}
	case PurposePublicMatchRehearsal:
		if classification.Class != MatchClassPublicMatch {
			return fmt.Errorf("illegal purpose/class pair: %s/%s", classification.Purpose, classification.Class)
		}
	default:
		return fmt.Errorf("unknown run purpose: %q", classification.Purpose)
	}
	if classification.Class != MatchClassTI && classification.Class != MatchClassPublicTournament && classification.Class != MatchClassPublicMatch {
		return fmt.Errorf("unknown match class: %q", classification.Class)
	}
	return nil
}

func (classification RunClassificationV1) evidenceSchema() string {
	if classification.Purpose == PurposePublicMatchRehearsal {
		return RehearsalSchemaVersion
	}
	return SchemaVersion
}

func (classification RunClassificationV1) readinessSchema() string {
	if classification.Purpose == PurposePublicMatchRehearsal {
		return RehearsalReadinessSchemaVersion
	}
	return ReadinessSchemaVersion
}

func (classification RunClassificationV1) rootDomain() string {
	if classification.Purpose == PurposePublicMatchRehearsal {
		return rehearsalRootDomain
	}
	return p4RootDomain
}

func (classification RunClassificationV1) instruction() string {
	if classification.Purpose == PurposePublicMatchRehearsal {
		return RehearsalHumanInstruction
	}
	return HumanInstruction
}

func (classification RunClassificationV1) consoleState() string {
	if classification.Purpose == PurposePublicMatchRehearsal {
		return "REHEARSAL_READY"
	}
	return "ARMED"
}

func validateNonAcceptanceFields(purpose RunPurposeV1, claimsP4, qualifying, eligible bool, gate string) error {
	if claimsP4 || eligible {
		return errors.New("preflight/readiness cannot claim or grant P4 acceptance")
	}
	if purpose == PurposePublicMatchRehearsal && (qualifying || gate != "none") {
		return errors.New("rehearsal must be non-qualifying with acceptance gate none")
	}
	if purpose == PurposeP4Acceptance && gate != "manual_dot70_after_review" {
		return errors.New("P4 readiness must retain the reviewed manual gate")
	}
	return nil
}

type TypedAvailabilityV1 string

const (
	UnavailableForPublicMatch TypedAvailabilityV1 = "unavailable_for_public_match"
	NotApplicableRehearsal    TypedAvailabilityV1 = "not_applicable_rehearsal"
)

type SuppressionAuditV1 struct {
	SchemaVersion string              `json:"schema_version"`
	Branch        string              `json:"branch"`
	State         TypedAvailabilityV1 `json:"state"`
	Reason        string              `json:"reason"`
}

func PublicMatchSuppression(branch string) (SuppressionAuditV1, error) {
	if branch == "" {
		return SuppressionAuditV1{}, errors.New("suppressed branch is required")
	}
	return SuppressionAuditV1{
		SchemaVersion: "public_match_suppression_audit.v1",
		Branch:        branch,
		State:         UnavailableForPublicMatch,
		Reason:        "tournament identity unavailable for ordinary public match; display/account fallback prohibited",
	}, nil
}

func rehearsalAcceptanceChecks() []RehearsalAcceptanceCheckV1 {
	return []RehearsalAcceptanceCheckV1{
		{ID: "p4_acceptance", State: NotApplicableRehearsal},
		{ID: "professional_tournament_authority", State: NotApplicableRehearsal},
		{ID: "historical_tournament_identity", State: NotApplicableRehearsal},
	}
}

func rehearsalSuppressionAudits() []SuppressionAuditV1 {
	branches := []string{"historical_baseline", "professional_roster", "series_game", "tournament_identity"}
	result := make([]SuppressionAuditV1, 0, len(branches))
	for _, branch := range branches {
		audit, _ := PublicMatchSuppression(branch)
		result = append(result, audit)
	}
	return result
}
