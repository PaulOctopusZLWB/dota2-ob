package history

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// contentSHA256 returns a namespaced content identity over the canonical
// encoding of v. The kind prefix namespaces artifacts so a roster manifest and
// a discovery manifest with identical bodies still get distinct identities.
func contentSHA256(kind string, v any) (string, error) {
	b, err := contracts.MarshalCanonical(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(kind+":"), b...))
	return hex.EncodeToString(h[:]), nil
}

// ProvenanceRef records where a roster fact came from. URLs are public; no
// credentials, account IDs, or cookies are stored here.
type ProvenanceRef struct {
	URL        string    `json:"url"`
	RetrievedAt time.Time `json:"retrieved_at"`
}

// RosterPlayer is one player or coach in the frozen TI 2026 field.
type RosterPlayer struct {
	PersonID      string         `json:"person_id"`
	TeamID        string         `json:"team_id"`
	Handle        string         `json:"handle"`
	Aliases       []string       `json:"aliases"`
	Role          string         `json:"role"`
	EffectiveFrom time.Time      `json:"effective_from"`
	EffectiveUntil time.Time     `json:"effective_until"`
	Provenance    []ProvenanceRef `json:"provenance"`
}

// RosterTeam is one of the sixteen frozen teams.
type RosterTeam struct {
	TeamID        string    `json:"team_id"`
	RosterID      string    `json:"roster_id"`
	Handle        string    `json:"handle"`
	Aliases       []string  `json:"aliases"`
	EffectiveFrom time.Time `json:"effective_from"`
	EffectiveUntil time.Time `json:"effective_until"`
	Provenance    []ProvenanceRef `json:"provenance"`
}

// RosterManifestV1 is the versioned, content-addressed roster manifest M1
// materializes before discovery starts. It binds to an accepted
// TournamentScopeV1 by scope id and content hash; a later roster change
// creates a new manifest and never mutates the accepted one.
type RosterManifestV1 struct {
	SchemaVersion      string        `json:"schema_version"`
	ManifestID         string        `json:"manifest_id"`
	ContentSHA256      string        `json:"content_sha256"`
	TournamentScopeID  string        `json:"tournament_scope_id"`
	TournamentScopeSHA string        `json:"tournament_scope_sha256"`
	Edition            string        `json:"edition"`
	SampledAt          time.Time     `json:"sampled_at"`
	EffectiveCutoff    time.Time     `json:"effective_cutoff"`
	Teams              []RosterTeam  `json:"teams"`
	Players            []RosterPlayer `json:"players"`
	Sources            []ProvenanceRef `json:"sources"`
}

// Validate checks the manifest is self-consistent and binds to a scope. It
// does NOT re-validate the scope; callers pass an already-accepted scope and
// use ValidateAgainstScope for the binding check.
func (m RosterManifestV1) Validate() error {
	if m.SchemaVersion != RosterSchema {
		return schemaErr(RosterSchema, m.SchemaVersion)
	}
	if m.ManifestID == "" || m.ManifestID != m.ContentSHA256 || m.Edition == "" || m.SampledAt.IsZero() || m.EffectiveCutoff.IsZero() || len(m.Teams) != 16 || m.TournamentScopeID == "" || !isSHA(m.TournamentScopeSHA) || len(m.Sources) == 0 {
		return errors.New("invalid roster manifest")
	}
	teamSeen := map[string]bool{}
	for _, t := range m.Teams {
		if t.TeamID == "" || t.RosterID == "" || t.Handle == "" || t.EffectiveFrom.IsZero() || !t.EffectiveUntil.After(t.EffectiveFrom) || teamSeen[t.TeamID] {
			return errors.New("invalid roster team")
		}
		teamSeen[t.TeamID] = true
		if !sortedUnique(mustSortStrings(append(append([]string{}, t.Aliases...), t.Handle))) {
			return errors.New("roster team aliases not ordered/unique")
		}
		if !sourcesOrderedUnique(t.Provenance) {
			return errors.New("roster team provenance not ordered")
		}
	}
	if !sort.SliceIsSorted(m.Teams, func(i, j int) bool { return m.Teams[i].TeamID < m.Teams[j].TeamID }) {
		return errors.New("roster teams not ordered")
	}
	playersPerTeam := map[string]int{}
	personSeen := map[string]bool{}
	for _, p := range m.Players {
		if p.PersonID == "" || p.TeamID == "" || p.Handle == "" || (p.Role != "player" && p.Role != "coach") || p.EffectiveFrom.IsZero() || !p.EffectiveUntil.After(p.EffectiveFrom) || !teamSeen[p.TeamID] || personSeen[p.PersonID] {
			return errors.New("invalid roster player")
		}
		personSeen[p.PersonID] = true
		if p.Role == "player" {
			playersPerTeam[p.TeamID]++
		}
		if !sortedUnique(mustSortStrings(append(append([]string{}, p.Aliases...), p.Handle))) {
			return errors.New("roster player aliases not ordered/unique")
		}
		if !sourcesOrderedUnique(p.Provenance) {
			return errors.New("roster player provenance not ordered")
		}
	}
	for t := range teamSeen {
		if playersPerTeam[t] < 5 {
			return errors.New("roster team has fewer than five players")
		}
	}
	if !sort.SliceIsSorted(m.Players, func(i, j int) bool { return m.Players[i].PersonID < m.Players[j].PersonID }) {
		return errors.New("roster players not ordered")
	}
	if !sourcesOrderedUnique(m.Sources) || !sort.SliceIsSorted(m.Sources, func(i, j int) bool { return m.Sources[i].URL < m.Sources[j].URL }) {
		return errors.New("roster sources not ordered")
	}
	return verifySealed(m, "roster")
}

