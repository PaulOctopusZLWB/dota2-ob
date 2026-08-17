package m4match

import (
	"bufio"
	"bytes"
	"context"
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
	ExpectedRehearsalBranch         = "agent/dota2-fullstack-engineer/DOT-84-public-match-rehearsal-replacement"
	RehearsalHumanInstruction       = "After REHEARSAL_READY, Paul may manually choose and join one publicly spectatable public match before 0:00. This rehearsal never arms or advances P4/M4 acceptance."
	rehearsalRefusedConsoleState    = "REFUSED"
	rehearsalReadyConsoleState      = "REHEARSAL_READY"
)

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
	Failures            []string            `json:"failures"`
	HumanInstruction    string              `json:"human_instruction,omitempty"`
}

type RehearsalPreflightConfig struct {
	DataRoot string
	RepoRoot string
	Listen   func(string) (io.Closer, error)
}

func RehearsalPreflight(ctx context.Context, config RehearsalPreflightConfig) (RehearsalReadinessV1, error) {
	lease, err := acquireFreshRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	defer lease.Close()
	head, headErr := runText(ctx, config.RepoRoot, "git", "rev-parse", "HEAD")
	parents, parentErr := runText(ctx, config.RepoRoot, "git", "show", "-s", "--format=%P", "HEAD")
	tree, treeErr := runText(ctx, config.RepoRoot, "git", "rev-parse", "HEAD^{tree}")
	status, statusErr := runText(ctx, config.RepoRoot, "git", "status", "--porcelain=v1", "--untracked-files=all")
	remoteURL, remoteErr := runText(ctx, config.RepoRoot, "git", "remote", "get-url", "origin")
	remoteOutput, remoteBranchErr := runText(ctx, config.RepoRoot, "git", "ls-remote", "--heads", "origin", "refs/heads/"+ExpectedRehearsalBranch)
	remoteCommit := firstField(remoteOutput)
	pr, prErr := rehearsalPRSnapshot(ctx, config.RepoRoot)
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
	evidence := RehearsalEvidenceV1{
		SchemaVersion: RehearsalEvidenceSchemaVersion, Mode: "preflight", Identity: acceptedRehearsalIdentity(),
		CandidateCommit: head, CandidateSoleParent: parents, CandidateTree: tree,
		RemoteURL: remoteURL, RemoteBranch: ExpectedRehearsalBranch, RemoteBranchCommit: remoteCommit,
		DraftPRNumber: pr.Number, DraftPRURL: pr.URL, DraftPRHeadCommit: pr.HeadRefOID,
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
		EvidenceIndexSHA256: payloadSHA(evidencePayload), Failures: failures,
	}
	if readiness.Ready {
		readiness.ConsoleState = rehearsalReadyConsoleState
		readiness.HumanInstruction = RehearsalHumanInstruction
	}
	return readiness
}

