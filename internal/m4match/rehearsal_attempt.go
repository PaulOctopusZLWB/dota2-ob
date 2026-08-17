package m4match

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type RehearsalAttemptRequestV1 struct {
	SchemaVersion   string `json:"schema_version"`
	ExpectedMatchID string `json:"expected_match_id,omitempty"`
}
type RehearsalAttemptConfig struct {
	DataRoot string
	RepoRoot string
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
	RawSHA256     string                   `json:"raw_sha256"`
	RawBytes      int64                    `json:"raw_bytes"`
	Observed      uint64                   `json:"observed_frames"`
	Accepted      uint64                   `json:"accepted_frames"`
	Rejected      uint64                   `json:"rejected_frames"`
	FailureCode   string                   `json:"failure_code,omitempty"`
	Records       []RehearsalRawIdentityV1 `json:"records"`
}
type RehearsalDerivedFactsV1 struct {
	MatchID         string `json:"match_id,omitempty"`
	EntryBeforeZero bool   `json:"entry_before_zero"`
	ClockOrdered    bool   `json:"clock_ordered"`
	Continuous      bool   `json:"continuous"`
	NormalPostGame  bool   `json:"normal_post_game"`
	WinnerObserved  bool   `json:"winner_observed"`
}
type SuppressionExecutionV1 struct {
	SourceSequence   uint64 `json:"source_sequence"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
	Family           string `json:"family"`
	EligibilitySite  string `json:"eligibility_site"`
	Executed         bool   `json:"executed"`
	FallbackUsed     bool   `json:"fallback_used"`
}
type SuppressionAuditV1 struct {
	SourceSequence   uint64 `json:"source_sequence"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
	Family           string `json:"family"`
	EligibilitySite  string `json:"eligibility_site"`
	Reason           string `json:"reason"`
	Result           string `json:"result"`
	CandidateClaim   bool   `json:"candidate_claim"`
	DecisionClaim    bool   `json:"decision_claim"`
	OverlayClaim     bool   `json:"overlay_claim"`
	DisplayProduced  bool   `json:"display_produced"`
}
type RehearsalSuppressionEvidenceV1 struct {
	SchemaVersion  string                   `json:"schema_version"`
	RegistrySHA256 string                   `json:"registry_sha256"`
	FrameCount     uint64                   `json:"frame_count"`
	Executions     []SuppressionExecutionV1 `json:"executions"`
	Audits         []SuppressionAuditV1     `json:"audits"`
}
type RehearsalProducerReceiptV1 struct {
	SchemaVersion string `json:"schema_version"`
	SessionID     string `json:"session_id"`
	State         string `json:"state"`
	FailureCode   string `json:"failure_code,omitempty"`
	HarnessSHA256 string `json:"harness_sha256,omitempty"`
}
type RehearsalHarnessEvidenceV1 struct {
	SchemaVersion    string                       `json:"schema_version"`
	SessionID        string                       `json:"session_id"`
	RawSHA256        string                       `json:"raw_sha256"`
	AcquiredDota     RehearsalDotaIdentityV1      `json:"acquired_dota"`
	DotaObservations []RehearsalDotaObservationV1 `json:"dota_observations"`
	RecordingSHA256  string                       `json:"recording_sha256"`
	RecordingBytes   int64                        `json:"recording_bytes"`
	ValidationSHA256 string                       `json:"validation_sha256"`
	RecoverySHA256   string                       `json:"recovery_sha256"`
	ProductClean     bool                         `json:"product_clean"`
	OBSClean         bool                         `json:"obs_clean"`
}
type RehearsalTerminalV1 struct {
	SchemaVersion         string                  `json:"schema_version"`
	Identity              RehearsalIdentityV1     `json:"identity"`
	CandidateCommit       string                  `json:"candidate_commit"`
	PreflightSHA256       string                  `json:"preflight_sha256"`
	RequestSHA256         string                  `json:"request_sha256"`
	RawManifestSHA256     string                  `json:"raw_manifest_sha256"`
	ProducerReceiptSHA256 string                  `json:"producer_receipt_sha256"`
	HarnessSHA256         string                  `json:"harness_sha256"`
	SuppressionSHA256     string                  `json:"suppression_sha256"`
	RecoverySHA256        string                  `json:"recovery_sha256"`
	Coverage              FieldCoverageStatusV1   `json:"coverage"`
	DerivedFacts          RehearsalDerivedFactsV1 `json:"derived_facts"`
	Outcome               string                  `json:"outcome"`
	FailureCode           string                  `json:"failure_code,omitempty"`
	CleanupAdmissible     bool                    `json:"cleanup_admissible"`
	NonResumable          bool                    `json:"non_resumable"`
}
type RehearsalTerminalReceiptV1 struct {
	SchemaVersion     string `json:"schema_version"`
	TerminalSHA256    string `json:"terminal_sha256"`
	Outcome           string `json:"outcome"`
	FailureCode       string `json:"failure_code,omitempty"`
	CleanupAdmissible bool   `json:"cleanup_admissible"`
}
type scannedRehearsalRaw struct {
	Manifest RehearsalRawManifestV1
	Records  []*session.Record
	Payloads [][]byte
}

