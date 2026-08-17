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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	Pregame                bool
	Zero                   bool
	Post                   bool
	Aborted                bool
	RecordingFinalized     bool
	OperatorComplete       bool
	PrivacySafe            bool
	Reconciled             bool
	NoCacheRecovery        bool
	MeasurementsPassed     bool
	VisibilityPassed       bool
	OperatorInputBounds    bool
	CleanShutdown          bool
	Last                   uint64
	Frames                 uint64
	ArmedAt                time.Time
	FirstReceivedAt        time.Time
	LastReceivedAt         time.Time
	LastClock              int64
	WinnerObserved         bool
	IdentityContinuous     bool
	DotaProvenance         bool
	ConfigSHA256           string
	ConfigUserOnly         bool
	ExclusiveListener      bool
	KnownProducerAbsent    bool
	CorrelationStartSHA256 string
	CorrelationEndSHA256   string
	PaulConfirmed          bool
	Requests               uint64
	Accepted               uint64
	Rejected               uint64
	RawRecords             uint64
	TerminalOutcomes       uint64
	RecordingCoextensive   bool
}

type operatorInputFrame struct {
	Sequence   uint64 `json:"sequence"`
	ReceivedAt string `json:"received_at"`
	BodyBase64 string `json:"body_base64"`
	BodySHA256 string `json:"body_sha256"`
}

type deliveryProxy struct {
	server  *http.Server
	done    chan error
	once    sync.Once
	stopErr error
}

func readLineBefore(ctx context.Context, input io.Reader, deadline time.Time) (string, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return "", context.DeadlineExceeded
	}
	type result struct {
		line string
		err  error
	}
	completed := make(chan result, 1)
	go func() { line, err := bufio.NewReader(input).ReadString('\n'); completed <- result{line, err} }()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", context.DeadlineExceeded
	case got := <-completed:
		return got.line, got.err
	}
}

func startDeliveryProxy(root string) (*deliveryProxy, error) {
	journalPath := filepath.Join(root, "evidence/raw-operator-input.jsonl")
	if err := rootMkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		return nil, err
	}
	journal, err := rootOpenFile(journalPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var sequence uint64
	var replayed bool
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(request.Body, 1<<20))
		_ = request.Body.Close()
		if readErr != nil || len(body) > 1<<20 {
			http.Error(writer, "proxy_request_invalid", http.StatusRequestEntityTooLarge)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/v1/operator/commands" {
			mu.Lock()
			copies := 1
			if !replayed {
				copies = 2
				replayed = true
			}
			var marshalErr error
			for copyIndex := 0; copyIndex < copies && marshalErr == nil; copyIndex++ {
				sequence++
				frame := operatorInputFrame{Sequence: sequence, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano), BodyBase64: base64.StdEncoding.EncodeToString(body), BodySHA256: payloadSHA(body)}
				var payload []byte
				payload, marshalErr = canonical(frame)
				if marshalErr == nil {
					_, marshalErr = journal.Write(payload)
				}
			}
			if marshalErr == nil {
				marshalErr = journal.Sync()
			}
			mu.Unlock()
			if marshalErr != nil {
				http.Error(writer, "operator_input_journal_failed", http.StatusServiceUnavailable)
				return
			}
		}
		forward := func() (int, http.Header, []byte, error) {
			upstream, _ := http.NewRequestWithContext(request.Context(), request.Method, ProductDeliveryOrigin+request.URL.RequestURI(), bytes.NewReader(body))
			upstream.Header = request.Header.Clone()
			if upstream.Header.Get("Origin") == DeliveryOrigin {
				upstream.Header.Set("Origin", ProductDeliveryOrigin)
			}
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(upstream)
			if err != nil {
				return 0, nil, nil, err
			}
			defer response.Body.Close()
			responseBody, bodyErr := io.ReadAll(io.LimitReader(response.Body, AcceptedBounds().OtherAPIBytes+1))
			if bodyErr != nil || int64(len(responseBody)) > AcceptedBounds().OtherAPIBytes {
				return 0, nil, nil, errors.New("delivery response bound")
			}
			return response.StatusCode, response.Header.Clone(), responseBody, nil
		}
		status, headers, responseBody, err := forward()
		if err != nil {
			http.Error(writer, "delivery_upstream_unavailable", http.StatusBadGateway)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/v1/operator/commands" {
			mu.Lock()
			shouldReplay := sequence == 2
			mu.Unlock()
			if shouldReplay {
				replayStatus, _, replayBody, replayErr := forward()
				if replayErr != nil || replayStatus != status || !bytes.Equal(replayBody, responseBody) {
					http.Error(writer, "operator_replay_unstable", http.StatusBadGateway)
					return
				}
				var command struct {
					CommandID string `json:"command_id"`
				}
				if json.Unmarshal(body, &command) != nil || command.CommandID == "" || writeJSON(filepath.Join(root, "evidence/canonical/operator-replay-proof.json"), struct {
					SchemaVersion     string `json:"schema_version"`
					CommandID         string `json:"command_id"`
					RequestSHA256     string `json:"request_sha256"`
					ResponseSHA256    string `json:"response_sha256"`
					Status            int    `json:"status"`
					TransportAttempts int    `json:"transport_attempts"`
					DurableEffects    int    `json:"durable_effects"`
				}{"operator_replay_proof.v1", command.CommandID, payloadSHA(body), payloadSHA(responseBody), status, 2, 1}, 0o600) != nil {
					http.Error(writer, "operator_replay_proof_failed", http.StatusServiceUnavailable)
					return
				}
			}
		}
		for key, values := range headers {
			for _, value := range values {
				writer.Header().Add(key, strings.ReplaceAll(value, ProductDeliveryOrigin, DeliveryOrigin))
			}
		}
		writer.WriteHeader(status)
		_, _ = writer.Write(responseBody)
	})
	listener, err := net.Listen("tcp", DeliveryAddress)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	proxy := &deliveryProxy{server: &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second}, done: make(chan error, 1)}
	go func() {
		err := proxy.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		proxy.done <- errors.Join(err, journal.Close())
	}()
	return proxy, nil
}

