package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const (
	dot54OfficialAssetURL    = "https://www.dota2.com.cn/asset/international2026Sub/index/static/js/index-46b931ab.js"
	dot54OpenDotaExplorerURL = "https://api.opendota.com/api/explorer"
	dot54CutoffText          = "2026-08-12T00:00:00Z"
	dot54WindowStartText     = "2026-02-13T00:00:00Z"

	dot54StateExcludedPatch             = "excluded_patch_mismatch"
	dot54StateReplayMetadataMissing     = "replay_metadata_missing"
	dot54StateReplayProbeMissing        = "replay_probe_missing"
	dot54StateReplayUnavailable         = "replay_unavailable"
	dot54StateReplayPresentUnverified   = "replay_present_unverified"
	dot54StateScopeIdentityUnverifiable = "scope_identity_unverifiable"
	dot54StateAfterCutoff               = "after_cutoff"
)

type dot54ExplorerMatch struct {
	MatchID       int64  `json:"match_id"`
	StartTime     int64  `json:"start_time"`
	Duration      int64  `json:"duration"`
	Cluster       int64  `json:"cluster"`
	LeagueID      int64  `json:"leagueid"`
	Version       *int64 `json:"version"`
	ReplaySalt    *int64 `json:"replay_salt"`
	RadiantTeamID int64  `json:"radiant_team_id"`
	DireTeamID    int64  `json:"dire_team_id"`
	Players       []struct {
		AccountID  int64 `json:"account_id"`
		PlayerSlot int64 `json:"player_slot"`
	} `json:"players"`
}

type dot54ExplorerResponse struct {
	RowCount int                  `json:"rowCount"`
	Rows     []dot54ExplorerMatch `json:"rows"`
}

type dot54ValveHead struct {
	MatchID       string `json:"match_id"`
	URL           string `json:"url"`
	HTTPStatus    int    `json:"http_status"`
	CurlExit      int    `json:"curl_exit"`
	ContentLength int64  `json:"content_length"`
	HeaderSHA256  string `json:"header_sha256"`
}

type dot54TerminalMatch struct {
	MatchID          int64  `json:"match_id"`
	StartTime        int64  `json:"start_time"`
	TerminalState    string `json:"terminal_state"`
	ReplayAccessible bool   `json:"replay_accessible"`
	ReplayURL        string `json:"replay_url,omitempty"`
	ProbeSHA256      string `json:"probe_sha256,omitempty"`
}

type dot54OfficialRoster struct {
	SchemaVersion string              `json:"schema_version"`
	SourceURL     string              `json:"source_url"`
	RetrievedAt   string              `json:"retrieved_at"`
	SourceSHA256  string              `json:"source_sha256"`
	Teams         []dot54OfficialTeam `json:"teams"`
}

type dot54OfficialTeam struct {
	PageLocalID int      `json:"page_local_id"`
	Name        string   `json:"name"`
	Abbr        string   `json:"abbr,omitempty"`
	Players     []string `json:"players"`
}

type dot54TeamRegistryEntry struct {
	TeamID int64  `json:"team_id"`
	Name   string `json:"name"`
	Tag    string `json:"tag"`
}

type dot54TeamPlayer struct {
	AccountID           int64  `json:"account_id"`
	Name                string `json:"name"`
	IsCurrentTeamMember bool   `json:"is_current_team_member"`
}

type dot54Patch struct {
	Name string `json:"name"`
	Date string `json:"date"`
	ID   int64  `json:"id"`
}

type dot54ProviderHash struct {
	ResponseSHA256   string  `json:"response_sha256"`
	CheckpointSHA256 string  `json:"checkpoint_sha256"`
	ManifestSHA256   string  `json:"manifest_sha256"`
	URL              string  `json:"url"`
	RetrievedAt      string  `json:"retrieved_at"`
	QuerySHA256      string  `json:"query_sha256"`
	Query            string  `json:"query"`
	TeamIDs          []int64 `json:"team_ids"`
	Pagination       string  `json:"pagination"`
	RowCount         int     `json:"row_count"`
	Count            int     `json:"count"`
}

