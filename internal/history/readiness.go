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
//     replays, all 16 teams represented by at least five matches, and every
//     enabled baseline cell meeting its rule's published minimum;
//   - restricted_history_go is an explicit reviewed scope revision when fewer
//     than 100 replays or a team has fewer than five; historical rules are
//     disabled per uncovered team/cell;
//   - historical_no_go applies when no historical rule family can meet its
//     published minimum.
//
// The gate cannot return a fourth state.
func ReadinessGate(scope contracts.TournamentScopeV1, manifest DiscoveryManifestV1) ReadinessEvidence {
	_ = scope.Validate()
	e := ReadinessEvidence{
		SchemaVersion:     ReadinessSchema,
		TournamentScopeID: scope.ScopeID,
		TeamMatchCount:    map[string]uint32{},
		PerStateCounts:    map[string]uint32{},
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

	// Match every scope team to its accessible match count (0 when absent)
	// so under-represented and absent teams are disabled per cell/family.
	scopeTeams := map[string]bool{}
	for _, t := range scope.Teams {
		scopeTeams[t.TeamID] = true
	}
	teamBelowMin := []string{}
	for t := range scopeTeams {
		if teamMatches[t] < minTeamMatches {
			teamBelowMin = append(teamBelowMin, t)
		}
	}
	sort.Strings(teamBelowMin)
	allTeamsCovered := len(teamBelowMin) == 0

	switch {
	case accessible >= fullTarget && allTeamsCovered:
		e.Outcome = ReadinessFullHistoryGo
	case accessible == 0:
		e.Outcome = ReadinessHistoricalNoGo
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