func (proxy *deliveryProxy) stop() error {
	if proxy == nil {
		return nil
	}
	proxy.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := proxy.server.Shutdown(ctx)
		serveErr := <-proxy.done
		proxy.stopErr = errors.Join(shutdownErr, serveErr)
	})
	return proxy.stopErr
}

func Live(ctx context.Context, config LiveConfig) (result Readiness, retErr error) {
	preflight, err := Verify(ctx, config.ReadinessRoot, config.RepoRoot, "preflight")
	if err != nil || !preflight.Ready {
		return Readiness{}, errors.New("exact environment preflight is not ready")
	}
	readinessLease, err := acquireExistingRoot(config.ReadinessRoot, config.RepoRoot)
	if err != nil {
		return Readiness{}, err
	}
	defer readinessLease.Close()
	preflightIndex, err := rootReadFile(filepath.Join(readinessLease.abs, "evidence/canonical/evidence-index.json"))
	if err != nil {
		return Readiness{}, err
	}
	var preflightEvidence Evidence
	if json.Unmarshal(preflightIndex, &preflightEvidence) != nil {
		return Readiness{}, errors.New("verified preflight identity cannot be loaded")
	}
	identity, err := readIdentity(config.IdentityPath)
	if err != nil {
		return Readiness{}, err
	}
	lease, err := acquireFreshRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return Readiness{}, err
	}
	defer lease.Close()
	root := lease.abs
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
	defer func() { retErr = errors.Join(retErr, os.Remove(installedConfig)) }()
	configHash, _, err := fileSHA(installedConfig)
	if err != nil {
		return Readiness{}, err
	}
	configInfo, err := os.Stat(installedConfig)
	if err != nil || configInfo.Mode().Perm() != 0o600 {
		return Readiness{}, errors.New("installed GSI config is not user-only")
	}
	proxy, err := startDeliveryProxy(root)
	if err != nil {
		return Readiness{}, err
	}
	defer func() { retErr = errors.Join(retErr, proxy.stop()) }()
	productLog, err := rootOpenFile(filepath.Join(root, "evidence/logs/product-live.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Readiness{}, err
	}
	defer func() { retErr = errors.Join(retErr, productLog.Close()) }()
	tokenPath := filepath.Join(root, "runtime/operator.token")
	product := exec.CommandContext(ctx, liveBinary,
		"--policy-mode", "v3-live-only", "--data-dir", filepath.Join(root, "data/sessions"), "--session-id", sessionID,
		"--addr", CaptureAddress, "--delivery-addr", ProductDeliveryAddress, "--operator-token-file", tokenPath,
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
	defer func() { retErr = errors.Join(retErr, stopProcess(product, productDone, 15*time.Second)) }()
	if err := waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second); err != nil {
		return Readiness{}, err
	}
	if err := proveProductListener(CaptureAddress, product.Process.Pid); err != nil {
		return Readiness{}, err
	}

	obsLog, err := rootOpenFile(filepath.Join(root, "evidence/logs/obs-live.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Readiness{}, err
	}
	defer func() { retErr = errors.Join(retErr, obsLog.Close()) }()
	obs := exec.CommandContext(ctx, "flatpak", "run", "--filesystem="+root, "--env=XDG_CONFIG_HOME="+filepath.Join(root, "config"), "--env=XDG_DATA_HOME="+filepath.Join(root, "data"), "--env=XDG_CACHE_HOME="+filepath.Join(root, "cache"), "com.obsproject.Studio", "--profile", "DOT65-P4", "--collection", "DOT65-P4", "--startrecording")
	obs.Dir, obs.Stdout, obs.Stderr = root, obsLog, obsLog
	if err := obs.Start(); err != nil {
		return Readiness{}, err
	}
	obsDone := make(chan error, 1)
	go func() { obsDone <- obs.Wait() }()
	defer func() { retErr = errors.Join(retErr, stopProcess(obs, obsDone, 30*time.Second)) }()
	startCorrelation, err := waitStableProcessCorrelation(ctx, os.Getpid(), product.Process.Pid, obs.Process.Pid, 20*time.Second)
	if err != nil {
		return Readiness{}, err
	}
	abortLive := func(reason string) (Readiness, error) {
		productStopErr := stopProcess(product, productDone, 15*time.Second)
		obsStopErr := stopProcess(obs, obsDone, 30*time.Second)
		proxyStopErr := proxy.stop()
		sealed, sealErr := sealFailedLive(root, preflight, preflightEvidence, artifacts, identity, reason)
		return sealed, errors.Join(sealErr, productStopErr, obsStopErr, proxyStopErr)
	}

	boundary := liveBoundary{ArmedAt: time.Now().UTC(), IdentityContinuous: true, DotaProvenance: true, ConfigSHA256: configHash, ConfigUserOnly: true, ExclusiveListener: true, KnownProducerAbsent: true, CorrelationStartSHA256: startCorrelation.SHA256}
	startDeadline := boundary.ArmedAt.Add(MaxArmingWindow)
	fmt.Fprintf(config.Output, "ARMED candidate=%s match_id=%s operator=%s/operator/ overlay=%s/overlay/\n", preflight.CandidateCommit, identity.MatchID, DeliveryOrigin, DeliveryOrigin)
	confirmationPhrase := fmt.Sprintf("CONFIRM_PREVIEW %s %s %s %s %s %s DOTA_PID=%d", identity.MatchID, identity.Tournament, identity.Series, identity.Game, identity.Radiant, identity.Dire, identity.DotaPID)
	fmt.Fprintf(config.Output, "After confirming the isolated OBS preview, recording indicator, official identity, and Dota process, type: %s\n", confirmationPhrase)
	confirmation, err := readLineBefore(ctx, config.Input, startDeadline)
	if err != nil || strings.TrimSpace(confirmation) != confirmationPhrase {
		return abortLive("preview_not_confirmed")
	}

	rawPath := filepath.Join(root, "data/sessions", sessionID, "raw.jsonl")
	samplesPath := filepath.Join(root, "evidence/samples.jsonl")
	samples, err := rootOpenFile(samplesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Readiness{}, err
	}
	defer func() { retErr = errors.Join(retErr, samples.Close()) }()
	visibilityFile, err := rootOpenFile(filepath.Join(root, "evidence/visibility.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Readiness{}, err
	}
	defer func() { retErr = errors.Join(retErr, visibilityFile.Close()) }()
	boundary.PaulConfirmed = true
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	visibilityTicker := time.NewTicker(100 * time.Millisecond)
	defer visibilityTicker.Stop()
	startTimer := time.NewTimer(time.Until(startDeadline))
	defer startTimer.Stop()
	for !boundary.Post && !boundary.Aborted {
		select {
		case <-ctx.Done():
			boundary.Aborted = true
		case <-startTimer.C:
			if !boundary.Pregame {
				boundary.Aborted = true
			}
		case err := <-productDone:
			if err != nil {
				boundary.Aborted = true
			} else if !boundary.Post {
				boundary.Aborted = true
			}
		case <-ticker.C:
			boundary, err = inspectBoundaries(rawPath, identity, boundary)
			if err != nil {
				boundary.Aborted = true
			}
			if boundary.Pregame {
				if !startTimer.Stop() {
					select {
					case <-startTimer.C:
					default:
					}
				}
			}
			sample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, tokenPath, root, sessionID, identity.MatchID)
			_ = json.NewEncoder(samples).Encode(sample)
			_ = samples.Sync()
		case <-visibilityTicker.C:
			visibility := collectVisibility(rawPath, tokenPath)
			if json.NewEncoder(visibilityFile).Encode(visibility) != nil || visibilityFile.Sync() != nil || !visibility.TelemetryComplete {
				boundary.Aborted = true
			}
		}
	}
	if boundary.Aborted {
		return abortLive("aborted_or_incomplete_match")
	}
	catchupDeadline := time.Now().Add(120 * time.Second)
	for {
		sample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, tokenPath, root, sessionID, identity.MatchID)
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
		return abortLive("terminal_reconciliation_timeout")
	}
	finalArtifacts, finalErr := captureFinalEndpoints(root, tokenPath)
	artifacts = append(artifacts, finalArtifacts...)
	finalSample := collectSample(product.Process.Pid, obs.Process.Pid, rawPath, tokenPath, root, sessionID, identity.MatchID)
	boundary.Requests, boundary.Accepted, boundary.Rejected = finalSample.RequestCount, finalSample.AcceptedCount, finalSample.RejectedCount
	boundary.RawRecords, boundary.TerminalOutcomes = finalSample.Sequence, finalSample.ProjectedSequence
	endCorrelation, correlationErr := captureProcessCorrelation(os.Getpid(), product.Process.Pid, obs.Process.Pid)
	if correlationErr != nil || endCorrelation.SHA256 != boundary.CorrelationStartSHA256 {
		boundary.KnownProducerAbsent = false
		boundary.Aborted = true
	} else {
		boundary.CorrelationEndSHA256 = endCorrelation.SHA256
	}
	if finalErr != nil {
		boundary.Aborted = true
	}
	productStopErr := stopProcess(product, productDone, 15*time.Second)
	obsStopErr := stopProcess(obs, obsDone, 30*time.Second)
	proxyStopErr := proxy.stop()
	validation, validationArtifacts, validationErr := validateCompletedAttempt(root, sessionID, productStopErr == nil, obsStopErr == nil)
	artifacts = append(artifacts, validationArtifacts...)
	boundary.RecordingFinalized = validation.RecordingFinalized
	boundary.OperatorComplete = validation.OperatorComplete
	boundary.PrivacySafe = validation.PrivacySafe
	boundary.Reconciled = validation.Reconciled
	boundary.NoCacheRecovery = validation.NoCacheRecovery
	boundary.MeasurementsPassed = validation.MeasurementsPassed
	boundary.VisibilityPassed = validation.VisibilityPassed
	boundary.OperatorInputBounds = validation.OperatorInputBounds
	boundary.CleanShutdown = productStopErr == nil && obsStopErr == nil && proxyStopErr == nil && validation.RecoveryCleanShutdown
	boundary.RecordingCoextensive = boundary.ArmedAt.Before(boundary.FirstReceivedAt) && validation.RecordingFinalized
	if finalErr != nil || correlationErr != nil || validationErr != nil || productStopErr != nil || obsStopErr != nil || proxyStopErr != nil {
		boundary.Aborted = true
	}
	result, retErr = sealCompletedLive(root, preflight, preflightEvidence, artifacts, identity, boundary)
	retErr = errors.Join(retErr, productStopErr, obsStopErr, proxyStopErr)
	return result, retErr
}

