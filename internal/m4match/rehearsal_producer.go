package m4match

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const rehearsalProducerReceiptSchema = "public_match_rehearsal_producer_receipt.v1"

type rehearsalProducerReceipt struct {
	SchemaVersion string `json:"schema_version"`
	SessionID     string `json:"session_id"`
	Outcome       string `json:"outcome"`
	FailureCode   string `json:"failure_code,omitempty"`
	HarnessSHA256 string `json:"harness_sha256,omitempty"`
	SealSHA256    string `json:"capture_seal_sha256,omitempty"`
}

type rehearsalComponentResult struct {
	ProductIdentity processCorrelationIdentity
	DotaIdentity    LiveIdentity
	DotaProcess     []RehearsalProcessObservationV1
	DotaTerminal    []RehearsalProcessObservationV1
	Validation      attemptValidation
	Artifacts       []Artifact
	RecoveryFirst   recoveryProof
	RecoverySecond  recoveryProof
	ProductClean    bool
	OBSClean        bool
}

// rehearsalRunComponents is the only test seam. It is unexported and receives
// no caller-selected evidence paths or asserted facts. Runtime always uses the
// accepted product/OBS/session lifecycle below; package tests may replace the
// physical processes while exercising the same sealing and terminal path.
var rehearsalRunComponents = runAcceptedRehearsalComponents

func acquireRehearsalDotaProcess(procRoot string) (LiveIdentity, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return LiveIdentity{}, err
	}
	pids := make([]int, 0, 1)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || !entry.IsDir() {
			continue
		}
		comm, readErr := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if readErr == nil && strings.ToLower(strings.TrimSpace(string(comm))) == "dota2" {
			pids = append(pids, pid)
		}
	}
	if len(pids) != 1 {
		return LiveIdentity{}, errors.New("exactly one manually started Dota process is required")
	}
	observed, err := readProcessCorrelationIdentity(procRoot, pids[0])
	if err != nil {
		return LiveIdentity{}, err
	}
	identity := LiveIdentity{DotaPID: observed.PID, DotaExecutableSHA256: observed.ExecutableSHA256, DotaProcessStartTicks: observed.StartTicks}
	if _, _, err := verifyDotaProcessAt(procRoot, identity); err != nil {
		return LiveIdentity{}, err
	}
	return identity, nil
}

func observeRehearsalDotaProcess(procRoot string, identity LiveIdentity, sequence uint64, state string) (RehearsalProcessObservationV1, error) {
	observed, pathSHA, err := verifyDotaProcessAt(procRoot, identity)
	if err != nil {
		return RehearsalProcessObservationV1{}, err
	}
	return RehearsalProcessObservationV1{SourceSequence: sequence, PID: observed.PID, Comm: observed.Comm, ExecutablePathSHA256: pathSHA, ExecutableSHA256: observed.ExecutableSHA256, StartTicks: observed.StartTicks, State: state}, nil
}

