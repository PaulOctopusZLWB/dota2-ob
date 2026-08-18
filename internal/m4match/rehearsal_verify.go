package m4match

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func VerifyRehearsal(ctx context.Context, dataRoot, repoRoot string) (RehearsalAttemptResultV1, error) {
	repo, err := filepath.Abs(repoRoot)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	lease, err := acquireExistingRoot(dataRoot, repo)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	defer lease.Close()
	preflight, err := readRehearsalPreflight(lease.abs)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	terminal, err := readRehearsalTerminal(lease.abs)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if terminal.PreflightSHA256 != preflight.PreflightSHA256 || terminal.CandidateCommit != preflight.CandidateCommit || terminal.CandidateParent != preflight.CandidateParent || terminal.SessionID != preflight.SessionID {
		return RehearsalAttemptResultV1{}, errors.New("terminal/preflight identity mismatch")
	}
	if err := validateRehearsalTerminal(terminal); err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if preflight.ConsoleState == RehearsalReady {
		listener, listenErr := net.Listen("tcp", CaptureAddress)
		if listenErr != nil {
			return RehearsalAttemptResultV1{}, errors.New("fresh exclusive listener verification failed")
		}
		_ = listener.Close()
	}
	if head, headErr := runText(ctx, repo, "git", "rev-parse", "HEAD"); headErr != nil || head != terminal.CandidateCommit {
		return RehearsalAttemptResultV1{}, errors.New("verifier candidate head mismatch")
	}
	if shouldRegenerateRetainedInput(terminal) {
		rawPath := filepath.Join(lease.abs, "data", "sessions", terminal.SessionID, "raw.jsonl")
		attested, readErr := session.ReadAttestedRawV1(rawPath, terminal.SessionID, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, MaxRehearsalRawRecords)
		manifestSHA, _ := contracts.CanonicalSHA256(attested.Records)
		if manifestSHA != terminal.RawManifestSHA256 || attested.Receipt.BytesRead != terminal.RawAdmission.BytesRead || attested.Receipt.ReadPrefixSHA256 != terminal.RawAdmission.ReadPrefixSHA256 || attested.Receipt.PreDescriptor.Device != terminal.RawAdmission.PreDescriptor.Device || attested.Receipt.PreDescriptor.Inode != terminal.RawAdmission.PreDescriptor.Inode {
			return RehearsalAttemptResultV1{}, errors.New("retained raw regeneration mismatch")
		}
		if terminal.RawAdmission.Failure.Primary == "" && readErr != nil {
			return RehearsalAttemptResultV1{}, errors.New("retained raw no longer admits")
		}
		suppression, suppressionErr := deriveRehearsalSuppression(terminal.SessionID, attested)
		if suppressionErr != nil || !reflect.DeepEqual(suppression, terminal.Suppression) {
			return RehearsalAttemptResultV1{}, errors.New("suppression regeneration mismatch")
		}
		if terminal.Coverage.State == "complete" {
			delta, coverageErr := buildFieldCoverage(attested.Decoded, attested.Records, filepath.Join(repo, "internal", "integration", "m4", "testdata", "captured_gsi_schedule.json"))
			if coverageErr != nil || terminal.Coverage.Delta == nil || !reflect.DeepEqual(delta, *terminal.Coverage.Delta) {
				return RehearsalAttemptResultV1{}, errors.New("coverage regeneration mismatch")
			}
		}
	}
	for name, expected := range map[string]any{
		"raw-manifest.json": struct {
			SchemaVersion string `json:"schema_version"`
			SHA256        string `json:"sha256"`
		}{"rehearsal_raw_manifest.v1", terminal.RawManifestSHA256},
		"coverage.json":    terminal.Coverage,
		"suppression.json": terminal.Suppression,
	} {
		payload, readErr := rootReadFile(filepath.Join(lease.abs, "rehearsal", name))
		canonicalPayload, marshalErr := canonical(expected)
		if readErr != nil || marshalErr != nil || !bytes.Equal(payload, canonicalPayload) {
			return RehearsalAttemptResultV1{}, errors.New("terminal component file mismatch")
		}
	}
	if terminal.Outcome == "rehearsal_completed" {
		if terminal.Producer == nil {
			return RehearsalAttemptResultV1{}, errors.New("producer completion evidence absent")
		}
		retained, retainedErr := readProducerEvidence(lease.abs)
		if retainedErr != nil || !reflect.DeepEqual(retained, *terminal.Producer) {
			return RehearsalAttemptResultV1{}, errors.New("producer completion evidence cross-link failed")
		}
		if err := verifyProducerEvidenceFromRetainedInputs(ctx, lease.abs, preflight, retained); err != nil {
			return RehearsalAttemptResultV1{}, errors.New("producer completion evidence verification failed")
		}
	} else if terminal.Producer != nil {
		retained, retainedErr := readProducerEvidence(lease.abs)
		validationErr := validateProducerEvidence(lease.abs, retained, false)
		if retainedErr != nil || !reflect.DeepEqual(retained, *terminal.Producer) || validationErr != nil {
			return RehearsalAttemptResultV1{}, errors.Join(errors.New("failed producer evidence invalid"), retainedErr, validationErr)
		}
	}
	return RehearsalAttemptResultV1{Outcome: terminal.Outcome, FailureCode: terminal.FailureCode, TerminalSHA256: terminal.CleanupToken, CleanupToken: terminal.CleanupToken}, nil
}

