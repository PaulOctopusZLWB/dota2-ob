package m4match

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type LiveConfig struct {
	DataRoot      string
	ReadinessRoot string
	RepoRoot      string
	IdentityPath  string
	Input         io.Reader
	Output        io.Writer
}

type liveBoundary struct {
	Pregame            bool
	Zero               bool
	Post               bool
	Aborted            bool
	RecordingFinalized bool
	OperatorComplete   bool
	PrivacySafe        bool
	Reconciled         bool
	NoCacheRecovery    bool
	MeasurementsPassed bool
	Last               uint64
}

func Live(ctx context.Context, config LiveConfig) (Readiness, error) {
	preflight, err := Verify(ctx, config.ReadinessRoot, config.RepoRoot, "preflight")
	if err != nil || !preflight.Ready {
		return Readiness{}, errors.New("exact environment preflight is not ready")
	}
	identity, err := readIdentity(config.IdentityPath)
	if err != nil {
		return Readiness{}, err
	}
	root, err := safeRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return Readiness{}, err
	}
	if entries, statErr := os.ReadDir(root); statErr == nil && len(entries) != 0 {
		return Readiness{}, errors.New("live root must be fresh and empty")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Readiness{}, err
	}
	artifacts, err := Prepare(root, 1920, 1080)
	if err != nil {
		return Readiness{}, err
	}
	sessionID := "ti-" + identity.MatchID
	policyArtifacts, err := writeLiveArtifacts(root, sessionID)
	if err != nil {
		return Readiness{}, err
	}
	artifacts = append(artifacts, policyArtifacts...)
	preflightBinary := filepath.Join(config.ReadinessRoot, "application/dota2-ob")
	liveBinary := filepath.Join(root, "application/dota2-ob")
	if err := copyFile(preflightBinary, liveBinary, 0o700); err != nil {
		return Readiness{}, err
	}
	hash, size, err := fileSHA(liveBinary)
	if err != nil {
		return Readiness{}, err
	}
	artifacts = append(artifacts, Artifact{Path: "application/dota2-ob", SHA256: hash, Bytes: size})

	installedConfig, err := installGSIConfig(root)
	if err != nil {
		return Readiness{}, err
	}
	defer os.Remove(installedConfig)
	productLog, _ := os.OpenFile(filepath.Join(root, "evidence/logs/product-live.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	defer productLog.Close()
	tokenPath := filepath.Join(root, "runtime/operator.token")
	product := exec.CommandContext(ctx, liveBinary,
		"--policy-mode", "v3-live-only", "--data-dir", filepath.Join(root, "data/sessions"), "--session-id", sessionID,
		"--addr", CaptureAddress, "--delivery-addr", DeliveryAddress, "--operator-token-file", tokenPath,
		"--history-binding-file", filepath.Join(root, "config/policy/history_availability_binding_v1.json"),
		"--live-only-lineage-file", filepath.Join(root, "config/policy/policy_lineage_manifest_v3.json"),
		"--live-only-release-file", filepath.Join(root, "config/policy/live_only_release_binding_v1.json"))
	product.Stdout, product.Stderr = productLog, productLog
	product.Env = append(os.Environ(), "GODEBUG=schedtrace=5000,scheddetail=1")
	if err := product.Start(); err != nil {
		return Readiness{}, err
	}
	productDone := make(chan error, 1)
	go func() { productDone <- product.Wait() }()
	defer stopProcess(product, productDone, 15*time.Second)
	if err := waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second); err != nil {
		return Readiness{}, err
	}

	obsLog, _ := os.OpenFile(filepath.Join(root, "evidence/logs/obs-live.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	defer obsLog.Close()
	obs := exec.CommandContext(ctx, "flatpak", "run", "--filesystem="+root, "--env=XDG_CONFIG_HOME="+filepath.Join(root, "config"), "--env=XDG_DATA_HOME="+filepath.Join(root, "data"), "--env=XDG_CACHE_HOME="+filepath.Join(root, "cache"), "com.obsproject.Studio", "--profile", "DOT65-P4", "--collection", "DOT65-P4", "--startrecording")
	obs.Dir, obs.Stdout, obs.Stderr = root, obsLog, obsLog
	if err := obs.Start(); err != nil {
		return Readiness{}, err
	}
	obsDone := make(chan error, 1)
	go func() { obsDone <- obs.Wait() }()
	defer stopProcess(obs, obsDone, 30*time.Second)

	fmt.Fprintf(config.Output, "ARMED candidate=%s match_id=%s operator=%s/operator/ overlay=%s/overlay/\n", preflight.CandidateCommit, identity.MatchID, DeliveryOrigin, DeliveryOrigin)
	fmt.Fprintf(config.Output, "After confirming the isolated OBS preview and recording, type: CONFIRM_PREVIEW %s\n", identity.MatchID)
	confirmation, err := bufio.NewReader(config.Input).ReadString('\n')
	if err != nil || strings.TrimSpace(confirmation) != "CONFIRM_PREVIEW "+identity.MatchID {
		return sealFailedLive(root, preflight, artifacts, identity, "preview_not_confirmed")
	}

	rawPath := filepath.Join(root, "data/sessions", sessionID, "raw.jsonl")
	samplesPath := filepath.Join(root, "evidence/samples.jsonl")
	samples, err := os.OpenFile(samplesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Readiness{}, err
	}
	defer samples.Close()
	boundary := liveBoundary{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for !boundary.Post && !boundary.Aborted {
		select {
		case <-ctx.Done():
			boundary.Aborted = true
		case err := <-productDone:
			if err != nil {
				boundary.Aborted = true
			} else if !boundary.Post {
				boundary.Aborted = true
			}
		case <-ticker.C:
			boundary, err = inspectBoundaries(rawPath, identity.MatchID, boundary)
			if err != nil {
				boundary.Aborted = true
			}
			sample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, tokenPath, root)
			_ = json.NewEncoder(samples).Encode(sample)
			_ = samples.Sync()
		}
	}
	if boundary.Aborted {
		return sealFailedLive(root, preflight, artifacts, identity, "aborted_or_incomplete_match")
	}
	catchupDeadline := time.Now().Add(120 * time.Second)
	for {
		sample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, tokenPath, root)
		_ = json.NewEncoder(samples).Encode(sample)
		_ = samples.Sync()
		if sample.Sequence > 0 && sample.ProjectedSequence == sample.Sequence && sample.Lag == 0 && sample.AcceptedCount == sample.Sequence {
			break
		}
		if time.Now().After(catchupDeadline) {
			boundary.Aborted = true
			break
		}
		select {
		case <-ctx.Done():
			boundary.Aborted = true
		case <-time.After(5 * time.Second):
		}
		if boundary.Aborted {
			break
		}
	}
	if boundary.Aborted {
		return sealFailedLive(root, preflight, artifacts, identity, "terminal_reconciliation_timeout")
	}
	finalArtifacts, finalErr := captureFinalEndpoints(root, tokenPath)
	artifacts = append(artifacts, finalArtifacts...)
	if finalErr != nil {
		boundary.Aborted = true
	}
	stopProcess(product, productDone, 15*time.Second)
	stopProcess(obs, obsDone, 30*time.Second)
	validation, validationArtifacts, validationErr := validateCompletedAttempt(root, sessionID)
	artifacts = append(artifacts, validationArtifacts...)
	boundary.RecordingFinalized = validation.RecordingFinalized
	boundary.OperatorComplete = validation.OperatorComplete
	boundary.PrivacySafe = validation.PrivacySafe
	boundary.Reconciled = validation.Reconciled
	boundary.NoCacheRecovery = validation.NoCacheRecovery
	boundary.MeasurementsPassed = validation.MeasurementsPassed
	if finalErr != nil || validationErr != nil {
		boundary.Aborted = true
	}
	return sealCompletedLive(root, preflight, artifacts, identity, boundary)
}

func readIdentity(path string) (LiveIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return LiveIdentity{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	var identity LiveIdentity
	if err := decoder.Decode(&identity); err != nil {
		return identity, err
	}
	values := []string{identity.Tournament, identity.Series, identity.Game, identity.Radiant, identity.Dire, identity.MatchID}
	for _, value := range values {
		if value == "" || len(value) > 160 || strings.ContainsAny(value, "\r\n<>/\\") {
			return identity, errors.New("public match identity is incomplete or unsafe")
		}
	}
	for _, r := range identity.MatchID {
		if r < '0' || r > '9' {
			return identity, errors.New("match_id must be decimal")
		}
	}
	return identity, nil
}

func installGSIConfig(root string) (string, error) {
	directory := filepath.Join(os.Getenv("HOME"), ".local/share/Steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(directory, "gamestate_integration_dota2_ob_dot65.cfg")
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("refusing to overwrite existing DOT-65 GSI config")
	}
	return target, copyFile(filepath.Join(root, "config/dota/gamestate_integration_dota2_ob_m4.cfg"), target, 0o600)
}

func copyFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}

func waitHTTP(ctx context.Context, url string, duration time.Duration) error {
	deadline := time.Now().Add(duration)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := client.Do(request)
		if err == nil && response.StatusCode == http.StatusOK {
			_ = response.Body.Close()
			return nil
		}
		if response != nil {
			_ = response.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("endpoint readiness deadline exceeded")
}

func stopProcess(command *exec.Cmd, done <-chan error, timeout time.Duration) {
	if command == nil || command.Process == nil || command.ProcessState != nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
	}
}

func inspectBoundaries(path, expectedMatch string, prior liveBoundary) (liveBoundary, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return prior, nil
	}
	if err != nil {
		return prior, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	sequence := uint64(0)
	result := prior
	for scanner.Scan() {
		var frame struct {
			Sequence uint64 `json:"sequence"`
			Raw      string `json:"raw_base64"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Sequence != sequence+1 {
			return result, errors.New("raw gap or malformed frame")
		}
		sequence = frame.Sequence
		decoded, err := base64.StdEncoding.Strict().DecodeString(frame.Raw)
		if err != nil {
			return result, err
		}
		var payload struct {
			Map struct {
				Clock   int64       `json:"clock_time"`
				State   string      `json:"game_state"`
				MatchID json.Number `json:"matchid"`
			} `json:"map"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(decoded)))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil {
			continue
		}
		if payload.Map.MatchID.String() != "" && payload.Map.MatchID.String() != expectedMatch {
			return result, errors.New("match identity changed")
		}
		if payload.Map.Clock < 0 {
			result.Pregame = true
		}
		if result.Pregame && payload.Map.Clock >= 0 {
			result.Zero = true
		}
		if payload.Map.State == "DOTA_GAMERULES_STATE_POST_GAME" {
			result.Post = true
		}
		if strings.Contains(payload.Map.State, "DISCONNECT") || strings.Contains(payload.Map.State, "ABANDON") {
			result.Aborted = true
		}
	}
	result.Last = sequence
	return result, scanner.Err()
}

