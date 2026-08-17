package m4match

import "time"

const (
	SchemaVersion                   = "m4_p4_evidence_index.v4"
	ReadinessSchemaVersion          = "m4_p4_readiness.v4"
	RehearsalSchemaVersion          = "m4_public_match_rehearsal_evidence.v1"
	RehearsalReadinessSchemaVersion = "m4_public_match_rehearsal_readiness.v1"
	AcceptedFunctionalBase          = "fa5e7c308ee272722469499ae227b99d1644e2ac"
	RequiredSuccessorParent         = "abb4210257f26364c2a539de90e386f411f3229a"
	AcceptedP4Spec                  = "ee91e97b9dcbe5e94845256d538ff01857e89e46"
	CapturedScheduleSHA256          = "2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e"
	ProductionGoldenSHA256          = "480ef715e7c04c22315ba8d81369708a430abe6706bc29a221220b813c0275bd"
	HumanInstruction                = "1. Manually launch Dota 2 within the assigned start window (maximum 30 minutes). 2. After the agent reports ARMED, join the identified authority-verified public tournament DotaTV game before 0:00 and confirm its tournament, series, game, teams, and match ID. 3. Execute the prescribed operator script: confirm preview; approve or record the prescribed deterministic ineligible terminal result; reject or record it; pin then unpin when eligible; emergency-hide then clear. 4. Remain through normal post-game and recording finalization. Stop and abort immediately for a late join, identity contradiction, or agent-reported failure."
	RehearsalHumanInstruction       = "1. Manually launch Dota 2 within the assigned start window (maximum 30 minutes). 2. After the agent reports REHEARSAL_READY, join the identified public DotaTV match before 0:00 and confirm its public match ID. 3. Execute the prescribed operator script: confirm preview; approve or record the prescribed deterministic ineligible terminal result; reject or record it; pin then unpin when eligible; emergency-hide then clear. 4. Remain through normal post-game and recording finalization. Stop and abort immediately for a late join, identity contradiction, or agent-reported failure."
	CaptureAddress                  = "127.0.0.1:43910"
	DeliveryAddress                 = "127.0.0.1:43911"
	ProductDeliveryAddress          = "127.0.0.1:43912"
	CaptureOrigin                   = "http://" + CaptureAddress
	DeliveryOrigin                  = "http://" + DeliveryAddress
	ProductDeliveryOrigin           = "http://" + ProductDeliveryAddress
	ExpectedRemoteURL               = "https://github.com/PaulOctopusZLWB/dota2-ob.git"
	ExpectedBranch                  = "agent/dota2-fullstack-engineer/DOT-65-ti-match-harness"
	ExpectedPR                      = "19"
	MaxArmingWindow                 = 30 * time.Minute
)

var RequiredFaults = []string{
	"audit_failure", "cache_independent_rebuild", "candidate_queue_saturation", "checkpoint_partial_frame",
	"cursor_loss", "gsi_body_bounds", "operator_command_revision_and_replay", "orderly_restart",
	"out_of_order_delivery", "overlay_disconnect", "overlay_state_bounds", "partial_policy_frame",
	"partial_raw_tail", "product_sigkill_restart", "stale_input", "two_second_fail_closed",
}

type Check struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Environment struct {
	GOOS                string `json:"goos"`
	GOARCH              string `json:"goarch"`
	Go                  string `json:"go"`
	Node                string `json:"node"`
	NPM                 string `json:"npm"`
	Zig                 string `json:"zig"`
	Kernel              string `json:"kernel"`
	Steam               string `json:"steam"`
	DotaBuild           string `json:"dota_build"`
	DotaBinary          string `json:"dota_binary_sha256"`
	OBSVersion          string `json:"obs_version"`
	OBSCommit           string `json:"obs_flatpak_commit"`
	OBSBinary           string `json:"obs_binary_version"`
	GPUDriver           string `json:"gpu_driver"`
	ClockTicksPerSecond string `json:"clock_ticks_per_second"`
}

