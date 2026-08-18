package m4match

import (
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const (
	AcceptedRehearsalSpec      = "1d793bc3d1d38b3ce3fd9e97005a5d4e45e928b2"
	RehearsalPurpose           = "public_match_rehearsal"
	RehearsalMatchClass        = "public_match"
	RehearsalReady             = "REHEARSAL_READY"
	RehearsalRefused           = "REFUSED"
	RehearsalPreflightSchemaV1 = "rehearsal_preflight.v1"
	RehearsalTerminalSchemaV1  = "rehearsal_terminal.v1"
	RehearsalCoverageSchemaV1  = "field_coverage_delta.v1"
	MaxRehearsalRawBytes       = 64 << 20
	MaxRehearsalRawLineBytes   = 13_985_113
	MaxRehearsalRawRecords     = 4096
	MaxRehearsalCoverageFrames = 4096
	MaxRehearsalExecutions     = MaxRehearsalRawRecords * insight.MaxFamilySuppressionAudits
	RehearsalInstruction       = "After a separate independent review reports REHEARSAL_READY, manually join one publicly spectatable match before 0:00; this command cannot arm or accept P4/M4."
)

var rehearsalCheckRegistry = [...]string{
	"accepted_ancestry", "candidate_commit", "candidate_parent", "captured_schedule", "clean_tree",
	"draft_pr_head", "exclusive_listener", "remote_branch_head", "rehearsal_identity", "toolchain",
	"gsi_arm",
}

var rehearsalSuccessorV3Sources = []string{
	"83e7016996cfcee081fe85bc7c2f6628b7257b100b1aa14079778d3cd55456a0",
	"2604f5422188b697591666201c70c8a41729ea6418755d2c2a2847aa451adae9",
	"d86ba36f415cac9bac7e5bb2e8028c9f7bdd58a5f7bd0d819625d26abb1aef97",
	"ce8c0d14fadf78973cfea56f577dfb67da0aac43a85d729498065ffaace6b31d",
	"4d84be98a0371b49a2fc7e010cdda7bd89f94b482873bf129fdde7f9b727a7ec",
	"4177fd172df569cee7b20d22540f5e15c7ac1fd341bbfb54978ae16ac7952ea5",
	"61ebd6da3217ed953c854fbae28197690845b088efa16743fd74b724ffe9ccfa",
	"0b0603b383916abf6cb1e1e56943de872729880ca115d59e9179bda4f5592c66",
	"1c48565d32a7cd3d72fea43b26fdca1dc23ab11a128b23b1dbdaa1b282ecae97",
	"8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7",
	"022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435",
	"2c2e4d0ece8f89f3b2488d2db3109e4e1c2921ad2d165baf733855ecc9c6f08b",
	"042448dda532b56ebcb272d7b7774609eb044719a8398cadfd1441d50aa170ba",
	"fd1d0f801c3cc43f7dc367ac95083d335082b0bccdd42a35eedf7711c10029af",
	"5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771",
}

func rehearsalSuccessorArtifacts(sessionID string) (contracts.HistoryAvailabilityBindingV1, contracts.PolicyLineageManifestV3, contracts.LiveOnlyReleaseBindingV1, error) {
	history, lineage, release, err := liveArtifacts(sessionID)
	if err != nil {
		return history, lineage, release, err
	}
	engineSources := append(append([]string(nil), rehearsalSuccessorV3Sources...), insight.RulesArtifact().ContentSHA256)
	lineage.EngineBuild = sourceArtifact("dota2-ob.product.v3", engineSources...)
	lineageID, err := lineage.ContentID()
	if err != nil {
		return history, lineage, release, err
	}
	release.LineageManifestID = lineageID
	release.LineageManifestSHA256 = lineageID
	if err := release.ValidateAgainst(history, lineage); err != nil {
		return history, lineage, release, err
	}
	return history, lineage, release, nil
}

type RehearsalCheckV1 struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Code   string `json:"code"`
}

type RehearsalPreflightV1 struct {
	SchemaVersion      string             `json:"schema_version"`
	Purpose            string             `json:"purpose"`
	MatchClass         string             `json:"match_class"`
	AcceptedSpec       string             `json:"accepted_rehearsal_spec"`
	AcceptedP4Spec     string             `json:"accepted_p4_spec"`
	CandidateCommit    string             `json:"candidate_commit"`
	CandidateParent    string             `json:"candidate_parent"`
	SessionID          string             `json:"session_id"`
	ConsoleState       string             `json:"console_state"`
	HumanInstruction   string             `json:"human_instruction,omitempty"`
	Checks             []RehearsalCheckV1 `json:"checks"`
	ClaimsP4           bool               `json:"claims_p4"`
	QualifyingMatch    bool               `json:"qualifying_match"`
	AcceptanceEligible bool               `json:"acceptance_eligible"`
	AcceptanceGate     string             `json:"acceptance_gate"`
	RootOwnerSHA256    string             `json:"root_owner_sha256"`
	ArmSHA256          string             `json:"arm_sha256,omitempty"`
	GoExecutable       string             `json:"go_executable,omitempty"`
	GoExecutableSHA256 string             `json:"go_executable_sha256,omitempty"`
	PreflightSHA256    string             `json:"preflight_sha256,omitempty"`
}

