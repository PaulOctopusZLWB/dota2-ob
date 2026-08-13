package contracts

import (
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"
)

const (
	PolicyCommitSchemaV2          = "policy_commit.v2"
	PolicyCheckpointSchemaV2      = "policy_checkpoint.v2"
	PolicyLineageManifestSchemaV2 = "policy_lineage_manifest.v2"
	PolicyStateSchemaV2           = "policy_state.v2"
	MaxPolicyLineageManifestBytes = 2 << 20
	MaxPolicyCheckpointBytes      = 16 << 20
	MaxPolicyIdentifierBytes      = 128
	MaxDisabledRules              = 256
	MaxRuleCooldowns              = 256
	MaxPolicyPins                 = 64
	MaxCandidateTombstones        = 4096
)

var (
	ErrSessionCommandLimit   = errors.New("session_command_limit")
	ErrPolicyIdentifierLimit = errors.New("policy_identifier_limit")
	ErrMalformedCommand      = errors.New("malformed_operator_command")
)

// PolicyArtifactIdentityV2 binds an immutable input by both its human-readable
// version and canonical content digest. Neither field may be inferred during
// recovery.
type PolicyArtifactIdentityV2 struct {
	Version       string `json:"version"`
	ContentSHA256 string `json:"content_sha256"`
}

func (v PolicyArtifactIdentityV2) validate(name string) error {
	if v.Version == "" || !isSHA(v.ContentSHA256) {
		return fmt.Errorf("invalid %s artifact identity", name)
	}
	return nil
}

// PolicyLineageManifestV2 is immutable content. Its external manifest ID is
// SHA-256 over the canonical value returned by MarshalCanonical.
type PolicyLineageManifestV2 struct {
	SchemaVersion                string                   `json:"schema_version"`
	SessionID                    string                   `json:"session_id"`
	RawRecordSchema              PolicyArtifactIdentityV2 `json:"raw_record_schema"`
	RawRecordFraming             PolicyArtifactIdentityV2 `json:"raw_record_framing"`
	RawPayloadSchema             PolicyArtifactIdentityV2 `json:"raw_payload_schema"`
	LiveObservationSchema        PolicyArtifactIdentityV2 `json:"live_observation_schema"`
	ProjectionMapping            PolicyArtifactIdentityV2 `json:"projection_mapping"`
	TournamentScopeID            string                   `json:"tournament_scope_id"`
	TournamentScopeSHA256        string                   `json:"tournament_scope_sha256"`
	HistoricalSnapshotID         string                   `json:"historical_snapshot_id"`
	HistoricalSnapshotSHA256     string                   `json:"historical_snapshot_sha256"`
	EligibleBaselineSHA256       []string                 `json:"eligible_baseline_sha256"`
	Rules                        PolicyArtifactIdentityV2 `json:"rules"`
	Config                       PolicyArtifactIdentityV2 `json:"config"`
	Catalog                      PolicyArtifactIdentityV2 `json:"catalog"`
	Terminology                  PolicyArtifactIdentityV2 `json:"terminology"`
	LocalizationParameterMapping PolicyArtifactIdentityV2 `json:"localization_parameter_mapping"`
	EngineBuild                  PolicyArtifactIdentityV2 `json:"engine_build"`
}

func (v PolicyLineageManifestV2) Validate() error {
	if v.SchemaVersion != PolicyLineageManifestSchemaV2 {
		return schemaError(PolicyLineageManifestSchemaV2, v.SchemaVersion)
	}
	if v.SessionID == "" || !validPolicyID(v.SessionID) || !isSHA(v.TournamentScopeID) || v.TournamentScopeID != v.TournamentScopeSHA256 || !isSHA(v.HistoricalSnapshotID) || v.HistoricalSnapshotID != v.HistoricalSnapshotSHA256 {
		return errors.New("invalid policy lineage identity")
	}
	artifacts := []struct {
		name string
		id   PolicyArtifactIdentityV2
	}{
		{"raw record schema", v.RawRecordSchema}, {"raw record framing", v.RawRecordFraming},
		{"raw payload schema", v.RawPayloadSchema}, {"live observation schema", v.LiveObservationSchema},
		{"projection mapping", v.ProjectionMapping}, {"rules", v.Rules}, {"config", v.Config},
		{"catalog", v.Catalog}, {"terminology", v.Terminology},
		{"localization parameter mapping", v.LocalizationParameterMapping}, {"engine build", v.EngineBuild},
	}
	for _, artifact := range artifacts {
		if err := artifact.id.validate(artifact.name); err != nil {
			return err
		}
	}
	for i, hash := range v.EligibleBaselineSHA256 {
		if !isSHA(hash) || (i > 0 && hash <= v.EligibleBaselineSHA256[i-1]) {
			return errors.New("eligible baseline hashes are not unique canonical order")
		}
	}
	return validateSize(v, MaxPolicyLineageManifestBytes)
}

