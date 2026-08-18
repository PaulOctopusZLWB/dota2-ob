package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const (
	rehearsalProducerSchemaV1 = "rehearsal_producer_evidence.v1"
	maxProducerArtifacts      = 32
	maxProducerArtifactBytes  = 16 << 30
)

var requiredProducerSteps = [...]string{
	"build_product", "start_product", "product_ready", "start_obs", "obs_ready",
	"capture", "operator_control", "resource_sampling", "finalize_obs", "shutdown_product",
	"shutdown_obs", "raw_reconciliation", "no_cache_recovery", "seal_evidence",
}

type rehearsalLifecycleContext struct {
	root      string
	repo      string
	preflight RehearsalPreflightV1
	boundDota RehearsalDotaIdentityV1
	identity  func() (RehearsalDotaIdentityV1, error)
}

type rehearsalLifecycleReport struct {
	sourceMode             string
	physicalMatch          bool
	steps                  []RehearsalProducerStepV1
	productPID             int
	obsPID                 int
	obsInstanceID          string
	obsExecutable          RehearsalOwnedProcessIdentityV1
	obsStartCorrelation    string
	obsTerminalCorrelation string
	recoveryPID            int
	dotaContinuity         []RehearsalProcessObservationV1
	recoveryProcess        []RehearsalOwnedProcessObservationV1
	operatorActions        []string
	resourceSamples        uint64
	visibilitySamples      uint64
	rawRecords             uint64
	recordingFinalized     bool
	reconciled             bool
	recoveryByteEqual      bool
	cleanShutdown          bool
}

type rehearsalLifecycleDriver interface {
	Run(context.Context, rehearsalLifecycleContext) (rehearsalLifecycleReport, error)
}

type executableRehearsalDriver struct{}
type closedRehearsalTestDriver struct{}

func (closedRehearsalTestDriver) Run(context.Context, rehearsalLifecycleContext) (rehearsalLifecycleReport, error) {
	return rehearsalLifecycleReport{sourceMode: "hermetic_helper", steps: []RehearsalProducerStepV1{{Name: "seal_evidence", Started: true, Completed: false, ExitCode: 1, Failure: "test lifecycle driver not supplied"}}}, errors.New("test lifecycle driver not supplied")
}

