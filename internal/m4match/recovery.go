package m4match

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type attemptValidation struct {
	SchemaVersion          string             `json:"schema_version"`
	RawCount               uint64             `json:"raw_count"`
	CursorSequence         uint64             `json:"cursor_sequence"`
	PolicyCommits          uint64             `json:"policy_commits"`
	ObservationCommits     uint64             `json:"observation_commits"`
	LastObservation        uint64             `json:"last_observation_sequence"`
	LastStateSHA256        string             `json:"last_state_sha256"`
	AuditSHA256            []string           `json:"audit_sha256"`
	OperatorActions        []string           `json:"operator_actions"`
	OperatorResults        []string           `json:"operator_results"`
	OperatorTerminals      []operatorTerminal `json:"operator_terminals"`
	RecordingFinalized     bool               `json:"recording_finalized"`
	OperatorComplete       bool               `json:"operator_complete"`
	PrivacySafe            bool               `json:"privacy_safe"`
	Reconciled             bool               `json:"reconciled"`
	NoCacheRecovery        bool               `json:"no_cache_recovery"`
	MeasurementsPassed     bool               `json:"measurements_passed"`
	VisibilityPassed       bool               `json:"visibility_passed"`
	OperatorInputBounds    bool               `json:"operator_input_bounds"`
	EveryRawTerminal       bool               `json:"every_raw_terminal"`
	RecoveryCursorSHA256   string             `json:"recovery_cursor_sha256"`
	RecoveryPolicySHA256   string             `json:"recovery_policy_sha256"`
	RecoveryAuditSHA256    string             `json:"recovery_audit_sha256"`
	RecoveryOperatorSHA256 string             `json:"recovery_operator_sha256"`
	RecoveryOverlaySHA256  string             `json:"recovery_overlay_sha256"`
	RecoveryMaxRSSBytes    int64              `json:"recovery_max_rss_bytes"`
	RecoveryCleanShutdown  bool               `json:"recovery_clean_shutdown"`
}

type operatorTerminal struct {
	CommandID         string `json:"command_id"`
	Action            string `json:"action"`
	TargetCandidateID string `json:"target_candidate_id,omitempty"`
	TargetRuleID      string `json:"target_rule_id,omitempty"`
	ExpectedRevision  uint64 `json:"expected_revision"`
	Status            string `json:"status"`
	PreviousRevision  uint64 `json:"previous_revision"`
	ResultingRevision uint64 `json:"resulting_revision"`
	Reason            string `json:"reason"`
}

type recoveryProof struct {
	CursorSHA256   string     `json:"cursor_sha256"`
	PolicySHA256   string     `json:"policy_sha256"`
	AuditSHA256    string     `json:"audit_sha256"`
	OperatorSHA256 string     `json:"operator_sha256"`
	OverlaySHA256  string     `json:"overlay_sha256"`
	MaxRSSBytes    int64      `json:"max_rss_bytes"`
	CleanShutdown  bool       `json:"clean_shutdown"`
	RawInputs      []Artifact `json:"raw_inputs"`
}

func captureFinalEndpoints(root, tokenPath string) ([]Artifact, error) {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, err
	}
	endpoints := map[string]string{"final-status.json": CaptureOrigin + "/api/status", "final-operator.json": DeliveryOrigin + "/v1/operator/state", "final-overlay.json": DeliveryOrigin + "/v1/overlay/state"}
	var artifacts []Artifact
	for name, url := range endpoints {
		request, _ := http.NewRequest(http.MethodGet, url, nil)
		if strings.HasPrefix(url, DeliveryOrigin) {
			request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			request.Header.Set("Origin", DeliveryOrigin)
		}
		response, err := (&http.Client{}).Do(request)
		if err != nil {
			return artifacts, err
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !json.Valid(payload) {
			return artifacts, fmt.Errorf("final endpoint invalid: %s", url)
		}
		relative := "evidence/canonical/" + name
		path := filepath.Join(root, relative)
		if err := writePrivate(path, payload); err != nil {
			return artifacts, err
		}
		artifacts = append(artifacts, Artifact{Path: relative, SHA256: payloadSHA(payload), Bytes: int64(len(payload))})
	}
	return artifacts, nil
}

func validateCompletedAttempt(root, sessionID string, productClean, obsClean bool) (attemptValidation, []Artifact, error) {
	return validateCompletedAttemptObserved(root, sessionID, productClean, obsClean, nil)
}

