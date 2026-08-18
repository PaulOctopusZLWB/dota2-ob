package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
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

// writeTestRoleRegistry writes a role registry into dir and returns its path.
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

// copyContractFiles copies the frozen metric registry, scoring contract, and
// team scoring registry into dir (loadRoleInputs requires them as
// publication/contract inputs).
func copyContractFiles(t *testing.T, dir string) {
	t.Helper()
	specsDir := "../../docs/specs"
	for _, name := range []string{"ti2026-role-phase-metrics-v1.json", "ti2026-metric-closure-v1.json", "ti2026-radar-scoring-v1.json", "ti2026-team-scoring-v1.json"} {
		b, err := os.ReadFile(filepath.Join(specsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
		ExpectedTeams:        make([]archive.ExpectedTeam, 2),
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

func TestLoadRoleInputs(t *testing.T) {
	dir := t.TempDir()
	writeTestRoleRegistry(t, dir)
	copyContractFiles(t, dir)
	manDir := filepath.Join(dir, "man")
	os.MkdirAll(manDir, 0o755)
	for _, name := range []string{"ti2026-five-replay-role-registry-v1.json", "ti2026-role-phase-metrics-v1.json", "ti2026-metric-closure-v1.json", "ti2026-radar-scoring-v1.json", "ti2026-team-scoring-v1.json"} {
		os.Rename(filepath.Join(dir, name), filepath.Join(manDir, name))
	}
	manifest := filepath.Join(manDir, "manifest.json")
	os.WriteFile(manifest, []byte(`{"schema_version":"x","matches":[]}`), 0o644)
	ri, err := loadRoleInputs(manifest, dir)
	if err != nil {
		t.Fatalf("loadRoleInputs: %v", err)
	}
	if ri.Registry == nil || ri.RegistrySHA == "" {
		t.Fatal("registry or hash not loaded")
	}
	if ri.RegistryPath == "" {
		t.Fatal("empty registry path")
	}
	if ri.MetricRegistry == nil || ri.MetricRegistrySHA == "" {
		t.Fatal("metric registry or hash not loaded")
	}
	if ri.ScoringContract == nil || ri.ScoringContractSHA == "" {
		t.Fatal("scoring contract or hash not loaded")
	}
	if ri.TeamContract == nil || ri.TeamContractSHA == "" {
		t.Fatal("team scoring registry or hash not loaded")
	}
	// Missing overrides file is fine (empty set), but a missing registry is a
	// hard error (publication gate).
	os.Remove(filepath.Join(manDir, "ti2026-five-replay-role-registry-v1.json"))
	if _, err := loadRoleInputs(manifest, dir); err == nil {
		t.Fatal("expected error for missing role registry")
	}
}

// fullProbeManifestDir writes a valid five-probe manifest plus its role
// registry into a temp dir and returns the manifest path.
func fullProbeManifestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	reg := roles.Registry{SchemaVersion: "ti2026.roles.v1", TournamentID: "ti2026"}
	matches := []archive.Match{}
	for mi, matchID := range []string{"1000000001", "1000000002", "1000000003", "1000000004", "1000000005"} {
		mt := archive.Match{
			MatchID: matchID, ArchiveSHA256: "a", DemoSHA256: "b",
			ArchiveRelativePath: matchID + ".dem.bz2", DemoRelativePath: "dem/" + matchID + ".dem",
			PublicDurationSeconds: 600,
			ExpectedTeams: []archive.ExpectedTeam{
				{TeamID: "9823272", TeamName: "TY", Side: "radiant"},
				{TeamID: "5017210", TeamName: "TR", Side: "dire"},
			},
		}
		rm := roles.RoleMatch{MatchID: matchID, Teams: []roles.RoleTeam{
			{TeamID: "9823272", TeamName: "TY", Side: "radiant", SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "t"},
			{TeamID: "5017210", TeamName: "TR", Side: "dire", SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "t"},
		}}
		for i := 0; i < 5; i++ {
			acct := fmt.Sprintf("%d", 1000+i+mi*10)
			mt.ExpectedParticipants = append(mt.ExpectedParticipants, archive.ExpectedPlayer{
				AccountID: acct, Side: "radiant", ExpectedHeroID: 145,
			})
			rm.Teams[0].Participants = append(rm.Teams[0].Participants, roles.RoleRecord{
				AccountID: acct, NominalRole: fmt.Sprintf("%d", i+1), RoleConfidence: "high",
			})
		}
		for i := 0; i < 5; i++ {
			acct := fmt.Sprintf("%d", 2000+i+mi*10)
			mt.ExpectedParticipants = append(mt.ExpectedParticipants, archive.ExpectedPlayer{
				AccountID: acct, Side: "dire", ExpectedHeroID: 19,
			})
			rm.Teams[1].Participants = append(rm.Teams[1].Participants, roles.RoleRecord{
				AccountID: acct, NominalRole: fmt.Sprintf("%d", i+1), RoleConfidence: "high",
			})
		}
		matches = append(matches, mt)
		reg.Matches = append(reg.Matches, rm)
	}
	mb, _ := json.Marshal(archive.Manifest{SchemaVersion: "ti2026.five-replay-probe.v1", Issue: "DOT-72", Matches: matches})
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, mb, 0o644); err != nil {
		t.Fatal(err)
	}
	rb, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(dir, "ti2026-five-replay-role-registry-v1.json"), rb, 0o644); err != nil {
		t.Fatal(err)
	}
	copyContractFiles(t, dir)
	return manifest
}

// TestProbeNonexistentRootFailsNonZero proves that a probe whose inputs are
// all missing exits non-zero (0/5 verified) while still writing its summary
// and per-match terminal states (missing).
func TestProbeNonexistentRootFailsNonZero(t *testing.T) {
	manifest := fullProbeManifestDir(t)
	dataRoot := t.TempDir()
	var out bytes.Buffer
	code := run([]string{"replay", "probe", "--manifest", manifest, "--replay-root", filepath.Join(t.TempDir(), "does-not-exist"), "--data-root", dataRoot}, &out)
	if code == 0 {
		t.Fatalf("probe with missing root exited 0; output=%s", out.String())
	}
	if !strings.Contains(out.String(), "0/5 verified") {
		t.Fatalf("probe summary missing 0/5 verified: %s", out.String())
	}
	// Per-match terminal states must still be recorded (missing statuses).
	var summary struct {
		StatusCounts map[string]int `json:"status_counts"`
	}
	// The summary JSON is the first line of output; extract by finding it.
	lines := strings.Split(out.String(), "\n")
	decoded := false
	for _, line := range lines {
		if strings.HasPrefix(line, "{") {
			if err := json.Unmarshal([]byte(line), &summary); err == nil {
				decoded = true
				break
			}
		}
	}
	if !decoded {
		t.Fatalf("no summary JSON in output: %s", out.String())
	}
	if summary.StatusCounts["missing"] != 5 {
		t.Fatalf("expected 5 missing terminal states, got %v", summary.StatusCounts)
	}
}

// TestBatchNonexistentRootFailsNonZero proves a failing batch exits non-zero
// after recording terminal states.
func TestBatchNonexistentRootFailsNonZero(t *testing.T) {
	manifest := fullProbeManifestDir(t)
	var out bytes.Buffer
	code := run([]string{"replay", "batch", "--manifest", manifest, "--replay-root", filepath.Join(t.TempDir(), "does-not-exist"), "--data-root", t.TempDir(), "--workers", "2"}, &out)
	if code == 0 {
		t.Fatalf("batch with missing root exited 0; output=%s", out.String())
	}
	if !strings.Contains(out.String(), "batch_result:") {
		t.Fatalf("batch summary missing: %s", out.String())
	}
}

// TestProbeMissingRoleRegistryFailsClosed proves a manifest copied away from
// its role registry fails closed (exit non-zero, explicit reason) instead of
// silently publishing without roles.
func TestProbeMissingRoleRegistryFailsClosed(t *testing.T) {
	manifest := fullProbeManifestDir(t)
	// Move the registry away.
	regPath := filepath.Join(filepath.Dir(manifest), "ti2026-five-replay-role-registry-v1.json")
	if err := os.Rename(regPath, regPath+".bak"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"replay", "probe", "--manifest", manifest, "--replay-root", t.TempDir(), "--data-root", t.TempDir()}, &out)
	if code == 0 {
		t.Fatalf("probe without role registry exited 0; output=%s", out.String())
	}
	if !strings.Contains(out.String(), "role_inputs_failed") {
		t.Fatalf("expected role_inputs_failed, got: %s", out.String())
	}
}