func executeRehearsalProducer(ctx context.Context, root, repo string, preflight RehearsalPreflightV1, bound RehearsalDotaIdentityV1, identity func() (RehearsalDotaIdentityV1, error), driver rehearsalLifecycleDriver) (RehearsalProducerEvidenceV1, error) {
	producerDir := filepath.Join(root, "rehearsal", "producer")
	preexisting := false
	if info, err := os.Lstat(producerDir); err == nil {
		if !info.IsDir() {
			return RehearsalProducerEvidenceV1{}, errors.New("producer evidence path is not a directory")
		}
		entries, readErr := os.ReadDir(producerDir)
		if readErr != nil {
			return RehearsalProducerEvidenceV1{}, readErr
		}
		preexisting = len(entries) != 0
	} else if !os.IsNotExist(err) {
		return RehearsalProducerEvidenceV1{}, errors.New("producer evidence path cannot be inspected")
	}
	if err := rootMkdirAll(producerDir, 0o700); err != nil {
		return RehearsalProducerEvidenceV1{}, err
	}
	if driver == nil {
		driver = executableRehearsalDriver{}
	}
	var report rehearsalLifecycleReport
	var runErr error
	if preexisting {
		report = rehearsalLifecycleReport{sourceMode: "physical_public_match", steps: []RehearsalProducerStepV1{{Name: "seal_evidence", Started: true, ExitCode: 1, Failure: "preexisting producer content"}}}
		runErr = errors.New("preexisting producer content rejected")
	} else {
		report, runErr = driver.Run(ctx, rehearsalLifecycleContext{root: root, repo: repo, preflight: preflight, boundDota: bound, identity: identity})
	}
	evidence := RehearsalProducerEvidenceV1{
		SchemaVersion: rehearsalProducerSchemaV1, Purpose: RehearsalPurpose, SessionID: preflight.SessionID,
		PreflightSHA256: preflight.PreflightSHA256, RootOwnerSHA256: preflight.RootOwnerSHA256,
		SourceMode: report.sourceMode, Steps: report.steps, ProductPID: report.productPID, OBSPID: report.obsPID,
		OBSInstanceID: report.obsInstanceID, OBSExecutable: report.obsExecutable,
		OBSStartCorrelation: report.obsStartCorrelation, OBSTerminalCorrelation: report.obsTerminalCorrelation,
		RecoveryPID: report.recoveryPID, OperatorActions: append([]string(nil), report.operatorActions...),
		DotaContinuity:  append([]RehearsalProcessObservationV1(nil), report.dotaContinuity...),
		RecoveryProcess: append([]RehearsalOwnedProcessObservationV1(nil), report.recoveryProcess...),
		ResourceSamples: report.resourceSamples, VisibilitySamples: report.visibilitySamples, RawRecords: report.rawRecords,
		RecordingFinalized: report.recordingFinalized, Reconciled: report.reconciled,
		RecoveryByteEqual: report.recoveryByteEqual, CleanShutdown: report.cleanShutdown, PhysicalMatch: report.physicalMatch,
	}
	evidence.RunID, _ = contracts.CanonicalSHA256(struct {
		Purpose   string `json:"purpose"`
		Session   string `json:"session"`
		Preflight string `json:"preflight"`
		Owner     string `json:"owner"`
	}{RehearsalPurpose, preflight.SessionID, preflight.PreflightSHA256, preflight.RootOwnerSHA256})
	evidence.Artifacts = collectProducerArtifacts(root, preflight.SessionID)
	evidence.ContentSHA256, _ = producerEvidenceContentID(evidence)
	if err := writeJSON(filepath.Join(producerDir, "evidence.json"), evidence, 0o600); err != nil {
		return evidence, errors.Join(runErr, err)
	}
	if err := writePrivate(filepath.Join(producerDir, "evidence.sha256"), []byte(evidence.ContentSHA256+"\n")); err != nil {
		return evidence, errors.Join(runErr, err)
	}
	if validateErr := validateProducerEvidence(root, evidence, false); validateErr != nil {
		runErr = errors.Join(runErr, validateErr)
	}
	return evidence, runErr
}