func VerifyRehearsalPreflight(ctx context.Context, root, repo string) (RehearsalReadinessV1, error) {
	lease, err := acquireExistingRoot(root, repo)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	defer lease.Close()
	readinessPayload, err := rootReadFile(filepath.Join(lease.abs, "evidence/readiness.json"))
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	var readiness RehearsalReadinessV1
	if err := strictCanonicalRehearsalJSON(readinessPayload, &readiness); err != nil {
		return RehearsalReadinessV1{}, err
	}
	indexPayload, err := rootReadFile(filepath.Join(lease.abs, "evidence/canonical/evidence-index.json"))
	if err != nil || payloadSHA(indexPayload) != readiness.EvidenceIndexSHA256 {
		return RehearsalReadinessV1{}, errors.New("rehearsal evidence index hash mismatch")
	}
	var evidence RehearsalEvidenceV1
	if err := strictCanonicalRehearsalJSON(indexPayload, &evidence); err != nil {
		return RehearsalReadinessV1{}, err
	}
	if evidence.SchemaVersion != RehearsalEvidenceSchemaVersion || evidence.Mode != "preflight" || evidence.Identity != acceptedRehearsalIdentity() || evidence.UnavailableIdentity != unavailablePublicMatchIdentity() || !evidence.NonResumable || readiness.SchemaVersion != RehearsalReadinessSchemaVersion || readiness.Identity != evidence.Identity || readiness.CandidateCommit != evidence.CandidateCommit {
		return RehearsalReadinessV1{}, errors.New("rehearsal preflight identity mismatch")
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
	if err := verifyCurrentRehearsalCandidate(ctx, repo, evidence); err != nil {
		return RehearsalReadinessV1{}, err
	}
	if err := rejectAcceptanceCapability(readinessPayload, indexPayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	return readiness, nil
}

func rehearsalCheckFailures(checks []RehearsalCheckV1) []string {
	failures := make([]string, 0)
	seen := map[string]bool{}
	for _, check := range checks {
		if check.ID == "" || seen[check.ID] || check.State != "passed" && check.State != "failed" && check.State != "not_applicable_rehearsal" {
			return []string{"invalid_check_contract"}
		}
		seen[check.ID] = true
		if check.State == "failed" {
			failures = append(failures, check.ID)
		}
	}
	sort.Strings(failures)
	return failures
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
	SchemaVersion           string `json:"schema_version"`
	SessionID               string `json:"session_id"`
	MatchID                 string `json:"match_id"`
	PreviewAccepted         bool   `json:"preview_accepted"`
	EntryBeforeZero         bool   `json:"entry_before_zero"`
	LateJoin                bool   `json:"late_join"`
	ProcessStable           bool   `json:"process_stable"`
	ProcessReplaced         bool   `json:"process_replaced"`
	ProcessExecutableSHA256 string `json:"process_executable_sha256"`
	ProcessStartTicks       uint64 `json:"process_start_ticks"`
	PlausibleContinuous     bool   `json:"plausible_continuous_frames"`
	NormalPostGame          bool   `json:"normal_post_game"`
	OBSFinalized            bool   `json:"obs_finalized"`
	RawReconciled           bool   `json:"raw_first_reconciled"`
	ClocksValid             bool   `json:"clocks_valid"`
	ResourcesSampled        bool   `json:"resources_sampled"`
	OperatorActions         uint8  `json:"operator_actions"`
	FailClosedWithin2S      bool   `json:"fail_closed_within_2s"`
	ConfinementVerified     bool   `json:"confinement_verified"`
	CleanExit               bool   `json:"clean_exit"`
}

type RehearsalAttemptConfig struct {
	DataRoot string
	RepoRoot string
	RawPath  string
	Request  RehearsalAttemptRequestV1
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
	Executed         bool   `json:"executed"`
	FallbackUsed     bool   `json:"fallback_used"`
}

type SuppressionAuditV1 struct {
	SchemaVersion    string `json:"schema_version"`
	SourceSequence   uint64 `json:"source_sequence"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
	Family           string `json:"family"`
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
	SchemaVersion        string                     `json:"schema_version"`
	Identity             RehearsalIdentityV1        `json:"identity"`
	CandidateCommit      string                     `json:"candidate_commit"`
	PreflightIndexSHA256 string                     `json:"preflight_index_sha256"`
	AttemptRootSHA256    string                     `json:"attempt_root_sha256"`
	AttemptRequestSHA256 string                     `json:"attempt_request_sha256"`
	RawManifestSHA256    string                     `json:"raw_manifest_sha256"`
	SuppressionSHA256    string                     `json:"suppression_sha256"`
	RecoverySHA256       string                     `json:"recovery_suppression_sha256"`
	RecoveryVerified     bool                       `json:"recovery_verified"`
	Coverage             FieldCoverageStatusV1      `json:"field_coverage"`
	CoverageBinding      RehearsalCoverageBindingV1 `json:"coverage_binding"`
	Outcome              string                     `json:"outcome"`
	FailureCode          string                     `json:"failure_code,omitempty"`
	CleanupAdmissible    bool                       `json:"cleanup_admissible"`
	NonResumable         bool                       `json:"non_resumable"`
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
	Payload  []byte
}

func scanRehearsalRaw(payload []byte, sessionID string) scannedRehearsalRaw {
	result := scannedRehearsalRaw{Payload: append([]byte(nil), payload...)}
	result.Manifest = RehearsalRawManifestV1{SchemaVersion: RehearsalRawManifestSchema, SessionID: sessionID, RawSHA256: payloadSHA(payload), RawBytes: int64(len(payload)), Records: []RehearsalRawIdentityV1{}}
	if len(payload) == 0 {
		return result
	}
	reader := bufio.NewReaderSize(bytes.NewReader(payload), 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		result.Manifest.Observed++
		if err != nil || len(line) < 2 || line[len(line)-1] != '\n' {
			result.Manifest.Rejected++
			result.Manifest.FailureCode = "partial_frame"
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
	return result
}

type suppressionCollector struct {
	recordBySequence map[uint64]RehearsalRawIdentityV1
	executions       []SuppressionExecutionV1
}

func (collector *suppressionCollector) HistoricalUnavailable(sequence uint64, rawPayloadSHA256, family string) {
	identity := collector.recordBySequence[sequence]
	collector.executions = append(collector.executions, SuppressionExecutionV1{
		SourceSequence: sequence, RawRecordSHA256: identity.RawRecordSHA256, RawPayloadSHA256: rawPayloadSHA256,
		Family: family, Executed: true,
	})
}

func evaluateRehearsalProduction(raw scannedRehearsalRaw) (RehearsalSuppressionEvidenceV1, string) {
	families := contracts.HistoricalDisabledFamiliesV1()
	registryPayload, _ := contracts.MarshalCanonical(families)
	result := RehearsalSuppressionEvidenceV1{SchemaVersion: RehearsalSuppressionSchema, RegistrySHA256: payloadSHA(registryPayload), Executions: []SuppressionExecutionV1{}, Audits: []SuppressionAuditV1{}}
	if len(raw.Records) == 0 {
		return result, ""
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
		candidates := insight.ExecuteLiveOnly(insight.LiveOnlyExecutionInput{
			LiveOnlyInput: insight.LiveOnlyInput{
				Observation: observation, Previous: previous, History: history, Lineage: lineage,
				PolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli(),
			},
			HistoricalUnavailableObserver: collector,
		}, insight.DefaultConfig())
		for _, candidate := range candidates {
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
			Family: execution.Family, Unavailable: "unavailable_for_public_match", Result: "suppressed",
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
	if value.SchemaVersion != RehearsalSuppressionSchema || value.RegistrySHA256 != payloadSHA(registryPayload) || value.FrameCount > manifest.Accepted || len(value.Executions) != int(value.FrameCount)*len(families) || len(value.Audits) != len(value.Executions) {
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
		if !ok || !containsString(families, execution.Family) || executions[key].Family != "" || !execution.Executed || execution.FallbackUsed || execution.RawRecordSHA256 != record.RawRecordSHA256 || execution.RawPayloadSHA256 != record.RawPayloadSHA256 {
			return errors.New("suppression execution is missing, duplicate, fallback-backed, or raw-unbound")
		}
		executions[key] = execution
	}
	audits := map[string]SuppressionAuditV1{}
	for _, audit := range value.Audits {
		key := fmt.Sprintf("%d/%s", audit.SourceSequence, audit.Family)
		execution, ok := executions[key]
		if !ok || audits[key].Family != "" || audit.SchemaVersion != RehearsalSuppressionSchema || audit.RawRecordSHA256 != execution.RawRecordSHA256 || audit.RawPayloadSHA256 != execution.RawPayloadSHA256 || audit.Unavailable != "unavailable_for_public_match" || audit.Result != "suppressed" || audit.CandidateClaim || audit.DecisionClaim || audit.OverlayClaim || audit.DisplayProduced {
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

func buildRehearsalAttempt(repo string, readiness RehearsalReadinessV1, request RehearsalAttemptRequestV1, raw scannedRehearsalRaw) (builtRehearsalAttempt, error) {
	if request.SchemaVersion != RehearsalAttemptSchemaVersion || request.SessionID == "" || strings.ContainsAny(request.SessionID, "/\\\x00\r\n") || raw.Manifest.SessionID != request.SessionID {
		return builtRehearsalAttempt{}, errors.New("invalid rehearsal attempt request/session")
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
		Domain, AcceptedSpec, CandidateCommit, PreflightIndexSHA256, RequestSHA256, RawManifestSHA256 string
	}{rehearsalRootDomain, AcceptedRehearsalSpec, readiness.CandidateCommit, readiness.EvidenceIndexSHA256, requestSHA, manifestSHA})
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

	failure := deriveRehearsalFailure(readiness, request, raw.Manifest, evaluationFailure, recoveryFailure, coverageFailure)
	outcome := "rehearsal_failed"
	if failure == "" {
		outcome = "rehearsal_completed"
	}
	terminal := RehearsalTerminalV1{
		SchemaVersion: RehearsalTerminalSchemaVersion, Identity: acceptedRehearsalIdentity(),
		CandidateCommit: readiness.CandidateCommit, PreflightIndexSHA256: readiness.EvidenceIndexSHA256,
		AttemptRootSHA256: attemptRootSHA, AttemptRequestSHA256: requestSHA, RawManifestSHA256: manifestSHA,
		SuppressionSHA256: payloadSHA(suppressionPayload), RecoverySHA256: payloadSHA(recoveryPayload), RecoveryVerified: recoveryFailure == "",
		Coverage: coverage, CoverageBinding: binding,
		Outcome: outcome, FailureCode: failure, CleanupAdmissible: true, NonResumable: true,
	}
	if err := validateTerminalStructure(terminal, request, raw.Manifest, suppression, failure); err != nil {
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

func deriveRehearsalFailure(readiness RehearsalReadinessV1, request RehearsalAttemptRequestV1, manifest RehearsalRawManifestV1, evaluationFailure, recoveryFailure, coverageFailure string) string {
	switch {
	case !readiness.Ready:
		return "preflight_refused"
	case !request.PreviewAccepted:
		return "preview_refused"
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
	case request.LateJoin:
		return "late_join"
	case request.MatchID == "" || request.MatchID == "0":
		return "invalid_match_id"
	case !request.EntryBeforeZero:
		return "late_join"
	case request.ProcessReplaced:
		return "process_replaced"
	case !request.ProcessStable:
		return "process_lost"
	case !validSHA256(request.ProcessExecutableSHA256) || request.ProcessStartTicks == 0:
		return "process_identity_failure"
	case !request.PlausibleContinuous:
		return "implausible_frame_continuity"
	case !request.NormalPostGame:
		return "post_game_failure"
	case !request.OBSFinalized:
		return "obs_finalization_failure"
	case !request.RawReconciled:
		return "raw_reconciliation_failure"
	case !request.ClocksValid:
		return "clock_reconciliation_failure"
	case !request.ResourcesSampled:
		return "resource_sampling_failure"
	case request.OperatorActions != 4:
		return "operator_script_failure"
	case !request.FailClosedWithin2S:
		return "fail_closed_deadline_failure"
	case !request.ConfinementVerified:
		return "confinement_failure"
	case !request.CleanExit:
		return "unclean_exit"
	default:
		return ""
	}
}

func validateTerminalStructure(terminal RehearsalTerminalV1, request RehearsalAttemptRequestV1, manifest RehearsalRawManifestV1, suppression RehearsalSuppressionEvidenceV1, expectedFailure string) error {
	if terminal.SchemaVersion != RehearsalTerminalSchemaVersion || terminal.Identity != acceptedRehearsalIdentity() || !validGitObjectID(terminal.CandidateCommit) || !validSHA256(terminal.PreflightIndexSHA256) || !validSHA256(terminal.AttemptRootSHA256) || !validSHA256(terminal.AttemptRequestSHA256) || !validSHA256(terminal.RawManifestSHA256) || !validSHA256(terminal.SuppressionSHA256) || !validSHA256(terminal.RecoverySHA256) || !terminal.RecoveryVerified && expectedFailure != "recovery_reproduction_failure" || !terminal.CleanupAdmissible || !terminal.NonResumable {
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
		if terminal.FailureCode != "" || terminal.Coverage.State != "complete" || manifest.Accepted < 2 || !request.PreviewAccepted || request.LateJoin || request.MatchID == "" || request.MatchID == "0" || !request.EntryBeforeZero || !request.ProcessStable || request.ProcessReplaced || !validSHA256(request.ProcessExecutableSHA256) || request.ProcessStartTicks == 0 || !request.PlausibleContinuous || !request.NormalPostGame || !request.OBSFinalized || !request.RawReconciled || !request.ClocksValid || !request.ResourcesSampled || request.OperatorActions != 4 || !request.FailClosedWithin2S || !request.ConfinementVerified || !request.CleanExit || !terminal.RecoveryVerified {
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
	if _, err := rootReadFile(filepath.Join(lease.abs, "evidence/canonical/terminal.json")); err == nil || !os.IsNotExist(err) {
		return RehearsalTerminalReceiptV1{}, errors.New("rehearsal attempt is non-resumable and already sealed")
	}
	var rawPayload []byte
	if config.RawPath != "" {
		info, statErr := os.Lstat(config.RawPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return RehearsalTerminalReceiptV1{}, errors.New("raw session must be one retained regular file")
		}
		rawPayload, err = os.ReadFile(config.RawPath)
		if err != nil {
			return RehearsalTerminalReceiptV1{}, err
		}
	}
	raw := scanRehearsalRaw(rawPayload, config.Request.SessionID)
	built, err := buildRehearsalAttempt(config.RepoRoot, readiness, config.Request, raw)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	writes := []struct {
		path    string
		payload []byte
	}{
		{"input/attempt-request.json", built.RequestPayload},
		{"input/raw.jsonl", rawPayload},
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
	requestPayload, err := rootReadFile(filepath.Join(lease.abs, "input/attempt-request.json"))
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	var request RehearsalAttemptRequestV1
	if err := strictCanonicalRehearsalJSON(requestPayload, &request); err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	rawPayload, err := rootReadFile(filepath.Join(lease.abs, "input/raw.jsonl"))
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	raw := scanRehearsalRaw(rawPayload, request.SessionID)
	rebuilt, err := buildRehearsalAttempt(repo, readiness, request, raw)
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
	} else if _, err := rootReadFile(filepath.Join(lease.abs, "evidence/canonical/field-coverage-delta.json")); err == nil || !os.IsNotExist(err) {
		return RehearsalTerminalReceiptV1{}, errors.New("unavailable coverage carries a partial delta artifact")
	}
	for _, comparison := range comparisons {
		observed, readErr := rootReadFile(filepath.Join(lease.abs, filepath.FromSlash(comparison.path)))
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
