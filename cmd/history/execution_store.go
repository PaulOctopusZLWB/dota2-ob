package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
)

const executionArtifactSchema = "history.parse-run-artifact.v1"

type executionPayloadBinding struct {
	SchemaVersion       string `json:"schema_version"`
	ExecutionID         string `json:"execution_id"`
	Kind                string `json:"kind"`
	MatchID             string `json:"match_id"`
	ReplaySHA256        string `json:"replay_sha256"`
	FactsSHA256         string `json:"facts_sha256"`
	ParserVersion       string `json:"parser_version"`
	AdapterVersion      string `json:"adapter_version"`
	ConfigSHA256        string `json:"config_sha256"`
	ParsedPayloadSHA256 string `json:"parsed_payload_sha256"`
	PayloadSHA256       string `json:"payload_sha256"`
}

type parsedExecutionArtifact struct {
	executionPayloadBinding
	Payload replay.ReplayFactsV1 `json:"payload"`
}

type normalizedExecutionArtifact struct {
	executionPayloadBinding
	Payload history.NormalizedMatchFacts `json:"payload"`
}

type parseExecutionRequest struct {
	ExecutionID          string
	ReplayPath           string
	ExpectedReplaySHA256 string
	NormalizeMeta        replay.NormalizeMeta
	ParticipantMapping   []replay.ParticipantMapping
}

type parseExecutionResult struct {
	Evidence   history.ParseExecutionEvidence
	Parsed     replay.ReplayFactsV1
	Normalized history.NormalizedMatchFacts
	Metrics    replay.Metrics
}

type parseExecutionDependencies struct {
	inspect        func(string) (replay.Source2DemoIdentity, error)
	parse          func(string) (*replay.ParseResult, error)
	releaseOwned   func(ownedReplayInput) error
	parserVersion  string
	adapterVersion string
}

