package contracts

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	HistoryAvailabilityBindingSchemaV1   = "history_availability_binding.v1"
	HistoricalUnavailableFixtureSchemaV1 = "historical_unavailable_fixture.v1"
	LiveOnlyReleaseBindingSchemaV1       = "live_only_release_binding.v1"
	PolicyLineageManifestSchemaV3        = "policy_lineage_manifest.v3"
	PolicyCommitSchemaV3                 = "policy_commit.v3"
	PolicyCheckpointSchemaV3             = "policy_checkpoint.v3"
	MaxHistoryBindingBytes               = 64 << 10
	MaxHistoricalUnavailableFixtureBytes = 64 << 10
	MaxLiveOnlyReleaseBindingBytes       = 64 << 10
	HistoryModeSnapshotBaseline          = "snapshot_baseline"
	HistoryModeNoGo                      = "historical_no_go"
	HistoricalNoGoOutcome                = "historical_no_go_accepted_live_only"
)

var HistoricalDisabledFamiliesV1 = []string{"hero", "item", "lane", "patch", "player", "player_hero", "role", "team"}

const (
	AcceptedHistoryCodeCommit      = "246a49825e2a7776c0673a2be448b23900a9d49e"
	AcceptedHistoryEvidenceCommit  = "77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b"
	AcceptedEvidenceIndexSHA256    = "9d5f6b6e95941c2704f96f85e6e49f61679055648df60b49ccb5ff9c396b0dc4"
	AcceptedArtifactTreeSHA256     = "ba6e7e168bba9117dbcb6804f99e9cb4f25482b7e5c6edad9adc740adbccc37a"
	AcceptedReplayGateAuditSHA256  = "c6a40c002548ba80773c00fdfb126196e079e46cfdf34a99ffe8f7a67705bcb4"
	AcceptedSourceProvenanceSHA256 = "e959e9ff1f7b723bd66641a069a7e610a900e326bc6694a5240c929635bad812"
	AcceptedTournamentScopeID      = "da84de9204dc8493c07a0d4ba9fafcdec7e4481c01060f6bad5d7b954ef5f8f2"
	AcceptedTournamentScopeSHA256  = "890c8517b6ae7859b6dd794aab486c27c2ce3b82f7c87e84884c7006825340bf"
)

type HistoryAvailabilityBindingV1 struct {
	SchemaVersion            string   `json:"schema_version"`
	Mode                     string   `json:"mode"`
	HistoricalSnapshotID     string   `json:"historical_snapshot_id,omitempty"`
	HistoricalSnapshotSHA256 string   `json:"historical_snapshot_sha256,omitempty"`
	EligibleBaselineSHA256   []string `json:"eligible_baseline_sha256,omitempty"`
	TerminalOutcome          string   `json:"terminal_outcome,omitempty"`
	CodeFoundationCommit     string   `json:"code_foundation_commit,omitempty"`
	EvidenceCommit           string   `json:"evidence_commit,omitempty"`
	EvidenceIndexSHA256      string   `json:"evidence_index_sha256,omitempty"`
	ArtifactTreeSHA256       string   `json:"artifact_tree_sha256,omitempty"`
	ReplayGateAuditSHA256    string   `json:"replay_gate_audit_sha256,omitempty"`
	SourceProvenanceSHA256   string   `json:"source_provenance_sha256,omitempty"`
	DisabledFamilies         []string `json:"disabled_families,omitempty"`
	TournamentScopeID        string   `json:"tournament_scope_id,omitempty"`
	TournamentScopeSHA256    string   `json:"tournament_scope_sha256,omitempty"`
	Cutoff                   string   `json:"cutoff,omitempty"`
	Trailing90Start          string   `json:"trailing_90_start,omitempty"`
	Trailing180Start         string   `json:"trailing_180_start,omitempty"`
	PatchID                  string   `json:"patch_id,omitempty"`
	DotaPatch                string   `json:"dota_patch,omitempty"`
}