func (executableRehearsalDriver) Run(ctx context.Context, run rehearsalLifecycleContext) (report rehearsalLifecycleReport, retErr error) {
	report.sourceMode, report.physicalMatch = "physical_public_match", true
	startIdentity, err := run.identity()
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "producer_start", ObservedIdentity: startIdentity})
	if err != nil || !sameRehearsalDotaIdentity(run.boundDota, startIdentity) {
		return report, errors.New("dota identity changed at producer start")
	}
	step := func(name string) *RehearsalProducerStepV1 {
		report.steps = append(report.steps, RehearsalProducerStepV1{Name: name, Started: true, ExitCode: -1})
		return &report.steps[len(report.steps)-1]
	}
	complete := func(value *RehearsalProducerStepV1, err error) {
		value.Completed, value.ExitCode = err == nil, 0
		if err != nil {
			value.ExitCode, value.Failure = 1, boundedFailure(err)
		}
	}

	build := step("build_product")
	for _, directory := range []string{"application", "config/policy", "evidence/logs", "evidence/canonical", "recordings", "runtime"} {
		if err := rootMkdirAll(filepath.Join(run.root, directory), 0o700); err != nil {
			complete(build, err)
			return report, err
		}
	}
	if _, err := Prepare(run.root, 1920, 1080); err != nil {
		complete(build, err)
		return report, err
	}
	if err := prepareRehearsalOBSProfile(run.root); err != nil {
		complete(build, err)
		return report, err
	}
	goHash, _, err := fileSHA(run.preflight.GoExecutable)
	if err != nil || goHash != run.preflight.GoExecutableSHA256 {
		err = errors.New("bound Go executable identity changed")
		complete(build, err)
		return report, err
	}
	history, lineage, release, err := rehearsalSuccessorArtifacts(run.preflight.SessionID)
	if err == nil {
		err = writeJSON(filepath.Join(run.root, "config/policy/history_availability_binding_v1.json"), history, 0o600)
	}
	if err == nil {
		err = writeJSON(filepath.Join(run.root, "config/policy/policy_lineage_manifest_v3.json"), lineage, 0o600)
	}
	if err == nil {
		err = writeJSON(filepath.Join(run.root, "config/policy/live_only_release_binding_v1.json"), release, 0o600)
	}
	binary := filepath.Join(run.root, "application", "dota2-ob")
	if err == nil {
		command := exec.CommandContext(ctx, run.preflight.GoExecutable, "build", "-trimpath", "-buildvcs=false", "-o", binary, "./cmd/dota2-ob")
		command.Dir = run.repo
		err = command.Run()
	}
	complete(build, err)
	if err != nil {
		return report, err
	}

	proxy, err := startDeliveryProxy(run.root)
	if err != nil {
		return report, err
	}
	defer func() { retErr = errors.Join(retErr, proxy.stop()) }()
	productLog, err := rootOpenFile(filepath.Join(run.root, "evidence/logs/product-live.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return report, err
	}
	defer productLog.Close()
	productStep := step("start_product")
	product := exec.CommandContext(ctx, binary,
		"--policy-mode", "v3-live-only", "--data-dir", filepath.Join(run.root, "data/sessions"), "--session-id", run.preflight.SessionID,
		"--addr", CaptureAddress, "--delivery-addr", ProductDeliveryAddress, "--operator-token-file", filepath.Join(run.root, "runtime/operator.token"),
		"--history-binding-file", filepath.Join(run.root, "config/policy/history_availability_binding_v1.json"),
		"--live-only-lineage-file", filepath.Join(run.root, "config/policy/policy_lineage_manifest_v3.json"),
		"--live-only-release-file", filepath.Join(run.root, "config/policy/live_only_release_binding_v1.json"))
	product.Stdout, product.Stderr = productLog, productLog
	err = product.Start()
	complete(productStep, err)
	if err != nil {
		return report, err
	}
	report.productPID = product.Process.Pid
	productDone := make(chan error, 1)
	go func() { productDone <- product.Wait() }()
	productReady := step("product_ready")
	err = waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second)
	complete(productReady, err)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		return report, err
	}

	obsLog, err := rootOpenFile(filepath.Join(run.root, "evidence/logs/obs-live.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		return report, err
	}
	defer obsLog.Close()
	obsStep := step("start_obs")
	obs, err := startOwnedFlatpakOBS(ctx, run.root, obsLog)
	complete(obsStep, err)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		return report, err
	}
	report.obsPID, report.obsInstanceID, report.obsExecutable = obs.OBSPID, obs.InstanceID, obs.Identity
	obsStopped := false
	defer func() {
		if !obsStopped {
			retErr = errors.Join(retErr, stopOwnedFlatpakOBS(context.Background(), obs, 30*time.Second))
		}
	}()
	obsReady := step("obs_ready")
	correlation, err := waitOwnedFlatpakCorrelation(ctx, product.Process.Pid, obs, 20*time.Second)
	if err == nil {
		report.obsStartCorrelation = correlation.SHA256
		err = waitOBSRecordingStarted(ctx, run.root, 30*time.Second)
	}
	complete(obsReady, err)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		_ = stopOwnedFlatpakOBS(context.Background(), obs, 30*time.Second)
		obsStopped = true
		return report, err
	}

	samples, err := rootOpenFile(filepath.Join(run.root, "evidence/samples.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return report, err
	}
	defer samples.Close()
	visibility, err := rootOpenFile(filepath.Join(run.root, "evidence/visibility.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return report, err
	}
	defer visibility.Close()
	captureStep := step("capture")
	resourceStep := step("resource_sampling")
	rawPath := filepath.Join(run.root, "data/sessions", run.preflight.SessionID, "raw.jsonl")
	timeout := time.NewTimer(MaxArmingWindow)
	defer timeout.Stop()
	resourceTicker := time.NewTicker(5 * time.Second)
	defer resourceTicker.Stop()
	visibilityTicker := time.NewTicker(100 * time.Millisecond)
	defer visibilityTicker.Stop()
	terminal := false
	var admittedRaw uint64
	for !terminal {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			terminal = true
		case <-timeout.C:
			err = errors.New("rehearsal capture timeout")
			terminal = true
		case productErr := <-productDone:
			err = errors.New("product exited before terminal: " + boundedFailure(productErr))
			terminal = true
		case <-resourceTicker.C:
			if _, correlationErr := captureOwnedFlatpakCorrelation(ctx, product.Process.Pid, obs); correlationErr != nil {
				err = correlationErr
				terminal = true
				break
			}
			admittedRaw, err = admitProducerRawIdentities(rawPath, run.preflight.SessionID, run.boundDota, run.identity, admittedRaw, &report.dotaContinuity)
			if err != nil {
				terminal = true
				break
			}
			sample := collectSample(product.Process.Pid, obs.OBSPID, rawPath, filepath.Join(run.root, "runtime/operator.token"), run.root, run.preflight.SessionID, "")
			if encodeErr := json.NewEncoder(samples).Encode(sample); encodeErr != nil {
				err = encodeErr
				terminal = true
			} else {
				_ = samples.Sync()
				report.resourceSamples++
			}
			if !terminal {
				terminal, _ = rehearsalRawReachedNormalPostgame(rawPath, run.preflight.SessionID)
			}
		case <-visibilityTicker.C:
			admittedRaw, err = admitProducerRawIdentities(rawPath, run.preflight.SessionID, run.boundDota, run.identity, admittedRaw, &report.dotaContinuity)
			if err != nil {
				terminal = true
				break
			}
			value := collectVisibility(rawPath, filepath.Join(run.root, "runtime/operator.token"))
			if encodeErr := json.NewEncoder(visibility).Encode(value); encodeErr != nil {
				err = encodeErr
				terminal = true
			} else {
				_ = visibility.Sync()
				report.visibilitySamples++
			}
		}
	}
	if finalCount, finalErr := admitProducerRawIdentities(rawPath, run.preflight.SessionID, run.boundDota, run.identity, admittedRaw, &report.dotaContinuity); finalErr != nil {
		err = errors.Join(err, finalErr)
	} else {
		admittedRaw = finalCount
	}
	captureIdentity, captureIdentityErr := run.identity()
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "capture_terminal", RawSequence: admittedRaw, ObservedIdentity: captureIdentity})
	if captureIdentityErr != nil || !sameRehearsalDotaIdentity(run.boundDota, captureIdentity) {
		err = errors.Join(err, errors.New("dota identity changed at capture terminal"))
	}
	complete(captureStep, err)
	complete(resourceStep, err)
	operatorStep := step("operator_control")
	finalArtifacts, endpointErr := captureFinalEndpoints(run.root, filepath.Join(run.root, "runtime/operator.token"))
	_ = finalArtifacts
	complete(operatorStep, endpointErr)
	terminalCorrelation, terminalCorrelationErr := captureOwnedFlatpakCorrelation(context.Background(), product.Process.Pid, obs)
	if terminalCorrelationErr == nil {
		report.obsTerminalCorrelation = terminalCorrelation.SHA256
	}
	productShutdown := step("shutdown_product")
	productStop := stopProcess(product, productDone, 15*time.Second)
	complete(productShutdown, productStop)
	obsFinalize := step("finalize_obs")
	obsStop := stopOwnedFlatpakOBS(context.Background(), obs, 30*time.Second)
	obsStopped = true
	obsStop = errors.Join(terminalCorrelationErr, obsStop, obsLog.Close())
	complete(obsFinalize, obsStop)
	obsShutdown := step("shutdown_obs")
	complete(obsShutdown, obsStop)
	var recoveryObservations []RehearsalOwnedProcessObservationV1
	validation, _, validationErr := validateCompletedAttemptObserved(run.root, run.preflight.SessionID, productStop == nil, obsStop == nil, func(boundary string, identity RehearsalOwnedProcessIdentityV1) {
		recoveryObservations = append(recoveryObservations, RehearsalOwnedProcessObservationV1{Boundary: boundary, Identity: identity})
	})
	reconcile := step("raw_reconciliation")
	complete(reconcile, boolError(validation.Reconciled, "raw reconciliation failed"))
	recovery := step("no_cache_recovery")
	complete(recovery, boolError(validation.NoCacheRecovery, "raw-only recovery failed"))
	seal := step("seal_evidence")
	complete(seal, nil)
	report.operatorActions = append([]string(nil), validation.OperatorActions...)
	report.rawRecords = validation.RawCount
	report.recordingFinalized = validation.RecordingFinalized
	report.reconciled = validation.Reconciled
	report.recoveryByteEqual = validation.NoCacheRecovery
	report.cleanShutdown = productStop == nil && obsStop == nil && validation.RecoveryCleanShutdown
	report.recoveryProcess = recoveryObservations
	if len(recoveryObservations) > 0 {
		report.recoveryPID = recoveryObservations[0].Identity.PID
	}
	terminalIdentity, terminalIdentityErr := run.identity()
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "producer_terminal", RawSequence: admittedRaw, ObservedIdentity: terminalIdentity})
	if terminalIdentityErr != nil || !sameRehearsalDotaIdentity(run.boundDota, terminalIdentity) {
		retErr = errors.Join(retErr, errors.New("dota identity changed at producer terminal"))
	}
	return report, errors.Join(err, endpointErr, productStop, obsStop, validationErr)
}