type CandidateIdentity struct {
	Commit              string       `json:"commit"`
	SoleParent          string       `json:"sole_parent"`
	RepositoryRootSHA   string       `json:"repository_root_sha256"`
	RemoteURL           string       `json:"remote_url"`
	RemoteBranchCommit  string       `json:"remote_branch_commit"`
	PRHeadCommit        string       `json:"pr_head_commit"`
	BinarySHA256        string       `json:"binary_sha256"`
	BinaryVCSRevision   string       `json:"binary_vcs_revision"`
	BinaryVCSModified   bool         `json:"binary_vcs_modified"`
	HarnessVCSRevision  string       `json:"harness_vcs_revision"`
	HarnessVCSModified  bool         `json:"harness_vcs_modified"`
	EnvironmentSHA256   string       `json:"environment_sha256"`
	RunPurpose          RunPurposeV1 `json:"run_purpose"`
	MatchClass          MatchClassV1 `json:"match_class"`
	AcceptedAmendment   string       `json:"accepted_amendment_commit"`
	AuthorityRootSHA256 string       `json:"authority_root_sha256"`
}

type CandidateIdentitySubcheck struct {
	ID         string `json:"id"`
	Passed     bool   `json:"passed"`
	ReasonCode string `json:"reason_code"`
}

type CandidateRepositorySnapshot struct {
	Commit             string `json:"commit"`
	SoleParent         string `json:"sole_parent"`
	RepositoryRootSHA  string `json:"repository_root_sha256"`
	RemoteURL          string `json:"remote_url"`
	RemoteBranchCommit string `json:"remote_branch_commit"`
	PRHeadCommit       string `json:"pr_head_commit"`
}

type CandidateIdentityEvidence struct {
	Checks              []CandidateIdentitySubcheck `json:"checks"`
	Start               CandidateRepositorySnapshot `json:"start"`
	End                 CandidateRepositorySnapshot `json:"end"`
	BinarySHA256        string                      `json:"binary_sha256"`
	BinaryVCSRevision   string                      `json:"binary_vcs_revision"`
	BinaryVCSModified   bool                        `json:"binary_vcs_modified"`
	HarnessVCSRevision  string                      `json:"harness_vcs_revision"`
	HarnessVCSModified  bool                        `json:"harness_vcs_modified"`
	EnvironmentSHA256   string                      `json:"environment_sha256"`
	RunPurpose          RunPurposeV1                `json:"run_purpose"`
	MatchClass          MatchClassV1                `json:"match_class"`
	AcceptedAmendment   string                      `json:"accepted_amendment_commit"`
	AuthorityRootSHA256 string                      `json:"authority_root_sha256"`
}

type Bounds struct {
	SampleIntervalSeconds    int   `json:"sample_interval_seconds"`
	NotificationCapacity     int   `json:"notification_capacity"`
	CandidateQueueCapacity   int   `json:"candidate_queue_capacity"`
	GSIRequestBytes          int64 `json:"gsi_request_bytes"`
	OtherAPIBytes            int64 `json:"other_api_bytes"`
	OverlayStateBytes        int64 `json:"overlay_state_bytes"`
	CombinedRSSBytes         int64 `json:"combined_rss_bytes"`
	RecoveryRSSBytes         int64 `json:"recovery_rss_bytes"`
	PostWarmupRSSGrowthBytes int64 `json:"post_warmup_rss_growth_bytes"`
	GoroutineDelta           int   `json:"goroutine_delta"`
	OpenFDs                  int   `json:"open_fds"`
	OpenFDDelta              int   `json:"open_fd_delta"`
	PolicyBytes              int64 `json:"policy_bytes"`
	PolicySegments           int   `json:"policy_segments"`
	NonRawFiles              int   `json:"non_raw_files"`
	NonRawBytes              int64 `json:"non_raw_bytes"`
	OperationalLogFiles      int   `json:"operational_log_files"`
	OperationalLogBytes      int64 `json:"operational_log_bytes"`
	FailClosedDeadlineMillis int   `json:"fail_closed_deadline_ms"`
}