func collectVisibility(rawPath, tokenPath string) VisibilitySample {
	sample := VisibilitySample{At: time.Now().UTC()}
	sequence, sequenceErr := readRawSequences(rawPath)
	sample.RawSequence = sequence
	token, tokenErr := os.ReadFile(tokenPath)
	request, _ := http.NewRequest(http.MethodGet, DeliveryOrigin+"/v1/overlay/state", nil)
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Origin", DeliveryOrigin)
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		return sample
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, int64(AcceptedBounds().OverlayStateBytes)+1))
	var state struct {
		SessionID  string          `json:"session_id"`
		Visibility string          `json:"visibility"`
		HealthCode string          `json:"health_code"`
		DecisionID string          `json:"decision_id"`
		Claim      json.RawMessage `json:"claim"`
	}
	decodeErr := json.Unmarshal(body, &state)
	sample.SessionID, sample.Visibility, sample.HealthCode, sample.DecisionID = state.SessionID, state.Visibility, state.HealthCode, state.DecisionID
	sample.ClaimPresent = len(state.Claim) > 0 && string(state.Claim) != "null"
	sample.StateSHA256 = payloadSHA(body)
	sample.TelemetryComplete = sequenceErr == nil && tokenErr == nil && response.StatusCode == http.StatusOK && readErr == nil && decodeErr == nil && len(body) <= int(AcceptedBounds().OverlayStateBytes) && state.SessionID != "" && (state.Visibility == "visible" || state.Visibility == "hidden") && !(state.Visibility == "hidden" && sample.ClaimPresent)
	return sample
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
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return identity, errors.New("public match identity must contain exactly one JSON value")
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
	if strings.Trim(identity.MatchID, "0") == "" {
		return identity, errors.New("match_id must be nonzero")
	}
	parsedURL, err := url.Parse(identity.OfficialSourceURL)
	if err != nil || parsedURL.Scheme != "https" || (parsedURL.Host != "www.dota2.com.cn" && parsedURL.Host != "www.dota2.com") || !strings.Contains(strings.ToLower(parsedURL.Path), "international") {
		return identity, errors.New("official TI identity source URL is invalid")
	}
	confirmedAt, err := time.Parse(time.RFC3339Nano, identity.ConfirmedAt)
	if err != nil || confirmedAt.UTC().Format(time.RFC3339Nano) != identity.ConfirmedAt || time.Since(confirmedAt) < -time.Minute || time.Since(confirmedAt) > 15*time.Minute {
		return identity, errors.New("public identity confirmation is stale or noncanonical")
	}
	if identity.DotaPID <= 1 || identity.DotaProcessStartTicks == 0 {
		return identity, errors.New("Dota process provenance is incomplete")
	}
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(identity.DotaPID), "comm"))
	if err != nil || strings.ToLower(strings.TrimSpace(string(comm))) != "dota2" {
		return identity, errors.New("confirmed Dota process is not running")
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(identity.DotaPID), "exe"))
	if err != nil {
		return identity, errors.New("cannot resolve confirmed Dota executable")
	}
	hash, _, err := fileSHA(executable)
	if err != nil || hash != identity.DotaExecutableSHA256 {
		return identity, errors.New("confirmed Dota executable hash mismatch")
	}
	_, _, _, _, startTicks, metricErr := processMetricsOne(identity.DotaPID)
	if metricErr != nil || startTicks != identity.DotaProcessStartTicks {
		return identity, errors.New("confirmed Dota process start identity mismatch")
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

func validateLocalhostGSITrust(e LocalhostGSITrustEvidence) error {
	if len(e.ConfigSHA256) != 64 || e.ConfigURI != "http://"+CaptureAddress+"/gsi" || e.ListenerURI != e.ConfigURI || !e.ConfigUserOnly || !e.ExclusiveListener || !e.ListenerProductOwned || !e.DotaProcessStable || !e.KnownProducerAbsent || len(e.CorrelationStartSHA256) != 64 || e.CorrelationEndSHA256 != e.CorrelationStartSHA256 || !e.IdentityContinuous || !e.RecordingCoextensive || !e.PaulConfirmedIdentity || e.PerRequestAttested {
		return errors.New("localhost GSI trust identity or provenance mismatch")
	}
	if e.Requests == 0 || e.Requests != e.Accepted+e.Rejected || e.Accepted != e.RawRecords || e.RawRecords != e.TerminalOutcomes {
		return errors.New("localhost GSI request/record/outcome reconciliation mismatch")
	}
	if len(e.ResidualReasonCodes) != 1 || e.ResidualReasonCodes[0] != "localhost_gsi_sender_unattested" {
		return errors.New("localhost GSI residual limitation mismatch")
	}
	return nil
}

func copyFile(source, target string, mode os.FileMode) error {
	in, err := rootOpenFile(source, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := rootMkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := rootOpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = rootRemove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = rootRemove(target)
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

func stopProcess(command *exec.Cmd, done <-chan error, timeout time.Duration) error {
	if command == nil || command.Process == nil || command.ProcessState != nil {
		return nil
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		killErr := command.Process.Kill()
		waitErr := <-done
		return errors.Join(errors.New("process did not stop cleanly before deadline"), killErr, waitErr)
	}
}

func inspectBoundaries(path string, identity LiveIdentity, prior liveBoundary) (liveBoundary, error) {
	if err := verifyDotaProcess(identity); err != nil {
		prior.DotaProvenance = false
		return prior, err
	}
	return inspectBoundaryRecords(path, identity, prior)
}

func inspectBoundaryRecords(path string, identity LiveIdentity, prior liveBoundary) (liveBoundary, error) {
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
			Sequence   uint64 `json:"sequence"`
			Raw        string `json:"raw_base64"`
			ReceivedAt string `json:"received_at"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Sequence != sequence+1 {
			return result, errors.New("raw gap or malformed frame")
		}
		sequence = frame.Sequence
		decoded, err := base64.StdEncoding.Strict().DecodeString(frame.Raw)
		if err != nil {
			return result, err
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, frame.ReceivedAt)
		if err != nil || receivedAt.UTC().Format(time.RFC3339Nano) != frame.ReceivedAt {
			return result, errors.New("raw receive time is missing or noncanonical")
		}
		previousReceived := result.LastReceivedAt
		if sequence == 1 {
			if result.ArmedAt.IsZero() || receivedAt.Before(result.ArmedAt) {
				return result, errors.New("accepted input predates complete capture/OBS arming")
			}
			result.FirstReceivedAt = receivedAt
		} else if receivedAt.Before(result.LastReceivedAt) || receivedAt.Sub(result.LastReceivedAt) > 7*time.Second {
			return result, errors.New("unexplained GSI receive gap or time regression")
		}
		result.LastReceivedAt = receivedAt
		var payload struct {
			Map struct {
				Clock   int64       `json:"clock_time"`
				State   string      `json:"game_state"`
				MatchID json.Number `json:"matchid"`
				WinTeam string      `json:"win_team"`
			} `json:"map"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(decoded)))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil {
			return result, errors.New("accepted raw frame cannot be decoded for match continuity")
		}
		if payload.Map.MatchID.String() == "" || payload.Map.MatchID.String() == "0" || payload.Map.MatchID.String() != identity.MatchID {
			result.IdentityContinuous = false
			return result, errors.New("match identity missing, zero, or changed")
		}
		if sequence > 1 && payload.Map.Clock < result.LastClock && payload.Map.State != "DOTA_GAMERULES_STATE_PRE_GAME" {
			return result, errors.New("game clock regressed without a new attempt boundary")
		}
		if sequence > 1 && payload.Map.Clock-result.LastClock > int64(receivedAt.Sub(previousReceived)/time.Second)+2 {
			return result, errors.New("game clock advanced faster than the retained live receive cadence")
		}
		if payload.Map.Clock < 0 {
			if result.Zero || result.Post {
				return result, errors.New("pregame state revived after game start")
			}
			result.Pregame = true
		}
		if result.Pregame && payload.Map.Clock >= 0 {
			result.Zero = true
		}
		if payload.Map.State == "DOTA_GAMERULES_STATE_POST_GAME" {
			if !result.Zero || (payload.Map.WinTeam != "radiant" && payload.Map.WinTeam != "dire") {
				return result, errors.New("post-game state lacks normal winner/finalization identity")
			}
			result.Post = true
			result.WinnerObserved = true
		}
		upperState := strings.ToUpper(payload.Map.State)
		if strings.Contains(upperState, "DISCONNECT") || strings.Contains(upperState, "ABANDON") || strings.Contains(upperState, "REMAKE") {
			result.Aborted = true
		}
		result.LastClock = payload.Map.Clock
		result.Frames++
	}
	result.Last = sequence
	return result, scanner.Err()
}