// TestLoopbackOnlyListen proves the serve command rejects non-loopback binds.
func TestLoopbackOnlyListen(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:43211", "localhost:43211", "[::1]:43211", "127.0.0.2:43211", "0.0.0.0:43211", ":43211", "192.168.1.5:43211"} {
		ok := loopbackOnly(addr)
		expect := strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "localhost") || strings.HasPrefix(addr, "[::1]") || addr == ":43211" && false
		_ = expect
		if !ok && (strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "localhost") || strings.HasPrefix(addr, "[::1]")) {
			t.Fatalf("loopback address %q rejected", addr)
		}
		if ok && (strings.HasPrefix(addr, "0.0.0.0") || strings.HasPrefix(addr, "192.168.")) {
			t.Fatalf("public address %q accepted", addr)
		}
	}
	// Empty host (":43211") is treated as loopback by convention? We reject
	// it because it binds all interfaces; assert it is NOT loopback-only here
	// by calling the helper directly.
	if loopbackOnly(":43211") {
		t.Fatal("wildcard :43211 should not be loopback-only")
	}
	if loopbackOnly("0.0.0.0:43211") {
		t.Fatal("0.0.0.0 should not be loopback-only")
	}
	if !loopbackOnly("127.0.0.1:43211") || !loopbackOnly("[::1]:43211") {
		t.Fatal("loopback addresses should be allowed")
	}
}

// TestScoreRequiresManifest proves the score subcommand fails closed without
// the contract inputs.
func TestScoreRequiresManifest(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"replay", "score"}, &out); code != 2 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "requires --manifest") {
		t.Fatalf("output=%s", out.String())
	}
}

// TestScoreEmptyCorpusSucceeds proves scoring an empty (or unverified) data
// root completes deterministically and reports the suppression gate.
func TestScoreEmptyCorpusSucceeds(t *testing.T) {
	manifest := fullProbeManifestDir(t)
	dataRoot := t.TempDir()
	var out bytes.Buffer
	if code := run([]string{"replay", "score", "--manifest", manifest, "--data-root", dataRoot}, &out); code != 0 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "score_result: 0 corpus matches") {
		t.Fatalf("output=%s", out.String())
	}
	if !strings.Contains(out.String(), "score_gate: corpus_matches=0_less_than_minimum_3") {
		t.Fatalf("expected suppression gate message, output=%s", out.String())
	}
}
