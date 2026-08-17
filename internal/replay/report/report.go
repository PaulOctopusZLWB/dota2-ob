// Package report assembles the per-match report artifact that the local API
// and web pages render. It merges the persisted identity, clock, verification,
// facts summary, episodes, phases, and metrics artifacts into one stable
// document with explicit unavailable reasons.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Participant is the report view of one verified participant with its nominal
// role and provenance.
type Participant struct {
	Slot         int32  `json:"slot"`
	AccountID    string `json:"account_id"`
	PlayerName   string `json:"player_name"`
	HeroName     string `json:"hero_name"`
	HeroID       int32  `json:"hero_id"`
	Side         string `json:"side"`
	TeamID       string `json:"team_id"`
	TeamName     string `json:"team_name"`
	NominalRole  string `json:"nominal_role"`
	RoleSource   string `json:"role_source_kind"`
	RoleSourceURL string `json:"role_source_url"`
	RoleSourceRetrievedAt string `json:"role_source_retrieved_at"`
	RoleConfidence string `json:"role_confidence"`
	RoleRecordVersion string `json:"role_record_version"`
}

// Team is the report view of one team.
type Team struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Tag      string `json:"tag"`
	Side     string `json:"side"`
}

// Report is the combined per-match document.
type Report struct {
	SchemaVersion    string            `json:"schema_version"`
	MatchID          string            `json:"match_id"`
	Category         string            `json:"category"`
	Status           string            `json:"status"`
	Publication      string            `json:"publication_state"`
	Verification     *archive.Verification `json:"verification"`
	Identity         *identity.Identity `json:"identity"`
	Clock            *clock.Clock       `json:"clock"`
	FactsSummary     *facts.Summary     `json:"facts_summary"`
	Episodes         *episodes.Output   `json:"episodes"`
	Phases           *phase.Output      `json:"phases"`
	Metrics          *metrics.Output    `json:"metrics"`
	Participants     []Participant      `json:"participants"`
	Teams            []Team             `json:"teams"`
	Canonical        *store.Canonical   `json:"canonical"`
	RoleRegistry     string             `json:"role_registry_version"`
	UnavailableReasons []string         `json:"unavailable_reasons"`
}