func scanRehearsalRaw(path, sessionID string) scannedRehearsalRaw {
	result := scannedRehearsalRaw{Manifest: RehearsalRawManifestV1{SchemaVersion: "public_match_rehearsal_raw_manifest.v3", SessionID: sessionID, Records: []RehearsalRawIdentityV1{}}, Records: []*session.Record{}, Payloads: [][]byte{}}
	summary, err := session.ReadAttestedRaw(path, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, func(line []byte, sequence uint64) error {
		result.Manifest.Observed++
		if sequence > MaxRehearsalRecords {
			return errors.New("raw record population exceeded")
		}
		record, decodeErr := session.DecodeRecordV3(line, sessionID, sequence)
		if decodeErr != nil {
			result.Manifest.Rejected++
			return decodeErr
		}
		lineSum := sha256.Sum256(line)
		payloadSum := sha256.Sum256(record.Raw)
		result.Manifest.Accepted++
		result.Manifest.Records = append(result.Manifest.Records, RehearsalRawIdentityV1{Sequence: sequence, RawRecordSHA256: hex.EncodeToString(lineSum[:]), RawPayloadSHA256: hex.EncodeToString(payloadSum[:])})
		result.Records = append(result.Records, record)
		result.Payloads = append(result.Payloads, append([]byte(nil), record.Raw...))
		return nil
	})
	if err != nil {
		result.Manifest.FailureCode = classifyRawFailure(err)
	}
	result.Manifest.RawSHA256, result.Manifest.RawBytes = summary.SHA256, summary.Bytes
	if result.Manifest.RawSHA256 == "" {
		empty := sha256.Sum256(nil)
		result.Manifest.RawSHA256 = hex.EncodeToString(empty[:])
	}
	return result
}
func classifyRawFailure(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "total limit"):
		return "raw_total_limit"
	case strings.Contains(message, "line limit"):
		return "raw_line_limit"
	case strings.Contains(message, "unterminated"):
		return "raw_unterminated"
	case strings.Contains(message, "changed"):
		return "raw_identity_changed"
	case strings.Contains(message, "population"):
		return "raw_record_limit"
	default:
		return "raw_provenance_failure"
	}
}

func deriveRehearsalFacts(raw scannedRehearsalRaw) RehearsalDerivedFactsV1 {
	facts := RehearsalDerivedFactsV1{ClockOrdered: true, Continuous: true}
	var priorClock int64
	haveClock := false
	for index, record := range raw.Records {
		observation, err := capture.MapLiveObservationV1(record)
		if err != nil {
			facts.ClockOrdered = false
			facts.Continuous = false
			continue
		}
		if observation.MatchID.Value == nil || observation.Map.ClockTime.Value == nil {
			facts.Continuous = false
			continue
		}
		match := *observation.MatchID.Value
		clock := *observation.Map.ClockTime.Value
		if index == 0 {
			facts.MatchID = match
			facts.EntryBeforeZero = clock < 0
		} else if match != facts.MatchID {
			facts.Continuous = false
		}
		if haveClock && clock < priorClock {
			facts.ClockOrdered = false
		}
		priorClock = clock
		haveClock = true
		if observation.Map.GameState.Value != nil && strings.Contains(strings.ToUpper(*observation.Map.GameState.Value), "POST_GAME") {
			facts.NormalPostGame = true
			facts.WinnerObserved = observation.Map.WinTeam.Value != nil && (*observation.Map.WinTeam.Value == "radiant" || *observation.Map.WinTeam.Value == "dire")
		}
	}
	return facts
}