func AcceptedBounds() Bounds {
	return Bounds{
		SampleIntervalSeconds: 5, NotificationCapacity: 1, CandidateQueueCapacity: 64,
		GSIRequestBytes: 10 << 20, OtherAPIBytes: 1 << 20, OverlayStateBytes: 64 << 10,
		CombinedRSSBytes: 384 << 20, RecoveryRSSBytes: 192 << 20, PostWarmupRSSGrowthBytes: 64 << 20,
		GoroutineDelta: 10, OpenFDs: 128, OpenFDDelta: 16, PolicyBytes: 1 << 30, PolicySegments: 104,
		NonRawFiles: 192, NonRawBytes: (16 << 30) / 10, OperationalLogFiles: 10, OperationalLogBytes: 100 << 20,
		FailClosedDeadlineMillis: 2000,
	}
}

func RuntimeCapacityAccepted(schema string, notification, candidate int, healthy bool) bool {
	bounds := AcceptedBounds()
	return schema == "runtime_capacity.v2" && healthy && notification == bounds.NotificationCapacity && candidate == bounds.CandidateQueueCapacity
}

type Evidence struct {
	SchemaVersion                 string                    `json:"schema_version"`
	Mode                          string                    `json:"mode"`
	CandidateCommit               string                    `json:"candidate_commit"`
	CandidateParent               string                    `json:"candidate_parent"`
	CandidateIdentity             CandidateIdentity         `json:"candidate_identity"`
	CandidateIdentityEvidence     CandidateIdentityEvidence `json:"candidate_identity_evidence"`
	AcceptedBase                  string                    `json:"accepted_functional_base"`
	AcceptedSpec                  string                    `json:"accepted_p4_spec"`
	FixtureSHA256                 string                    `json:"captured_schedule_sha256"`
	GoldenSHA256                  string                    `json:"production_golden_sha256"`
	Environment                   Environment               `json:"environment"`
	Bounds                        Bounds                    `json:"bounds"`
	Checks                        []Check                   `json:"checks"`
	Faults                        []string                  `json:"faults_exercised"`
	Artifacts                     []Artifact                `json:"prepared_artifacts"`
	StartBoundary                 string                    `json:"start_boundary"`
	GameZeroBoundary              string                    `json:"game_clock_zero_boundary"`
	PostGameBoundary              string                    `json:"post_game_boundary"`
	RecordingBoundary             string                    `json:"recording_finalization_boundary"`
	OperatorScript                string                    `json:"operator_script"`
	NonResumable                  bool                      `json:"non_resumable_acceptance"`
	SyntheticOnly                 bool                      `json:"synthetic_only"`
	ClaimsP4                      bool                      `json:"claims_p4"`
	ReadinessIssueText            string                    `json:"readiness_issue_instruction"`
	LocalhostGSI                  LocalhostGSITrustEvidence `json:"localhost_gsi_trust"`
	RunPurpose                    RunPurposeV1              `json:"run_purpose"`
	MatchClass                    MatchClassV1              `json:"match_class"`
	RootDomain                    string                    `json:"root_domain"`
	QualifyingMatch               bool                      `json:"qualifying_match"`
	AcceptanceEligible            bool                      `json:"acceptance_eligible"`
	AcceptanceGate                string                    `json:"acceptance_gate"`
	AuthorityRootSHA256           string                    `json:"authority_root_sha256,omitempty"`
	AuthorityMatchID              string                    `json:"authority_match_id,omitempty"`
	AuthorityReadinessIndexSHA256 string                    `json:"authority_readiness_index_sha256,omitempty"`
	AuthoritySelectionSHA256      string                    `json:"authority_selection_sha256,omitempty"`
	AuthorityPreflightSHA256      string                    `json:"authority_preflight_sha256,omitempty"`
	TerminalOutcome               string                    `json:"terminal_outcome"`
}

