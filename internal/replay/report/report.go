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
	"strings"

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
// role and full provenance. When a manual override is effective, the override
// source/reason/timestamp are carried alongside the underlying registry
// provenance so both are auditable.
type Participant struct {
	Slot       int32  `json:"slot"`
	AccountID  string `json:"account_id"`
	PlayerName string `json:"player_name"`
	HeroName   string `json:"hero_name"`
	HeroID     int32  `json:"hero_id"`
	Side       string `json:"side"`
	TeamID     string `json:"team_id"`
	TeamName   string `json:"team_name"`
	// SourceNominalRole is the immutable nominal role from the frozen source
	// registry; it is never overwritten by an override.
	SourceNominalRole string `json:"source_nominal_role"`
	// NominalRole is the current effective role (source unless overridden).
	NominalRole string `json:"nominal_role"`
	// RoleSourceKind is the effective source of the nominal role: the base
	// registry source kind, or "manual_override" when an override applies.
	RoleSourceKind string `json:"role_source_kind"`
	// Base registry provenance retained separately from any override.
	RoleSourceURL         string `json:"role_source_url"`
	RoleSourceRetrievedAt string `json:"role_source_retrieved_at"`
	RoleConfidence        string `json:"role_confidence"`
	RoleRecordVersion     string `json:"role_record_version"`
	// Effective manual-override provenance (present only when an override is
	// applied to this participant's role).
	OverrideApplied bool    `json:"override_applied"`
	OverrideReason  *string `json:"override_reason,omitempty"`
	OverrideAt      *string `json:"override_at,omitempty"`
	// OverrideAuthor is the review author of the effective override. It never
	// implies the manual value came from the public source registry.
	OverrideAuthor *string `json:"override_author,omitempty"`
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
	SchemaVersion      string                `json:"schema_version"`
	MatchID            string                `json:"match_id"`
	Category           string                `json:"category"`
	Status             string                `json:"status"`
	Reason             string                `json:"reason,omitempty"`
	Publication        string                `json:"publication_state"`
	Verification       *archive.Verification `json:"verification"`
	Identity           *identity.Identity    `json:"identity"`
	Clock              *clock.Clock          `json:"clock"`
	FactsSummary       *facts.Summary        `json:"facts_summary"`
	Episodes           *episodes.Output      `json:"episodes"`
	Phases             *phase.Output         `json:"phases"`
	Metrics            *metrics.Output       `json:"metrics"`
	Participants       []Participant         `json:"participants"`
	Teams              []Team                `json:"teams"`
	Canonical          *store.Canonical      `json:"canonical"`
	RoleRegistry       string                `json:"role_registry_version"`
	UnavailableReasons []string              `json:"unavailable_reasons"`
}

// OverlayRoles deterministically re-resolves each participant's role from the
// frozen registry plus the current effective override store, updating the
// report's source/effective role and override provenance in place. It is the
// on-read compatibility overlay for older reports: it never rewrites immutable
// artifacts and never fabricates provenance. RoleReg may be nil (roles left
// as-is for auditability).
func OverlayRoles(r *Report, matchID string, roleReg *roles.Registry, overrides *roles.OverrideFile) {
	if r == nil || roleReg == nil {
		return
	}
	for i := range r.Participants {
		p := &r.Participants[i]
		eff, ok := roleReg.Effective(matchID, p.AccountID, overrides)
		if !ok {
			continue
		}
		p.SourceNominalRole = eff.SourceNominalRole
		p.NominalRole = eff.NominalRole
		p.TeamID = eff.TeamID
		p.TeamName = eff.TeamName
		p.RoleSourceKind = eff.SourceKind
		p.RoleSourceURL = eff.SourceURL
		p.RoleSourceRetrievedAt = eff.RetrievedAt
		p.RoleConfidence = eff.Confidence
		p.RoleRecordVersion = eff.RecordVersion
		p.OverrideApplied = eff.OverrideApplied
		p.OverrideReason = eff.OverrideReason
		p.OverrideAt = eff.OverrideAt
		p.OverrideAuthor = eff.OverrideAuthor
		if eff.OverrideApplied {
			p.RoleSourceKind = "manual_override"
		}
	}
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
		SchemaVersion:      version.ReportSchema,
		MatchID:            matchID,
		Publication:        "suppressed",
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
				part.RoleSourceKind = "unavailable"
				r.UnavailableReasons = append(r.UnavailableReasons, fmt.Sprintf("role:%s:registry_unavailable", p.AccountID))
			} else if eff, ok := roleReg.Effective(matchID, p.AccountID, overrides); ok {
				part.SourceNominalRole = eff.SourceNominalRole
				part.NominalRole = eff.NominalRole
				part.TeamID = eff.TeamID
				part.TeamName = eff.TeamName
				// Effective source: manual_override when applied, else base.
				part.RoleSourceKind = eff.SourceKind
				// Underlying registry provenance retained separately.
				part.RoleSourceURL = eff.SourceURL
				part.RoleSourceRetrievedAt = eff.RetrievedAt
				part.RoleConfidence = eff.Confidence
				part.RoleRecordVersion = eff.RecordVersion
				// Effective manual-override provenance.
				part.OverrideApplied = eff.OverrideApplied
				part.OverrideReason = eff.OverrideReason
				part.OverrideAt = eff.OverrideAt
				part.OverrideAuthor = eff.OverrideAuthor
				if eff.OverrideApplied {
					part.RoleSourceKind = "manual_override"
				}
			} else {
				part.NominalRole = "unassigned"
				part.RoleConfidence = "unavailable"
				part.RoleSourceKind = "unavailable"
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

	// Authoritative gated state: source gates plus the publication (role
	// provenance) gate. The report never independently claims published; the
	// runner persists this state and status.json is the single source of
	// truth consumed by catalog and API.
	r.Status, r.Publication, r.Reason = ComputeState(r)
	sort.Strings(r.UnavailableReasons)
	return r, nil
}