func verifyDotaProcess(identity LiveIdentity) error {
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(identity.DotaPID), "comm"))
	if err != nil || strings.ToLower(strings.TrimSpace(string(comm))) != "dota2" {
		return errors.New("bound Dota process exited or changed")
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(identity.DotaPID), "exe"))
	if err != nil {
		return err
	}
	hash, _, err := fileSHA(executable)
	if err != nil || hash != identity.DotaExecutableSHA256 {
		return errors.New("bound Dota executable changed")
	}
	_, _, _, _, start, err := processMetricsOne(identity.DotaPID)
	if err != nil || start != identity.DotaProcessStartTicks {
		return errors.New("bound Dota process instance changed")
	}
	return nil
}

// verifyDotaProcessAt is shared by the accepted live harness and rehearsal
// producer. It binds the opened /proc identity, including the executable path
// fact, before comparing the accepted PID/hash/start-tick contract.
func verifyDotaProcessAt(procRoot string, identity LiveIdentity) (processCorrelationIdentity, string, error) {
	observed, err := readProcessCorrelationIdentity(procRoot, identity.DotaPID)
	if err != nil || strings.ToLower(observed.Comm) != "dota2" {
		return processCorrelationIdentity{}, "", errors.New("bound Dota process exited or changed")
	}
	if observed.ExecutableSHA256 != identity.DotaExecutableSHA256 {
		return processCorrelationIdentity{}, "", errors.New("bound Dota executable changed")
	}
	if observed.StartTicks != identity.DotaProcessStartTicks {
		return processCorrelationIdentity{}, "", errors.New("bound Dota process instance changed")
	}
	executable, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(identity.DotaPID), "exe"))
	if err != nil {
		return processCorrelationIdentity{}, "", err
	}
	return observed, payloadSHA([]byte(executable)), nil
}

