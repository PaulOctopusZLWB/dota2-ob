package history

import (
	"errors"
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// DiscoveryRequest is the frozen provider/query/page contract for one
// bounded 180-day discovery run. It is the reproducible union, not an
// open-ended search: provider, endpoint, query parameters, pagination/cursor
// bounds, retrieval time, and response-page hashes are all pinned.
type DiscoveryRequest struct {
	ContractVersion string            `json:"contract_version"`
	Providers      []string          `json:"providers"`
	Endpoint       string            `json:"endpoint"`
	Query          map[string]string `json:"query"`
	PageLimit      uint32            `json:"page_limit"`
	CutoffTime     time.Time         `json:"cutoff_time"`
	RetrievedAt    time.Time         `json:"retrieved_at"`
	PageSHA256     []string          `json:"page_sha256"`
}

// Terminal states for one discovered match/replay. Every item in the union
// gets exactly one of these; none means "still running", which is not a
// terminal publishable state.
const (
	MatchDiscovered       = "discovered"         // metadata seen, replay not yet attempted
	MatchReplayAccessible = "replay_accessible"   // download+checksum+magic+identity all passed
	MatchReplayQuarantined = "replay_quarantined" // held: identity mismatch pending reconciliation
	MatchReplayExpired    = "replay_expired"      // CDN returned 404/expired
	MatchReplayPurged     = "replay_purged"       // provider metadata purged
	MatchReplayUnlisted   = "replay_unlisted"     // no replay_url in metadata
	MatchChecksumMismatch = "replay_checksum_mismatch"
	MatchIdentityMismatch = "replay_identity_mismatch"
	MatchParseFailed      = "parse_failed"
	MatchNormalizeFailed  = "normalize_failed"
	MatchOmitted          = "omitted" // excluded with an explicit reason (e.g. wrong patch, non-pro)
)

// IdentityStatus for a match whose replay has been correlated against public
// metadata uses the contract's verified/quarantined states directly.

// DiscoveryMatch is one item in the deduplicated match/replay union.
type DiscoveryMatch struct {
	MatchID              string   `json:"match_id"`
	SourceEventTime      time.Time `json:"source_event_time"`
	LeagueID             string   `json:"league_id"`
	PatchID              string   `json:"patch_id"`
	RadiantTeamID        string   `json:"radiant_team_id"`
	DireTeamID           string   `json:"dire_team_id"`
	PlayerPersonIDs      []string `json:"player_person_ids"`
	ReplayURL            string   `json:"replay_url,omitempty"`
	ReplayCluster        string   `json:"replay_cluster,omitempty"`
	ReplaySalt           string   `json:"replay_salt,omitempty"`
	ReplayFormatVersion  uint32   `json:"replay_format_version,omitempty"`
	Providers            []string `json:"providers"`
	PageSHA256           []string `json:"page_sha256"`
	State                string   `json:"state"`
	Reason               string   `json:"reason,omitempty"`
	IdentityStatus       string   `json:"identity_status,omitempty"`
	ReplaySHA256         string   `json:"replay_sha256,omitempty"`
	ParseAttempts        int      `json:"parse_attempts,omitempty"`
}

// DiscoveryCoverage summarizes the manifest so an operator can read coverage
// rather than fabricate it. Counts are derived from the match list; they must
// match a fresh recount or the manifest is invalid.
type DiscoveryCoverage struct {
	DiscoveredTotal uint32            `json:"discovered_total"`
	DeduplicatedTotal uint32          `json:"deduplicated_total"`
	ByState         map[string]uint32 `json:"by_state"`
	TeamsRepresented []string         `json:"teams_represented"`
	TeamMatchCount  map[string]uint32 `json:"team_match_count"`
	PatchesSeen     []string          `json:"patches_seen"`
}

// DiscoveryManifestV1 is the content-addressed, deduplicated 180-day
// match/replay manifest. Every discovered item has a terminal, explainable
// state.
type DiscoveryManifestV1 struct {
	SchemaVersion       string             `json:"schema_version"`
	ManifestID          string             `json:"manifest_id"`
	ContentSHA256       string             `json:"content_sha256"`
	TournamentScopeID   string             `json:"tournament_scope_id"`
	TournamentScopeSHA  string             `json:"tournament_scope_sha256"`
	RosterManifestID     string             `json:"roster_manifest_id"`
	CutoffTime          time.Time          `json:"cutoff_time"`
	Request             DiscoveryRequest   `json:"request"`
	Matches             []DiscoveryMatch   `json:"matches"`
	Coverage            DiscoveryCoverage  `json:"coverage"`
}

func (m DiscoveryManifestV1) Validate() error {
	if m.SchemaVersion != DiscoverySchema {
		return schemaErr(DiscoverySchema, m.SchemaVersion)
	}
	if m.ManifestID == "" || m.ManifestID != m.ContentSHA256 || m.TournamentScopeID == "" || !isSHA(m.TournamentScopeSHA) || !isSHA(m.RosterManifestID) || m.CutoffTime.IsZero() {
		return errors.New("invalid discovery manifest identity")
	}
	if m.Request.ContractVersion != DiscoveryContractVersion || len(m.Request.Providers) == 0 || !sort.StringsAreSorted(m.Request.Providers) || m.Request.Endpoint == "" || m.Request.PageLimit == 0 || m.Request.RetrievedAt.IsZero() || m.Request.CutoffTime.IsZero() {
		return errors.New("invalid discovery request")
	}
	if !sort.SliceIsSorted(m.Request.PageSHA256, func(i, j int) bool { return m.Request.PageSHA256[i] < m.Request.PageSHA256[j] }) {
		return errors.New("discovery page hashes not ordered")
	}
	seen := map[string]bool{}
	for _, x := range m.Matches {
		if x.MatchID == "" || x.SourceEventTime.IsZero() || x.State == "" || seen[x.MatchID] {
			return errors.New("invalid discovery match")
		}
		seen[x.MatchID] = true
		if x.SourceEventTime.After(m.CutoffTime) {
			return errors.New("discovery match after cutoff")
		}
		if !validMatchState(x.State) {
			return errors.New("discovery match has invalid state")
		}
		if x.State == MatchReplayAccessible && (!isSHA(x.ReplaySHA256) || x.IdentityStatus != contracts.IdentityVerified) {
			return errors.New("replay_accessible match missing verified identity")
		}
		if x.State == MatchReplayQuarantined && x.IdentityStatus != contracts.IdentityQuarantined {
			return errors.New("quarantined match missing quarantine identity status")
		}
		if !sort.StringsAreSorted(x.Providers) || !sort.StringsAreSorted(x.PlayerPersonIDs) || !sort.StringsAreSorted(x.PageSHA256) {
			return errors.New("discovery match arrays not ordered")
		}
	}
	if !sort.SliceIsSorted(m.Matches, func(i, j int) bool { return m.Matches[i].MatchID < m.Matches[j].MatchID }) {
		return errors.New("discovery matches not ordered")
	}
	if err := m.validateCoverage(); err != nil {
		return err
	}
	return verifySealedDiscovery(m)
}

func (m DiscoveryManifestV1) validateCoverage() error {
	c := DiscoveryCoverage{ByState: map[string]uint32{}, TeamMatchCount: map[string]uint32{}}
	teams := map[string]bool{}
	patches := map[string]bool{}
	for _, x := range m.Matches {
		c.DeduplicatedTotal++
		c.ByState[x.State]++
		for _, t := range []string{x.RadiantTeamID, x.DireTeamID} {
			if t == "" {
				continue
			}
			teams[t] = true
			c.TeamMatchCount[t]++
		}
		if x.PatchID != "" {
			patches[x.PatchID] = true
		}
	}
	c.DiscoveredTotal = c.DeduplicatedTotal
	c.TeamsRepresented = sortedKeys(teams)
	c.PatchesSeen = sortedKeys(patches)
	if c.DiscoveredTotal != m.Coverage.DiscoveredTotal || c.DeduplicatedTotal != m.Coverage.DeduplicatedTotal {
		return errors.New("discovery coverage totals mismatch")
	}
	if len(c.ByState) != len(m.Coverage.ByState) {
		return errors.New("discovery coverage by_state mismatch")
	}
	for k, v := range c.ByState {
		if m.Coverage.ByState[k] != v {
			return errors.New("discovery coverage by_state count mismatch")
		}
	}
	if !equalStrings(c.TeamsRepresented, m.Coverage.TeamsRepresented) || !equalStrings(c.PatchesSeen, m.Coverage.PatchesSeen) {
		return errors.New("discovery coverage teams/patches mismatch")
	}
	if len(c.TeamMatchCount) != len(m.Coverage.TeamMatchCount) {
		return errors.New("discovery coverage team match count mismatch")
	}
	for k, v := range c.TeamMatchCount {
		if m.Coverage.TeamMatchCount[k] != v {
			return errors.New("discovery coverage team match count mismatch")
		}
	}
	return nil
}

// ValidateAgainstScope confirms the manifest binds to the accepted scope.
func (m DiscoveryManifestV1) ValidateAgainstScope(scope contracts.TournamentScopeV1, roster RosterManifestV1) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := roster.ValidateAgainstScope(scope); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if m.TournamentScopeID != scope.ScopeID || m.TournamentScopeSHA != scope.ContentSHA256 || !m.CutoffTime.Equal(scope.HistoryCutoff) || m.RosterManifestID != roster.ManifestID {
		return errors.New("discovery manifest scope/roster binding mismatch")
	}
	if m.Request.ContractVersion != scope.DiscoveryContractVersion {
		return errors.New("discovery contract version mismatch")
	}
	return nil
}

