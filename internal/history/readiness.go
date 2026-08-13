package history

import (
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type ProcessedReplayEvidence struct {
	MatchID               string `json:"match_id"`
	ReplaySHA256          string `json:"replay_sha256"`
	FactsSHA256           string `json:"facts_sha256"`
	SuccessfulParsePasses uint32 `json:"successful_parse_passes"`
}

type ReadinessInput struct {
	Scope       contracts.TournamentScopeV1
	Roster      RosterManifestV1
	Manifest    DiscoveryManifestV1
	Facts       []NormalizedMatchFacts
	Processed   []ProcessedReplayEvidence
	Windows     CutoffWindow
	Patch       PatchWindow
	GeneratedAt time.Time
}

type ReadinessEvidence struct {
	SchemaVersion            string            `json:"schema_version"`
	Outcome                  string            `json:"outcome"`
	TournamentScopeID        string            `json:"tournament_scope_id"`
	ReplayAccessibleTotal    uint32            `json:"replay_accessible_total"`
	RepeatablyProcessedTotal uint32            `json:"repeatably_processed_total"`
	EnabledCellTotal         uint32            `json:"enabled_cell_total"`
	TeamsRepresented         []string          `json:"teams_represented"`
	TeamMatchCount           map[string]uint32 `json:"team_match_count"`
	PerStateCounts           map[string]uint32 `json:"per_state_counts"`
	DisabledFamilies         []string          `json:"disabled_families,omitempty"`
	DisabledCells            []string          `json:"disabled_cells,omitempty"`
	RestrictedReason         string            `json:"restricted_reason,omitempty"`
}

// ReadinessGate derives readiness only from the sealed scope/roster/discovery
// chain, facts that correlate with accessible manifest entries, explicit proof
// of at least two successful parses, and freshly recomputed aggregate cells.
func ReadinessGate(in ReadinessInput) ReadinessEvidence {
	e := ReadinessEvidence{SchemaVersion: ReadinessSchema, TournamentScopeID: in.Scope.ScopeID, TeamMatchCount: map[string]uint32{}, PerStateCounts: map[string]uint32{}}
	fail := func(reason string) ReadinessEvidence {
		e.Outcome = ReadinessHistoricalNoGo
		e.RestrictedReason = reason
		return e
	}
	if err := in.Scope.Validate(); err != nil {
		return fail("scope_invalid:" + err.Error())
	}
	if err := in.Roster.ValidateAgainstScope(in.Scope); err != nil {
		return fail("roster_invalid:" + err.Error())
	}
	if err := in.Manifest.ValidateAgainstScope(in.Scope, in.Roster); err != nil {
		return fail("manifest_invalid:" + err.Error())
	}

	manifestByID := map[string]DiscoveryMatch{}
	for _, m := range in.Manifest.Matches {
		e.PerStateCounts[m.State]++
		manifestByID[m.MatchID] = m
		if m.State == MatchReplayAccessible {
			e.ReplayAccessibleTotal++
		}
	}
	factsByID := map[string]NormalizedMatchFacts{}
	for _, f := range in.Facts {
		if err := f.Validate(); err != nil {
			return fail("facts_invalid:" + f.MatchID)
		}
		if _, exists := factsByID[f.MatchID]; exists {
			return fail("duplicate_facts:" + f.MatchID)
		}
		factsByID[f.MatchID] = f
	}

	processedSeen := map[string]bool{}
	var eligible []NormalizedMatchFacts
	eligibleMatches := map[string]DiscoveryMatch{}
	for _, p := range in.Processed {
		if processedSeen[p.MatchID] {
			return fail("duplicate_processed_evidence:" + p.MatchID)
		}
		processedSeen[p.MatchID] = true
		m, mok := manifestByID[p.MatchID]
		f, fok := factsByID[p.MatchID]
		if !mok || !fok || m.State != MatchReplayAccessible || p.SuccessfulParsePasses < 2 || p.ReplaySHA256 != m.ReplaySHA256 || p.ReplaySHA256 != f.ReplaySHA256 || p.FactsSHA256 != f.ContentSHA256 || correlateMatch(m, f) != "" {
			continue
		}
		eligible = append(eligible, f)
		eligibleMatches[p.MatchID] = m
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].MatchID < eligible[j].MatchID })
	e.RepeatablyProcessedTotal = uint32(len(eligible))

	teamSet := map[string]bool{}
	for _, m := range eligibleMatches {
		for _, team := range []string{m.RadiantTeamID, m.DireTeamID} {
			teamSet[team] = true
			e.TeamMatchCount[team]++
		}
	}
	for team := range teamSet {
		e.TeamsRepresented = append(e.TeamsRepresented, team)
	}
	sort.Strings(e.TeamsRepresented)

	if len(eligible) == 0 {
		return fail("no_repeatably_processed_replays")
	}
	cells, err := Aggregate(AggregateInput{Facts: eligible, Roster: in.Roster, Windows: in.Windows, Patch: in.Patch, GeneratedAt: in.GeneratedAt})
	if err != nil {
		return fail("aggregate_invalid:" + err.Error())
	}
	allEnabledMeetMin := true
	for _, c := range cells {
		id := readinessCellID(c.Key)
		if c.Value.State != contracts.ValuePresent {
			e.DisabledCells = append(e.DisabledCells, id)
			continue
		}
		e.EnabledCellTotal++
		if c.Value.SampleSize < readinessCellMinimum(c.Key.SampleDefinition) {
			allEnabledMeetMin = false
		}
	}
	sort.Strings(e.DisabledCells)
	if e.EnabledCellTotal == 0 {
		return fail("no_enabled_baseline_cells")
	}

	below := []string{}
	for _, t := range in.Scope.Teams {
		if e.TeamMatchCount[t.TeamID] < in.Scope.Discovery.MinimumTeamMatches {
			below = append(below, t.TeamID)
			e.DisabledFamilies = append(e.DisabledFamilies, "team:"+t.TeamID)
		}
	}
	sort.Strings(below)
	sort.Strings(e.DisabledFamilies)
	if e.RepeatablyProcessedTotal >= in.Scope.Discovery.FullHistoryReplayTarget && len(below) == 0 && allEnabledMeetMin {
		e.Outcome = ReadinessFullHistoryGo
		return e
	}
	e.Outcome = ReadinessRestrictedGo
	switch {
	case !allEnabledMeetMin:
		e.RestrictedReason = "enabled_cell_below_minimum"
	case e.RepeatablyProcessedTotal < in.Scope.Discovery.FullHistoryReplayTarget:
		e.RestrictedReason = "repeatably_processed_below_target"
	case len(below) > 0:
		e.RestrictedReason = "team_below_minimum_matches"
	}
	return e
}

func readinessCellMinimum(sampleDefinition string) uint64 {
	if len(sampleDefinition) >= len("team.scalar.") && sampleDefinition[:len("team.scalar.")] == "team.scalar." {
		return MinTeamCompCell
	}
	if len(sampleDefinition) >= len("player.distribution.") && sampleDefinition[:len("player.distribution.")] == "player.distribution." {
		return MinLaneItemCell
	}
	return MinDraftCell
}

func readinessCellID(k contracts.HistoricalBaselineKeyV1) string {
	return k.RosterID + "|" + k.PlayerID + "|" + k.Role + "|" + k.HeroID + "|" + k.Patch + "|" + k.Metric + "|" + k.Window + "|" + k.SampleDefinition
}