func collectSample(pid, obsPID int, rawPath, tokenPath, root, expectedSession, expectedMatch string) Sample {
	sample := Sample{At: time.Now().UTC()}
	var telemetryOK = true
	var err error
	sample.Sequence, err = readRawSequences(rawPath)
	telemetryOK = telemetryOK && err == nil
	productTree, productErr := processTreeMetrics(pid)
	obsTree, obsErr := processTreeMetrics(obsPID)
	telemetryOK = telemetryOK && productErr == nil && obsErr == nil
	sample.ProductTreeProcesses, sample.ProductTreeRSSBytes, sample.ProductTreeFDs, sample.ProductTreeCPUClockTicks = productTree.Processes, productTree.RSS, productTree.FDs, productTree.CPU
	sample.OBSProcessTreeCount, sample.OBSProcessTreeRSSBytes, sample.OBSProcessTreeFDs, sample.OBSProcessTreeCPUClockTicks = obsTree.Processes, obsTree.RSS, obsTree.FDs, obsTree.CPU
	sample.OBSBrowserProcesses, sample.OBSBrowserCPUClockTicks = obsTree.BrowserProcesses, obsTree.BrowserCPU
	sample.ProcessRSSBytes, sample.ProcessFDs, sample.ProcessThreads, sample.ProcessCPUClockTicks = productTree.RSS, productTree.FDs, productTree.Threads, productTree.CPU
	sample.OBSProcessRSSBytes, sample.OBSProcessFDs = obsTree.RSS, obsTree.FDs
	sample.ClockTicksPerSecond, err = clockTicksPerSecond()
	telemetryOK = telemetryOK && err == nil && sample.ClockTicksPerSecond > 0
	sample.CrossPlaneMatchID, err = lastRawMatchIdentity(rawPath)
	telemetryOK = telemetryOK && err == nil && sample.CrossPlaneMatchID == expectedMatch
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
	sample.LargestGSIRequestBytes, err = largestRawPayload(rawPath)
	telemetryOK = telemetryOK && err == nil && sample.LargestGSIRequestBytes > 0
	token, _ := os.ReadFile(tokenPath)
	if response, err := (&http.Client{Timeout: time.Second}).Get(CaptureOrigin + "/api/status"); err == nil {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		sample.StatusResponseBytes = len(body)
		var status struct {
			SessionID          string `json:"session_id"`
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
			RuntimeCapacity struct {
				SchemaVersion string `json:"schema_version"`
				Notification  int    `json:"notification_capacity"`
				Candidate     int    `json:"candidate_queue_capacity"`
				PolicyHealthy bool   `json:"policy_healthy"`
			} `json:"runtime_capacity"`
		}
		if json.Unmarshal(body, &status) == nil {
			sample.RequestCount, sample.AcceptedCount, sample.RejectedCount = status.Request, status.Accepted, status.Rejected
			sample.RawWriteFailures, sample.ProjectionFailures = status.RawFailures, status.ProjectionFailures
			sample.NotificationCapacity = status.RuntimeCapacity.Notification
			sample.CandidateQueueCapacity = status.RuntimeCapacity.Candidate
			telemetryOK = telemetryOK && RuntimeCapacityAccepted(status.RuntimeCapacity.SchemaVersion, status.RuntimeCapacity.Notification, status.RuntimeCapacity.Candidate, status.RuntimeCapacity.PolicyHealthy)
			sample.ProjectedSequence, sample.Lag = status.Live.Projected, status.Live.Lag
			sample.CrossPlaneSessionID = status.SessionID
			telemetryOK = telemetryOK && status.SessionID == expectedSession
		} else {
			telemetryOK = false
		}
	} else {
		telemetryOK = false
	}
	for endpoint, target := range map[string]*string{"/v1/operator/state": &sample.OperatorStateSHA256, "/v1/overlay/state": &sample.OverlayStateSHA256} {
		request, _ := http.NewRequest(http.MethodGet, DeliveryOrigin+endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		request.Header.Set("Origin", DeliveryOrigin)
		if response, err := (&http.Client{Timeout: time.Second}).Do(request); err == nil && response.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			*target = payloadSHA(body)
			if strings.Contains(endpoint, "operator") {
				sample.OperatorResponseBytes = len(body)
				var state struct {
					SessionID string            `json:"session_id"`
					Previews  []json.RawMessage `json:"previews"`
				}
				if json.Unmarshal(body, &state) == nil {
					sample.QueueOccupancy = len(state.Previews)
					if sample.CrossPlaneSessionID == "" {
						sample.CrossPlaneSessionID = state.SessionID
					} else if sample.CrossPlaneSessionID != state.SessionID {
						telemetryOK = false
					}
				} else {
					telemetryOK = false
				}
			} else {
				sample.OverlayResponseBytes = len(body)
				var state struct {
					SessionID  string          `json:"session_id"`
					Visibility string          `json:"visibility"`
					Claim      json.RawMessage `json:"claim"`
				}
				if json.Unmarshal(body, &state) != nil || state.SessionID == "" {
					telemetryOK = false
				} else {
					if sample.CrossPlaneSessionID == "" {
						sample.CrossPlaneSessionID = state.SessionID
					} else if sample.CrossPlaneSessionID != state.SessionID {
						telemetryOK = false
					}
					if state.Visibility == "visible" && len(state.Claim) > 0 && string(state.Claim) != "null" {
						sample.OverlayVisibleClaims = 1
					}
				}
			}
		} else {
			telemetryOK = false
		}
	}
	sample.OBSRenderedFrames, sample.OBSMissedFrames, sample.OBSSkippedFrames = obsFrames(filepath.Join(root, "evidence/logs/obs-live.log"))
	telemetryOK = telemetryOK && sample.ProductGoroutines > 0 && sample.ProductTreeProcesses > 0 && sample.OBSProcessTreeCount > 0 && sample.OBSBrowserProcesses > 0 && sample.StatusResponseBytes > 0 && sample.OperatorResponseBytes > 0 && sample.OverlayResponseBytes > 0 && sample.CrossPlaneSessionID == expectedSession
	sample.TelemetryComplete = telemetryOK
	return sample
}

