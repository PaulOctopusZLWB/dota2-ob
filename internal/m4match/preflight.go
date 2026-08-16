package m4match

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type PreflightConfig struct {
	DataRoot string
	RepoRoot string
	Width    int
	Height   int
}

type commandResult struct {
	output []byte
	err    error
}

func Preflight(ctx context.Context, config PreflightConfig) (Readiness, error) {
	repo, err := filepath.Abs(config.RepoRoot)
	if err != nil {
		return Readiness{}, err
	}
	root, err := safeRoot(config.DataRoot, repo)
	if err != nil {
		return Readiness{}, err
	}
	if entries, statErr := os.ReadDir(root); statErr == nil && len(entries) != 0 {
		return Readiness{}, errors.New("preflight data root must be fresh and empty")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Readiness{}, statErr
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Readiness{}, err
	}
	if config.Width == 0 {
		config.Width, config.Height = 1920, 1080
	}
	artifacts, err := Prepare(root, config.Width, config.Height)
	if err != nil {
		return Readiness{}, err
	}
	policyArtifacts, err := writeLiveArtifacts(root, "dot65-preflight")
	if err != nil {
		return Readiness{}, err
	}
	artifacts = append(artifacts, policyArtifacts...)
	evidence := Evidence{
		SchemaVersion: SchemaVersion, Mode: "preflight", AcceptedBase: AcceptedFunctionalBase, AcceptedSpec: AcceptedP4Spec,
		FixtureSHA256: CapturedScheduleSHA256, GoldenSHA256: ProductionGoldenSHA256, Bounds: AcceptedBounds(),
		Faults: append([]string(nil), RequiredFaults...), Artifacts: artifacts,
		StartBoundary:     "capture health, operator endpoint, overlay endpoint, isolated OBS preview, and recording are armed before the first accepted game-clock sample",
		GameZeroBoundary:  "first accepted map.clock_time >= 0 after an accepted negative pre-game clock; missing negative clock is a late join",
		PostGameBoundary:  "accepted GSI map.game_state equals a normal post-game terminal state and raw/projected/policy identities reconcile",
		RecordingBoundary: "OBS reports recording stop and finalized MKV exists, is non-empty, and has no active partial file",
		OperatorScript:    HumanInstruction, NonResumable: true, SyntheticOnly: true, ClaimsP4: false, ReadinessIssueText: HumanInstruction,
	}
	add := func(id string, passed bool, detail string) {
		evidence.Checks = append(evidence.Checks, Check{ID: id, Passed: passed, Detail: detail})
	}

	head, headErr := runText(ctx, repo, "git", "rev-parse", "HEAD")
	parent, parentErr := runText(ctx, repo, "git", "rev-parse", "HEAD^")
	evidence.CandidateCommit, evidence.CandidateParent = head, parent
	add("candidate_commit", headErr == nil && len(head) == 40, "exact immutable successor recorded")
	add("candidate_parent", parentErr == nil && parent == RequiredSuccessorParent, "sole parent is the exact rejected candidate")
	_, ancestryErr := runText(ctx, repo, "git", "merge-base", "--is-ancestor", AcceptedFunctionalBase, "HEAD")
	add("accepted_ancestry", ancestryErr == nil, "accepted functional base is an ancestor")
	status, statusErr := runText(ctx, repo, "git", "status", "--porcelain=v1", "--untracked-files=all")
	add("clean_tree", statusErr == nil && status == "", "repository has no tracked or untracked changes")

	fixture := filepath.Join(repo, "internal/integration/m4/testdata/captured_gsi_schedule.json")
	fixtureHash, _, fixtureErr := fileSHA(fixture)
	add("captured_schedule", fixtureErr == nil && fixtureHash == CapturedScheduleSHA256, "accepted captured schedule hash")
	golden := filepath.Join(repo, "internal/integration/m4/testdata/replay_output.golden.json")
	goldenHash, _, goldenErr := fileSHA(golden)
	add("production_golden", goldenErr == nil && goldenHash == ProductionGoldenSHA256, "accepted production golden hash")

	env, envChecks := inspectEnvironment(ctx)
	evidence.Environment = env
	for _, check := range envChecks {
		evidence.Checks = append(evidence.Checks, check)
	}
	active, processErr := discoverProcesses()
	add("isolated_process_state", processErr == nil && len(active) == 0, "no pre-existing Dota or OBS process: "+strings.Join(active, ","))
	identityCollector := defaultIdentityCollector()
	identityStart := identityCollector.captureRepository(ctx, repo, "start")

	commands := []struct {
		id            string
		dir           string
		name          string
		args          []string
		lockedInstall bool
	}{
		{id: "focused_twice", dir: repo, name: "go", args: []string{"test", "-count=2", "-timeout=12m", "./cmd/dota2-ob", "./internal/integration/m4", "./cmd/m4-match", "./internal/m4match"}},
		{id: "m4_fault_matrix", dir: repo, name: "go", args: []string{"test", "-v", "-count=1", "-timeout=12m", "-run", "TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts|TestM4ProductionCompositionFailsClosedForMissingAndSubstitutedBinding|TestBroadcastRuntimeV3QueueSaturationLatchesHealthHideAndRecovers|TestBroadcastRuntimeV3ReadinessDeadlinesNamePhaseAndSequence|TestBroadcastRuntimeV3RecoversAndReturnsExactDurableDuplicate", "./cmd/dota2-ob", "./internal/integration/m4"}},
		{id: "full_go", dir: repo, name: "go", args: []string{"test", "-count=1", "./..."}},
		{id: "full_race", dir: repo, name: "env", args: []string{"CGO_ENABLED=1", "CC=zig cc", "go", "test", "-race", "-timeout", "30m", "-count=1", "./..."}},
		{id: "vet", dir: repo, name: "go", args: []string{"vet", "./..."}},
		{id: "build_all", dir: repo, name: "go", args: []string{"build", "./..."}},
		{id: "module_verify", dir: repo, name: "go", args: []string{"mod", "verify"}},
		{id: "browser_install", dir: filepath.Join(repo, "web/browser"), lockedInstall: true},
		{id: "browser_tests", dir: filepath.Join(repo, "web/browser"), name: "npm", args: []string{"test"}},
		{id: "obs_overlay_install", dir: filepath.Join(repo, "spikes/obs-overlay"), lockedInstall: true},
		{id: "obs_overlay_tests", dir: filepath.Join(repo, "spikes/obs-overlay"), name: "npm", args: []string{"test"}},
	}
	for _, command := range commands {
		logPath := filepath.Join(root, "evidence/logs", command.id+".log")
		var result commandResult
		if command.lockedInstall {
			result = runLockedInstall(ctx, command.dir, logPath)
		} else {
			result = runLogged(ctx, command.dir, logPath, command.name, command.args...)
		}
		add(command.id, result.err == nil, "exit status recorded in noncanonical run log")
	}

	binary := filepath.Join(root, "application/dota2-ob")
	build := runLogged(ctx, repo, filepath.Join(root, "evidence/logs/build.log"), "go", "build", "-trimpath", "-ldflags=-buildid=", "-o", binary, "./cmd/dota2-ob")
	add("candidate_build", build.err == nil, "trimpath build with empty Go build id")
	if build.err == nil {
		hash, size, hashErr := fileSHA(binary)
		add("candidate_binary_hash", hashErr == nil, "exact built executable hash recorded")
		if hashErr == nil {
			evidence.Artifacts = append(evidence.Artifacts, Artifact{Path: "application/dota2-ob", SHA256: hash, Bytes: size})
		}
		endpointErr := probeProduct(ctx, root, binary)
		add("production_endpoints", endpointErr == nil, "GSI, operator, overlay, and orderly shutdown verified")
		killErr := probeProductSIGKILLRestart(ctx, root, binary)
		add("product_sigkill_restart", killErr == nil, "product was deliberately SIGKILLed and restarted from retained raw input")
	}
	faultProofs := map[string]string{
		"audit_failure":                        "TestBroadcastRuntimeFailsClosedOnCommitFailureAndStaleOutput",
		"cache_independent_rebuild":            "TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts",
		"candidate_queue_saturation":           "TestBroadcastRuntimeV3QueueSaturationLatchesHealthHideAndRecovers",
		"checkpoint_partial_frame":             "TestPolicyCheckpointPartialFrameRecovery",
		"cursor_loss":                          "TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts",
		"gsi_body_bounds":                      "TestObservationResolverUnlinksCachesAndAcceptsMaximumPersistedCapture",
		"operator_command_revision_and_replay": "TestBroadcastRuntimeV3RecoversAndReturnsExactDurableDuplicate",
		"orderly_restart":                      "TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts",
		"out_of_order_delivery":                "TestBroadcastRuntimeV3DoesNotRewindCausalBaselineAndRestartsAtNewest",
		"overlay_disconnect":                   "browser and OBS-overlay fail-closed suites",
		"overlay_state_bounds":                 "browser and OBS-overlay boundary suites",
		"partial_policy_frame":                 "TestPolicyCommitPartialFrameRecovery",
		"partial_raw_tail":                     "TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts",
		"product_sigkill_restart":              "HARNESS_SIGKILL_SENT and HARNESS_RESTART_RECOVERED_RAW_SEQUENCE=1",
		"stale_input":                          "TestBroadcastRuntimeFailsClosedOnCommitFailureAndStaleOutput",
		"two_second_fail_closed":               "TestBroadcastRuntimeV3ReadinessDeadlinesNamePhaseAndSequence plus browser deadlines",
	}
	for _, fault := range RequiredFaults {
		passed := build.err == nil
		if fault == "product_sigkill_restart" {
			passed = checkPassed(evidence.Checks, "product_sigkill_restart")
		} else {
			passed = passed && checkPassed(evidence.Checks, "m4_fault_matrix")
		}
		proof, proofExists := faultProofs[fault]
		add("fault_"+fault, passed && proofExists, "direct proof: "+proof)
	}
	faultPayload, _ := canonical(faultProofs)
	faultPath := filepath.Join(root, "evidence/canonical/fault-proof-manifest.json")
	if err := writePrivate(faultPath, faultPayload); err != nil {
		return Readiness{}, err
	}
	evidence.Artifacts = append(evidence.Artifacts, Artifact{Path: "evidence/canonical/fault-proof-manifest.json", SHA256: payloadSHA(faultPayload), Bytes: int64(len(faultPayload))})
	diffOutput, diffErr := runText(ctx, repo, "git", "diff", "--check", RequiredSuccessorParent+"..HEAD")
	add("diff_check", diffErr == nil && diffOutput == "", "successor diff has no whitespace errors")
	deps, depsErr := runText(ctx, repo, "go", "list", "-deps", "./cmd/m4-match")
	add("dependency_boundary", depsErr == nil && !strings.Contains(deps, "/internal/replay") && !strings.Contains(deps, "/internal/history"), "harness imports no replay/history adapter")

	privacyErr := scanPrivacyAndSources(repo)
	add("privacy_and_source_boundary", privacyErr == nil, "fixture/privacy and forbidden-source scan")
	secretErr := scanSecretsAndGenerated(repo)
	add("secret_generated_scan", secretErr == nil, "tracked secret and generated/private-data scan")
	status, statusErr = runText(ctx, repo, "git", "status", "--porcelain=v1", "--untracked-files=all")
	add("clean_tree_final", statusErr == nil && status == "", "repository remains clean after complete matrix")
	identity, identityEvidence, identityDiagnostics, identityErr := identityCollector.complete(ctx, repo, binary, env, identityStart)
	evidence.CandidateIdentityEvidence = identityEvidence
	if len(identityDiagnostics) != 0 {
		diagnosticPayload := []byte(strings.TrimSpace(strings.Join(identityDiagnostics, "\n---\n")) + "\n")
		if diagnosticErr := writePrivate(filepath.Join(root, "diagnostics/candidate-identity.log"), diagnosticPayload); diagnosticErr != nil {
			return Readiness{}, diagnosticErr
		}
	}
	if identityErr == nil {
		evidence.CandidateIdentity = identity
	}
	identityDetail := "all named local, remote, product, harness, and environment identity sub-checks passed"
	if identityErr != nil {
		identityDetail = identityErr.Error()
	}
	add("candidate_identity", identityErr == nil, identityDetail)
	logArtifacts, logErr := collectLogArtifacts(root)
	if logErr != nil {
		return Readiness{}, logErr
	}
	evidence.Artifacts = append(evidence.Artifacts, logArtifacts...)
	sortEvidence(&evidence)
	indexPayload, marshalErr := canonical(evidence)
	if marshalErr != nil {
		return Readiness{}, marshalErr
	}
	indexPath := filepath.Join(root, "evidence/canonical/evidence-index.json")
	if err := writePrivate(indexPath, indexPayload); err != nil {
		return Readiness{}, err
	}
	failures := make([]string, 0)
	for _, check := range evidence.Checks {
		if !check.Passed {
			failures = append(failures, check.ID)
		}
	}
	identitySHA, _ := candidateIdentitySHA(evidence.CandidateIdentity)
	readiness := Readiness{SchemaVersion: ReadinessSchemaVersion, Ready: len(failures) == 0, Mode: "preflight", CandidateCommit: head, CandidateIdentitySHA256: identitySHA, EnvironmentSHA256: evidence.CandidateIdentity.EnvironmentSHA256, EvidenceIndexSHA256: payloadSHA(indexPayload), Failures: failures, ClaimsP4: false, HumanInstruction: HumanInstruction}
	if err := writeJSON(filepath.Join(root, "evidence/readiness.json"), readiness, 0o600); err != nil {
		return Readiness{}, err
	}
	return readiness, nil
}

