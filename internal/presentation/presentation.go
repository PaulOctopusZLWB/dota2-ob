// Package presentation owns localization, versioned broadcast terminology,
// and fail-closed assembly of display-ready OverlayStateV1 values.
package presentation

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

type presentationError string

func (e presentationError) Error() string { return string(e) }

// BuildInput contains only the accepted candidate/decision ports and explicit
// presentation clocks. It deliberately has no raw-source or storage input.
type BuildInput struct {
	Locale            string
	Candidate         contracts.InsightCandidateV1
	Decision          contracts.BroadcastDecisionV1
	PublicationTimeMS int64
	StaleDeadlineMS   int64
}

// Build localizes one committed display decision. Any mismatch, unsafe text,
// unknown terminology, expired candidate, or incompatible parameter set
// returns no state so the caller can publish a claim-free Hidden value.
func Build(input BuildInput) (contracts.OverlayStateV1, error) {
	if err := input.Candidate.Validate(); err != nil {
		return contracts.OverlayStateV1{}, presentationError("invalid_candidate")
	}
	if err := input.Decision.Validate(); err != nil {
		return contracts.OverlayStateV1{}, presentationError("invalid_decision")
	}
	if input.Candidate.Availability != "available" ||
		input.Candidate.SessionID != input.Decision.SessionID ||
		input.Candidate.CandidateID != input.Decision.CandidateID ||
		(input.Decision.ResultingState != contracts.DecisionShown && input.Decision.ResultingState != contracts.DecisionPinned) ||
		input.PublicationTimeMS <= 0 || input.StaleDeadlineMS < input.PublicationTimeMS ||
		input.Decision.PolicyTimeMS > input.PublicationTimeMS || input.Candidate.ExpiryTimeMS <= input.PublicationTimeMS {
		return contracts.OverlayStateV1{}, presentationError("unsafe_presentation_input")
	}
	claim, err := render(input.Locale, input.Candidate)
	if err != nil {
		return contracts.OverlayStateV1{}, err
	}
	received := input.Candidate.Evidence[len(input.Candidate.Evidence)-1].ReceiveTime
	confidence := input.Candidate.Confidence
	if input.Locale == "zh-CN" && confidence == "observed" {
		confidence = "已观测"
	}
	state := contracts.OverlayStateV1{
		SchemaVersion: contracts.OverlayStateSchemaV1,
		SessionID:     input.Candidate.SessionID, PublicationTimeMS: input.PublicationTimeMS,
		StaleDeadlineMS: input.StaleDeadlineMS, Visibility: "visible",
		DecisionID: input.Decision.DecisionID, Evidence: append([]contracts.EvidenceRefV1(nil), input.Candidate.Evidence...),
		Confidence: confidence, SourceReceiveTime: &received, SnapshotID: input.Candidate.SnapshotID,
		Claim: &claim,
	}
	if err := state.Validate(); err != nil {
		return contracts.OverlayStateV1{}, presentationError("unsafe_overlay_state")
	}
	return state, nil
}

// Hidden constructs the only safe output for unavailable, stale, malformed,
// disconnected, out-of-order, or emergency-hidden presentation state.
func Hidden(sessionID string, publicationTimeMS, staleDeadlineMS int64, healthCode string) (contracts.OverlayStateV1, error) {
	state := contracts.OverlayStateV1{
		SchemaVersion: contracts.OverlayStateSchemaV1,
		SessionID:     sessionID, PublicationTimeMS: publicationTimeMS,
		StaleDeadlineMS: staleDeadlineMS, Visibility: "hidden", HealthCode: healthCode,
		Evidence: []contracts.EvidenceRefV1{},
	}
	if err := state.Validate(); err != nil {
		return contracts.OverlayStateV1{}, presentationError("invalid_hidden_state")
	}
	return state, nil
}