func clockTicksPerSecond() (int, error) {
	output, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || value <= 0 {
		return 0, errors.New("invalid CLK_TCK")
	}
	return value, nil
}

func largestRawPayload(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var largest int64
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var frame struct {
			RawByteLength int64 `json:"raw_byte_length"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.RawByteLength <= 0 {
			return 0, errors.New("raw request length telemetry absent")
		}
		if frame.RawByteLength > largest {
			largest = frame.RawByteLength
		}
	}
	return largest, scanner.Err()
}

func lastRawMatchIdentity(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var match string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var frame struct {
			Raw string `json:"raw_base64"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return "", errors.New("raw identity frame malformed")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(frame.Raw)
		if err != nil {
			return "", err
		}
		var payload struct {
			Map struct {
				MatchID json.Number `json:"matchid"`
			} `json:"map"`
		}
		decoder := json.NewDecoder(bytes.NewReader(decoded))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil || payload.Map.MatchID.String() == "" || payload.Map.MatchID.String() == "0" {
			return "", errors.New("raw match identity missing")
		}
		if match != "" && match != payload.Map.MatchID.String() {
			return "", errors.New("raw match identity changed")
		}
		match = payload.Map.MatchID.String()
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if match == "" {
		return "", errors.New("raw match identity absent")
	}
	return match, nil
}

func processMetrics(pid int) (rss int64, fds, threads int, cpu uint64) {
	rss, fds, threads, cpu, _, _ = processMetricsOne(pid)
	return
}

func processMetricsOne(pid int) (rss int64, fds, threads int, cpu, startTicks uint64, resultErr error) {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	payload, err := os.ReadFile(filepath.Join(base, "statm"))
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	fields := strings.Fields(string(payload))
	if len(fields) <= 1 {
		return 0, 0, 0, 0, 0, errors.New("process statm is incomplete")
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || pages <= 0 {
		return 0, 0, 0, 0, 0, errors.New("process RSS is unavailable")
	}
	rss = pages * int64(os.Getpagesize())
	payload, err = os.ReadFile(filepath.Join(base, "status"))
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "Threads:") {
			threads, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Threads:")))
		}
	}
	entries, err := os.ReadDir(filepath.Join(base, "fd"))
	if err != nil || threads <= 0 {
		return 0, 0, 0, 0, 0, errors.New("process FD/thread telemetry unavailable")
	}
	fds = len(entries)
	payload, err = os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	close := strings.LastIndex(string(payload), ")")
	if close < 0 {
		return 0, 0, 0, 0, 0, errors.New("process stat is malformed")
	}
	fields = strings.Fields(string(payload)[close+1:])
	if len(fields) <= 19 {
		return 0, 0, 0, 0, 0, errors.New("process stat fields are incomplete")
	}
	user, userErr := strconv.ParseUint(fields[11], 10, 64)
	system, systemErr := strconv.ParseUint(fields[12], 10, 64)
	startTicks, err = strconv.ParseUint(fields[19], 10, 64)
	if userErr != nil || systemErr != nil || err != nil {
		return 0, 0, 0, 0, 0, errors.New("process CPU/start telemetry is invalid")
	}
	cpu = user + system
	return rss, fds, threads, cpu, startTicks, nil
}