func collectSample(pid, obsPID int, rawPath, tokenPath, root string) Sample {
	sample := Sample{At: time.Now().UTC()}
	sample.Sequence, _ = readRawSequences(rawPath)
	sample.ProcessRSSBytes, sample.ProcessFDs, sample.ProcessThreads, sample.ProcessCPUClockTicks = processMetrics(pid)
	sample.OBSProcessRSSBytes, sample.OBSProcessFDs, _, _ = processMetrics(obsPID)
	sample.ProductGoroutines = goroutineCountFromLog(filepath.Join(root, "evidence/logs/product-live.log"))
	filepath.WalkDir(filepath.Join(root, "data"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, _ := entry.Info()
		if filepath.Base(path) == "raw.jsonl" {
			sample.RawFiles++
			sample.RawBytes += info.Size()
		} else {
			sample.NonRawFiles++
			sample.NonRawBytes += info.Size()
			if strings.HasSuffix(path, ".pcl3") {
				sample.PolicySegments++
				sample.PolicyBytes += info.Size()
			}
		}
		return nil
	})
	token, _ := os.ReadFile(tokenPath)
	if response, err := (&http.Client{Timeout: time.Second}).Get(CaptureOrigin + "/api/status"); err == nil {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		sample.StatusResponseBytes = len(body)
		var status struct {
			Request            uint64 `json:"request_count"`
			Accepted           uint64 `json:"accepted_count"`
			Rejected           uint64 `json:"rejected_count"`
			RawFailures        uint64 `json:"raw_write_failure_count"`
			ProjectionFailures uint64 `json:"post_processing_failure_count"`
			Live               struct {
				Projected uint64 `json:"projected_sequence"`
				HighWater uint64 `json:"high_water"`
				Lag       uint64 `json:"lag_count"`
			} `json:"live_projection"`
		}
		if json.Unmarshal(body, &status) == nil {
			sample.RequestCount, sample.AcceptedCount, sample.RejectedCount = status.Request, status.Accepted, status.Rejected
			sample.RawWriteFailures, sample.ProjectionFailures = status.RawFailures, status.ProjectionFailures
			sample.ProjectedSequence, sample.Lag = status.Live.Projected, status.Live.Lag
		}
	}
	for endpoint, target := range map[string]*string{"/v1/operator/state": &sample.OperatorStateSHA256, "/v1/overlay/state": &sample.OverlayStateSHA256} {
		request, _ := http.NewRequest(http.MethodGet, DeliveryOrigin+endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		request.Header.Set("Origin", DeliveryOrigin)
		if response, err := (&http.Client{Timeout: time.Second}).Do(request); err == nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			*target = payloadSHA(body)
			if strings.Contains(endpoint, "operator") {
				sample.OperatorResponseBytes = len(body)
				var state struct {
					Previews []json.RawMessage `json:"previews"`
				}
				if json.Unmarshal(body, &state) == nil {
					sample.QueueOccupancy = len(state.Previews)
				}
			} else {
				sample.OverlayResponseBytes = len(body)
			}
		}
	}
	sample.OBSRenderedFrames, sample.OBSMissedFrames, sample.OBSSkippedFrames = obsFrames(filepath.Join(root, "evidence/logs/obs-live.log"))
	return sample
}