func readRehearsalTerminal(root string) (RehearsalTerminalV1, error) {
	var terminal RehearsalTerminalV1
	payload, err := rootReadFile(filepath.Join(root, "rehearsal", "terminal.json"))
	if err != nil || json.Unmarshal(payload, &terminal) != nil {
		return terminal, errors.New("rehearsal terminal unavailable")
	}
	canonicalPayload, marshalErr := canonical(terminal)
	if marshalErr != nil || !bytes.Equal(payload, canonicalPayload) {
		return terminal, errors.New("rehearsal terminal is not canonical")
	}
	token, tokenErr := rehearsalTerminalContentID(terminal)
	seal, sealErr := rootReadFile(filepath.Join(root, "rehearsal", "terminal.sha256"))
	if tokenErr != nil || sealErr != nil || terminal.CleanupToken != token || string(seal) != token+"\n" {
		return terminal, errors.New("rehearsal terminal seal mismatch")
	}
	return terminal, nil
}

func validateRehearsalTerminal(value RehearsalTerminalV1) error {
	if value.SchemaVersion != RehearsalTerminalSchemaV1 || value.Purpose != RehearsalPurpose || value.MatchClass != RehearsalMatchClass ||
		value.AcceptedSpec != AcceptedRehearsalSpec || value.AcceptedP4Spec != AcceptedP4Spec || value.ClaimsP4 || value.QualifyingMatch || value.AcceptanceEligible || value.AcceptanceGate != "none" {
		return errors.New("rehearsal terminal contract mismatch")
	}
	if value.Outcome == "rehearsal_completed" {
		if value.FailureCode != "" || value.Producer == nil || value.Coverage.State != "complete" || value.Coverage.Delta == nil || value.Coverage.AcceptedFrames == 0 || value.RawAdmission.Failure.Primary != "" || value.RawAdmission.Failure.ConcurrentChange != "" {
			return errors.New("invalid completed rehearsal")
		}
	} else if value.Outcome == "rehearsal_failed" {
		if value.FailureCode == "" {
			return errors.New("failed rehearsal lacks typed failure")
		}
	} else {
		return errors.New("unknown rehearsal outcome")
	}
	if value.Coverage.State == "unavailable" {
		accepted := map[string]bool{"no_accepted_frames": true, "insufficient_frames": true, "baseline_identity_failure": true, "rehearsal_identity_failure": true, "coverage_generation_failure": true}
		if !accepted[value.Coverage.Reason] || value.Coverage.Delta != nil {
			return errors.New("invalid unavailable coverage union")
		}
	} else if value.Coverage.State == "complete" {
		if value.Coverage.Reason != "" || value.Coverage.Delta == nil || value.Coverage.AcceptedFrames != value.Coverage.Delta.FrameCount {
			return errors.New("invalid complete coverage union")
		}
	} else {
		return errors.New("unknown coverage union")
	}
	if value.RawAdmission.BytesRead > MaxRehearsalRawBytes+1 || value.RawAdmission.AcceptedRecords > MaxRehearsalRawRecords || value.RawAdmission.CompleteLines > MaxRehearsalRawRecords+1 {
		return errors.New("raw receipt population exceeds bounds")
	}
	if value.RawAdmission.BytesRead > 0 {
		if !validLowerSHA256(value.RawAdmission.ReadPrefixSHA256) || value.RawAdmission.PreDescriptor.Inode == 0 || value.RawAdmission.PostDescriptor.Inode == 0 || value.RawAdmission.PrePath.Inode == 0 || value.RawAdmission.PostPath.Inode == 0 {
			return errors.New("identity-bearing failed raw receipt incomplete")
		}
		if (value.RawAdmission.Failure.Primary != "" || value.RawAdmission.Failure.ConcurrentChange != "") && value.RawAdmission.FullContentHash {
			return errors.New("failed raw receipt claims full content")
		}
	}
	if len(value.Suppression) > MaxRehearsalRawRecords || len(value.Process) > MaxRehearsalRawRecords+3 {
		return errors.New("terminal population exceeds bounds")
	}
	if uint64(len(value.Suppression)) != value.RawAdmission.AcceptedRecords {
		return errors.New("suppression/raw population mismatch")
	}
	for index, frame := range value.Suppression {
		if frame.RawSequence != uint64(index+1) || !validLowerSHA256(frame.RawRecordSHA256) || len(frame.Audits) != insight.MaxFamilySuppressionAudits {
			return errors.New("invalid terminal suppression population")
		}
	}
	if err := validateRehearsalProcessPopulation(value); err != nil {
		return err
	}
	return nil
}