func evaluateRehearsalSuppression(raw scannedRehearsalRaw) (RehearsalSuppressionEvidenceV1, string) {
	families := contracts.HistoricalDisabledFamiliesV1()
	registryPayload, _ := contracts.MarshalCanonical(families)
	result := RehearsalSuppressionEvidenceV1{SchemaVersion: "public_match_rehearsal_suppression.v3", RegistrySHA256: payloadSHA(registryPayload), Executions: []SuppressionExecutionV1{}, Audits: []SuppressionAuditV1{}}
	if len(raw.Records) == 0 {
		return result, ""
	}
	history, lineage, _, err := liveArtifacts(raw.Manifest.SessionID)
	if err != nil {
		return result, "production_binding_failure"
	}
	var previous *contracts.LiveObservationV1
	for index, record := range raw.Records {
		observation, mapErr := capture.MapLiveObservationV1(record)
		if mapErr != nil || observation.Validate() != nil {
			return result, "production_evaluation_failure"
		}
		evaluation, evaluateErr := insight.EvaluateLiveOnlyProduction(insight.LiveOnlyInput{Observation: observation, Previous: previous, History: history, Lineage: lineage, PolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli()}, insight.DefaultConfig())
		if evaluateErr != nil {
			return result, "production_evaluation_failure"
		}
		if len(evaluation.History) != len(families) {
			return result, "suppression_population_failure"
		}
		identity := raw.Manifest.Records[index]
		for familyIndex, audit := range evaluation.History {
			if audit.Family != families[familyIndex] || !audit.Executed || audit.Reason != "historical_unavailable" || audit.Evidence.Sequence != identity.Sequence || audit.Evidence.RawPayloadSHA256 != identity.RawPayloadSHA256 || !strings.HasPrefix(audit.EligibilitySite, audit.Family+".") {
				return result, "suppression_branch_failure"
			}
			execution := SuppressionExecutionV1{SourceSequence: identity.Sequence, RawRecordSHA256: identity.RawRecordSHA256, RawPayloadSHA256: identity.RawPayloadSHA256, Family: audit.Family, EligibilitySite: audit.EligibilitySite, Executed: true}
			result.Executions = append(result.Executions, execution)
			result.Audits = append(result.Audits, SuppressionAuditV1{SourceSequence: identity.Sequence, RawRecordSHA256: identity.RawRecordSHA256, RawPayloadSHA256: identity.RawPayloadSHA256, Family: audit.Family, EligibilitySite: audit.EligibilitySite, Reason: "unavailable_for_public_match", Result: "suppressed"})
		}
		for _, candidate := range evaluation.Candidates {
			for _, family := range families {
				if insight.Family(candidate.RuleVersion) == family && candidate.Availability == "available" {
					return result, "historical_claim_produced"
				}
			}
		}
		copyObservation := observation
		previous = &copyObservation
		result.FrameCount++
	}
	if err := validateRehearsalSuppression(result, raw.Manifest); err != nil {
		return result, "suppression_reconciliation_failure"
	}
	return result, ""
}
func validateRehearsalSuppression(value RehearsalSuppressionEvidenceV1, manifest RehearsalRawManifestV1) error {
	families := contracts.HistoricalDisabledFamiliesV1()
	registryPayload, _ := contracts.MarshalCanonical(families)
	if value.SchemaVersion != "public_match_rehearsal_suppression.v3" || value.RegistrySHA256 != payloadSHA(registryPayload) || value.FrameCount != manifest.Accepted || len(value.Executions) != int(value.FrameCount)*len(families) || len(value.Audits) != len(value.Executions) || len(value.Executions) > MaxRehearsalExecutions || len(value.Audits) > MaxRehearsalAudits {
		return errors.New("suppression population mismatch")
	}
	for index, execution := range value.Executions {
		recordIndex := index / len(families)
		familyIndex := index % len(families)
		record := manifest.Records[recordIndex]
		audit := value.Audits[index]
		if execution.SourceSequence != record.Sequence || execution.RawRecordSHA256 != record.RawRecordSHA256 || execution.RawPayloadSHA256 != record.RawPayloadSHA256 || execution.Family != families[familyIndex] || !execution.Executed || execution.FallbackUsed || !strings.HasPrefix(execution.EligibilitySite, execution.Family+".") || audit.SourceSequence != execution.SourceSequence || audit.RawRecordSHA256 != execution.RawRecordSHA256 || audit.RawPayloadSHA256 != execution.RawPayloadSHA256 || audit.Family != execution.Family || audit.EligibilitySite != execution.EligibilitySite || audit.Reason != "unavailable_for_public_match" || audit.Result != "suppressed" || audit.CandidateClaim || audit.DecisionClaim || audit.OverlayClaim || audit.DisplayProduced {
			return errors.New("suppression execution/audit mismatch")
		}
	}
	return nil
}

