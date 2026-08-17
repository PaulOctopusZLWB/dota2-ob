package m4match

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const (
	AcceptedRehearsalSpec           = "958c3f0d3fd464df4960905bc4bb7918521884e6"
	RequiredRehearsalParent         = "abb4210257f26364c2a539de90e386f411f3229a"
	RehearsalEvidenceSchemaVersion  = "public_match_rehearsal_evidence.v2"
	RehearsalReadinessSchemaVersion = "public_match_rehearsal_readiness.v2"
	RehearsalAttemptSchemaVersion   = "public_match_rehearsal_attempt.v2"
	RehearsalTerminalSchemaVersion  = "public_match_rehearsal_terminal.v2"
	RehearsalReceiptSchemaVersion   = "public_match_rehearsal_receipt.v2"
	RehearsalRawManifestSchema      = "public_match_rehearsal_raw_manifest.v2"
	RehearsalSuppressionSchema      = "public_match_suppression_audit.v2"
	rehearsalRootDomain             = "dota2-ob.m4.public-match-rehearsal.v2"
	ExpectedRehearsalBranch         = "agent/dota2-fullstack-engineer/DOT-84-rehearsal-producer-v5"
	RehearsalHumanInstruction       = "After REHEARSAL_READY, Paul may manually choose and join one publicly spectatable public match before 0:00. This rehearsal never arms or advances P4/M4 acceptance."
	rehearsalRefusedConsoleState    = "REFUSED"
	rehearsalReadyConsoleState      = "REHEARSAL_READY"
	MaxRehearsalRawBytes            = 64 << 20
	MaxRehearsalRawLineBytes        = (((10 << 20) + 2) / 3 * 4) + (4 << 10) + 1 // accepted V3 frame plus newline
	MaxRehearsalRecords             = 4096
	MaxRehearsalCoverageFrames      = MaxRehearsalRecords
	MaxRehearsalExecutions          = MaxRehearsalRecords * 8
	MaxRehearsalAudits              = MaxRehearsalExecutions
	MaxRehearsalArtifactBytes       = 32 << 20
)

var rehearsalCheckRegistry = [...]RehearsalCheckV1{
	{ID: "candidate_commit", State: "dynamic"},
	{ID: "candidate_sole_parent", State: "dynamic"},
	{ID: "candidate_tree", State: "dynamic"},
	{ID: "clean_tree", State: "dynamic"},
	{ID: "draft_pr_head", State: "dynamic"},
	{ID: "exclusive_localhost_gsi_listener", State: "dynamic"},
	{ID: "expected_remote", State: "dynamic"},
	{ID: "p4_acceptance", State: "not_applicable_rehearsal"},
	{ID: "professional_tournament_authority", State: "not_applicable_rehearsal"},
	{ID: "remote_branch_head", State: "dynamic"},
}

type RehearsalPurposeV1 string
type RehearsalMatchClassV1 string

const (
	PurposePublicMatchRehearsal RehearsalPurposeV1    = "public_match_rehearsal"
	MatchClassPublicMatch       RehearsalMatchClassV1 = "public_match"
)

type RehearsalClassificationV1 struct {
	Purpose RehearsalPurposeV1    `json:"run_purpose"`
	Class   RehearsalMatchClassV1 `json:"match_class"`
}

func ParseRehearsalClassification(purpose, class string) (RehearsalClassificationV1, error) {
	classification := RehearsalClassificationV1{Purpose: RehearsalPurposeV1(purpose), Class: RehearsalMatchClassV1(class)}
	if classification.Purpose != PurposePublicMatchRehearsal || classification.Class != MatchClassPublicMatch {
		return RehearsalClassificationV1{}, errors.New("only public_match_rehearsal + public_match is legal")
	}
	return classification, nil
}

type RehearsalIdentityV1 struct {
	AcceptedRehearsalSpec string                    `json:"accepted_rehearsal_spec"`
	Classification        RehearsalClassificationV1 `json:"classification"`
	RootDomain            string                    `json:"root_domain"`
	ClaimsP4              bool                      `json:"claims_p4"`
	QualifyingMatch       bool                      `json:"qualifying_match"`
	AcceptanceEligible    bool                      `json:"acceptance_eligible"`
	AcceptanceGate        string                    `json:"acceptance_gate"`
}

func acceptedRehearsalIdentity() RehearsalIdentityV1 {
	classification, _ := ParseRehearsalClassification(string(PurposePublicMatchRehearsal), string(MatchClassPublicMatch))
	return RehearsalIdentityV1{AcceptedRehearsalSpec: AcceptedRehearsalSpec, Classification: classification, RootDomain: rehearsalRootDomain, AcceptanceGate: "none"}
}

type PublicMatchUnavailableIdentityV1 struct {
	Competition string `json:"competition"`
	League      string `json:"league"`
	Series      string `json:"series"`
	GameNumber  string `json:"game_number"`
	Teams       string `json:"teams"`
	Roster      string `json:"roster_history"`
	Organizer   string `json:"organizer"`
	TIIdentity  string `json:"ti_identity"`
}

func unavailablePublicMatchIdentity() PublicMatchUnavailableIdentityV1 {
	return PublicMatchUnavailableIdentityV1{
		Competition: "unavailable_for_public_match", League: "unavailable_for_public_match",
		Series: "unavailable_for_public_match", GameNumber: "unavailable_for_public_match",
		Teams: "unavailable_for_public_match", Roster: "unavailable_for_public_match",
		Organizer: "unavailable_for_public_match", TIIdentity: "unavailable_for_public_match",
	}
}

type RehearsalCheckV1 struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type RehearsalEvidenceV1 struct {
	SchemaVersion       string                           `json:"schema_version"`
	Mode                string                           `json:"mode"`
	Identity            RehearsalIdentityV1              `json:"identity"`
	CandidateCommit     string                           `json:"candidate_commit"`
	CandidateSoleParent string                           `json:"candidate_sole_parent"`
	CandidateTree       string                           `json:"candidate_tree"`
	RemoteURL           string                           `json:"remote_url"`
	RemoteBranch        string                           `json:"remote_branch"`
	RemoteBranchCommit  string                           `json:"remote_branch_commit"`
	DraftPRNumber       int                              `json:"draft_pr_number"`
	DraftPRURL          string                           `json:"draft_pr_url"`
	DraftPRHeadCommit   string                           `json:"draft_pr_head_commit"`
	CaptureOwnerSHA256  string                           `json:"capture_owner_sha256"`
	UnavailableIdentity PublicMatchUnavailableIdentityV1 `json:"unavailable_identity"`
	Checks              []RehearsalCheckV1               `json:"checks"`
	NonResumable        bool                             `json:"non_resumable"`
}

type RehearsalReadinessV1 struct {
	SchemaVersion       string              `json:"schema_version"`
	Ready               bool                `json:"ready"`
	ConsoleState        string              `json:"console_state"`
	Identity            RehearsalIdentityV1 `json:"identity"`
	CandidateCommit     string              `json:"candidate_commit"`
	EvidenceIndexSHA256 string              `json:"evidence_index_sha256"`
	CaptureOwnerSHA256  string              `json:"capture_owner_sha256"`
	Failures            []string            `json:"failures"`
	HumanInstruction    string              `json:"human_instruction,omitempty"`
}

type RehearsalCaptureOwnerV1 struct {
	SchemaVersion       string `json:"schema_version"`
	CandidateCommit     string `json:"candidate_commit"`
	SessionID           string `json:"session_id"`
	RawRelativePath     string `json:"raw_relative_path"`
	HarnessRelativePath string `json:"harness_relative_path"`
	SealRelativePath    string `json:"seal_relative_path"`
}

type RehearsalPreflightConfig struct {
	DataRoot string
	RepoRoot string
	Listen   func(string) (io.Closer, error)
	runText  func(context.Context, string, string, ...string) (string, error)
	readPR   func(context.Context, string) (rehearsalPR, error)
}