func verifyProducerEvidenceFromRetainedInputs(ctx context.Context, root string, preflight RehearsalPreflightV1, evidence RehearsalProducerEvidenceV1) error {
	if err := validateProducerEvidence(root, evidence, true); err != nil {
		return err
	}
	ownerSHA, wantRunID, identityErr := deriveRetainedProducerRunIdentity(root, preflight)
	if identityErr != nil || evidence.RootOwnerSHA256 != ownerSHA || evidence.PreflightSHA256 != preflight.PreflightSHA256 || evidence.SessionID != preflight.SessionID || evidence.RunID != wantRunID {
		return errors.New("producer run identity does not derive from retained owner/preflight evidence")
	}
	regenerated := collectProducerArtifacts(root, evidence.SessionID)
	if !reflect.DeepEqual(regenerated, evidence.Artifacts) {
		return errors.New("producer artifact manifest regeneration mismatch")
	}
	rawPath := filepath.Join(root, "data/sessions", evidence.SessionID, "raw.jsonl")
	rawCount, err := readRawSequences(rawPath)
	if err != nil || rawCount != evidence.RawRecords {
		return errors.New("producer raw population regeneration mismatch")
	}
	validation, err := summarizeAttempt(filepath.Join(root, "data/sessions", evidence.SessionID))
	everyRawTerminal := validation.RawCount > 0 && validation.CursorSequence == validation.RawCount && validation.LastObservation <= validation.CursorSequence && validation.ObservationCommits > 0
	if err != nil || validation.RawCount != rawCount || !everyRawTerminal {
		return errors.New("producer raw/policy reconciliation mismatch")
	}
	resourceCount, resourceErr := countCanonicalJSONL(filepath.Join(root, "evidence/samples.jsonl"), 200_000)
	if resourceErr != nil || resourceCount != evidence.ResourceSamples || validateSamples(filepath.Join(root, "evidence/samples.jsonl"), root) != nil {
		return errors.New("producer resource samples failed independent validation")
	}
	visibilityCount, visibilityErr := countCanonicalJSONL(filepath.Join(root, "evidence/visibility.jsonl"), 200_000)
	if visibilityErr != nil || visibilityCount != evidence.VisibilitySamples || validateVisibility(filepath.Join(root, "evidence/visibility.jsonl"), filepath.Join(root, "data/sessions", evidence.SessionID)) != nil {
		return errors.New("producer visibility samples failed independent validation")
	}
	if !recordingFinalized(filepath.Join(root, "recordings"), filepath.Join(root, "evidence/logs/obs-live.log")) {
		return errors.New("producer recording finalization did not regenerate")
	}
	actions := append([]string(nil), validation.OperatorActions...)
	if !reflect.DeepEqual(actions, evidence.OperatorActions) || validateOperatorActionPopulation(actions) != nil {
		return errors.New("producer operator action regeneration mismatch")
	}
	sourceRaw, _, sourceErr := fileSHA(rawPath)
	recoveryRaw, _, recoveryErr := fileSHA(filepath.Join(root, "evidence/recovery-input", evidence.SessionID, "raw.jsonl"))
	if sourceErr != nil || recoveryErr != nil || sourceRaw != recoveryRaw {
		return errors.New("producer recovery input is not byte-equal to retained raw")
	}
	if err := validateRecoveryProcessPopulation(evidence.RecoveryPID, evidence.RecoveryProcess); err != nil {
		return err
	}
	var declared recoveryProof
	proofPayload, proofErr := rootReadFile(filepath.Join(root, "evidence/canonical/raw-only-recovery.json"))
	if proofErr != nil || json.Unmarshal(proofPayload, &declared) != nil || !declared.CleanShutdown {
		return errors.New("producer recovery proof invalid")
	}
	regeneratedProof, regeneratedProcess, regenerationErr := reconstructRecoveryFromRetained(ctx, root, evidence.SessionID)
	if regenerationErr != nil || len(regeneratedProcess) != 2 || validateRecoveryProcessPopulation(regeneratedProcess[0].Identity.PID, regeneratedProcess) != nil {
		return errors.New("fresh retained-input recovery reconstruction failed")
	}
	if declared.CursorSHA256 != regeneratedProof.CursorSHA256 || declared.PolicySHA256 != regeneratedProof.PolicySHA256 || declared.AuditSHA256 != regeneratedProof.AuditSHA256 || declared.OperatorSHA256 != regeneratedProof.OperatorSHA256 || declared.OverlaySHA256 != regeneratedProof.OverlaySHA256 || !regeneratedProof.CleanShutdown || regeneratedProof.MaxRSSBytes <= 0 || regeneratedProof.MaxRSSBytes > AcceptedBounds().RecoveryRSSBytes {
		return errors.New("fresh recovery material outputs differ from retained claims")
	}
	return nil
}

