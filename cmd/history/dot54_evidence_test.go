package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestClassifyDOT54MatchesTerminatesEveryCandidate(t *testing.T) {
	rows := []dot54ExplorerMatch{
		{MatchID: 1, StartTime: 1774313458, ReplaySalt: ptrInt64(10), Version: ptrInt64(22)},
		{MatchID: 2, StartTime: 1774313459, ReplaySalt: nil, Version: ptrInt64(22)},
		{MatchID: 3, StartTime: 1774313460, ReplaySalt: ptrInt64(11), Version: ptrInt64(22)},
		{MatchID: 4, StartTime: 1786492801, ReplaySalt: ptrInt64(12), Version: ptrInt64(22)},
	}
	heads := map[int64]dot54ValveHead{
		3: {MatchID: "3", HTTPStatus: 200, CurlExit: 0, ContentLength: 99},
	}

	got := classifyDOT54Matches(rows, heads, 1774313459, 1786492800, true)
	want := []string{
		dot54StateExcludedPatch,
		dot54StateReplayMetadataMissing,
		dot54StateScopeIdentityUnverifiable,
		dot54StateAfterCutoff,
	}
	states := make([]string, len(got))
	for i := range got {
		states[i] = got[i].TerminalState
		if got[i].TerminalState == "" || got[i].TerminalState == "discovered" {
			t.Fatalf("match %d is not terminal: %#v", got[i].MatchID, got[i])
		}
	}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states=%v want=%v", states, want)
	}
}

