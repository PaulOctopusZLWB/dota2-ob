package m4match

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

const (
	remoteSnapshotTimeout = 10 * time.Second
	remoteSnapshotRetries = 2
)

type identityCommand func(context.Context, string, string, ...string) (string, error)

type identityCollector struct {
	run        identityCommand
	executable func(string) (string, bool, error)
	harness    func() (string, bool, error)
	delay      func(context.Context) error
}

type repositoryCapture struct {
	snapshot    CandidateRepositorySnapshot
	checks      []CandidateIdentitySubcheck
	diagnostics []string
}

func defaultIdentityCollector() identityCollector {
	return identityCollector{
		run:        runText,
		executable: executableVCS,
		harness:    runningVCS,
		delay: func(ctx context.Context) error {
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

func environmentIdentity(environment Environment) (string, error) {
	payload, err := canonical(environment)
	if err != nil {
		return "", err
	}
	return payloadSHA(payload), nil
}

func currentCandidateIdentity(ctx context.Context, repo, binary string, environment Environment) (CandidateIdentity, CandidateIdentityEvidence, []string, error) {
	return currentCandidateIdentityForClassification(ctx, repo, binary, environment, RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassTI})
}

func currentCandidateIdentityForClassification(ctx context.Context, repo, binary string, environment Environment, classification RunClassificationV1) (CandidateIdentity, CandidateIdentityEvidence, []string, error) {
	collector := defaultIdentityCollector()
	start := collector.captureRepository(ctx, repo, "start")
	return collector.completeForClassification(ctx, repo, binary, environment, start, classification)
}

func (collector identityCollector) captureRepository(ctx context.Context, repo, phase string) repositoryCapture {
	result := repositoryCapture{}
	check := func(name string, passed bool, reason string) {
		if passed {
			reason = "ok"
		}
		result.checks = append(result.checks, CandidateIdentitySubcheck{ID: name + "_" + phase, Passed: passed, ReasonCode: reason})
	}

	head, err := collector.run(ctx, repo, "git", "rev-parse", "HEAD")
	headOK := err == nil && validGitOID(head)
	if headOK {
		result.snapshot.Commit = head
	}
	check("local_head", headOK, reasonForCommand(err, "invalid_object_id"))

	parents, parentErr := collector.run(ctx, repo, "git", "rev-list", "--parents", "-n", "1", "HEAD")
	fields := strings.Fields(parents)
	parentOK := parentErr == nil && headOK && len(fields) == 2 && fields[0] == head && fields[1] == RequiredSuccessorParent
	if parentErr == nil && len(fields) == 2 && validGitOID(fields[1]) {
		result.snapshot.SoleParent = fields[1]
	}
	check("sole_parent", parentOK, reasonForCommand(parentErr, "sole_parent_mismatch"))

	tree, treeErr := collector.run(ctx, repo, "git", "rev-parse", "HEAD^{tree}")
	treeOK := treeErr == nil && validGitOID(tree)
	if treeOK {
		result.snapshot.RepositoryRootSHA = tree
	}
	check("repository_tree", treeOK, reasonForCommand(treeErr, "invalid_object_id"))

	remote, remoteErr := collector.run(ctx, repo, "git", "remote", "get-url", "origin")
	remoteOK := remoteErr == nil && normalizeRemote(remote) == normalizeRemote(ExpectedRemoteURL)
	if remoteErr == nil {
		result.snapshot.RemoteURL = nonSecretRemote(remote)
	}
	check("origin", remoteOK, reasonForCommand(remoteErr, "origin_mismatch"))

	branch, pr, remoteReason, diagnostics := collector.remoteSnapshot(ctx, repo)
	result.diagnostics = append(result.diagnostics, diagnostics...)
	result.snapshot.RemoteBranchCommit = branch
	result.snapshot.PRHeadCommit = pr
	branchReason, prReason := splitRemoteReason(remoteReason)
	branchOK := remoteReason == "ok" && headOK && branch == head
	prOK := remoteReason == "ok" && headOK && pr == head
	if remoteReason == "ok" && headOK && branch != head {
		branchReason = "remote_branch_mismatch"
	}
	if remoteReason == "ok" && headOK && pr != head {
		prReason = "pr_head_mismatch"
	}
	check("remote_branch", branchOK, branchReason)
	check("pr_head", prOK, prReason)
	return result
}

func (collector identityCollector) remoteSnapshot(ctx context.Context, repo string) (string, string, string, []string) {
	branchRef := "refs/heads/" + ExpectedBranch
	prRef := "refs/pull/" + ExpectedPR + "/head"
	diagnostics := make([]string, 0, remoteSnapshotRetries)
	for attempt := 0; attempt < remoteSnapshotRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, remoteSnapshotTimeout)
		value, err := collector.run(attemptCtx, repo, "git", "ls-remote", "origin", branchRef, prRef)
		cancel()
		if err != nil {
			diagnostics = append(diagnostics, sanitizeIdentityDiagnostic(value+"\n"+err.Error()))
			if attempt+1 < remoteSnapshotRetries && collector.delay(ctx) == nil {
				continue
			}
			return "", "", "remote_transport_unavailable", diagnostics
		}
		branch, pr, reason := parseRemoteSnapshot(value, branchRef, prRef)
		return branch, pr, reason, diagnostics
	}
	return "", "", "remote_transport_unavailable", diagnostics
}

