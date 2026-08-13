package history

import (
	"errors"
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type ParseExecutionEvidence struct {
	SchemaVersion            string `json:"schema_version"`
	ContentSHA256            string `json:"content_sha256"`
	ExecutionID              string `json:"execution_id"`
	MatchID                  string `json:"match_id"`
	ReplaySHA256             string `json:"replay_sha256"`
	FactsSHA256              string `json:"facts_sha256"`
	ParserVersion            string `json:"parser_version"`
	AdapterVersion           string `json:"adapter_version"`
	ConfigSHA256             string `json:"config_sha256"`
	ParsedArtifactSHA256     string `json:"parsed_artifact_sha256"`
	NormalizedArtifactSHA256 string `json:"normalized_artifact_sha256"`
	RunArtifactSHA256        string `json:"run_artifact_sha256"`
	CheckpointSHA256         string `json:"checkpoint_sha256"`
	Deterministic            bool   `json:"deterministic"`
}

func sealParseExecutionEvidence(p *ParseExecutionEvidence) error {
	if p == nil {
		return errors.New("nil parse execution evidence")
	}
	p.ContentSHA256 = ""
	h, err := contentSHA256("parse-execution", *p)
	if err != nil {
		return err
	}
	p.ContentSHA256 = h
	return p.Validate()
}

func (p ParseExecutionEvidence) Validate() error {
	if p.SchemaVersion != "history.parse-execution.v1" || p.ExecutionID == "" || p.MatchID == "" || !isSHA(p.ReplaySHA256) || !isSHA(p.FactsSHA256) || p.ParserVersion == "" || p.AdapterVersion == "" || !isSHA(p.ConfigSHA256) || !isSHA(p.ParsedArtifactSHA256) || !isSHA(p.NormalizedArtifactSHA256) || !isSHA(p.RunArtifactSHA256) || !isSHA(p.CheckpointSHA256) || !p.Deterministic || !isSHA(p.ContentSHA256) {
		return errors.New("invalid parse execution evidence")
	}
	got := p.ContentSHA256
	p.ContentSHA256 = ""
	want, err := contentSHA256("parse-execution", p)
	if err != nil || got != want {
		return errors.New("parse execution evidence identity mismatch")
	}
	return nil
}

type ProcessedReplayEvidence struct {
	MatchID string                   `json:"match_id"`
	Passes  []ParseExecutionEvidence `json:"passes"`
}

type ReadinessInput struct {
	Scope             contracts.TournamentScopeV1
	Roster            RosterManifestV1
	Manifest          DiscoveryManifestV1
	Facts             []NormalizedMatchFacts
	Processed         []ProcessedReplayEvidence
	ValidateExecution func(ParseExecutionEvidence) error
	Batch             StageBatch
	Windows           CutoffWindow
	Patch             PatchWindow
	GeneratedAt       time.Time
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
	if err := ValidateStageBatch(in.Manifest, in.Batch); err != nil {
		return fail("batch_invalid:" + err.Error())
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
		stage, sok := in.Batch.Entries[p.MatchID]
		if !mok || !fok || !sok || stage.Status != StageSucceeded || stage.ReplaySHA256 != m.ReplaySHA256 || stage.FactsSHA256 != f.ContentSHA256 || m.State != MatchReplayAccessible || !consistentParsePasses(in.ValidateExecution, p, m, f) || correlateMatch(m, f) != "" {
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

func consistentParsePasses(validate func(ParseExecutionEvidence) error, p ProcessedReplayEvidence, m DiscoveryMatch, f NormalizedMatchFacts) bool {
	if len(p.Passes) < 2 {
		return false
	}
	if validate == nil {
		return false
	}
	ids, receipts := map[string]bool{}, map[string]bool{}
	parsedArtifacts, normalizedArtifacts := map[string]bool{}, map[string]bool{}
	runArtifacts, checkpoints := map[string]bool{}, map[string]bool{}
	parserVersion, adapterVersion, configSHA := "", "", ""
	for _, pass := range p.Passes {
		if validate(pass) != nil || pass.MatchID != p.MatchID || pass.MatchID != f.MatchID || pass.ReplaySHA256 != m.ReplaySHA256 || pass.ReplaySHA256 != f.ReplaySHA256 || pass.FactsSHA256 != f.ContentSHA256 || ids[pass.ExecutionID] || receipts[pass.ContentSHA256] || parsedArtifacts[pass.ParsedArtifactSHA256] || normalizedArtifacts[pass.NormalizedArtifactSHA256] || runArtifacts[pass.RunArtifactSHA256] || checkpoints[pass.CheckpointSHA256] {
			return false
		}
		if parserVersion == "" {
			parserVersion, adapterVersion, configSHA = pass.ParserVersion, pass.AdapterVersion, pass.ConfigSHA256
		} else if pass.ParserVersion != parserVersion || pass.AdapterVersion != adapterVersion || pass.ConfigSHA256 != configSHA {
			return false
		}
		ids[pass.ExecutionID], receipts[pass.ContentSHA256] = true, true
		parsedArtifacts[pass.ParsedArtifactSHA256] = true
		normalizedArtifacts[pass.NormalizedArtifactSHA256] = true
		runArtifacts[pass.RunArtifactSHA256] = true
		checkpoints[pass.CheckpointSHA256] = true
	}
	return true
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