func produceRehearsalEvidence(ctx context.Context, root, repo string, owner RehearsalCaptureOwnerV1, expectedMatchID string) rehearsalProducerReceipt {
	receipt := rehearsalProducerReceipt{SchemaVersion: rehearsalProducerReceiptSchema, SessionID: owner.SessionID, Outcome: "failed"}
	if expectedMatchID == "" {
		receipt.FailureCode = "expected_identity_required_for_physical_producer"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	result, err := rehearsalRunComponents(ctx, root, repo, owner, expectedMatchID)
	if err != nil {
		receipt.FailureCode = "producer_component_failure"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	rawPath := filepath.Join(root, filepath.FromSlash(owner.RawRelativePath))
	if os.Chmod(rawPath, 0o400) != nil {
		receipt.FailureCode = "producer_raw_seal_failure"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	raw := scanRehearsalRawPath(rawPath, owner.SessionID)
	if raw.Manifest.FailureCode != "" || raw.Manifest.Accepted == 0 {
		receipt.FailureCode = "producer_raw_failure"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	harness, err := deriveProducerHarness(root, owner, raw.Manifest, result)
	if err != nil {
		receipt.FailureCode = "producer_derivation_failure"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	harnessPayload, err := canonical(harness)
	if err != nil || writePrivate(filepath.Join(root, filepath.FromSlash(owner.HarnessRelativePath)), harnessPayload) != nil {
		receipt.FailureCode = "producer_harness_seal_failure"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	ownerPayload, _ := readBoundedFile(filepath.Join(root, "capture/owner.json"), MaxRehearsalArtifactBytes)
	seal := RehearsalCaptureSealV1{
		SchemaVersion: "public_match_rehearsal_capture_seal.v1", CaptureOwnerSHA256: payloadSHA(ownerPayload),
		RawSHA256: raw.Manifest.RawSHA256, RawBytes: raw.Manifest.RawBytes, HarnessSHA256: payloadSHA(harnessPayload),
	}
	sealPayload, _ := canonical(seal)
	if writePrivate(filepath.Join(root, filepath.FromSlash(owner.SealRelativePath)), sealPayload) != nil {
		receipt.FailureCode = "producer_capture_seal_failure"
		writeRehearsalProducerReceipt(root, receipt)
		return receipt
	}
	receipt.Outcome, receipt.HarnessSHA256, receipt.SealSHA256 = "sealed", payloadSHA(harnessPayload), payloadSHA(sealPayload)
	writeRehearsalProducerReceipt(root, receipt)
	return receipt
}

func writeRehearsalProducerReceipt(root string, receipt rehearsalProducerReceipt) {
	payload, _ := canonical(receipt)
	_ = writePrivate(filepath.Join(root, "evidence/producer-receipt.json"), payload)
}

func runAcceptedRehearsalComponents(ctx context.Context, root, repo string, owner RehearsalCaptureOwnerV1, expectedMatchID string) (result rehearsalComponentResult, retErr error) {
	var err error
	result.DotaIdentity, err = acquireRehearsalDotaProcess("/proc")
	if err != nil {
		return result, err
	}
	binary := filepath.Join(root, "application/dota2-ob")
	if hash, _, err := fileSHA(binary); err != nil || hash == "" {
		return result, errors.New("preflight-owned product binary is unavailable")
	}
	installedConfig, err := installGSIConfig(root)
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, os.Remove(installedConfig)) }()
	proxy, err := startDeliveryProxy(root)
	if err != nil {
		return result, err
	}
	proxyStopped := false
	defer func() {
		if !proxyStopped {
			retErr = errors.Join(retErr, proxy.stop())
		}
	}()
	productLog, err := rootOpenFile(filepath.Join(root, "evidence/logs/product-live.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return result, err
	}
	defer productLog.Close()
	tokenPath := filepath.Join(root, "runtime/operator.token")
	product := exec.CommandContext(ctx, binary,
		"--policy-mode", "v3-live-only", "--data-dir", filepath.Join(root, "data/sessions"), "--session-id", owner.SessionID,
		"--addr", CaptureAddress, "--delivery-addr", ProductDeliveryAddress, "--operator-token-file", tokenPath,
		"--history-binding-file", filepath.Join(root, "config/policy/history_availability_binding_v1.json"),
		"--live-only-lineage-file", filepath.Join(root, "config/policy/policy_lineage_manifest_v3.json"),
		"--live-only-release-file", filepath.Join(root, "config/policy/live_only_release_binding_v1.json"))
	product.Stdout, product.Stderr = productLog, productLog
	if err := product.Start(); err != nil {
		return result, err
	}
	productDone := make(chan error, 1)
	go func() { productDone <- product.Wait() }()
	productStopped := false
	defer func() {
		if !productStopped {
			retErr = errors.Join(retErr, stopProcess(product, productDone, 15*time.Second))
		}
	}()
	if err := waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second); err != nil {
		return result, err
	}
	if err := proveProductListener(CaptureAddress, product.Process.Pid); err != nil {
		return result, err
	}
	result.ProductIdentity, err = readProcessCorrelationIdentity("/proc", product.Process.Pid)
	if err != nil {
		return result, err
	}
	obsLog, err := rootOpenFile(filepath.Join(root, "evidence/logs/obs-live.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return result, err
	}
	defer obsLog.Close()
	obs := exec.CommandContext(ctx, "flatpak", "run", "--filesystem="+root, "--env=XDG_CONFIG_HOME="+filepath.Join(root, "config"), "--env=XDG_DATA_HOME="+filepath.Join(root, "data"), "--env=XDG_CACHE_HOME="+filepath.Join(root, "cache"), "com.obsproject.Studio", "--profile", "DOT65-P4", "--collection", "DOT65-P4", "--startrecording")
	obs.Dir, obs.Stdout, obs.Stderr = root, obsLog, obsLog
	if err := obs.Start(); err != nil {
		return result, err
	}
	obsDone := make(chan error, 1)
	go func() { obsDone <- obs.Wait() }()
	obsStopped := false
	defer func() {
		if !obsStopped {
			retErr = errors.Join(retErr, stopProcess(obs, obsDone, 30*time.Second))
		}
	}()
	if _, err := waitStableProcessCorrelation(ctx, os.Getpid(), product.Process.Pid, obs.Process.Pid, 20*time.Second); err != nil {
		return result, err
	}
	samples, err := rootOpenFile(filepath.Join(root, "evidence/samples.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return result, err
	}
	defer samples.Close()
	visibility, err := rootOpenFile(filepath.Join(root, "evidence/visibility.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return result, err
	}
	defer visibility.Close()
	rawPath := filepath.Join(root, filepath.FromSlash(owner.RawRelativePath))
	deadline := time.NewTimer(MaxArmingWindow)
	defer deadline.Stop()
	sampleTicker := time.NewTicker(5 * time.Second)
	defer sampleTicker.Stop()
	visibilityTicker := time.NewTicker(100 * time.Millisecond)
	defer visibilityTicker.Stop()
	completed := false
	lastDotaSequence := uint64(0)
	for !completed {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline.C:
			return result, errors.New("rehearsal producer timed out")
		case err := <-productDone:
			return result, fmt.Errorf("product exited before terminal capture: %w", err)
		case <-sampleTicker.C:
			sample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, tokenPath, root, owner.SessionID, expectedMatchID)
			if json.NewEncoder(samples).Encode(sample) != nil || samples.Sync() != nil {
				return result, errors.New("resource sample persistence failed")
			}
			raw := scanRehearsalRawPath(rawPath, owner.SessionID)
			for sequence := lastDotaSequence + 1; sequence <= raw.Manifest.Accepted; sequence++ {
				observation, identityErr := observeRehearsalDotaProcess("/proc", result.DotaIdentity, sequence, "running")
				if identityErr != nil {
					return result, identityErr
				}
				result.DotaProcess = append(result.DotaProcess, observation)
				lastDotaSequence = sequence
			}
			facts := deriveRawFacts(raw)
			if facts.MatchID != "" && facts.MatchID != expectedMatchID {
				return result, errors.New("observed match identity differs from expected selector")
			}
			completed = facts.EntryBeforeZero && facts.NormalPostGame && facts.WinnerObserved && facts.Continuous && facts.ClockOrdered
		case <-visibilityTicker.C:
			observation := collectVisibility(rawPath, tokenPath)
			if json.NewEncoder(visibility).Encode(observation) != nil || visibility.Sync() != nil || !observation.TelemetryComplete {
				return result, errors.New("overlay fail-closed observation failed")
			}
		}
	}
	terminalSequence := lastDotaSequence
	if terminalSequence == 0 {
		terminalSequence = 1
	}
	terminalCapture, err := observeRehearsalDotaProcess("/proc", result.DotaIdentity, terminalSequence, "terminal_capture")
	if err != nil {
		return result, err
	}
	result.DotaTerminal = append(result.DotaTerminal, terminalCapture)
	if _, err := captureFinalEndpoints(root, tokenPath); err != nil {
		return result, err
	}
	productErr := stopProcess(product, productDone, 15*time.Second)
	obsErr := stopProcess(obs, obsDone, 30*time.Second)
	productStopped, obsStopped = true, true
	proxyErr := proxy.stop()
	proxyStopped = true
	terminalShutdown, err := observeRehearsalDotaProcess("/proc", result.DotaIdentity, terminalSequence, "terminal_shutdown")
	if err != nil {
		return result, err
	}
	result.DotaTerminal = append(result.DotaTerminal, terminalShutdown)
	result.ProductClean, result.OBSClean = productErr == nil, obsErr == nil
	if proxyErr != nil {
		return result, proxyErr
	}
	result.Validation, result.Artifacts, result.RecoveryFirst, err = validateCompletedAttemptWithProof(root, owner.SessionID, result.ProductClean, result.OBSClean)
	if err != nil {
		return result, err
	}
	for _, relative := range []string{"evidence/recovery-work", "evidence/recovery-input"} {
		if err := os.RemoveAll(filepath.Join(root, relative)); err != nil {
			return result, err
		}
	}
	var secondArtifacts []Artifact
	result.RecoverySecond, secondArtifacts, err = performRawOnlyRecovery(ctx, root, owner.SessionID)
	if err != nil {
		return result, err
	}
	kept := result.Artifacts[:0]
	for _, artifact := range result.Artifacts {
		if !strings.HasPrefix(artifact.Path, "evidence/recovery-") {
			kept = append(kept, artifact)
		}
	}
	result.Artifacts = append(kept, secondArtifacts...)
	return result, nil
}

func deriveProducerHarness(root string, owner RehearsalCaptureOwnerV1, manifest RehearsalRawManifestV1, result rehearsalComponentResult) (RehearsalHarnessEvidenceV1, error) {
	if !result.ProductClean || !result.OBSClean || result.ProductIdentity.StartTicks == 0 || result.ProductIdentity.ExecutableSHA256 == "" || result.DotaIdentity.DotaPID <= 0 || result.DotaIdentity.DotaProcessStartTicks == 0 || !validSHA256(result.DotaIdentity.DotaExecutableSHA256) {
		return RehearsalHarnessEvidenceV1{}, errors.New("producer processes did not close cleanly")
	}
	harness := RehearsalHarnessEvidenceV1{SchemaVersion: "public_match_rehearsal_harness_evidence.v2", SessionID: owner.SessionID, RawSHA256: manifest.RawSHA256, PartialRecordings: 0, Process: result.DotaProcess, TerminalProcess: result.DotaTerminal}
	recordings, _ := filepath.Glob(filepath.Join(root, "recordings", "*.mkv"))
	if len(recordings) != 1 {
		return harness, errors.New("producer did not finalize exactly one recording")
	}
	recordingSHA256, recordingBytes, _ := fileSHA(recordings[0])
	harness.RecordingSHA256 = recordingSHA256
	harness.RecordingBytes = uint64(recordingBytes)
	actions := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	position := 0
	for _, terminal := range result.Validation.OperatorTerminals {
		if position < len(actions) && terminal.Action == actions[position] {
			payload, _ := canonical(terminal)
			harness.Operator = append(harness.Operator, RehearsalOperatorObservationV1{Ordinal: uint8(position + 1), Action: terminal.Action, SourceSequence: manifest.Accepted, AuditSHA256: payloadSHA(payload)})
			position++
		}
	}
	if position != len(actions) {
		return harness, errors.New("producer operator journal is incomplete")
	}
	if err := appendProducerSamples(root, manifest, &harness); err != nil {
		return harness, err
	}
	harness.ProjectedCount, harness.PolicyCount, harness.AuditCount, harness.OverlayCount = result.Validation.CursorSequence, result.Validation.ObservationCommits, result.Validation.RawCount, result.Validation.RawCount
	validationPayload, _ := canonical(result.Validation)
	harness.ReconciliationSHA256 = payloadSHA(validationPayload)
	firstIdentity := recoveryFunctionalIdentity(result.RecoveryFirst)
	secondIdentity := recoveryFunctionalIdentity(result.RecoverySecond)
	harness.RecoveryInputSHA256 = manifest.RawSHA256
	harness.RecoveryFirstSHA256, harness.RecoverySecondSHA256 = firstIdentity, secondIdentity
	harness.RecoveryStartTicks1, harness.RecoveryStartTicks2 = result.RecoveryFirst.StartTicks, result.RecoverySecond.StartTicks
	harness.ProductExitCode, harness.OBSExitCode = 0, 0
	harness.Artifacts = collectProducerArtifacts(root, result.Artifacts, recordings[0])
	artifactPayload, _ := canonical(harness.Artifacts)
	harness.ConfinementSHA256 = payloadSHA(artifactPayload)
	return harness, nil
}

func appendProducerSamples(root string, manifest RehearsalRawManifestV1, harness *RehearsalHarnessEvidenceV1) error {
	file, err := os.Open(filepath.Join(root, "evidence/samples.jsonl"))
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var sample Sample
		if json.Unmarshal(scanner.Bytes(), &sample) != nil || !sample.TelemetryComplete || sample.Sequence == 0 || sample.Sequence > manifest.Accepted {
			return errors.New("producer resource sample is invalid")
		}
		harness.Resources = append(harness.Resources, RehearsalResourceObservationV1{SourceSequence: sample.Sequence, IntervalMS: 5000, ProductRSS: uint64(sample.ProcessRSSBytes), OBSRSS: uint64(sample.OBSProcessRSSBytes)})
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	visibility, err := os.Open(filepath.Join(root, "evidence/visibility.jsonl"))
	if err != nil {
		return err
	}
	defer visibility.Close()
	scanner = bufio.NewScanner(visibility)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var sample VisibilitySample
		if json.Unmarshal(scanner.Bytes(), &sample) != nil || !sample.TelemetryComplete || sample.RawSequence == 0 || sample.RawSequence > manifest.Accepted {
			return errors.New("producer visibility sample is invalid")
		}
		if sample.Visibility == "hidden" {
			harness.FailClosed = append(harness.FailClosed, RehearsalFailClosedObservationV1{SourceSequence: sample.RawSequence, ElapsedMS: 100, CandidateClaim: sample.ClaimPresent, OverlayClaim: sample.ClaimPresent, DisplayClaim: sample.ClaimPresent})
		}
	}
	return scanner.Err()
}

func recoveryFunctionalIdentity(proof recoveryProof) string {
	payload, _ := canonical(struct{ Cursor, Policy, Audit, Operator, Overlay string }{proof.CursorSHA256, proof.PolicySHA256, proof.AuditSHA256, proof.OperatorSHA256, proof.OverlaySHA256})
	return payloadSHA(payload)
}

func collectProducerArtifacts(root string, base []Artifact, recording string) []Artifact {
	byPath := make(map[string]Artifact, len(base)+5)
	for _, artifact := range base {
		byPath[artifact.Path] = artifact
	}
	for _, relative := range []string{"evidence/samples.jsonl", "evidence/visibility.jsonl", "evidence/raw-operator-input.jsonl", "evidence/canonical/live-validation.json"} {
		if hash, size, err := fileSHA(filepath.Join(root, filepath.FromSlash(relative))); err == nil {
			byPath[relative] = Artifact{Path: relative, SHA256: hash, Bytes: size}
		}
	}
	if hash, size, err := fileSHA(recording); err == nil {
		relative := filepath.ToSlash(strings.TrimPrefix(recording, root+string(os.PathSeparator)))
		byPath[relative] = Artifact{Path: relative, SHA256: hash, Bytes: size}
	}
	artifacts := make([]Artifact, 0, len(byPath))
	for _, artifact := range byPath {
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts
}

func validateProducerArtifactManifest(root string, harness RehearsalHarnessEvidenceV1) error {
	if len(harness.Artifacts) == 0 {
		return errors.New("producer artifact manifest is empty")
	}
	prior := ""
	for _, artifact := range harness.Artifacts {
		clean := filepath.Clean(filepath.FromSlash(artifact.Path))
		if artifact.Path == "" || clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || artifact.Path <= prior || !validSHA256(artifact.SHA256) || artifact.Bytes <= 0 {
			return errors.New("producer artifact identity is invalid")
		}
		hash, size, err := fileSHA(filepath.Join(root, clean))
		if err != nil || hash != artifact.SHA256 || size != artifact.Bytes || size > MaxRehearsalArtifactBytes && !strings.HasPrefix(artifact.Path, "recordings/") {
			return errors.New("producer artifact content mismatch")
		}
		prior = artifact.Path
	}
	payload, _ := canonical(harness.Artifacts)
	if payloadSHA(payload) != harness.ConfinementSHA256 {
		return errors.New("producer artifact confinement mismatch")
	}
	return nil
}

var _ = syscall.SIGTERM
