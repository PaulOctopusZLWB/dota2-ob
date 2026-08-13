package history

import (
	"sort"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// ReadinessEvidence is the measured outcome of an exhaustive discovery run.
// It records exactly what was reached and forces one of the three spec-defined
// outcomes; no count is reached by relaxing source safety, inventing
// identity, mixing patches/windows, or treating unavailable replays as
// successful samples.
type ReadinessEvidence struct {
	SchemaVersion           string            `json:"schema_version"`
	Outcome                 string            `json:"outcome"`
	TournamentScopeID       string            `json:"tournament_scope_id"`
	ReplayAccessibleTotal   uint32            `json:"replay_accessible_total"`
	TeamsRepresented        []string          `json:"teams_represented"`
	TeamMatchCount          map[string]uint32 `json:"team_match_count"`
	PerStateCounts          map[string]uint32 `json:"per_state_counts"`
	DisabledFamilies        []string          `json:"disabled_families,omitempty"`
	RestrictedReason        string            `json:"restricted_reason,omitempty"`
}

// ReadinessGate evaluates the exhaustive discovery manifest against the
// frozen scope's discovery policy and returns the evidence.
//
//   - full_history_go requires at least 100 replay-accessible verified
//     replays, all 16 teams represented by at least five matches; per-cell
//     baseline minima are enforced by Aggregate (cells below min publish as
//     absent), so the gate additionally requires at least one accessible
//     baseline family to be enabled;
//   - restricted_history_go is an explicit reviewed scope revision when fewer
//     than 100 replays or a team has fewer than five, BUT at least one team
//     reaches the minimum so some baseline families can be enabled;
//     historical rules are disabled per uncovered team/cell;
//   - historical_no_go applies when no historical rule family can meet its
//     published minimum (no accessible replays, or no team reaches its
//     minimum-match coverage).
//
// The gate validates the scope and that the manifest binds to it before
// evaluating coverage. It cannot return a fourth state.
func ReadinessGate(scope contracts.TournamentScopeV1, manifest DiscoveryManifestV1) ReadinessEvidence {
	e := ReadinessEvidence{
		SchemaVersion:     ReadinessSchema,
		TournamentScopeID: scope.ScopeID,
		TeamMatchCount:    map[string]uint32{},
		PerStateCounts:    map[string]uint32{},
	}
	// Validate scope and that the manifest binds to it; an invalid or unbound
	// manifest cannot support any baseline family.
	if err := scope.Validate(); err != nil {
		e.Outcome = ReadinessHistoricalNoGo
		e.RestrictedReason = "scope_invalid:" + err.Error()
		return e
	}
	if err := manifest.Validate(); err != nil {
		e.Outcome = ReadinessHistoricalNoGo
		e.RestrictedReason = "manifest_invalid:" + err.Error()
		return e
	}
	if manifest.TournamentScopeID != scope.ScopeID || manifest.TournamentScopeSHA != scope.ContentSHA256 || !manifest.CutoffTime.Equal(scope.HistoryCutoff) || manifest.Request.ContractVersion != scope.DiscoveryContractVersion {
		e.Outcome = ReadinessHistoricalNoGo
		e.RestrictedReason = "manifest_not_bound_to_scope"
		return e
	}
	teamMatches := map[string]uint32{}
	teams := map[string]bool{}
	var accessible uint32
	for _, m := range manifest.Matches {
		e.PerStateCounts[m.State]++
		for _, t := range []string{m.RadiantTeamID, m.DireTeamID} {
			if t == "" {
				continue
			}
			teams[t] = true
			if m.State == MatchReplayAccessible {
				teamMatches[t]++
			}
		}
		if m.State == MatchReplayAccessible {
			accessible++
		}
	}
	e.ReplayAccessibleTotal = accessible
	for t := range teams {
		e.TeamsRepresented = append(e.TeamsRepresented, t)
	}
	sort.Strings(e.TeamsRepresented)
	for t, c := range teamMatches {
		e.TeamMatchCount[t] = c
	}

	fullTarget := scope.Discovery.FullHistoryReplayTarget
	minTeamMatches := scope.Discovery.MinimumTeamMatches

	scopeTeams := map[string]bool{}
	for _, t := range scope.Teams {
		scopeTeams[t.TeamID] = true
	}
	teamBelowMin := []string{}
	teamsMeetingMin := uint32(0)
	for t := range scopeTeams {
		if teamMatches[t] < minTeamMatches {
			teamBelowMin = append(teamBelowMin, t)
		} else {
			teamsMeetingMin++
		}
	}
	sort.Strings(teamBelowMin)
	allTeamsCovered := len(teamBelowMin) == 0
	noTeamMeetsMin := teamsMeetingMin == 0

	switch {
	case accessible >= fullTarget && allTeamsCovered && accessible > 0:
		e.Outcome = ReadinessFullHistoryGo
	case accessible == 0 || noTeamMeetsMin:
		// One accessible match (or none) where no team reaches its minimum
		// cannot meet any baseline cell minimum -> no_go, not restricted.
		e.Outcome = ReadinessHistoricalNoGo
		if accessible == 0 {
			e.RestrictedReason = "no_accessible_replays"
		} else {
			e.RestrictedReason = "no_team_meets_minimum_matches"
		}
	default:
		e.Outcome = ReadinessRestrictedGo
		if accessible < fullTarget {
			e.RestrictedReason = "replay_accessible_below_target"
		}
		if len(teamBelowMin) > 0 {
			e.RestrictedReason = "team_below_minimum_matches"
			for _, t := range teamBelowMin {
				e.DisabledFamilies = append(e.DisabledFamilies, "team:"+t)
			}
		}
		if !allTeamsCovered {
			e.RestrictedReason = "not_all_teams_represented"
		}
	}
	if e.RestrictedReason != "" {
		sort.Strings(e.DisabledFamilies)
	}
	return e
}