func processMetrics(pid int) (rss int64, fds, threads int, cpu uint64) {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	if payload, err := os.ReadFile(filepath.Join(base, "statm")); err == nil {
		fields := strings.Fields(string(payload))
		if len(fields) > 1 {
			pages, _ := strconv.ParseInt(fields[1], 10, 64)
			rss = pages * int64(os.Getpagesize())
		}
	}
	if payload, err := os.ReadFile(filepath.Join(base, "status")); err == nil {
		for _, line := range strings.Split(string(payload), "\n") {
			if strings.HasPrefix(line, "Threads:") {
				threads, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Threads:")))
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(base, "fd")); err == nil {
		fds = len(entries)
	}
	if payload, err := os.ReadFile(filepath.Join(base, "stat")); err == nil {
		close := strings.LastIndex(string(payload), ")")
		if close >= 0 {
			fields := strings.Fields(string(payload)[close+1:])
			if len(fields) > 12 {
				user, _ := strconv.ParseUint(fields[11], 10, 64)
				system, _ := strconv.ParseUint(fields[12], 10, 64)
				cpu = user + system
			}
		}
	}
	return
}

func goroutineCountFromLog(path string) int {
	payload, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	marker := bytes.LastIndex(payload, []byte("SCHED "))
	if marker < 0 {
		return 0
	}
	count := 0
	for _, line := range bytes.Split(payload[marker:], []byte{'\n'}) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("G")) {
			count++
		}
	}
	return count
}

