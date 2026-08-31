package history

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func makeMatch(id string, eventTime time.Time, state string, radiant, dire string) DiscoveryMatch {
	return DiscoveryMatch{
		MatchID: id, SourceEventTime: eventTime, PatchID: "60",
		RadiantTeamID: radiant, DireTeamID: dire, State: state,
		Providers: []string{ProviderOpenDota},
	}
}

// testSHA deterministically derive a valid 64-hex SHA-256 string from a
// short input so tests do not rely on external replay bytes.
func testSHA(seed string) string {
	return sha256HexSeed(seed)
}
func sha256HexSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])
}

func buildDiscovery(t *testing.T, scope contracts.TournamentScopeV1, roster RosterManifestV1, matches []DiscoveryMatch) DiscoveryManifestV1 {
	t.Helper()
	for i := range matches {
		switch matches[i].State {
		case MatchReplayAccessible:
			if matches[i].GameBuild == 0 {
				matches[i].GameBuild = 6896
			}
			if len(matches[i].PlayerPersonIDs) == 0 {
				for _, p := range roster.Players {
					if p.Role == "player" && (p.TeamID == matches[i].RadiantTeamID || p.TeamID == matches[i].DireTeamID) {
						matches[i].PlayerPersonIDs = append(matches[i].PlayerPersonIDs, p.PersonID)
					}
				}
				sort.Strings(matches[i].PlayerPersonIDs)
			}
			if matches[i].ReplaySHA256 == "" {
				matches[i].ReplaySHA256 = testSHA(matches[i].MatchID)
			}
			if matches[i].IdentityStatus == "" {
				matches[i].IdentityStatus = contracts.IdentityVerified
			}
		case MatchReplayQuarantined:
			if matches[i].IdentityStatus == "" {
				matches[i].IdentityStatus = contracts.IdentityQuarantined
			}
		}
	}
	matches = DedupeAndSortMatches(matches)
	dm := DiscoveryManifestV1{
		SchemaVersion:      DiscoverySchema,
		TournamentScopeID:  scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256,
		RosterManifestID:   roster.ManifestID,
		CutoffTime:         scope.HistoryCutoff,
		Request: DiscoveryRequest{
			ContractVersion: DiscoveryContractVersion,
			Providers:       []string{ProviderOpenDota, ProviderSteam},
			Endpoint:        "https://api.opendota.com/api/explorer",
			Query:           map[string]string{"q": "pro"},
			PageLimit:       100,
			CutoffTime:      scope.HistoryCutoff,
			RetrievedAt:     scope.SampledAt,
			PageSHA256:      []string{"abc123"},
		},
		Matches:  matches,
		Coverage: SummarizeCoverage(matches),
	}
	if err := SealDiscoveryManifestV1(&dm); err != nil {
		t.Fatalf("seal discovery: %v", err)
	}
	if err := dm.ValidateAgainstScope(scope, roster); err != nil {
		t.Fatalf("discovery binding: %v", err)
	}
	return dm
}

func TestDiscoveryDedupMergesAcrossProviders(t *testing.T) {
	scope := buildScope(t)
	_ = buildRoster(t, scope)
	eventTime := mustParseTime(t, "2026-07-01T00:00:00Z")
	a := makeMatch("8941092540", eventTime, MatchReplayAccessible, "team-a", "team-b")
	a.Providers = []string{ProviderOpenDota}
	a.ReplaySHA256 = testSHA("8941092540")
	a.IdentityStatus = contracts.IdentityVerified
	b := makeMatch("8941092540", eventTime, MatchReplayAccessible, "team-a", "team-b")
	b.Providers = []string{ProviderSteam}
	b.ReplayURL = "http://replay22.valve.net/570/8941092540_1.dem.bz2"
	merged := DedupeAndSortMatches([]DiscoveryMatch{a, b})
	if len(merged) != 1 {
		t.Fatalf("expected 1 deduped match, got %d", len(merged))
	}
	if len(merged[0].Providers) != 2 {
		t.Fatalf("expected providers merged, got %v", merged[0].Providers)
	}
	if merged[0].ReplayURL == "" {
		t.Fatalf("expected replay url merged from second provider")
	}
}

func TestDiscoverySealingAndCoverageConsistency(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	t1 := mustParseTime(t, "2026-07-01T00:00:00Z")
	t2 := mustParseTime(t, "2026-07-02T00:00:00Z")
	dupes := []DiscoveryMatch{
		makeMatch("1", t1, MatchReplayAccessible, "team-a", "team-b"),
		makeMatch("2", t2, MatchReplayExpired, "team-a", "team-c"),
		makeMatch("3", t1, MatchReplayQuarantined, "team-d", "team-e"),
	}
	dm := buildDiscovery(t, scope, roster, dupes)
	dupes[0].ReplaySHA256 = testSHA("1")
	dupes[0].IdentityStatus = contracts.IdentityVerified
	dupes[2].IdentityStatus = contracts.IdentityQuarantined
	dm = buildDiscovery(t, scope, roster, dupes)
	if dm.Coverage.ByState[MatchReplayAccessible] != 1 {
		t.Fatalf("coverage by_state accessible mismatch: %v", dm.Coverage.ByState)
	}
	if dm.Coverage.TeamMatchCount["team-a"] != 2 {
		t.Fatalf("team match count mismatch: %v", dm.Coverage.TeamMatchCount)
	}
}

func TestDiscoveryRejectsAfterCutoff(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	after := scope.HistoryCutoff.Add(time.Hour)
	m := makeMatch("1", after, MatchDiscovered, "team-a", "team-b")
	dm := DiscoveryManifestV1{
		SchemaVersion: DiscoverySchema, TournamentScopeID: scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256, RosterManifestID: roster.ManifestID,
		CutoffTime: scope.HistoryCutoff,
		Request: DiscoveryRequest{
			ContractVersion: DiscoveryContractVersion, Providers: []string{ProviderOpenDota},
			Endpoint: "https://api.opendota.com/api/explorer", Query: map[string]string{"q": "pro"},
			PageLimit: 100, CutoffTime: scope.HistoryCutoff, RetrievedAt: scope.SampledAt, PageSHA256: []string{"abc123"},
		},
		Matches:  []DiscoveryMatch{m},
		Coverage: SummarizeCoverage([]DiscoveryMatch{m}),
	}
	if err := SealDiscoveryManifestV1(&dm); err == nil {
		t.Fatalf("expected discovery to reject match after cutoff")
	}
}

func TestDiscoveryQuarantineRequiresStatus(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	m := makeMatch("1", mustParseTime(t, "2026-07-01T00:00:00Z"), MatchReplayQuarantined, "team-a", "team-b")
	m.IdentityStatus = contracts.IdentityVerified
	dm := DiscoveryManifestV1{
		SchemaVersion: DiscoverySchema, TournamentScopeID: scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256, RosterManifestID: roster.ManifestID,
		CutoffTime: scope.HistoryCutoff,
		Request: DiscoveryRequest{
			ContractVersion: DiscoveryContractVersion, Providers: []string{ProviderOpenDota},
			Endpoint: "https://api.opendota.com/api/explorer", Query: map[string]string{"q": "pro"},
			PageLimit: 100, CutoffTime: scope.HistoryCutoff, RetrievedAt: scope.SampledAt, PageSHA256: []string{"abc123"},
		},
		Matches:  []DiscoveryMatch{m},
		Coverage: SummarizeCoverage([]DiscoveryMatch{m}),
	}
	if err := SealDiscoveryManifestV1(&dm); err == nil {
		t.Fatalf("expected quarantine to require quarantined identity status")
	}
}