func loadBaselinePayloads(repo string) ([][]byte, error) {
	path := filepath.Join(repo, "internal/integration/m4/testdata/captured_gsi_schedule.json")
	hash, _, err := fileSHA(path)
	if err != nil || hash != acceptedBaselineSHA256 {
		return nil, errors.New("baseline identity failure")
	}
	data, err := readBoundedEvidence(path, MaxRehearsalArtifactBytes)
	if err != nil {
		return nil, err
	}
	var schedule struct {
		Updates []struct {
			Body json.RawMessage `json:"body"`
		} `json:"updates"`
	}
	if json.Unmarshal(data, &schedule) != nil || len(schedule.Updates) == 0 {
		return nil, errors.New("baseline decode failure")
	}
	result := make([][]byte, len(schedule.Updates))
	for i := range schedule.Updates {
		result[i] = append([]byte(nil), schedule.Updates[i].Body...)
	}
	return result, nil
}

type rebuiltRehearsal struct {
	Manifest, Suppression, Terminal, Receipt, Coverage []byte
	TerminalValue                                      RehearsalTerminalV1
	ReceiptValue                                       RehearsalTerminalReceiptV1
}

func rebuildRehearsal(root, repo string, readiness RehearsalReadinessV1, request RehearsalAttemptRequestV1) (rebuiltRehearsal, error) {
	ownerData, err := readBoundedEvidence(filepath.Join(root, "capture/owner.json"), MaxRehearsalArtifactBytes)
	if err != nil {
		return rebuiltRehearsal{}, err
	}
	var owner RehearsalCaptureOwnerV1
	if strictRehearsalJSON(ownerData, &owner) != nil {
		return rebuiltRehearsal{}, errors.New("owner invalid")
	}
	raw := scanRehearsalRaw(filepath.Join(root, filepath.FromSlash(owner.RawPath)), owner.SessionID)
	facts := deriveRehearsalFacts(raw)
	suppression, suppressionFailure := evaluateRehearsalSuppression(raw)
	manifestPayload, _ := canonical(raw.Manifest)
	suppressionPayload, _ := canonical(suppression)
	producerData, err := readBoundedEvidence(filepath.Join(root, "evidence/producer-receipt.json"), MaxRehearsalArtifactBytes)
	if err != nil {
		return rebuiltRehearsal{}, err
	}
	var producer RehearsalProducerReceiptV1
	if strictRehearsalJSON(producerData, &producer) != nil || producer.SessionID != owner.SessionID {
		return rebuiltRehearsal{}, errors.New("producer receipt invalid")
	}
	harnessSHA := unavailableArtifactSHA()
	var harness RehearsalHarnessEvidenceV1
	if producer.State == "sealed" {
		harnessData, readErr := readBoundedEvidence(filepath.Join(root, "evidence/harness.json"), MaxRehearsalArtifactBytes)
		if readErr != nil || payloadSHA(harnessData) != producer.HarnessSHA256 || strictRehearsalJSON(harnessData, &harness) != nil {
			return rebuiltRehearsal{}, errors.New("producer harness invalid")
		}
		harnessSHA = producer.HarnessSHA256
		if err = validateRehearsalHarness(root, harness, raw); err != nil {
			return rebuiltRehearsal{}, err
		}
	}
	coverage := FieldCoverageStatusV1{State: "unavailable", Unavailable: &FieldCoverageUnavailableV1{Reason: "no_accepted_frames", Observed: raw.Manifest.Observed, Accepted: raw.Manifest.Accepted, Rejected: raw.Manifest.Rejected, RehearsalRawSHA256: raw.Manifest.RawSHA256}}
	var coveragePayload []byte
	if raw.Manifest.Accepted == 1 {
		coverage.Unavailable.Reason = "insufficient_frames"
	} else if raw.Manifest.Accepted >= 2 {
		baseline, baselineErr := loadBaselinePayloads(repo)
		if baselineErr != nil {
			coverage.Unavailable.Reason = "baseline_identity_failure"
		} else {
			rootSHA := coverageRootSHA(readiness.EvidenceIndexSHA256, payloadSHA(manifestPayload), payloadSHA(suppressionPayload))
			delta, deltaErr := buildCoverageDelta(baseline, raw.Payloads, raw.Manifest.RawSHA256, rootSHA)
			if deltaErr != nil {
				coverage.Unavailable.Reason = "coverage_generation_failure"
			} else {
				coverage = FieldCoverageStatusV1{State: "complete", Complete: &delta}
				coveragePayload, _ = canonical(delta)
			}
		}
	}
	recoverySHA, recoveryErr := rehearsalRecoveryIdentity(owner.SessionID, raw.Manifest.Records, suppression)
	if recoveryErr != nil {
		recoverySHA = unavailableArtifactSHA()
	}
	failure := ""
	switch {
	case !readiness.Ready:
		failure = "preflight_refused"
	case producer.State != "sealed":
		failure = producer.FailureCode
	case producer.FailureCode != "":
		failure = producer.FailureCode
	case raw.Manifest.FailureCode != "":
		failure = raw.Manifest.FailureCode
	case raw.Manifest.Accepted == 0:
		failure = "no_accepted_frames"
	case raw.Manifest.Accepted == 1:
		failure = "insufficient_frames"
	case request.ExpectedMatchID != "" && facts.MatchID != request.ExpectedMatchID:
		failure = "expected_match_mismatch"
	case !facts.EntryBeforeZero:
		failure = "late_join"
	case !facts.ClockOrdered || !facts.Continuous:
		failure = "clock_or_continuity_failure"
	case !facts.NormalPostGame || !facts.WinnerObserved:
		failure = "post_game_failure"
	case suppressionFailure != "":
		failure = suppressionFailure
	case coverage.State != "complete":
		failure = coverage.Unavailable.Reason
	case harness.SessionID != owner.SessionID || harness.RawSHA256 != raw.Manifest.RawSHA256:
		failure = "producer_harness_mismatch"
	}
	outcome := "rehearsal_failed"
	if failure == "" {
		outcome = "rehearsal_completed"
	}
	requestPayload, _ := canonical(request)
	terminal := RehearsalTerminalV1{SchemaVersion: "public_match_rehearsal_terminal.v3", Identity: acceptedRehearsalIdentity(), CandidateCommit: readiness.CandidateCommit, PreflightSHA256: readiness.EvidenceIndexSHA256, RequestSHA256: payloadSHA(requestPayload), RawManifestSHA256: payloadSHA(manifestPayload), ProducerReceiptSHA256: payloadSHA(producerData), HarnessSHA256: harnessSHA, SuppressionSHA256: payloadSHA(suppressionPayload), RecoverySHA256: recoverySHA, Coverage: coverage, DerivedFacts: facts, Outcome: outcome, FailureCode: failure, CleanupAdmissible: true, NonResumable: true}
	terminalPayload, _ := canonical(terminal)
	receipt := RehearsalTerminalReceiptV1{SchemaVersion: "public_match_rehearsal_receipt.v3", TerminalSHA256: payloadSHA(terminalPayload), Outcome: outcome, FailureCode: failure, CleanupAdmissible: true}
	receiptPayload, _ := canonical(receipt)
	if forbiddenRehearsalBytes(terminalPayload, receiptPayload, suppressionPayload, coveragePayload) {
		return rebuiltRehearsal{}, errors.New("canonical rehearsal output leaked forbidden identity/capability")
	}
	return rebuiltRehearsal{Manifest: manifestPayload, Suppression: suppressionPayload, Terminal: terminalPayload, Receipt: receiptPayload, Coverage: coveragePayload, TerminalValue: terminal, ReceiptValue: receipt}, nil
}

