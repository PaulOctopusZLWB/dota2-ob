package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
)

func writeTestManifest(t *testing.T, matches []archive.Match) string {
	t.Helper()
	m := archive.Manifest{SchemaVersion: "ti2026.five-replay-probe.v1", Issue: "DOT-72", Matches: matches}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestRoleRegistry(t *testing.T, dir string) string {
	t.Helper()
	reg := roles.Registry{SchemaVersion: "ti2026.roles.v1", TournamentID: "ti2026", Matches: []roles.RoleMatch{{
		MatchID: "1000000001", Teams: []roles.RoleTeam{{
			TeamID: "9823272", TeamName: "Team Yandex", Side: "radiant",
			SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "2026-08-17T00:00:00Z",
			Participants: []roles.RoleRecord{{AccountID: "1000", NominalRole: "1", RoleConfidence: "high"}},
		}},
	}}}
	b, _ := json.Marshal(reg)
	path := filepath.Join(dir, "ti2026-five-replay-role-registry-v1.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplaySubcommandDispatch(t *testing.T) {
	// Unknown subcommand returns usage code 2.
	var out bytes.Buffer
	if code := run([]string{"replay", "bogus"}, &out); code != 2 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	// No subcommand returns usage code 2.
	out.Reset()
	if code := run([]string{"replay"}, &out); code != 2 {
		t.Fatalf("exit=%d", code)
	}
}

func TestReplayVerifyRequiresFlags(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"replay", "verify"}, &out); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(out.String(), "requires") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestReplayRebuildCatalogEmpty(t *testing.T) {
	dataRoot := t.TempDir()
	var out bytes.Buffer
	if code := run([]string{"replay", "rebuild-catalog", "--data-root", dataRoot}, &out); code != 0 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "catalog.json")); err != nil {
		t.Fatalf("catalog not written: %v", err)
	}
}

func TestReplayParseUnknownMatch(t *testing.T) {
	mt := archive.Match{
		MatchID: "9999999999", ArchiveSHA256: "a", DemoSHA256: "b",
		ExpectedTeams:       make([]archive.ExpectedTeam, 2),
		ExpectedParticipants: make([]archive.ExpectedPlayer, 10),
	}
	manifest := writeTestManifest(t, []archive.Match{mt})
	var out bytes.Buffer
	if code := run([]string{"replay", "parse", "--manifest", manifest, "--replay-root", t.TempDir(), "--data-root", t.TempDir(), "--match-id", "1"}, &out); code != 1 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "not in manifest") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestReplayEvaluateMissingGold(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"replay", "evaluate", "--gold", "/nonexistent/gold.json", "--data-root", t.TempDir()}, &out); code != 1 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "gold_read_failed") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestLoadRegistryFallback(t *testing.T) {
	dir := t.TempDir()
	writeTestRoleRegistry(t, dir)
	// Manifest dir must contain the role registry for loadRegistry to find it.
	manDir := filepath.Join(dir, "man")
	os.MkdirAll(manDir, 0o755)
	// Put registry in manDir next to manifest.
	os.Rename(filepath.Join(dir, "ti2026-five-replay-role-registry-v1.json"), filepath.Join(manDir, "ti2026-five-replay-role-registry-v1.json"))
	manifest := filepath.Join(manDir, "manifest.json")
	os.WriteFile(manifest, []byte(`{"schema_version":"x","matches":[]}`), 0o644)
	reg, roleFile := loadRegistry(manifest, dir)
	if reg == nil {
		t.Fatal("registry not loaded")
	}
	if roleFile == "" {
		t.Fatal("empty role file")
	}
}

var _ = store.StatusVerified