func validateCompletedAttemptObserved(root, sessionID string, productClean, obsClean bool, observe func(string, RehearsalOwnedProcessIdentityV1)) (attemptValidation, []Artifact, error) {
	sessionDir := filepath.Join(root, "data/sessions", sessionID)
	validation, err := summarizeAttempt(sessionDir)
	if err != nil {
		return validation, nil, err
	}
	validation.RecordingFinalized = obsClean && recordingFinalized(filepath.Join(root, "recordings"), filepath.Join(root, "evidence/logs/obs-live.log"))
	validation.PrivacySafe = scanRawPrivacy(filepath.Join(sessionDir, "raw.jsonl")) == nil
	validation.OperatorComplete = validateOperatorTerminals(validation.OperatorTerminals) == nil && validateOperatorReplayProof(filepath.Join(root, "evidence/canonical/operator-replay-proof.json")) == nil
	validation.EveryRawTerminal = validation.RawCount > 0 && validation.CursorSequence == validation.RawCount && validation.LastObservation <= validation.CursorSequence && validation.ObservationCommits > 0
	validation.Reconciled = validation.EveryRawTerminal
	validation.MeasurementsPassed = validateSamples(filepath.Join(root, "evidence/samples.jsonl"), root) == nil
	validation.VisibilityPassed = validateVisibility(filepath.Join(root, "evidence/visibility.jsonl"), sessionDir) == nil
	operatorInputs, operatorInputErr := readOperatorInputs(filepath.Join(root, "evidence/raw-operator-input.jsonl"))
	validation.OperatorInputBounds = operatorInputErr == nil && len(operatorInputs) > 0
	for _, input := range operatorInputs {
		validation.OperatorInputBounds = validation.OperatorInputBounds && len(input) <= 16<<10
	}
	proof, recoveryArtifacts, recoveryErr := performRawOnlyRecoveryObserved(context.Background(), root, sessionID, observe)
	validation.RecoveryCursorSHA256 = proof.CursorSHA256
	validation.RecoveryPolicySHA256 = proof.PolicySHA256
	validation.RecoveryAuditSHA256 = proof.AuditSHA256
	validation.RecoveryOperatorSHA256 = proof.OperatorSHA256
	validation.RecoveryOverlaySHA256 = proof.OverlaySHA256
	validation.RecoveryMaxRSSBytes = proof.MaxRSSBytes
	validation.RecoveryCleanShutdown = proof.CleanShutdown
	validation.NoCacheRecovery = recoveryErr == nil && proof.CleanShutdown && proof.MaxRSSBytes > 0 && proof.MaxRSSBytes <= AcceptedBounds().RecoveryRSSBytes
	payload, _ := canonical(validation)
	relative := "evidence/canonical/live-validation.json"
	if err := writePrivate(filepath.Join(root, relative), payload); err != nil {
		return validation, nil, err
	}
	artifacts := append(recoveryArtifacts, Artifact{Path: relative, SHA256: payloadSHA(payload), Bytes: int64(len(payload))})
	if !productClean || !validation.RecordingFinalized || !validation.OperatorComplete || !validation.PrivacySafe || !validation.Reconciled || !validation.NoCacheRecovery || !validation.MeasurementsPassed || !validation.VisibilityPassed || !validation.OperatorInputBounds {
		return validation, artifacts, errors.New("completed attempt validation failed")
	}
	return validation, artifacts, nil
}