func (v HistoryAvailabilityBindingV1) Validate() error {
	if v.SchemaVersion != HistoryAvailabilityBindingSchemaV1 {
		return schemaError(HistoryAvailabilityBindingSchemaV1, v.SchemaVersion)
	}
	switch v.Mode {
	case HistoryModeSnapshotBaseline:
		if !isSHA(v.HistoricalSnapshotID) || v.HistoricalSnapshotID != v.HistoricalSnapshotSHA256 || len(v.EligibleBaselineSHA256) == 0 || hasNoGoFields(v) {
			return errors.New("invalid snapshot history binding")
		}
		for i, h := range v.EligibleBaselineSHA256 {
			if !isSHA(h) || (i > 0 && h <= v.EligibleBaselineSHA256[i-1]) {
				return errors.New("baseline hashes not canonical")
			}
		}
	case HistoryModeNoGo:
		if v.HistoricalSnapshotID != "" || v.HistoricalSnapshotSHA256 != "" || len(v.EligibleBaselineSHA256) != 0 {
			return errors.New("historical no-go contains snapshot")
		}
		if v.TerminalOutcome != HistoricalNoGoOutcome || v.CodeFoundationCommit != AcceptedHistoryCodeCommit || v.EvidenceCommit != AcceptedHistoryEvidenceCommit || v.EvidenceIndexSHA256 != AcceptedEvidenceIndexSHA256 || v.ArtifactTreeSHA256 != AcceptedArtifactTreeSHA256 || v.ReplayGateAuditSHA256 != AcceptedReplayGateAuditSHA256 || v.SourceProvenanceSHA256 != AcceptedSourceProvenanceSHA256 || v.TournamentScopeID != AcceptedTournamentScopeID || v.TournamentScopeSHA256 != AcceptedTournamentScopeSHA256 || v.PatchID != "60" || v.DotaPatch != "7.41" || v.Cutoff != "2026-08-12T00:00:00Z" || v.Trailing90Start != "2026-05-14T00:00:00Z" || v.Trailing180Start != "2026-02-13T00:00:00Z" || !equalStrings(v.DisabledFamilies, HistoricalDisabledFamiliesV1) {
			return errors.New("historical no-go identity mismatch")
		}
	default:
		return errors.New("unknown history mode")
	}
	return validateSize(v, MaxHistoryBindingBytes)
}
func hasNoGoFields(v HistoryAvailabilityBindingV1) bool {
	return v.TerminalOutcome != "" || v.CodeFoundationCommit != "" || v.EvidenceCommit != "" || v.EvidenceIndexSHA256 != "" || v.ArtifactTreeSHA256 != "" || v.ReplayGateAuditSHA256 != "" || v.SourceProvenanceSHA256 != "" || len(v.DisabledFamilies) > 0 || v.TournamentScopeID != "" || v.TournamentScopeSHA256 != "" || v.Cutoff != "" || v.Trailing90Start != "" || v.Trailing180Start != "" || v.PatchID != "" || v.DotaPatch != ""
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (v HistoryAvailabilityBindingV1) ContentID() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	return CanonicalSHA256(v)
}
func (v HistoryAvailabilityBindingV1) MustContentID() string { id, _ := v.ContentID(); return id }
func DecodeCanonicalHistoryAvailabilityBindingV1(data []byte) (HistoryAvailabilityBindingV1, error) {
	var v HistoryAvailabilityBindingV1
	if len(data) > MaxHistoryBindingBytes {
		return v, errors.New("history binding size")
	}
	if err := DecodeStrict(data, &v); err != nil {
		return v, err
	}
	canonical, err := MarshalCanonical(v)
	if err != nil || !bytes.Equal(data, canonical) {
		return v, errors.New("non-canonical history binding")
	}
	return v, v.Validate()
}

type PolicyLineageManifestV3 struct {
	SchemaVersion                    string                   `json:"schema_version"`
	SessionID                        string                   `json:"session_id"`
	RawRecordSchema                  PolicyArtifactIdentityV2 `json:"raw_record_schema"`
	RawRecordFraming                 PolicyArtifactIdentityV2 `json:"raw_record_framing"`
	RawPayloadSchema                 PolicyArtifactIdentityV2 `json:"raw_payload_schema"`
	LiveObservationSchema            PolicyArtifactIdentityV2 `json:"live_observation_schema"`
	ProjectionMapping                PolicyArtifactIdentityV2 `json:"projection_mapping"`
	TournamentScopeID                string                   `json:"tournament_scope_id"`
	TournamentScopeSHA256            string                   `json:"tournament_scope_sha256"`
	HistoryAvailabilityBindingID     string                   `json:"history_availability_binding_id"`
	HistoryAvailabilityBindingSHA256 string                   `json:"history_availability_binding_sha256"`
	Rules                            PolicyArtifactIdentityV2 `json:"rules"`
	Config                           PolicyArtifactIdentityV2 `json:"config"`
	Catalog                          PolicyArtifactIdentityV2 `json:"catalog"`
	Terminology                      PolicyArtifactIdentityV2 `json:"terminology"`
	LocalizationParameterMapping     PolicyArtifactIdentityV2 `json:"localization_parameter_mapping"`
	EngineBuild                      PolicyArtifactIdentityV2 `json:"engine_build"`
}