func (v PolicyLineageManifestV2) ContentID() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	return CanonicalSHA256(v)
}

func (v PolicyLineageManifestV2) MustContentID() string {
	id, _ := v.ContentID()
	return id
}

type PolicyCommitV2 struct {
	SchemaVersion                string                   `json:"schema_version"`
	LineageManifestID            string                   `json:"lineage_manifest_id"`
	LineageManifestSHA256        string                   `json:"lineage_manifest_sha256"`
	SessionID                    string                   `json:"session_id"`
	CommitSequence               uint64                   `json:"commit_sequence"`
	ObservationSequence          uint64                   `json:"observation_sequence,omitempty"`
	ObservationEvidence          *EvidenceRefV1           `json:"observation_evidence,omitempty"`
	RawRecordSHA256              string                   `json:"raw_record_sha256,omitempty"`
	LiveObservationSHA256        string                   `json:"live_observation_sha256,omitempty"`
	CommandID                    string                   `json:"command_id,omitempty"`
	Command                      *OperatorCommandV1       `json:"command,omitempty"`
	PriorPolicyRevision          uint64                   `json:"prior_policy_revision"`
	ResultingPolicyRevision      uint64                   `json:"resulting_policy_revision"`
	PriorStateHash               string                   `json:"prior_state_hash,omitempty"`
	ResultingStateHash           string                   `json:"resulting_state_hash"`
	ResultingObservationSequence uint64                   `json:"resulting_observation_sequence"`
	ResultingPolicyTimeMS        int64                    `json:"resulting_policy_time_ms"`
	Decisions                    []BroadcastDecisionV1    `json:"decisions"`
	CommandResult                *OperatorCommandResultV1 `json:"command_result,omitempty"`
	AuditEvents                  []AuditEventV1           `json:"audit_events"`
	Publication                  string                   `json:"publication"`
}

func (v PolicyCommitV2) Validate() error {
	if v.SchemaVersion != PolicyCommitSchemaV2 {
		return schemaError(PolicyCommitSchemaV2, v.SchemaVersion)
	}
	if !isSHA(v.LineageManifestID) || v.LineageManifestID != v.LineageManifestSHA256 || v.SessionID == "" || !validPolicyID(v.SessionID) || v.CommitSequence == 0 || !isSHA(v.ResultingStateHash) || !publications[v.Publication] || v.ResultingPolicyRevision < v.PriorPolicyRevision || v.ResultingPolicyTimeMS < 0 {
		return errors.New("invalid policy commit v2")
	}
	if v.CommitSequence > 1 && !isSHA(v.PriorStateHash) {
		return errors.New("invalid prior state hash")
	}
	observation := v.ObservationSequence != 0 || v.ObservationEvidence != nil || v.RawRecordSHA256 != "" || v.LiveObservationSHA256 != ""
	command := v.CommandID != "" || v.Command != nil || v.CommandResult != nil
	if observation == command {
		return errors.New("policy commit v2 requires exactly one causal input")
	}
	if observation {
		if v.ObservationSequence == 0 || v.ObservationEvidence == nil || !isSHA(v.RawRecordSHA256) || !isSHA(v.LiveObservationSHA256) || v.CommandID != "" || v.Command != nil || v.CommandResult != nil {
			return errors.New("incomplete observation causal input")
		}
		if err := v.ObservationEvidence.validate(v.SessionID); err != nil || v.ObservationEvidence.Sequence != v.ObservationSequence || v.ResultingObservationSequence < v.ObservationSequence {
			return errors.New("observation causal input mismatch")
		}
	} else {
		if v.Command == nil || v.CommandResult == nil || v.CommandID == "" || !validPolicyID(v.CommandID) {
			return errors.New("incomplete command causal input")
		}
		if err := v.Command.Validate(); err != nil || v.Command.CommandID != v.CommandID || v.Command.SessionID != v.SessionID {
			return errors.New("embedded command mismatch")
		}
		if err := v.CommandResult.Validate(); err != nil || v.CommandResult.CommandID != v.CommandID || v.CommandResult.SessionID != v.SessionID || v.CommandResult.PreviousRevision != v.PriorPolicyRevision || v.CommandResult.ResultingRevision != v.ResultingPolicyRevision {
			return errors.New("command result mismatch")
		}
	}
	decisionIDs := make(map[string]bool, len(v.Decisions))
	for i, decision := range v.Decisions {
		commandCauseMismatch := command && decision.CommandID != v.CommandID
		observationCauseMismatch := observation && decision.CommandID != ""
		if err := decision.Validate(); err != nil || decision.SessionID != v.SessionID || decision.PolicyRevision != v.ResultingPolicyRevision || commandCauseMismatch || observationCauseMismatch || decisionIDs[decision.DecisionID] {
			return fmt.Errorf("decisions[%d] incoherent", i)
		}
		decisionIDs[decision.DecisionID] = true
	}
	if v.CommandResult != nil {
		for _, id := range v.CommandResult.DecisionIDs {
			if !decisionIDs[id] {
				return errors.New("command result references unknown decision")
			}
		}
	}
	if len(v.AuditEvents) == 0 {
		return errors.New("policy commit v2 missing audit event")
	}
	for i, event := range v.AuditEvents {
		commandCauseMismatch := command && event.CommandID != v.CommandID
		observationCauseMismatch := observation && event.CommandID != ""
		if err := event.Validate(); err != nil || event.SessionID != v.SessionID || commandCauseMismatch || observationCauseMismatch {
			return fmt.Errorf("audit_events[%d] incoherent", i)
		}
	}
	if v.Publication == PublicationPublish && len(v.Decisions) == 0 {
		return errors.New("publish commit missing decision")
	}
	return validateSize(v, MaxPolicyCommitBytes)
}

