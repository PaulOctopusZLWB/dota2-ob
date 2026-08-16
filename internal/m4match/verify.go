package m4match

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func Verify(ctx context.Context, root, repo, expect string) (Readiness, error) {
	abs, err := safeRoot(root, repo)
	if err != nil {
		return Readiness{}, err
	}
	readinessPath := filepath.Join(abs, "evidence/readiness.json")
	payload, err := os.ReadFile(readinessPath)
	if err != nil {
		return Readiness{}, err
	}
	var readiness Readiness
	if err := json.Unmarshal(payload, &readiness); err != nil {
		return Readiness{}, err
	}
	canonicalReadiness, err := canonical(readiness)
	if err != nil || !bytes.Equal(payload, canonicalReadiness) {
		return Readiness{}, errors.New("readiness result is not canonical")
	}
	if readiness.SchemaVersion != ReadinessSchemaVersion || readiness.Mode != expect || readiness.ClaimsP4 || readiness.HumanInstruction != HumanInstruction {
		return Readiness{}, errors.New("readiness contract mismatch")
	}
	indexPath := filepath.Join(abs, "evidence/canonical/evidence-index.json")
	indexPayload, err := os.ReadFile(indexPath)
	if err != nil {
		return Readiness{}, err
	}
	if payloadSHA(indexPayload) != readiness.EvidenceIndexSHA256 {
		return Readiness{}, errors.New("evidence index hash mismatch")
	}
	var evidence Evidence
	if err := json.Unmarshal(indexPayload, &evidence); err != nil {
		return Readiness{}, err
	}
	canonicalIndex, err := canonical(evidence)
	if err != nil || !bytes.Equal(indexPayload, canonicalIndex) {
		return Readiness{}, errors.New("evidence index is not canonical")
	}
	if evidence.SchemaVersion != SchemaVersion || evidence.Mode != expect || evidence.AcceptedBase != AcceptedFunctionalBase || evidence.AcceptedSpec != AcceptedP4Spec || evidence.ClaimsP4 || !evidence.NonResumable {
		return Readiness{}, errors.New("evidence identity or safety contract mismatch")
	}
	identitySHA, err := candidateIdentitySHA(evidence.CandidateIdentity)
	if err != nil {
		return Readiness{}, err
	}
	if evidence.FixtureSHA256 != CapturedScheduleSHA256 || evidence.GoldenSHA256 != ProductionGoldenSHA256 {
		return Readiness{}, errors.New("accepted synthetic evidence identity mismatch")
	}
	for _, artifact := range evidence.Artifacts {
		clean := filepath.Clean(filepath.FromSlash(artifact.Path))
		if artifact.Path == "" || filepath.IsAbs(clean) || clean == ".." || len(clean) >= 3 && clean[:3] == ".."+string(os.PathSeparator) {
			return Readiness{}, errors.New("prepared artifact path escapes evidence root")
		}
		path := filepath.Join(abs, clean)
		hash, size, err := fileSHA(path)
		if err != nil || hash != artifact.SHA256 || size != artifact.Bytes {
			return Readiness{}, fmt.Errorf("prepared artifact mismatch: %s", artifact.Path)
		}
	}
	binary := filepath.Join(abs, "application/dota2-ob")
	currentEnvironment, environmentChecks := inspectEnvironment(ctx)
	for _, check := range environmentChecks {
		if !check.Passed {
			return Readiness{}, fmt.Errorf("current environment capture failed: %s", check.ID)
		}
	}
	if currentEnvironment != evidence.Environment {
		return Readiness{}, errors.New("current environment does not equal captured environment")
	}
	currentIdentity, currentIdentityEvidence, _, err := currentCandidateIdentity(ctx, repo, binary, currentEnvironment)
	if err != nil {
		return Readiness{}, fmt.Errorf("current candidate identity: %w", err)
	}
	if err := validateCandidateIdentityEvidence(currentIdentityEvidence, currentIdentity); err != nil {
		return Readiness{}, fmt.Errorf("current candidate identity evidence: %w", err)
	}
	if err := validateCandidateIdentityEvidence(evidence.CandidateIdentityEvidence, evidence.CandidateIdentity); err != nil {
		return Readiness{}, fmt.Errorf("captured candidate identity evidence: %w", err)
	}
	currentIdentitySHA, _ := candidateIdentitySHA(currentIdentity)
	if err := validateIdentityBindings(readiness, evidence, currentIdentity); err != nil || currentIdentitySHA != identitySHA {
		return Readiness{}, errors.New("current repository, remote, PR, executable, or environment identity changed")
	}
	if err := validateTransitiveEvidence(evidence, expect); err != nil {
		return Readiness{}, err
	}
	failures := make([]string, 0)
	for _, check := range evidence.Checks {
		if !check.Passed {
			failures = append(failures, check.ID)
		}
	}
	if len(failures) != len(readiness.Failures) {
		return Readiness{}, errors.New("readiness failure set mismatch")
	}
	for index := range failures {
		if failures[index] != readiness.Failures[index] {
			return Readiness{}, errors.New("readiness failure order mismatch")
		}
	}
	if readiness.Ready != (len(failures) == 0) {
		return Readiness{}, errors.New("readiness decision mismatch")
	}
	if expect == "preflight" && !evidence.SyntheticOnly {
		return Readiness{}, errors.New("preflight incorrectly represented as live evidence")
	}
	return readiness, nil
}