func summarizeAttempt(sessionDir string) (attemptValidation, error) {
	result := attemptValidation{SchemaVersion: "m4_live_validation.v1", PrivacySafe: true}
	result.RawCount, _ = readRawSequences(filepath.Join(sessionDir, "raw.jsonl"))
	if cursor, err := os.ReadFile(filepath.Join(sessionDir, "live_projection_cursor.json")); err == nil {
		var value struct {
			Sequence uint64 `json:"sequence"`
		}
		if json.Unmarshal(cursor, &value) == nil {
			result.CursorSequence = value.Sequence
		}
	} else {
		result.CursorSequence = result.RawCount
	}
	segments, _ := filepath.Glob(filepath.Join(sessionDir, "*.pcl3"))
	sort.Strings(segments)
	seenObservations := map[uint64]bool{}
	for _, segment := range segments {
		commits, err := readPolicySegment(segment)
		if err != nil {
			return result, err
		}
		for _, commit := range commits {
			result.PolicyCommits++
			if commit.ObservationSequence > 0 {
				if seenObservations[commit.ObservationSequence] {
					return result, errors.New("duplicate observation terminal outcome")
				}
				seenObservations[commit.ObservationSequence] = true
				result.ObservationCommits++
			}
			if commit.ResultingObservationSequence > result.LastObservation {
				result.LastObservation = commit.ResultingObservationSequence
			}
			result.LastStateSHA256 = commit.ResultingStateHash
			if commit.Command != nil {
				result.OperatorActions = append(result.OperatorActions, commit.Command.Action)
				result.OperatorResults = append(result.OperatorResults, commit.Command.Action+":"+commit.CommandResult.Status+":"+commit.CommandResult.Reason)
				result.OperatorTerminals = append(result.OperatorTerminals, operatorTerminal{CommandID: commit.Command.CommandID, Action: commit.Command.Action, TargetCandidateID: commit.Command.TargetCandidateID, TargetRuleID: commit.Command.TargetRuleID, ExpectedRevision: commit.Command.ExpectedPolicyRevision, Status: commit.CommandResult.Status, PreviousRevision: commit.CommandResult.PreviousRevision, ResultingRevision: commit.CommandResult.ResultingRevision, Reason: commit.CommandResult.Reason})
			}
			for _, audit := range commit.AuditEvents {
				payload, _ := contracts.MarshalCanonical(audit)
				result.AuditSHA256 = append(result.AuditSHA256, payloadSHA(payload))
			}
		}
	}
	sort.Strings(result.AuditSHA256)
	return result, nil
}

func performRawOnlyRecovery(ctx context.Context, root, sessionID string) (recoveryProof, []Artifact, error) {
	return performRawOnlyRecoveryObserved(ctx, root, sessionID, nil)
}