type RuleCooldownV2 struct {
	RuleID            string `json:"rule_id"`
	UntilPolicyTimeMS int64  `json:"until_policy_time_ms"`
}

type PolicyPinV2 struct {
	CandidateID string `json:"candidate_id"`
	DecisionID  string `json:"decision_id"`
}

type PolicyActivePrimaryV2 struct {
	Candidate InsightCandidateV1  `json:"candidate"`
	Decision  BroadcastDecisionV1 `json:"decision"`
}

type PolicyCommandResultRefV2 struct {
	CommandID    string `json:"command_id"`
	ResultSHA256 string `json:"result_sha256"`
}

type OperatorCommandAdmissionV2 struct {
	Duplicate    bool
	ResultSHA256 string
}

// AdmitOperatorCommandV2 freezes the reviewed gateway ordering: validate only
// the bound session/ID first, resolve an indexed duplicate next, then enforce
// capacity and full action/target validation for a new ID.
func AdmitOperatorCommandV2(sessionID string, command OperatorCommandV1, index []PolicyCommandResultRefV2) (OperatorCommandAdmissionV2, error) {
	if sessionID == "" || command.SchemaVersion != OperatorCommandSchemaV1 || command.SessionID != sessionID || !validPolicyID(sessionID) || !validPolicyID(command.CommandID) {
		if len([]byte(command.CommandID)) > MaxPolicyIdentifierBytes || len([]byte(sessionID)) > MaxPolicyIdentifierBytes {
			return OperatorCommandAdmissionV2{}, ErrPolicyIdentifierLimit
		}
		return OperatorCommandAdmissionV2{}, ErrMalformedCommand
	}
	for i, entry := range index {
		if !validPolicyID(entry.CommandID) || !isSHA(entry.ResultSHA256) || (i > 0 && entry.CommandID <= index[i-1].CommandID) {
			return OperatorCommandAdmissionV2{}, errors.New("invalid semantic command index")
		}
		if entry.CommandID == command.CommandID {
			return OperatorCommandAdmissionV2{Duplicate: true, ResultSHA256: entry.ResultSHA256}, nil
		}
	}
	if len(index) >= MaxCheckpointCommandResults {
		return OperatorCommandAdmissionV2{}, ErrSessionCommandLimit
	}
	if len([]byte(command.TargetCandidateID)) > MaxPolicyIdentifierBytes || len([]byte(command.TargetRuleID)) > MaxPolicyIdentifierBytes {
		return OperatorCommandAdmissionV2{}, ErrPolicyIdentifierLimit
	}
	if err := command.Validate(); err != nil {
		return OperatorCommandAdmissionV2{}, ErrMalformedCommand
	}
	return OperatorCommandAdmissionV2{}, nil
}