type RehearsalArmV1 struct {
	SchemaVersion    string `json:"schema_version"`
	Purpose          string `json:"purpose"`
	SessionID        string `json:"session_id"`
	CandidateCommit  string `json:"candidate_commit"`
	RootOwnerSHA256  string `json:"root_owner_sha256"`
	ConfigSourcePath string `json:"config_source_path"`
	ConfigTargetPath string `json:"config_target_path"`
	ConfigSHA256     string `json:"config_sha256"`
	ConfigBytes      int64  `json:"config_bytes"`
	ConfigDevice     uint64 `json:"config_device"`
	ConfigInode      uint64 `json:"config_inode"`
	ArmToken         string `json:"arm_token,omitempty"`
}

type RehearsalRootOwnerV1 struct {
	SchemaVersion   string `json:"schema_version"`
	Purpose         string `json:"purpose"`
	SessionID       string `json:"session_id"`
	CandidateCommit string `json:"candidate_commit"`
	RawRelativePath string `json:"raw_relative_path"`
}

type FieldCoverageEntryV1 struct {
	Path                     string   `json:"path"`
	PathKind                 string   `json:"path_kind"`
	BaselinePresentFrames    uint64   `json:"baseline_present_frames"`
	BaselineNullFrames       uint64   `json:"baseline_null_frames"`
	BaselineOccurrenceCount  uint64   `json:"baseline_occurrence_count"`
	BaselineTypes            []string `json:"baseline_types"`
	RehearsalPresentFrames   uint64   `json:"rehearsal_present_frames"`
	RehearsalNullFrames      uint64   `json:"rehearsal_null_frames"`
	RehearsalOccurrenceCount uint64   `json:"rehearsal_occurrence_count"`
	RehearsalTypes           []string `json:"rehearsal_types"`
	Classification           string   `json:"classification"`
}

type FieldCoverageDeltaV1 struct {
	SchemaVersion     string                 `json:"schema_version"`
	Algorithm         string                 `json:"algorithm"`
	BaselineSHA256    string                 `json:"baseline_sha256"`
	RehearsalSpec     string                 `json:"rehearsal_spec"`
	RawManifestSHA256 string                 `json:"raw_manifest_sha256"`
	BaselineFrames    uint64                 `json:"baseline_frames"`
	FrameCount        uint64                 `json:"frame_count"`
	Entries           []FieldCoverageEntryV1 `json:"entries"`
	ContentSHA256     string                 `json:"content_sha256,omitempty"`
}

type FieldCoverageStatusV1 struct {
	State           string                `json:"state"`
	Reason          string                `json:"reason,omitempty"`
	AcceptedFrames  uint64                `json:"accepted_frames"`
	RawPrefixSHA256 string                `json:"raw_prefix_sha256"`
	Delta           *FieldCoverageDeltaV1 `json:"delta,omitempty"`
}

type RehearsalProcessObservationV1 struct {
	Boundary         string                  `json:"boundary"`
	RawSequence      uint64                  `json:"raw_sequence"`
	RawRecordSHA256  string                  `json:"raw_record_sha256,omitempty"`
	ObservedIdentity RehearsalDotaIdentityV1 `json:"observed_identity"`
}

type RehearsalOwnedProcessIdentityV1 struct {
	SchemaVersion        string `json:"schema_version"`
	PID                  int    `json:"pid"`
	Comm                 string `json:"comm"`
	ExecutablePath       string `json:"executable_path"`
	ExecutablePathSHA256 string `json:"executable_path_sha256"`
	ExecutableSHA256     string `json:"executable_sha256"`
	ProcessStartTicks    uint64 `json:"process_start_ticks"`
	ExecutableDevice     uint64 `json:"executable_device"`
	ExecutableInode      uint64 `json:"executable_inode"`
}

type RehearsalOwnedProcessObservationV1 struct {
	Boundary string                          `json:"boundary"`
	Identity RehearsalOwnedProcessIdentityV1 `json:"identity"`
}

type RehearsalSuppressionFrameV1 struct {
	RawSequence     uint64                             `json:"raw_sequence"`
	RawRecordSHA256 string                             `json:"raw_record_sha256"`
	Audits          []insight.FamilySuppressionAuditV1 `json:"audits"`
}

type RehearsalProducerStepV1 struct {
	Name      string `json:"name"`
	Started   bool   `json:"started"`
	Completed bool   `json:"completed"`
	ExitCode  int    `json:"exit_code"`
	Failure   string `json:"failure,omitempty"`
}

