package m4match

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func RehearsalAttempt(ctx context.Context, config RehearsalAttemptConfig) (RehearsalAttemptResultV1, error) {
	repo, err := filepath.Abs(config.RepoRoot)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	lease, err := acquireExistingRoot(config.DataRoot, repo)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	defer lease.Close()
	preflight, err := readRehearsalPreflight(lease.abs)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if _, statErr := rootReadFile(filepath.Join(lease.abs, "rehearsal", "terminal.json")); statErr == nil {
		return RehearsalAttemptResultV1{}, errors.New("rehearsal attempts are non-resumable")
	}
	terminal := RehearsalTerminalV1{
		SchemaVersion: RehearsalTerminalSchemaV1, Purpose: RehearsalPurpose, MatchClass: RehearsalMatchClass,
		AcceptedSpec: AcceptedRehearsalSpec, AcceptedP4Spec: AcceptedP4Spec, CandidateCommit: preflight.CandidateCommit,
		CandidateParent: preflight.CandidateParent, SessionID: preflight.SessionID, PreflightSHA256: preflight.PreflightSHA256,
		Outcome: "rehearsal_failed", ClaimsP4: false, QualifyingMatch: false, AcceptanceEligible: false, AcceptanceGate: "none",
	}
	if preflight.ConsoleState == RehearsalRefused {
		terminal.FailureCode = "preflight_refused"
		terminal.Coverage = unavailableCoverage("no_accepted_frames", 0, "")
		return sealRehearsalTerminal(lease.abs, terminal)
	}
	if preflight.ConsoleState != RehearsalReady {
		return RehearsalAttemptResultV1{}, errors.New("unknown rehearsal preflight state")
	}
	identity := config.identity
	if identity == nil {
		identity = discoverRehearsalDotaIdentity
	}
	bound, identityErr := identity()
	if identityErr != nil {
		terminal.FailureCode = "dota_identity_unavailable"
		terminal.Coverage = unavailableCoverage("no_accepted_frames", 0, "")
		return sealRehearsalTerminal(lease.abs, terminal)
	}
	terminal.Process = append(terminal.Process, RehearsalProcessObservationV1{Boundary: "attempt_start", ObservedIdentity: bound})
	producerDriver := config.producer
	if producerDriver == nil && config.identity != nil {
		// An injected identity is an unexported package-test seam. It must never
		// cause the runtime executable to launch physical processes implicitly.
		producerDriver = closedRehearsalTestDriver{}
	}
	producerEvidence, producerErr := executeRehearsalProducer(ctx, lease.abs, repo, preflight, bound, identity, producerDriver)
	terminal.Producer = &producerEvidence
	rawPath := filepath.Join(lease.abs, "data", "sessions", preflight.SessionID, "raw.jsonl")
	attested, admissionErr := session.ReadAttestedRawV1WithGuard(rawPath, preflight.SessionID, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, MaxRehearsalRawRecords, func(record *session.Record, _ string) error {
		observed, observeErr := identity()
		terminal.Process = append(terminal.Process, RehearsalProcessObservationV1{Boundary: "raw_admission", RawSequence: record.Sequence, ObservedIdentity: observed})
		if observeErr != nil || !sameRehearsalDotaIdentity(bound, observed) {
			return errors.New("dota identity changed before raw admission")
		}
		return nil
	})
	terminal.RawAdmission = attested.Receipt
	terminal.RawManifestSHA256, _ = contracts.CanonicalSHA256(attested.Records)
	endIdentity, endErr := identity()
	terminal.Process = append(terminal.Process, RehearsalProcessObservationV1{Boundary: "attempt_terminal", RawSequence: uint64(len(attested.Records)), ObservedIdentity: endIdentity})
	if endErr != nil || !sameRehearsalDotaIdentity(bound, endIdentity) {
		if terminal.RawAdmission.Failure.ConcurrentChange == "" {
			terminal.RawAdmission.Failure.ConcurrentChange = "dota_identity_terminal_drift"
		}
		admissionErr = errors.New("dota identity changed at terminal")
	}
	if admissionErr != nil {
		terminal.FailureCode = "raw_admission_failed"
	}

	suppression, suppressionErr := deriveRehearsalSuppression(preflight.SessionID, attested)
	terminal.Suppression = suppression
	if suppressionErr != nil && terminal.FailureCode == "" {
		terminal.FailureCode = "suppression_evidence_failed"
	}
	if len(attested.Records) == 0 {
		terminal.Coverage = unavailableCoverage("no_accepted_frames", 0, attested.Receipt.ReadPrefixSHA256)
		if terminal.FailureCode == "" {
			terminal.FailureCode = "zero_frame_attempt"
		}
	} else if len(attested.Records) == 1 {
		terminal.Coverage = unavailableCoverage("insufficient_frames", 1, attested.Receipt.ReadPrefixSHA256)
		if terminal.FailureCode == "" {
			terminal.FailureCode = "partial_frame_attempt"
		}
	} else {
		delta, coverageErr := buildFieldCoverage(attested.Decoded, attested.Records, filepath.Join(repo, "internal", "integration", "m4", "testdata", "captured_gsi_schedule.json"))
		if coverageErr != nil {
			terminal.Coverage = unavailableCoverage("coverage_generation_failure", uint64(len(attested.Records)), attested.Receipt.ReadPrefixSHA256)
			if terminal.FailureCode == "" {
				terminal.FailureCode = "coverage_generation_failed"
			}
		} else {
			terminal.Coverage = FieldCoverageStatusV1{State: "complete", AcceptedFrames: uint64(len(attested.Records)), RawPrefixSHA256: attested.Receipt.ReadPrefixSHA256, Delta: &delta}
		}
	}
	// A completed outcome additionally requires producer-owned operator, OBS,
	// resource, recovery, and reconciliation artifacts. They are deliberately
	// absent from automated no-match runs, so those runs fail deterministically.
	if terminal.FailureCode == "" {
		if producerErr != nil || terminal.Producer == nil || validateProducerEvidence(lease.abs, *terminal.Producer, true) != nil {
			terminal.FailureCode = "producer_lifecycle_failed"
		} else {
			terminal.Outcome = "rehearsal_completed"
		}
	}
	return sealRehearsalTerminal(lease.abs, terminal)
}