// PolicyCommandLocatorV2 is application-owned cache state. It is deliberately
// excluded from PolicyStateV2 and therefore from ComputeStateHash.
type PolicyCommandLocatorV2 struct {
	CommandID      string `json:"command_id"`
	SegmentID      string `json:"segment_id"`
	FrameOffset    int64  `json:"frame_offset"`
	CommitSequence uint64 `json:"commit_sequence"`
	FrameSHA256    string `json:"frame_sha256"`
}

type PolicyCandidateTombstoneV2 struct {
	CandidateID               string `json:"candidate_id"`
	RuleID                    string `json:"rule_id"`
	TerminalDecisionID        string `json:"terminal_decision_id"`
	TerminalState             string `json:"terminal_state"`
	SuppressUntilPolicyTimeMS int64  `json:"suppress_until_policy_time_ms"`
}

func ExpirePolicyTombstonesV2(values []PolicyCandidateTombstoneV2, policyTimeMS int64) []PolicyCandidateTombstoneV2 {
	remaining := make([]PolicyCandidateTombstoneV2, 0, len(values))
	for _, value := range values {
		if value.SuppressUntilPolicyTimeMS > policyTimeMS {
			remaining = append(remaining, value)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].SuppressUntilPolicyTimeMS != remaining[j].SuppressUntilPolicyTimeMS {
			return remaining[i].SuppressUntilPolicyTimeMS < remaining[j].SuppressUntilPolicyTimeMS
		}
		return remaining[i].CandidateID < remaining[j].CandidateID
	})
	return remaining
}

func InsertPolicyTombstoneV2(values []PolicyCandidateTombstoneV2, value PolicyCandidateTombstoneV2) ([]PolicyCandidateTombstoneV2, string) {
	for _, existing := range values {
		if existing.CandidateID == value.CandidateID {
			return append([]PolicyCandidateTombstoneV2(nil), values...), "duplicate_candidate"
		}
	}
	if len(values) >= MaxCandidateTombstones {
		return append([]PolicyCandidateTombstoneV2(nil), values...), "candidate_index_capacity"
	}
	result := append(append([]PolicyCandidateTombstoneV2(nil), values...), value)
	sort.Slice(result, func(i, j int) bool {
		if result[i].SuppressUntilPolicyTimeMS != result[j].SuppressUntilPolicyTimeMS {
			return result[i].SuppressUntilPolicyTimeMS < result[j].SuppressUntilPolicyTimeMS
		}
		return result[i].CandidateID < result[j].CandidateID
	})
	return result, ""
}

// PolicyStateV2 is the complete pure semantic state-hash projection. It has no
// checkpoint creation/anchor fields and no filesystem locator fields.
type PolicyStateV2 struct {
	SchemaVersion           string                       `json:"schema_version"`
	SessionID               string                       `json:"session_id"`
	PolicyRevision          uint64                       `json:"policy_revision"`
	LastObservationSequence uint64                       `json:"last_observation_sequence"`
	LastPolicyTimeMS        int64                        `json:"last_policy_time_ms"`
	Preview                 []InsightCandidateV1         `json:"preview"`
	DisabledRuleIDs         []string                     `json:"disabled_rule_ids"`
	Cooldowns               []RuleCooldownV2             `json:"cooldowns"`
	Pins                    []PolicyPinV2                `json:"pins"`
	EmergencyHide           bool                         `json:"emergency_hide"`
	ActivePrimary           *PolicyActivePrimaryV2       `json:"active_primary,omitempty"`
	CommandResults          []PolicyCommandResultRefV2   `json:"command_results"`
	CandidateTombstones     []PolicyCandidateTombstoneV2 `json:"candidate_tombstones"`
}