func RehearsalPreflight(ctx context.Context, config RehearsalPreflightConfig) (RehearsalReadinessV1, error) {
	run := config.runText
	if run == nil {
		run = runText
	}
	readPR := config.readPR
	if readPR == nil {
		readPR = rehearsalPRSnapshot
	}
	lease, err := acquireFreshRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	defer lease.Close()
	if _, err := Prepare(lease.abs, 1920, 1080); err != nil {
		return RehearsalReadinessV1{}, err
	}
	head, headErr := run(ctx, config.RepoRoot, "git", "rev-parse", "HEAD")
	parents, parentErr := run(ctx, config.RepoRoot, "git", "show", "-s", "--format=%P", "HEAD")
	tree, treeErr := run(ctx, config.RepoRoot, "git", "rev-parse", "HEAD^{tree}")
	status, statusErr := run(ctx, config.RepoRoot, "git", "status", "--porcelain=v1", "--untracked-files=all")
	remoteURL, remoteErr := run(ctx, config.RepoRoot, "git", "remote", "get-url", "origin")
	remoteOutput, remoteBranchErr := run(ctx, config.RepoRoot, "git", "ls-remote", "--heads", "origin", "refs/heads/"+ExpectedRehearsalBranch)
	remoteCommit := firstField(remoteOutput)
	pr, prErr := readPR(ctx, config.RepoRoot)
	listenerOK := rehearsalListenerAvailable(config.Listen, CaptureAddress)
	checks := []RehearsalCheckV1{
		{ID: "candidate_commit", State: checkState(headErr == nil && validGitObjectID(head))},
		{ID: "candidate_sole_parent", State: checkState(parentErr == nil && parents == RequiredRehearsalParent && len(strings.Fields(parents)) == 1)},
		{ID: "candidate_tree", State: checkState(treeErr == nil && validGitObjectID(tree))},
		{ID: "clean_tree", State: checkState(statusErr == nil && status == "")},
		{ID: "expected_remote", State: checkState(remoteErr == nil && remoteURL == ExpectedRemoteURL)},
		{ID: "remote_branch_head", State: checkState(remoteBranchErr == nil && remoteCommit == head)},
		{ID: "draft_pr_head", State: checkState(prErr == nil && pr.IsDraft && pr.HeadRefOID == head && pr.Number > 0 && pr.URL != "")},
		{ID: "exclusive_localhost_gsi_listener", State: checkState(listenerOK)},
		{ID: "p4_acceptance", State: "not_applicable_rehearsal"},
		{ID: "professional_tournament_authority", State: "not_applicable_rehearsal"},
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	sessionID := "dot84-" + firstN(head, 12)
	owner := RehearsalCaptureOwnerV1{
		SchemaVersion: "public_match_rehearsal_capture_owner.v1", CandidateCommit: head, SessionID: sessionID,
		RawRelativePath:     "data/sessions/" + sessionID + "/raw.jsonl",
		HarnessRelativePath: "data/sessions/" + sessionID + "/harness-evidence.json",
		SealRelativePath:    "data/sessions/" + sessionID + "/capture-seal.json",
	}
	ownerPayload, err := canonical(owner)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	store, err := session.NewStore(filepath.Join(lease.abs, "data/sessions"), session.WithSessionID(sessionID))
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	if err := store.Close(); err != nil {
		return RehearsalReadinessV1{}, err
	}
	binary := filepath.Join(lease.abs, "application/dota2-ob")
	build := runLogged(ctx, config.RepoRoot, filepath.Join(lease.abs, "evidence/logs/rehearsal-build.log"), "go", "build", "-trimpath", "-ldflags=-buildid=", "-o", binary, "./cmd/dota2-ob")
	if build.err != nil {
		return RehearsalReadinessV1{}, build.err
	}
	if _, err := writeLiveArtifacts(lease.abs, sessionID); err != nil {
		return RehearsalReadinessV1{}, err
	}
	if err := writePrivate(filepath.Join(lease.abs, "capture/owner.json"), ownerPayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	evidence := RehearsalEvidenceV1{
		SchemaVersion: RehearsalEvidenceSchemaVersion, Mode: "preflight", Identity: acceptedRehearsalIdentity(),
		CandidateCommit: head, CandidateSoleParent: parents, CandidateTree: tree,
		RemoteURL: remoteURL, RemoteBranch: ExpectedRehearsalBranch, RemoteBranchCommit: remoteCommit,
		DraftPRNumber: pr.Number, DraftPRURL: pr.URL, DraftPRHeadCommit: pr.HeadRefOID, CaptureOwnerSHA256: payloadSHA(ownerPayload),
		UnavailableIdentity: unavailablePublicMatchIdentity(), Checks: checks, NonResumable: true,
	}
	evidencePayload, err := canonical(evidence)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	if err := writePrivate(filepath.Join(lease.abs, "evidence/canonical/evidence-index.json"), evidencePayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	readiness := buildRehearsalReadiness(evidence, evidencePayload)
	if err := writeJSON(filepath.Join(lease.abs, "evidence/readiness.json"), readiness, 0o600); err != nil {
		return RehearsalReadinessV1{}, err
	}
	return readiness, nil
}

func buildRehearsalReadiness(evidence RehearsalEvidenceV1, evidencePayload []byte) RehearsalReadinessV1 {
	failures := rehearsalCheckFailures(evidence.Checks)
	readiness := RehearsalReadinessV1{
		SchemaVersion: RehearsalReadinessSchemaVersion, Ready: len(failures) == 0,
		ConsoleState: rehearsalRefusedConsoleState, Identity: acceptedRehearsalIdentity(), CandidateCommit: evidence.CandidateCommit,
		EvidenceIndexSHA256: payloadSHA(evidencePayload), CaptureOwnerSHA256: evidence.CaptureOwnerSHA256, Failures: failures,
	}
	if readiness.Ready {
		readiness.ConsoleState = rehearsalReadyConsoleState
		readiness.HumanInstruction = RehearsalHumanInstruction
	}
	return readiness
}

func VerifyRehearsalPreflight(ctx context.Context, root, repo string) (RehearsalReadinessV1, error) {
	return verifyRehearsalPreflight(ctx, root, repo, nil)
}

func verifyRehearsalPreflight(ctx context.Context, root, repo string, listen func(string) (io.Closer, error)) (RehearsalReadinessV1, error) {
	lease, err := acquireExistingRoot(root, repo)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	defer lease.Close()
	readinessPayload, err := readBoundedFile(filepath.Join(lease.abs, "evidence/readiness.json"), MaxRehearsalArtifactBytes)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	var readiness RehearsalReadinessV1
	if err := strictCanonicalRehearsalJSON(readinessPayload, &readiness); err != nil {
		return RehearsalReadinessV1{}, err
	}
	indexPayload, err := readBoundedFile(filepath.Join(lease.abs, "evidence/canonical/evidence-index.json"), MaxRehearsalArtifactBytes)
	if err != nil || payloadSHA(indexPayload) != readiness.EvidenceIndexSHA256 {
		return RehearsalReadinessV1{}, errors.New("rehearsal evidence index hash mismatch")
	}
	var evidence RehearsalEvidenceV1
	if err := strictCanonicalRehearsalJSON(indexPayload, &evidence); err != nil {
		return RehearsalReadinessV1{}, err
	}
	if evidence.SchemaVersion != RehearsalEvidenceSchemaVersion || evidence.Mode != "preflight" || evidence.Identity != acceptedRehearsalIdentity() || evidence.UnavailableIdentity != unavailablePublicMatchIdentity() || !evidence.NonResumable || readiness.SchemaVersion != RehearsalReadinessSchemaVersion || readiness.Identity != evidence.Identity || readiness.CandidateCommit != evidence.CandidateCommit || readiness.CaptureOwnerSHA256 != evidence.CaptureOwnerSHA256 || !validSHA256(evidence.CaptureOwnerSHA256) {
		return RehearsalReadinessV1{}, errors.New("rehearsal preflight identity mismatch")
	}
	ownerPayload, err := readBoundedFile(filepath.Join(lease.abs, "capture/owner.json"), MaxRehearsalArtifactBytes)
	if err != nil || payloadSHA(ownerPayload) != evidence.CaptureOwnerSHA256 {
		return RehearsalReadinessV1{}, errors.New("capture owner identity mismatch")
	}
	var owner RehearsalCaptureOwnerV1
	if strictCanonicalRehearsalJSON(ownerPayload, &owner) != nil || owner.SchemaVersion != "public_match_rehearsal_capture_owner.v1" || owner.CandidateCommit != evidence.CandidateCommit || owner.SessionID == "" || owner.RawRelativePath != "data/sessions/"+owner.SessionID+"/raw.jsonl" || owner.HarnessRelativePath != "data/sessions/"+owner.SessionID+"/harness-evidence.json" || owner.SealRelativePath != "data/sessions/"+owner.SessionID+"/capture-seal.json" {
		return RehearsalReadinessV1{}, errors.New("capture owner contract mismatch")
	}
	failures := rehearsalCheckFailures(evidence.Checks)
	if !equalStrings(failures, readiness.Failures) || readiness.Ready != (len(failures) == 0) {
		return RehearsalReadinessV1{}, errors.New("rehearsal preflight decision mismatch")
	}
	if readiness.Ready {
		if readiness.ConsoleState != rehearsalReadyConsoleState || readiness.HumanInstruction != RehearsalHumanInstruction {
			return RehearsalReadinessV1{}, errors.New("ready rehearsal lacks exact positive state/instruction")
		}
	} else if readiness.ConsoleState != rehearsalRefusedConsoleState || readiness.HumanInstruction != "" {
		return RehearsalReadinessV1{}, errors.New("refused rehearsal exposes positive state or activation instruction")
	}
	if evidence.CandidateSoleParent != RequiredRehearsalParent || len(strings.Fields(evidence.CandidateSoleParent)) != 1 {
		return RehearsalReadinessV1{}, errors.New("rehearsal candidate sole-parent mismatch")
	}
	sealedListener := checkByID(evidence.Checks, "exclusive_localhost_gsi_listener") == "passed"
	if !volatileListenerMatches(sealedListener, listen) {
		return RehearsalReadinessV1{}, errors.New("volatile listener state differs from sealed preflight")
	}
	if readiness.Ready {
		if err := verifyCurrentRehearsalCandidate(ctx, repo, evidence); err != nil {
			return RehearsalReadinessV1{}, err
		}
	}
	if err := rejectAcceptanceCapability(readinessPayload, indexPayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	return readiness, nil
}

func volatileListenerMatches(sealed bool, listen func(string) (io.Closer, error)) bool {
	return rehearsalListenerAvailable(listen, CaptureAddress) == sealed
}

func rehearsalCheckFailures(checks []RehearsalCheckV1) []string {
	if len(checks) != len(rehearsalCheckRegistry) {
		return []string{"invalid_check_contract"}
	}
	failures := make([]string, 0)
	for index, check := range checks {
		expected := rehearsalCheckRegistry[index]
		if check.ID != expected.ID || expected.State == "not_applicable_rehearsal" && check.State != expected.State || expected.State == "dynamic" && check.State != "passed" && check.State != "failed" {
			return []string{"invalid_check_contract"}
		}
		if check.State == "failed" {
			failures = append(failures, check.ID)
		}
	}
	sort.Strings(failures)
	return failures
}

func checkByID(checks []RehearsalCheckV1, id string) string {
	for _, check := range checks {
		if check.ID == id {
			return check.State
		}
	}
	return ""
}

func rehearsalListenerAvailable(open func(string) (io.Closer, error), address string) bool {
	if open == nil {
		open = func(address string) (io.Closer, error) { return net.Listen("tcp", address) }
	}
	listener, err := open(address)
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

type RehearsalAttemptRequestV1 struct {
	SchemaVersion   string `json:"schema_version"`
	ExpectedMatchID string `json:"expected_match_id,omitempty"`
}

type RehearsalAttemptConfig struct {
	DataRoot string
	RepoRoot string
	Request  RehearsalAttemptRequestV1
}

type RehearsalDerivedFactsV1 struct {
	MatchID         string `json:"match_id,omitempty"`
	EntryBeforeZero bool   `json:"entry_before_zero"`
	ClockOrdered    bool   `json:"clock_ordered"`
	Continuous      bool   `json:"continuous"`
	NormalPostGame  bool   `json:"normal_post_game"`
	WinnerObserved  bool   `json:"winner_observed"`
}

type RehearsalProcessObservationV1 struct {
	SourceSequence       uint64 `json:"source_sequence"`
	PID                  int    `json:"dota_pid"`
	Comm                 string `json:"comm"`
	ExecutablePathSHA256 string `json:"executable_path_sha256"`
	ExecutableSHA256     string `json:"executable_sha256"`
	StartTicks           uint64 `json:"start_ticks"`
	State                string `json:"state"`
}

type RehearsalFailClosedObservationV1 struct {
	SourceSequence uint64 `json:"source_sequence"`
	ElapsedMS      uint64 `json:"elapsed_ms"`
	CandidateClaim bool   `json:"candidate_claim"`
	DecisionClaim  bool   `json:"decision_claim"`
	OverlayClaim   bool   `json:"overlay_claim"`
	DisplayClaim   bool   `json:"display_claim"`
}

type RehearsalOperatorObservationV1 struct {
	Ordinal        uint8  `json:"ordinal"`
	Action         string `json:"action"`
	SourceSequence uint64 `json:"source_sequence"`
	AuditSHA256    string `json:"audit_sha256"`
}

type RehearsalResourceObservationV1 struct {
	SourceSequence uint64 `json:"source_sequence"`
	IntervalMS     uint64 `json:"interval_ms"`
	ProductRSS     uint64 `json:"product_rss_bytes"`
	OBSRSS         uint64 `json:"obs_rss_bytes"`
}

type RehearsalHarnessEvidenceV1 struct {
	SchemaVersion        string                             `json:"schema_version"`
	SessionID            string                             `json:"session_id"`
	RawSHA256            string                             `json:"raw_sha256"`
	Process              []RehearsalProcessObservationV1    `json:"process_observations"`
	TerminalProcess      []RehearsalProcessObservationV1    `json:"terminal_process_observations"`
	RecordingSHA256      string                             `json:"finalized_recording_sha256"`
	RecordingBytes       uint64                             `json:"finalized_recording_bytes"`
	PartialRecordings    uint64                             `json:"partial_recording_count"`
	FailClosed           []RehearsalFailClosedObservationV1 `json:"fail_closed_observations"`
	Operator             []RehearsalOperatorObservationV1   `json:"operator_observations"`
	Resources            []RehearsalResourceObservationV1   `json:"resource_observations"`
	ProjectedCount       uint64                             `json:"projected_count"`
	PolicyCount          uint64                             `json:"policy_count"`
	AuditCount           uint64                             `json:"audit_count"`
	OverlayCount         uint64                             `json:"overlay_count"`
	ReconciliationSHA256 string                             `json:"reconciliation_sha256"`
	ConfinementSHA256    string                             `json:"confinement_sha256"`
	RecoveryInputSHA256  string                             `json:"recovery_input_sha256"`
	RecoveryFirstSHA256  string                             `json:"recovery_first_sha256"`
	RecoverySecondSHA256 string                             `json:"recovery_second_sha256"`
	RecoveryStartTicks1  uint64                             `json:"recovery_start_ticks_1"`
	RecoveryStartTicks2  uint64                             `json:"recovery_start_ticks_2"`
	ProductExitCode      int                                `json:"product_exit_code"`
	OBSExitCode          int                                `json:"obs_exit_code"`
	Artifacts            []Artifact                         `json:"producer_artifacts"`
}

type RehearsalCaptureSealV1 struct {
	SchemaVersion      string `json:"schema_version"`
	CaptureOwnerSHA256 string `json:"capture_owner_sha256"`
	RawSHA256          string `json:"raw_sha256"`
	RawBytes           int64  `json:"raw_bytes"`
	HarnessSHA256      string `json:"harness_evidence_sha256"`
}

type RehearsalRawIdentityV1 struct {
	Sequence         uint64 `json:"sequence"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
}

type RehearsalRawManifestV1 struct {
	SchemaVersion string                   `json:"schema_version"`
	SessionID     string                   `json:"session_id"`
	RawSHA256     string                   `json:"raw_session_sha256"`
	RawBytes      int64                    `json:"raw_session_bytes"`
	Observed      uint64                   `json:"observed_frames"`
	Accepted      uint64                   `json:"accepted_frames"`
	Rejected      uint64                   `json:"rejected_frames"`
	FailureCode   string                   `json:"scan_failure_code,omitempty"`
	Records       []RehearsalRawIdentityV1 `json:"records"`
}

type FieldCoverageUnavailableV1 struct {
	Reason                   string `json:"reason"`
	ObservedFrames           uint64 `json:"observed_frames"`
	AcceptedFrames           uint64 `json:"accepted_frames"`
	RejectedFrames           uint64 `json:"rejected_frames"`
	BaselineRawSessionSHA256 string `json:"baseline_raw_session_sha256,omitempty"`
	RehearsalRawSHA256       string `json:"rehearsal_raw_session_sha256,omitempty"`
}

type FieldCoverageStatusV1 struct {
	State       string                      `json:"state"`
	Delta       *FieldCoverageDeltaV1       `json:"delta,omitempty"`
	Unavailable *FieldCoverageUnavailableV1 `json:"unavailable,omitempty"`
}

type RehearsalCoverageBindingV1 struct {
	State        string `json:"state"`
	ArtifactPath string `json:"artifact_path,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	RootSHA256   string `json:"root_sha256"`
}

type SuppressionExecutionV1 struct {
	SourceSequence   uint64 `json:"source_sequence"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
	Family           string `json:"family"`
	EligibilitySite  string `json:"eligibility_site"`
	Executed         bool   `json:"executed"`
	FallbackUsed     bool   `json:"fallback_used"`
}

type SuppressionAuditV1 struct {
	SchemaVersion    string `json:"schema_version"`
	SourceSequence   uint64 `json:"source_sequence"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
	Family           string `json:"family"`
	EligibilitySite  string `json:"eligibility_site"`
	Unavailable      string `json:"unavailable_reason"`
	Result           string `json:"result"`
	CandidateClaim   bool   `json:"candidate_claim"`
	DecisionClaim    bool   `json:"decision_claim"`
	OverlayClaim     bool   `json:"overlay_claim"`
	DisplayProduced  bool   `json:"display_produced"`
}

type RehearsalSuppressionEvidenceV1 struct {
	SchemaVersion  string                   `json:"schema_version"`
	RegistrySHA256 string                   `json:"registry_sha256"`
	FrameCount     uint64                   `json:"evaluated_frame_count"`
	Executions     []SuppressionExecutionV1 `json:"executions"`
	Audits         []SuppressionAuditV1     `json:"audits"`
}

type RehearsalTerminalV1 struct {
	SchemaVersion         string                     `json:"schema_version"`
	Identity              RehearsalIdentityV1        `json:"identity"`
	CandidateCommit       string                     `json:"candidate_commit"`
	PreflightIndexSHA256  string                     `json:"preflight_index_sha256"`
	AttemptRootSHA256     string                     `json:"attempt_root_sha256"`
	AttemptRequestSHA256  string                     `json:"attempt_request_sha256"`
	CaptureOwnerSHA256    string                     `json:"capture_owner_sha256"`
	CaptureSealSHA256     string                     `json:"capture_seal_sha256"`
	HarnessEvidenceSHA256 string                     `json:"harness_evidence_sha256"`
	ProducerReceiptSHA256 string                     `json:"producer_receipt_sha256"`
	RawManifestSHA256     string                     `json:"raw_manifest_sha256"`
	DerivedFacts          RehearsalDerivedFactsV1    `json:"derived_facts"`
	SuppressionSHA256     string                     `json:"suppression_sha256"`
	RecoverySHA256        string                     `json:"recovery_suppression_sha256"`
	RecoveryVerified      bool                       `json:"recovery_verified"`
	Coverage              FieldCoverageStatusV1      `json:"field_coverage"`
	CoverageBinding       RehearsalCoverageBindingV1 `json:"coverage_binding"`
	Outcome               string                     `json:"outcome"`
	FailureCode           string                     `json:"failure_code,omitempty"`
	CleanupAdmissible     bool                       `json:"cleanup_admissible"`
	NonResumable          bool                       `json:"non_resumable"`
}

type RehearsalTerminalReceiptV1 struct {
	SchemaVersion     string `json:"schema_version"`
	AttemptRootSHA256 string `json:"attempt_root_sha256"`
	TerminalSHA256    string `json:"terminal_sha256"`
	Outcome           string `json:"outcome"`
	FailureCode       string `json:"failure_code,omitempty"`
	CleanupAdmissible bool   `json:"cleanup_admissible"`
}

type scannedRehearsalRaw struct {
	Manifest RehearsalRawManifestV1
	Frames   []CoverageFrameV1
	Records  []*session.Record
}

type rehearsalCountingReader struct {
	reader io.Reader
	read   int64
}

func (reader *rehearsalCountingReader) Read(buffer []byte) (int, error) {
	n, err := reader.reader.Read(buffer)
	reader.read += int64(n)
	return n, err
}

// rehearsalRawDescriptorBoundHook is an unexported deterministic race seam.
// Production never assigns it; tests use it to append/replace after binding.
var rehearsalRawDescriptorBoundHook func()

func scanRehearsalRaw(payload []byte, sessionID string) scannedRehearsalRaw {
	if len(payload) > MaxRehearsalRawBytes {
		return limitedRawManifest(sessionID, int64(len(payload)), "raw_total_limit")
	}
	return scanRehearsalReader(bytes.NewReader(payload), sessionID)
}

func scanRehearsalRawPath(path, sessionID string) scannedRehearsalRaw {
	file, err := rootOpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return limitedRawManifest(sessionID, 0, "raw_provenance_failure")
	}
	defer file.Close()
	bound, ok := session.BindRawV3Descriptor(file)
	if !ok {
		return limitedRawManifest(sessionID, 0, "raw_provenance_failure")
	}
	if rehearsalRawDescriptorBoundHook != nil {
		rehearsalRawDescriptorBoundHook()
	}
	counter := &rehearsalCountingReader{reader: io.LimitReader(file, MaxRehearsalRawBytes+1)}
	result := scanRehearsalReader(counter, sessionID)
	if counter.read > MaxRehearsalRawBytes {
		return limitedRawManifest(sessionID, counter.read, "raw_total_limit")
	}
	if !session.VerifyRawV3Descriptor(file, path, bound, counter.read) {
		return limitedRawManifest(sessionID, counter.read, "raw_changed_during_read")
	}
	return result
}

func limitedRawManifest(sessionID string, size int64, code string) scannedRehearsalRaw {
	identity, _ := canonical(struct {
		Code string `json:"code"`
		Size int64  `json:"size"`
	}{code, size})
	return scannedRehearsalRaw{Manifest: RehearsalRawManifestV1{
		SchemaVersion: RehearsalRawManifestSchema, SessionID: sessionID, RawSHA256: payloadSHA(identity), RawBytes: size,
		Observed: 1, Rejected: 1, FailureCode: code, Records: []RehearsalRawIdentityV1{},
	}}
}

func scanRehearsalReader(input io.Reader, sessionID string) scannedRehearsalRaw {
	hash := sha256.New()
	counter := &rehearsalCountingReader{reader: input}
	reader := bufio.NewReaderSize(io.TeeReader(counter, hash), MaxRehearsalRawLineBytes+1)
	result := scannedRehearsalRaw{}
	result.Manifest = RehearsalRawManifestV1{SchemaVersion: RehearsalRawManifestSchema, SessionID: sessionID, Records: []RehearsalRawIdentityV1{}}
	for {
		line, err := reader.ReadSlice('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		result.Manifest.Observed++
		if err == bufio.ErrBufferFull || len(line) > MaxRehearsalRawLineBytes {
			result.Manifest.Rejected++
			result.Manifest.FailureCode = "raw_line_limit"
			break
		}
		if err != nil || len(line) < 2 || line[len(line)-1] != '\n' {
			result.Manifest.Rejected++
			result.Manifest.FailureCode = "partial_frame"
			break
		}
		if result.Manifest.Accepted >= MaxRehearsalRecords {
			result.Manifest.Rejected++
			result.Manifest.FailureCode = "record_limit"
			break
		}
		frame := line[:len(line)-1]
		sequence := result.Manifest.Accepted + 1
		record, decodeErr := session.DecodeRecordV3(frame, sessionID, sequence)
		if decodeErr != nil {
			result.Manifest.Rejected++
			result.Manifest.FailureCode = "malformed_frame"
			break
		}
		recordSHA := payloadSHA(frame)
		payloadSHAValue := payloadSHA(record.Raw)
		result.Manifest.Accepted++
		result.Manifest.Records = append(result.Manifest.Records, RehearsalRawIdentityV1{Sequence: sequence, RawRecordSHA256: recordSHA, RawPayloadSHA256: payloadSHAValue})
		result.Frames = append(result.Frames, CoverageFrameV1{Sequence: sequence, RawRecordSHA256: recordSHA, RawRecord: append(json.RawMessage(nil), record.Raw...), IdentityRecord: append([]byte(nil), frame...)})
		result.Records = append(result.Records, record)
		if err == io.EOF {
			break
		}
	}
	result.Manifest.RawBytes = counter.read
	if counter.read > MaxRehearsalRawBytes {
		result.Manifest.FailureCode = "raw_total_limit"
		result.Manifest.Rejected++
	}
	if result.Manifest.FailureCode == "" {
		result.Manifest.RawSHA256 = fmt.Sprintf("%x", hash.Sum(nil))
	} else {
		failureIdentity, _ := canonical(struct {
			Code     string `json:"code"`
			Size     int64  `json:"size"`
			Accepted uint64 `json:"accepted"`
		}{result.Manifest.FailureCode, counter.read, result.Manifest.Accepted})
		result.Manifest.RawSHA256 = payloadSHA(failureIdentity)
	}
	return result
}

type suppressionCollector struct {
	recordBySequence map[uint64]RehearsalRawIdentityV1
	executions       []SuppressionExecutionV1
}

func (collector *suppressionCollector) appendProductionExecution(event insight.HistoricalUnavailableExecution) error {
	if event.Reason != "historical_unavailable" || event.EligibilitySite != event.Family+".history_unavailable.v1" {
		return errors.New("history branch emitted a claim or identity")
	}
	sequence := event.Evidence.Sequence
	rawPayloadSHA256 := event.Evidence.RawPayloadSHA256
	family := event.Family
	identity := collector.recordBySequence[sequence]
	if identity.Sequence == 0 || identity.RawPayloadSHA256 != rawPayloadSHA256 {
		return errors.New("history branch execution is not raw-bound")
	}
	collector.executions = append(collector.executions, SuppressionExecutionV1{
		SourceSequence: sequence, RawRecordSHA256: identity.RawRecordSHA256, RawPayloadSHA256: rawPayloadSHA256,
		Family: family, EligibilitySite: event.EligibilitySite, Executed: true,
	})
	return nil
}

func evaluateRehearsalProduction(raw scannedRehearsalRaw) (RehearsalSuppressionEvidenceV1, string) {
	families := contracts.HistoricalDisabledFamiliesV1()
	registryPayload, _ := contracts.MarshalCanonical(families)
	result := RehearsalSuppressionEvidenceV1{SchemaVersion: RehearsalSuppressionSchema, RegistrySHA256: payloadSHA(registryPayload), Executions: []SuppressionExecutionV1{}, Audits: []SuppressionAuditV1{}}
	if len(raw.Records) == 0 {
		return result, ""
	}
	if len(raw.Records) > MaxRehearsalRecords || len(raw.Frames) > MaxRehearsalCoverageFrames {
		return result, "resource_limit_failure"
	}
	history, lineage, _, err := liveArtifacts(raw.Manifest.SessionID)
	if err != nil {
		return result, "production_binding_failure"
	}
	identities := make(map[uint64]RehearsalRawIdentityV1, len(raw.Manifest.Records))
	for _, identity := range raw.Manifest.Records {
		identities[identity.Sequence] = identity
	}
	collector := &suppressionCollector{recordBySequence: identities}
	var previous *contracts.LiveObservationV1
	for _, record := range raw.Records {
		observation, mapErr := capture.MapLiveObservationV1(record)
		if mapErr != nil || observation.Validate() != nil {
			return result, "production_evaluation_failure"
		}
		evaluation, branchErr := insight.EvaluateLiveOnlyProduction(insight.LiveOnlyInput{
			Observation: observation, Previous: previous, History: history, Lineage: lineage,
			PolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli(),
		}, insight.DefaultConfig())
		if branchErr != nil {
			return result, "production_evaluation_failure"
		}
		for _, execution := range evaluation.Suppressions {
			if collector.appendProductionExecution(execution) != nil {
				return result, "production_evaluation_failure"
			}
		}
		for _, candidate := range evaluation.Candidates {
			for _, family := range families {
				if insight.Family(candidate.RuleVersion) == family {
					return result, "historical_claim_produced"
				}
			}
		}
		copyObservation := observation
		previous = &copyObservation
		result.FrameCount++
	}
	result.Executions = append(result.Executions, collector.executions...)
	for _, execution := range result.Executions {
		result.Audits = append(result.Audits, SuppressionAuditV1{
			SchemaVersion: RehearsalSuppressionSchema, SourceSequence: execution.SourceSequence,
			RawRecordSHA256: execution.RawRecordSHA256, RawPayloadSHA256: execution.RawPayloadSHA256,
			Family: execution.Family, EligibilitySite: execution.EligibilitySite, Unavailable: "unavailable_for_public_match", Result: "suppressed",
		})
	}
	if err := validateSuppressionEvidence(result, raw.Manifest); err != nil {
		return result, "suppression_reconciliation_failure"
	}
	return result, ""
}

func validateSuppressionEvidence(value RehearsalSuppressionEvidenceV1, manifest RehearsalRawManifestV1) error {
	families := contracts.HistoricalDisabledFamiliesV1()
	registryPayload, _ := contracts.MarshalCanonical(families)
	if value.SchemaVersion != RehearsalSuppressionSchema || value.RegistrySHA256 != payloadSHA(registryPayload) || value.FrameCount > manifest.Accepted || value.FrameCount > MaxRehearsalRecords || len(value.Executions) > MaxRehearsalExecutions || len(value.Audits) > MaxRehearsalAudits || len(value.Executions) != int(value.FrameCount)*len(families) || len(value.Audits) != len(value.Executions) {
		return errors.New("suppression population or registry mismatch")
	}
	records := map[uint64]RehearsalRawIdentityV1{}
	for _, record := range manifest.Records {
		records[record.Sequence] = record
	}
	executions := map[string]SuppressionExecutionV1{}
	for _, execution := range value.Executions {
		record, ok := records[execution.SourceSequence]
		key := fmt.Sprintf("%d/%s", execution.SourceSequence, execution.Family)
		if !ok || !containsString(families, execution.Family) || execution.EligibilitySite != execution.Family+".history_unavailable.v1" || executions[key].Family != "" || !execution.Executed || execution.FallbackUsed || execution.RawRecordSHA256 != record.RawRecordSHA256 || execution.RawPayloadSHA256 != record.RawPayloadSHA256 {
			return errors.New("suppression execution is missing, duplicate, fallback-backed, or raw-unbound")
		}
		executions[key] = execution
	}
	audits := map[string]SuppressionAuditV1{}
	for _, audit := range value.Audits {
		key := fmt.Sprintf("%d/%s", audit.SourceSequence, audit.Family)
		execution, ok := executions[key]
		if !ok || audits[key].Family != "" || audit.SchemaVersion != RehearsalSuppressionSchema || audit.EligibilitySite != execution.EligibilitySite || audit.RawRecordSHA256 != execution.RawRecordSHA256 || audit.RawPayloadSHA256 != execution.RawPayloadSHA256 || audit.Unavailable != "unavailable_for_public_match" || audit.Result != "suppressed" || audit.CandidateClaim || audit.DecisionClaim || audit.OverlayClaim || audit.DisplayProduced {
			return errors.New("suppression audit is missing, duplicate, spliced, leaking, or claim-producing")
		}
		audits[key] = audit
	}
	for index := uint64(1); index <= value.FrameCount; index++ {
		for _, family := range families {
			key := fmt.Sprintf("%d/%s", index, family)
			if executions[key].Family == "" || audits[key].Family == "" {
				return errors.New("accepted production family execution/audit is absent")
			}
		}
	}
	return nil
}

type builtRehearsalAttempt struct {
	RequestPayload     []byte
	ManifestPayload    []byte
	SuppressionPayload []byte
	CoveragePayload    []byte
	TerminalPayload    []byte
	ReceiptPayload     []byte
	Terminal           RehearsalTerminalV1
	Receipt            RehearsalTerminalReceiptV1
}

type rehearsalCaptureBinding struct {
	Owner          RehearsalCaptureOwnerV1
	OwnerSHA256    string
	SealSHA256     string
	HarnessSHA256  string
	ProducerSHA256 string
	Facts          RehearsalDerivedFactsV1
	Harness        RehearsalHarnessEvidenceV1
	Failure        string
}

func loadRehearsalCapture(root string, readiness RehearsalReadinessV1, request RehearsalAttemptRequestV1) (rehearsalCaptureBinding, scannedRehearsalRaw) {
	var binding rehearsalCaptureBinding
	ownerPayload, err := readBoundedFile(filepath.Join(root, "capture/owner.json"), MaxRehearsalArtifactBytes)
	if err != nil || payloadSHA(ownerPayload) != readiness.CaptureOwnerSHA256 || strictCanonicalRehearsalJSON(ownerPayload, &binding.Owner) != nil {
		binding.Failure = "capture_owner_failure"
		return binding, limitedRawManifest("unavailable", 0, "raw_provenance_failure")
	}
	binding.OwnerSHA256 = payloadSHA(ownerPayload)
	producerPayload, producerErr := readBoundedFile(filepath.Join(root, "evidence/producer-receipt.json"), MaxRehearsalArtifactBytes)
	var producer rehearsalProducerReceipt
	if producerErr != nil || strictCanonicalRehearsalJSON(producerPayload, &producer) != nil || producer.SchemaVersion != rehearsalProducerReceiptSchema || producer.SessionID != binding.Owner.SessionID {
		binding.ProducerSHA256 = typedMissingSHA("producer_receipt")
		binding.Failure = "missing_producer_receipt"
	} else {
		binding.ProducerSHA256 = payloadSHA(producerPayload)
		if producer.Outcome != "sealed" {
			binding.Failure = producer.FailureCode
		}
	}
	rawPath := filepath.Join(root, filepath.FromSlash(binding.Owner.RawRelativePath))
	raw := scanRehearsalRawPath(rawPath, binding.Owner.SessionID)
	binding.Facts = deriveRawFacts(raw)
	if binding.Owner.CandidateCommit != readiness.CandidateCommit || binding.Owner.SessionID == "" || binding.Owner.RawRelativePath != "data/sessions/"+binding.Owner.SessionID+"/raw.jsonl" || binding.Owner.HarnessRelativePath != "data/sessions/"+binding.Owner.SessionID+"/harness-evidence.json" || binding.Owner.SealRelativePath != "data/sessions/"+binding.Owner.SessionID+"/capture-seal.json" {
		binding.Failure = "capture_owner_failure"
		return binding, raw
	}
	if binding.Failure != "" {
		return binding, raw
	}
	lineagePath := filepath.Join(root, "data/sessions", binding.Owner.SessionID, "capture_lineage_v3.json")
	lineagePayload, lineageErr := readBoundedFile(lineagePath, 64<<10)
	if lineageErr != nil || !bytes.Contains(lineagePayload, []byte(session.RawRecordSchemaV3Identity)) || !session.VerifyRawV3ContentGuard(rawPath) {
		binding.Failure = "raw_provenance_failure"
		return binding, raw
	}
	rawInfo, statErr := os.Stat(rawPath)
	if statErr != nil || rawInfo.Mode().Perm()&0o222 != 0 {
		binding.Failure = "capture_not_sealed"
		return binding, raw
	}
	harnessPayload, harnessErr := readBoundedFile(filepath.Join(root, filepath.FromSlash(binding.Owner.HarnessRelativePath)), MaxRehearsalArtifactBytes)
	sealPayload, sealErr := readBoundedFile(filepath.Join(root, filepath.FromSlash(binding.Owner.SealRelativePath)), MaxRehearsalArtifactBytes)
	var seal RehearsalCaptureSealV1
	if harnessErr != nil || strictCanonicalRehearsalJSON(harnessPayload, &binding.Harness) != nil {
		binding.Failure = "missing_harness_evidence"
		return binding, raw
	}
	binding.HarnessSHA256 = payloadSHA(harnessPayload)
	if sealErr != nil || strictCanonicalRehearsalJSON(sealPayload, &seal) != nil {
		binding.Failure = "capture_not_sealed"
		return binding, raw
	}
	binding.SealSHA256 = payloadSHA(sealPayload)
	if seal.SchemaVersion != "public_match_rehearsal_capture_seal.v1" || seal.CaptureOwnerSHA256 != binding.OwnerSHA256 || seal.RawSHA256 != raw.Manifest.RawSHA256 || seal.RawBytes != raw.Manifest.RawBytes || seal.HarnessSHA256 != binding.HarnessSHA256 || binding.Harness.SchemaVersion != "public_match_rehearsal_harness_evidence.v2" || binding.Harness.SessionID != binding.Owner.SessionID || binding.Harness.RawSHA256 != raw.Manifest.RawSHA256 {
		binding.Failure = "capture_seal_mismatch"
		return binding, raw
	}
	if request.ExpectedMatchID != "" && request.ExpectedMatchID != binding.Facts.MatchID {
		binding.Failure = "expected_match_mismatch"
		return binding, raw
	}
	binding.Failure = validateHarnessEvidence(root, binding.Harness, raw.Manifest)
	return binding, raw
}

func deriveRawFacts(raw scannedRehearsalRaw) RehearsalDerivedFactsV1 {
	facts := RehearsalDerivedFactsV1{ClockOrdered: true, Continuous: raw.Manifest.FailureCode == "" && raw.Manifest.Accepted > 0}
	var previousClock int64
	var haveClock bool
	for index, record := range raw.Records {
		var payload struct {
			Map struct {
				MatchID   string          `json:"matchid"`
				ClockTime *int64          `json:"clock_time"`
				GameState string          `json:"game_state"`
				WinTeam   json.RawMessage `json:"win_team"`
			} `json:"map"`
		}
		if json.Unmarshal(record.Raw, &payload) != nil || payload.Map.MatchID == "" || payload.Map.ClockTime == nil {
			facts.Continuous, facts.ClockOrdered = false, false
			continue
		}
		if index == 0 {
			facts.MatchID = payload.Map.MatchID
			facts.EntryBeforeZero = *payload.Map.ClockTime < 0
		} else if payload.Map.MatchID != facts.MatchID {
			facts.Continuous = false
		}
		if haveClock && *payload.Map.ClockTime < previousClock {
			facts.ClockOrdered = false
		}
		previousClock, haveClock = *payload.Map.ClockTime, true
		if strings.Contains(strings.ToUpper(payload.Map.GameState), "POST_GAME") {
			facts.NormalPostGame = true
			winner := strings.Trim(string(payload.Map.WinTeam), "\"")
			facts.WinnerObserved = winner != "" && winner != "0" && winner != "null"
		}
	}
	return facts
}

func validateHarnessEvidence(root string, value RehearsalHarnessEvidenceV1, manifest RehearsalRawManifestV1) string {
	if value.SchemaVersion != "public_match_rehearsal_harness_evidence.v2" || validateProducerArtifactManifest(root, value) != nil {
		return "producer_artifact_failure"
	}
	if len(value.Process) != int(manifest.Accepted) || manifest.Accepted == 0 {
		return "process_evidence_failure"
	}
	var executable, path string
	var start uint64
	var pid int
	for index, observation := range value.Process {
		if observation.SourceSequence != uint64(index+1) || observation.PID <= 0 || strings.ToLower(observation.Comm) != "dota2" || !validSHA256(observation.ExecutablePathSHA256) || !validSHA256(observation.ExecutableSHA256) || observation.StartTicks == 0 || observation.State != "running" {
			return "process_evidence_failure"
		}
		if index == 0 {
			executable, path, start, pid = observation.ExecutableSHA256, observation.ExecutablePathSHA256, observation.StartTicks, observation.PID
		} else if observation.PID != pid || observation.ExecutableSHA256 != executable || observation.ExecutablePathSHA256 != path || observation.StartTicks != start {
			return "process_replaced"
		}
	}
	if len(value.TerminalProcess) != 2 {
		return "process_evidence_failure"
	}
	for index, observation := range value.TerminalProcess {
		expectedState := []string{"terminal_capture", "terminal_shutdown"}[index]
		if observation.SourceSequence != manifest.Accepted || observation.PID != pid || strings.ToLower(observation.Comm) != "dota2" || observation.ExecutableSHA256 != executable || observation.ExecutablePathSHA256 != path || observation.StartTicks != start || observation.State != expectedState {
			return "process_replaced"
		}
	}
	if !validSHA256(value.RecordingSHA256) || value.RecordingBytes == 0 || value.PartialRecordings != 0 {
		return "obs_finalization_failure"
	}
	if len(value.FailClosed) == 0 {
		return "fail_closed_evidence_failure"
	}
	for _, observation := range value.FailClosed {
		if observation.SourceSequence == 0 || observation.SourceSequence > manifest.Accepted || observation.ElapsedMS > 2000 || observation.CandidateClaim || observation.DecisionClaim || observation.OverlayClaim || observation.DisplayClaim {
			return "fail_closed_evidence_failure"
		}
	}
	actions := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	if len(value.Operator) != len(actions) {
		return "operator_script_failure"
	}
	for index, observation := range value.Operator {
		if observation.Ordinal != uint8(index+1) || observation.Action != actions[index] || observation.SourceSequence == 0 || observation.SourceSequence > manifest.Accepted || !validSHA256(observation.AuditSHA256) {
			return "operator_script_failure"
		}
	}
	if len(value.Resources) == 0 {
		return "resource_sampling_failure"
	}
	for _, observation := range value.Resources {
		if observation.SourceSequence == 0 || observation.SourceSequence > manifest.Accepted || observation.IntervalMS != 5000 || observation.ProductRSS == 0 || observation.OBSRSS == 0 {
			return "resource_sampling_failure"
		}
	}
	if value.ProjectedCount != manifest.Accepted || value.PolicyCount != manifest.Accepted || value.AuditCount != manifest.Accepted || value.OverlayCount != manifest.Accepted || !validSHA256(value.ReconciliationSHA256) {
		return "raw_reconciliation_failure"
	}
	if !validSHA256(value.ConfinementSHA256) {
		return "confinement_failure"
	}
	if value.RecoveryInputSHA256 != manifest.RawSHA256 || !validSHA256(value.RecoveryFirstSHA256) || value.RecoveryFirstSHA256 != value.RecoverySecondSHA256 || value.RecoveryStartTicks1 == 0 || value.RecoveryStartTicks2 == 0 || value.RecoveryStartTicks1 == value.RecoveryStartTicks2 {
		return "recovery_evidence_failure"
	}
	if value.ProductExitCode != 0 || value.OBSExitCode != 0 {
		return "unclean_exit"
	}
	return ""
}

func buildRehearsalAttempt(repo string, readiness RehearsalReadinessV1, request RehearsalAttemptRequestV1, raw scannedRehearsalRaw, captureBinding rehearsalCaptureBinding) (builtRehearsalAttempt, error) {
	if request.SchemaVersion != RehearsalAttemptSchemaVersion || strings.ContainsAny(request.ExpectedMatchID, "/\\\x00\r\n") || raw.Manifest.SessionID == "" || raw.Manifest.SessionID != captureBinding.Owner.SessionID {
		return builtRehearsalAttempt{}, errors.New("invalid rehearsal attempt request/session")
	}
	if captureBinding.OwnerSHA256 == "" {
		captureBinding.OwnerSHA256 = typedMissingSHA("capture_owner")
	}
	if captureBinding.SealSHA256 == "" {
		captureBinding.SealSHA256 = typedMissingSHA("capture_seal")
	}
	if captureBinding.HarnessSHA256 == "" {
		captureBinding.HarnessSHA256 = typedMissingSHA("harness_evidence")
	}
	if captureBinding.ProducerSHA256 == "" {
		captureBinding.ProducerSHA256 = typedMissingSHA("producer_receipt")
	}
	requestPayload, err := canonical(request)
	if err != nil {
		return builtRehearsalAttempt{}, err
	}
	manifestPayload, err := canonical(raw.Manifest)
	if err != nil {
		return builtRehearsalAttempt{}, err
	}
	requestSHA := payloadSHA(requestPayload)
	manifestSHA := payloadSHA(manifestPayload)
	attemptRootPayload, _ := canonical(struct {
		Domain, AcceptedSpec, CandidateCommit, PreflightIndexSHA256, RequestSHA256, CaptureOwnerSHA256, CaptureSealSHA256, HarnessEvidenceSHA256, ProducerReceiptSHA256, RawManifestSHA256 string
	}{rehearsalRootDomain, AcceptedRehearsalSpec, readiness.CandidateCommit, readiness.EvidenceIndexSHA256, requestSHA, captureBinding.OwnerSHA256, captureBinding.SealSHA256, captureBinding.HarnessSHA256, captureBinding.ProducerSHA256, manifestSHA})
	attemptRootSHA := payloadSHA(attemptRootPayload)

	suppression, evaluationFailure := evaluateRehearsalProduction(raw)
	suppressionPayload, err := canonical(suppression)
	if err != nil {
		return builtRehearsalAttempt{}, err
	}
	recovery, recoveryFailure := evaluateRehearsalProduction(raw)
	recoveryPayload, _ := canonical(recovery)
	if recoveryFailure == "" && !bytes.Equal(recoveryPayload, suppressionPayload) {
		recoveryFailure = "recovery_reproduction_failure"
	}

	coverageRootPayload, _ := canonical(struct {
		Domain, AcceptedSpec, AttemptRootSHA256, BaselineSHA256, RehearsalSHA256, Algorithm string
	}{rehearsalRootDomain, AcceptedRehearsalSpec, attemptRootSHA, CapturedScheduleSHA256, raw.Manifest.RawSHA256, CoverageNormalizationVersion})
	coverageRootSHA := payloadSHA(coverageRootPayload)
	coverage, binding, coveragePayload, coverageFailure := buildCoverageStatus(repo, raw, coverageRootSHA)

	failure := deriveRehearsalFailure(readiness, request, raw.Manifest, captureBinding, evaluationFailure, recoveryFailure, coverageFailure)
	outcome := "rehearsal_failed"
	if failure == "" {
		outcome = "rehearsal_completed"
	}
	terminal := RehearsalTerminalV1{
		SchemaVersion: RehearsalTerminalSchemaVersion, Identity: acceptedRehearsalIdentity(),
		CandidateCommit: readiness.CandidateCommit, PreflightIndexSHA256: readiness.EvidenceIndexSHA256,
		AttemptRootSHA256: attemptRootSHA, AttemptRequestSHA256: requestSHA,
		CaptureOwnerSHA256: captureBinding.OwnerSHA256, CaptureSealSHA256: captureBinding.SealSHA256, HarnessEvidenceSHA256: captureBinding.HarnessSHA256,
		ProducerReceiptSHA256: captureBinding.ProducerSHA256,
		RawManifestSHA256:     manifestSHA, DerivedFacts: captureBinding.Facts,
		SuppressionSHA256: payloadSHA(suppressionPayload), RecoverySHA256: payloadSHA(recoveryPayload), RecoveryVerified: recoveryFailure == "",
		Coverage: coverage, CoverageBinding: binding,
		Outcome: outcome, FailureCode: failure, CleanupAdmissible: true, NonResumable: true,
	}
	if err := validateTerminalStructure(terminal, request, raw.Manifest, captureBinding, suppression, failure); err != nil {
		return builtRehearsalAttempt{}, err
	}
	terminalPayload, _ := canonical(terminal)
	receipt := RehearsalTerminalReceiptV1{
		SchemaVersion: RehearsalReceiptSchemaVersion, AttemptRootSHA256: attemptRootSHA,
		TerminalSHA256: payloadSHA(terminalPayload), Outcome: outcome, FailureCode: failure, CleanupAdmissible: true,
	}
	receiptPayload, _ := canonical(receipt)
	return builtRehearsalAttempt{
		RequestPayload: requestPayload, ManifestPayload: manifestPayload, SuppressionPayload: suppressionPayload,
		CoveragePayload: coveragePayload, TerminalPayload: terminalPayload, ReceiptPayload: receiptPayload,
		Terminal: terminal, Receipt: receipt,
	}, nil
}

func typedMissingSHA(kind string) string { return payloadSHA([]byte("missing:" + kind)) }

func buildCoverageStatus(repo string, raw scannedRehearsalRaw, rootSHA string) (FieldCoverageStatusV1, RehearsalCoverageBindingV1, []byte, string) {
	binding := RehearsalCoverageBindingV1{State: "unavailable", RootSHA256: rootSHA}
	unavailable := func(reason string) (FieldCoverageStatusV1, RehearsalCoverageBindingV1, []byte, string) {
		status := FieldCoverageStatusV1{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{
			Reason: reason, ObservedFrames: raw.Manifest.Observed, AcceptedFrames: raw.Manifest.Accepted,
			RejectedFrames: raw.Manifest.Rejected, BaselineRawSessionSHA256: CapturedScheduleSHA256,
			RehearsalRawSHA256: raw.Manifest.RawSHA256,
		}}
		return status, binding, nil, reason
	}
	if raw.Manifest.FailureCode != "" {
		return unavailable("rehearsal_identity_failure")
	}
	if raw.Manifest.Accepted == 0 {
		return unavailable("no_accepted_frames")
	}
	if raw.Manifest.Accepted == 1 {
		return unavailable("insufficient_frames")
	}
	baseline, baselineSHA, err := loadBaselineCoverageFrames(filepath.Join(repo, "internal/integration/m4/testdata/captured_gsi_schedule.json"))
	if err != nil || baselineSHA != CapturedScheduleSHA256 {
		return unavailable("baseline_identity_failure")
	}
	delta, err := GenerateFieldCoverageDelta(baseline, raw.Frames, baselineSHA, raw.Manifest.RawSHA256, rootSHA)
	if err != nil {
		return unavailable("coverage_generation_failure")
	}
	payload, _ := canonical(delta)
	binding = RehearsalCoverageBindingV1{State: "complete", ArtifactPath: "evidence/canonical/field-coverage-delta.json", SHA256: payloadSHA(payload), RootSHA256: rootSHA}
	return FieldCoverageStatusV1{State: "complete", Delta: &delta}, binding, payload, ""
}

func deriveRehearsalFailure(readiness RehearsalReadinessV1, request RehearsalAttemptRequestV1, manifest RehearsalRawManifestV1, captureBinding rehearsalCaptureBinding, evaluationFailure, recoveryFailure, coverageFailure string) string {
	switch {
	case !readiness.Ready:
		return "preflight_refused"
	case manifest.FailureCode != "":
		return manifest.FailureCode
	case manifest.Accepted == 0:
		return "zero_frames"
	case manifest.Accepted == 1:
		return "insufficient_frames"
	case evaluationFailure != "":
		return evaluationFailure
	case recoveryFailure != "":
		return recoveryFailure
	case coverageFailure != "":
		return coverageFailure
	case captureBinding.Failure != "":
		return captureBinding.Failure
	case captureBinding.Facts.MatchID == "" || captureBinding.Facts.MatchID == "0":
		return "invalid_match_id"
	case request.ExpectedMatchID != "" && request.ExpectedMatchID != captureBinding.Facts.MatchID:
		return "expected_match_mismatch"
	case !captureBinding.Facts.EntryBeforeZero:
		return "late_join"
	case !captureBinding.Facts.Continuous:
		return "implausible_frame_continuity"
	case !captureBinding.Facts.ClockOrdered:
		return "clock_reconciliation_failure"
	case !captureBinding.Facts.NormalPostGame || !captureBinding.Facts.WinnerObserved:
		return "post_game_failure"
	default:
		return ""
	}
}

func validateTerminalStructure(terminal RehearsalTerminalV1, request RehearsalAttemptRequestV1, manifest RehearsalRawManifestV1, captureBinding rehearsalCaptureBinding, suppression RehearsalSuppressionEvidenceV1, expectedFailure string) error {
	if terminal.SchemaVersion != RehearsalTerminalSchemaVersion || terminal.Identity != acceptedRehearsalIdentity() || !validGitObjectID(terminal.CandidateCommit) || !validSHA256(terminal.PreflightIndexSHA256) || !validSHA256(terminal.AttemptRootSHA256) || !validSHA256(terminal.AttemptRequestSHA256) || !validSHA256(terminal.CaptureOwnerSHA256) || !validSHA256(terminal.CaptureSealSHA256) || !validSHA256(terminal.HarnessEvidenceSHA256) || !validSHA256(terminal.ProducerReceiptSHA256) || !validSHA256(terminal.RawManifestSHA256) || !validSHA256(terminal.SuppressionSHA256) || !validSHA256(terminal.RecoverySHA256) || !terminal.RecoveryVerified && expectedFailure != "recovery_reproduction_failure" || !terminal.CleanupAdmissible || !terminal.NonResumable || terminal.DerivedFacts != captureBinding.Facts {
		return errors.New("terminal identity/binding contract mismatch")
	}
	if terminal.RecoveryVerified && terminal.RecoverySHA256 != terminal.SuppressionSHA256 {
		return errors.New("no-cache recovery suppression bytes differ from recorded attempt")
	}
	if err := validateCoverageStatus(terminal.Coverage, terminal.CoverageBinding, manifest); err != nil {
		return err
	}
	if err := validateSuppressionEvidence(suppression, manifest); err != nil {
		return err
	}
	if terminal.FailureCode != expectedFailure {
		return errors.New("terminal failure code does not derive from retained attempt state")
	}
	if suppression.FrameCount != manifest.Accepted && terminal.FailureCode != "production_evaluation_failure" && terminal.FailureCode != "historical_claim_produced" && terminal.FailureCode != "suppression_reconciliation_failure" {
		return errors.New("suppression population is spliced from raw attempt")
	}
	if terminal.Outcome == "rehearsal_completed" {
		if terminal.FailureCode != "" || terminal.Coverage.State != "complete" || manifest.Accepted < 2 || captureBinding.Failure != "" || captureBinding.Facts.MatchID == "" || !captureBinding.Facts.EntryBeforeZero || !captureBinding.Facts.ClockOrdered || !captureBinding.Facts.Continuous || !captureBinding.Facts.NormalPostGame || !captureBinding.Facts.WinnerObserved || !terminal.RecoveryVerified {
			return errors.New("completed rehearsal lacks complete exact production-shaped evidence")
		}
	} else if terminal.Outcome != "rehearsal_failed" || terminal.FailureCode == "" {
		return errors.New("terminal outcome/failure contract mismatch")
	}
	return nil
}

func RehearsalAttempt(ctx context.Context, config RehearsalAttemptConfig) (RehearsalTerminalReceiptV1, error) {
	readiness, err := VerifyRehearsalPreflight(ctx, config.DataRoot, config.RepoRoot)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	lease, err := acquireExistingRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	defer lease.Close()
	if _, err := readBoundedFile(filepath.Join(lease.abs, "evidence/canonical/terminal.json"), MaxRehearsalArtifactBytes); err == nil || !os.IsNotExist(err) {
		return RehearsalTerminalReceiptV1{}, errors.New("rehearsal attempt is non-resumable and already sealed")
	}
	if readiness.Ready {
		ownerPayload, ownerErr := readBoundedFile(filepath.Join(lease.abs, "capture/owner.json"), MaxRehearsalArtifactBytes)
		var owner RehearsalCaptureOwnerV1
		if ownerErr == nil && strictCanonicalRehearsalJSON(ownerPayload, &owner) == nil {
			_ = produceRehearsalEvidence(ctx, lease.abs, config.RepoRoot, owner, config.Request.ExpectedMatchID)
		}
	}
	captureBinding, raw := loadRehearsalCapture(lease.abs, readiness, config.Request)
	built, err := buildRehearsalAttempt(config.RepoRoot, readiness, config.Request, raw, captureBinding)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	writes := []struct {
		path    string
		payload []byte
	}{
		{"input/attempt-request.json", built.RequestPayload},
		{"evidence/canonical/raw-manifest.json", built.ManifestPayload},
		{"evidence/canonical/suppression-evidence.json", built.SuppressionPayload},
	}
	if len(built.CoveragePayload) > 0 {
		writes = append(writes, struct {
			path    string
			payload []byte
		}{"evidence/canonical/field-coverage-delta.json", built.CoveragePayload})
	}
	writes = append(writes,
		struct {
			path    string
			payload []byte
		}{"evidence/canonical/terminal.json", built.TerminalPayload},
		struct {
			path    string
			payload []byte
		}{"evidence/terminal-receipt.json", built.ReceiptPayload},
	)
	for _, write := range writes {
		if err := writePrivate(filepath.Join(lease.abs, filepath.FromSlash(write.path)), write.payload); err != nil {
			return RehearsalTerminalReceiptV1{}, err
		}
	}
	return built.Receipt, nil
}

func VerifyRehearsalTerminal(ctx context.Context, root, repo string) (RehearsalTerminalReceiptV1, error) {
	readiness, err := VerifyRehearsalPreflight(ctx, root, repo)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	lease, err := acquireExistingRoot(root, repo)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	defer lease.Close()
	requestPayload, err := readBoundedFile(filepath.Join(lease.abs, "input/attempt-request.json"), MaxRehearsalArtifactBytes)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	var request RehearsalAttemptRequestV1
	if err := strictCanonicalRehearsalJSON(requestPayload, &request); err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	captureBinding, raw := loadRehearsalCapture(lease.abs, readiness, request)
	rebuilt, err := buildRehearsalAttempt(repo, readiness, request, raw, captureBinding)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	comparisons := []struct {
		path    string
		payload []byte
	}{
		{"evidence/canonical/raw-manifest.json", rebuilt.ManifestPayload},
		{"evidence/canonical/suppression-evidence.json", rebuilt.SuppressionPayload},
		{"evidence/canonical/terminal.json", rebuilt.TerminalPayload},
		{"evidence/terminal-receipt.json", rebuilt.ReceiptPayload},
	}
	if len(rebuilt.CoveragePayload) > 0 {
		comparisons = append(comparisons, struct {
			path    string
			payload []byte
		}{"evidence/canonical/field-coverage-delta.json", rebuilt.CoveragePayload})
	} else if _, err := readBoundedFile(filepath.Join(lease.abs, "evidence/canonical/field-coverage-delta.json"), MaxRehearsalArtifactBytes); err == nil || !os.IsNotExist(err) {
		return RehearsalTerminalReceiptV1{}, errors.New("unavailable coverage carries a partial delta artifact")
	}
	for _, comparison := range comparisons {
		observed, readErr := readBoundedFile(filepath.Join(lease.abs, filepath.FromSlash(comparison.path)), MaxRehearsalArtifactBytes)
		if readErr != nil || !bytes.Equal(observed, comparison.payload) {
			return RehearsalTerminalReceiptV1{}, fmt.Errorf("retained-input regeneration mismatch: %s", comparison.path)
		}
	}
	if err := rejectAcceptanceCapability(rebuilt.TerminalPayload, rebuilt.ReceiptPayload, rebuilt.SuppressionPayload); err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	return rebuilt.Receipt, nil
}

func CleanupRehearsal(root, repo, confirmation string) error {
	receipt, err := VerifyRehearsalTerminal(context.Background(), root, repo)
	if err != nil {
		return fmt.Errorf("refusing cleanup without independently verified terminal: %w", err)
	}
	if confirmation == "" || confirmation != receipt.TerminalSHA256 {
		return errors.New("cleanup confirmation must equal verified terminal SHA-256")
	}
	lease, err := acquireExistingRoot(root, repo)
	if err != nil {
		return err
	}
	defer lease.Close()
	return lease.removeAll()
}

func strictCanonicalRehearsalJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("rehearsal JSON contains trailing data")
	}
	canonicalPayload, err := canonical(target)
	if err != nil || !bytes.Equal(payload, canonicalPayload) {
		return errors.New("rehearsal JSON is not canonical")
	}
	return nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := rootOpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("bounded evidence file size rejected")
	}
	payload := make([]byte, info.Size())
	if _, err := io.ReadFull(file, payload); err != nil {
		return nil, err
	}
	var extra [1]byte
	if n, err := file.Read(extra[:]); n != 0 || err != io.EOF {
		return nil, errors.New("bounded evidence file changed during read")
	}
	return payload, nil
}

func firstN(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[:count]
}

func rejectAcceptanceCapability(payloads ...[]byte) error {
	for _, payload := range payloads {
		for _, forbidden := range []string{"ARMED", "DOT-64", "DOT-62", "DOT-70", "mention://", "public_tournament", "MatchAuthorityRootV1", "steamid", "account_id", "persona_name", "display_name"} {
			if bytes.Contains(payload, []byte(forbidden)) {
				return fmt.Errorf("rehearsal artifact contains forbidden acceptance/identity capability: %s", forbidden)
			}
		}
	}
	return nil
}

func verifyCurrentRehearsalCandidate(ctx context.Context, repo string, evidence RehearsalEvidenceV1) error {
	head, headErr := runText(ctx, repo, "git", "rev-parse", "HEAD")
	parents, parentErr := runText(ctx, repo, "git", "show", "-s", "--format=%P", "HEAD")
	tree, treeErr := runText(ctx, repo, "git", "rev-parse", "HEAD^{tree}")
	status, statusErr := runText(ctx, repo, "git", "status", "--porcelain=v1", "--untracked-files=all")
	remoteURL, remoteErr := runText(ctx, repo, "git", "remote", "get-url", "origin")
	remoteOutput, branchErr := runText(ctx, repo, "git", "ls-remote", "--heads", "origin", "refs/heads/"+ExpectedRehearsalBranch)
	pr, prErr := rehearsalPRSnapshot(ctx, repo)
	if headErr != nil || parentErr != nil || treeErr != nil || statusErr != nil || remoteErr != nil || branchErr != nil || prErr != nil ||
		head != evidence.CandidateCommit || parents != RequiredRehearsalParent || len(strings.Fields(parents)) != 1 || tree != evidence.CandidateTree || status != "" ||
		remoteURL != evidence.RemoteURL || remoteURL != ExpectedRemoteURL || evidence.RemoteBranch != ExpectedRehearsalBranch || firstField(remoteOutput) != head || evidence.RemoteBranchCommit != head ||
		!pr.IsDraft || pr.Number != evidence.DraftPRNumber || pr.URL != evidence.DraftPRURL || pr.HeadRefOID != head || evidence.DraftPRHeadCommit != head {
		return errors.New("current repository/remote/draft PR differs from sealed rehearsal candidate")
	}
	return nil
}

type rehearsalPR struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	HeadRefOID string `json:"headRefOid"`
	IsDraft    bool   `json:"isDraft"`
}

func rehearsalPRSnapshot(ctx context.Context, repo string) (rehearsalPR, error) {
	payload, err := runText(ctx, repo, "gh", "pr", "view", ExpectedRehearsalBranch, "--json", "number,url,headRefOid,isDraft")
	if err != nil {
		return rehearsalPR{}, err
	}
	var result rehearsalPR
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return rehearsalPR{}, err
	}
	return result, nil
}