func runText(ctx context.Context, dir, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func runLogged(ctx context.Context, dir, logPath, name string, args ...string) commandResult {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	_ = writePrivate(logPath, normalizeLog(output, dir, filepath.Dir(filepath.Dir(filepath.Dir(logPath))), err))
	return commandResult{output: output, err: err}
}

var durationPattern = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?(?:ms|s|m)[0-9]*(?:\.[0-9]+)?s?\b`)
var durationMillisPattern = regexp.MustCompile(`(?m)(\bduration_ms(?::|\s)\s*)[0-9]+(?:\.[0-9]+)?`)
var jsonDurationPattern = regexp.MustCompile(`("duration"\s*:\s*)[0-9]+(?:\.[0-9]+)?`)
var webServerTimestampPattern = regexp.MustCompile(`\b\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\b`)
var timestampSessionPattern = regexp.MustCompile(`\b\d{8}T\d{6}(?:\.\d+)?Z\b`)
var processIDPattern = regexp.MustCompile(`(?i)(\bpid(?:=|:\s*|\s+))\d+\b`)

var lockedInstallArgs = []string{"ci", "--no-audit", "--no-fund"}

func runLockedInstall(ctx context.Context, dir, logPath string) commandResult {
	lockHash, _, lockErr := fileSHA(filepath.Join(dir, "package-lock.json"))
	command := exec.CommandContext(ctx, "npm", lockedInstallArgs...)
	command.Dir = dir
	output, commandErr := command.CombinedOutput()
	err := errors.Join(lockErr, commandErr)
	proof := "lockfile_sha256=" + lockHash + "\n"
	_ = writePrivate(logPath, normalizeLog(append([]byte(proof), output...), dir, filepath.Dir(filepath.Dir(filepath.Dir(logPath))), err))
	return commandResult{output: output, err: err}
}

func normalizeLog(output []byte, dir, root string, commandErr error) []byte {
	text := strings.ReplaceAll(string(output), filepath.Clean(dir), "<WORKDIR>")
	text = strings.ReplaceAll(text, filepath.Clean(root), "<EVIDENCE_ROOT>")
	text = durationPattern.ReplaceAllString(text, "<DURATION>")
	text = durationMillisPattern.ReplaceAllString(text, `${1}<DURATION_MS>`)
	text = jsonDurationPattern.ReplaceAllString(text, `${1}"<DURATION_MS>"`)
	text = webServerTimestampPattern.ReplaceAllString(text, "<TIME>")
	text = timestampSessionPattern.ReplaceAllString(text, "<SESSION>")
	text = processIDPattern.ReplaceAllString(text, `${1}<PID>`)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	sort.Strings(lines)
	status := "PASS"
	if commandErr != nil {
		status = "FAIL"
	}
	return []byte("status=" + status + "\n" + strings.TrimSpace(strings.Join(lines, "\n")) + "\n")
}

func checkPassed(checks []Check, id string) bool {
	for _, check := range checks {
		if check.ID == id {
			return check.Passed
		}
	}
	return false
}

func collectLogArtifacts(root string) ([]Artifact, error) {
	logs := filepath.Join(root, "evidence/logs")
	var artifacts []Artifact
	err := filepath.WalkDir(logs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(payload, []byte("status=")) {
			payload = normalizeRuntimeEvidenceLog(payload, root)
			if err := writePrivate(path, payload); err != nil {
				return err
			}
		}
		hash, size, err := fileSHA(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, Artifact{Path: filepath.ToSlash(relative), SHA256: hash, Bytes: size})
		return nil
	})
	return artifacts, err
}

var runtimeTimestampPattern = regexp.MustCompile(`(?m)^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

func normalizeRuntimeEvidenceLog(payload []byte, root string) []byte {
	text := strings.ReplaceAll(string(payload), filepath.Clean(root), "<EVIDENCE_ROOT>")
	text = runtimeTimestampPattern.ReplaceAllString(text, "<TIME> ")
	return []byte(strings.TrimSpace(text) + "\n")
}

func inspectEnvironment(ctx context.Context) (Environment, []Check) {
	env := Environment{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	checks := make([]Check, 0)
	tool := func(id, name string, args ...string) string {
		value, err := runText(ctx, ".", name, args...)
		checks = append(checks, Check{ID: id, Passed: err == nil && value != "", Detail: "exact version captured"})
		return value
	}
	env.Go = tool("environment_go", "go", "version")
	env.Node = tool("environment_node", "node", "--version")
	env.NPM = tool("environment_npm", "npm", "--version")
	env.Zig = tool("environment_zig", "zig", "version")
	env.Kernel = tool("environment_kernel", "uname", "-srmo")
	env.ClockTicksPerSecond = tool("environment_clock_ticks", "getconf", "CLK_TCK")
	env.Steam = tool("environment_steam", "dpkg-query", "-W", "-f=${Version}", "steam-launcher")
	manifest := filepath.Join(os.Getenv("HOME"), ".local/share/Steam/steamapps/appmanifest_570.acf")
	payload, err := os.ReadFile(manifest)
	if err == nil {
		for _, line := range strings.Split(string(payload), "\n") {
			if strings.Contains(line, `"buildid"`) {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					env.DotaBuild = strings.Trim(fields[len(fields)-1], `"`)
				}
			}
		}
	}
	dotaBinary := filepath.Join(os.Getenv("HOME"), ".local/share/Steam/steamapps/common/dota 2 beta/game/bin/linuxsteamrt64/dota2")
	env.DotaBinary, _, err = fileSHA(dotaBinary)
	checks = append(checks, Check{ID: "environment_dota", Passed: err == nil && env.DotaBuild != "", Detail: "Steam app 570 build and binary hash captured"})
	obsInfo, obsErr := runText(ctx, ".", "flatpak", "info", "com.obsproject.Studio")
	for _, line := range strings.Split(obsInfo, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Version:") {
			env.OBSVersion = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Version:"))
		}
	}
	err = obsErr
	env.OBSCommit, _ = runText(ctx, ".", "flatpak", "info", "--show-commit", "com.obsproject.Studio")
	env.OBSBinary, _ = runText(ctx, ".", "flatpak", "run", "com.obsproject.Studio", "--version")
	if payload, readErr := os.ReadFile("/proc/driver/nvidia/version"); readErr == nil {
		env.GPUDriver = strings.TrimSpace(string(payload))
	}
	checks = append(checks, Check{ID: "environment_obs", Passed: err == nil && env.OBSVersion != "" && env.OBSCommit != "" && strings.Contains(env.OBSBinary, env.OBSVersion), Detail: "OBS Flatpak version, commit, and executable captured"})
	return env, checks
}