func deriveRehearsalSuppression(sessionID string, raw session.AttestedRawV1) ([]RehearsalSuppressionFrameV1, error) {
	history, lineage, _, err := rehearsalSuccessorArtifacts(sessionID)
	if err != nil {
		return nil, err
	}
	availability, err := insight.ProductLiveOnlyAvailabilityV1(history)
	if err != nil {
		return nil, err
	}
	if len(raw.Decoded)*insight.MaxFamilySuppressionAudits > MaxRehearsalExecutions {
		return nil, errors.New("suppression population exceeds bound")
	}
	frames := make([]RehearsalSuppressionFrameV1, 0, len(raw.Decoded))
	var previous *contracts.LiveObservationV1
	for index, record := range raw.Decoded {
		observation, mapErr := capture.MapLiveObservationV1(record)
		if mapErr != nil {
			return nil, mapErr
		}
		input := insight.LiveOnlyInputV2{
			Observation: observation, Previous: previous, History: history, Lineage: lineage, Availability: availability,
			RawRecordSHA256: raw.Records[index].RawRecordSHA256, PolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli(),
		}
		result := insight.EvaluateLiveOnlyV2(input, insight.DefaultConfig())
		if validateErr := insight.ValidateLiveOnlyEvaluationV2(result, input); validateErr != nil {
			return nil, validateErr
		}
		frames = append(frames, RehearsalSuppressionFrameV1{RawSequence: record.Sequence, RawRecordSHA256: raw.Records[index].RawRecordSHA256, Audits: result.Audits})
		copyObservation := observation
		previous = &copyObservation
	}
	return frames, nil
}

func unavailableCoverage(reason string, frames uint64, prefix string) FieldCoverageStatusV1 {
	return FieldCoverageStatusV1{State: "unavailable", Reason: reason, AcceptedFrames: frames, RawPrefixSHA256: prefix}
}

func sealRehearsalTerminal(root string, terminal RehearsalTerminalV1) (RehearsalAttemptResultV1, error) {
	token, err := rehearsalTerminalContentID(terminal)
	if err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	terminal.CleanupToken = token
	if err := writeJSON(filepath.Join(root, "rehearsal", "raw-manifest.json"), struct {
		SchemaVersion string `json:"schema_version"`
		SHA256        string `json:"sha256"`
	}{"rehearsal_raw_manifest.v1", terminal.RawManifestSHA256}, 0o600); err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if err := writeJSON(filepath.Join(root, "rehearsal", "coverage.json"), terminal.Coverage, 0o600); err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if err := writeJSON(filepath.Join(root, "rehearsal", "suppression.json"), terminal.Suppression, 0o600); err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if err := writeJSON(filepath.Join(root, "rehearsal", "terminal.json"), terminal, 0o600); err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	if err := writePrivate(filepath.Join(root, "rehearsal", "terminal.sha256"), []byte(token+"\n")); err != nil {
		return RehearsalAttemptResultV1{}, err
	}
	return RehearsalAttemptResultV1{Outcome: terminal.Outcome, FailureCode: terminal.FailureCode, TerminalSHA256: token, CleanupToken: token}, nil
}

func rehearsalTerminalContentID(value RehearsalTerminalV1) (string, error) {
	value.CleanupToken = ""
	return payloadSHAFromCanonical(value)
}
