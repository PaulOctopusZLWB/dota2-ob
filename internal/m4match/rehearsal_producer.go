package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// rehearsalProducerDeps is deliberately private. Tests can replace physical
// process discovery without adding a runtime injection surface to m4-match.
type rehearsalProducerDeps struct {
	procRoot string
	now      func() time.Time
}

var defaultRehearsalProducerDeps = rehearsalProducerDeps{procRoot: "/proc", now: time.Now}

func writeProducerReceipt(root string, receipt RehearsalProducerReceiptV1) error {
	return writeJSON(filepath.Join(root, "evidence/producer-receipt.json"), receipt, 0o600)
}

func produceRehearsalEvidence(ctx context.Context, root, repo string, owner RehearsalCaptureOwnerV1, expectedMatch string) {
	receipt := RehearsalProducerReceiptV1{
		SchemaVersion: "public_match_rehearsal_producer_receipt.v2",
		SessionID:     owner.SessionID,
		State:         "failed",
		FailureCode:   "producer_internal_failure",
	}
	defer func() { _ = writeProducerReceipt(root, receipt) }()

	bound, err := acquireRehearsalDota(defaultRehearsalProducerDeps.procRoot)
	if err != nil {
		receipt.FailureCode = "dota_identity_unavailable"
		return
	}
	observations := make([]RehearsalDotaObservationV1, 0, 16)
	appendObservation := func(sequence uint64, transition string) error {
		observation, observeErr := observeBoundRehearsalDota(defaultRehearsalProducerDeps.procRoot, bound, sequence, transition)
		if observeErr == nil {
			observations = append(observations, observation)
		}
		return observeErr
	}
	if err = appendObservation(0, "acquired"); err != nil {
		receipt.FailureCode = "dota_identity_drift"
		return
	}

	if _, err = Prepare(root, 1920, 1080); err != nil {
		receipt.FailureCode = "obs_configuration_failure"
		return
	}
	installedConfig, err := installGSIConfig(root)
	if err != nil {
		receipt.FailureCode = "gsi_install_failure"
		return
	}
	defer os.Remove(installedConfig)
	proxy, err := startDeliveryProxy(root)
	if err != nil {
		receipt.FailureCode = "delivery_start_failure"
		return
	}
	defer proxy.stop()

	productLog, err := rootOpenFile(filepath.Join(root, "evidence/logs/product-live.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		receipt.FailureCode = "product_log_failure"
		return
	}
	defer productLog.Close()
	product := exec.CommandContext(ctx, filepath.Join(root, "application/dota2-ob"),
		"--policy-mode", "v3-live-only", "--data-dir", filepath.Join(root, "data/sessions"), "--session-id", owner.SessionID,
		"--addr", CaptureAddress, "--delivery-addr", ProductDeliveryAddress, "--operator-token-file", filepath.Join(root, "runtime/operator.token"),
		"--history-binding-file", filepath.Join(root, "config/policy/history_availability_binding_v1.json"),
		"--live-only-lineage-file", filepath.Join(root, "config/policy/policy_lineage_manifest_v3.json"),
		"--live-only-release-file", filepath.Join(root, "config/policy/live_only_release_binding_v1.json"))
	product.Stdout, product.Stderr = productLog, productLog
	if err = product.Start(); err != nil {
		receipt.FailureCode = "product_start_failure"
		return
	}
	productDone := make(chan error, 1)
	go func() { productDone <- product.Wait() }()
	productClean := false
	defer func() {
		if !productClean {
			_ = stopProcess(product, productDone, 15*time.Second)
		}
	}()
	if err = waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second); err != nil || proveProductListener(CaptureAddress, product.Process.Pid) != nil {
		receipt.FailureCode = "product_readiness_failure"
		return
	}
	if err = appendObservation(0, "product_ready"); err != nil {
		receipt.FailureCode = "dota_identity_drift"
		return
	}

	obsLog, err := rootOpenFile(filepath.Join(root, "evidence/logs/obs-live.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		receipt.FailureCode = "obs_log_failure"
		return
	}
	defer obsLog.Close()
	obs := exec.CommandContext(ctx, "flatpak", "run", "--filesystem="+root,
		"--env=XDG_CONFIG_HOME="+filepath.Join(root, "config"), "--env=XDG_DATA_HOME="+filepath.Join(root, "data"),
		"--env=XDG_CACHE_HOME="+filepath.Join(root, "cache"), "com.obsproject.Studio", "--profile", "DOT65-P4",
		"--collection", "DOT65-P4", "--startrecording")
	obs.Dir, obs.Stdout, obs.Stderr = root, obsLog, obsLog
	if err = obs.Start(); err != nil {
		receipt.FailureCode = "obs_start_failure"
		return
	}
	obsDone := make(chan error, 1)
	go func() { obsDone <- obs.Wait() }()
	obsClean := false
	defer func() {
		if !obsClean {
			_ = stopProcess(obs, obsDone, 30*time.Second)
		}
	}()
	if err = appendObservation(0, "obs_started"); err != nil {
		receipt.FailureCode = "dota_identity_drift"
		return
	}

	rawPath := filepath.Join(root, filepath.FromSlash(owner.RawPath))
	samples, err := rootOpenFile(filepath.Join(root, "evidence/samples.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		receipt.FailureCode = "resource_evidence_failure"
		return
	}
	defer samples.Close()
	visibility, err := rootOpenFile(filepath.Join(root, "evidence/visibility.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		receipt.FailureCode = "visibility_evidence_failure"
		return
	}
	defer visibility.Close()
	deadline := defaultRehearsalProducerDeps.now().Add(MaxRehearsalAttemptDuration)
	lastObserved := uint64(0)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	visibilityTicker := time.NewTicker(100 * time.Millisecond)
	defer visibilityTicker.Stop()
	tickCount := uint64(0)
	for {
		select {
		case <-ctx.Done():
			receipt.FailureCode = "attempt_cancelled"
			return
		case processErr := <-productDone:
			productClean = processErr == nil
			receipt.FailureCode = "product_exited_early"
			return
		case <-visibilityTicker.C:
			value := collectVisibility(rawPath, filepath.Join(root, "runtime/operator.token"))
			if json.NewEncoder(visibility).Encode(value) != nil || visibility.Sync() != nil || !value.TelemetryComplete {
				receipt.FailureCode = "visibility_evidence_failure"
				return
			}
		case <-ticker.C:
			tickCount++
			raw := scanRehearsalRaw(rawPath, owner.SessionID)
			if raw.Manifest.FailureCode != "" && raw.Manifest.FailureCode != "raw_provenance_failure" {
				receipt.FailureCode = raw.Manifest.FailureCode
				return
			}
			for sequence := lastObserved + 1; sequence <= raw.Manifest.Accepted; sequence++ {
				if err = appendObservation(sequence, "raw"); err != nil {
					receipt.FailureCode = "dota_identity_drift"
					return
				}
				lastObserved = sequence
			}
			facts := deriveRehearsalFacts(raw)
			if expectedMatch != "" && facts.MatchID != "" && facts.MatchID != expectedMatch {
				receipt.FailureCode = "expected_match_mismatch"
				return
			}
			if tickCount%5 == 0 && facts.MatchID != "" {
				sample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, filepath.Join(root, "runtime/operator.token"), root, owner.SessionID, facts.MatchID)
				if json.NewEncoder(samples).Encode(sample) != nil || samples.Sync() != nil || !sample.TelemetryComplete {
					receipt.FailureCode = "resource_evidence_failure"
					return
				}
			}
			if facts.NormalPostGame && facts.WinnerObserved {
				goto terminal
			}
			if !defaultRehearsalProducerDeps.now().Before(deadline) {
				receipt.FailureCode = "attempt_deadline"
				return
			}
		}
	}

terminal:
	if err = appendObservation(lastObserved, "terminal_before_shutdown"); err != nil {
		receipt.FailureCode = "dota_identity_drift"
		return
	}
	_, endpointErr := captureFinalEndpoints(root, filepath.Join(root, "runtime/operator.token"))
	productClean = stopProcess(product, productDone, 15*time.Second) == nil
	obsClean = stopProcess(obs, obsDone, 30*time.Second) == nil
	if err = appendObservation(lastObserved, "terminal_after_shutdown"); err != nil {
		receipt.FailureCode = "dota_identity_drift"
		return
	}
	if endpointErr != nil || !productClean || !obsClean {
		receipt.FailureCode = "terminal_shutdown_failure"
		return
	}
	validation, _, validationErr := validateCompletedAttempt(root, owner.SessionID, productClean, obsClean)
	if validationErr != nil {
		receipt.FailureCode = "harness_validation_failure"
		return
	}
	recordingSHA, recordingBytes, recordingErr := singleRecordingIdentity(filepath.Join(root, "recordings"))
	validationSHA, _, validationHashErr := fileSHA(filepath.Join(root, "evidence/canonical/live-validation.json"))
	rawSHA, _, rawHashErr := fileSHA(rawPath)
	if recordingErr != nil || validationHashErr != nil || rawHashErr != nil {
		receipt.FailureCode = "producer_artifact_failure"
		return
	}
	harness := RehearsalHarnessEvidenceV1{
		SchemaVersion: "public_match_rehearsal_harness.v2", SessionID: owner.SessionID, RawSHA256: rawSHA,
		AcquiredDota: bound, DotaObservations: observations, RecordingSHA256: recordingSHA, RecordingBytes: recordingBytes,
		ValidationSHA256: validationSHA, RecoverySHA256: validation.RecoveryCursorSHA256, ProductClean: productClean, OBSClean: obsClean,
	}
	harnessPayload, marshalErr := canonical(harness)
	if marshalErr != nil || writePrivate(filepath.Join(root, "evidence/harness.json"), harnessPayload) != nil {
		receipt.FailureCode = "producer_artifact_failure"
		return
	}
	receipt.State = "sealed"
	receipt.FailureCode = ""
	receipt.HarnessSHA256 = payloadSHA(harnessPayload)
}

func singleRecordingIdentity(directory string) (string, int64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", 0, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".mkv" {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) != 1 {
		return "", 0, errors.New("exactly one finalized recording required")
	}
	return fileSHA(paths[0])
}
