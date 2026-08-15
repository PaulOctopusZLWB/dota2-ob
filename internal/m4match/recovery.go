package m4match

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type attemptValidation struct {
	SchemaVersion      string   `json:"schema_version"`
	RawCount           uint64   `json:"raw_count"`
	CursorSequence     uint64   `json:"cursor_sequence"`
	PolicyCommits      uint64   `json:"policy_commits"`
	ObservationCommits uint64   `json:"observation_commits"`
	LastObservation    uint64   `json:"last_observation_sequence"`
	LastStateSHA256    string   `json:"last_state_sha256"`
	AuditSHA256        []string `json:"audit_sha256"`
	OperatorActions    []string `json:"operator_actions"`
	RecordingFinalized bool     `json:"recording_finalized"`
	OperatorComplete   bool     `json:"operator_complete"`
	PrivacySafe        bool     `json:"privacy_safe"`
	Reconciled         bool     `json:"reconciled"`
	NoCacheRecovery    bool     `json:"no_cache_recovery"`
	MeasurementsPassed bool     `json:"measurements_passed"`
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

func validateCompletedAttempt(root, sessionID string) (attemptValidation, []Artifact, error) {
	sessionDir := filepath.Join(root, "data/sessions", sessionID)
	validation, err := summarizeAttempt(sessionDir)
	if err != nil {
		return validation, nil, err
	}
	validation.RecordingFinalized = recordingFinalized(filepath.Join(root, "recordings"))
	validation.PrivacySafe = scanRawPrivacy(filepath.Join(sessionDir, "raw.jsonl")) == nil
	validation.OperatorComplete = hasOperatorScript(validation.OperatorActions)
	validation.Reconciled = validation.RawCount > 0 && validation.CursorSequence == validation.RawCount && validation.LastObservation <= validation.CursorSequence && validation.ObservationCommits > 0
	validation.MeasurementsPassed = validateSamples(filepath.Join(root, "evidence/samples.jsonl"), root) == nil
	recoveryDir := filepath.Join(root, "evidence/recovery-input", sessionID)
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		return validation, nil, err
	}
	for _, name := range []string{"raw.jsonl"} {
		if err := copyFile(filepath.Join(sessionDir, name), filepath.Join(recoveryDir, name), 0o600); err != nil {
			return validation, nil, err
		}
	}
	segments, _ := filepath.Glob(filepath.Join(sessionDir, "*.pcl3"))
	sort.Strings(segments)
	for _, segment := range segments {
		if err := copyFile(segment, filepath.Join(recoveryDir, filepath.Base(segment)), 0o600); err != nil {
			return validation, nil, err
		}
	}
	rebuilt, err := summarizeAttempt(recoveryDir)
	if err == nil {
		rebuilt.CursorSequence = validation.CursorSequence // cursor is an excluded cache; raw count is its rebuilt authority.
		rebuilt.RecordingFinalized, rebuilt.PrivacySafe, rebuilt.OperatorComplete, rebuilt.Reconciled, rebuilt.MeasurementsPassed = validation.RecordingFinalized, validation.PrivacySafe, validation.OperatorComplete, validation.Reconciled, validation.MeasurementsPassed
		left, _ := semanticValidationBytes(validation)
		right, _ := semanticValidationBytes(rebuilt)
		validation.NoCacheRecovery = bytes.Equal(left, right)
	}
	payload, _ := canonical(validation)
	relative := "evidence/canonical/live-validation.json"
	if err := writePrivate(filepath.Join(root, relative), payload); err != nil {
		return validation, nil, err
	}
	artifact := Artifact{Path: relative, SHA256: payloadSHA(payload), Bytes: int64(len(payload))}
	if !validation.RecordingFinalized || !validation.OperatorComplete || !validation.PrivacySafe || !validation.Reconciled || !validation.NoCacheRecovery || !validation.MeasurementsPassed {
		return validation, []Artifact{artifact}, errors.New("completed attempt validation failed")
	}
	return validation, []Artifact{artifact}, nil
}

func semanticValidationBytes(value attemptValidation) ([]byte, error) {
	value.RecordingFinalized, value.PrivacySafe, value.OperatorComplete, value.Reconciled, value.NoCacheRecovery, value.MeasurementsPassed = false, false, false, false, false, false
	return canonical(value)
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
			}
			for _, audit := range commit.AuditEvents {
				payload, _ := contracts.MarshalCanonical(audit)
				result.AuditSHA256 = append(result.AuditSHA256, payloadSHA(payload))
			}
		}
	}
	sort.Strings(result.AuditSHA256)
	sort.Strings(result.OperatorActions)
	return result, nil
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

func recordingFinalized(root string) bool {
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
			if info != nil && info.Size() > 0 {
				found = true
			}
		}
	}
	return found
}

func hasOperatorScript(actions []string) bool {
	have := map[string]bool{}
	for _, action := range actions {
		have[action] = true
	}
	return have[contracts.ActionApprove] && have[contracts.ActionReject] && have[contracts.ActionEmergencyHide] && have[contracts.ActionClearEmergencyHide] && ((!have[contracts.ActionPin] && !have[contracts.ActionUnpin]) || (have[contracts.ActionPin] && have[contracts.ActionUnpin]))
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
		}
		if sample.QueueOccupancy > bounds.CandidateQueueCapacity || sample.ProcessRSSBytes > bounds.CombinedRSSBytes || sample.ProcessRSSBytes-baselineRSS > bounds.PostWarmupRSSGrowthBytes || sample.ProcessFDs > bounds.OpenFDs || sample.PolicySegments > bounds.PolicySegments || sample.PolicyBytes > bounds.PolicyBytes || sample.NonRawFiles > bounds.NonRawFiles || sample.NonRawBytes > bounds.NonRawBytes || sample.StatusResponseBytes > int(bounds.OtherAPIBytes) || sample.OperatorResponseBytes > int(bounds.OtherAPIBytes) || sample.OverlayResponseBytes > int(bounds.OverlayStateBytes) || sample.RawWriteFailures != 0 || sample.ProjectionFailures != 0 || sample.ProductGoroutines <= 0 {
			return errors.New("measurement bound failed")
		}
		previous = sample
		count++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if count == 0 || previous.Lag != 0 || previous.ProjectedSequence != previous.Sequence || previous.AcceptedCount != previous.Sequence {
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