type PolicyCheckpointV2 struct {
	SchemaVersion           string                       `json:"schema_version"`
	LineageManifestID       string                       `json:"lineage_manifest_id"`
	LineageManifestSHA256   string                       `json:"lineage_manifest_sha256"`
	SessionID               string                       `json:"session_id"`
	CommitSequence          uint64                       `json:"commit_sequence"`
	ReferencedCommitSHA256  string                       `json:"referenced_commit_sha256"`
	LastObservationSequence uint64                       `json:"last_observation_sequence"`
	PolicyRevision          uint64                       `json:"policy_revision"`
	LastPolicyTimeMS        int64                        `json:"last_policy_time_ms"`
	StateHash               string                       `json:"state_hash"`
	CreatedTimeMS           int64                        `json:"created_time_ms"`
	Preview                 []InsightCandidateV1         `json:"preview"`
	DisabledRuleIDs         []string                     `json:"disabled_rule_ids"`
	Cooldowns               []RuleCooldownV2             `json:"cooldowns"`
	Pins                    []PolicyPinV2                `json:"pins"`
	EmergencyHide           bool                         `json:"emergency_hide"`
	ActivePrimary           *PolicyActivePrimaryV2       `json:"active_primary,omitempty"`
	CommandResults          []PolicyCommandResultRefV2   `json:"command_results"`
	CommandLocators         []PolicyCommandLocatorV2     `json:"command_locators"`
	CandidateTombstones     []PolicyCandidateTombstoneV2 `json:"candidate_tombstones"`
}

func (v PolicyCheckpointV2) StateProjection() PolicyStateV2 {
	return PolicyStateV2{
		SchemaVersion: PolicyStateSchemaV2, SessionID: v.SessionID, PolicyRevision: v.PolicyRevision,
		LastObservationSequence: v.LastObservationSequence, LastPolicyTimeMS: v.LastPolicyTimeMS,
		Preview: v.Preview, DisabledRuleIDs: v.DisabledRuleIDs, Cooldowns: v.Cooldowns, Pins: v.Pins,
		EmergencyHide: v.EmergencyHide, ActivePrimary: v.ActivePrimary, CommandResults: v.CommandResults,
		CandidateTombstones: v.CandidateTombstones,
	}
}

func (v PolicyCheckpointV2) ComputeStateHash() (string, error) {
	return CanonicalSHA256(v.StateProjection())
}

func (v PolicyCheckpointV2) Validate() error {
	if v.SchemaVersion != PolicyCheckpointSchemaV2 {
		return schemaError(PolicyCheckpointSchemaV2, v.SchemaVersion)
	}
	if !isSHA(v.LineageManifestID) || v.LineageManifestID != v.LineageManifestSHA256 || v.SessionID == "" || !validPolicyID(v.SessionID) || v.CommitSequence == 0 || !isSHA(v.ReferencedCommitSHA256) || !isSHA(v.StateHash) || v.LastPolicyTimeMS < 0 || v.CreatedTimeMS < 0 {
		return errors.New("invalid policy checkpoint v2 identity or anchor")
	}
	if len(v.Preview) > MaxPreviewCandidates || len(v.DisabledRuleIDs) > MaxDisabledRules || len(v.Cooldowns) > MaxRuleCooldowns || len(v.Pins) > MaxPolicyPins || len(v.CommandResults) > MaxCheckpointCommandResults || len(v.CommandLocators) > MaxCheckpointCommandResults || len(v.CandidateTombstones) > MaxCandidateTombstones {
		return errors.New("policy checkpoint v2 collection bound exceeded")
	}
	if err := validateCheckpointState(v); err != nil {
		return err
	}
	hash, err := v.ComputeStateHash()
	if err != nil || hash != v.StateHash {
		return errors.New("policy checkpoint v2 state hash mismatch")
	}
	return validateSize(v, MaxPolicyCheckpointBytes)
}

func (v PolicyCheckpointV2) ValidateAgainstCommit(commit PolicyCommitV2, frameSHA256 string) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := commit.Validate(); err != nil {
		return err
	}
	if !isSHA(frameSHA256) || v.LineageManifestID != commit.LineageManifestID || v.LineageManifestSHA256 != commit.LineageManifestSHA256 || v.SessionID != commit.SessionID || v.CommitSequence != commit.CommitSequence || v.ReferencedCommitSHA256 != frameSHA256 || v.PolicyRevision != commit.ResultingPolicyRevision || v.StateHash != commit.ResultingStateHash || v.LastObservationSequence != commit.ResultingObservationSequence || v.LastPolicyTimeMS != commit.ResultingPolicyTimeMS {
		return errors.New("policy checkpoint v2 commit anchor mismatch")
	}
	return nil
}

