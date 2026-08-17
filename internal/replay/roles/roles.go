// Package roles loads the auditable nominal-role registry and applies explicit
// manual overrides. Nominal roles are frozen from reliable public tournament,
// team, and database records; they are never inferred from farm share, hero,
// lane, items, or replay behavior.
package roles

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Registry is the frozen role registry document.
type Registry struct {
	SchemaVersion string      `json:"schema_version"`
	TournamentID  string      `json:"tournament_id"`
	Matches       []RoleMatch `json:"matches"`
}

// RoleMatch is one match's role records.
type RoleMatch struct {
	MatchID string     `json:"match_id"`
	Teams   []RoleTeam `json:"teams"`
}

// RoleTeam is one team's roster records with provenance.
type RoleTeam struct {
	TeamID       string       `json:"team_id"`
	TeamName     string       `json:"team_name"`
	Side         string       `json:"side"`
	SourceKind   string       `json:"source_kind"`
	SourceURL    string       `json:"source_url"`
	SourceLocator string      `json:"source_locator"`
	RetrievedAt  string       `json:"retrieved_at"`
	Participants []RoleRecord `json:"participants"`
}

// RoleRecord is one participant's nominal role record.
type RoleRecord struct {
	RoleRecordID   string `json:"role_record_id"`
	AccountID      string `json:"account_id"`
	PlayerName     string `json:"player_name"`
	ExpectedHeroID int    `json:"expected_hero_id"`
	NominalRole    string `json:"nominal_role"`
	RoleConfidence string `json:"role_confidence"`
}

// Override is one explicit manual role override.
type Override struct {
	MatchID     string `json:"match_id"`
	AccountID   string `json:"account_id"`
	NominalRole string `json:"nominal_role"`
	Reason      string `json:"reason"`
	AppliedAt   string `json:"applied_at"`
}

// OverrideFile is the persisted override set.
type OverrideFile struct {
	SchemaVersion string      `json:"schema_version"`
	Overrides     []Override  `json:"overrides"`
}

// EffectiveRole is the resolved role for one participant in one match.
type EffectiveRole struct {
	TournamentID  string  `json:"tournament_id"`
	MatchID       string  `json:"match_id"`
	TeamID        string  `json:"team_id"`
	TeamName      string  `json:"team_name"`
	AccountID     string  `json:"account_id"`
	PlayerName    string  `json:"player_name"`
	NominalRole   string  `json:"nominal_role"`
	SourceKind    string  `json:"source_kind"`
	SourceURL     string  `json:"source_url"`
	SourceLocator string  `json:"source_locator"`
	RetrievedAt   string  `json:"retrieved_at"`
	Confidence    string  `json:"confidence"`
	RecordVersion string  `json:"record_version"`
	OverrideApplied bool `json:"override_applied"`
	OverrideReason *string `json:"override_reason,omitempty"`
	OverrideAt     *string `json:"override_at,omitempty"`
}

// LoadRegistry reads a role registry JSON file.
func LoadRegistry(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("roles: read registry: %w", err)
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("roles: decode registry: %w", err)
	}
	return &r, nil
}

// LoadOverrides reads an override file (missing file is empty overrides).
func LoadOverrides(path string) (*OverrideFile, error) {
	o := &OverrideFile{SchemaVersion: version.RoleSchema, Overrides: []Override{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return o, nil
		}
		return nil, fmt.Errorf("roles: read overrides: %w", err)
	}
	if err := json.Unmarshal(b, o); err != nil {
		return nil, fmt.Errorf("roles: decode overrides: %w", err)
	}
	return o, nil
}

// Effective returns the resolved role for a participant, applying the first
// matching override. Returns (nil, false) when the registry has no record for
// the match/account pair.
func (r *Registry) Effective(matchID, accountID string, overrides *OverrideFile) (*EffectiveRole, bool) {
	base := r.raw(matchID, accountID)
	if base == nil {
		return nil, false
	}
	eff := &EffectiveRole{
		TournamentID:  r.TournamentID,
		MatchID:       matchID,
		TeamID:        base.teamID,
		TeamName:      base.teamName,
		AccountID:     accountID,
		PlayerName:    base.playerName,
		NominalRole:   base.nominalRole,
		SourceKind:    base.sourceKind,
		SourceURL:     base.sourceURL,
		SourceLocator: base.sourceLocator,
		RetrievedAt:   base.retrievedAt,
		Confidence:    base.confidence,
		RecordVersion: r.SchemaVersion,
	}
	if overrides != nil {
		for _, o := range overrides.Overrides {
			if o.MatchID == matchID && o.AccountID == accountID {
				eff.NominalRole = o.NominalRole
				eff.OverrideApplied = true
				reason := o.Reason
				eff.OverrideReason = &reason
				at := o.AppliedAt
				if at == "" {
					at = time.Now().UTC().Format(time.RFC3339)
				}
				eff.OverrideAt = &at
				break
			}
		}
	}
	return eff, true
}

type rawRole struct {
	teamID, teamName, playerName, nominalRole, confidence string
	sourceKind, sourceURL, sourceLocator, retrievedAt      string
}

func (r *Registry) raw(matchID, accountID string) *rawRole {
	for i := range r.Matches {
		m := &r.Matches[i]
		if m.MatchID != matchID {
			continue
		}
		for j := range m.Teams {
			t := &m.Teams[j]
			for k := range t.Participants {
				p := &t.Participants[k]
				if p.AccountID == accountID {
					return &rawRole{
						teamID:       t.TeamID,
						teamName:     t.TeamName,
						playerName:   p.PlayerName,
						nominalRole:  p.NominalRole,
						confidence:   p.RoleConfidence,
						sourceKind:   t.SourceKind,
						sourceURL:    t.SourceURL,
						sourceLocator: t.SourceLocator,
						retrievedAt:  t.RetrievedAt,
					}
				}
			}
		}
	}
	return nil
}

// CanonicalJSON returns the deterministic encoding of the registry.
func (r *Registry) CanonicalJSON() ([]byte, error) { return json.Marshal(r) }

// CanonicalJSON returns the deterministic encoding of the overrides.
func (o *OverrideFile) CanonicalJSON() ([]byte, error) { return json.Marshal(o) }