type LocalhostGSITrustEvidence struct {
	ConfigSHA256           string   `json:"config_sha256,omitempty"`
	ConfigURI              string   `json:"config_uri,omitempty"`
	ConfigUserOnly         bool     `json:"config_user_only"`
	ExclusiveListener      bool     `json:"exclusive_listener"`
	ListenerProductOwned   bool     `json:"listener_product_owned"`
	ListenerURI            string   `json:"listener_uri,omitempty"`
	DotaProcessStable      bool     `json:"dota_process_stable"`
	KnownProducerAbsent    bool     `json:"known_producer_absent"`
	CorrelationStartSHA256 string   `json:"correlation_start_sha256,omitempty"`
	CorrelationEndSHA256   string   `json:"correlation_end_sha256,omitempty"`
	IdentityContinuous     bool     `json:"identity_continuous"`
	Requests               uint64   `json:"requests"`
	Accepted               uint64   `json:"accepted"`
	Rejected               uint64   `json:"rejected"`
	RawRecords             uint64   `json:"raw_records"`
	TerminalOutcomes       uint64   `json:"terminal_outcomes"`
	RecordingCoextensive   bool     `json:"recording_coextensive"`
	PaulConfirmedIdentity  bool     `json:"paul_confirmed_identity"`
	PerRequestAttested     bool     `json:"per_request_sender_attested"`
	ResidualReasonCodes    []string `json:"residual_reason_codes"`
}

type Readiness struct {
	SchemaVersion           string       `json:"schema_version"`
	Ready                   bool         `json:"ready"`
	Mode                    string       `json:"mode"`
	CandidateCommit         string       `json:"candidate_commit"`
	CandidateIdentitySHA256 string       `json:"candidate_identity_sha256"`
	EnvironmentSHA256       string       `json:"environment_sha256"`
	EvidenceIndexSHA256     string       `json:"evidence_index_sha256"`
	Failures                []string     `json:"failures"`
	ClaimsP4                bool         `json:"claims_p4"`
	HumanInstruction        string       `json:"human_instruction"`
	RunPurpose              RunPurposeV1 `json:"run_purpose"`
	MatchClass              MatchClassV1 `json:"match_class"`
	RootDomain              string       `json:"root_domain"`
	QualifyingMatch         bool         `json:"qualifying_match"`
	AcceptanceEligible      bool         `json:"acceptance_eligible"`
	AcceptanceGate          string       `json:"acceptance_gate"`
	ConsoleState            string       `json:"console_state"`
}

// PublicMatchRehearsalEvidenceV1 is deliberately a distinct wire contract.
// The extra required contract member makes discriminator editing insufficient
// to turn rehearsal evidence into P4 evidence; both verifiers decode strictly.
type PublicMatchRehearsalEvidenceV1 struct {
	Evidence
	RehearsalContractVersion string                       `json:"rehearsal_contract_version"`
	FieldCoverage            *RehearsalCoverageBindingV1  `json:"field_coverage,omitempty"`
	AcceptanceChecks         []RehearsalAcceptanceCheckV1 `json:"acceptance_checks"`
	SuppressionAudits        []SuppressionAuditV1         `json:"suppression_audits"`
}

type RehearsalCoverageBindingV1 struct {
	ArtifactPath string `json:"artifact_path"`
	SHA256       string `json:"sha256"`
	RootSHA256   string `json:"root_sha256"`
}

type RehearsalAcceptanceCheckV1 struct {
	ID    string              `json:"id"`
	State TypedAvailabilityV1 `json:"state"`
}

type PublicMatchRehearsalReadinessV1 struct {
	Readiness
	RehearsalContractVersion string `json:"rehearsal_contract_version"`
	TerminalOutcome          string `json:"terminal_outcome,omitempty"`
}

type LiveIdentity struct {
	Tournament            string `json:"tournament"`
	Series                string `json:"series"`
	Game                  string `json:"game"`
	Radiant               string `json:"radiant"`
	Dire                  string `json:"dire"`
	MatchID               string `json:"match_id,omitempty"`
	OfficialSourceURL     string `json:"official_source_url"`
	ConfirmedAt           string `json:"confirmed_at"`
	DotaPID               int    `json:"dota_pid"`
	DotaExecutableSHA256  string `json:"dota_executable_sha256"`
	DotaProcessStartTicks uint64 `json:"dota_process_start_ticks"`
}