func performRawOnlyRecoveryObserved(ctx context.Context, root, sessionID string, observe func(string, RehearsalOwnedProcessIdentityV1)) (recoveryProof, []Artifact, error) {
	var proof recoveryProof
	sourceSession := filepath.Join(root, "data/sessions", sessionID)
	inputRoot := filepath.Join(root, "evidence/recovery-input", sessionID)
	recoveryRoot := filepath.Join(root, "evidence/recovery-work")
	if err := rootMkdirAll(filepath.Join(recoveryRoot, "data/sessions", sessionID), 0o700); err != nil {
		return proof, nil, err
	}
	rawSource := filepath.Join(sourceSession, "raw.jsonl")
	rawInput := filepath.Join(inputRoot, "raw.jsonl")
	if err := copyFile(rawSource, rawInput, 0o600); err != nil {
		return proof, nil, err
	}
	if err := copyFile(rawInput, filepath.Join(recoveryRoot, "data/sessions", sessionID, "raw.jsonl"), 0o600); err != nil {
		return proof, nil, err
	}
	sourceJournal := filepath.Join(root, "evidence/raw-operator-input.jsonl")
	journalPath := filepath.Join(inputRoot, "operator-input.jsonl")
	if err := copyFile(sourceJournal, journalPath, 0o600); err != nil {
		return proof, nil, err
	}
	operatorInputs, err := readOperatorInputs(journalPath)
	if err != nil || len(operatorInputs) == 0 {
		return proof, nil, errors.New("durable raw operator-input journal is absent or invalid")
	}
	committed, err := committedCommands(sourceSession)
	uniqueInputs := make([][]byte, 0, len(operatorInputs))
	seenInput := map[string][]byte{}
	duplicateSeen := false
	for _, input := range operatorInputs {
		var command contracts.OperatorCommandV1
		_ = contracts.DecodeStrict(input, &command)
		if prior, ok := seenInput[command.CommandID]; ok {
			if !bytes.Equal(prior, input) {
				return proof, nil, errors.New("replayed command ID changed bytes")
			}
			duplicateSeen = true
			continue
		}
		seenInput[command.CommandID] = input
		uniqueInputs = append(uniqueInputs, input)
	}
	if err != nil || !duplicateSeen || len(committed) != len(uniqueInputs) {
		return proof, nil, errors.New("operator input/terminal commit reconciliation failed")
	}
	for index, command := range committed {
		canonicalCommand, _ := contracts.MarshalCanonical(command)
		if !bytes.Equal(canonicalCommand, uniqueInputs[index]) {
			return proof, nil, errors.New("operator input does not equal committed command in order")
		}
	}
	if err != nil {
		return proof, nil, err
	}
	if _, err := writeLiveArtifacts(recoveryRoot, sessionID); err != nil {
		return proof, nil, err
	}
	for _, directory := range []string{"runtime", "evidence/canonical", "evidence/logs"} {
		if err := rootMkdirAll(filepath.Join(recoveryRoot, directory), 0o700); err != nil {
			return proof, nil, err
		}
	}
	logFile, err := rootOpenFile(filepath.Join(recoveryRoot, "evidence/logs/product-recovery.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return proof, nil, err
	}
	binary := filepath.Join(root, "application/dota2-ob")
	tokenPath := filepath.Join(recoveryRoot, "runtime/operator.token")
	process := exec.CommandContext(ctx, binary, "--policy-mode", "v3-live-only", "--data-dir", filepath.Join(recoveryRoot, "data/sessions"), "--session-id", sessionID,
		"--addr", CaptureAddress, "--delivery-addr", DeliveryAddress, "--operator-token-file", tokenPath,
		"--history-binding-file", filepath.Join(recoveryRoot, "config/policy/history_availability_binding_v1.json"),
		"--live-only-lineage-file", filepath.Join(recoveryRoot, "config/policy/policy_lineage_manifest_v3.json"),
		"--live-only-release-file", filepath.Join(recoveryRoot, "config/policy/live_only_release_binding_v1.json"))
	process.Stdout, process.Stderr = logFile, logFile
	if err := process.Start(); err != nil {
		_ = logFile.Close()
		return proof, nil, err
	}
	startIdentity, identityErr := readRehearsalOwnedProcessIdentity(process.Process.Pid)
	if identityErr != nil {
		_ = process.Process.Kill()
		_ = logFile.Close()
		return proof, nil, errors.New("recovery process identity unavailable")
	}
	if observe != nil {
		observe("recovery_start", startIdentity)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	clean := false
	defer func() {
		if !clean && process.ProcessState == nil {
			_ = process.Process.Kill()
			<-done
		}
		_ = logFile.Close()
	}()
	if err := waitHTTP(ctx, CaptureOrigin+"/healthz", 15*time.Second); err != nil {
		return proof, nil, err
	}
	target, err := readRawSequences(rawInput)
	if err != nil || target == 0 {
		return proof, nil, errors.New("recovery raw input is absent or invalid")
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		tree, treeErr := processTreeMetrics(process.Process.Pid)
		if treeErr != nil {
			return proof, nil, errors.New("recovery process-tree telemetry unavailable")
		}
		if tree.RSS > proof.MaxRSSBytes {
			proof.MaxRSSBytes = tree.RSS
		}
		projected, highWater, lag, statusErr := recoveryStatus()
		if statusErr == nil && projected == target && highWater == target && lag == 0 {
			break
		}
		if time.Now().After(deadline) {
			return proof, nil, errors.New("raw-only recovery did not reach terminal high-water")
		}
		time.Sleep(100 * time.Millisecond)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return proof, nil, err
	}
	for _, payload := range operatorInputs {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, DeliveryOrigin+"/v1/operator/commands", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		request.Header.Set("Origin", DeliveryOrigin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Dota2-OB-CSRF", "operator-command")
		response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Do(request)
		if requestErr != nil || response.StatusCode != http.StatusOK {
			if response != nil {
				_ = response.Body.Close()
			}
			return proof, nil, errors.New("raw operator-input replay failed")
		}
		_ = response.Body.Close()
		tree, treeErr := processTreeMetrics(process.Process.Pid)
		if treeErr != nil {
			return proof, nil, errors.New("recovery process-tree telemetry unavailable after operator input")
		}
		if tree.RSS > proof.MaxRSSBytes {
			proof.MaxRSSBytes = tree.RSS
		}
	}
	if _, err := captureFinalEndpoints(recoveryRoot, tokenPath); err != nil {
		return proof, nil, err
	}
	terminalIdentity, identityErr := readRehearsalOwnedProcessIdentity(process.Process.Pid)
	if identityErr != nil || !sameRehearsalOwnedProcessIdentity(startIdentity, terminalIdentity) {
		return proof, nil, errors.New("recovery process identity changed")
	}
	if observe != nil {
		observe("recovery_terminal", terminalIdentity)
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		return proof, nil, err
	}
	select {
	case err := <-done:
		if err != nil {
			return proof, nil, err
		}
		clean = true
		proof.CleanShutdown = true
	case <-time.After(15 * time.Second):
		return proof, nil, errors.New("recovery product did not stop cleanly")
	}
	if err := logFile.Close(); err != nil {
		return proof, nil, err
	}
	proof.CursorSHA256, _, err = fileSHA(filepath.Join(recoveryRoot, "data/sessions", sessionID, "live_projection_cursor.json"))
	if err != nil {
		return proof, nil, err
	}
	proof.PolicySHA256, err = policyTreeSHA(filepath.Join(recoveryRoot, "data/sessions", sessionID))
	if err != nil {
		return proof, nil, err
	}
	rebuilt, err := summarizeAttempt(filepath.Join(recoveryRoot, "data/sessions", sessionID))
	if err != nil {
		return proof, nil, err
	}
	auditPayload, _ := canonical(rebuilt.AuditSHA256)
	proof.AuditSHA256 = payloadSHA(auditPayload)
	proof.OperatorSHA256, _, err = fileSHA(filepath.Join(recoveryRoot, "evidence/canonical/final-operator.json"))
	if err != nil {
		return proof, nil, err
	}
	proof.OverlaySHA256, _, err = fileSHA(filepath.Join(recoveryRoot, "evidence/canonical/final-overlay.json"))
	if err != nil {
		return proof, nil, err
	}
	originalCursor, _, _ := fileSHA(filepath.Join(sourceSession, "live_projection_cursor.json"))
	originalPolicy, _ := policyTreeSHA(sourceSession)
	original, _ := summarizeAttempt(sourceSession)
	originalAuditPayload, _ := canonical(original.AuditSHA256)
	originalOperator, _, _ := fileSHA(filepath.Join(root, "evidence/canonical/final-operator.json"))
	originalOverlay, _, _ := fileSHA(filepath.Join(root, "evidence/canonical/final-overlay.json"))
	if proof.CursorSHA256 != originalCursor || proof.PolicySHA256 != originalPolicy || proof.AuditSHA256 != payloadSHA(originalAuditPayload) || proof.OperatorSHA256 != originalOperator || proof.OverlaySHA256 != originalOverlay {
		return proof, nil, errors.New("raw-only rebuilt cursor/policy/audit/operator/overlay bytes differ")
	}
	proof.RawInputs = []Artifact{}
	for _, path := range []string{rawInput, journalPath} {
		hash, size, hashErr := fileSHA(path)
		if hashErr != nil {
			return proof, nil, hashErr
		}
		relative, _ := filepath.Rel(root, path)
		proof.RawInputs = append(proof.RawInputs, Artifact{Path: filepath.ToSlash(relative), SHA256: hash, Bytes: size})
	}
	proofPayload, _ := canonical(proof)
	proofPath := filepath.Join(root, "evidence/canonical/raw-only-recovery.json")
	if err := writePrivate(proofPath, proofPayload); err != nil {
		return proof, nil, err
	}
	artifacts := append([]Artifact(nil), proof.RawInputs...)
	artifacts = append(artifacts, Artifact{Path: "evidence/canonical/raw-only-recovery.json", SHA256: payloadSHA(proofPayload), Bytes: int64(len(proofPayload))})
	return proof, artifacts, nil
}

func readOperatorInputs(path string) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var result [][]byte
	var sequence uint64
	for scanner.Scan() {
		var frame operatorInputFrame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Sequence != sequence+1 {
			return nil, errors.New("operator input sequence is malformed")
		}
		received, err := time.Parse(time.RFC3339Nano, frame.ReceivedAt)
		body, decodeErr := base64.StdEncoding.Strict().DecodeString(frame.BodyBase64)
		if err != nil || received.UTC().Format(time.RFC3339Nano) != frame.ReceivedAt || decodeErr != nil || payloadSHA(body) != frame.BodySHA256 {
			return nil, errors.New("operator input frame identity mismatch")
		}
		var command contracts.OperatorCommandV1
		if contracts.DecodeStrict(body, &command) != nil || command.Validate() != nil {
			return nil, errors.New("operator input is not a canonical valid command")
		}
		canonicalBody, _ := contracts.MarshalCanonical(command)
		if !bytes.Equal(body, canonicalBody) {
			return nil, errors.New("operator input command is noncanonical")
		}
		result = append(result, body)
		sequence = frame.Sequence
	}
	return result, scanner.Err()
}

func committedCommands(sessionDir string) ([]contracts.OperatorCommandV1, error) {
	segments, _ := filepath.Glob(filepath.Join(sessionDir, "*.pcl3"))
	sort.Strings(segments)
	var commands []contracts.OperatorCommandV1
	for _, segment := range segments {
		commits, err := readPolicySegment(segment)
		if err != nil {
			return nil, err
		}
		for _, commit := range commits {
			if commit.Command != nil {
				commands = append(commands, *commit.Command)
			}
		}
	}
	return commands, nil
}

func recoveryStatus() (projected, highWater, lag uint64, err error) {
	response, err := (&http.Client{Timeout: time.Second}).Get(CaptureOrigin + "/api/status")
	if err != nil {
		return 0, 0, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK {
		return 0, 0, 0, errors.New("recovery status unavailable")
	}
	var status struct {
		Live struct {
			Projected uint64 `json:"projected_sequence"`
			HighWater uint64 `json:"high_water"`
			Lag       uint64 `json:"lag_count"`
		} `json:"live_projection"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return 0, 0, 0, err
	}
	return status.Live.Projected, status.Live.HighWater, status.Live.Lag, nil
}

func policyTreeSHA(sessionDir string) (string, error) {
	segments, _ := filepath.Glob(filepath.Join(sessionDir, "*.pcl3"))
	sort.Strings(segments)
	if len(segments) == 0 {
		return "", errors.New("policy output absent")
	}
	hash := sha256.New()
	for _, segment := range segments {
		payload, err := os.ReadFile(segment)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.Base(segment)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(payload)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func readPolicySegment(path string) ([]contracts.PolicyCommitV3, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var commits []contracts.PolicyCommitV3
	for offset := 0; offset < len(payload); {
		if len(payload)-offset < 44 {
			return nil, errors.New("partial V3 policy frame")
		}
		size := int(binary.BigEndian.Uint32(payload[offset : offset+4]))
		end := offset + 4 + size + 32 + 8
		if size <= 0 || end > len(payload) {
			return nil, errors.New("invalid V3 policy frame size")
		}
		body := payload[offset+4 : offset+4+size]
		storedHash := payload[offset+4+size : offset+4+size+32]
		marker := payload[end-8 : end]
		hash := sha256.Sum256(body)
		if !bytes.Equal(hash[:], storedHash) || !bytes.Equal(marker, []byte{'P', 'C', 'O', 'M', 'M', 'I', 'T', 3}) {
			return nil, errors.New("invalid V3 policy frame integrity")
		}
		var commit contracts.PolicyCommitV3
		if contracts.DecodeStrict(body, &commit) != nil || commit.Validate() != nil {
			return nil, errors.New("invalid V3 policy commit")
		}
		commits = append(commits, commit)
		offset = end
	}
	return commits, nil
}

func recordingFinalized(root, obsLog string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lower := strings.ToLower(entry.Name())
		if strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".tmp") {
			return false
		}
		if strings.HasSuffix(lower, ".mkv") {
			info, _ := entry.Info()
			if info != nil && info.Size() > 4 {
				file, openErr := os.Open(filepath.Join(root, entry.Name()))
				var magic [4]byte
				var readErr error
				if openErr == nil {
					_, readErr = io.ReadFull(file, magic[:])
				}
				if file != nil {
					_ = file.Close()
				}
				if openErr == nil && readErr == nil && bytes.Equal(magic[:], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
					found = true
				}
			}
		}
	}
	rendered, missed, skipped := obsFrames(obsLog)
	return found && rendered > 0 && missed <= rendered && skipped <= rendered
}

func validateOperatorTerminals(terminals []operatorTerminal) error {
	required := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	reasons := map[string]string{contracts.ActionApprove: "approve", contracts.ActionReject: "reject", contracts.ActionPin: "pin", contracts.ActionUnpin: "unpin", contracts.ActionEmergencyHide: "emergency_hide", contracts.ActionClearEmergencyHide: "emergency_hide_cleared"}
	position := 0
	for _, terminal := range terminals {
		if position >= len(required) || terminal.Action != required[position] {
			continue
		}
		if terminal.CommandID == "" || terminal.Status != contracts.CommandAccepted || terminal.PreviousRevision != terminal.ExpectedRevision || terminal.ResultingRevision != terminal.ExpectedRevision+1 {
			return errors.New("operator terminal status or revision mismatch")
		}
		candidateAction := terminal.Action == contracts.ActionApprove || terminal.Action == contracts.ActionReject || terminal.Action == contracts.ActionPin || terminal.Action == contracts.ActionUnpin
		if candidateAction != (terminal.TargetCandidateID != "") || terminal.TargetRuleID != "" {
			return errors.New("operator terminal target mismatch")
		}
		if terminal.Reason != reasons[terminal.Action] && !(candidateAction && terminal.Reason == "candidate_expired") {
			return errors.New("operator terminal reason mismatch")
		}
		position++
	}
	if position != len(required) {
		return errors.New("operator terminal script incomplete")
	}
	return nil
}

func validateOperatorReplayProof(path string) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var proof struct {
		SchemaVersion     string `json:"schema_version"`
		CommandID         string `json:"command_id"`
		RequestSHA256     string `json:"request_sha256"`
		ResponseSHA256    string `json:"response_sha256"`
		Status            int    `json:"status"`
		TransportAttempts int    `json:"transport_attempts"`
		DurableEffects    int    `json:"durable_effects"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil || proof.SchemaVersion != "operator_replay_proof.v1" || proof.CommandID == "" || len(proof.RequestSHA256) != 64 || len(proof.ResponseSHA256) != 64 || proof.Status < 200 || proof.Status >= 300 || proof.TransportAttempts != 2 || proof.DurableEffects != 1 {
		return errors.New("operator replay proof invalid")
	}
	return nil
}

func hasOperatorScript(actions []string) bool {
	required := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	position := 0
	for _, action := range actions {
		if position < len(required) && action == required[position] {
			position++
		}
	}
	return position == len(required)
}

func scanRawPrivacy(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	forbidden := map[string]bool{"steamid": true, "accountid": true, "account_id": true, "auth": true, "token": true, "cookie": true, "chat": true}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var frame struct {
			Raw string `json:"raw_base64"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return errors.New("malformed raw frame")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(frame.Raw)
		if err != nil {
			return err
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(decoded))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return errors.New("malformed raw payload")
		}
		var walk func(any) error
		walk = func(item any) error {
			switch typed := item.(type) {
			case map[string]any:
				for key, child := range typed {
					if forbidden[strings.ToLower(key)] {
						return fmt.Errorf("private raw key: %s", key)
					}
					if err := walk(child); err != nil {
						return err
					}
				}
			case []any:
				for _, child := range typed {
					if err := walk(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk(value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func validateSamples(path, root string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	bounds := AcceptedBounds()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var previous Sample
	count := 0
	baselineRSS := int64(0)
	baselineFDs := 0
	baselineGoroutines := 0
	for scanner.Scan() {
		var sample Sample
		if json.Unmarshal(scanner.Bytes(), &sample) != nil {
			return errors.New("invalid measurement sample")
		}
		if count > 0 && (sample.At.Before(previous.At) || sample.At.Sub(previous.At) > 6_000_000_000) {
			return errors.New("measurement cadence exceeded")
		}
		if count == 0 {
			baselineRSS = sample.ProcessRSSBytes
			baselineFDs = sample.ProductTreeFDs
			baselineGoroutines = sample.ProductGoroutines
		}
		if !sample.TelemetryComplete || sample.NotificationCapacity != bounds.NotificationCapacity || sample.CandidateQueueCapacity != bounds.CandidateQueueCapacity || sample.ClockTicksPerSecond <= 0 || sample.LargestGSIRequestBytes <= 0 || sample.LargestGSIRequestBytes > bounds.GSIRequestBytes || sample.QueueOccupancy > bounds.CandidateQueueCapacity || sample.ProductTreeRSSBytes <= 0 || sample.ProductTreeRSSBytes > bounds.CombinedRSSBytes || sample.OBSProcessTreeRSSBytes <= 0 || sample.OBSProcessTreeRSSBytes > 1<<30 || sample.OBSBrowserProcesses <= 0 || sample.ProcessRSSBytes-baselineRSS > bounds.PostWarmupRSSGrowthBytes || sample.ProductTreeFDs > bounds.OpenFDs || sample.ProductTreeFDs-baselineFDs > bounds.OpenFDDelta || sample.ProductGoroutines-baselineGoroutines > bounds.GoroutineDelta || sample.PolicySegments > bounds.PolicySegments || sample.PolicyBytes > bounds.PolicyBytes || sample.NonRawFiles > bounds.NonRawFiles || sample.NonRawBytes > bounds.NonRawBytes || sample.StatusResponseBytes <= 0 || sample.StatusResponseBytes > int(bounds.OtherAPIBytes) || sample.OperatorResponseBytes <= 0 || sample.OperatorResponseBytes > int(bounds.OtherAPIBytes) || sample.OverlayResponseBytes <= 0 || sample.OverlayResponseBytes > int(bounds.OverlayStateBytes) || sample.RawWriteFailures != 0 || sample.ProjectionFailures != 0 || sample.ProductGoroutines <= 0 || sample.ProductTreeProcesses <= 0 || sample.OBSProcessTreeCount <= 0 || sample.CrossPlaneSessionID == "" || sample.CrossPlaneMatchID == "" {
			return errors.New("measurement bound failed")
		}
		if count > 0 && (sample.ProductTreeCPUClockTicks < previous.ProductTreeCPUClockTicks || sample.OBSProcessTreeCPUClockTicks < previous.OBSProcessTreeCPUClockTicks || sample.OBSRenderedFrames < previous.OBSRenderedFrames) {
			return errors.New("process/frame counters regressed")
		}
		if count > 0 {
			elapsed := sample.At.Sub(previous.At)
			browserTicks := sample.OBSBrowserCPUClockTicks - previous.OBSBrowserCPUClockTicks
			if sample.ClockTicksPerSecond != previous.ClockTicksPerSecond || sample.OBSBrowserCPUClockTicks < previous.OBSBrowserCPUClockTicks || float64(browserTicks) > elapsed.Seconds()*float64(sample.ClockTicksPerSecond)*0.05 {
				return errors.New("OBS Browser Source CPU bound failed")
			}
		}
		previous = sample
		count++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if count == 0 || previous.Lag != 0 || previous.ProjectedSequence != previous.Sequence || previous.AcceptedCount != previous.Sequence || previous.OBSMissedFrames > previous.OBSRenderedFrames || previous.OBSSkippedFrames > previous.OBSRenderedFrames {
		return errors.New("measurement terminal reconciliation failed")
	}
	logFiles := 0
	logBytes := int64(0)
	temporary := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		lower := strings.ToLower(entry.Name())
		if strings.Contains(filepath.ToSlash(path), "/logs/") {
			info, _ := entry.Info()
			logFiles++
			logBytes += info.Size()
		}
		if strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".part") {
			temporary = true
		}
		return nil
	})
	if logFiles > bounds.OperationalLogFiles || logBytes > bounds.OperationalLogBytes || temporary {
		return errors.New("log or temporary-file bound failed")
	}
	return nil
}

func validateVisibility(path, sessionDir string) error {
	segments, _ := filepath.Glob(filepath.Join(sessionDir, "*.pcl3"))
	sort.Strings(segments)
	var hideMS, clearMS int64
	for _, segment := range segments {
		commits, err := readPolicySegment(segment)
		if err != nil {
			return err
		}
		for _, commit := range commits {
			if commit.Command == nil {
				continue
			}
			switch commit.Command.Action {
			case contracts.ActionEmergencyHide:
				if hideMS == 0 {
					hideMS = commit.Command.PolicyTimeMS
				}
			case contracts.ActionClearEmergencyHide:
				if hideMS != 0 && clearMS == 0 {
					clearMS = commit.Command.PolicyTimeMS
				}
			}
		}
	}
	if hideMS <= 0 || clearMS <= hideMS {
		return errors.New("ordered emergency-hide/clear command evidence absent")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var firstHidden *VisibilitySample
	var hideRaw, clearRaw uint64
	seen := 0
	for scanner.Scan() {
		var sample VisibilitySample
		if json.Unmarshal(scanner.Bytes(), &sample) != nil || !sample.TelemetryComplete || sample.SessionID != filepath.Base(sessionDir) || (sample.Visibility == "hidden" && sample.ClaimPresent) {
			return errors.New("invalid or claim-bearing visibility sample")
		}
		at := sample.At.UnixMilli()
		if at >= hideMS && at < clearMS {
			if hideRaw == 0 {
				hideRaw = sample.RawSequence
			}
			if sample.Visibility != "hidden" {
				return errors.New("analytical claim revived during emergency-hide interval")
			}
			if firstHidden == nil {
				copy := sample
				firstHidden = &copy
			}
		}
		if at >= clearMS && clearRaw == 0 {
			clearRaw = sample.RawSequence
		}
		seen++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if seen == 0 || firstHidden == nil || firstHidden.At.UnixMilli()-hideMS > int64(AcceptedBounds().FailClosedDeadlineMillis) || clearRaw <= hideRaw {
		return errors.New("two-second fail-closed or continued-raw-capture proof failed")
	}
	return nil
}