func parseRemoteSnapshot(value, branchRef, prRef string) (string, string, string) {
	values := map[string][]string{branchRef: {}, prRef: {}}
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !validGitOID(fields[0]) {
			return "", "", "remote_malformed"
		}
		if _, expected := values[fields[1]]; !expected {
			return "", "", "remote_unexpected_ref"
		}
		values[fields[1]] = append(values[fields[1]], fields[0])
	}
	if len(values[branchRef]) == 0 {
		return "", first(values[prRef]), "remote_branch_missing"
	}
	if len(values[branchRef]) != 1 {
		return "", first(values[prRef]), "remote_branch_ambiguous"
	}
	if len(values[prRef]) == 0 {
		return values[branchRef][0], "", "pr_head_missing"
	}
	if len(values[prRef]) != 1 {
		return values[branchRef][0], "", "pr_head_ambiguous"
	}
	return values[branchRef][0], values[prRef][0], "ok"
}

func splitRemoteReason(reason string) (string, string) {
	switch reason {
	case "ok":
		return "ok", "ok"
	case "remote_branch_missing", "remote_branch_ambiguous":
		return reason, "remote_snapshot_invalid"
	case "pr_head_missing", "pr_head_ambiguous":
		return "remote_snapshot_invalid", reason
	default:
		return reason, reason
	}
}

func (collector identityCollector) complete(ctx context.Context, repo, binary string, environment Environment, start repositoryCapture) (CandidateIdentity, CandidateIdentityEvidence, []string, error) {
	return collector.completeForClassification(ctx, repo, binary, environment, start, RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassTI})
}

func (collector identityCollector) completeForClassification(ctx context.Context, repo, binary string, environment Environment, start repositoryCapture, classification RunClassificationV1) (CandidateIdentity, CandidateIdentityEvidence, []string, error) {
	if err := classification.Validate(); err != nil {
		return CandidateIdentity{}, CandidateIdentityEvidence{}, nil, err
	}
	end := collector.captureRepository(ctx, repo, "end")
	evidence := CandidateIdentityEvidence{Start: start.snapshot, End: end.snapshot, RunPurpose: classification.Purpose, MatchClass: classification.Class, AcceptedAmendment: AcceptedP4Spec, AuthorityRootSHA256: EmbeddedAuthorityRootSHA256}
	evidence.Checks = append(evidence.Checks, start.checks...)
	evidence.Checks = append(evidence.Checks, end.checks...)
	diagnostics := append(append([]string{}, start.diagnostics...), end.diagnostics...)
	add := func(id string, passed bool, reason string) {
		if passed {
			reason = "ok"
		}
		evidence.Checks = append(evidence.Checks, CandidateIdentitySubcheck{ID: id, Passed: passed, ReasonCode: reason})
	}

	binaryRevision, binaryModified, binaryVCSErr := collector.executable(binary)
	evidence.BinaryVCSRevision, evidence.BinaryVCSModified = binaryRevision, binaryModified
	add("product_vcs", binaryVCSErr == nil && binaryRevision == end.snapshot.Commit && !binaryModified, vcsReason(binaryVCSErr, binaryRevision, end.snapshot.Commit, binaryModified, "product"))
	binaryHash, _, binaryHashErr := fileSHA(binary)
	if binaryHashErr == nil {
		evidence.BinarySHA256 = binaryHash
	}
	add("product_hash", binaryHashErr == nil && binaryHash != "", reasonForCommand(binaryHashErr, "product_hash_missing"))

	harnessRevision, harnessModified, harnessErr := collector.harness()
	evidence.HarnessVCSRevision, evidence.HarnessVCSModified = harnessRevision, harnessModified
	add("harness_vcs", harnessErr == nil && harnessRevision == end.snapshot.Commit && !harnessModified, vcsReason(harnessErr, harnessRevision, end.snapshot.Commit, harnessModified, "harness"))

	environmentHash, environmentErr := environmentIdentity(environment)
	if environmentErr == nil {
		evidence.EnvironmentSHA256 = environmentHash
	}
	add("environment_hash", environmentErr == nil && environmentHash != "", reasonForCommand(environmentErr, "environment_hash_missing"))

	stable := start.snapshot == end.snapshot
	add("repository_remote_stability", stable, "start_end_identity_changed")
	sort.Slice(evidence.Checks, func(i, j int) bool { return evidence.Checks[i].ID < evidence.Checks[j].ID })
	for _, check := range evidence.Checks {
		if !check.Passed {
			return CandidateIdentity{}, evidence, diagnostics, fmt.Errorf("candidate identity sub-check %s: %s", check.ID, check.ReasonCode)
		}
	}
	identity := CandidateIdentity{
		Commit: end.snapshot.Commit, SoleParent: end.snapshot.SoleParent, RepositoryRootSHA: end.snapshot.RepositoryRootSHA,
		RemoteURL: end.snapshot.RemoteURL, RemoteBranchCommit: end.snapshot.RemoteBranchCommit, PRHeadCommit: end.snapshot.PRHeadCommit,
		BinarySHA256: evidence.BinarySHA256, BinaryVCSRevision: binaryRevision, BinaryVCSModified: binaryModified,
		HarnessVCSRevision: harnessRevision, HarnessVCSModified: harnessModified, EnvironmentSHA256: environmentHash,
		RunPurpose: classification.Purpose, MatchClass: classification.Class, AcceptedAmendment: AcceptedP4Spec, AuthorityRootSHA256: EmbeddedAuthorityRootSHA256,
	}
	return identity, evidence, diagnostics, nil
}

