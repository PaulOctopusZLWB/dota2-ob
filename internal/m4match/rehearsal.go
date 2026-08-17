package m4match

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const (
	AcceptedRehearsalSpec               = "958c3f0d3fd464df4960905bc4bb7918521884e6"
	RequiredRehearsalParent             = "abb4210257f26364c2a539de90e386f411f3229a"
	ExpectedRehearsalBranch             = "agent/dota2-fullstack-engineer/DOT-84-rehearsal-v6"
	rehearsalDomain                     = "dota2-ob.public-match-rehearsal.v3"
	rehearsalReady                      = "REHEARSAL_READY"
	rehearsalRefused                    = "REFUSED"
	RehearsalHumanInstruction           = "After REHEARSAL_READY, Paul may manually choose and join one publicly spectatable public match before 0:00. This rehearsal never arms or advances P4/M4 acceptance."
	RehearsalAttemptSchemaVersion       = "public_match_rehearsal_attempt.v3"
	MaxRehearsalRawBytes          int64 = 64 << 20
	MaxRehearsalRawLineBytes      int64 = (((10 << 20) + 2) / 3 * 4) + (4 << 10) + 1
	MaxRehearsalRecords                 = 4096
	MaxRehearsalCoverageFrames          = 4096
	MaxRehearsalExecutions              = 32768
	MaxRehearsalAudits                  = 32768
	MaxRehearsalArtifactBytes     int64 = 32 << 20
	MaxRehearsalAttemptDuration         = 30 * time.Minute
)

type RehearsalClassificationV1 struct {
	Purpose string `json:"run_purpose"`
	Class   string `json:"match_class"`
}
type RehearsalIdentityV1 struct {
	AcceptedSpec       string                    `json:"accepted_rehearsal_spec"`
	Classification     RehearsalClassificationV1 `json:"classification"`
	RootDomain         string                    `json:"root_domain"`
	ClaimsP4           bool                      `json:"claims_p4"`
	QualifyingMatch    bool                      `json:"qualifying_match"`
	AcceptanceEligible bool                      `json:"acceptance_eligible"`
	AcceptanceGate     string                    `json:"acceptance_gate"`
}
type RehearsalCheckV1 struct {
	ID    string `json:"id"`
	State string `json:"state"`
}
type RehearsalEvidenceV1 struct {
	SchemaVersion      string              `json:"schema_version"`
	Mode               string              `json:"mode"`
	Identity           RehearsalIdentityV1 `json:"identity"`
	CandidateCommit    string              `json:"candidate_commit"`
	CandidateParent    string              `json:"candidate_sole_parent"`
	CandidateTree      string              `json:"candidate_tree"`
	RemoteURL          string              `json:"remote_url"`
	RemoteBranch       string              `json:"remote_branch"`
	RemoteCommit       string              `json:"remote_commit"`
	DraftPRNumber      int                 `json:"draft_pr_number"`
	DraftPRURL         string              `json:"draft_pr_url"`
	DraftPRCommit      string              `json:"draft_pr_commit"`
	CaptureOwnerSHA256 string              `json:"capture_owner_sha256"`
	Checks             []RehearsalCheckV1  `json:"checks"`
	NonResumable       bool                `json:"non_resumable"`
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
	SchemaVersion   string `json:"schema_version"`
	CandidateCommit string `json:"candidate_commit"`
	SessionID       string `json:"session_id"`
	RawPath         string `json:"raw_relative_path"`
}
type rehearsalPR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Head   string `json:"headRefOid"`
	Draft  bool   `json:"isDraft"`
}
type rehearsalTextCommand func(context.Context, string, string, ...string) (string, error)
type RehearsalPreflightConfig struct {
	DataRoot string
	RepoRoot string
	Listen   func(string) (io.Closer, error)
	run      rehearsalTextCommand
	pr       func(context.Context, string) (rehearsalPR, error)
}