func validateCandidateIdentityEvidence(observed CandidateIdentityEvidence, identity CandidateIdentity) error {
	if len(observed.Checks) == 0 {
		return errors.New("named candidate identity sub-checks are absent")
	}
	seen := make(map[string]bool, len(observed.Checks))
	for _, check := range observed.Checks {
		if check.ID == "" || seen[check.ID] || !check.Passed || check.ReasonCode != "ok" {
			return fmt.Errorf("invalid candidate identity sub-check: %s/%s", check.ID, check.ReasonCode)
		}
		seen[check.ID] = true
	}
	for _, required := range []string{
		"local_head_start", "sole_parent_start", "repository_tree_start", "origin_start", "remote_branch_start", "pr_head_start",
		"local_head_end", "sole_parent_end", "repository_tree_end", "origin_end", "remote_branch_end", "pr_head_end",
		"product_vcs", "product_hash", "harness_vcs", "environment_hash", "repository_remote_stability",
	} {
		if !seen[required] {
			return fmt.Errorf("candidate identity sub-check absent: %s", required)
		}
	}
	if observed.Start != observed.End {
		return errors.New("candidate repository start/end evidence differs")
	}
	expected := CandidateIdentity{
		Commit: observed.End.Commit, SoleParent: observed.End.SoleParent, RepositoryRootSHA: observed.End.RepositoryRootSHA,
		RemoteURL: observed.End.RemoteURL, RemoteBranchCommit: observed.End.RemoteBranchCommit, PRHeadCommit: observed.End.PRHeadCommit,
		BinarySHA256: observed.BinarySHA256, BinaryVCSRevision: observed.BinaryVCSRevision, BinaryVCSModified: observed.BinaryVCSModified,
		HarnessVCSRevision: observed.HarnessVCSRevision, HarnessVCSModified: observed.HarnessVCSModified, EnvironmentSHA256: observed.EnvironmentSHA256,
	}
	if expected != identity {
		return errors.New("named candidate identity observations do not bind the composite identity")
	}
	return nil
}

func validateIdentityBindings(readiness Readiness, evidence Evidence, current CandidateIdentity) error {
	if evidence.CandidateCommit == "" || readiness.CandidateCommit != evidence.CandidateCommit || evidence.CandidateParent != RequiredSuccessorParent {
		return errors.New("candidate commit or sole-parent identity mismatch")
	}
	identitySHA, err := candidateIdentitySHA(evidence.CandidateIdentity)
	if err != nil || identitySHA != readiness.CandidateIdentitySHA256 || evidence.CandidateIdentity.Commit != evidence.CandidateCommit || evidence.CandidateIdentity.SoleParent != RequiredSuccessorParent || evidence.CandidateIdentity.EnvironmentSHA256 != readiness.EnvironmentSHA256 {
		return errors.New("candidate identity binding mismatch")
	}
	currentSHA, err := candidateIdentitySHA(current)
	if err != nil || currentSHA != identitySHA {
		return errors.New("current candidate identity mismatch")
	}
	return nil
}

func validateTransitiveEvidence(evidence Evidence, expect string) error {
	artifactPaths := make(map[string]bool, len(evidence.Artifacts))
	for _, artifact := range evidence.Artifacts {
		artifactPaths[artifact.Path] = true
	}
	if expect == "preflight" {
		for _, required := range []string{
			"evidence/canonical/fault-proof-manifest.json",
			"evidence/logs/focused_twice.log", "evidence/logs/m4_fault_matrix.log", "evidence/logs/full_go.log",
			"evidence/logs/full_race.log", "evidence/logs/vet.log", "evidence/logs/build_all.log",
			"evidence/logs/module_verify.log", "evidence/logs/browser_tests.log", "evidence/logs/obs_overlay_tests.log",
			"evidence/logs/product-probe.log", "evidence/logs/product-sigkill-restart.log",
		} {
			if !artifactPaths[required] {
				return fmt.Errorf("required transitive log absent: %s", required)
			}
		}
		checks := make(map[string]bool, len(evidence.Checks))
		for _, check := range evidence.Checks {
			checks[check.ID] = check.Passed
		}
		for _, required := range []string{"accepted_ancestry", "candidate_commit", "candidate_parent", "candidate_identity", "candidate_binary_hash", "captured_schedule", "production_golden", "clean_tree", "clean_tree_final", "isolated_process_state", "focused_twice", "m4_fault_matrix", "full_go", "full_race", "vet", "build_all", "module_verify", "browser_install", "browser_tests", "obs_overlay_install", "obs_overlay_tests", "production_endpoints", "product_sigkill_restart", "privacy_and_source_boundary", "secret_generated_scan", "dependency_boundary", "diff_check"} {
			if !checks[required] {
				return fmt.Errorf("required passing readiness check absent: %s", required)
			}
		}
	}
	faults := make(map[string]bool, len(evidence.Faults))
	for _, fault := range evidence.Faults {
		faults[fault] = true
	}
	for _, fault := range RequiredFaults {
		if !faults[fault] {
			return fmt.Errorf("required fault absent: %s", fault)
		}
		if expect == "preflight" {
			found := false
			for _, check := range evidence.Checks {
				if check.ID == "fault_"+fault && check.Passed {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("required passing fault check absent: %s", fault)
			}
		}
	}
	return nil
}

func Cleanup(root, repo, confirmation string) error {
	abs, err := safeRoot(root, repo)
	if err != nil {
		return err
	}
	readiness, err := Verify(context.Background(), abs, repo, "preflight")
	if err != nil {
		readiness, err = Verify(context.Background(), abs, repo, "live")
	}
	if err != nil {
		return fmt.Errorf("refusing cleanup of unverifiable root: %w", err)
	}
	if confirmation == "" || confirmation != readiness.EvidenceIndexSHA256 {
		return errors.New("cleanup confirmation must equal the evidence index SHA-256")
	}
	checkedAgain, err := safeRoot(abs, repo)
	if err != nil || checkedAgain != abs {
		return errors.New("cleanup root changed after verification")
	}
	return os.RemoveAll(abs)
}