// Build loads the persisted artifacts for one match from the store and merges
// them into a report. Missing artifacts are tolerated and reported.
// Build loads the persisted artifacts for one match from the store and merges
// them into a report. roleReg (and overrides) supply nominal-role provenance;
// when they are nil the participants are still listed (with unassigned roles
// and explicit reasons) so the failure is auditable instead of a silent empty
// table. Publication still requires the role gate via PublicationGate.
func Build(st *store.Store, matchID string, roleReg *roles.Registry, overrides *roles.OverrideFile) (*Report, error) {
	r := &Report{
		SchemaVersion: version.ReportSchema,
		MatchID:       matchID,
		Publication:   "suppressed",
		UnavailableReasons: []string{},
	}

	var ver archive.Verification
	if err := st.ReadJSON(matchID, store.ArtifactVerification, &ver); err == nil {
		r.Verification = &ver
		r.Status = ver.State
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var idn identity.Identity
	if err := st.ReadJSON(matchID, store.ArtifactIdentity, &idn); err == nil {
		r.Identity = &idn
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var clk clock.Clock
	if err := st.ReadJSON(matchID, store.ArtifactClock, &clk); err == nil {
		r.Clock = &clk
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var fs facts.Summary
	if err := st.ReadJSON(matchID, store.ArtifactFactsSummary, &fs); err == nil {
		r.FactsSummary = &fs
	}

	var ep episodes.Output
	if err := st.ReadJSON(matchID, store.ArtifactEpisodes, &ep); err == nil {
		r.Episodes = &ep
		for _, u := range ep.Unavailable {
			r.UnavailableReasons = append(r.UnavailableReasons, fmt.Sprintf("episode:%s:%s", u.Kind, u.Reason))
		}
	}

	var ph phase.Output
	if err := st.ReadJSON(matchID, store.ArtifactPhases, &ph); err == nil {
		r.Phases = &ph
	}

	var met metrics.Output
	if err := st.ReadJSON(matchID, store.ArtifactMetrics, &met); err == nil {
		r.Metrics = &met
		for _, u := range met.Unavailable {
			if u.UnavailableReason != "" {
				r.UnavailableReasons = append(r.UnavailableReasons, fmt.Sprintf("metric:%s/%s:%s", u.AccountID, u.MetricID, u.UnavailableReason))
			}
		}
	}

	var can store.Canonical
	if err := st.ReadJSON(matchID, store.ArtifactCanonical, &can); err == nil {
		r.Canonical = &can
	}

	var input archive.Match
	if err := st.ReadJSON(matchID, store.ArtifactInput, &input); err == nil {
		r.Category = input.Category
	}

	// Participants with role provenance from the frozen registry. Identity
	// participants are ALWAYS listed; when role data is unavailable the row
	// carries an explicit unassigned role and reason so the gap is auditable.
	if r.Identity != nil {
		for _, p := range r.Identity.Participants {
			part := Participant{
				Slot: p.Slot, AccountID: p.AccountID, PlayerName: p.PlayerName,
				HeroName: p.HeroName, HeroID: p.HeroID, Side: p.Side,
			}
			if roleReg == nil {
				part.NominalRole = "unassigned"
				part.RoleConfidence = "unavailable"
				r.UnavailableReasons = append(r.UnavailableReasons, fmt.Sprintf("role:%s:registry_unavailable", p.AccountID))
			} else if eff, ok := roleReg.Effective(matchID, p.AccountID, overrides); ok {
				part.NominalRole = eff.NominalRole
				part.TeamID = eff.TeamID
				part.TeamName = eff.TeamName
				part.RoleSource = eff.SourceKind
				part.RoleSourceURL = eff.SourceURL
				part.RoleSourceRetrievedAt = eff.RetrievedAt
				part.RoleConfidence = eff.Confidence
				part.RoleRecordVersion = eff.RecordVersion
			} else {
				part.NominalRole = "unassigned"
				part.RoleConfidence = "unavailable"
				r.UnavailableReasons = append(r.UnavailableReasons, fmt.Sprintf("role:%s:no_registry_record", p.AccountID))
			}
			r.Participants = append(r.Participants, part)
		}
	}

	if r.Identity != nil {
		for _, t := range r.Identity.Teams {
			r.Teams = append(r.Teams, Team{TeamID: t.TeamID, TeamName: t.TeamName, Tag: t.Tag, Side: t.Side})
		}
	}

	// Terminal status derivation mirrors the store catalog logic.
	r.Status, r.Publication = deriveStatus(r)
	sort.Strings(r.UnavailableReasons)
	return r, nil
}

func deriveStatus(r *Report) (status, publication string) {
	if r.Verification == nil {
		return store.StatusMissing, "suppressed"
	}
	switch r.Verification.State {
	case archive.StateMissing:
		return store.StatusMissing, "suppressed"
	case archive.StateCorrupt:
		return store.StatusCorrupt, "suppressed"
	case archive.StateParseFailed:
		return store.StatusParseFailed, "suppressed"
	}
	if r.Verification.State != archive.StateVerified {
		return store.StatusQuarantined, "suppressed"
	}
	if r.Identity == nil || r.Identity.State != identity.StateVerified {
		return store.StatusQuarantined, "suppressed"
	}
	if r.Clock == nil || r.Clock.State != clock.StateCalibrated {
		return store.StatusQuarantined, "suppressed"
	}
	if r.Canonical == nil || r.Canonical.TreeSHA256 == "" {
		return store.StatusQuarantined, "suppressed"
	}
	if r.Phases == nil {
		return store.StatusQuarantined, "suppressed"
	}
	return store.StatusVerified, "published"
}

// CanonicalJSON returns the deterministic encoding.
func (r *Report) CanonicalJSON() ([]byte, error) { return json.Marshal(r) }

// SortParticipants orders participants by slot for deterministic output.
func (r *Report) SortParticipants() {
	sort.Slice(r.Participants, func(i, j int) bool { return r.Participants[i].Slot < r.Participants[j].Slot })
}

// GateResult is the outcome of the publication gate.
type GateResult struct {
	OK      bool
	Reasons []string
}

// PublicationGate verifies the role-provenance publication gate: exactly ten
// identity-bound participants, each with a valid nominal role 1-5 and source
// provenance (source kind and confidence present). Missing registry, missing
// participant rows, or an invalid role fails the gate with explicit reasons.
func (r *Report) PublicationGate() GateResult {
	g := GateResult{OK: true, Reasons: []string{}}
	if r.Identity == nil || r.Identity.State != identity.StateVerified {
		g.OK = false
		g.Reasons = append(g.Reasons, "identity_gate_not_verified")
	}
	if len(r.Participants) != 10 {
		g.OK = false
		g.Reasons = append(g.Reasons, fmt.Sprintf("participants=%d_want_10", len(r.Participants)))
	}
	rolesBySide := map[string]map[string]bool{
		"radiant": {},
		"dire":    {},
	}
	for _, p := range r.Participants {
		valid := false
		switch p.NominalRole {
		case "1", "2", "3", "4", "5":
			valid = true
		}
		if !valid {
			g.OK = false
			g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:invalid_or_missing(%s)", p.AccountID, p.NominalRole))
			continue
		}
		if p.RoleSource == "" {
			g.OK = false
			g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:missing_source", p.AccountID))
		}
		if p.RoleConfidence == "" || p.RoleConfidence == "unavailable" {
			g.OK = false
			g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:missing_confidence", p.AccountID))
		}
		if m, ok := rolesBySide[p.Side]; ok {
			m[p.NominalRole] = true
		}
	}
	// Exactly two participants per role per side (frozen roster contract).
	for side, m := range rolesBySide {
		for _, role := range []string{"1", "2", "3", "4", "5"} {
			if !m[role] {
				g.OK = false
				g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:%s_missing", side, role))
			}
		}
	}
	if len(g.Reasons) == 0 {
		g.OK = true
	}
	return g
}