package m4match

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	AcceptedRehearsalSpec           = "958c3f0d3fd464df4960905bc4bb7918521884e6"
	RequiredRehearsalParent         = "abb4210257f26364c2a539de90e386f411f3229a"
	RehearsalSchemaVersion          = "public_match_rehearsal_evidence.v1"
	RehearsalReadinessSchemaVersion = "public_match_rehearsal_readiness.v1"
	RehearsalTerminalSchemaVersion  = "public_match_rehearsal_terminal.v1"
	rehearsalRootDomain             = "dota2-ob.m4.public-match-rehearsal.v1"
	RehearsalHumanInstruction       = "After REHEARSAL_READY, Paul may manually choose and join one publicly spectatable public match before 0:00. This rehearsal never arms or advances P4/M4 acceptance."
	ExpectedRehearsalBranch         = "agent/dota2-fullstack-engineer/DOT-84-public-match-rehearsal"
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
	result := RehearsalClassificationV1{Purpose: RehearsalPurposeV1(purpose), Class: RehearsalMatchClassV1(class)}
	if result.Purpose != PurposePublicMatchRehearsal || result.Class != MatchClassPublicMatch {
		return RehearsalClassificationV1{}, errors.New("only public_match_rehearsal + public_match is legal")
	}
	return result, nil
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

func UnavailablePublicMatchIdentity() PublicMatchUnavailableIdentityV1 {
	return PublicMatchUnavailableIdentityV1{
		Competition: "unavailable_for_public_match", League: "unavailable_for_public_match",
		Series: "unavailable_for_public_match", GameNumber: "unavailable_for_public_match",
		Teams: "unavailable_for_public_match", Roster: "unavailable_for_public_match",
		Organizer: "unavailable_for_public_match", TIIdentity: "unavailable_for_public_match",
	}
}

type SuppressionBranchV1 struct {
	ID     string `json:"id"`
	Family string `json:"family"`
}

var acceptedSuppressionBranches = []SuppressionBranchV1{
	{ID: "historical_baseline.lookup", Family: "history"},
	{ID: "professional_roster.resolve", Family: "roster"},
	{ID: "series_game.qualify", Family: "series"},
	{ID: "tournament_identity.resolve", Family: "tournament"},
}

type SuppressionInputV1 struct {
	Availability string `json:"availability"`
}

type SuppressionExecutionV1 struct {
	Sequence     uint64 `json:"source_sequence"`
	Branch       string `json:"branch"`
	Family       string `json:"family"`
	Executed     bool   `json:"executed"`
	FallbackUsed bool   `json:"fallback_used"`
}

type SuppressionAuditV1 struct {
	SchemaVersion   string `json:"schema_version"`
	SourceSequence  uint64 `json:"source_sequence"`
	Branch          string `json:"branch"`
	Family          string `json:"family"`
	Unavailable     string `json:"unavailable_reason"`
	Result          string `json:"result"`
	CandidateClaim  bool   `json:"candidate_claim"`
	DecisionClaim   bool   `json:"decision_claim"`
	OverlayClaim    bool   `json:"overlay_claim"`
	RuntimeExecuted bool   `json:"runtime_executed"`
}

type SuppressionRuntimeV1 struct {
	executions []SuppressionExecutionV1
	audits     []SuppressionAuditV1
}

func NewSuppressionRuntime() *SuppressionRuntimeV1 { return &SuppressionRuntimeV1{} }

func (runtime *SuppressionRuntimeV1) Evaluate(sequence uint64, branch string, input SuppressionInputV1) error {
	if sequence == 0 || input.Availability != "unavailable_for_public_match" {
		return errors.New("suppression requires a sequence-linked typed unavailable input")
	}
	var selected *SuppressionBranchV1
	for index := range acceptedSuppressionBranches {
		if acceptedSuppressionBranches[index].ID == branch {
			selected = &acceptedSuppressionBranches[index]
			break
		}
	}
	if selected == nil {
		return errors.New("suppression branch is not in the accepted registry")
	}
	runtime.executions = append(runtime.executions, SuppressionExecutionV1{Sequence: sequence, Branch: selected.ID, Family: selected.Family, Executed: true})
	runtime.audits = append(runtime.audits, SuppressionAuditV1{
		SchemaVersion: "public_match_suppression_audit.v1", SourceSequence: sequence,
		Branch: selected.ID, Family: selected.Family, Unavailable: input.Availability,
		Result: "suppressed", RuntimeExecuted: true,
	})
	return nil
}