type Sample struct {
	At                          time.Time `json:"at"`
	Sequence                    uint64    `json:"sequence"`
	ProjectedSequence           uint64    `json:"projected_sequence"`
	Lag                         uint64    `json:"lag"`
	RequestCount                uint64    `json:"request_count"`
	AcceptedCount               uint64    `json:"accepted_count"`
	RejectedCount               uint64    `json:"rejected_count"`
	RawWriteFailures            uint64    `json:"raw_write_failures"`
	ProjectionFailures          uint64    `json:"projection_failures"`
	QueueOccupancy              int       `json:"queue_occupancy"`
	ProductGoroutines           int       `json:"product_goroutines"`
	RawFiles                    int       `json:"raw_files"`
	RawBytes                    int64     `json:"raw_bytes"`
	NonRawFiles                 int       `json:"non_raw_files"`
	NonRawBytes                 int64     `json:"non_raw_bytes"`
	ProcessRSSBytes             int64     `json:"process_rss_bytes"`
	ProcessCPUClockTicks        uint64    `json:"process_cpu_clock_ticks"`
	ProcessFDs                  int       `json:"process_fds"`
	ProcessThreads              int       `json:"process_threads"`
	PolicySegments              int       `json:"policy_segments"`
	PolicyBytes                 int64     `json:"policy_bytes"`
	StatusResponseBytes         int       `json:"status_response_bytes"`
	OperatorResponseBytes       int       `json:"operator_response_bytes"`
	OverlayResponseBytes        int       `json:"overlay_response_bytes"`
	OperatorStateSHA256         string    `json:"operator_state_sha256,omitempty"`
	OverlayStateSHA256          string    `json:"overlay_state_sha256,omitempty"`
	OBSRenderedFrames           uint64    `json:"obs_rendered_frames,omitempty"`
	OBSMissedFrames             uint64    `json:"obs_missed_frames,omitempty"`
	OBSSkippedFrames            uint64    `json:"obs_skipped_frames,omitempty"`
	OBSProcessRSSBytes          int64     `json:"obs_process_rss_bytes"`
	OBSProcessFDs               int       `json:"obs_process_fds"`
	TelemetryComplete           bool      `json:"telemetry_complete"`
	ProductTreeProcesses        int       `json:"product_tree_processes"`
	OBSProcessTreeCount         int       `json:"obs_process_tree_count"`
	ProductTreeRSSBytes         int64     `json:"product_tree_rss_bytes"`
	OBSProcessTreeRSSBytes      int64     `json:"obs_process_tree_rss_bytes"`
	ProductTreeFDs              int       `json:"product_tree_fds"`
	OBSProcessTreeFDs           int       `json:"obs_process_tree_fds"`
	ProductTreeCPUClockTicks    uint64    `json:"product_tree_cpu_clock_ticks"`
	OBSProcessTreeCPUClockTicks uint64    `json:"obs_process_tree_cpu_clock_ticks"`
	OBSBrowserProcesses         int       `json:"obs_browser_processes"`
	OBSBrowserCPUClockTicks     uint64    `json:"obs_browser_cpu_clock_ticks"`
	ClockTicksPerSecond         int       `json:"clock_ticks_per_second"`
	NotificationCapacity        int       `json:"notification_capacity"`
	CandidateQueueCapacity      int       `json:"candidate_queue_capacity"`
	LargestGSIRequestBytes      int64     `json:"largest_gsi_request_bytes"`
	RecoveryRSSBytes            int64     `json:"recovery_rss_bytes"`
	OverlayVisibleClaims        int       `json:"overlay_visible_claims"`
	FailClosedMillis            int64     `json:"fail_closed_millis"`
	CrossPlaneSessionID         string    `json:"cross_plane_session_id"`
	CrossPlaneMatchID           string    `json:"cross_plane_match_id"`
}

type VisibilitySample struct {
	At                time.Time `json:"at"`
	RawSequence       uint64    `json:"raw_sequence"`
	SessionID         string    `json:"session_id"`
	Visibility        string    `json:"visibility"`
	HealthCode        string    `json:"health_code,omitempty"`
	DecisionID        string    `json:"decision_id,omitempty"`
	ClaimPresent      bool      `json:"claim_present"`
	StateSHA256       string    `json:"state_sha256"`
	TelemetryComplete bool      `json:"telemetry_complete"`
}