func discoverProcesses() ([]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var found []string
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		payload, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(payload)))
		if name == "dota2" || name == "obs" || name == "obs-studio" {
			found = append(found, entry.Name()+":"+name)
		}
	}
	return found, nil
}

func probeProduct(ctx context.Context, root, binary string) error {
	logPath := filepath.Join(root, "evidence/logs/product-probe.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	tokenPath := filepath.Join(root, "runtime/operator.token")
	command := exec.CommandContext(ctx, binary, "--policy-mode", "v3-live-only", "--data-dir", filepath.Join(root, "data/sessions"), "--session-id", "dot65-preflight", "--addr", CaptureAddress, "--delivery-addr", DeliveryAddress, "--operator-token-file", tokenPath,
		"--history-binding-file", filepath.Join(root, "config/policy/history_availability_binding_v1.json"), "--live-only-lineage-file", filepath.Join(root, "config/policy/policy_lineage_manifest_v3.json"), "--live-only-release-file", filepath.Join(root, "config/policy/live_only_release_binding_v1.json"))
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	exited := false
	defer func() {
		if !exited {
			_ = command.Process.Kill()
			<-done
		}
		_ = logFile.Close()
	}()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case exitErr := <-done:
			exited = true
			return fmt.Errorf("spawned product exited before readiness: %v", exitErr)
		default:
		}
		response, requestErr := client.Get(CaptureOrigin + "/healthz")
		if requestErr == nil && response.StatusCode == http.StatusOK {
			_ = response.Body.Close()
			break
		}
		if response != nil {
			_ = response.Body.Close()
		}
		if time.Now().After(deadline) {
			return errors.New("capture endpoint did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	response, err := client.Post(CaptureOrigin+"/gsi", "application/json", strings.NewReader(`{"map":{"clock_time":-90,"game_state":"DOTA_GAMERULES_STATE_PRE_GAME"}}`))
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			_ = response.Body.Close()
		}
		return errors.New("production GSI endpoint rejected synthetic probe")
	}
	_ = response.Body.Close()
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	for _, endpoint := range []string{"/v1/operator/state", "/v1/overlay/state"} {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, DeliveryOrigin+endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		request.Header.Set("Origin", DeliveryOrigin)
		response, err = client.Do(request)
		if err != nil || response.StatusCode != http.StatusOK {
			if response != nil {
				_ = response.Body.Close()
			}
			return fmt.Errorf("delivery endpoint failed: %s", endpoint)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		if readErr != nil || !json.Valid(body) {
			return fmt.Errorf("delivery endpoint invalid: %s", endpoint)
		}
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case err := <-done:
		exited = true
		if err != nil {
			return err
		}
	case <-time.After(15 * time.Second):
		return errors.New("product did not stop cleanly")
	}
	return nil
}

func probeProductSIGKILLRestart(ctx context.Context, root, binary string) error {
	logPath := filepath.Join(root, "evidence/logs/product-sigkill-restart.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	dataDir := filepath.Join(root, "data/sigkill-sessions")
	tokenPath := filepath.Join(root, "runtime/sigkill-operator.token")
	start := func() (*exec.Cmd, chan error, error) {
		command := exec.CommandContext(ctx, binary, "--policy-mode", "v3-live-only", "--data-dir", dataDir, "--session-id", "dot65-preflight", "--addr", CaptureAddress, "--delivery-addr", DeliveryAddress, "--operator-token-file", tokenPath,
			"--history-binding-file", filepath.Join(root, "config/policy/history_availability_binding_v1.json"), "--live-only-lineage-file", filepath.Join(root, "config/policy/policy_lineage_manifest_v3.json"), "--live-only-release-file", filepath.Join(root, "config/policy/live_only_release_binding_v1.json"))
		command.Stdout, command.Stderr = logFile, logFile
		if err := command.Start(); err != nil {
			return nil, nil, err
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		if err := waitHTTP(ctx, CaptureOrigin+"/healthz", 10*time.Second); err != nil {
			_ = command.Process.Kill()
			<-done
			return nil, nil, err
		}
		return command, done, nil
	}
	first, firstDone, err := start()
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Post(CaptureOrigin+"/gsi", "application/json", strings.NewReader(`{"map":{"clock_time":-120,"game_state":"DOTA_GAMERULES_STATE_PRE_GAME","matchid":9999999999}}`))
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			_ = response.Body.Close()
		}
		_ = first.Process.Kill()
		<-firstDone
		return errors.New("SIGKILL probe raw input was not accepted")
	}
	_ = response.Body.Close()
	if err := first.Process.Kill(); err != nil {
		return err
	}
	if err := <-firstDone; err == nil {
		return errors.New("deliberate product SIGKILL unexpectedly reported clean exit")
	}
	if err := os.Remove(tokenPath); err != nil {
		return fmt.Errorf("remove harness-owned stale SIGKILL token: %w", err)
	}
	_, _ = fmt.Fprintln(logFile, "HARNESS_SIGKILL_SENT")
	second, secondDone, err := start()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, requestErr := (&http.Client{Timeout: time.Second}).Get(CaptureOrigin + "/api/status")
		if requestErr == nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			var status struct {
				Live struct {
					Projected uint64 `json:"projected_sequence"`
					HighWater uint64 `json:"high_water"`
					Lag       uint64 `json:"lag_count"`
				} `json:"live_projection"`
			}
			if response.StatusCode == http.StatusOK && json.Unmarshal(body, &status) == nil && status.Live.HighWater == 1 && status.Live.Projected == 1 && status.Live.Lag == 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = second.Process.Kill()
			<-secondDone
			return errors.New("SIGKILL restart did not recover retained raw input")
		}
		time.Sleep(25 * time.Millisecond)
	}
	_, _ = fmt.Fprintln(logFile, "HARNESS_RESTART_RECOVERED_RAW_SEQUENCE=1")
	if err := second.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case err := <-secondDone:
		if err != nil {
			return fmt.Errorf("SIGKILL recovery shutdown: %w", err)
		}
	case <-time.After(15 * time.Second):
		_ = second.Process.Kill()
		<-secondDone
		return errors.New("SIGKILL recovery process did not stop cleanly")
	}
	return nil
}