func admitProducerRawIdentities(path, sessionID string, bound RehearsalDotaIdentityV1, observe func() (RehearsalDotaIdentityV1, error), admitted uint64, receipts *[]RehearsalProcessObservationV1) (uint64, error) {
	attested, err := session.ReadAttestedRawV1WithGuard(path, sessionID, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, MaxRehearsalRawRecords, func(record *session.Record, rawRecordSHA256 string) error {
		if record.Sequence <= admitted {
			return nil
		}
		if record.Sequence != admitted+1 {
			return errors.New("producer raw identity sequence gap")
		}
		identity, identityErr := observe()
		receipt := RehearsalProcessObservationV1{Boundary: "raw_admission", RawSequence: record.Sequence, RawRecordSHA256: rawRecordSHA256, ObservedIdentity: identity}
		*receipts = append(*receipts, receipt)
		if identityErr != nil || !sameRehearsalDotaIdentity(bound, identity) {
			return errors.New("dota identity changed before producer raw admission")
		}
		admitted = record.Sequence
		return nil
	})
	if err != nil {
		return admitted, err
	}
	if uint64(len(attested.Records)) < admitted {
		return admitted, errors.New("producer raw population regressed")
	}
	return admitted, nil
}

func prepareRehearsalOBSProfile(root string) error {
	path := filepath.Join(root, "config/obs-studio/basic/profiles/DOT65-P4/basic.ini")
	payload, err := rootReadFile(path)
	if err != nil {
		return err
	}
	payload = []byte(strings.Replace(string(payload), "RecFilePath=recordings", "RecFilePath="+filepath.Join(root, "recordings"), 1))
	if err := writePrivate(path, payload); err != nil {
		return err
	}
	return writeJSON(filepath.Join(root, "config/obs-studio/plugin_config/obs-websocket/config.json"), map[string]any{
		"alerts_enabled": false, "auth_required": false, "first_load": false, "server_enabled": true,
		"server_password": "", "server_port": obsWebSocketPort,
	}, 0o600)
}