// SealDiscoveryManifestV1 computes the content identity and validates.
func SealDiscoveryManifestV1(m *DiscoveryManifestV1) error {
	if m == nil {
		return errors.New("nil discovery manifest")
	}
	m.ManifestID = ""
	m.ContentSHA256 = ""
	h, err := contentSHA256("discovery", *m)
	if err != nil {
		return err
	}
	m.ManifestID = h
	m.ContentSHA256 = h
	return m.Validate()
}

// DedupeAndSortMatches merges candidate matches from multiple providers into a
// deduplicated, match_id-sorted union. Later providers only fill fields that
// earlier providers left empty; a populated field is never silently
// overwritten. Provider lists accumulate and are sorted/deduped per match.
// This is the pure core of discovery; the HTTP fetch lives in the adapter.
func DedupeAndSortMatches(candidates []DiscoveryMatch) []DiscoveryMatch {
	byID := map[string]*DiscoveryMatch{}
	order := []string{}
	for _, c := range candidates {
		if c.MatchID == "" {
			continue
		}
		existing, ok := byID[c.MatchID]
		if !ok {
			cp := c
			cp.Providers = sortedDedupe(c.Providers)
			cp.PageSHA256 = sortedDedupe(c.PageSHA256)
			cp.PlayerPersonIDs = sortedDedupe(c.PlayerPersonIDs)
			byID[c.MatchID] = &cp
			order = append(order, c.MatchID)
			continue
		}
		mergeMatch(existing, &c)
	}
	out := make([]DiscoveryMatch, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MatchID < out[j].MatchID })
	return out
}