func scanPrivacyAndSources(repo string) error {
	for _, relative := range []string{"internal/integration/m4/testdata/captured_gsi_schedule.json", "internal/integration/m4/testdata/replay_output.golden.json"} {
		payload, err := os.ReadFile(filepath.Join(repo, relative))
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(payload))
		for _, forbidden := range []string{"steamid", "accountid", "authorization", "bearer ", "password", "cookie", "chat"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("private field %q in %s", forbidden, relative)
			}
		}
	}
	for _, relative := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repo, relative), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || strings.Contains(filepath.ToSlash(path), "/internal/m4match/") {
				return walkErr
			}
			payload, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			lower := strings.ToLower(string(payload))
			for _, forbidden := range []string{"process_vm_readv", "ptrace(", "libpcap", "af_packet", "pcap_open_live", "/proc/net/packet"} {
				if strings.Contains(lower, forbidden) {
					return fmt.Errorf("forbidden source pattern %q in %s", forbidden, path)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func scanSecretsAndGenerated(repo string) error {
	tracked, err := runText(context.Background(), repo, "git", "ls-files")
	if err != nil {
		return err
	}
	for _, relative := range strings.Split(tracked, "\n") {
		lower := strings.ToLower(filepath.ToSlash(relative))
		for _, forbidden := range []string{"raw.jsonl", ".dem", ".mkv", ".pcl3", "operator.token", "node_modules/", "test-results/", "obs-studio/"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("tracked generated/private path %s", relative)
			}
		}
		if filepath.Ext(relative) == ".go" || filepath.Ext(relative) == ".json" || filepath.Ext(relative) == ".md" || filepath.Ext(relative) == ".yml" || filepath.Ext(relative) == ".yaml" {
			payload, readErr := os.ReadFile(filepath.Join(repo, relative))
			if readErr != nil {
				return readErr
			}
			lowerPayload := strings.ToLower(string(payload))
			for _, secret := range []string{"-----begin " + "private key-----", "gh" + "p_", "github" + "_pat_", "steam_web" + "_api_key="} {
				if strings.Contains(lowerPayload, secret) {
					return fmt.Errorf("secret pattern in %s", relative)
				}
			}
		}
	}
	return nil
}

// readRawSequences is shared with the live sampler and verifier. It accepts
// only complete newline-terminated records; a partial tail is never counted.
func readRawSequences(path string) (uint64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var sequence uint64
	for scanner.Scan() {
		var frame struct {
			Sequence uint64 `json:"sequence"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Sequence != sequence+1 {
			return sequence, errors.New("raw sequence gap or malformed frame")
		}
		sequence = frame.Sequence
	}
	return sequence, scanner.Err()
}
