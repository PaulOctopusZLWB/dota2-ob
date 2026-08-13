package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
)

func TestMaterializeParseExecutionRejectsValidJSONWithWrongSchemas(t *testing.T) {
	draft := history.ParseExecutionEvidence{
		SchemaVersion:  "history.parse-execution.v1",
		ExecutionID:    "caller-resealed-pass",
		MatchID:        "8941092540",
		ReplaySHA256:   sha256Text("replay"),
		FactsSHA256:    sha256Text("facts"),
		ParserVersion:  "manta/v1.5.0",
		AdapterVersion: "internal/replay/spike.v2",
		ConfigSHA256:   sha256Text("config"),
		Deterministic:  true,
	}
	dir := t.TempDir()
	parsedEnvelope := map[string]any{
		"schema_version": executionArtifactSchema, "execution_id": draft.ExecutionID, "kind": "parsed", "match_id": draft.MatchID,
		"replay_sha256": draft.ReplaySHA256, "facts_sha256": draft.FactsSHA256, "parser_version": draft.ParserVersion,
		"adapter_version": draft.AdapterVersion, "config_sha256": draft.ConfigSHA256, "parsed_payload_sha256": sha256Text("bogus-parsed"),
		"payload_sha256": sha256Text("bogus-parsed"), "payload": map[string]string{"not": "replay facts"},
	}
	normalizedEnvelope := map[string]any{
		"schema_version": executionArtifactSchema, "execution_id": draft.ExecutionID, "kind": "normalized", "match_id": draft.MatchID,
		"replay_sha256": draft.ReplaySHA256, "facts_sha256": draft.FactsSHA256, "parser_version": draft.ParserVersion,
		"adapter_version": draft.AdapterVersion, "config_sha256": draft.ConfigSHA256, "parsed_payload_sha256": sha256Text("bogus-parsed"),
		"payload_sha256": sha256Text("bogus-normalized"), "payload": map[string]string{"not": "normalized facts"},
	}
	parsedBytes, _ := json.Marshal(parsedEnvelope)
	normalizedBytes, _ := json.Marshal(normalizedEnvelope)
	parsedPath, normalizedPath := filepath.Join(dir, "parsed.json"), filepath.Join(dir, "normalized.json")
	if err := os.WriteFile(parsedPath, parsedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(normalizedPath, normalizedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readExecutionPayload(parsedPath, draft, "parsed"); err == nil {
		t.Fatal("caller-manufactured non-schema parse/normalize payloads were sealed as valid execution evidence")
	}
	if _, _, err := readNormalizedExecutionPayload(normalizedPath, draft); err == nil {
		t.Fatal("caller-manufactured non-schema normalized payload was accepted")
	}
}

func TestMaterializeParseExecutionRejectsSemanticallyInvalidReplayFacts(t *testing.T) {
	parsed := replay.ReplayFactsV1{
		SchemaVersion: replay.FactsSchema,
		Provenance:    replay.Provenance{ParserName: replay.ParserName, ParserVersion: replay.ParserVersion, AdapterName: replay.AdapterName, AdapterVersion: replay.AdapterVersion, SchemaVersion: replay.FactsSchema},
	}
	replayPath := filepath.Join(t.TempDir(), "replay.dem")
	if err := os.WriteFile(replayPath, []byte("owned replay bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	replaySHA := sha256Text("owned replay bytes")
	request := parseExecutionRequest{
		ExecutionID: "empty-typed-pass", ReplayPath: replayPath, ExpectedReplaySHA256: replaySHA,
		NormalizeMeta: replay.NormalizeMeta{MatchID: "8941092540", SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60"},
	}
	if _, err := executeParseExecutionWith(t.TempDir(), request, testParseExecutionDependencies(t, replayPath, parsed)); err == nil {
		t.Fatal("semantically empty ReplayFactsV1 was sealed as valid execution evidence")
	}
}

func TestParseExecutionDerivesNormalizationFromOwnedParserResult(t *testing.T) {
	parsed := replay.BuildFacts(&replay.Collected{GameBuild: 6896, ServerName: "caller", MessageCounts: map[string]uint64{"caller": 1}})
	callerNormalized := history.NormalizedMatchFacts{
		SchemaVersion: history.FactsSchema, MatchID: "8941092540", ReplaySHA256: sha256Text("owned replay bytes"),
		SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
		IdentityStatus: contracts.IdentityQuarantined,
		Availability:   history.FactsAvailability{Available: []string{"caller-manufactured"}},
	}
	if err := history.SealNormalizedMatchFacts(&callerNormalized); err != nil {
		t.Fatal(err)
	}
	replayPath := filepath.Join(t.TempDir(), "replay.dem")
	if err := os.WriteFile(replayPath, []byte("owned replay bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	request := parseExecutionRequest{
		ExecutionID: "owned-pass", ReplayPath: replayPath, ExpectedReplaySHA256: sha256Text("owned replay bytes"),
		NormalizeMeta: replay.NormalizeMeta{
			MatchID: "8941092540", ReplaySHA256: sha256Text("caller-label-is-overridden"),
			SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
		},
	}
	result, err := executeParseExecutionWith(t.TempDir(), request, testParseExecutionDependencies(t, replayPath, *parsed))
	if err != nil {
		t.Fatal(err)
	}
	if result.Normalized.ContentSHA256 == callerNormalized.ContentSHA256 || result.Evidence.FactsSHA256 == callerNormalized.ContentSHA256 {
		t.Fatal("independently sealed caller normalization became execution evidence")
	}
	want, err := replay.Normalize(parsed, replay.NormalizeMeta{
		MatchID: "8941092540", ReplaySHA256: sha256Text("owned replay bytes"),
		SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Normalized.ContentSHA256 != want.ContentSHA256 || result.Evidence.FactsSHA256 != want.ContentSHA256 {
		t.Fatal("execution evidence was not derived from production normalization of parser output")
	}
}

func TestParseExecutionOwnsReplayBytesBeforeCallerPathReplacement(t *testing.T) {
	dir := t.TempDir()
	replayPath := filepath.Join(dir, "replay.dem")
	replacementPath := filepath.Join(dir, "replacement.dem")
	const original = "replay A"
	const replacement = "replay B"
	if err := os.WriteFile(replayPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacementPath, []byte(replacement), 0o644); err != nil {
		t.Fatal(err)
	}
	var ownedPath, parsedBytes string
	deps := parseExecutionDependencies{
		inspect: func(path string) (replay.Source2DemoIdentity, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return replay.Source2DemoIdentity{}, err
			}
			if err := os.Rename(replacementPath, replayPath); err != nil {
				return replay.Source2DemoIdentity{}, err
			}
			return replay.Source2DemoIdentity{SHA256: bytesSHA256(b), Bytes: int64(len(b)), Magic: "test"}, nil
		},
		parse: func(path string) (*replay.ParseResult, error) {
			ownedPath = path
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			parsedBytes = string(b)
			facts := replay.BuildFacts(&replay.Collected{GameBuild: 6896, ServerName: parsedBytes, MessageCounts: map[string]uint64{"fixture": 1}})
			hash, err := facts.Hash()
			return &replay.ParseResult{Facts: facts, Hash: hash}, err
		},
		parserVersion:  replay.ParserName + "/" + replay.ParserVersion,
		adapterVersion: replay.AdapterName + "/" + replay.AdapterVersion,
	}
	request := parseExecutionRequest{
		ExecutionID: "owned-path-swap", ReplayPath: replayPath, ExpectedReplaySHA256: sha256Text(original),
		NormalizeMeta: replay.NormalizeMeta{
			MatchID: "8941092540", SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
		},
	}
	result, err := executeParseExecutionWith(t.TempDir(), request, deps)
	if ownedPath != "" {
		if _, statErr := os.Stat(ownedPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("execution-owned replay descriptor was not released: %v", statErr)
		}
	}
	if err != nil {
		return // Failing closed before evidence is also acceptable.
	}
	if parsedBytes != original {
		t.Fatalf("evidence bound to replay A sha after parser consumed %q", parsedBytes)
	}
	if result.Evidence.ReplaySHA256 != sha256Text(original) || result.Parsed.Meta.ServerName != original {
		t.Fatal("owned replay identity and parsed payload diverged")
	}
}

func TestParseExecutionReleasesOwnedReplayAfterParserErrorWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	replayPath := filepath.Join(dir, "replay.dem")
	if err := os.WriteFile(replayPath, []byte("replay A"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ownedPath string
	deps := parseExecutionDependencies{
		inspect: func(path string) (replay.Source2DemoIdentity, error) {
			b, err := os.ReadFile(path)
			return replay.Source2DemoIdentity{SHA256: bytesSHA256(b), Bytes: int64(len(b)), Magic: "test"}, err
		},
		parse: func(path string) (*replay.ParseResult, error) {
			ownedPath = path
			return nil, errors.New("injected parser failure")
		},
		parserVersion:  replay.ParserName + "/" + replay.ParserVersion,
		adapterVersion: replay.AdapterName + "/" + replay.AdapterVersion,
	}
	executionRoot := t.TempDir()
	_, err := executeParseExecutionWith(executionRoot, parseExecutionRequest{
		ExecutionID: "owned-parser-error", ReplayPath: replayPath, ExpectedReplaySHA256: sha256Text("replay A"),
		NormalizeMeta: replay.NormalizeMeta{MatchID: "8941092540", SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60"},
	}, deps)
	if err == nil {
		t.Fatal("parser failure produced usable evidence")
	}
	if _, statErr := os.Stat(ownedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("execution-owned replay descriptor survived parser failure: %v", statErr)
	}
	entries, readErr := os.ReadDir(executionRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("parser failure wrote durable evidence: %v", entries)
	}
}

func TestParseExecutionCleanupFailureCannotPublishEvidence(t *testing.T) {
	dir := t.TempDir()
	replayPath := filepath.Join(dir, "replay.dem")
	if err := os.WriteFile(replayPath, []byte("replay A"), 0o644); err != nil {
		t.Fatal(err)
	}
	facts := replay.BuildFacts(&replay.Collected{GameBuild: 6896, ServerName: "cleanup", MessageCounts: map[string]uint64{"fixture": 1}})
	deps := testParseExecutionDependencies(t, replayPath, *facts)
	deps.releaseOwned = func(owned ownedReplayInput) error {
		if err := owned.file.Close(); err != nil {
			return err
		}
		return errors.New("injected cleanup failure")
	}
	executionRoot := t.TempDir()
	_, err := executeParseExecutionWith(executionRoot, parseExecutionRequest{
		ExecutionID: "owned-cleanup-error", ReplayPath: replayPath, ExpectedReplaySHA256: sha256Text("replay A"),
		NormalizeMeta: replay.NormalizeMeta{
			MatchID: "8941092540", SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60", GameBuild: 6896,
		},
	}, deps)
	if err == nil {
		t.Fatal("cleanup failure published usable evidence")
	}
	entries, readErr := os.ReadDir(executionRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("cleanup failure wrote durable evidence: %v", entries)
	}
}

func testParseExecutionDependencies(t *testing.T, replayPath string, facts replay.ReplayFactsV1) parseExecutionDependencies {
	t.Helper()
	wantReplay, err := os.ReadFile(replayPath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := facts.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return parseExecutionDependencies{
		inspect: func(path string) (replay.Source2DemoIdentity, error) {
			b, err := os.ReadFile(path)
			if string(b) != string(wantReplay) {
				t.Fatalf("inspected bytes %q, want owned source bytes %q", b, wantReplay)
			}
			return replay.Source2DemoIdentity{SHA256: bytesSHA256(b), Bytes: int64(len(b)), Magic: "test"}, err
		},
		parse: func(path string) (*replay.ParseResult, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if string(b) != string(wantReplay) {
				t.Fatalf("parsed bytes %q, want owned source bytes %q", b, wantReplay)
			}
			copyFacts := facts
			return &replay.ParseResult{Facts: &copyFacts, Hash: bytesSHA256(payload)}, nil
		},
		parserVersion:  replay.ParserName + "/" + replay.ParserVersion,
		adapterVersion: replay.AdapterName + "/" + replay.AdapterVersion,
	}
}
