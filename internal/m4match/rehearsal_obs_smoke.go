package m4match

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type obsOnlySmokeDriver struct{}

func (obsOnlySmokeDriver) Run(ctx context.Context, run rehearsalLifecycleContext) (report rehearsalLifecycleReport, retErr error) {
	report.sourceMode = "obs_only_smoke"
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
	if err := rootMkdirAll(filepath.Join(run.root, "config/policy"), 0o700); err != nil {
		complete(build, err)
		return report, err
	}
	if _, err := Prepare(run.root, 1920, 1080); err != nil {
		complete(build, err)
		return report, err
	}
	if err := prepareRehearsalOBSProfile(run.root); err != nil {
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
	goHash, _, goErr := fileSHA(run.preflight.GoExecutable)
	if err == nil && (goErr != nil || goHash != run.preflight.GoExecutableSHA256) {
		err = errors.New("bound Go executable identity changed")
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
		_ = productLog.Close()
		return report, err
	}
	report.productPID = product.Process.Pid
	productDone := make(chan error, 1)
	go func() { productDone <- product.Wait() }()
	ready := step("product_ready")
	err = waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second)
	complete(ready, err)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		_ = productLog.Close()
		return report, err
	}
	obsLog, err := rootOpenFile(filepath.Join(run.root, "evidence/logs/obs-live.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		_ = productLog.Close()
		return report, err
	}
	startOBS := step("start_obs")
	obs, err := startOwnedFlatpakOBS(ctx, run.root, obsLog)
	complete(startOBS, err)
	if err != nil {
		_ = stopProcess(product, productDone, 15*time.Second)
		_ = productLog.Close()
		_ = obsLog.Close()
		return report, err
	}
	report.obsPID, report.obsInstanceID, report.obsExecutable = obs.OBSPID, obs.InstanceID, obs.Identity
	obsReady := step("obs_ready")
	startCorrelation, err := waitOwnedFlatpakCorrelation(ctx, product.Process.Pid, obs, 20*time.Second)
	if err == nil {
		report.obsStartCorrelation = startCorrelation.SHA256
		err = waitOBSRecordingStarted(ctx, run.root, 30*time.Second)
	}
	complete(obsReady, err)
	if err == nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	terminalCorrelation, correlationErr := captureOwnedFlatpakCorrelation(context.Background(), product.Process.Pid, obs)
	if correlationErr == nil {
		report.obsTerminalCorrelation = terminalCorrelation.SHA256
	}
	finalize := step("finalize_obs")
	obsStop := stopOwnedFlatpakOBS(context.Background(), obs, 30*time.Second)
	obsStop = errors.Join(correlationErr, obsStop, obsLog.Close())
	report.recordingFinalized = obsStop == nil && recordingFinalized(filepath.Join(run.root, "recordings"), filepath.Join(run.root, "evidence/logs/obs-live.log"))
	obsStop = errors.Join(obsStop, boolError(report.recordingFinalized, "OBS smoke recording did not finalize"))
	complete(finalize, obsStop)
	shutdownOBS := step("shutdown_obs")
	complete(shutdownOBS, obsStop)
	shutdownProduct := step("shutdown_product")
	productStop := stopProcess(product, productDone, 15*time.Second)
	productStop = errors.Join(productStop, productLog.Close())
	complete(shutdownProduct, productStop)
	seal := step("seal_evidence")
	finalErr := errors.Join(err, obsStop, productStop)
	complete(seal, finalErr)
	report.cleanShutdown = finalErr == nil
	return report, finalErr
}