func obsFrames(path string) (rendered, missed, skipped uint64) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return
	}
	text := string(payload)
	parseLast := func(prefix string) uint64 {
		index := strings.LastIndex(text, prefix)
		if index < 0 {
			return 0
		}
		fields := strings.Fields(text[index+len(prefix):])
		if len(fields) == 0 {
			return 0
		}
		value, _ := strconv.ParseUint(strings.Trim(fields[0], "(),"), 10, 64)
		return value
	}
	return parseLast("Total frames output:"), parseLast("lagged frames"), parseLast("number of skipped frames due to encoding lag:")
}

func sealFailedLive(root string, preflight Readiness, artifacts []Artifact, identity LiveIdentity, reason string) (Readiness, error) {
	return sealLive(root, preflight, artifacts, identity, liveBoundary{}, []string{reason})
}

func sealCompletedLive(root string, preflight Readiness, artifacts []Artifact, identity LiveIdentity, boundary liveBoundary) (Readiness, error) {
	failures := []string{"independent_p4_acceptance_required"}
	if !boundary.Pregame {
		failures = append(failures, "late_join")
	}
	if !boundary.Zero {
		failures = append(failures, "missing_game_zero")
	}
	if !boundary.Post {
		failures = append(failures, "missing_post_game")
	}
	if !boundary.RecordingFinalized {
		failures = append(failures, "recording_not_finalized")
	}
	if !boundary.OperatorComplete {
		failures = append(failures, "operator_script_incomplete")
	}
	if !boundary.PrivacySafe {
		failures = append(failures, "private_payload_detected")
	}
	if !boundary.Reconciled {
		failures = append(failures, "evidence_reconciliation_failed")
	}
	if !boundary.NoCacheRecovery {
		failures = append(failures, "no_cache_recovery_failed")
	}
	if !boundary.MeasurementsPassed {
		failures = append(failures, "measurement_bounds_failed")
	}
	return sealLive(root, preflight, artifacts, identity, boundary, failures)
}