var rehearsalCheckContract = [...]RehearsalCheckV1{
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

func ParseRehearsalClassification(purpose, class string) (RehearsalClassificationV1, error) {
	if purpose != "public_match_rehearsal" || class != "public_match" {
		return RehearsalClassificationV1{}, errors.New("only public_match_rehearsal + public_match is legal")
	}
	return RehearsalClassificationV1{Purpose: purpose, Class: class}, nil
}
func acceptedRehearsalIdentity() RehearsalIdentityV1 {
	return RehearsalIdentityV1{AcceptedSpec: AcceptedRehearsalSpec, Classification: RehearsalClassificationV1{Purpose: "public_match_rehearsal", Class: "public_match"}, RootDomain: rehearsalDomain, ClaimsP4: false, QualifyingMatch: false, AcceptanceEligible: false, AcceptanceGate: "none"}
}

func RehearsalPreflight(ctx context.Context, config RehearsalPreflightConfig) (RehearsalReadinessV1, error) {
	lease, err := acquireFreshRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	defer lease.Close()
	run := config.run
	if run == nil {
		run = runText
	}
	readPR := config.pr
	if readPR == nil {
		readPR = rehearsalPRSnapshot
	}
	head, headErr := run(ctx, config.RepoRoot, "git", "rev-parse", "HEAD")
	parent, parentErr := run(ctx, config.RepoRoot, "git", "show", "-s", "--format=%P", "HEAD")
	tree, treeErr := run(ctx, config.RepoRoot, "git", "rev-parse", "HEAD^{tree}")
	status, statusErr := run(ctx, config.RepoRoot, "git", "status", "--porcelain=v1", "--untracked-files=all")
	remote, remoteErr := run(ctx, config.RepoRoot, "git", "remote", "get-url", "origin")
	remoteLine, branchErr := run(ctx, config.RepoRoot, "git", "ls-remote", "--heads", "origin", "refs/heads/"+ExpectedRehearsalBranch)
	pr, prErr := readPR(ctx, config.RepoRoot)
	remoteCommit := firstField(remoteLine)
	checks := []RehearsalCheckV1{
		{ID: "candidate_commit", State: passedState(headErr == nil && validGitOID(head))},
		{ID: "candidate_sole_parent", State: passedState(parentErr == nil && parent == RequiredRehearsalParent && len(strings.Fields(parent)) == 1)},
		{ID: "candidate_tree", State: passedState(treeErr == nil && validGitOID(tree))},
		{ID: "clean_tree", State: passedState(statusErr == nil && status == "")},
		{ID: "draft_pr_head", State: passedState(prErr == nil && pr.Draft && pr.Head == head && pr.Number > 0 && pr.URL != "")},
		{ID: "exclusive_localhost_gsi_listener", State: passedState(rehearsalListenerAvailable(config.Listen, CaptureAddress))},
		{ID: "expected_remote", State: passedState(remoteErr == nil && remote == ExpectedRemoteURL)},
		{ID: "p4_acceptance", State: "not_applicable_rehearsal"},
		{ID: "professional_tournament_authority", State: "not_applicable_rehearsal"},
		{ID: "remote_branch_head", State: passedState(branchErr == nil && remoteCommit == head)},
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	sessionID := "dot84-" + firstN(head, 12)
	store, err := session.NewStore(filepath.Join(lease.abs, "data/sessions"), session.WithSessionID(sessionID))
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	if err = store.Close(); err != nil {
		return RehearsalReadinessV1{}, err
	}
	if _, err = writeLiveArtifacts(lease.abs, sessionID); err != nil {
		return RehearsalReadinessV1{}, err
	}
	if err = rootMkdirAll(filepath.Join(lease.abs, "application"), 0o700); err != nil {
		return RehearsalReadinessV1{}, err
	}
	build := runLogged(ctx, config.RepoRoot, filepath.Join(lease.abs, "evidence/logs/rehearsal-build.log"), "go", "build", "-trimpath", "-ldflags=-buildid=", "-o", filepath.Join(lease.abs, "application/dota2-ob"), "./cmd/dota2-ob")
	if build.err != nil {
		return RehearsalReadinessV1{}, build.err
	}
	owner := RehearsalCaptureOwnerV1{SchemaVersion: "public_match_rehearsal_capture_owner.v1", CandidateCommit: head, SessionID: sessionID, RawPath: "data/sessions/" + sessionID + "/raw.jsonl"}
	ownerPayload, _ := canonical(owner)
	if err = writePrivate(filepath.Join(lease.abs, "capture/owner.json"), ownerPayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	evidence := RehearsalEvidenceV1{SchemaVersion: "public_match_rehearsal_preflight.v3", Mode: "preflight", Identity: acceptedRehearsalIdentity(), CandidateCommit: head, CandidateParent: parent, CandidateTree: tree, RemoteURL: remote, RemoteBranch: ExpectedRehearsalBranch, RemoteCommit: remoteCommit, DraftPRNumber: pr.Number, DraftPRURL: pr.URL, DraftPRCommit: pr.Head, CaptureOwnerSHA256: payloadSHA(ownerPayload), Checks: checks, NonResumable: true}
	indexPayload, _ := canonical(evidence)
	if err = writePrivate(filepath.Join(lease.abs, "evidence/canonical/evidence-index.json"), indexPayload); err != nil {
		return RehearsalReadinessV1{}, err
	}
	failures := rehearsalCheckFailures(checks)
	readiness := RehearsalReadinessV1{SchemaVersion: "public_match_rehearsal_readiness.v3", Ready: len(failures) == 0, ConsoleState: rehearsalRefused, Identity: acceptedRehearsalIdentity(), CandidateCommit: head, EvidenceIndexSHA256: payloadSHA(indexPayload), CaptureOwnerSHA256: evidence.CaptureOwnerSHA256, Failures: failures}
	if readiness.Ready {
		readiness.ConsoleState = rehearsalReady
		readiness.HumanInstruction = RehearsalHumanInstruction
	}
	if err = writeJSON(filepath.Join(lease.abs, "evidence/readiness.json"), readiness, 0o600); err != nil {
		return RehearsalReadinessV1{}, err
	}
	return readiness, nil
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
	readinessPayload, err := readBoundedEvidence(filepath.Join(lease.abs, "evidence/readiness.json"), MaxRehearsalArtifactBytes)
	if err != nil {
		return RehearsalReadinessV1{}, err
	}
	var readiness RehearsalReadinessV1
	if err = strictRehearsalJSON(readinessPayload, &readiness); err != nil {
		return RehearsalReadinessV1{}, err
	}
	indexPayload, err := readBoundedEvidence(filepath.Join(lease.abs, "evidence/canonical/evidence-index.json"), MaxRehearsalArtifactBytes)
	if err != nil || payloadSHA(indexPayload) != readiness.EvidenceIndexSHA256 {
		return RehearsalReadinessV1{}, errors.New("preflight evidence hash mismatch")
	}
	var evidence RehearsalEvidenceV1
	if err = strictRehearsalJSON(indexPayload, &evidence); err != nil {
		return RehearsalReadinessV1{}, err
	}
	ownerPayload, err := readBoundedEvidence(filepath.Join(lease.abs, "capture/owner.json"), MaxRehearsalArtifactBytes)
	if err != nil || payloadSHA(ownerPayload) != evidence.CaptureOwnerSHA256 {
		return RehearsalReadinessV1{}, errors.New("capture owner mismatch")
	}
	var owner RehearsalCaptureOwnerV1
	if strictRehearsalJSON(ownerPayload, &owner) != nil || owner.CandidateCommit != evidence.CandidateCommit || owner.RawPath != "data/sessions/"+owner.SessionID+"/raw.jsonl" {
		return RehearsalReadinessV1{}, errors.New("capture owner contract mismatch")
	}
	failures := rehearsalCheckFailures(evidence.Checks)
	if evidence.SchemaVersion != "public_match_rehearsal_preflight.v3" || evidence.Mode != "preflight" || evidence.Identity != acceptedRehearsalIdentity() || !evidence.NonResumable || evidence.CandidateParent != RequiredRehearsalParent || len(strings.Fields(evidence.CandidateParent)) != 1 || readiness.Identity != evidence.Identity || readiness.CandidateCommit != evidence.CandidateCommit || readiness.CaptureOwnerSHA256 != evidence.CaptureOwnerSHA256 || !equalStringSlice(failures, readiness.Failures) || readiness.Ready != (len(failures) == 0) {
		return RehearsalReadinessV1{}, errors.New("preflight decision identity mismatch")
	}
	if readiness.Ready {
		if readiness.ConsoleState != rehearsalReady || readiness.HumanInstruction != RehearsalHumanInstruction {
			return RehearsalReadinessV1{}, errors.New("ready state contract mismatch")
		}
	} else if readiness.ConsoleState != rehearsalRefused || readiness.HumanInstruction != "" {
		return RehearsalReadinessV1{}, errors.New("refused state exposes activation")
	}
	sealedListener := checkState(evidence.Checks, "exclusive_localhost_gsi_listener") == "passed"
	// A passed listener check is volatile and must still pass. A canonically
	// failed check is immutable refusal evidence: it must remain verifiable and
	// cleanable even after the external listener becomes free.
	if sealedListener && !rehearsalListenerAvailable(listen, CaptureAddress) {
		return RehearsalReadinessV1{}, errors.New("volatile listener state differs from sealed preflight")
	}
	if readiness.Ready {
		if err = verifyCurrentRehearsalCandidate(ctx, repo, evidence); err != nil {
			return RehearsalReadinessV1{}, err
		}
	}
	if forbiddenRehearsalBytes(readinessPayload, indexPayload) {
		return RehearsalReadinessV1{}, errors.New("acceptance or identity capability leaked")
	}
	return readiness, nil
}

func rehearsalCheckFailures(checks []RehearsalCheckV1) []string {
	if len(checks) != len(rehearsalCheckContract) {
		return []string{"invalid_check_contract"}
	}
	failures := []string{}
	for i, check := range checks {
		expected := rehearsalCheckContract[i]
		if check.ID != expected.ID || (expected.State == "dynamic" && check.State != "passed" && check.State != "failed") || (expected.State != "dynamic" && check.State != expected.State) {
			return []string{"invalid_check_contract"}
		}
		if check.State == "failed" {
			failures = append(failures, check.ID)
		}
	}
	sort.Strings(failures)
	return failures
}
func verifyCurrentRehearsalCandidate(ctx context.Context, repo string, e RehearsalEvidenceV1) error {
	head, he := runText(ctx, repo, "git", "rev-parse", "HEAD")
	parent, pe := runText(ctx, repo, "git", "show", "-s", "--format=%P", "HEAD")
	tree, te := runText(ctx, repo, "git", "rev-parse", "HEAD^{tree}")
	status, se := runText(ctx, repo, "git", "status", "--porcelain=v1", "--untracked-files=all")
	remote, re := runText(ctx, repo, "git", "remote", "get-url", "origin")
	branch, be := runText(ctx, repo, "git", "ls-remote", "--heads", "origin", "refs/heads/"+ExpectedRehearsalBranch)
	pr, pre := rehearsalPRSnapshot(ctx, repo)
	if he != nil || pe != nil || te != nil || se != nil || re != nil || be != nil || pre != nil || head != e.CandidateCommit || parent != RequiredRehearsalParent || tree != e.CandidateTree || status != "" || remote != ExpectedRemoteURL || firstField(branch) != head || e.RemoteCommit != head || !pr.Draft || pr.Head != head || pr.Number != e.DraftPRNumber || pr.URL != e.DraftPRURL {
		return errors.New("current candidate differs from sealed ready preflight")
	}
	return nil
}
func rehearsalPRSnapshot(ctx context.Context, repo string) (rehearsalPR, error) {
	value, err := runText(ctx, repo, "gh", "pr", "view", ExpectedRehearsalBranch, "--json", "number,url,headRefOid,isDraft")
	if err != nil {
		return rehearsalPR{}, err
	}
	var pr rehearsalPR
	err = json.Unmarshal([]byte(value), &pr)
	return pr, err
}
func rehearsalListenerAvailable(open func(string) (io.Closer, error), address string) bool {
	if open == nil {
		open = func(a string) (io.Closer, error) { return net.Listen("tcp", a) }
	}
	listener, err := open(address)
	if err != nil {
		return false
	}
	return listener.Close() == nil
}
func readBoundedEvidence(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() < 0 || opened.Size() > limit {
		return nil, errors.New("bounded evidence rejected")
	}
	openedStat, openedOK := opened.Sys().(*syscall.Stat_t)
	pathInfo, pathErr := os.Lstat(path)
	if !openedOK || pathErr != nil {
		return nil, errors.New("bounded evidence path identity rejected")
	}
	pathStat, pathOK := pathInfo.Sys().(*syscall.Stat_t)
	if !pathOK || pathInfo.Mode()&os.ModeSymlink != 0 || openedStat.Dev != pathStat.Dev || openedStat.Ino != pathStat.Ino {
		return nil, errors.New("bounded evidence path identity rejected")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("bounded evidence limit exceeded")
	}
	closed, statErr := file.Stat()
	pathInfo, pathErr = os.Lstat(path)
	if statErr != nil || pathErr != nil {
		return nil, errors.New("evidence changed during bounded read")
	}
	closedStat, closedOK := closed.Sys().(*syscall.Stat_t)
	pathStat, pathOK = pathInfo.Sys().(*syscall.Stat_t)
	if !closedOK || !pathOK || pathInfo.Mode()&os.ModeSymlink != 0 ||
		closedStat.Dev != openedStat.Dev || closedStat.Ino != openedStat.Ino || pathStat.Dev != openedStat.Dev || pathStat.Ino != openedStat.Ino ||
		closed.Size() != int64(len(data)) || closed.Size() != opened.Size() {
		return nil, errors.New("evidence changed during bounded read")
	}
	return data, nil
}
func strictRehearsalJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON")
	}
	canonicalData, err := canonical(target)
	if err != nil || !bytes.Equal(data, canonicalData) {
		return errors.New("noncanonical JSON")
	}
	return nil
}
func forbiddenRehearsalBytes(values ...[]byte) bool {
	for _, value := range values {
		for _, forbidden := range []string{"ARMED", "DOT-70", "public_tournament", "MatchAuthorityRootV1", "steamid", "account_id", "persona_name", "display_name", "mention://"} {
			if bytes.Contains(value, []byte(forbidden)) {
				return true
			}
		}
	}
	return false
}
func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
func passedState(ok bool) string {
	if ok {
		return "passed"
	}
	return "failed"
}
func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
func firstN(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[:count]
}
func checkState(checks []RehearsalCheckV1, id string) string {
	for _, check := range checks {
		if check.ID == id {
			return check.State
		}
	}
	return ""
}
func equalStringSlice(a, b []string) bool {
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