func validateRehearsalHarness(root string, harness RehearsalHarnessEvidenceV1, raw scannedRehearsalRaw) error {
	bound := harness.AcquiredDota
	if harness.SchemaVersion != "public_match_rehearsal_harness.v2" || harness.SessionID != raw.Manifest.SessionID || harness.RawSHA256 != raw.Manifest.RawSHA256 ||
		bound.PID <= 0 || strings.ToLower(bound.Comm) != "dota2" || !validSHA256(bound.ExecutableSHA256) || !validSHA256(bound.ExecutablePathSHA256) || bound.StartTicks == 0 ||
		!harness.ProductClean || !harness.OBSClean || !validSHA256(harness.RecordingSHA256) || harness.RecordingBytes <= 0 || !validSHA256(harness.ValidationSHA256) || !validSHA256(harness.RecoverySHA256) {
		return errors.New("producer harness identity invalid")
	}
	wantCount := len(raw.Manifest.Records) + 5
	if len(harness.DotaObservations) != wantCount {
		return errors.New("Dota observation population mismatch")
	}
	for index, observation := range harness.DotaObservations {
		if observation.Identity != bound {
			return errors.New("Dota observation identity drift")
		}
		switch {
		case index == 0 && observation.Transition == "acquired" && observation.SourceSequence == 0:
		case index == 1 && observation.Transition == "product_ready" && observation.SourceSequence == 0:
		case index == 2 && observation.Transition == "obs_started" && observation.SourceSequence == 0:
		case index >= 3 && index < 3+len(raw.Manifest.Records) && observation.Transition == "raw" && observation.SourceSequence == raw.Manifest.Records[index-3].Sequence:
		case index == wantCount-2 && observation.Transition == "terminal_before_shutdown" && observation.SourceSequence == raw.Manifest.Accepted:
		case index == wantCount-1 && observation.Transition == "terminal_after_shutdown" && observation.SourceSequence == raw.Manifest.Accepted:
		default:
			return errors.New("Dota observation order mismatch")
		}
	}
	recordingSHA, recordingBytes, err := singleRecordingIdentity(filepath.Join(root, "recordings"))
	if err != nil || recordingSHA != harness.RecordingSHA256 || recordingBytes != harness.RecordingBytes {
		return errors.New("recording identity mismatch")
	}
	validationPath := filepath.Join(root, "evidence/canonical/live-validation.json")
	validationSHA, _, err := fileSHA(validationPath)
	if err != nil || validationSHA != harness.ValidationSHA256 {
		return errors.New("validation identity mismatch")
	}
	validationData, err := readBoundedEvidence(validationPath, MaxRehearsalArtifactBytes)
	if err != nil {
		return err
	}
	var validation attemptValidation
	if strictRehearsalJSON(validationData, &validation) != nil || validation.RecoveryCursorSHA256 != harness.RecoverySHA256 || !validation.RecordingFinalized || !validation.Reconciled || !validation.NoCacheRecovery || !validation.MeasurementsPassed || !validation.VisibilityPassed || !validation.OperatorComplete || !validation.OperatorInputBounds {
		return errors.New("validation/recovery contract mismatch")
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
	if _, err = readBoundedEvidence(filepath.Join(lease.abs, "evidence/canonical/terminal.json"), MaxRehearsalArtifactBytes); err == nil || !os.IsNotExist(err) {
		return RehearsalTerminalReceiptV1{}, errors.New("attempt is non-resumable")
	}
	if config.Request.SchemaVersion != RehearsalAttemptSchemaVersion {
		return RehearsalTerminalReceiptV1{}, errors.New("attempt request schema mismatch")
	}
	requestPayload, _ := canonical(config.Request)
	if err = writePrivate(filepath.Join(lease.abs, "input/attempt-request.json"), requestPayload); err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	var owner RehearsalCaptureOwnerV1
	ownerData, _ := readBoundedEvidence(filepath.Join(lease.abs, "capture/owner.json"), MaxRehearsalArtifactBytes)
	if strictRehearsalJSON(ownerData, &owner) != nil {
		return RehearsalTerminalReceiptV1{}, errors.New("capture owner invalid")
	}
	if readiness.Ready {
		produceRehearsalEvidence(ctx, lease.abs, config.RepoRoot, owner, config.Request.ExpectedMatchID)
	} else {
		writeProducerReceipt(lease.abs, RehearsalProducerReceiptV1{SchemaVersion: "public_match_rehearsal_producer_receipt.v2", SessionID: owner.SessionID, State: "not_started", FailureCode: "preflight_refused"})
	}
	rebuilt, err := rebuildRehearsal(lease.abs, config.RepoRoot, readiness, config.Request)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	writes := map[string][]byte{"evidence/canonical/raw-manifest.json": rebuilt.Manifest, "evidence/canonical/suppression-evidence.json": rebuilt.Suppression, "evidence/canonical/terminal.json": rebuilt.Terminal, "evidence/terminal-receipt.json": rebuilt.Receipt}
	if len(rebuilt.Coverage) > 0 {
		writes["evidence/canonical/field-coverage-delta.json"] = rebuilt.Coverage
	}
	for path, data := range writes {
		if err = writePrivate(filepath.Join(lease.abs, filepath.FromSlash(path)), data); err != nil {
			return RehearsalTerminalReceiptV1{}, err
		}
	}
	return rebuilt.ReceiptValue, nil
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
	requestData, err := readBoundedEvidence(filepath.Join(lease.abs, "input/attempt-request.json"), MaxRehearsalArtifactBytes)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	var request RehearsalAttemptRequestV1
	if strictRehearsalJSON(requestData, &request) != nil {
		return RehearsalTerminalReceiptV1{}, errors.New("attempt request invalid")
	}
	rebuilt, err := rebuildRehearsal(lease.abs, repo, readiness, request)
	if err != nil {
		return RehearsalTerminalReceiptV1{}, err
	}
	comparisons := map[string][]byte{"evidence/canonical/raw-manifest.json": rebuilt.Manifest, "evidence/canonical/suppression-evidence.json": rebuilt.Suppression, "evidence/canonical/terminal.json": rebuilt.Terminal, "evidence/terminal-receipt.json": rebuilt.Receipt}
	if len(rebuilt.Coverage) > 0 {
		comparisons["evidence/canonical/field-coverage-delta.json"] = rebuilt.Coverage
	}
	for path, want := range comparisons {
		got, readErr := readBoundedEvidence(filepath.Join(lease.abs, filepath.FromSlash(path)), MaxRehearsalArtifactBytes)
		if readErr != nil || !bytes.Equal(got, want) {
			return RehearsalTerminalReceiptV1{}, fmt.Errorf("retained-input regeneration mismatch: %s", path)
		}
	}
	return rebuilt.ReceiptValue, nil
}
func CleanupRehearsal(root, repo, token string) error {
	receipt, err := VerifyRehearsalTerminal(context.Background(), root, repo)
	if err != nil {
		return fmt.Errorf("cleanup requires verified terminal: %w", err)
	}
	if token == "" || token != receipt.TerminalSHA256 {
		return errors.New("cleanup token must equal verified terminal SHA-256")
	}
	lease, err := acquireExistingRoot(root, repo)
	if err != nil {
		return err
	}
	defer lease.Close()
	return lease.removeAll()
}
func unavailableArtifactSHA() string {
	sum := sha256.Sum256([]byte("unavailable_for_public_match"))
	return hex.EncodeToString(sum[:])
}