func sealLive(root string, preflight Readiness, artifacts []Artifact, identity LiveIdentity, boundary liveBoundary, failures []string) (Readiness, error) {
	evidence := Evidence{SchemaVersion: SchemaVersion, Mode: "live", CandidateCommit: preflight.CandidateCommit, CandidateParent: AcceptedFunctionalBase,
		AcceptedBase: AcceptedFunctionalBase, AcceptedSpec: AcceptedP4Spec, FixtureSHA256: CapturedScheduleSHA256, GoldenSHA256: ProductionGoldenSHA256,
		Bounds: AcceptedBounds(), Faults: append([]string(nil), RequiredFaults...), Artifacts: artifacts, StartBoundary: "capture/operator/overlay/OBS armed before manual preview confirmation",
		GameZeroBoundary: "negative clock followed by nonnegative clock", PostGameBoundary: "normal post-game GSI state", RecordingBoundary: "OBS finalization required",
		OperatorScript: HumanInstruction, NonResumable: true, SyntheticOnly: false, ClaimsP4: false, ReadinessIssueText: HumanInstruction}
	evidence.Checks = []Check{{"pregame_boundary", boundary.Pregame, "negative game clock observed"}, {"game_zero_boundary", boundary.Zero, "clock zero transition observed"}, {"post_game_boundary", boundary.Post, "normal post-game state observed"}, {"recording_finalized", boundary.RecordingFinalized, "non-empty MKV and no partial recording"}, {"operator_script", boundary.OperatorComplete, "required durable command actions present"}, {"privacy", boundary.PrivacySafe, "raw payload key scan passed"}, {"reconciliation", boundary.Reconciled, "raw/cursor/policy identities reconcile"}, {"no_cache_recovery", boundary.NoCacheRecovery, "isolated cache-free evidence rebuild byte-matched"}, {"measurement_bounds", boundary.MeasurementsPassed, "five-second samples satisfy accepted resource/body/state bounds"}, {"public_match_identity", identity.MatchID != "", "public identity supplied"}, {"independent_acceptance", false, "live command never self-accepts P4"}}
	for _, failure := range failures {
		evidence.Checks = append(evidence.Checks, Check{ID: failure, Passed: false, Detail: "sealed live attempt failure"})
	}
	sortEvidence(&evidence)
	index, err := canonical(evidence)
	if err != nil {
		return Readiness{}, err
	}
	if err := writePrivate(filepath.Join(root, "evidence/canonical/evidence-index.json"), index); err != nil {
		return Readiness{}, err
	}
	allFailures := make([]string, 0)
	for _, check := range evidence.Checks {
		if !check.Passed {
			allFailures = append(allFailures, check.ID)
		}
	}
	result := Readiness{SchemaVersion: ReadinessSchemaVersion, Ready: false, Mode: "live", CandidateCommit: preflight.CandidateCommit, EvidenceIndexSHA256: payloadSHA(index), Failures: allFailures, ClaimsP4: false, HumanInstruction: HumanInstruction}
	if err := writeJSON(filepath.Join(root, "evidence/readiness.json"), result, 0o600); err != nil {
		return Readiness{}, err
	}
	return result, nil
}