func rehearsalRawReachedNormalPostgame(path, sessionID string) (bool, error) {
	attested, err := session.ReadAttestedRawV1(path, sessionID, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, MaxRehearsalRawRecords)
	if err != nil {
		return false, err
	}
	seenNegative, seenZero := false, false
	for _, record := range attested.Decoded {
		observation, mapErr := capture.MapLiveObservationV1(record)
		if mapErr != nil {
			return false, mapErr
		}
		if observation.Map.ClockTime.State == contracts.ValuePresent && observation.Map.ClockTime.Value != nil {
			seenNegative = seenNegative || *observation.Map.ClockTime.Value < 0
			seenZero = seenZero || (seenNegative && *observation.Map.ClockTime.Value >= 0)
		}
		if seenZero && observation.Map.GameState.State == contracts.ValuePresent && observation.Map.GameState.Value != nil && observation.Map.WinTeam.State == contracts.ValuePresent && observation.Map.WinTeam.Value != nil {
			state := strings.ToLower(*observation.Map.GameState.Value)
			if state == "dota_gamerules_state_post_game" || state == "post_game" {
				return true, nil
			}
		}
	}
	return false, nil
}

func collectProducerArtifacts(root, sessionID string) []RehearsalProducerArtifactV1 {
	roles := map[string]string{
		"product_binary": "application/dota2-ob", "product_log": "evidence/logs/product-live.log", "obs_log": "evidence/logs/obs-live.log",
		"raw_capture": filepath.ToSlash(filepath.Join("data/sessions", sessionID, "raw.jsonl")), "operator_journal": "evidence/raw-operator-input.jsonl",
		"resource_samples": "evidence/samples.jsonl", "visibility_samples": "evidence/visibility.jsonl", "final_status": "evidence/canonical/final-status.json",
		"final_operator": "evidence/canonical/final-operator.json", "final_overlay": "evidence/canonical/final-overlay.json",
		"recovery_raw": filepath.ToSlash(filepath.Join("evidence/recovery-input", sessionID, "raw.jsonl")), "recovery_proof": "evidence/canonical/raw-only-recovery.json",
		"validation": "evidence/canonical/live-validation.json",
	}
	values := make([]RehearsalProducerArtifactV1, 0, len(roles)+1)
	for role, relative := range roles {
		hash, size, err := fileSHA(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			continue
		}
		artifact := RehearsalProducerArtifactV1{Role: role, Path: relative, SHA256: hash, Bytes: size}
		if role == "raw_capture" {
			artifact.Records, _ = readRawSequences(filepath.Join(root, filepath.FromSlash(relative)))
		}
		values = append(values, artifact)
	}
	recordings, _ := filepath.Glob(filepath.Join(root, "recordings", "*.mkv"))
	sort.Strings(recordings)
	if len(recordings) == 1 {
		if hash, size, err := fileSHA(recordings[0]); err == nil {
			rel, _ := filepath.Rel(root, recordings[0])
			values = append(values, RehearsalProducerArtifactV1{Role: "recording", Path: filepath.ToSlash(rel), SHA256: hash, Bytes: size})
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Role < values[j].Role })
	return values
}