func (runtime *SuppressionRuntimeV1) Evidence() ([]SuppressionExecutionV1, []SuppressionAuditV1) {
	return append([]SuppressionExecutionV1(nil), runtime.executions...), append([]SuppressionAuditV1(nil), runtime.audits...)
}

// RehearsalProductionAdapter is the production-shaped entry point for all
// tournament/history-dependent public-match branches. Suppression audits are
// emitted at the point of execution and cannot be supplied by the caller.
type RehearsalProductionAdapter struct{ runtime *SuppressionRuntimeV1 }

func NewRehearsalProductionAdapter(runtime *SuppressionRuntimeV1) RehearsalProductionAdapter {
	return RehearsalProductionAdapter{runtime: runtime}
}

func (adapter RehearsalProductionAdapter) EvaluateFrame(sequence uint64, input SuppressionInputV1) error {
	if adapter.runtime == nil {
		return errors.New("suppression runtime is required")
	}
	for _, evaluate := range []func(uint64, SuppressionInputV1) error{
		adapter.historicalBaseline, adapter.professionalRoster, adapter.seriesGame, adapter.tournamentIdentity,
	} {
		if err := evaluate(sequence, input); err != nil {
			return err
		}
	}
	return nil
}

func (adapter RehearsalProductionAdapter) historicalBaseline(sequence uint64, input SuppressionInputV1) error {
	return adapter.runtime.Evaluate(sequence, "historical_baseline.lookup", input)
}
func (adapter RehearsalProductionAdapter) professionalRoster(sequence uint64, input SuppressionInputV1) error {
	return adapter.runtime.Evaluate(sequence, "professional_roster.resolve", input)
}
func (adapter RehearsalProductionAdapter) seriesGame(sequence uint64, input SuppressionInputV1) error {
	return adapter.runtime.Evaluate(sequence, "series_game.qualify", input)
}
func (adapter RehearsalProductionAdapter) tournamentIdentity(sequence uint64, input SuppressionInputV1) error {
	return adapter.runtime.Evaluate(sequence, "tournament_identity.resolve", input)
}

func validateSuppressionEvidence(executions []SuppressionExecutionV1, audits []SuppressionAuditV1) error {
	if len(executions) != len(acceptedSuppressionBranches) || len(audits) != len(acceptedSuppressionBranches) {
		return errors.New("suppression execution/audit population does not match accepted registry")
	}
	executionByBranch := make(map[string]SuppressionExecutionV1, len(executions))
	auditByBranch := make(map[string]SuppressionAuditV1, len(audits))
	for _, execution := range executions {
		if execution.Branch == "" || execution.Sequence == 0 || !execution.Executed || execution.FallbackUsed || executionByBranch[execution.Branch].Branch != "" {
			return errors.New("suppression contains missing, duplicate, unexecuted, or fallback-backed execution")
		}
		executionByBranch[execution.Branch] = execution
	}
	for _, audit := range audits {
		if audit.SchemaVersion != "public_match_suppression_audit.v1" || audit.SourceSequence == 0 || audit.Unavailable != "unavailable_for_public_match" || audit.Result != "suppressed" || !audit.RuntimeExecuted || audit.CandidateClaim || audit.DecisionClaim || audit.OverlayClaim || auditByBranch[audit.Branch].Branch != "" {
			return errors.New("suppression audit is duplicated, identity-leaking, static, or claim-producing")
		}
		auditByBranch[audit.Branch] = audit
	}
	for _, branch := range acceptedSuppressionBranches {
		execution, executionOK := executionByBranch[branch.ID]
		audit, auditOK := auditByBranch[branch.ID]
		if !executionOK || !auditOK || execution.Family != branch.Family || audit.Family != branch.Family || audit.SourceSequence != execution.Sequence {
			return errors.New("suppression evidence does not reconcile with accepted registry/config identity")
		}
	}
	return nil
}