func validateCheckpointState(v PolicyCheckpointV2) error {
	retained := make(map[string]bool, len(v.Preview)+1)
	for i, candidate := range v.Preview {
		if err := candidate.Validate(); err != nil || candidate.SessionID != v.SessionID || !validPolicyID(candidate.CandidateID) || retained[candidate.CandidateID] {
			return fmt.Errorf("preview[%d] invalid", i)
		}
		retained[candidate.CandidateID] = true
		if i > 0 && !candidateRanksBefore(v.Preview[i-1], candidate) {
			return errors.New("preview candidates are not deterministic rank order")
		}
	}
	if v.ActivePrimary != nil {
		if err := v.ActivePrimary.Candidate.Validate(); err != nil || v.ActivePrimary.Candidate.SessionID != v.SessionID || !validPolicyID(v.ActivePrimary.Candidate.CandidateID) || retained[v.ActivePrimary.Candidate.CandidateID] {
			return errors.New("invalid active primary candidate")
		}
		if err := v.ActivePrimary.Decision.Validate(); err != nil || v.ActivePrimary.Decision.SessionID != v.SessionID || v.ActivePrimary.Decision.CandidateID != v.ActivePrimary.Candidate.CandidateID {
			return errors.New("invalid active primary decision")
		}
		retained[v.ActivePrimary.Candidate.CandidateID] = true
	}
	if err := validateSortedIDs(v.DisabledRuleIDs); err != nil {
		return fmt.Errorf("disabled rules: %w", err)
	}
	for i, cooldown := range v.Cooldowns {
		if !validPolicyID(cooldown.RuleID) || cooldown.UntilPolicyTimeMS < 0 || (i > 0 && cooldown.RuleID <= v.Cooldowns[i-1].RuleID) {
			return errors.New("invalid cooldown order or value")
		}
	}
	for i, pin := range v.Pins {
		if !validPolicyID(pin.CandidateID) || !validPolicyID(pin.DecisionID) || !retained[pin.CandidateID] || (i > 0 && pin.CandidateID <= v.Pins[i-1].CandidateID) {
			return errors.New("invalid pin order or target")
		}
	}
	for i, ref := range v.CommandResults {
		if !validPolicyID(ref.CommandID) || !isSHA(ref.ResultSHA256) || (i > 0 && ref.CommandID <= v.CommandResults[i-1].CommandID) {
			return errors.New("invalid semantic command result index")
		}
	}
	if len(v.CommandResults) != len(v.CommandLocators) {
		return errors.New("semantic result and locator indexes are not one-to-one")
	}
	for i, locator := range v.CommandLocators {
		if !validPolicyID(locator.CommandID) || !validPolicyID(locator.SegmentID) || locator.FrameOffset < 0 || locator.CommitSequence == 0 || !isSHA(locator.FrameSHA256) || (i > 0 && locator.CommandID <= v.CommandLocators[i-1].CommandID) || locator.CommandID != v.CommandResults[i].CommandID {
			return errors.New("invalid command locator index")
		}
	}
	for i, tombstone := range v.CandidateTombstones {
		if !validPolicyID(tombstone.CandidateID) || !validPolicyID(tombstone.RuleID) || !validPolicyID(tombstone.TerminalDecisionID) || !decisionStates[tombstone.TerminalState] || tombstone.SuppressUntilPolicyTimeMS < 0 {
			return errors.New("invalid candidate tombstone")
		}
		if i > 0 {
			prior := v.CandidateTombstones[i-1]
			if tombstone.SuppressUntilPolicyTimeMS < prior.SuppressUntilPolicyTimeMS || (tombstone.SuppressUntilPolicyTimeMS == prior.SuppressUntilPolicyTimeMS && tombstone.CandidateID <= prior.CandidateID) {
				return errors.New("candidate tombstones are not canonical order")
			}
		}
	}
	return nil
}

func candidateRanksBefore(a, b InsightCandidateV1) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	aTime, bTime := int64(0), int64(0)
	if len(a.Evidence) > 0 {
		aTime = a.Evidence[0].ReceiveTime.UnixMilli()
	}
	if len(b.Evidence) > 0 {
		bTime = b.Evidence[0].ReceiveTime.UnixMilli()
	}
	if aTime != bTime {
		return aTime < bTime
	}
	if a.RuleVersion != b.RuleVersion {
		return a.RuleVersion < b.RuleVersion
	}
	return a.CandidateID < b.CandidateID
}

func validateSortedIDs(ids []string) error {
	if !sort.StringsAreSorted(ids) {
		return errors.New("identifiers are not sorted")
	}
	for i, id := range ids {
		if !validPolicyID(id) || (i > 0 && id == ids[i-1]) {
			return errors.New("identifiers are invalid or duplicated")
		}
	}
	return nil
}

func validPolicyID(value string) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= MaxPolicyIdentifierBytes
}

func ValidPolicyIdentifierV2(value string) bool { return validPolicyID(value) }