func producerEvidenceContentID(value RehearsalProducerEvidenceV1) (string, error) {
	value.ContentSHA256 = ""
	return payloadSHAFromCanonical(value)
}

func validateProducerEvidence(root string, evidence RehearsalProducerEvidenceV1, requireCompletion bool) error {
	if evidence.SchemaVersion != rehearsalProducerSchemaV1 || evidence.Purpose != RehearsalPurpose || evidence.SessionID == "" || !validLowerSHA256(evidence.PreflightSHA256) || !validLowerSHA256(evidence.RootOwnerSHA256) || !validLowerSHA256(evidence.RunID) || (evidence.SourceMode != "physical_public_match" && evidence.SourceMode != "hermetic_helper" && evidence.SourceMode != "obs_only_smoke") || len(evidence.Steps) > len(requiredProducerSteps) || len(evidence.Artifacts) > maxProducerArtifacts {
		return errors.New("producer evidence envelope invalid")
	}
	want, err := producerEvidenceContentID(evidence)
	if err != nil || evidence.ContentSHA256 != want {
		return errors.New("producer evidence content identity mismatch")
	}
	seenSteps := map[string]bool{}
	for _, step := range evidence.Steps {
		if seenSteps[step.Name] {
			return errors.New("duplicate producer step")
		}
		seenSteps[step.Name] = true
	}
	seenRoles := map[string]bool{}
	var total int64
	for _, artifact := range evidence.Artifacts {
		if seenRoles[artifact.Role] || artifact.Role == "" || artifact.Path == "" || filepath.IsAbs(artifact.Path) || !validLowerSHA256(artifact.SHA256) || artifact.Bytes < 0 {
			return errors.New("producer artifact invalid")
		}
		seenRoles[artifact.Role] = true
		total += artifact.Bytes
		if total > maxProducerArtifactBytes {
			return errors.New("producer artifacts exceed bound")
		}
		hash, size, hashErr := fileSHA(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if hashErr != nil || hash != artifact.SHA256 || size != artifact.Bytes {
			return errors.New("producer artifact content changed")
		}
		if artifact.Role == "raw_capture" {
			count, countErr := readRawSequences(filepath.Join(root, filepath.FromSlash(artifact.Path)))
			if (requireCompletion && countErr != nil) || (countErr == nil && count != artifact.Records) {
				return errors.New("producer raw count mismatch")
			}
		}
	}
	if !requireCompletion {
		return nil
	}
	for _, name := range requiredProducerSteps {
		if !seenSteps[name] {
			return errors.New("producer step missing: " + name)
		}
	}
	for _, step := range evidence.Steps {
		if !step.Started || !step.Completed || step.ExitCode != 0 || step.Failure != "" {
			return errors.New("producer step did not complete: " + step.Name)
		}
	}
	for _, role := range []string{"product_binary", "product_log", "obs_log", "raw_capture", "operator_journal", "resource_samples", "visibility_samples", "recording", "final_status", "final_operator", "final_overlay", "recovery_raw", "recovery_proof", "validation"} {
		if !seenRoles[role] {
			return errors.New("producer completion artifact missing: " + role)
		}
	}
	if evidence.SourceMode != "physical_public_match" || !evidence.PhysicalMatch || evidence.ProductPID <= 1 || evidence.OBSPID <= 1 || evidence.OBSInstanceID == "" || evidence.OBSExecutable.PID != evidence.OBSPID || !validOwnedProcessIdentity(evidence.OBSExecutable) || !strings.Contains(strings.ToLower(evidence.OBSExecutable.Comm), "obs") || !validLowerSHA256(evidence.OBSStartCorrelation) || !validLowerSHA256(evidence.OBSTerminalCorrelation) || evidence.RawRecords == 0 || evidence.ResourceSamples == 0 || evidence.VisibilitySamples == 0 || !evidence.RecordingFinalized || !evidence.Reconciled || !evidence.RecoveryByteEqual || !evidence.CleanShutdown {
		return errors.New("producer operational completion facts invalid")
	}
	rawPath := filepath.Join(root, "data", "sessions", evidence.SessionID, "raw.jsonl")
	attested, rawErr := session.ReadAttestedRawV1(rawPath, evidence.SessionID, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, MaxRehearsalRawRecords)
	if rawErr != nil || validateProducerDotaContinuity(evidence.DotaContinuity, RehearsalDotaIdentityV1{}, attested.Records, true) != nil {
		return errors.New("producer-interleaved Dota continuity invalid")
	}
	if err := validateRecoveryProcessPopulation(evidence.RecoveryPID, evidence.RecoveryProcess); err != nil {
		return err
	}
	if validateOperatorActionPopulation(evidence.OperatorActions) != nil {
		return errors.New("producer operator actions invalid")
	}
	return nil
}

func validateProducerDotaContinuity(observations []RehearsalProcessObservationV1, expected RehearsalDotaIdentityV1, records []session.RawRecordAttestationV1, requireCompletion bool) error {
	if len(observations) == 0 {
		if requireCompletion {
			return errors.New("producer Dota continuity absent")
		}
		return nil
	}
	bound := observations[0].ObservedIdentity
	if observations[0].Boundary != "producer_start" || observations[0].RawSequence != 0 || observations[0].RawRecordSHA256 != "" || !validRehearsalIdentity(bound) {
		return errors.New("producer Dota start identity invalid")
	}
	if validRehearsalIdentity(expected) && !sameRehearsalDotaIdentity(expected, bound) {
		return errors.New("producer Dota identity differs from acquired identity")
	}
	if !requireCompletion {
		for _, observation := range observations[1:] {
			if !validRehearsalIdentity(observation.ObservedIdentity) {
				return errors.New("producer Dota observation invalid")
			}
		}
		return nil
	}
	if len(observations) != len(records)+3 {
		return errors.New("producer Dota continuity population mismatch")
	}
	for index, record := range records {
		observation := observations[index+1]
		if observation.Boundary != "raw_admission" || observation.RawSequence != record.Sequence || observation.RawRecordSHA256 != record.RawRecordSHA256 || !sameRehearsalDotaIdentity(bound, observation.ObservedIdentity) {
			return errors.New("producer Dota raw admission binding mismatch")
		}
	}
	for index, boundary := range []string{"capture_terminal", "producer_terminal"} {
		observation := observations[len(records)+1+index]
		if observation.Boundary != boundary || observation.RawSequence != uint64(len(records)) || observation.RawRecordSHA256 != "" || !sameRehearsalDotaIdentity(bound, observation.ObservedIdentity) {
			return errors.New("producer Dota terminal continuity mismatch")
		}
	}
	return nil
}

func validateRecoveryProcessPopulation(pid int, observations []RehearsalOwnedProcessObservationV1) error {
	if pid <= 1 || len(observations) != 2 || observations[0].Boundary != "recovery_start" || observations[1].Boundary != "recovery_terminal" {
		return errors.New("recovery process lifecycle evidence invalid")
	}
	start, terminal := observations[0].Identity, observations[1].Identity
	if !validOwnedProcessIdentity(start) || !sameRehearsalOwnedProcessIdentity(start, terminal) || start.PID != pid {
		return errors.New("recovery process identity mismatch")
	}
	return nil
}

func validOwnedProcessIdentity(value RehearsalOwnedProcessIdentityV1) bool {
	return value.SchemaVersion == "rehearsal_owned_process_identity.v1" && value.PID > 1 && value.Comm != "" && value.ExecutablePath != "" && validLowerSHA256(value.ExecutablePathSHA256) && validLowerSHA256(value.ExecutableSHA256) && value.ProcessStartTicks > 0 && value.ExecutableDevice > 0 && value.ExecutableInode > 0
}

func readProducerEvidence(root string) (RehearsalProducerEvidenceV1, error) {
	var value RehearsalProducerEvidenceV1
	payload, err := rootReadFile(filepath.Join(root, "rehearsal/producer/evidence.json"))
	if err != nil || json.Unmarshal(payload, &value) != nil {
		return value, errors.New("producer evidence unavailable")
	}
	canonicalPayload, canonicalErr := canonical(value)
	seal, sealErr := rootReadFile(filepath.Join(root, "rehearsal/producer/evidence.sha256"))
	if canonicalErr != nil || !strings.EqualFold(string(canonicalPayload), string(payload)) || sealErr != nil || string(seal) != value.ContentSHA256+"\n" {
		return value, errors.New("producer evidence seal mismatch")
	}
	return value, nil
}

func validateOperatorActionPopulation(actions []string) error {
	want := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	if len(actions) != len(want) {
		return errors.New("operator action population mismatch")
	}
	for index := range want {
		if actions[index] != want[index] {
			return errors.New("operator action order mismatch")
		}
	}
	return nil
}

func boolError(ok bool, message string) error {
	if ok {
		return nil
	}
	return errors.New(message)
}
func boundedFailure(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

var _ = fmt.Sprintf
