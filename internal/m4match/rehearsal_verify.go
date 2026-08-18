package m4match

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
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
		if err := verifyProducerEvidenceFromRetainedInputs(lease.abs, retained); err != nil {
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
	if len(value.Suppression) > MaxRehearsalRawRecords || len(value.Process) > MaxRehearsalRawRecords+2 {
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

func verifyProducerEvidenceFromRetainedInputs(root string, evidence RehearsalProducerEvidenceV1) error {
	if err := validateProducerEvidence(root, evidence, true); err != nil {
		return err
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
	if err := validateSamples(filepath.Join(root, "evidence/samples.jsonl"), root); err != nil {
		return errors.New("producer resource samples failed independent validation")
	}
	if err := validateVisibility(filepath.Join(root, "evidence/visibility.jsonl"), filepath.Join(root, "data/sessions", evidence.SessionID)); err != nil {
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
	var proof recoveryProof
	proofPayload, proofErr := rootReadFile(filepath.Join(root, "evidence/canonical/raw-only-recovery.json"))
	if proofErr != nil || json.Unmarshal(proofPayload, &proof) != nil || !proof.CleanShutdown || proof.CursorSHA256 == "" || proof.PolicySHA256 == "" || proof.AuditSHA256 == "" || proof.OperatorSHA256 == "" || proof.OverlaySHA256 == "" {
		return errors.New("producer recovery proof invalid")
	}
	return nil
}

func validateRehearsalProcessPopulation(value RehearsalTerminalV1) error {
	if value.FailureCode == "preflight_refused" || value.FailureCode == "dota_identity_unavailable" {
		if len(value.Process) != 0 {
			return errors.New("identity-free failure contains process observations")
		}
		return nil
	}
	if len(value.Process) < 2 || value.Process[0].Boundary != "attempt_start" || value.Process[len(value.Process)-1].Boundary != "attempt_terminal" {
		return errors.New("process terminal boundaries missing")
	}
	bound := value.Process[0].ObservedIdentity
	if !validRehearsalIdentity(bound) {
		return errors.New("bound Dota identity invalid")
	}
	for index, observation := range value.Process[1 : len(value.Process)-1] {
		if observation.Boundary != "raw_admission" || observation.RawSequence != uint64(index+1) || !validRehearsalIdentity(observation.ObservedIdentity) {
			return errors.New("per-sequence Dota identity population invalid")
		}
	}
	terminal := value.Process[len(value.Process)-1]
	if terminal.RawSequence != value.RawAdmission.AcceptedRecords || !validRehearsalIdentity(terminal.ObservedIdentity) {
		return errors.New("terminal Dota identity invalid")
	}
	if value.Outcome == "rehearsal_completed" {
		if len(value.Process) != int(value.RawAdmission.AcceptedRecords)+2 {
			return errors.New("completed identity population mismatch")
		}
		for _, observation := range value.Process[1:] {
			if !sameRehearsalDotaIdentity(bound, observation.ObservedIdentity) {
				return errors.New("completed Dota identity drift")
			}
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
