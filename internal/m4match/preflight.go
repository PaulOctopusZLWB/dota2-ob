package m4match

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	add("candidate_commit", headErr == nil && len(head) == 40, "exact immutable candidate recorded")
	add("candidate_parent", parentErr == nil && parent == AcceptedFunctionalBase, "sole parent is accepted functional base")
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

	commands := []struct {
		id   string
		dir  string
		name string
		args []string
	}{
		{"m4_fault_matrix", repo, "go", []string{"test", "-count=2", "-timeout=12m", "./cmd/dota2-ob", "./internal/integration/m4"}},
		{"browser_install", filepath.Join(repo, "web/browser"), "npm", []string{"ci"}},
		{"browser_tests", filepath.Join(repo, "web/browser"), "npm", []string{"test"}},
		{"obs_overlay_install", filepath.Join(repo, "spikes/obs-overlay"), "npm", []string{"ci"}},
		{"obs_overlay_tests", filepath.Join(repo, "spikes/obs-overlay"), "npm", []string{"test"}},
	}
	for _, command := range commands {
		result := runLogged(ctx, command.dir, filepath.Join(root, "evidence/logs", command.id+".log"), command.name, command.args...)
		add(command.id, result.err == nil, "exit status recorded in noncanonical run log")
		if command.id == "m4_fault_matrix" {
			for _, fault := range RequiredFaults {
				add("fault_"+fault, result.err == nil, "accepted production M4 suite exercises this invariant")
			}
		}
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
	}

	privacyErr := scanPrivacyAndSources(repo)
	add("privacy_and_source_boundary", privacyErr == nil, "fixture/privacy and forbidden-source scan")
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
	readiness := Readiness{SchemaVersion: ReadinessSchemaVersion, Ready: len(failures) == 0, Mode: "preflight", CandidateCommit: head, EvidenceIndexSHA256: payloadSHA(indexPayload), Failures: failures, ClaimsP4: false, HumanInstruction: HumanInstruction}
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
	_ = writePrivate(logPath, output)
	return commandResult{output: output, err: err}
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