type RehearsalCheckV1 struct {
	ID    string `json:"id"`
	State string `json:"state"`
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
	RegistrySHA256      string                           `json:"suppression_registry_sha256"`
	UnavailableIdentity PublicMatchUnavailableIdentityV1 `json:"unavailable_identity"`
	Checks              []RehearsalCheckV1               `json:"checks"`
	Executions          []SuppressionExecutionV1         `json:"suppression_executions"`
	Audits              []SuppressionAuditV1             `json:"suppression_audits"`
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
	HumanInstruction    string              `json:"human_instruction"`
}

type RehearsalPreflightConfig struct {
	DataRoot string
	RepoRoot string
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
	checks := []RehearsalCheckV1{
		{ID: "candidate_commit", State: state(headErr == nil && validGitObjectID(head))},
		{ID: "candidate_sole_parent", State: state(parentErr == nil && parents == RequiredRehearsalParent && len(strings.Fields(parents)) == 1)},
		{ID: "candidate_tree", State: state(treeErr == nil && validGitObjectID(tree))},
		{ID: "clean_tree", State: state(statusErr == nil && status == "")},
		{ID: "expected_remote", State: state(remoteErr == nil && remoteURL == ExpectedRemoteURL)},
		{ID: "remote_branch_head", State: state(remoteBranchErr == nil && remoteCommit == head)},
		{ID: "draft_pr_head", State: state(prErr == nil && pr.IsDraft && pr.HeadRefOID == head && pr.Number > 0 && pr.URL != "")},
		{ID: "exclusive_localhost_gsi_listener", State: state(listenerAvailable(CaptureAddress))},
		{ID: "p4_acceptance", State: "not_applicable_rehearsal"},
		{ID: "professional_tournament_authority", State: "not_applicable_rehearsal"},
	}
	runtime := NewSuppressionRuntime()
	adapter := NewRehearsalProductionAdapter(runtime)
	if err := adapter.EvaluateFrame(1, SuppressionInputV1{Availability: "unavailable_for_public_match"}); err != nil {
		return RehearsalReadinessV1{}, err
	}
	executions, audits := runtime.Evidence()
	if err := validateSuppressionEvidence(executions, audits); err != nil {
		return RehearsalReadinessV1{}, err
	}
	registryPayload, _ := canonical(acceptedSuppressionBranches)
	evidence := RehearsalEvidenceV1{
		SchemaVersion: RehearsalSchemaVersion, Mode: "preflight", Identity: acceptedRehearsalIdentity(),
		CandidateCommit: head, CandidateSoleParent: parents, CandidateTree: tree,
		RemoteURL: remoteURL, RemoteBranch: ExpectedRehearsalBranch, RemoteBranchCommit: remoteCommit,
		DraftPRNumber: pr.Number, DraftPRURL: pr.URL, DraftPRHeadCommit: pr.HeadRefOID,
		RegistrySHA256: payloadSHA(registryPayload), UnavailableIdentity: UnavailablePublicMatchIdentity(),
		Checks: checks, Executions: executions, Audits: audits, NonResumable: true,
	}
	sort.Slice(evidence.Checks, func(i, j int) bool { return evidence.Checks[i].ID < evidence.Checks[j].ID })
	evidencePayload, _ := canonical(evidence)
	if err := writePrivate(filepath.Join(lease.abs, "evidence/canonical/evidence-index.json"), evidencePayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	failures := []string{}
	for _, check := range checks {
		if check.State == "failed" {
			failures = append(failures, check.ID)
		}
	}
	sort.Strings(failures)
	readiness := RehearsalReadinessV1{
		SchemaVersion: RehearsalReadinessSchemaVersion, Ready: len(failures) == 0,
		ConsoleState: "REHEARSAL_READY", Identity: acceptedRehearsalIdentity(), CandidateCommit: head,
		EvidenceIndexSHA256: payloadSHA(evidencePayload), Failures: failures, HumanInstruction: RehearsalHumanInstruction,
	}
	if err := writeJSON(filepath.Join(lease.abs, "evidence/readiness.json"), readiness, 0o600); err != nil {
		return RehearsalReadinessV1{}, err
	}
	return readiness, nil
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
	if readiness.SchemaVersion != RehearsalReadinessSchemaVersion || readiness.ConsoleState != "REHEARSAL_READY" || readiness.Identity != acceptedRehearsalIdentity() || evidence.SchemaVersion != RehearsalSchemaVersion || evidence.Mode != "preflight" || evidence.Identity != acceptedRehearsalIdentity() || !evidence.NonResumable || readiness.HumanInstruction != RehearsalHumanInstruction {
		return RehearsalReadinessV1{}, errors.New("rehearsal identity/readiness contract mismatch")
	}
	if evidence.UnavailableIdentity != UnavailablePublicMatchIdentity() {
		return RehearsalReadinessV1{}, errors.New("public-match identity was filled or relabeled")
	}
	registryPayload, _ := canonical(acceptedSuppressionBranches)
	if evidence.RegistrySHA256 != payloadSHA(registryPayload) || validateSuppressionEvidence(evidence.Executions, evidence.Audits) != nil {
		return RehearsalReadinessV1{}, errors.New("runtime suppression evidence mismatch")
	}
	if evidence.CandidateSoleParent != RequiredRehearsalParent || len(strings.Fields(evidence.CandidateSoleParent)) != 1 || evidence.CandidateCommit != readiness.CandidateCommit {
		return RehearsalReadinessV1{}, errors.New("rehearsal candidate ancestry mismatch")
	}
	currentHead, headErr := runText(ctx, repo, "git", "rev-parse", "HEAD")
	currentParents, parentErr := runText(ctx, repo, "git", "show", "-s", "--format=%P", "HEAD")
	currentTree, treeErr := runText(ctx, repo, "git", "rev-parse", "HEAD^{tree}")
	currentStatus, statusErr := runText(ctx, repo, "git", "status", "--porcelain=v1", "--untracked-files=all")
	currentRemote, remoteErr := runText(ctx, repo, "git", "remote", "get-url", "origin")
	remoteOutput, remoteBranchErr := runText(ctx, repo, "git", "ls-remote", "--heads", "origin", "refs/heads/"+ExpectedRehearsalBranch)
	currentPR, prErr := rehearsalPRSnapshot(ctx, repo)
	if headErr != nil || parentErr != nil || treeErr != nil || statusErr != nil || remoteErr != nil || remoteBranchErr != nil || prErr != nil || currentHead != evidence.CandidateCommit || currentParents != RequiredRehearsalParent || len(strings.Fields(currentParents)) != 1 || currentTree != evidence.CandidateTree || currentStatus != "" || currentRemote != evidence.RemoteURL || evidence.RemoteURL != ExpectedRemoteURL || evidence.RemoteBranch != ExpectedRehearsalBranch || firstField(remoteOutput) != evidence.RemoteBranchCommit || evidence.RemoteBranchCommit != currentHead || !currentPR.IsDraft || currentPR.Number != evidence.DraftPRNumber || currentPR.URL != evidence.DraftPRURL || currentPR.HeadRefOID != evidence.DraftPRHeadCommit || evidence.DraftPRHeadCommit != currentHead {
		return RehearsalReadinessV1{}, errors.New("current repository no longer equals the clean single-parent rehearsal candidate")
	}
	forbidden := []string{"ARMED", "DOT-64", "DOT-62", "DOT-70", "mention://", "public_tournament", "MatchAuthorityRootV1"}
	for _, value := range forbidden {
		if bytes.Contains(indexPayload, []byte(value)) || bytes.Contains(readinessPayload, []byte(value)) {
			return RehearsalReadinessV1{}, fmt.Errorf("rehearsal artifact contains forbidden acceptance capability: %s", value)
		}
	}
	failures := make([]string, 0)
	for _, check := range evidence.Checks {
		if check.State != "passed" && check.State != "not_applicable_rehearsal" && check.State != "failed" {
			return RehearsalReadinessV1{}, errors.New("unknown rehearsal check state")
		}
		if check.State == "failed" {
			failures = append(failures, check.ID)
		}
	}
	sort.Strings(failures)
	if !equalStrings(failures, readiness.Failures) || readiness.Ready != (len(failures) == 0) {
		return RehearsalReadinessV1{}, errors.New("rehearsal readiness decision mismatch")
	}
	return readiness, nil
}

func CleanupRehearsal(root, repo, confirmation string) error {
	readiness, err := VerifyRehearsalPreflight(context.Background(), root, repo)
	if err != nil {
		return fmt.Errorf("refusing cleanup of unverifiable rehearsal root: %w", err)
	}
	if confirmation == "" || confirmation != readiness.EvidenceIndexSHA256 {
		return errors.New("cleanup confirmation must equal the rehearsal evidence index SHA-256")
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
	if err := decoder.Decode(&struct{}{}); err == nil {
		return errors.New("multiple JSON values")
	}
	canonicalPayload, err := canonical(target)
	if err != nil || !bytes.Equal(payload, canonicalPayload) {
		return errors.New("rehearsal JSON is not canonical")
	}
	return nil
}

func listenerAvailable(address string) bool {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
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

func state(ok bool) string {
	if ok {
		return "passed"
	}
	return "failed"
}

type FieldCoverageUnavailableV1 struct {
	Reason                   string `json:"reason"`
	ObservedFrames           uint64 `json:"observed_frames"`
	AcceptedFrames           uint64 `json:"accepted_frames"`
	RejectedFrames           uint64 `json:"rejected_frames"`
	BaselineRawSessionSHA256 string `json:"baseline_raw_session_sha256,omitempty"`
	RehearsalRawSHA256       string `json:"rehearsal_raw_session_sha256,omitempty"`
}

type RehearsalCoverageBindingV1 struct {
	ArtifactPath string `json:"artifact_path"`
	SHA256       string `json:"sha256"`
	RootSHA256   string `json:"root_sha256"`
}

type FieldCoverageStatusV1 struct {
	State       string                      `json:"state"`
	Delta       *FieldCoverageDeltaV1       `json:"delta,omitempty"`
	Unavailable *FieldCoverageUnavailableV1 `json:"unavailable,omitempty"`
}

var acceptedCoverageUnavailableReasons = map[string]bool{
	"no_accepted_frames": true, "insufficient_frames": true, "baseline_identity_failure": true,
	"rehearsal_identity_failure": true, "coverage_generation_failure": true,
}

var acceptedRehearsalFailureCodes = map[string]bool{
	"preview_refused": true, "zero_frames": true, "insufficient_frames": true,
	"partial_frame": true, "late_join": true, "malformed_frame": true,
	"process_lost": true, "process_replaced": true, "baseline_identity_failure": true,
	"rehearsal_identity_failure": true, "coverage_generation_failure": true,
	"verifier_restart": true, "operator_abort": true, "obs_finalization_failure": true,
	"raw_reconciliation_failure": true, "cleanup_failure": true,
}

func ValidateFieldCoverageStatus(status FieldCoverageStatusV1) error {
	switch status.State {
	case "complete":
		if status.Delta == nil || status.Unavailable != nil {
			return errors.New("complete coverage must carry exactly one delta")
		}
		return ValidateFieldCoverageDelta(*status.Delta)
	case "unavailable":
		if status.Delta != nil || status.Unavailable == nil || !acceptedCoverageUnavailableReasons[status.Unavailable.Reason] {
			return errors.New("unavailable coverage must carry exactly one accepted typed reason")
		}
		value := status.Unavailable
		if value.ObservedFrames != value.AcceptedFrames+value.RejectedFrames {
			return errors.New("coverage unavailable counters do not reconcile")
		}
		if value.BaselineRawSessionSHA256 != "" && !validSHA256(value.BaselineRawSessionSHA256) || value.RehearsalRawSHA256 != "" && !validSHA256(value.RehearsalRawSHA256) {
			return errors.New("coverage unavailable retained hash is invalid")
		}
		if value.Reason == "no_accepted_frames" && value.AcceptedFrames != 0 || value.Reason == "insufficient_frames" && value.AcceptedFrames != 1 {
			return errors.New("coverage unavailable reason and population disagree")
		}
		return nil
	default:
		return errors.New("unknown field coverage status")
	}
}

type RehearsalAttemptV1 struct {
	ObservedFrames uint64 `json:"observed_frames"`
	AcceptedFrames uint64 `json:"accepted_frames"`
	RejectedFrames uint64 `json:"rejected_frames"`
	ProcessStable  bool   `json:"process_stable"`
	LateJoin       bool   `json:"late_join"`
	OBSFinalized   bool   `json:"obs_finalized"`
	CleanExit      bool   `json:"clean_exit"`
}

type RehearsalTerminalV1 struct {
	SchemaVersion     string                   `json:"schema_version"`
	Identity          RehearsalIdentityV1      `json:"identity"`
	Outcome           string                   `json:"outcome"`
	FailureCode       string                   `json:"failure_code,omitempty"`
	Attempt           RehearsalAttemptV1       `json:"attempt"`
	Coverage          FieldCoverageStatusV1    `json:"field_coverage"`
	Executions        []SuppressionExecutionV1 `json:"suppression_executions"`
	Audits            []SuppressionAuditV1     `json:"suppression_audits"`
	CleanupAdmissible bool                     `json:"cleanup_admissible"`
	NonResumable      bool                     `json:"non_resumable"`
}

func ValidateRehearsalTerminal(result RehearsalTerminalV1) error {
	if result.SchemaVersion != RehearsalTerminalSchemaVersion || result.Identity != acceptedRehearsalIdentity() || !result.CleanupAdmissible || !result.NonResumable {
		return errors.New("rehearsal terminal identity or cleanup contract mismatch")
	}
	if result.Attempt.ObservedFrames != result.Attempt.AcceptedFrames+result.Attempt.RejectedFrames {
		return errors.New("rehearsal terminal frame counters do not reconcile")
	}
	if err := ValidateFieldCoverageStatus(result.Coverage); err != nil {
		return err
	}
	if result.Coverage.State == "complete" && result.Coverage.Delta.RehearsalSourceFrameCount != result.Attempt.AcceptedFrames {
		return errors.New("complete coverage population does not reconcile with accepted frames")
	}
	if result.Coverage.State == "unavailable" && (result.Coverage.Unavailable.ObservedFrames != result.Attempt.ObservedFrames || result.Coverage.Unavailable.AcceptedFrames != result.Attempt.AcceptedFrames || result.Coverage.Unavailable.RejectedFrames != result.Attempt.RejectedFrames) {
		return errors.New("unavailable coverage counters do not reconcile with terminal attempt")
	}
	if err := validateSuppressionEvidence(result.Executions, result.Audits); err != nil {
		return err
	}
	switch result.Outcome {
	case "rehearsal_completed":
		if result.FailureCode != "" || result.Coverage.State != "complete" || result.Attempt.AcceptedFrames == 0 || !result.Attempt.ProcessStable || result.Attempt.LateJoin || !result.Attempt.OBSFinalized || !result.Attempt.CleanExit {
			return errors.New("completed rehearsal lacks complete nonzero production-shaped evidence")
		}
	case "rehearsal_failed":
		if !acceptedRehearsalFailureCodes[result.FailureCode] {
			return errors.New("failed rehearsal lacks deterministic failure code")
		}
	default:
		return errors.New("unknown rehearsal terminal outcome")
	}
	return nil
}

func AssertNoScalarOrIdentityLeak(value any) error {
	payload, err := canonical(value)
	if err != nil {
		return err
	}
	lower := strings.ToLower(string(payload))
	for _, forbidden := range []string{"steamid", "steam_id", "accountid", "account_id", "persona", "display_name", "raw_fragment", "scalar_sample"} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("canonical rehearsal evidence contains forbidden value/identity field: %s", forbidden)
		}
	}
	return nil
}

func removeRehearsalRootForTest(path string) error { return os.RemoveAll(path) }