func reasonForCommand(err error, invalid string) string {
	if err != nil {
		return "command_failed"
	}
	return invalid
}

func vcsReason(err error, observed, expected string, modified bool, prefix string) string {
	if err != nil {
		return prefix + "_vcs_unavailable"
	}
	if observed != expected {
		return prefix + "_vcs_revision_mismatch"
	}
	if modified {
		return prefix + "_vcs_modified"
	}
	return "ok"
}

func validGitOID(value string) bool {
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

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

var diagnosticURLPattern = regexp.MustCompile(`(?i)(?:https?|ssh)://[^\s]+`)
var diagnosticSecretPattern = regexp.MustCompile(`(?i)(token|password|authorization|cookie|api[_-]?key)\s*[:=]\s*[^\s]+`)

func sanitizeIdentityDiagnostic(value string) string {
	value = diagnosticURLPattern.ReplaceAllString(value, "<REDACTED_URL>")
	value = diagnosticSecretPattern.ReplaceAllString(value, `${1}=<REDACTED>`)
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > 8 {
		lines = lines[:8]
	}
	result := strings.Join(lines, "\n")
	if len(result) > 2048 {
		result = result[:2048]
	}
	return result
}

func nonSecretRemote(value string) string {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.User != nil {
		parsed.User = nil
		return parsed.String()
	}
	return trimmed
}

func normalizeRemote(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), ".git")
}

func executableVCS(binary string) (string, bool, error) {
	output, err := exec.Command("go", "version", "-m", binary).CombinedOutput()
	if err != nil {
		return "", false, err
	}
	return parseVCS(string(output))
}

func parseVCS(value string) (string, bool, error) {
	var revision string
	modified := true
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "build" && strings.HasPrefix(fields[1], "vcs.revision=") {
			revision = strings.TrimPrefix(fields[1], "vcs.revision=")
		}
		if len(fields) == 2 && fields[0] == "build" && strings.HasPrefix(fields[1], "vcs.modified=") {
			modified = strings.TrimPrefix(fields[1], "vcs.modified=") != "false"
		}
	}
	if revision == "" {
		return "", false, errors.New("Go VCS revision is absent")
	}
	return revision, modified, nil
}

func runningVCS() (string, bool, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, errors.New("running Go build info is absent")
	}
	var revision string
	modified := true
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value != "false"
		}
	}
	if revision == "" {
		return "", false, errors.New("running harness VCS revision is absent")
	}
	return revision, modified, nil
}

func candidateIdentitySHA(identity CandidateIdentity) (string, error) {
	payload, err := canonical(identity)
	if err != nil {
		return "", err
	}
	return payloadSHA(payload), nil
}