type ownedReplayInput struct {
	file *os.File
	path string
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

func executeParseExecution(root string, request parseExecutionRequest) (parseExecutionResult, error) {
	return executeParseExecutionWith(root, request, parseExecutionDependencies{
		inspect: replay.InspectSource2DemoFile, parse: replay.ParseFile,
		releaseOwned:   releaseOwnedReplayInput,
		parserVersion:  replay.ParserName + "/" + replay.ParserVersion,
		adapterVersion: replay.AdapterName + "/" + replay.AdapterVersion,
	})
}

func executeParseExecutionWith(root string, request parseExecutionRequest, deps parseExecutionDependencies) (parseExecutionResult, error) {
	if root == "" || request.ExecutionID == "" || request.ReplayPath == "" || request.ExpectedReplaySHA256 == "" || deps.inspect == nil || deps.parse == nil || deps.parserVersion == "" || deps.adapterVersion == "" {
		return parseExecutionResult{}, errors.New("invalid parse execution request")
	}
	owned, err := acquireOwnedReplayInput(request.ReplayPath)
	if err != nil {
		return parseExecutionResult{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = owned.file.Close()
		}
	}()
	identity, err := deps.inspect(owned.path)
	if err != nil || identity.SHA256 != request.ExpectedReplaySHA256 {
		return parseExecutionResult{}, errors.New("parse execution replay identity mismatch")
	}
	parsedResult, err := deps.parse(owned.path)
	if err != nil || parsedResult == nil || parsedResult.Facts == nil {
		return parseExecutionResult{}, errors.New("parse execution parser failed")
	}
	parsed := *parsedResult.Facts
	canonicalParsed, err := parsed.CanonicalJSON()
	if err != nil || bytesSHA256(canonicalParsed) != parsedResult.Hash {
		return parseExecutionResult{}, errors.New("parse execution parser result identity mismatch")
	}
	releaseOwned := deps.releaseOwned
	if releaseOwned == nil {
		releaseOwned = releaseOwnedReplayInput
	}
	if err := releaseOwned(owned); err != nil {
		return parseExecutionResult{}, fmt.Errorf("parse execution release owned replay: %w", err)
	}
	closed = true
	request.NormalizeMeta.ReplaySHA256 = identity.SHA256
	normalized, err := replay.Normalize(&parsed, request.NormalizeMeta, request.ParticipantMapping)
	if err != nil {
		return parseExecutionResult{}, err
	}
	configSHA, err := parseExecutionConfigSHA(request.NormalizeMeta, request.ParticipantMapping)
	if err != nil {
		return parseExecutionResult{}, err
	}
	draft := history.ParseExecutionEvidence{
		SchemaVersion: "history.parse-execution.v1", ExecutionID: request.ExecutionID,
		MatchID: request.NormalizeMeta.MatchID, ReplaySHA256: identity.SHA256, FactsSHA256: normalized.ContentSHA256,
		ParserVersion: deps.parserVersion, AdapterVersion: deps.adapterVersion, ConfigSHA256: configSHA, Deterministic: true,
	}
	if err := validateReplayFactsPayload(parsed, draft); err != nil {
		return parseExecutionResult{}, err
	}
	if err := validateNormalizedFactsPayload(*normalized, draft); err != nil {
		return parseExecutionResult{}, err
	}
	draft.ContentSHA256, draft.ParsedArtifactSHA256, draft.NormalizedArtifactSHA256, draft.RunArtifactSHA256, draft.CheckpointSHA256 = "", "", "", "", ""
	canonicalNormalized, err := contracts.MarshalCanonical(*normalized)
	if err != nil {
		return parseExecutionResult{}, err
	}
	parsedPayloadSHA := bytesSHA256(canonicalParsed)
	parsedBinding := executionPayloadEnvelope(draft, "parsed", bytesSHA256(canonicalParsed), parsedPayloadSHA)
	parsedEnvelope, err := json.Marshal(parsedExecutionArtifact{executionPayloadBinding: parsedBinding, Payload: parsed})
	if err != nil {
		return parseExecutionResult{}, err
	}
	normalizedBinding := executionPayloadEnvelope(draft, "normalized", bytesSHA256(canonicalNormalized), parsedPayloadSHA)
	normalizedEnvelope, err := json.Marshal(normalizedExecutionArtifact{executionPayloadBinding: normalizedBinding, Payload: *normalized})
	if err != nil {
		return parseExecutionResult{}, err
	}
	parsedSHA, normalizedSHA := bytesSHA256(parsedEnvelope), bytesSHA256(normalizedEnvelope)
	draft.ParsedArtifactSHA256, draft.NormalizedArtifactSHA256 = parsedSHA, normalizedSHA
	dir := executionDir(root, draft.MatchID, draft.ExecutionID)
	if err := atomicfile.WriteFile(filepath.Join(dir, "parsed.json"), append(parsedEnvelope, '\n'), 0o644); err != nil {
		return parseExecutionResult{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "normalized.json"), append(normalizedEnvelope, '\n'), 0o644); err != nil {
		return parseExecutionResult{}, err
	}
	checkpoint := executionBinding(draft, "checkpoint")
	checkpointBytes, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return parseExecutionResult{}, err
	}
	draft.CheckpointSHA256 = bytesSHA256(checkpointBytes)
	if err := atomicfile.WriteFile(filepath.Join(dir, "checkpoint.json"), append(checkpointBytes, '\n'), 0o644); err != nil {
		return parseExecutionResult{}, err
	}
	runBytes, err := contracts.MarshalCanonical(executionBinding(draft, "run"))
	if err != nil {
		return parseExecutionResult{}, err
	}
	draft.RunArtifactSHA256 = bytesSHA256(runBytes)
	if err := atomicfile.WriteFile(filepath.Join(dir, "run.json"), append(runBytes, '\n'), 0o644); err != nil {
		return parseExecutionResult{}, err
	}
	if err := sealExecutionEvidence(&draft); err != nil {
		return parseExecutionResult{}, err
	}
	if err := validateParseExecutionArtifacts(root, draft); err != nil {
		return parseExecutionResult{}, err
	}
	return parseExecutionResult{Evidence: draft, Parsed: parsed, Normalized: *normalized, Metrics: parsedResult.Metrics}, nil
}

func releaseOwnedReplayInput(owned ownedReplayInput) error {
	return owned.file.Close()
}

