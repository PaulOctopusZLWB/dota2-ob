package m4match

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Verify(ctx context.Context, root, repo, expect string) (Readiness, error) {
	return VerifyClassification(ctx, root, repo, expect, RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassTI})
}

func VerifyClassification(ctx context.Context, root, repo, expect string, classification RunClassificationV1) (Readiness, error) {
	if err := classification.Validate(); err != nil {
		return Readiness{}, err
	}
	lease, err := acquireExistingRoot(root, repo)
	if err != nil {
		return Readiness{}, err
	}
	defer lease.Close()
	abs := lease.abs
	readinessPath := filepath.Join(abs, "evidence/readiness.json")
	payload, err := rootReadFile(readinessPath)
	if err != nil {
		return Readiness{}, err
	}
	readiness, rehearsalReadiness, err := decodeReadinessContract(payload, classification)
	if err != nil {
		return Readiness{}, err
	}
	if classification.Purpose == PurposePublicMatchRehearsal {
		if expect != "preflight" && expect != "rehearsal_complete" && expect != "rehearsal_failed" {
			return Readiness{}, errors.New("rehearsal verifier requires preflight or an exact rehearsal terminal outcome")
		}
		if expect != "preflight" && rehearsalReadiness.TerminalOutcome != expect {
			return Readiness{}, errors.New("rehearsal readiness terminal outcome mismatch")
		}
	}
	if readiness.SchemaVersion != classification.readinessSchema() || readiness.Mode != expect || readiness.HumanInstruction != classification.instruction() || readiness.RunPurpose != classification.Purpose || readiness.MatchClass != classification.Class || readiness.RootDomain != classification.rootDomain() || readiness.ConsoleState != classification.consoleState() || validateNonAcceptanceFields(readiness.RunPurpose, readiness.ClaimsP4, readiness.QualifyingMatch, readiness.AcceptanceEligible, readiness.AcceptanceGate) != nil {
		return Readiness{}, errors.New("readiness contract mismatch")
	}
	indexPath := filepath.Join(abs, "evidence/canonical/evidence-index.json")
	indexPayload, err := rootReadFile(indexPath)
	if err != nil {
		return Readiness{}, err
	}
	if payloadSHA(indexPayload) != readiness.EvidenceIndexSHA256 {
		return Readiness{}, errors.New("evidence index hash mismatch")
	}
	evidence, rehearsalEvidence, err := decodeEvidenceContract(indexPayload, classification)
	if err != nil {
		return Readiness{}, err
	}
	if evidence.SchemaVersion != classification.evidenceSchema() || evidence.Mode != expect || evidence.AcceptedBase != AcceptedFunctionalBase || evidence.AcceptedSpec != AcceptedP4Spec || !evidence.NonResumable || evidence.RunPurpose != classification.Purpose || evidence.MatchClass != classification.Class || evidence.RootDomain != classification.rootDomain() || evidence.AuthorityRootSHA256 != EmbeddedAuthorityRootSHA256 || validateNonAcceptanceFields(evidence.RunPurpose, evidence.ClaimsP4, evidence.QualifyingMatch, evidence.AcceptanceEligible, evidence.AcceptanceGate) != nil {
		return Readiness{}, errors.New("evidence identity or safety contract mismatch")
	}
	if classification.Purpose == PurposeP4Acceptance && (expect == "preflight" && evidence.TerminalOutcome != "" || expect == "live" && evidence.TerminalOutcome != "p4_independent_review_required") {
		return Readiness{}, errors.New("P4 terminal outcome contract mismatch")
	}
	if classification.Class == MatchClassPublicTournament && expect == "live" && !validSHA256(evidence.AuthorityPreflightSHA256) {
		return Readiness{}, errors.New("public tournament live evidence lacks bound authority preflight")
	}
	if classification.Class == MatchClassPublicTournament && expect == "live" {
		authorityReadiness := Readiness{CandidateCommit: evidence.CandidateCommit, EvidenceIndexSHA256: evidence.AuthorityReadinessIndexSHA256}
		authoritySHA, authorityErr := verifyAuthorityEvidenceAt(filepath.Join(abs, "evidence/authority-root"), authorityReadiness, evidence, evidence.AuthorityMatchID)
		if authorityErr != nil || authoritySHA != evidence.AuthorityPreflightSHA256 {
			return Readiness{}, errors.New("public tournament authority graph is not transitively valid at final verification")
		}
		selectionPayload, selectionErr := rootReadFile(filepath.Join(abs, "evidence/authority-root/evidence/canonical/match-authority-evidence.json"))
		var authorityEvidence MatchAuthorityEvidenceV1
		if selectionErr != nil || strictCanonicalJSON(selectionPayload, &authorityEvidence) != nil {
			return Readiness{}, errors.New("public tournament authority selection is unavailable")
		}
		canonicalSelection, _ := canonical(authorityEvidence.Selection)
		if payloadSHA(canonicalSelection) != evidence.AuthoritySelectionSHA256 {
			return Readiness{}, errors.New("public tournament authority selection continuity mismatch")
		}
	}
	if classification.Purpose == PurposePublicMatchRehearsal && expect != "preflight" {
		if evidence.TerminalOutcome != expect || rehearsalEvidence.FieldCoverage == nil {
			return Readiness{}, errors.New("rehearsal terminal evidence or coverage binding is absent")
		}
		if err := verifyRehearsalEvidence(abs, repo, evidence, rehearsalEvidence); err != nil {
			return Readiness{}, err
		}
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
	currentIdentity, currentIdentityEvidence, _, err := currentCandidateIdentityForClassification(ctx, repo, binary, currentEnvironment, classification)
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

func decodeReadinessContract(payload []byte, classification RunClassificationV1) (Readiness, PublicMatchRehearsalReadinessV1, error) {
	if classification.Purpose == PurposePublicMatchRehearsal {
		var contract PublicMatchRehearsalReadinessV1
		if err := strictCanonicalJSON(payload, &contract); err != nil || contract.RehearsalContractVersion != rehearsalContractV1 {
			return Readiness{}, contract, errors.New("public-match rehearsal readiness contract is invalid")
		}
		return contract.Readiness, contract, nil
	}
	var readiness Readiness
	if err := strictCanonicalJSON(payload, &readiness); err != nil {
		return Readiness{}, PublicMatchRehearsalReadinessV1{}, errors.New("P4 readiness contract is invalid")
	}
	return readiness, PublicMatchRehearsalReadinessV1{}, nil
}

func decodeEvidenceContract(payload []byte, classification RunClassificationV1) (Evidence, PublicMatchRehearsalEvidenceV1, error) {
	if classification.Purpose == PurposePublicMatchRehearsal {
		var contract PublicMatchRehearsalEvidenceV1
		if err := strictCanonicalJSON(payload, &contract); err != nil || contract.RehearsalContractVersion != rehearsalContractV1 || len(contract.AcceptanceChecks) == 0 || len(contract.SuppressionAudits) == 0 {
			return Evidence{}, contract, errors.New("public-match rehearsal evidence contract is invalid")
		}
		return contract.Evidence, contract, nil
	}
	var evidence Evidence
	if err := strictCanonicalJSON(payload, &evidence); err != nil {
		return Evidence{}, PublicMatchRehearsalEvidenceV1{}, errors.New("P4 evidence contract is invalid")
	}
	return evidence, PublicMatchRehearsalEvidenceV1{}, nil
}

func verifyRehearsalEvidence(root, repo string, evidence Evidence, contract PublicMatchRehearsalEvidenceV1) error {
	if evidence.AuthorityPreflightSHA256 != "" || evidence.AuthorityRootSHA256 != EmbeddedAuthorityRootSHA256 || evidence.QualifyingMatch || evidence.AcceptanceEligible || evidence.AcceptanceGate != "none" || evidence.ClaimsP4 || evidence.TerminalOutcome != "rehearsal_complete" && evidence.TerminalOutcome != "rehearsal_failed" {
		return errors.New("rehearsal evidence contains an acceptance or authority capability")
	}
	expectedChecks, _ := canonical(rehearsalAcceptanceChecks())
	actualChecks, _ := canonical(contract.AcceptanceChecks)
	expectedSuppressions, _ := canonical(rehearsalSuppressionAudits())
	actualSuppressions, _ := canonical(contract.SuppressionAudits)
	if !bytes.Equal(expectedChecks, actualChecks) || !bytes.Equal(expectedSuppressions, actualSuppressions) {
		return errors.New("rehearsal typed N/A or suppression audit set mismatch")
	}
	contractPayload, _ := canonical(contract)
	for _, forbidden := range []string{"DOT-64", "DOT-62", "DOT-70", "mention://", "status_transition", "ARMED"} {
		if bytes.Contains(contractPayload, []byte(forbidden)) {
			return errors.New("rehearsal evidence contains a forbidden acceptance side-effect reference")
		}
	}
	binding := contract.FieldCoverage
	if binding.ArtifactPath != "evidence/canonical/field-coverage-delta.json" || !validSHA256(binding.SHA256) || !validSHA256(binding.RootSHA256) {
		return errors.New("rehearsal coverage binding is invalid")
	}
	deltaPayload, err := rootReadFile(filepath.Join(root, filepath.FromSlash(binding.ArtifactPath)))
	if err != nil || payloadSHA(deltaPayload) != binding.SHA256 {
		return errors.New("rehearsal coverage artifact hash mismatch")
	}
	var delta FieldCoverageDeltaV1
	if err := strictCanonicalJSON(deltaPayload, &delta); err != nil || ValidateFieldCoverageDelta(delta) != nil || delta.EvidenceRootSHA256 != binding.RootSHA256 {
		return errors.New("rehearsal coverage artifact is invalid")
	}
	baseline, baselineSHA, err := loadBaselineCoverageFrames(filepath.Join(repo, "internal/integration/m4/testdata/captured_gsi_schedule.json"))
	if err != nil || baselineSHA != delta.BaselineRawSessionSHA256 {
		return errors.New("rehearsal coverage baseline identity mismatch")
	}
	var rawPath string
	for _, artifact := range evidence.Artifacts {
		if strings.HasPrefix(artifact.Path, "data/sessions/") && strings.HasSuffix(artifact.Path, "/raw.jsonl") {
			if rawPath != "" {
				return errors.New("rehearsal evidence contains multiple raw sessions")
			}
			rawPath = filepath.Join(root, filepath.FromSlash(artifact.Path))
		}
	}
	if rawPath == "" {
		return errors.New("rehearsal raw session is absent")
	}
	rehearsal, rehearsalSHA, err := loadRehearsalCoverageFrames(rawPath)
	if err != nil || rehearsalSHA != delta.RehearsalRawSessionSHA256 {
		return errors.New("rehearsal coverage raw-session identity mismatch")
	}
	rootPayload, _ := canonical(struct {
		Domain, CandidateCommit, BaselineSHA256, RehearsalSHA256, Algorithm string
	}{rehearsalRootDomain, evidence.CandidateCommit, baselineSHA, rehearsalSHA, CoverageNormalizationVersion})
	if payloadSHA(rootPayload) != binding.RootSHA256 {
		return errors.New("rehearsal coverage root binding mismatch")
	}
	rebuilt, err := GenerateFieldCoverageDelta(baseline, rehearsal, baselineSHA, rehearsalSHA, binding.RootSHA256)
	if err != nil {
		return err
	}
	rebuiltPayload, _ := canonical(rebuilt)
	if !bytes.Equal(rebuiltPayload, deltaPayload) {
		return errors.New("rehearsal coverage does not reproduce from exact raw frames")
	}
	return nil
}

func validateCandidateIdentityEvidence(observed CandidateIdentityEvidence, identity CandidateIdentity) error {
	if observed.AcceptedAmendment != AcceptedP4Spec || observed.AuthorityRootSHA256 != EmbeddedAuthorityRootSHA256 || observed.RunPurpose == "" || observed.MatchClass == "" {
		return errors.New("candidate identity does not bind amendment, authority root, purpose, and class")
	}
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
		RunPurpose: observed.RunPurpose, MatchClass: observed.MatchClass, AcceptedAmendment: observed.AcceptedAmendment, AuthorityRootSHA256: observed.AuthorityRootSHA256,
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
	if evidence.CandidateIdentity.RunPurpose != readiness.RunPurpose || evidence.CandidateIdentity.MatchClass != readiness.MatchClass || evidence.CandidateIdentity.AcceptedAmendment != AcceptedP4Spec || evidence.CandidateIdentity.AuthorityRootSHA256 != EmbeddedAuthorityRootSHA256 {
		return errors.New("candidate purpose/amendment/authority identity binding mismatch")
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
	return CleanupClassification(root, repo, confirmation, RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassTI})
}

func CleanupClassification(root, repo, confirmation string, classification RunClassificationV1) error {
	abs, err := safeRoot(root, repo)
	if err != nil {
		return err
	}
	readiness, err := VerifyClassification(context.Background(), abs, repo, "preflight", classification)
	if err != nil {
		if classification.Purpose == PurposePublicMatchRehearsal {
			readiness, err = VerifyClassification(context.Background(), abs, repo, "rehearsal_complete", classification)
			if err != nil {
				readiness, err = VerifyClassification(context.Background(), abs, repo, "rehearsal_failed", classification)
			}
		} else {
			readiness, err = VerifyClassification(context.Background(), abs, repo, "live", classification)
		}
	}
	if err != nil {
		return fmt.Errorf("refusing cleanup of unverifiable root: %w", err)
	}
	if confirmation == "" || confirmation != readiness.EvidenceIndexSHA256 {
		return errors.New("cleanup confirmation must equal the evidence index SHA-256")
	}
	lease, err := acquireExistingRoot(abs, repo)
	if err != nil {
		return err
	}
	defer lease.Close()
	return lease.removeAll()
}