type treeMetrics struct {
	Processes        int
	RSS              int64
	FDs              int
	Threads          int
	CPU              uint64
	BrowserProcesses int
	BrowserCPU       uint64
}

func processTreeMetrics(rootPID int) (treeMetrics, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return treeMetrics{}, err
	}
	children := map[int][]int{}
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		payload, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if readErr != nil {
			continue
		}
		close := strings.LastIndex(string(payload), ")")
		if close < 0 {
			continue
		}
		fields := strings.Fields(string(payload)[close+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		children[ppid] = append(children[ppid], pid)
	}
	queue := []int{rootPID}
	seen := map[int]bool{}
	var total treeMetrics
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		rss, fds, threads, cpu, _, metricErr := processMetricsOne(pid)
		if metricErr != nil {
			return treeMetrics{}, metricErr
		}
		total.Processes++
		total.RSS += rss
		total.FDs += fds
		total.Threads += threads
		total.CPU += cpu
		if comm, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm")); readErr == nil {
			name := strings.ToLower(strings.TrimSpace(string(comm)))
			if strings.Contains(name, "obs-browser") || strings.Contains(name, "cef") {
				total.BrowserProcesses++
				total.BrowserCPU += cpu
			}
		}
		queue = append(queue, children[pid]...)
	}
	if total.Processes == 0 {
		return treeMetrics{}, errors.New("empty process tree")
	}
	return total, nil
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

func sealFailedLive(root string, preflight Readiness, preflightEvidence Evidence, artifacts []Artifact, identity LiveIdentity, reason string) (Readiness, error) {
	return sealLive(root, preflight, preflightEvidence, artifacts, identity, liveBoundary{}, []string{reason})
}

func sealCompletedLive(root string, preflight Readiness, preflightEvidence Evidence, artifacts []Artifact, identity LiveIdentity, boundary liveBoundary) (Readiness, error) {
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
	if !boundary.VisibilityPassed {
		failures = append(failures, "visibility_fail_closed_failed")
	}
	if !boundary.OperatorInputBounds {
		failures = append(failures, "operator_input_body_bounds_failed")
	}
	if !boundary.IdentityContinuous || !boundary.DotaProvenance || boundary.Frames == 0 {
		failures = append(failures, "live_identity_or_provenance_failed")
	}
	if !boundary.CleanShutdown {
		failures = append(failures, "unclean_product_obs_or_recovery_shutdown")
	}
	return sealLive(root, preflight, preflightEvidence, artifacts, identity, boundary, failures)
}