type dot54PageCheckpoint struct {
	TeamID     string `json:"team_id"`
	Kind       string `json:"kind"`
	HTTPStatus int    `json:"http_status"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
}

type dot54TeamMatch struct {
	MatchID   int64 `json:"match_id"`
	StartTime int64 `json:"start_time"`
}

type dot54ProviderSourceDescriptor struct {
	Official struct {
		Shell dot54ProviderHash `json:"shell"`
		Asset dot54ProviderHash `json:"asset"`
	} `json:"official"`
	OpenDota struct {
		Teams     dot54ProviderHash `json:"teams"`
		TeamPages dot54ProviderHash `json:"team_pages"`
		Explorer  dot54ProviderHash `json:"explorer"`
		Patch     dot54ProviderHash `json:"patch"`
	} `json:"opendota"`
	ValveCDN dot54ProviderHash `json:"valve_cdn"`
}

type dot54ResolvedPlayer struct {
	PersonID string   `json:"person_id"`
	Handle   string   `json:"handle"`
	Aliases  []string `json:"aliases"`
}

type dot54ResolvedTeam struct {
	TeamID  string                `json:"team_id"`
	Name    string                `json:"name"`
	Aliases []string              `json:"aliases"`
	Players []dot54ResolvedPlayer `json:"players"`
}

type dot54ScopeCandidate struct {
	SchemaVersion       string              `json:"schema_version"`
	ContentSHA256       string              `json:"content_sha256"`
	Outcome             string              `json:"outcome"`
	Edition             string              `json:"edition"`
	Cutoff              string              `json:"cutoff"`
	OfficialSource      string              `json:"official_source"`
	OfficialPageSHA     string              `json:"official_page_sha256"`
	Teams               []dot54ResolvedTeam `json:"teams"`
	ClassifiedConflicts []string            `json:"classified_conflicts"`
	UnresolvedFields    []string            `json:"unresolved_fields"`
}

type dot54DiscoveryEvidence struct {
	SchemaVersion string               `json:"schema_version"`
	ContentSHA256 string               `json:"content_sha256"`
	WindowStart   string               `json:"window_start"`
	Cutoff        string               `json:"cutoff"`
	PatchID       int64                `json:"patch_id"`
	PatchName     string               `json:"patch_name"`
	PatchStart    string               `json:"patch_start"`
	QueryBounds   map[string]any       `json:"query_bounds"`
	TerminalCount map[string]uint32    `json:"terminal_counts"`
	Matches       []dot54TerminalMatch `json:"matches"`
}

type dot54ReadinessEvidence struct {
	SchemaVersion            string            `json:"schema_version"`
	ContentSHA256            string            `json:"content_sha256"`
	Outcome                  string            `json:"outcome"`
	DiscoveredTotal          uint32            `json:"discovered_total"`
	ReplayPresentProbeTotal  uint32            `json:"replay_present_probe_total"`
	ReplayAccessibleTotal    uint32            `json:"replay_accessible_total"`
	RepeatablyProcessedTotal uint32            `json:"repeatably_processed_total"`
	ParserRequired           string            `json:"parser_required"`
	ParserExecutionTotal     uint32            `json:"parser_execution_total"`
	TerminalCounts           map[string]uint32 `json:"terminal_counts"`
	DisabledTeams            []string          `json:"disabled_teams"`
	DisabledFamilies         []string          `json:"disabled_families"`
	Reason                   string            `json:"reason"`
}

type dot54SourceFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type dot54SourceProvenance struct {
	SchemaVersion string            `json:"schema_version"`
	ContentSHA256 string            `json:"content_sha256"`
	Policy        map[string]string `json:"policy"`
	Files         []dot54SourceFile `json:"files"`
}

type dot54IndexEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type dot54EvidenceIndex struct {
	SchemaVersion string            `json:"schema_version"`
	ContentSHA256 string            `json:"content_sha256"`
	Outcome       string            `json:"outcome"`
	Artifacts     []dot54IndexEntry `json:"artifacts"`
}

type dot54RunSummary struct {
	Created            int
	Reused             int
	IndexSHA256        string
	ArtifactTreeSHA256 string
	StorageBytes       int64
	Files              int
	TerminalCounts     map[string]uint32
}

func runDOT54EvidenceCommand(args []string) error {
	fs := flag.NewFlagSet("dot54-evidence", flag.ContinueOnError)
	sourceRoot := fs.String("source-root", "", "checkpointed provider-page root")
	dataDir := fs.String("data-dir", "", "clean or resumable external evidence output root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sourceRoot == "" || *dataDir == "" {
		return errors.New("dot54-evidence requires --source-root and --data-dir")
	}
	s, err := runDOT54Evidence(*sourceRoot, *dataDir)
	if err != nil {
		return err
	}
	fmt.Printf("outcome=historical_no_go_candidate discovered=%d created=%d reused=%d index_sha256=%s artifact_tree_sha256=%s storage_bytes=%d files=%d terminal_counts=%s\n",
		sumDOT54Counts(s.TerminalCounts), s.Created, s.Reused, s.IndexSHA256, s.ArtifactTreeSHA256, s.StorageBytes, s.Files, formatDOT54Counts(s.TerminalCounts))
	return nil
}

func classifyDOT54Matches(rows []dot54ExplorerMatch, heads map[int64]dot54ValveHead, patchStart, cutoff int64, scopeBlocked bool) []dot54TerminalMatch {
	out := make([]dot54TerminalMatch, 0, len(rows))
	for _, row := range rows {
		entry := dot54TerminalMatch{MatchID: row.MatchID, StartTime: row.StartTime}
		switch {
		case row.StartTime > cutoff:
			entry.TerminalState = dot54StateAfterCutoff
		case row.StartTime < patchStart:
			entry.TerminalState = dot54StateExcludedPatch
		case row.ReplaySalt == nil || row.Version == nil:
			entry.TerminalState = dot54StateReplayMetadataMissing
		default:
			head, ok := heads[row.MatchID]
			if !ok {
				entry.TerminalState = dot54StateReplayProbeMissing
				break
			}
			entry.ReplayURL, entry.ProbeSHA256 = head.URL, head.HeaderSHA256
			if head.CurlExit != 0 || head.HTTPStatus != 200 {
				entry.TerminalState = dot54StateReplayUnavailable
			} else if scopeBlocked {
				entry.TerminalState = dot54StateScopeIdentityUnverifiable
			} else {
				entry.TerminalState = dot54StateReplayPresentUnverified
			}
		}
		out = append(out, entry)
	}
	return out
}

func runDOT54Evidence(sourceRoot, outputRoot string) (*dot54RunSummary, error) {
	if sourceRoot == "" || outputRoot == "" {
		return nil, errors.New("dot54 evidence requires source and output roots")
	}
	var official dot54OfficialRoster
	var registry []dot54TeamRegistryEntry
	var explorer dot54ExplorerResponse
	var patches []dot54Patch
	var heads []dot54ValveHead
	var provider dot54ProviderSourceDescriptor
	if err := readDOT54JSON(filepath.Join(sourceRoot, "official-roster-extracted.json"), &official); err != nil {
		return nil, err
	}
	if err := readDOT54JSON(filepath.Join(sourceRoot, "pages/opendota/teams.json"), &registry); err != nil {
		return nil, err
	}
	if err := readDOT54JSON(filepath.Join(sourceRoot, "pages/opendota/explorer-frozen-identity.json"), &explorer); err != nil {
		return nil, err
	}
	if err := readDOT54JSON(filepath.Join(sourceRoot, "pages/opendota/constants-patch.json"), &patches); err != nil {
		return nil, err
	}
	if err := readDOT54JSON(filepath.Join(sourceRoot, "pages/valve-head/manifest.json"), &heads); err != nil {
		return nil, err
	}
	if err := readDOT54JSON(filepath.Join(sourceRoot, "pages/opendota/provider-source.json"), &provider); err != nil {
		return nil, err
	}
	checkpoints, err := verifyDOT54ProviderHashes(sourceRoot, official, provider)
	if err != nil {
		return nil, err
	}

	cutoff, _ := time.Parse(time.RFC3339, dot54CutoffText)
	retrieved, err := time.Parse(time.RFC3339Nano, official.RetrievedAt)
	if err != nil {
		return nil, fmt.Errorf("official retrieved_at: %w", err)
	}
	if official.SchemaVersion != "dot54.official-roster-extract.v1" || official.SourceURL != dot54OfficialAssetURL || len(official.Teams) != 16 {
		return nil, errors.New("invalid official 16-team roster extract")
	}
	for _, team := range official.Teams {
		if len(team.Players) != 5 {
			return nil, fmt.Errorf("official team %q does not have five players", team.Name)
		}
	}

	resolved, conflicts, err := resolveDOT54Roster(sourceRoot, official.Teams, registry)
	if err != nil {
		return nil, err
	}
	scopeBlocked := retrieved.After(cutoff)
	unresolved := []string{"roster_effective_intervals_at_cutoff_absent_from_sources"}
	if scopeBlocked {
		unresolved = append(unresolved, "official_roster_source_retrieved_after_cutoff")
	}
	sort.Strings(unresolved)
	scope := dot54ScopeCandidate{SchemaVersion: "dot54.tournament-scope-candidate.v1", Outcome: "unresolved", Edition: "The International 2026", Cutoff: dot54CutoffText, OfficialSource: official.SourceURL, OfficialPageSHA: official.SourceSHA256, Teams: resolved, ClassifiedConflicts: conflicts, UnresolvedFields: unresolved}
	if err := sealDOT54(&scope.ContentSHA256, scope); err != nil {
		return nil, err
	}

	var patch dot54Patch
	for _, p := range patches {
		if p.ID == 60 && p.Name == "7.41" {
			patch = p
			break
		}
	}
	if patch.ID == 0 {
		return nil, errors.New("OpenDota patch 60 / Dota 7.41 not found")
	}
	patchStart, err := time.Parse(time.RFC3339Nano, patch.Date)
	if err != nil {
		return nil, fmt.Errorf("patch start: %w", err)
	}
	if err := reconcileDOT54Discovery(sourceRoot, explorer, provider, checkpoints, cutoff.Unix()); err != nil {
		return nil, err
	}
	headByID := map[int64]dot54ValveHead{}
	for _, h := range heads {
		id, err := strconv.ParseInt(h.MatchID, 10, 64)
		if err != nil {
			return nil, err
		}
		if _, exists := headByID[id]; exists {
			return nil, fmt.Errorf("duplicate Valve HEAD match %d", id)
		}
		headByID[id] = h
	}
	sort.Slice(explorer.Rows, func(i, j int) bool { return explorer.Rows[i].MatchID < explorer.Rows[j].MatchID })
	terminal := classifyDOT54Matches(explorer.Rows, headByID, patchStart.Unix(), cutoff.Unix(), true)
	counts := map[string]uint32{}
	for _, m := range terminal {
		counts[m.TerminalState]++
	}
	explorerMeta := provider.OpenDota.Explorer
	discovery := dot54DiscoveryEvidence{SchemaVersion: "dot54.discovery-candidate.v1", WindowStart: dot54WindowStartText, Cutoff: dot54CutoffText, PatchID: patch.ID, PatchName: patch.Name, PatchStart: patch.Date, QueryBounds: map[string]any{"provider": "OpenDota", "endpoint": explorerMeta.URL, "retrieved_at": explorerMeta.RetrievedAt, "response_sha256": explorerMeta.ResponseSHA256, "query": explorerMeta.Query, "query_sha256": explorerMeta.QuerySHA256, "candidate_team_ids": explorerMeta.TeamIDs, "inclusive_start_epoch": int64(1770940800), "inclusive_cutoff_epoch": cutoff.Unix(), "team_identity_candidates": len(explorerMeta.TeamIDs), "row_count": explorerMeta.RowCount, "pagination": explorerMeta.Pagination, "reconciliation": "exact match-id equality with hash-verified complete team-history pages"}, TerminalCount: counts, Matches: terminal}
	if err := sealDOT54(&discovery.ContentSHA256, discovery); err != nil {
		return nil, err
	}

	disabled := make([]string, 0, len(resolved))
	for _, team := range resolved {
		disabled = append(disabled, team.TeamID)
	}
	readiness := dot54ReadinessEvidence{SchemaVersion: "dot54.readiness-candidate.v1", Outcome: "historical_no_go_candidate", DiscoveredTotal: uint32(len(terminal)), ReplayPresentProbeTotal: counts[dot54StateScopeIdentityUnverifiable], ParserRequired: "github.com/dotabuff/manta v1.5.0", ParserExecutionTotal: 0, TerminalCounts: counts, DisabledTeams: disabled, DisabledFamilies: []string{"hero", "item", "lane", "patch", "player", "player_hero", "role", "team"}, Reason: "cutoff roster effective intervals are not verifiable from the retrieved public sources; HEAD-only Valve objects are not accepted replay evidence"}
	if err := sealDOT54(&readiness.ContentSHA256, readiness); err != nil {
		return nil, err
	}

	provenance, err := buildDOT54Provenance(sourceRoot)
	if err != nil {
		return nil, err
	}
	if err := sealDOT54(&provenance.ContentSHA256, provenance); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(outputRoot, 0o700); err != nil {
		return nil, err
	}
	summary := &dot54RunSummary{TerminalCounts: counts}
	artifacts := []struct {
		name  string
		value any
	}{
		{"tournament-scope-candidate.json", scope}, {"discovery-manifest.json", discovery}, {"readiness.json", readiness}, {"source-provenance.json", provenance},
	}
	indexEntries := make([]dot54IndexEntry, 0, len(artifacts)+1)
	for _, artifact := range artifacts {
		b, err := contracts.MarshalCanonical(artifact.value)
		if err != nil {
			return nil, err
		}
		b = append(b, '\n')
		created, err := writeDOT54Immutable(filepath.Join(outputRoot, artifact.name), b)
		if err != nil {
			return nil, err
		}
		if created {
			summary.Created++
		} else {
			summary.Reused++
		}
		indexEntries = append(indexEntries, dot54IndexEntry{Path: artifact.name, SHA256: shaBytes(b)})
	}
	report := dot54Report(scope, discovery, readiness)
	created, err := writeDOT54Immutable(filepath.Join(outputRoot, "evidence-report.md"), []byte(report))
	if err != nil {
		return nil, err
	}
	if created {
		summary.Created++
	} else {
		summary.Reused++
	}
	indexEntries = append(indexEntries, dot54IndexEntry{Path: "evidence-report.md", SHA256: shaBytes([]byte(report))})
	index := dot54EvidenceIndex{SchemaVersion: "dot54.evidence-index.v1", Outcome: readiness.Outcome, Artifacts: indexEntries}
	if err := sealDOT54(&index.ContentSHA256, index); err != nil {
		return nil, err
	}
	indexBytes, err := contracts.MarshalCanonical(index)
	if err != nil {
		return nil, err
	}
	indexBytes = append(indexBytes, '\n')
	created, err = writeDOT54Immutable(filepath.Join(outputRoot, "evidence-index.json"), indexBytes)
	if err != nil {
		return nil, err
	}
	if created {
		summary.Created++
	} else {
		summary.Reused++
	}
	summary.IndexSHA256 = shaBytes(indexBytes)
	summary.ArtifactTreeSHA256, summary.StorageBytes, summary.Files, err = artifactTreeSHA(outputRoot)
	return summary, err
}

func verifyDOT54ProviderHashes(root string, official dot54OfficialRoster, provider dot54ProviderSourceDescriptor) ([]dot54PageCheckpoint, error) {
	expected := []struct {
		path string
		sha  string
	}{
		{"pages/official/ti2026.html", provider.Official.Shell.ResponseSHA256},
		{"pages/official/index-46b931ab.js", provider.Official.Asset.ResponseSHA256},
		{"pages/opendota/teams.json", provider.OpenDota.Teams.ResponseSHA256},
		{"pages/opendota/team-pages.checkpoint.jsonl", provider.OpenDota.TeamPages.CheckpointSHA256},
		{"pages/opendota/explorer-frozen-identity.json", provider.OpenDota.Explorer.ResponseSHA256},
		{"pages/opendota/constants-patch.json", provider.OpenDota.Patch.ResponseSHA256},
		{"pages/valve-head/manifest.json", provider.ValveCDN.ManifestSHA256},
	}
	if official.SourceSHA256 == "" || official.SourceSHA256 != provider.Official.Asset.ResponseSHA256 {
		return nil, errors.New("official roster extract is not bound to the provider asset hash")
	}
	for _, item := range expected {
		if item.sha == "" {
			return nil, fmt.Errorf("missing checkpoint hash for %s", item.path)
		}
		got, err := fileSHA(filepath.Join(root, filepath.FromSlash(item.path)))
		if err != nil {
			return nil, err
		}
		if got != item.sha {
			return nil, fmt.Errorf("source hash mismatch for %s: got %s want %s", item.path, got, item.sha)
		}
	}
	asset, err := os.ReadFile(filepath.Join(root, "pages/official/index-46b931ab.js"))
	if err != nil {
		return nil, err
	}
	canonicalAsset := canonicalDOT54JSEscapes(string(asset))
	seenPageIDs := map[int]bool{}
	for _, team := range official.Teams {
		if team.PageLocalID <= 0 || seenPageIDs[team.PageLocalID] {
			return nil, fmt.Errorf("invalid or duplicate official page-local team id: %d", team.PageLocalID)
		}
		seenPageIDs[team.PageLocalID] = true
		players := make([]string, 0, len(team.Players))
		for _, player := range team.Players {
			players = append(players, regexp.QuoteMeta(strconv.QuoteToASCII(player)))
		}
		association := fmt.Sprintf(`id:%d,name:%s,.{0,600}?players:\[%s\]`, team.PageLocalID, regexp.QuoteMeta(strconv.QuoteToASCII(team.Name)), strings.Join(players, ","))
		matched, err := regexp.MatchString(association, canonicalAsset)
		if err != nil {
			return nil, err
		}
		if !matched {
			return nil, fmt.Errorf("official roster association absent from hashed asset: team=%q", team.Name)
		}
	}
	checkpoints, err := readDOT54PageCheckpoints(filepath.Join(root, "pages/opendota/team-pages.checkpoint.jsonl"))
	if err != nil {
		return nil, err
	}
	if provider.OpenDota.TeamPages.Count != len(checkpoints) {
		return nil, fmt.Errorf("team-page checkpoint count=%d want=%d", len(checkpoints), provider.OpenDota.TeamPages.Count)
	}
	seen := map[string]bool{}
	for _, checkpoint := range checkpoints {
		if checkpoint.HTTPStatus != 200 || (checkpoint.Kind != "players" && checkpoint.Kind != "matches") {
			return nil, fmt.Errorf("invalid team-page checkpoint: team=%s kind=%s status=%d", checkpoint.TeamID, checkpoint.Kind, checkpoint.HTTPStatus)
		}
		rel := filepath.ToSlash(filepath.Join("pages/opendota/teams", checkpoint.TeamID, checkpoint.Kind+".json"))
		if seen[rel] {
			return nil, fmt.Errorf("duplicate team-page checkpoint: %s", rel)
		}
		seen[rel] = true
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		got, err := fileSHA(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if got != checkpoint.SHA256 || info.Size() != checkpoint.Bytes {
			return nil, fmt.Errorf("team-page checkpoint mismatch: %s", rel)
		}
	}
	pageFiles, err := filepath.Glob(filepath.Join(root, "pages/opendota/teams/*/*.json"))
	if err != nil {
		return nil, err
	}
	if len(pageFiles) != len(seen) {
		return nil, fmt.Errorf("team-page files=%d checkpointed=%d", len(pageFiles), len(seen))
	}
	return checkpoints, nil
}

var dot54JSEscapePattern = regexp.MustCompile(`\\u[0-9A-Fa-f]{4}`)

func canonicalDOT54JSEscapes(value string) string {
	return dot54JSEscapePattern.ReplaceAllStringFunc(value, strings.ToLower)
}

func readDOT54PageCheckpoints(path string) ([]dot54PageCheckpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []dot54PageCheckpoint
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var checkpoint dot54PageCheckpoint
		if err := json.Unmarshal(scanner.Bytes(), &checkpoint); err != nil {
			return nil, fmt.Errorf("decode team-page checkpoint: %w", err)
		}
		out = append(out, checkpoint)
	}
	return out, scanner.Err()
}

func reconcileDOT54Discovery(root string, explorer dot54ExplorerResponse, provider dot54ProviderSourceDescriptor, checkpoints []dot54PageCheckpoint, cutoff int64) error {
	meta := provider.OpenDota.Explorer
	if meta.URL != dot54OpenDotaExplorerURL || meta.RetrievedAt == "" || meta.QuerySHA256 == "" || meta.Query == "" || len(meta.TeamIDs) == 0 || meta.Pagination == "" {
		return errors.New("incomplete Explorer source descriptor")
	}
	if _, err := time.Parse(time.RFC3339Nano, meta.RetrievedAt); err != nil {
		return fmt.Errorf("Explorer retrieved_at: %w", err)
	}
	if explorer.RowCount != len(explorer.Rows) || meta.RowCount != len(explorer.Rows) {
		return fmt.Errorf("Explorer row count mismatch: response=%d descriptor=%d rows=%d", explorer.RowCount, meta.RowCount, len(explorer.Rows))
	}
	if meta.Query != dot54ExplorerQuery(meta.TeamIDs) || sha256Text(meta.Query) != meta.QuerySHA256 {
		return errors.New("Explorer query text, bounds, candidate team IDs, or hash mismatch")
	}
	expectedPages := map[string]bool{}
	for _, teamID := range meta.TeamIDs {
		if teamID <= 0 {
			return fmt.Errorf("invalid Explorer candidate team ID: %d", teamID)
		}
		for _, kind := range []string{"matches", "players"} {
			expectedPages[fmt.Sprintf("%d/%s", teamID, kind)] = true
		}
	}
	if len(expectedPages) != len(meta.TeamIDs)*2 || len(checkpoints) != len(expectedPages) {
		return errors.New("Explorer candidate team IDs are duplicated or checkpoint page-pair count differs")
	}
	for _, checkpoint := range checkpoints {
		key := checkpoint.TeamID + "/" + checkpoint.Kind
		if !expectedPages[key] {
			return fmt.Errorf("unexpected or duplicate team-page checkpoint: %s", key)
		}
		delete(expectedPages, key)
	}
	if len(expectedPages) != 0 {
		return fmt.Errorf("missing required team-page checkpoint pairs: %v", sortedDOT54MapKeys(expectedPages))
	}
	explorerIDs := make(map[int64]bool, len(explorer.Rows))
	for _, row := range explorer.Rows {
		if row.MatchID == 0 || row.StartTime < 1770940800 || row.StartTime > cutoff {
			return fmt.Errorf("Explorer row outside frozen bounds: match=%d start=%d", row.MatchID, row.StartTime)
		}
		if explorerIDs[row.MatchID] {
			return fmt.Errorf("duplicate Explorer match_id: %d", row.MatchID)
		}
		explorerIDs[row.MatchID] = true
	}
	union := map[int64]bool{}
	for _, checkpoint := range checkpoints {
		if checkpoint.Kind != "matches" {
			continue
		}
		var matches []dot54TeamMatch
		path := filepath.Join(root, "pages/opendota/teams", checkpoint.TeamID, "matches.json")
		if err := readDOT54JSON(path, &matches); err != nil {
			return err
		}
		for _, match := range matches {
			if match.StartTime >= 1770940800 && match.StartTime <= cutoff {
				union[match.MatchID] = true
			}
		}
	}
	if len(union) != len(explorerIDs) {
		return fmt.Errorf("team-page/Explorer union count mismatch: team_pages=%d explorer=%d", len(union), len(explorerIDs))
	}
	for id := range union {
		if !explorerIDs[id] {
			return fmt.Errorf("team-page match %d absent from Explorer set", id)
		}
	}
	return nil
}

func dot54ExplorerQuery(teamIDs []int64) string {
	ids := make([]string, len(teamIDs))
	for i, id := range teamIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	list := strings.Join(ids, ",")
	return "select m.match_id,m.start_time,m.duration,m.cluster,m.leagueid,m.version,m.replay_salt,m.radiant_team_id,m.dire_team_id,json_agg(json_build_object('account_id',pm.account_id,'player_slot',pm.player_slot) order by pm.player_slot) as players from matches m join player_matches pm on pm.match_id=m.match_id where m.start_time between 1770940800 and 1786492800 and (m.radiant_team_id in (" + list + ") or m.dire_team_id in (" + list + ")) group by m.match_id,m.start_time,m.duration,m.cluster,m.leagueid,m.version,m.replay_salt,m.radiant_team_id,m.dire_team_id order by m.match_id"
}

func sortedDOT54MapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resolveDOT54Roster(sourceRoot string, official []dot54OfficialTeam, registry []dot54TeamRegistryEntry) ([]dot54ResolvedTeam, []string, error) {
	aliases := map[string]string{"ogesports": "og"}
	handleAliases := map[string]string{"atf": "ammarthef", "malady": "maladych"}
	result := make([]dot54ResolvedTeam, 0, len(official))
	var conflicts []string
	for _, team := range official {
		wantTeam := normalizeDOT54(team.Name)
		if v := aliases[wantTeam]; v != "" {
			wantTeam = v
		}
		type candidate struct {
			reg     dot54TeamRegistryEntry
			players []dot54TeamPlayer
			matched map[string]dot54TeamPlayer
		}
		var candidates []candidate
		for _, reg := range registry {
			if normalizeDOT54(reg.Name) != wantTeam {
				continue
			}
			var players []dot54TeamPlayer
			playerPath := filepath.Join(sourceRoot, "pages/opendota/teams", strconv.FormatInt(reg.TeamID, 10), "players.json")
			if err := readDOT54JSON(playerPath, &players); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					conflicts = append(conflicts, fmt.Sprintf("%s: candidate valve-team:%d skipped because no checkpointed players page exists", team.Name, reg.TeamID))
					continue
				}
				return nil, nil, err
			}
			byName := map[string]dot54TeamPlayer{}
			for _, p := range players {
				if p.IsCurrentTeamMember {
					byName[normalizeDOT54(p.Name)] = p
				}
			}
			matched := map[string]dot54TeamPlayer{}
			for _, handle := range team.Players {
				key := normalizeDOT54(handle)
				if v := handleAliases[key]; v != "" {
					key = v
				}
				if p, ok := byName[key]; ok {
					matched[handle] = p
				}
			}
			candidates = append(candidates, candidate{reg: reg, players: players, matched: matched})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if len(candidates[i].matched) != len(candidates[j].matched) {
				return len(candidates[i].matched) > len(candidates[j].matched)
			}
			return candidates[i].reg.TeamID < candidates[j].reg.TeamID
		})
		if len(candidates) == 0 || len(candidates[0].matched) != 5 || (len(candidates) > 1 && len(candidates[1].matched) == 5) {
			return nil, nil, fmt.Errorf("team %q stable identity unresolved", team.Name)
		}
		chosen := candidates[0]
		for _, rejected := range candidates[1:] {
			conflicts = append(conflicts, fmt.Sprintf("%s: rejected valve-team:%d with %d/5 official-handle matches; selected valve-team:%d with 5/5", team.Name, rejected.reg.TeamID, len(rejected.matched), chosen.reg.TeamID))
		}
		resolved := dot54ResolvedTeam{TeamID: fmt.Sprintf("valve-team:%d", chosen.reg.TeamID), Name: team.Name, Aliases: sortedDOT54Strings(team.Name, chosen.reg.Name, team.Abbr)}
		selectedAccounts := map[int64]bool{}
		for _, handle := range team.Players {
			p := chosen.matched[handle]
			selectedAccounts[p.AccountID] = true
			resolved.Players = append(resolved.Players, dot54ResolvedPlayer{PersonID: fmt.Sprintf("steam-account32:%d", p.AccountID), Handle: handle, Aliases: sortedDOT54Strings(handle, p.Name)})
		}
		var extras []string
		for _, player := range chosen.players {
			if player.IsCurrentTeamMember && !selectedAccounts[player.AccountID] {
				extras = append(extras, fmt.Sprintf("%s(steam-account32:%d)", player.Name, player.AccountID))
			}
		}
		if len(extras) > 0 {
			sort.Strings(extras)
			conflicts = append(conflicts, fmt.Sprintf("%s: excluded supplemental current-member extras: %s", team.Name, strings.Join(extras, ", ")))
		}
		result = append(result, resolved)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TeamID < result[j].TeamID })
	sort.Strings(conflicts)
	return result, conflicts, nil
}

func normalizeDOT54(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedDOT54Strings(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func buildDOT54Provenance(root string) (dot54SourceProvenance, error) {
	p := dot54SourceProvenance{SchemaVersion: "dot54.source-provenance.v1", Policy: map[string]string{"official_tournament": "primary field/handles only", "opendota": "supplemental public metadata; Valve-derived, not independent corroboration", "valve_cdn": "public replay-object presence probe only; no authenticity claim", "datdota": "not used"}}
	paths := []string{"official-roster-extracted.json", "pages/official/ti2026.html", "pages/official/index-46b931ab.js", "pages/opendota/teams.json", "pages/opendota/team-pages.checkpoint.jsonl", "pages/opendota/explorer-frozen-identity.json", "pages/opendota/constants-patch.json", "pages/opendota/provider-source.json", "pages/valve-head/jobs.tsv", "pages/valve-head/manifest.json"}
	checkpoints, err := readDOT54PageCheckpoints(filepath.Join(root, "pages/opendota/team-pages.checkpoint.jsonl"))
	if err != nil {
		return p, err
	}
	for _, checkpoint := range checkpoints {
		paths = append(paths, filepath.ToSlash(filepath.Join("pages/opendota/teams", checkpoint.TeamID, checkpoint.Kind+".json")))
	}
	sort.Strings(paths)
	for _, rel := range paths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return p, err
		}
		sha, err := fileSHA(path)
		if err != nil {
			return p, err
		}
		p.Files = append(p.Files, dot54SourceFile{Path: rel, Bytes: info.Size(), SHA256: sha})
	}
	return p, nil
}

func dot54Report(scope dot54ScopeCandidate, discovery dot54DiscoveryEvidence, readiness dot54ReadinessEvidence) string {
	return fmt.Sprintf("# DOT-54 M1 terminal evidence\n\nOutcome: `%s`\n\nThe official field contains %d teams and %d resolved stable player account IDs, but the retrieved sources do not prove roster effective intervals at the frozen cutoff. Therefore the %d metadata candidates are terminally classified without treating HEAD-only objects as replay-accessible.\n\nTerminal counts: `%s`\n\nParser provenance: `%s`; executions: %d (blocked upstream of download and parse).\n\nFull-history gate: 0 repeatably parsed / 100 required; 0/16 teams accepted. All historical cell families remain disabled.\n", readiness.Outcome, len(scope.Teams), countDOT54Players(scope.Teams), len(discovery.Matches), formatDOT54Counts(discovery.TerminalCount), readiness.ParserRequired, readiness.ParserExecutionTotal)
}

func countDOT54Players(teams []dot54ResolvedTeam) int {
	n := 0
	for _, t := range teams {
		n += len(t.Players)
	}
	return n
}
func formatDOT54Counts(m map[string]uint32) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func sumDOT54Counts(m map[string]uint32) uint32 {
	var total uint32
	for _, count := range m {
		total += count
	}
	return total
}

func sealDOT54(target *string, value any) error {
	*target = ""
	b, err := contracts.MarshalCanonical(value)
	if err != nil {
		return err
	}
	*target = shaBytes(b)
	return nil
}
func readDOT54JSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
func writeDOT54Immutable(path string, b []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(existing, b) {
			return false, nil
		}
		return false, fmt.Errorf("immutable output conflict: %s", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := atomicfile.WriteFile(path, b, 0o600); err != nil {
		return false, err
	}
	return true, nil
}
