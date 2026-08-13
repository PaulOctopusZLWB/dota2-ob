package main

import (
	"encoding/json"
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

func testParseExecutionDependencies(t *testing.T, replayPath string, facts replay.ReplayFactsV1) parseExecutionDependencies {
	t.Helper()
	payload, err := facts.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return parseExecutionDependencies{
		inspect: func(path string) (replay.Source2DemoIdentity, error) {
			if path != replayPath {
				t.Fatalf("inspected path %q, want %q", path, replayPath)
			}
			b, err := os.ReadFile(path)
			return replay.Source2DemoIdentity{SHA256: bytesSHA256(b), Bytes: int64(len(b)), Magic: "test"}, err
		},
		parse: func(path string) (*replay.ParseResult, error) {
			if path != replayPath {
				t.Fatalf("parsed path %q, want %q", path, replayPath)
			}
			copyFacts := facts
			return &replay.ParseResult{Facts: &copyFacts, Hash: bytesSHA256(payload)}, nil
		},
		parserVersion:  replay.ParserName + "/" + replay.ParserVersion,
		adapterVersion: replay.AdapterName + "/" + replay.AdapterVersion,
	}
}