// ValidateAgainstScope validates the manifest and confirms it binds to the
// accepted scope by id, content hash, edition, and cutoff.
func (m RosterManifestV1) ValidateAgainstScope(scope contracts.TournamentScopeV1) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if m.TournamentScopeID != scope.ScopeID || m.TournamentScopeSHA != scope.ContentSHA256 || m.Edition != scope.Edition || !m.EffectiveCutoff.Equal(scope.HistoryCutoff) || !m.SampledAt.Equal(scope.SampledAt) {
		return errors.New("roster manifest scope binding mismatch")
	}
	for _, t := range m.Teams {
		found := false
		for _, st := range scope.Teams {
			if st.TeamID == t.TeamID && st.RosterID == t.RosterID && st.EffectiveFrom.Equal(t.EffectiveFrom) && st.EffectiveUntil.Equal(t.EffectiveUntil) {
				found = true
				break
			}
		}
		if !found {
			return errors.New("roster team not present in scope")
		}
	}
	// Every roster player must bind to a scope participant by person, team,
	// role, and effective window. A roster player with no matching scope
	// participant is uncorroborated identity and must not bind to the scope.
	for _, p := range m.Players {
		found := false
		for _, sp := range scope.Participants {
			if sp.PersonID == p.PersonID && sp.TeamID == p.TeamID && sp.Role == p.Role && sp.EffectiveFrom.Equal(p.EffectiveFrom) && sp.EffectiveUntil.Equal(p.EffectiveUntil) {
				found = true
				break
			}
		}
		if !found {
			return errors.New("roster player not present in scope")
		}
	}
	return nil
}

// SealRosterManifestV1 computes the content identity and sets it on both
// ManifestID and ContentSHA256, then validates. A sealed manifest is immutable;
// mutating any field invalidates the content hash.
func SealRosterManifestV1(m *RosterManifestV1) error {
	if m == nil {
		return errors.New("nil roster manifest")
	}
	m.ManifestID = ""
	m.ContentSHA256 = ""
	h, err := contentSHA256("roster", *m)
	if err != nil {
		return err
	}
	m.ManifestID = h
	m.ContentSHA256 = h
	return m.Validate()
}

func schemaErr(want, got string) error {
	return errors.New("schema version: want " + want + ", got " + got)
}
func isSHA(v string) bool { return len(v) == 64 && strings.Count(v, "") == 65 && allHex(v) }
func allHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
func mustSortStrings(in []string) []string {
	sort.Strings(in)
	return in
}
func sortedUnique(in []string) bool {
	if !sort.StringsAreSorted(in) {
		return false
	}
	for i := 1; i < len(in); i++ {
		if in[i] == in[i-1] {
			return false
		}
	}
	return true
}
func sourcesOrderedUnique(s []ProvenanceRef) bool {
	for i := range s {
		if s[i].URL == "" || s[i].RetrievedAt.IsZero() || !strings.HasPrefix(s[i].URL, "https://") {
			return false
		}
		if i > 0 && s[i].URL <= s[i-1].URL {
			return false
		}
	}
	return true
}

// verifySealed re-hashes a copy with identity fields cleared and compares the
// recomputed digest to the stored ContentSHA256. It mutates nothing on the
// caller's manifest.
func verifySealed(m RosterManifestV1, kind string) error {
	got := m.ContentSHA256
	if !isSHA(got) {
		return errors.New(kind + " content identity missing")
	}
	m.ManifestID = ""
	m.ContentSHA256 = ""
	want, err := contentSHA256(kind, m)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New(kind + " content identity mismatch")
	}
	return nil
}