func deriveRetainedProducerRunIdentity(root string, preflight RehearsalPreflightV1) (string, string, error) {
	var owner RehearsalRootOwnerV1
	ownerPayload, ownerErr := rootReadFile(filepath.Join(root, "rehearsal/owner.json"))
	if ownerErr != nil || json.Unmarshal(ownerPayload, &owner) != nil {
		return "", "", errors.New("retained owner evidence unavailable")
	}
	ownerCanonical, canonicalErr := canonical(owner)
	ownerSHA, shaErr := payloadSHAFromCanonical(owner)
	if canonicalErr != nil || shaErr != nil || !bytes.Equal(ownerPayload, ownerCanonical) || ownerSHA != preflight.RootOwnerSHA256 || owner.SchemaVersion != "rehearsal_root_owner.v1" || owner.Purpose != RehearsalPurpose || owner.SessionID != preflight.SessionID || owner.CandidateCommit != preflight.CandidateCommit || owner.RawRelativePath != filepath.ToSlash(filepath.Join("data/sessions", preflight.SessionID, "raw.jsonl")) {
		return "", "", errors.New("retained owner identity mismatch")
	}
	runID, runErr := contracts.CanonicalSHA256(struct {
		Purpose   string `json:"purpose"`
		Session   string `json:"session"`
		Preflight string `json:"preflight"`
		Owner     string `json:"owner"`
	}{RehearsalPurpose, preflight.SessionID, preflight.PreflightSHA256, ownerSHA})
	return ownerSHA, runID, runErr
}

func countCanonicalJSONL(path string, limit uint64) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var count uint64
	for scanner.Scan() {
		count++
		if count > limit || !json.Valid(scanner.Bytes()) {
			return count, errors.New("JSONL population invalid or exceeds bound")
		}
	}
	return count, scanner.Err()
}

