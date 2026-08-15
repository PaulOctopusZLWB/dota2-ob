package m4match

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"
)

func environmentIdentity(environment Environment) (string, error) {
	payload, err := canonical(environment)
	if err != nil {
		return "", err
	}
	return payloadSHA(payload), nil
}

func currentCandidateIdentity(ctx context.Context, repo, binary string, environment Environment) (CandidateIdentity, error) {
	var result CandidateIdentity
	head, err := runText(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil || len(head) != 40 {
		return result, errors.New("cannot resolve exact repository HEAD")
	}
	parents, err := runText(ctx, repo, "git", "rev-list", "--parents", "-n", "1", "HEAD")
	fields := strings.Fields(parents)
	if err != nil || len(fields) != 2 || fields[0] != head || fields[1] != RequiredSuccessorParent {
		return result, errors.New("candidate must have exactly the rejected candidate as its sole parent")
	}
	remote, err := runText(ctx, repo, "git", "remote", "get-url", "origin")
	if err != nil || normalizeRemote(remote) != normalizeRemote(ExpectedRemoteURL) {
		return result, errors.New("origin repository identity mismatch")
	}
	remoteBranch, err := remoteRef(ctx, repo, "refs/heads/"+ExpectedBranch)
	if err != nil || remoteBranch != head {
		return result, errors.New("remote branch does not equal candidate HEAD")
	}
	prHead, err := remoteRef(ctx, repo, "refs/pull/"+ExpectedPR+"/head")
	if err != nil || prHead != head {
		return result, errors.New("PR head does not equal candidate HEAD")
	}
	tree, err := runText(ctx, repo, "git", "rev-parse", "HEAD^{tree}")
	if err != nil || len(tree) != 40 {
		return result, errors.New("cannot resolve candidate tree")
	}
	binaryRevision, binaryModified, err := executableVCS(binary)
	if err != nil || binaryRevision != head || binaryModified {
		return result, errors.New("executing product binary VCS identity mismatch")
	}
	harnessRevision, harnessModified, err := runningVCS()
	if err != nil || harnessRevision != head || harnessModified {
		return result, errors.New("executing harness VCS identity mismatch")
	}
	binaryHash, _, err := fileSHA(binary)
	if err != nil {
		return result, err
	}
	environmentHash, err := environmentIdentity(environment)
	if err != nil {
		return result, err
	}
	result = CandidateIdentity{
		Commit: head, SoleParent: fields[1], RepositoryRootSHA: tree, RemoteURL: remote,
		RemoteBranchCommit: remoteBranch, PRHeadCommit: prHead, BinarySHA256: binaryHash,
		BinaryVCSRevision: binaryRevision, BinaryVCSModified: binaryModified,
		HarnessVCSRevision: harnessRevision, HarnessVCSModified: harnessModified,
		EnvironmentSHA256: environmentHash,
	}
	return result, nil
}

func remoteRef(ctx context.Context, repo, ref string) (string, error) {
	value, err := runText(ctx, repo, "git", "ls-remote", "origin", ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(value)
	if len(fields) != 2 || fields[1] != ref || len(fields[0]) != 40 {
		return "", fmt.Errorf("remote ref %s is absent or ambiguous", ref)
	}
	return fields[0], nil
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