func TestClassifyDOT54MatchesDoesNotPromoteHEADToReplayAccessible(t *testing.T) {
	rows := []dot54ExplorerMatch{{MatchID: 7, StartTime: 1774313460, ReplaySalt: ptrInt64(11), Version: ptrInt64(22)}}
	heads := map[int64]dot54ValveHead{7: {MatchID: "7", HTTPStatus: 200, CurlExit: 0, ContentLength: 123}}

	got := classifyDOT54Matches(rows, heads, 1774313459, 1786492800, false)
	if got[0].TerminalState != dot54StateReplayPresentUnverified {
		t.Fatalf("state=%q want=%q", got[0].TerminalState, dot54StateReplayPresentUnverified)
	}
	if got[0].ReplayAccessible {
		t.Fatal("HEAD-only evidence must not mark a replay accessible")
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestRunDOT54EvidenceIsDeterministicAndResumeSafe(t *testing.T) {
	source := writeDOT54TestSource(t)
	runA := filepath.Join(t.TempDir(), "run-a")
	runB := filepath.Join(t.TempDir(), "run-b")

	firstA, err := runDOT54Evidence(source, runA)
	if err != nil {
		t.Fatal(err)
	}
	firstB, err := runDOT54Evidence(source, runB)
	if err != nil {
		t.Fatal(err)
	}
	if firstA.IndexSHA256 != firstB.IndexSHA256 || firstA.ArtifactTreeSHA256 != firstB.ArtifactTreeSHA256 {
		t.Fatalf("clean roots differ: A=%#v B=%#v", firstA, firstB)
	}
	if firstA.Created == 0 || firstA.Reused != 0 {
		t.Fatalf("unexpected first-run effects: %#v", firstA)
	}
	var scope dot54ScopeCandidate
	if err := readDOT54JSON(filepath.Join(runA, "tournament-scope-candidate.json"), &scope); err != nil {
		t.Fatal(err)
	}
	if len(scope.ClassifiedConflicts) != 0 {
		t.Fatalf("synthetic input produced non-derived conflicts: %v", scope.ClassifiedConflicts)
	}

	resume, err := runDOT54Evidence(source, runA)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Created != 0 || resume.Reused != firstA.Created {
		t.Fatalf("resume effects: %#v first=%#v", resume, firstA)
	}
	if resume.IndexSHA256 != firstA.IndexSHA256 || resume.ArtifactTreeSHA256 != firstA.ArtifactTreeSHA256 {
		t.Fatalf("resume changed identity: %#v first=%#v", resume, firstA)
	}
}

func TestRunDOT54EvidenceSubcommand(t *testing.T) {
	source := writeDOT54TestSource(t)
	out := filepath.Join(t.TempDir(), "evidence")
	if err := run([]string{"dot54-evidence", "--source-root", source, "--data-dir", out}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tournament-scope-candidate.json", "discovery-manifest.json", "readiness.json", "source-provenance.json", "evidence-report.md", "evidence-index.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestRunDOT54EvidenceRejectsSourceHashMismatch(t *testing.T) {
	source := writeDOT54TestSource(t)
	if err := os.WriteFile(filepath.Join(source, "pages", "official", "index-46b931ab.js"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runDOT54Evidence(source, filepath.Join(t.TempDir(), "evidence")); err == nil || !strings.Contains(err.Error(), "source hash mismatch") {
		t.Fatalf("error=%v, want source hash mismatch", err)
	}
}

func TestRunDOT54EvidenceRejectsTeamPageHashMismatch(t *testing.T) {
	source := writeDOT54TestSource(t)
	path := filepath.Join(source, "pages", "opendota", "teams", "1001", "players.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runDOT54Evidence(source, filepath.Join(t.TempDir(), "evidence")); err == nil || !strings.Contains(err.Error(), "team-page checkpoint mismatch") {
		t.Fatalf("error=%v, want team-page checkpoint mismatch", err)
	}
}

func TestRunDOT54EvidenceRejectsSwappedOfficialPlayers(t *testing.T) {
	source := writeDOT54TestSource(t)
	path := filepath.Join(source, "official-roster-extracted.json")
	var official dot54OfficialRoster
	if err := readDOT54JSON(path, &official); err != nil {
		t.Fatal(err)
	}
	official.Teams[0].Players[0], official.Teams[1].Players[0] = official.Teams[1].Players[0], official.Teams[0].Players[0]
	b, err := json.MarshalIndent(official, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runDOT54Evidence(source, filepath.Join(t.TempDir(), "evidence")); err == nil || !strings.Contains(err.Error(), "official roster association absent") {
		t.Fatalf("error=%v, want official roster association absent", err)
	}
}

func TestReconcileDOT54DiscoveryRejectsDuplicateMatch(t *testing.T) {
	source := writeDOT54TestSource(t)
	var explorer dot54ExplorerResponse
	var provider dot54ProviderSourceDescriptor
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "explorer-frozen-identity.json"), &explorer); err != nil {
		t.Fatal(err)
	}
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "provider-source.json"), &provider); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := readDOT54PageCheckpoints(filepath.Join(source, "pages", "opendota", "team-pages.checkpoint.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	explorer.Rows = append(explorer.Rows, explorer.Rows[0])
	explorer.RowCount++
	provider.OpenDota.Explorer.RowCount++
	resolved, _, err := resolveDOT54Roster(source, mustReadDOT54Official(t, source).Teams, mustReadDOT54Registry(t, source))
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileDOT54Discovery(source, explorer, provider, checkpoints, resolved, 1786492800); err == nil || !strings.Contains(err.Error(), "duplicate Explorer match_id") {
		t.Fatalf("error=%v, want duplicate Explorer match_id", err)
	}
}

func TestReconcileDOT54DiscoveryRequiresEveryCandidatePagePair(t *testing.T) {
	source := writeDOT54TestSource(t)
	var explorer dot54ExplorerResponse
	var provider dot54ProviderSourceDescriptor
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "explorer-frozen-identity.json"), &explorer); err != nil {
		t.Fatal(err)
	}
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "provider-source.json"), &provider); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := readDOT54PageCheckpoints(filepath.Join(source, "pages", "opendota", "team-pages.checkpoint.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoints = checkpoints[:len(checkpoints)-1]
	resolved, _, err := resolveDOT54Roster(source, mustReadDOT54Official(t, source).Teams, mustReadDOT54Registry(t, source))
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileDOT54Discovery(source, explorer, provider, checkpoints, resolved, 1786492800); err == nil || !strings.Contains(err.Error(), "checkpoint page-pair count differs") {
		t.Fatalf("error=%v, want checkpoint page-pair count differs", err)
	}
}

func TestReconcileDOT54DiscoveryRejectsCandidateOutsideResolvedAllowSet(t *testing.T) {
	source := writeDOT54TestSource(t)
	var explorer dot54ExplorerResponse
	var provider dot54ProviderSourceDescriptor
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "explorer-frozen-identity.json"), &explorer); err != nil {
		t.Fatal(err)
	}
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "provider-source.json"), &provider); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := readDOT54PageCheckpoints(filepath.Join(source, "pages", "opendota", "team-pages.checkpoint.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	resolved := make([]dot54ResolvedTeam, 0, len(provider.OpenDota.Explorer.TeamIDs)-1)
	for _, id := range provider.OpenDota.Explorer.TeamIDs[1:] {
		resolved = append(resolved, dot54ResolvedTeam{TeamID: fmt.Sprintf("valve-team:%d", id)})
	}
	if err := reconcileDOT54Discovery(source, explorer, provider, checkpoints, resolved, 1786492800); err == nil || !strings.Contains(err.Error(), "conflict-resolved allow-set") {
		t.Fatalf("error=%v, want conflict-resolved allow-set rejection", err)
	}
}

func TestUnresolvedScopeDoesNotEmitHistoricalNoGoCandidate(t *testing.T) {
	source := writeDOT54TestSource(t)
	out := filepath.Join(t.TempDir(), "evidence")
	if _, err := runDOT54Evidence(source, out); err != nil {
		t.Fatal(err)
	}
	var readiness dot54ReadinessEvidence
	if err := readDOT54JSON(filepath.Join(out, "readiness.json"), &readiness); err != nil {
		t.Fatal(err)
	}
	if readiness.Outcome == "historical_no_go_candidate" {
		t.Fatalf("unresolved prerequisite was mislabeled as terminal outcome: %#v", readiness)
	}
	if readiness.Outcome != "unresolved_scope_blocker" {
		t.Fatalf("outcome=%q want unresolved_scope_blocker", readiness.Outcome)
	}
}

func TestParseDOT54TournamentRosterUsesCutoffRolesAndRejectsFormer(t *testing.T) {
	wiki := `|{{Opponent|LGD Gaming
|players={{Persons
|{{Person|role=1|Yuma}}
|{{Person|role=2|Topson|trophies=2}}
|{{Person|role=3|Wisper}}
|{{Person|role=4|Thiolicor}}
|{{Person|role=5|KJ}}
|{{Person|role=2|TaiLung|status=former|results=false}}
}}
}}`
	teams, err := parseDOT54TournamentRoster(wiki)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 1 || len(teams[0].Players) != 5 {
		t.Fatalf("teams=%#v", teams)
	}
	if teams[0].Players[1].Handle != "Topson" || teams[0].Players[1].Position != 2 {
		t.Fatalf("cutoff role not preserved: %#v", teams[0].Players)
	}
	for _, player := range teams[0].Players {
		if player.Handle == "TaiLung" {
			t.Fatal("former player re-entered cutoff roster")
		}
	}
}

func TestDOT54ReportIsDerivedFromSealedReadiness(t *testing.T) {
	report := dot54Report(
		dot54ScopeCandidate{Outcome: "materialized", Teams: []dot54ResolvedTeam{{TeamID: "1", Players: []dot54ResolvedPlayer{{PersonID: "10"}}}}},
		dot54DiscoveryEvidence{Matches: make([]dot54TerminalMatch, 2), TerminalCount: map[string]uint32{dot54StateReplayIdentityQuarantined: 1, dot54StateGateTargetNotSelected: 1}},
		dot54ReadinessEvidence{Outcome: "historical_no_go_candidate", ReplayAccessibleTotal: 1, ParserExecutionTotal: 2, RepeatablyProcessedTotal: 0, ParserRequired: "parser@version", Reason: "identity correlation failed"},
	)
	for _, want := range []string{"`materialized`", "1 selected teams", "2 independent parser executions", "identity correlation failed"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "do not prove roster effective intervals") || strings.Contains(report, "blocked upstream of download") {
		t.Fatalf("report retained rejected evidence statement:\n%s", report)
	}
}

func writeDOT54TestSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustMkdir := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mustJSON := func(path string, v any) {
		t.Helper()
		mustMkdir(filepath.Dir(path))
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	official := dot54OfficialRoster{SchemaVersion: "dot54.official-roster-extract.v1", SourceURL: dot54OfficialAssetURL, RetrievedAt: "2026-08-14T01:48:35Z"}
	registry := make([]dot54TeamRegistryEntry, 0, 16)
	var checkpointLines [][]byte
	for i := 1; i <= 16; i++ {
		teamID := int64(1000 + i)
		team := dot54OfficialTeam{PageLocalID: i, Name: fmt.Sprintf("Team %02d", i)}
		players := make([]dot54TeamPlayer, 0, 5)
		for j := 1; j <= 5; j++ {
			name := fmt.Sprintf("p%02d-%d", i, j)
			team.Players = append(team.Players, name)
			players = append(players, dot54TeamPlayer{AccountID: int64(i*100 + j), Name: name, IsCurrentTeamMember: true})
		}
		official.Teams = append(official.Teams, team)
		registry = append(registry, dot54TeamRegistryEntry{TeamID: teamID, Name: team.Name})
		teamDir := filepath.Join(root, "pages", "opendota", "teams", fmt.Sprint(teamID))
		playerPath := filepath.Join(teamDir, "players.json")
		mustJSON(playerPath, players)
		matches := []dot54TeamMatch{}
		if i == 1 {
			matches = []dot54TeamMatch{{MatchID: 1, StartTime: 1774313458}, {MatchID: 2, StartTime: 1774313459}, {MatchID: 3, StartTime: 1774313460}}
		}
		matchPath := filepath.Join(teamDir, "matches.json")
		mustJSON(matchPath, matches)
		for kind, path := range map[string]string{"players": playerPath, "matches": matchPath} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			line, err := json.Marshal(dot54PageCheckpoint{TeamID: fmt.Sprint(teamID), Kind: kind, URL: fmt.Sprintf("https://api.opendota.com/api/teams/%d/%s", teamID, kind), RetrievedAt: "2026-08-14T01:50:00Z", ProvenanceClass: "Valve-derived test metadata", HTTPStatus: 200, SHA256: mustDOT54TestFileSHA(t, path), Bytes: info.Size()})
			if err != nil {
				t.Fatal(err)
			}
			checkpointLines = append(checkpointLines, line)
		}
	}
	assetObjects := []string{}
	for _, team := range official.Teams {
		players := make([]string, len(team.Players))
		for i, player := range team.Players {
			players[i] = strconv.QuoteToASCII(player)
		}
		assetObjects = append(assetObjects, fmt.Sprintf(`{id:%d,name:%s,abbr:%s,logo:"fixture",players:[%s]}`, team.PageLocalID, strconv.QuoteToASCII(team.Name), strconv.QuoteToASCII(team.Abbr), strings.Join(players, ",")))
	}
	assetBytes := []byte("[" + strings.Join(assetObjects, ",") + "]")
	official.SourceSHA256 = shaBytes(assetBytes)
	mustJSON(filepath.Join(root, "official-roster-extracted.json"), official)
	mustJSON(filepath.Join(root, "pages", "opendota", "teams.json"), registry)
	mustJSON(filepath.Join(root, "pages", "opendota", "constants-patch.json"), []dot54Patch{{Name: "7.41", Date: "2026-03-24T00:50:59Z", ID: 60}})
	explorer := dot54ExplorerResponse{RowCount: 3, Rows: []dot54ExplorerMatch{
		{MatchID: 1, StartTime: 1774313458, ReplaySalt: ptrInt64(10), Version: ptrInt64(22), RadiantTeamID: 1001},
		{MatchID: 2, StartTime: 1774313459, ReplaySalt: nil, Version: ptrInt64(22), RadiantTeamID: 1001},
		{MatchID: 3, StartTime: 1774313460, ReplaySalt: ptrInt64(11), Version: ptrInt64(22), RadiantTeamID: 1001},
	}}
	mustJSON(filepath.Join(root, "pages", "opendota", "explorer-frozen-identity.json"), explorer)
	mustJSON(filepath.Join(root, "pages", "valve-head", "manifest.json"), []dot54ValveHead{{MatchID: "3", URL: "http://replay1.valve.net/570/3_11.dem.bz2", HTTPStatus: 200, ContentLength: 99, HeaderSHA256: sha256Text("head")}})
	mustMkdir(filepath.Join(root, "pages", "official"))
	for path, content := range map[string][]byte{
		"pages/official/ti2026.html":                 []byte("shell"),
		"pages/official/index-46b931ab.js":           assetBytes,
		"pages/opendota/team-pages.checkpoint.jsonl": append(bytes.Join(checkpointLines, []byte{'\n'}), '\n'),
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustJSON(filepath.Join(root, "pages", "opendota", "provider-source.json"), map[string]any{
		"official": map[string]any{
			"shell": map[string]any{"response_sha256": sha256Text("shell")},
			"asset": map[string]any{"response_sha256": shaBytes(assetBytes)},
		},
		"opendota": map[string]any{
			"teams":      map[string]any{"response_sha256": mustDOT54TestFileSHA(t, filepath.Join(root, "pages", "opendota", "teams.json"))},
			"team_pages": map[string]any{"checkpoint_sha256": mustDOT54TestFileSHA(t, filepath.Join(root, "pages", "opendota", "team-pages.checkpoint.jsonl")), "count": len(checkpointLines)},
			"explorer": func() map[string]any {
				teamIDs := make([]int64, len(registry))
				for i, team := range registry {
					teamIDs[i] = team.TeamID
				}
				query := dot54ExplorerQuery(teamIDs)
				return map[string]any{"url": dot54OpenDotaExplorerURL, "retrieved_at": "2026-08-14T01:53:43Z", "response_sha256": mustDOT54TestFileSHA(t, filepath.Join(root, "pages", "opendota", "explorer-frozen-identity.json")), "query": query, "query_sha256": sha256Text(query), "team_ids": teamIDs, "row_count": 3, "pagination": "one bounded set page ordered by match_id"}
			}(),
			"patch": map[string]any{"response_sha256": mustDOT54TestFileSHA(t, filepath.Join(root, "pages", "opendota", "constants-patch.json"))},
		},
		"valve_cdn": map[string]any{"manifest_sha256": mustDOT54TestFileSHA(t, filepath.Join(root, "pages", "valve-head", "manifest.json"))},
	})
	return root
}

func mustReadDOT54Official(t *testing.T, source string) dot54OfficialRoster {
	t.Helper()
	var value dot54OfficialRoster
	if err := readDOT54JSON(filepath.Join(source, "official-roster-extracted.json"), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustReadDOT54Registry(t *testing.T, source string) []dot54TeamRegistryEntry {
	t.Helper()
	var value []dot54TeamRegistryEntry
	if err := readDOT54JSON(filepath.Join(source, "pages", "opendota", "teams.json"), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustDOT54TestFileSHA(t *testing.T, path string) string {
	t.Helper()
	got, err := fileSHA(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