func acquireOwnedReplayInput(sourcePath string) (ownedReplayInput, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return ownedReplayInput{}, err
	}
	temporary, err := os.CreateTemp("", "dota2-ob-replay-execution-*.dem")
	if err != nil {
		_ = source.Close()
		return ownedReplayInput{}, err
	}
	if err := os.Remove(temporary.Name()); err != nil {
		_ = source.Close()
		_ = temporary.Close()
		return ownedReplayInput{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = temporary.Close()
		}
	}()
	if _, err := io.Copy(temporary, source); err != nil {
		_ = source.Close()
		return ownedReplayInput{}, err
	}
	if err := source.Close(); err != nil {
		return ownedReplayInput{}, err
	}
	if err := temporary.Sync(); err != nil {
		return ownedReplayInput{}, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return ownedReplayInput{}, err
	}
	// The staging file was unlinked before bytes were copied. The inspector and
	// parser can only reopen its exact inode through this held descriptor;
	// caller-visible path replacement cannot change the bytes either consumes.
	procPath := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(temporary.Fd()), 10))
	if _, err := os.Stat(procPath); err != nil {
		return ownedReplayInput{}, fmt.Errorf("parse execution owned replay descriptor: %w", err)
	}
	ok = true
	return ownedReplayInput{file: temporary, path: procPath}, nil
}