func mergeMatch(dst, src *DiscoveryMatch) {
	if dst.LeagueID == "" {
		dst.LeagueID = src.LeagueID
	}
	if dst.PatchID == "" {
		dst.PatchID = src.PatchID
	}
	if dst.RadiantTeamID == "" {
		dst.RadiantTeamID = src.RadiantTeamID
	}
	if dst.DireTeamID == "" {
		dst.DireTeamID = src.DireTeamID
	}
	if dst.ReplayURL == "" {
		dst.ReplayURL = src.ReplayURL
	}
	if dst.ReplayCluster == "" {
		dst.ReplayCluster = src.ReplayCluster
	}
	if dst.ReplaySalt == "" {
		dst.ReplaySalt = src.ReplaySalt
	}
	if dst.ReplayFormatVersion == 0 {
		dst.ReplayFormatVersion = src.ReplayFormatVersion
	}
	if dst.SourceEventTime.IsZero() {
		dst.SourceEventTime = src.SourceEventTime
	}
	if dst.State == "" {
		dst.State = src.State
	}
	if dst.Reason == "" {
		dst.Reason = src.Reason
	}
	if dst.ReplaySHA256 == "" {
		dst.ReplaySHA256 = src.ReplaySHA256
	}
	if dst.IdentityStatus == "" {
		dst.IdentityStatus = src.IdentityStatus
	}
	if dst.ParseAttempts == 0 {
		dst.ParseAttempts = src.ParseAttempts
	}
	dst.Providers = sortedDedupe(append(dst.Providers, src.Providers...))
	dst.PageSHA256 = sortedDedupe(append(dst.PageSHA256, src.PageSHA256...))
	dst.PlayerPersonIDs = sortedDedupe(append(dst.PlayerPersonIDs, src.PlayerPersonIDs...))
}

func validMatchState(s string) bool {
	switch s {
	case MatchDiscovered, MatchReplayAccessible, MatchReplayQuarantined, MatchReplayExpired,
		MatchReplayPurged, MatchReplayUnlisted, MatchChecksumMismatch, MatchIdentityMismatch,
		MatchParseFailed, MatchNormalizeFailed, MatchOmitted:
		return true
	}
	return false
}

func sortedDedupe(in []string) []string {
	set := map[string]bool{}
	for _, x := range in {
		if x != "" {
			set[x] = true
		}
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func verifySealedDiscovery(m DiscoveryManifestV1) error {
	got := m.ContentSHA256
	if !isSHA(got) {
		return errors.New("discovery content identity missing")
	}
	m.ManifestID = ""
	m.ContentSHA256 = ""
	want, err := contentSHA256("discovery", m)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("discovery content identity mismatch")
	}
	return nil
}

// SummarizeCoverage recomputes coverage from a match list. It is the pure
// helper adapters call before sealing so the stored coverage is a derived,
// not hand-edited, field.
func SummarizeCoverage(matches []DiscoveryMatch) DiscoveryCoverage {
	c := DiscoveryCoverage{ByState: map[string]uint32{}, TeamMatchCount: map[string]uint32{}}
	teams := map[string]bool{}
	patches := map[string]bool{}
	for _, x := range matches {
		c.DeduplicatedTotal++
		c.DiscoveredTotal++
		c.ByState[x.State]++
		for _, t := range []string{x.RadiantTeamID, x.DireTeamID} {
			if t == "" {
				continue
			}
			teams[t] = true
			c.TeamMatchCount[t]++
		}
		if x.PatchID != "" {
			patches[x.PatchID] = true
		}
	}
	c.TeamsRepresented = sortedKeys(teams)
	c.PatchesSeen = sortedKeys(patches)
	return c
}