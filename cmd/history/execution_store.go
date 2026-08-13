package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
)

const executionArtifactSchema = "history.parse-run-artifact.v1"

type executionPayloadArtifact struct {
	SchemaVersion string          `json:"schema_version"`
	ExecutionID   string          `json:"execution_id"`
	Kind          string          `json:"kind"`
	PayloadSHA256 string          `json:"payload_sha256"`
	Payload       json.RawMessage `json:"payload"`
}

type executionBindingArtifact struct {
	SchemaVersion            string `json:"schema_version"`
	Kind                     string `json:"kind"`
	ExecutionID              string `json:"execution_id"`
	MatchID                  string `json:"match_id"`
	ReplaySHA256             string `json:"replay_sha256"`
	FactsSHA256              string `json:"facts_sha256"`
	ParserVersion            string `json:"parser_version"`
	AdapterVersion           string `json:"adapter_version"`
	ConfigSHA256             string `json:"config_sha256"`
	ParsedArtifactSHA256     string `json:"parsed_artifact_sha256"`
	NormalizedArtifactSHA256 string `json:"normalized_artifact_sha256"`
	CheckpointSHA256         string `json:"checkpoint_sha256,omitempty"`
}

func materializeParseExecution(root string, draft history.ParseExecutionEvidence, parsedJSON, normalizedJSON []byte) (history.ParseExecutionEvidence, error) {
	if root == "" || draft.ExecutionID == "" || draft.MatchID == "" || !json.Valid(parsedJSON) || !json.Valid(normalizedJSON) {
		return history.ParseExecutionEvidence{}, errors.New("invalid parse execution material")
	}
	draft.ContentSHA256, draft.ParsedArtifactSHA256, draft.NormalizedArtifactSHA256, draft.RunArtifactSHA256, draft.CheckpointSHA256 = "", "", "", "", ""
	parsed, parsedSHA, err := marshalExecutionPayload(draft.ExecutionID, "parsed", parsedJSON)
	if err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	normalized, normalizedSHA, err := marshalExecutionPayload(draft.ExecutionID, "normalized", normalizedJSON)
	if err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	draft.ParsedArtifactSHA256, draft.NormalizedArtifactSHA256 = parsedSHA, normalizedSHA
	dir := executionDir(root, draft.MatchID, draft.ExecutionID)
	if err := atomicfile.WriteFile(filepath.Join(dir, "parsed.json"), append(parsed, '\n'), 0o644); err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "normalized.json"), append(normalized, '\n'), 0o644); err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	checkpoint := executionBinding(draft, "checkpoint")
	checkpointBytes, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	draft.CheckpointSHA256 = bytesSHA256(checkpointBytes)
	if err := atomicfile.WriteFile(filepath.Join(dir, "checkpoint.json"), append(checkpointBytes, '\n'), 0o644); err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	runBytes, err := contracts.MarshalCanonical(executionBinding(draft, "run"))
	if err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	draft.RunArtifactSHA256 = bytesSHA256(runBytes)
	if err := atomicfile.WriteFile(filepath.Join(dir, "run.json"), append(runBytes, '\n'), 0o644); err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	if err := sealExecutionEvidence(&draft); err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	if err := validateParseExecutionArtifacts(root, draft); err != nil {
		return history.ParseExecutionEvidence{}, err
	}
	return draft, nil
}

func validateParseExecutionArtifacts(root string, pass history.ParseExecutionEvidence) error {
	if root == "" || pass.Validate() != nil {
		return errors.New("invalid durable parse execution")
	}
	dir := executionDir(root, pass.MatchID, pass.ExecutionID)
	checks := []struct{ name, sha string }{{"parsed.json", pass.ParsedArtifactSHA256}, {"normalized.json", pass.NormalizedArtifactSHA256}, {"checkpoint.json", pass.CheckpointSHA256}, {"run.json", pass.RunArtifactSHA256}}
	for _, check := range checks {
		b, err := os.ReadFile(filepath.Join(dir, check.name))
		if err != nil || bytesSHA256(trimFinalNewline(b)) != check.sha {
			return errors.New("parse execution artifact missing or mismatched")
		}
	}
	checkpoint := executionBinding(pass, "checkpoint")
	checkpoint.CheckpointSHA256 = ""
	wantCheckpoint, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return err
	}
	gotCheckpoint, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil || string(trimFinalNewline(gotCheckpoint)) != string(wantCheckpoint) {
		return errors.New("parse execution checkpoint binding mismatch")
	}
	wantRun, err := contracts.MarshalCanonical(executionBinding(pass, "run"))
	if err != nil {
		return err
	}
	gotRun, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil || string(trimFinalNewline(gotRun)) != string(wantRun) {
		return errors.New("parse execution run binding mismatch")
	}
	return nil
}

func sealExecutionEvidence(p *history.ParseExecutionEvidence) error {
	p.ContentSHA256 = ""
	b, err := contracts.MarshalCanonical(*p)
	if err != nil {
		return err
	}
	p.ContentSHA256 = bytesSHA256(append([]byte("parse-execution:"), b...))
	return p.Validate()
}

func marshalExecutionPayload(executionID, kind string, payload []byte) ([]byte, string, error) {
	a := executionPayloadArtifact{SchemaVersion: executionArtifactSchema, ExecutionID: executionID, Kind: kind, PayloadSHA256: bytesSHA256(payload), Payload: json.RawMessage(payload)}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, "", err
	}
	return b, bytesSHA256(b), nil
}

func executionBinding(p history.ParseExecutionEvidence, kind string) executionBindingArtifact {
	return executionBindingArtifact{SchemaVersion: executionArtifactSchema, Kind: kind, ExecutionID: p.ExecutionID, MatchID: p.MatchID, ReplaySHA256: p.ReplaySHA256, FactsSHA256: p.FactsSHA256, ParserVersion: p.ParserVersion, AdapterVersion: p.AdapterVersion, ConfigSHA256: p.ConfigSHA256, ParsedArtifactSHA256: p.ParsedArtifactSHA256, NormalizedArtifactSHA256: p.NormalizedArtifactSHA256, CheckpointSHA256: p.CheckpointSHA256}
}

func executionDir(root, matchID, executionID string) string {
	return filepath.Join(root, bytesSHA256([]byte(matchID)), bytesSHA256([]byte(executionID)))
}
func bytesSHA256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func trimFinalNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		return b[:len(b)-1]
	}
	return b
}