func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func checkState(ok bool) string {
	if ok {
		return "passed"
	}
	return "failed"
}

func validGitObjectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateCoverageStatus(status FieldCoverageStatusV1, binding RehearsalCoverageBindingV1, manifest RehearsalRawManifestV1) error {
	if !validSHA256(binding.RootSHA256) {
		return errors.New("coverage root binding is invalid")
	}
	if status.State == "complete" {
		if status.Delta == nil || status.Unavailable != nil || binding.State != "complete" || binding.ArtifactPath != "evidence/canonical/field-coverage-delta.json" || !validSHA256(binding.SHA256) || ValidateFieldCoverageDelta(*status.Delta) != nil || status.Delta.RehearsalSourceFrameCount != manifest.Accepted || status.Delta.RehearsalRawSessionSHA256 != manifest.RawSHA256 || status.Delta.EvidenceRootSHA256 != binding.RootSHA256 {
			return errors.New("complete coverage does not bind exact raw population")
		}
		return nil
	}
	if status.State != "unavailable" || status.Delta != nil || status.Unavailable == nil || binding.State != "unavailable" || binding.ArtifactPath != "" || binding.SHA256 != "" {
		return errors.New("coverage status is not a closed union")
	}
	allowed := map[string]bool{"no_accepted_frames": true, "insufficient_frames": true, "baseline_identity_failure": true, "rehearsal_identity_failure": true, "coverage_generation_failure": true}
	value := status.Unavailable
	if !allowed[value.Reason] || value.ObservedFrames != manifest.Observed || value.AcceptedFrames != manifest.Accepted || value.RejectedFrames != manifest.Rejected || value.ObservedFrames != value.AcceptedFrames+value.RejectedFrames || value.BaselineRawSessionSHA256 != CapturedScheduleSHA256 || value.RehearsalRawSHA256 != manifest.RawSHA256 {
		return errors.New("unavailable coverage reason/counters/hashes do not reconcile")
	}
	if value.Reason == "no_accepted_frames" && manifest.Accepted != 0 || value.Reason == "insufficient_frames" && manifest.Accepted != 1 || value.Reason == "rehearsal_identity_failure" && manifest.FailureCode == "" {
		return errors.New("coverage unavailable reason disagrees with retained input")
	}
	return nil
}