func (v PolicyLineageManifestV3) Validate() error {
	if v.SchemaVersion != PolicyLineageManifestSchemaV3 || !validPolicyID(v.SessionID) || !isSHA(v.TournamentScopeID) || v.TournamentScopeID != v.TournamentScopeSHA256 || !isSHA(v.HistoryAvailabilityBindingID) || v.HistoryAvailabilityBindingID != v.HistoryAvailabilityBindingSHA256 {
		return errors.New("invalid policy lineage v3 identity")
	}
	for n, a := range map[string]PolicyArtifactIdentityV2{"raw_record_schema": v.RawRecordSchema, "raw_record_framing": v.RawRecordFraming, "raw_payload_schema": v.RawPayloadSchema, "live_observation_schema": v.LiveObservationSchema, "projection_mapping": v.ProjectionMapping, "rules": v.Rules, "config": v.Config, "catalog": v.Catalog, "terminology": v.Terminology, "localization": v.LocalizationParameterMapping, "engine": v.EngineBuild} {
		if err := a.validate(n); err != nil {
			return err
		}
	}
	return validateSize(v, MaxPolicyLineageManifestBytes)
}
func (v PolicyLineageManifestV3) ContentID() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	return CanonicalSHA256(v)
}
func (v PolicyLineageManifestV3) MustContentID() string { id, _ := v.ContentID(); return id }

type PolicyCommitV3 PolicyCommitV2

// V3 retains the reviewed V2 semantic indexes verbatim; only the durable
// lineage/commit/checkpoint envelope is versioned.
type PolicyCommandLocatorV3 = PolicyCommandLocatorV2
type PolicyCommandResultRefV3 = PolicyCommandResultRefV2

func ValidPolicyIdentifierV3(value string) bool { return ValidPolicyIdentifierV2(value) }

func (v PolicyCommitV3) Validate() error {
	if v.SchemaVersion != PolicyCommitSchemaV3 {
		return schemaError(PolicyCommitSchemaV3, v.SchemaVersion)
	}
	x := PolicyCommitV2(v)
	x.SchemaVersion = PolicyCommitSchemaV2
	return x.Validate()
}

type PolicyCheckpointV3 PolicyCheckpointV2

func (v PolicyCheckpointV3) StateProjection() PolicyStateV2 {
	x := PolicyCheckpointV2(v)
	return x.StateProjection()
}
func (v PolicyCheckpointV3) ComputeStateHash() (string, error) {
	return CanonicalSHA256(v.StateProjection())
}
func (v PolicyCheckpointV3) Validate() error {
	if v.SchemaVersion != PolicyCheckpointSchemaV3 {
		return schemaError(PolicyCheckpointSchemaV3, v.SchemaVersion)
	}
	x := PolicyCheckpointV2(v)
	x.SchemaVersion = PolicyCheckpointSchemaV2
	return x.Validate()
}
func (v PolicyCheckpointV3) ValidateAgainstCommit(c PolicyCommitV3, frame string) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if !isSHA(frame) || v.LineageManifestID != c.LineageManifestID || v.LineageManifestSHA256 != c.LineageManifestSHA256 || v.SessionID != c.SessionID || v.CommitSequence != c.CommitSequence || v.ReferencedCommitSHA256 != frame || v.PolicyRevision != c.ResultingPolicyRevision || v.StateHash != c.ResultingStateHash || v.LastObservationSequence != c.ResultingObservationSequence || v.LastPolicyTimeMS != c.ResultingPolicyTimeMS {
		return errors.New("policy checkpoint v3 commit anchor mismatch")
	}
	return nil
}

type HistoricalUnavailableFixtureV1 struct {
	SchemaVersion                    string            `json:"schema_version"`
	HistoryAvailabilityBindingID     string            `json:"history_availability_binding_id"`
	HistoryAvailabilityBindingSHA256 string            `json:"history_availability_binding_sha256"`
	LineageManifestID                string            `json:"lineage_manifest_id"`
	LineageManifestSHA256            string            `json:"lineage_manifest_sha256"`
	Observation                      LiveObservationV1 `json:"observation"`
	HistoryState                     string            `json:"history_state"`
	DisabledFamilies                 []string          `json:"disabled_families"`
}