func sealLive(root string, preflight Readiness, preflightEvidence Evidence, artifacts []Artifact, identity LiveIdentity, boundary liveBoundary, failures []string) (Readiness, error) {
	liveArtifacts, err := collectLiveArtifacts(root)
	if err != nil {
		return Readiness{}, err
	}
	artifacts = mergeArtifacts(artifacts, liveArtifacts)
	evidence := Evidence{SchemaVersion: SchemaVersion, Mode: "live", CandidateCommit: preflight.CandidateCommit, CandidateParent: AcceptedFunctionalBase,
		AcceptedBase: AcceptedFunctionalBase, AcceptedSpec: AcceptedP4Spec, FixtureSHA256: CapturedScheduleSHA256, GoldenSHA256: ProductionGoldenSHA256,
		Bounds: AcceptedBounds(), Faults: append([]string(nil), RequiredFaults...), Artifacts: artifacts, StartBoundary: "capture/operator/overlay/OBS armed before manual preview confirmation",
		GameZeroBoundary: "negative clock followed by nonnegative clock", PostGameBoundary: "normal post-game GSI state", RecordingBoundary: "OBS finalization required",
		OperatorScript: HumanInstruction, NonResumable: true, SyntheticOnly: false, ClaimsP4: false, ReadinessIssueText: HumanInstruction}
	evidence.CandidateParent = RequiredSuccessorParent
	evidence.CandidateIdentity = preflightEvidence.CandidateIdentity
	evidence.CandidateIdentityEvidence = preflightEvidence.CandidateIdentityEvidence
	evidence.Environment = preflightEvidence.Environment
	evidence.LocalhostGSI = LocalhostGSITrustEvidence{ConfigSHA256: boundary.ConfigSHA256, ConfigURI: "http://" + CaptureAddress + "/gsi", ConfigUserOnly: boundary.ConfigUserOnly, ExclusiveListener: boundary.ExclusiveListener, ListenerProductOwned: boundary.ExclusiveListener, ListenerURI: "http://" + CaptureAddress + "/gsi", DotaProcessStable: boundary.DotaProvenance, KnownProducerAbsent: boundary.KnownProducerAbsent, CorrelationStartSHA256: boundary.CorrelationStartSHA256, CorrelationEndSHA256: boundary.CorrelationEndSHA256, IdentityContinuous: boundary.IdentityContinuous, Requests: boundary.Requests, Accepted: boundary.Accepted, Rejected: boundary.Rejected, RawRecords: boundary.RawRecords, TerminalOutcomes: boundary.TerminalOutcomes, RecordingCoextensive: boundary.RecordingCoextensive, PaulConfirmedIdentity: boundary.PaulConfirmed, PerRequestAttested: false, ResidualReasonCodes: []string{"localhost_gsi_sender_unattested"}}
	trustOK := validateLocalhostGSITrust(evidence.LocalhostGSI) == nil
	evidence.Checks = []Check{{"pregame_boundary", boundary.Pregame, "negative game clock observed after complete arming"}, {"game_zero_boundary", boundary.Zero, "clock zero transition observed"}, {"post_game_boundary", boundary.Post && boundary.WinnerObserved, "normal post-game state and winner observed"}, {"recording_finalized", boundary.RecordingFinalized, "clean OBS stop, EBML MKV, frame counters, and no partial recording"}, {"operator_script", boundary.OperatorComplete, "ordered durable command attempts/revisions/audits present"}, {"operator_input_bounds", boundary.OperatorInputBounds, "durable raw operator inputs are present and within 16 KiB"}, {"privacy", boundary.PrivacySafe, "raw payload key scan passed"}, {"reconciliation", boundary.Reconciled, "every raw record reached exactly one terminal cursor outcome"}, {"no_cache_recovery", boundary.NoCacheRecovery, "genuine isolated product restart rebuilt from raw inputs and byte-matched five planes"}, {"measurement_bounds", boundary.MeasurementsPassed, "complete five-second process-tree/body/state/resource plane passed"}, {"visibility_fail_closed", boundary.VisibilityPassed, "100ms samples prove two-second claim-free output, no stale revival, and continued raw capture"}, {"live_identity_continuity", boundary.IdentityContinuous && boundary.DotaProvenance && boundary.Frames > 0, "nonzero match identity and bound Dota process remained stable for every frame"}, {"clean_shutdown", boundary.CleanShutdown, "product, OBS, and recovery product stopped cleanly"}, {"public_match_identity", identity.MatchID != "" && identity.OfficialSourceURL != "", "explicit official public TI identity supplied"}, {"independent_acceptance", false, "live command never self-accepts P4"}}
	evidence.Checks = append(evidence.Checks, Check{"localhost_gsi_trust_boundary", trustOK, "accepted localhost-GSI trust boundary and residual limitation recorded"})
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
	result := Readiness{SchemaVersion: ReadinessSchemaVersion, Ready: false, Mode: "live", CandidateCommit: preflight.CandidateCommit, CandidateIdentitySHA256: preflight.CandidateIdentitySHA256, EnvironmentSHA256: preflight.EnvironmentSHA256, EvidenceIndexSHA256: payloadSHA(index), Failures: allFailures, ClaimsP4: false, HumanInstruction: HumanInstruction}
	if err := writeJSON(filepath.Join(root, "evidence/readiness.json"), result, 0o600); err != nil {
		return Readiness{}, err
	}
	return result, nil
}

func collectLiveArtifacts(root string) ([]Artifact, error) {
	var artifacts []Artifact
	for _, relativeRoot := range []string{"data/sessions", "evidence/logs", "evidence/samples.jsonl", "evidence/visibility.jsonl", "evidence/raw-operator-input.jsonl", "evidence/recovery-input", "evidence/recovery-work", "evidence/canonical", "recordings"} {
		base := filepath.Join(root, filepath.FromSlash(relativeRoot))
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if relative == "evidence/canonical/evidence-index.json" || relative == "evidence/readiness.json" || strings.Contains(relative, "operator.token") {
				return nil
			}
			hash, size, err := fileSHA(path)
			if err != nil {
				return err
			}
			artifacts = append(artifacts, Artifact{Path: relative, SHA256: hash, Bytes: size})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

func mergeArtifacts(groups ...[]Artifact) []Artifact {
	byPath := map[string]Artifact{}
	for _, group := range groups {
		for _, artifact := range group {
			byPath[artifact.Path] = artifact
		}
	}
	result := make([]Artifact, 0, len(byPath))
	for _, artifact := range byPath {
		result = append(result, artifact)
	}
	return result
}
