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
	normalized := history.NormalizedMatchFacts{
		SchemaVersion: history.FactsSchema, MatchID: "8941092540", ReplaySHA256: sha256Text("replay"),
		SourceEventTime: time.Date(2026, 8, 11, 20, 33, 21, 0, time.UTC), PatchID: "60",
		IdentityStatus: contracts.IdentityQuarantined,
	}
	if err := history.SealNormalizedMatchFacts(&normalized); err != nil {
		t.Fatal(err)
	}
	parsed := replay.ReplayFactsV1{
		SchemaVersion: replay.FactsSchema,
		Provenance:    replay.Provenance{ParserName: replay.ParserName, ParserVersion: replay.ParserVersion, AdapterName: replay.AdapterName, AdapterVersion: replay.AdapterVersion, SchemaVersion: replay.FactsSchema},
	}
	draft := history.ParseExecutionEvidence{
		SchemaVersion: "history.parse-execution.v1", ExecutionID: "empty-typed-pass", MatchID: normalized.MatchID,
		ReplaySHA256: normalized.ReplaySHA256, FactsSHA256: normalized.ContentSHA256,
		ParserVersion: replay.ParserName + "/" + replay.ParserVersion, AdapterVersion: replay.AdapterName + "/" + replay.AdapterVersion,
		ConfigSHA256: sha256Text("config"), Deterministic: true,
	}
	if _, err := materializeParseExecution(t.TempDir(), draft, parseExecutionMaterial{Parsed: parsed, Normalized: normalized}); err == nil {
		t.Fatal("semantically empty ReplayFactsV1 was sealed as valid execution evidence")
	}
}