func (v HistoricalUnavailableFixtureV1) Validate() error {
	if v.SchemaVersion != HistoricalUnavailableFixtureSchemaV1 || !isSHA(v.HistoryAvailabilityBindingID) || v.HistoryAvailabilityBindingID != v.HistoryAvailabilityBindingSHA256 || !isSHA(v.LineageManifestID) || v.LineageManifestID != v.LineageManifestSHA256 || v.HistoryState != "unavailable" || !equalStrings(v.DisabledFamilies, HistoricalDisabledFamiliesV1) || len(v.Observation.Participants) != 10 {
		return errors.New("invalid unavailable fixture")
	}
	if err := v.Observation.Validate(); err != nil {
		return fmt.Errorf("fixture observation: %w", err)
	}
	return validateSize(v, MaxHistoricalUnavailableFixtureBytes)
}
func (v HistoricalUnavailableFixtureV1) ContentID() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	return CanonicalSHA256(v)
}

type LiveOnlyReleaseBindingV1 struct {
	SchemaVersion                    string                   `json:"schema_version"`
	SourceCommit                     string                   `json:"source_commit"`
	LineageSchema                    string                   `json:"lineage_schema"`
	LineageManifestID                string                   `json:"lineage_manifest_id"`
	LineageManifestSHA256            string                   `json:"lineage_manifest_sha256"`
	HistoryAvailabilityBindingID     string                   `json:"history_availability_binding_id"`
	HistoryAvailabilityBindingSHA256 string                   `json:"history_availability_binding_sha256"`
	CodeFoundationCommit             string                   `json:"code_foundation_commit"`
	EvidenceCommit                   string                   `json:"evidence_commit"`
	EvidenceIndexSHA256              string                   `json:"evidence_index_sha256"`
	ArtifactTreeSHA256               string                   `json:"artifact_tree_sha256"`
	ReplayGateAuditSHA256            string                   `json:"replay_gate_audit_sha256"`
	SourceProvenanceSHA256           string                   `json:"source_provenance_sha256"`
	TournamentScopeID                string                   `json:"tournament_scope_id"`
	TournamentScopeSHA256            string                   `json:"tournament_scope_sha256"`
	Cutoff                           string                   `json:"cutoff"`
	Trailing90Start                  string                   `json:"trailing_90_start"`
	Trailing180Start                 string                   `json:"trailing_180_start"`
	PatchID                          string                   `json:"patch_id"`
	DotaPatch                        string                   `json:"dota_patch"`
	DisabledFamilies                 []string                 `json:"disabled_families"`
	Rules                            PolicyArtifactIdentityV2 `json:"rules"`
	Config                           PolicyArtifactIdentityV2 `json:"config"`
	Catalog                          PolicyArtifactIdentityV2 `json:"catalog"`
	Terminology                      PolicyArtifactIdentityV2 `json:"terminology"`
	LocalizationParameterMapping     PolicyArtifactIdentityV2 `json:"localization_parameter_mapping"`
}

func (v LiveOnlyReleaseBindingV1) Validate() error {
	if v.SchemaVersion != LiveOnlyReleaseBindingSchemaV1 || !validGitCommit(v.SourceCommit) || v.LineageSchema != PolicyLineageManifestSchemaV3 || !isSHA(v.LineageManifestID) || v.LineageManifestID != v.LineageManifestSHA256 || !isSHA(v.HistoryAvailabilityBindingID) || v.HistoryAvailabilityBindingID != v.HistoryAvailabilityBindingSHA256 || v.CodeFoundationCommit != AcceptedHistoryCodeCommit || v.EvidenceCommit != AcceptedHistoryEvidenceCommit || v.EvidenceIndexSHA256 != AcceptedEvidenceIndexSHA256 || v.ArtifactTreeSHA256 != AcceptedArtifactTreeSHA256 || v.ReplayGateAuditSHA256 != AcceptedReplayGateAuditSHA256 || v.SourceProvenanceSHA256 != AcceptedSourceProvenanceSHA256 || v.TournamentScopeID != AcceptedTournamentScopeID || v.TournamentScopeSHA256 != AcceptedTournamentScopeSHA256 || v.Cutoff != "2026-08-12T00:00:00Z" || v.Trailing90Start != "2026-05-14T00:00:00Z" || v.Trailing180Start != "2026-02-13T00:00:00Z" || v.PatchID != "60" || v.DotaPatch != "7.41" || !equalStrings(v.DisabledFamilies, HistoricalDisabledFamiliesV1) {
		return errors.New("invalid live-only release binding")
	}
	for n, a := range map[string]PolicyArtifactIdentityV2{"rules": v.Rules, "config": v.Config, "catalog": v.Catalog, "terminology": v.Terminology, "localization": v.LocalizationParameterMapping} {
		if err := a.validate(n); err != nil {
			return err
		}
	}
	return validateSize(v, MaxLiveOnlyReleaseBindingBytes)
}

func validGitCommit(v string) bool {
	if len(v) != 40 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func (v LiveOnlyReleaseBindingV1) ContentID() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	return CanonicalSHA256(v)
}