// ComputeState derives the authoritative terminal status, publication, and
// reason from the report's source gates and the role-provenance publication
// gate. It is used by the runner, report build, and any consumer that needs
// the gated state without re-deriving it independently.
func ComputeState(r *Report) (status, publication, reason string) {
	if r.Verification == nil {
		return store.StatusMissing, "suppressed", "verification_missing"
	}
	switch r.Verification.State {
	case archive.StateMissing:
		return store.StatusMissing, "suppressed", r.Verification.Reason
	case archive.StateCorrupt:
		return store.StatusCorrupt, "suppressed", r.Verification.Reason
	case archive.StateParseFailed:
		return store.StatusParseFailed, "suppressed", r.Verification.Reason
	}
	if r.Verification.State != archive.StateVerified {
		return store.StatusQuarantined, "suppressed", "verification_" + r.Verification.Reason
	}
	if r.Identity == nil || r.Identity.State != identity.StateVerified {
		return store.StatusQuarantined, "suppressed", "identity_gate_not_verified"
	}
	if r.Clock == nil || r.Clock.State != clock.StateCalibrated {
		return store.StatusQuarantined, "suppressed", "clock_gate_not_calibrated"
	}
	if r.Phases == nil {
		return store.StatusQuarantined, "suppressed", "phases_missing"
	}
	if gate := r.PublicationGate(); !gate.OK {
		return store.StatusQuarantined, "suppressed", "role_provenance_gate: " + strings.Join(gate.Reasons, "; ")
	}
	return store.StatusVerified, "published", "all_gates_pass"
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
// identity-bound participants, each with a valid effective nominal role 1-5
// and source provenance (source kind and confidence present), and a complete
// frozen source roster (each source side covers roles 1-5). Coverage is
// evaluated on the immutable SOURCE roles, because a manual override is a
// correction to a disputed assignment and must not un-publish the match merely
// by re-labelling one player; the override itself is separately validated for
// manual_override source + reason + timestamp + base source provenance.
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
	sourceRolesBySide := map[string]map[string]bool{
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
		// Effective source must be present. For an override, the source must
		// be manual_override with a deterministic reason and timestamp.
		if p.OverrideApplied {
			if p.RoleSourceKind != "manual_override" {
				g.OK = false
				g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:override_source_must_be_manual_override", p.AccountID))
			}
			if p.OverrideReason == nil || *p.OverrideReason == "" {
				g.OK = false
				g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:override_reason_required", p.AccountID))
			}
			if p.OverrideAt == nil || *p.OverrideAt == "" {
				g.OK = false
				g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:override_timestamp_required", p.AccountID))
			}
			// Base registry provenance still required for audit.
			if p.RoleSourceURL == "" || p.RoleSourceRetrievedAt == "" {
				g.OK = false
				g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:base_source_required", p.AccountID))
			}
		} else {
			if p.RoleSourceKind == "" {
				g.OK = false
				g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:missing_source", p.AccountID))
			}
		}
		if p.RoleConfidence == "" || p.RoleConfidence == "unavailable" {
			g.OK = false
			g.Reasons = append(g.Reasons, fmt.Sprintf("role:%s:missing_confidence", p.AccountID))
		}
		// Roster coverage uses the immutable source role.
		src := p.SourceNominalRole
		if src == "" {
			src = p.NominalRole
		}
		if m, ok := sourceRolesBySide[p.Side]; ok {
			m[src] = true
		}
	}
	// Frozen roster contract: each source side covers all roles 1-5.
	for side, m := range sourceRolesBySide {
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