func parseExecutionConfigSHA(meta replay.NormalizeMeta, mapping []replay.ParticipantMapping) (string, error) {
	b, err := contracts.MarshalCanonical(struct {
		SchemaVersion string                      `json:"schema_version"`
		Meta          replay.NormalizeMeta        `json:"meta"`
		Mapping       []replay.ParticipantMapping `json:"mapping"`
	}{SchemaVersion: "history.parse-normalize-config.v1", Meta: meta, Mapping: mapping})
	if err != nil {
		return "", err
	}
	return bytesSHA256(b), nil
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
	parsedArtifact, parsedFacts, err := readExecutionPayload(filepath.Join(dir, "parsed.json"), pass, "parsed")
	if err != nil {
		return err
	}
	normalizedArtifact, normalizedFacts, err := readNormalizedExecutionPayload(filepath.Join(dir, "normalized.json"), pass)
	if err != nil {
		return err
	}
	if parsedArtifact.ParsedPayloadSHA256 != parsedArtifact.PayloadSHA256 || normalizedArtifact.ParsedPayloadSHA256 != parsedArtifact.PayloadSHA256 {
		return errors.New("parse execution payload lineage mismatch")
	}
	if parserIdentity(parsedFacts) != pass.ParserVersion || adapterIdentity(parsedFacts) != pass.AdapterVersion || normalizedFacts.ContentSHA256 != pass.FactsSHA256 || normalizedFacts.MatchID != pass.MatchID || normalizedFacts.ReplaySHA256 != pass.ReplaySHA256 {
		return errors.New("parse execution typed payload binding mismatch")
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

func executionPayloadEnvelope(pass history.ParseExecutionEvidence, kind, payloadSHA, parsedPayloadSHA string) executionPayloadBinding {
	return executionPayloadBinding{
		SchemaVersion: executionArtifactSchema, ExecutionID: pass.ExecutionID, Kind: kind,
		MatchID: pass.MatchID, ReplaySHA256: pass.ReplaySHA256, FactsSHA256: pass.FactsSHA256,
		ParserVersion: pass.ParserVersion, AdapterVersion: pass.AdapterVersion, ConfigSHA256: pass.ConfigSHA256,
		ParsedPayloadSHA256: parsedPayloadSHA, PayloadSHA256: payloadSHA,
	}
}

func validateReplayFactsPayload(facts replay.ReplayFactsV1, pass history.ParseExecutionEvidence) error {
	if facts.SchemaVersion != replay.FactsSchema || facts.Provenance.SchemaVersion != replay.FactsSchema || facts.Provenance.ParserName == "" || facts.Provenance.ParserVersion == "" || facts.Provenance.AdapterName == "" || facts.Provenance.AdapterVersion == "" || parserIdentity(facts) != pass.ParserVersion || adapterIdentity(facts) != pass.AdapterVersion {
		return errors.New("parsed execution payload identity mismatch")
	}
	if err := validateReplayFactsStructure(facts); err != nil {
		return err
	}
	return nil
}

func validateReplayFactsStructure(facts replay.ReplayFactsV1) error {
	if facts.MessageCounts == nil || facts.CombatLogTypeCounts == nil || facts.ItemUses == nil || len(facts.Availability.Available)+len(facts.Availability.Deferred)+len(facts.Availability.Unavailable) == 0 {
		return errors.New("parsed execution payload is incomplete")
	}
	var combatTotal uint64
	for _, count := range facts.CombatLogTypeCounts {
		if combatTotal > ^uint64(0)-count {
			return errors.New("parsed execution combat counts overflow")
		}
		combatTotal += count
	}
	if combatTotal != facts.CombatLogTotal {
		return errors.New("parsed execution combat total mismatch")
	}
	if !sort.SliceIsSorted(facts.Heroes, func(i, j int) bool { return facts.Heroes[i].Name < facts.Heroes[j].Name }) {
		return errors.New("parsed execution heroes are not canonical")
	}
	seenHeroes := map[string]bool{}
	for _, hero := range facts.Heroes {
		if hero.Name == "" || hero.ItemUses == nil || seenHeroes[hero.Name] {
			return errors.New("parsed execution hero identity mismatch")
		}
		seenHeroes[hero.Name] = true
	}
	return nil
}

func validateNormalizedFactsPayload(facts history.NormalizedMatchFacts, pass history.ParseExecutionEvidence) error {
	if facts.Validate() != nil {
		return errors.New("normalized execution payload is not NormalizedMatchFacts")
	}
	if facts.ContentSHA256 != pass.FactsSHA256 || facts.MatchID != pass.MatchID || facts.ReplaySHA256 != pass.ReplaySHA256 {
		return errors.New("normalized execution payload identity mismatch")
	}
	return nil
}

func readExecutionPayload(path string, pass history.ParseExecutionEvidence, kind string) (parsedExecutionArtifact, replay.ReplayFactsV1, error) {
	var artifact parsedExecutionArtifact
	b, err := os.ReadFile(path)
	if err != nil || contracts.DecodeStrict(trimFinalNewline(b), &artifact) != nil || validatePayloadEnvelope(artifact.executionPayloadBinding, pass, kind) != nil {
		return artifact, replay.ReplayFactsV1{}, errors.New("parse execution parsed envelope mismatch")
	}
	payload, err := artifact.Payload.CanonicalJSON()
	if err != nil || bytesSHA256(payload) != artifact.PayloadSHA256 || validateReplayFactsPayload(artifact.Payload, pass) != nil {
		return artifact, replay.ReplayFactsV1{}, errors.New("parse execution parsed payload mismatch")
	}
	return artifact, artifact.Payload, nil
}

func readNormalizedExecutionPayload(path string, pass history.ParseExecutionEvidence) (normalizedExecutionArtifact, history.NormalizedMatchFacts, error) {
	var artifact normalizedExecutionArtifact
	b, err := os.ReadFile(path)
	if err != nil || contracts.DecodeStrict(trimFinalNewline(b), &artifact) != nil || validatePayloadEnvelope(artifact.executionPayloadBinding, pass, "normalized") != nil {
		return artifact, history.NormalizedMatchFacts{}, errors.New("parse execution normalized envelope mismatch")
	}
	payload, err := contracts.MarshalCanonical(artifact.Payload)
	if err != nil || bytesSHA256(payload) != artifact.PayloadSHA256 || validateNormalizedFactsPayload(artifact.Payload, pass) != nil {
		return artifact, history.NormalizedMatchFacts{}, errors.New("parse execution normalized payload mismatch")
	}
	return artifact, artifact.Payload, nil
}

func validatePayloadEnvelope(a executionPayloadBinding, pass history.ParseExecutionEvidence, kind string) error {
	if a.SchemaVersion != executionArtifactSchema || a.Kind != kind || a.ExecutionID != pass.ExecutionID || a.MatchID != pass.MatchID || a.ReplaySHA256 != pass.ReplaySHA256 || a.FactsSHA256 != pass.FactsSHA256 || a.ParserVersion != pass.ParserVersion || a.AdapterVersion != pass.AdapterVersion || a.ConfigSHA256 != pass.ConfigSHA256 || a.PayloadSHA256 == "" || a.ParsedPayloadSHA256 == "" {
		return errors.New("parse execution payload envelope mismatch")
	}
	return nil
}

func parserIdentity(f replay.ReplayFactsV1) string {
	return f.Provenance.ParserName + "/" + f.Provenance.ParserVersion
}

func adapterIdentity(f replay.ReplayFactsV1) string {
	return f.Provenance.AdapterName + "/" + f.Provenance.AdapterVersion
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