func reconstructRecoveryFromRetained(ctx context.Context, retainedRoot, sessionID string) (recoveryProof, []RehearsalOwnedProcessObservationV1, error) {
	fresh, err := os.MkdirTemp(isolatedBase, "dot84-recovery-verify-")
	if err != nil {
		return recoveryProof{}, nil, err
	}
	defer os.RemoveAll(fresh)
	paths := []string{
		"application/dota2-ob",
		filepath.ToSlash(filepath.Join("data/sessions", sessionID)),
		"evidence/raw-operator-input.jsonl",
		"evidence/canonical/final-operator.json",
		"evidence/canonical/final-overlay.json",
		"config/policy/history_availability_binding_v1.json",
		"config/policy/policy_lineage_manifest_v3.json",
		"config/policy/live_only_release_binding_v1.json",
	}
	for _, relative := range paths {
		if err := copyRetainedVerificationPath(retainedRoot, fresh, relative); err != nil {
			return recoveryProof{}, nil, err
		}
	}
	var process []RehearsalOwnedProcessObservationV1
	proof, _, err := performRawOnlyRecoveryObserved(ctx, fresh, sessionID, func(boundary string, identity RehearsalOwnedProcessIdentityV1) {
		process = append(process, RehearsalOwnedProcessObservationV1{Boundary: boundary, Identity: identity})
	})
	return proof, process, err
}

func copyRetainedVerificationPath(sourceRoot, destinationRoot, relative string) error {
	source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("retained verification source unavailable or linked")
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(destinationRoot, filepath.FromSlash(relative))), 0o700); err != nil {
			return err
		}
		return copyFile(source, filepath.Join(destinationRoot, filepath.FromSlash(relative)), info.Mode().Perm())
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			if walkErr != nil {
				return walkErr
			}
			return errors.New("retained verification tree contains a link")
		}
		suffix, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destinationRoot, filepath.FromSlash(relative), suffix)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			return errors.New("retained verification tree contains a non-regular file")
		}
		return copyFile(path, target, entryInfo.Mode().Perm())
	})
}

func validateRehearsalProcessPopulation(value RehearsalTerminalV1) error {
	if value.FailureCode == "preflight_refused" || value.FailureCode == "dota_identity_unavailable" {
		if len(value.Process) != 0 {
			return errors.New("identity-free failure contains process observations")
		}
		return nil
	}
	if value.Outcome == "rehearsal_completed" {
		if value.Producer == nil || !reflect.DeepEqual(value.Process, value.Producer.DotaContinuity) {
			return errors.New("terminal/producer Dota continuity mismatch")
		}
		return validateProducerDotaContinuity(value.Process, RehearsalDotaIdentityV1{}, nil, false)
	}
	for _, observation := range value.Process {
		if !validRehearsalIdentity(observation.ObservedIdentity) || (observation.RawRecordSHA256 != "" && !validLowerSHA256(observation.RawRecordSHA256)) {
			return errors.New("failed terminal contains invalid Dota observation")
		}
	}
	return nil
}

func validRehearsalIdentity(value RehearsalDotaIdentityV1) bool {
	return value.SchemaVersion == "rehearsal_dota_identity.v1" && value.PID > 1 && value.Comm == "dota2" && value.ExecutablePath != "" &&
		validLowerSHA256(value.ExecutablePathSHA256) && validLowerSHA256(value.ExecutableSHA256) && value.ProcessStartTicks > 0 && value.ExecutableDevice > 0 && value.ExecutableInode > 0
}

func validLowerSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func shouldRegenerateRetainedInput(terminal RehearsalTerminalV1) bool {
	return terminal.FailureCode != "preflight_refused" && terminal.FailureCode != "dota_identity_unavailable"
}

func CleanupRehearsal(ctx context.Context, dataRoot, repoRoot, token string) error {
	result, err := VerifyRehearsal(ctx, dataRoot, repoRoot)
	if err != nil {
		return err
	}
	if token == "" || token != result.CleanupToken {
		return errors.New("rehearsal cleanup token mismatch")
	}
	lease, err := acquireExistingRoot(dataRoot, repoRoot)
	if err != nil {
		return err
	}
	defer lease.base.Close()
	return lease.removeAll()
}