type RehearsalProducerArtifactV1 struct {
	Role    string `json:"role"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Records uint64 `json:"records,omitempty"`
}

type RehearsalProducerEvidenceV1 struct {
	SchemaVersion          string                               `json:"schema_version"`
	Purpose                string                               `json:"purpose"`
	SessionID              string                               `json:"session_id"`
	PreflightSHA256        string                               `json:"preflight_sha256"`
	RootOwnerSHA256        string                               `json:"root_owner_sha256"`
	RunID                  string                               `json:"run_id"`
	SourceMode             string                               `json:"source_mode"`
	Steps                  []RehearsalProducerStepV1            `json:"steps"`
	Artifacts              []RehearsalProducerArtifactV1        `json:"artifacts"`
	ProductPID             int                                  `json:"product_pid"`
	OBSPID                 int                                  `json:"obs_pid"`
	OBSLauncherPID         int                                  `json:"obs_flatpak_launcher_pid,omitempty"`
	OBSSandboxPID          int                                  `json:"obs_flatpak_sandbox_pid,omitempty"`
	OBSInstanceID          string                               `json:"obs_flatpak_instance_id,omitempty"`
	OBSExecutable          RehearsalOwnedProcessIdentityV1      `json:"obs_executable_identity,omitempty"`
	OBSProcesses           []RehearsalOwnedProcessIdentityV1    `json:"obs_process_population,omitempty"`
	OBSStartCorrelation    string                               `json:"obs_start_correlation_sha256,omitempty"`
	OBSTerminalCorrelation string                               `json:"obs_terminal_correlation_sha256,omitempty"`
	RecoveryPID            int                                  `json:"recovery_pid"`
	DotaContinuity         []RehearsalProcessObservationV1      `json:"dota_continuity"`
	RecoveryProcess        []RehearsalOwnedProcessObservationV1 `json:"recovery_process"`
	OperatorActions        []string                             `json:"operator_actions"`
	ResourceSamples        uint64                               `json:"resource_samples"`
	VisibilitySamples      uint64                               `json:"visibility_samples"`
	RawRecords             uint64                               `json:"raw_records"`
	RecordingFinalized     bool                                 `json:"recording_finalized"`
	Reconciled             bool                                 `json:"reconciled"`
	RecoveryByteEqual      bool                                 `json:"recovery_byte_equal"`
	CleanShutdown          bool                                 `json:"clean_shutdown"`
	PhysicalMatch          bool                                 `json:"physical_match"`
	ContentSHA256          string                               `json:"content_sha256,omitempty"`
}

type RehearsalTerminalV1 struct {
	SchemaVersion      string                          `json:"schema_version"`
	Purpose            string                          `json:"purpose"`
	MatchClass         string                          `json:"match_class"`
	AcceptedSpec       string                          `json:"accepted_rehearsal_spec"`
	AcceptedP4Spec     string                          `json:"accepted_p4_spec"`
	CandidateCommit    string                          `json:"candidate_commit"`
	CandidateParent    string                          `json:"candidate_parent"`
	SessionID          string                          `json:"session_id"`
	PreflightSHA256    string                          `json:"preflight_sha256"`
	Outcome            string                          `json:"outcome"`
	FailureCode        string                          `json:"failure_code,omitempty"`
	RawAdmission       session.RawAdmissionReceiptV1   `json:"raw_admission"`
	RawManifestSHA256  string                          `json:"raw_manifest_sha256"`
	Coverage           FieldCoverageStatusV1           `json:"coverage"`
	Suppression        []RehearsalSuppressionFrameV1   `json:"suppression"`
	Process            []RehearsalProcessObservationV1 `json:"process_identity"`
	Producer           *RehearsalProducerEvidenceV1    `json:"producer,omitempty"`
	ClaimsP4           bool                            `json:"claims_p4"`
	QualifyingMatch    bool                            `json:"qualifying_match"`
	AcceptanceEligible bool                            `json:"acceptance_eligible"`
	AcceptanceGate     string                          `json:"acceptance_gate"`
	CleanupToken       string                          `json:"cleanup_token,omitempty"`
}

type RehearsalAttemptResultV1 struct {
	Outcome        string `json:"outcome"`
	FailureCode    string `json:"failure_code,omitempty"`
	TerminalSHA256 string `json:"terminal_sha256"`
	CleanupToken   string `json:"cleanup_token"`
}

type RehearsalPreflightConfig struct {
	DataRoot string
	RepoRoot string
}

type RehearsalAttemptConfig struct {
	DataRoot     string
	RepoRoot     string
	OBSOnlySmoke bool
	identity     func() (RehearsalDotaIdentityV1, error)
	producer     rehearsalLifecycleDriver
}

type rehearsalClock func() time.Time
