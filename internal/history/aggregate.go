package history

import (
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// CellKind classifies a baseline cell so the correct minimum sample is
// enforced. The program spec fixes: draft cells >=5 eligible matches, lane/item
// distribution cells >=8 eligible observations, team comparative cells >=10.
type CellKind string

const (
	CellDraft           CellKind = "draft"
	CellDistribution    CellKind = "distribution"
	CellTeamComparative CellKind = "team_comparative"
)

func minForKind(k CellKind) uint64 {
	switch k {
	case CellDraft:
		return MinDraftCell
	case CellDistribution:
		return MinLaneItemCell
	case CellTeamComparative:
		return MinTeamCompCell
	}
	return 0
}

// AggregateInput is the pure, deterministic input to aggregation: a set of
// sealed normalized facts, the roster they bind to, the window definitions,
// and the active match id that must always be excluded. There is no ambient
// clock; period boundaries come from the caller.
type AggregateInput struct {
	Facts         []NormalizedMatchFacts
	Roster        RosterManifestV1
	Windows       CutoffWindow
	Patch         PatchWindow
	ActiveMatchID string
	GeneratedAt   time.Time
}

// BaselineCell is an aggregation result before it is sealed into a
// HistoricalBaselineV1. It carries the contract key and the contract value.
type BaselineCell struct {
	Key   contracts.HistoricalBaselineKeyV1
	Value contracts.HistoricalValueV1
}

type aggKey struct {
	rosterID, playerID, role, heroID, patch, metric, window, sampleDef string
}

type aggGroup struct {
	kind            CellKind
	sum             int64 // sum of present scalar observations (means)
	count           int64 // count of present scalar observations
	eligibleMatches map[string]bool
	presentMatches  map[string]bool
	winCount        int64 // matches won by the entity (player/hero/team)
	buckets         map[string]*bucketAgg
}

type bucketAgg struct {
	sum         int64
	count       int64
	eligibleObs int64
	presentObs  int64
}

// Aggregate produces the complete set of baseline cells across all dimensions
// (player, team, role, hero, player-hero, patch) and windows. Every cell
// exposes sample size, period, source coverage, and missingness. Cells below
// the minimum sample are emitted with State=absent (unavailable), not omitted,
// so coverage is reported rather than fabricated.
//
// Only facts with verified identity and a cutoff-eligible, non-active match
// contribute. Quarantined facts contribute nothing; their absence is recorded
// as missingness, never as a fabricated value.
func Aggregate(in AggregateInput) ([]BaselineCell, error) {
	if err := in.Roster.Validate(); err != nil {
		return nil, err
	}
	rosterByID := map[string]RosterPlayer{}
	for _, p := range in.Roster.Players {
		rosterByID[p.PersonID] = p
	}
	teamRoster := map[string]string{}
	for _, t := range in.Roster.Teams {
		teamRoster[t.TeamID] = t.RosterID
	}
	groups := map[aggKey]*aggGroup{}
	ensure := func(k aggKey, kind CellKind) *aggGroup {
		g, ok := groups[k]
		if !ok {
			g = &aggGroup{kind: kind, eligibleMatches: map[string]bool{}, presentMatches: map[string]bool{}, buckets: map[string]*bucketAgg{}}
			groups[k] = g
		}
		return g
	}
	mark := func(g *aggGroup, matchID string, present bool) {
		g.eligibleMatches[matchID] = true
		if present {
			g.presentMatches[matchID] = true
		}
	}
	addScalar := func(g *aggGroup, matchID string, val *int64) {
		mark(g, matchID, val != nil)
		if val != nil {
			g.sum += *val
			g.count++
		}
	}
	addDec := func(g *aggGroup, matchID string, v contracts.Decimal) {
		if !v.Valid() {
			mark(g, matchID, false)
			return
		}
		mark(g, matchID, true)
		if num, ok := parseDec(v); ok {
			g.sum += num
			g.count++
		}
	}

	for _, f := range in.Facts {
		if err := f.Validate(); err != nil {
			return nil, err
		}
		if f.IdentityStatus != contracts.IdentityVerified {
			continue
		}
		if f.MatchID == in.ActiveMatchID {
			continue
		}
		if f.SourceEventTime.IsZero() || f.SourceEventTime.After(in.Windows.HistoryCutoff) {
			continue
		}
		radiantWin := false
		if f.RadiantWin != nil {
			radiantWin = *f.RadiantWin
		}
for _, w := range AllWindows() {
				if !in.Windows.Includes(w, f.SourceEventTime) {
					continue
				}
				if w == WindowCurrentPatch && f.PatchID != in.Patch.PatchID {
					continue
				}
				teamsInMatch := map[string]string{}
				for _, p := range f.Participants {
				rosterID := teamRoster[p.TeamID]
				if rosterID == "" {
					continue
				}
				teamsInMatch[p.TeamID] = rosterID
				role := p.Role
				if rp, ok := rosterByID[p.PersonID]; ok && role == "" {
					role = rp.Role
				}
				hid := p.HeroName
				pid := p.PersonID
				won := (p.TeamID == f.RadiantTeamID && radiantWin) || (p.TeamID == f.DireTeamID && !radiantWin)

				// Count cell: games (matches the entity participated in).
				emitCount(ensure, mark, aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricGames, string(w), "player.scalar.games"}, f.MatchID, true)
				if hid != "" {
					emitCount(ensure, mark, aggKey{rosterID, "", "", hid, in.Patch.PatchID, MetricGames, string(w), "hero.scalar.games"}, f.MatchID, true)
				}
				if role != "" {
					emitCount(ensure, mark, aggKey{rosterID, "", role, "", in.Patch.PatchID, MetricGames, string(w), "role.scalar.games"}, f.MatchID, true)
				}
				// Count cell: wins (matches the entity's team won).
				emitCount(ensure, mark, aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricWins, string(w), "player.scalar.wins"}, f.MatchID, won)
				if hid != "" {
					emitCount(ensure, mark, aggKey{rosterID, "", "", hid, in.Patch.PatchID, MetricWins, string(w), "hero.scalar.wins"}, f.MatchID, won)
				}

				// Scalar mean metrics: kills, deaths, assists, gpm, xpm,
				// last_hits, denies, level, net_worth (decimal).
				for _, m := range []string{MetricKills, MetricDeaths, MetricAssists, MetricGPM, MetricXPM, MetricLastHits, MetricDenies, MetricLevel} {
					v := scalarPtr(p, m)
					addScalar(ensure(aggKey{rosterID, pid, "", "", in.Patch.PatchID, m, string(w), "player.scalar." + m}, CellDraft), f.MatchID, v)
					if hid != "" {
						addScalar(ensure(aggKey{rosterID, "", "", hid, in.Patch.PatchID, m, string(w), "hero.scalar." + m}, CellDraft), f.MatchID, v)
					}
					if role != "" {
						addScalar(ensure(aggKey{rosterID, "", role, "", in.Patch.PatchID, m, string(w), "role.scalar." + m}, CellDraft), f.MatchID, v)
					}
				}
				addDec(ensure(aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricNetWorth, string(w), "player.scalar." + MetricNetWorth}, CellDraft), f.MatchID, decOrEmpty(p.NetWorth))
				// kill participation (derived, decimal ratio expressed as integer sum scaled by 100).
				if p.Kills != nil && p.Assists != nil {
					kp := killParticipationValue(p, participantsOf(f, p.TeamID))
					if kp.Valid() {
						addDec(ensure(aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricKillParticipation, string(w), "player.scalar." + MetricKillParticipation}, CellDraft), f.MatchID, kp)
					} else {
						mark(ensure(aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricKillParticipation, string(w), "player.scalar." + MetricKillParticipation}, CellDraft), f.MatchID, false)
					}
				} else {
					mark(ensure(aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricKillParticipation, string(w), "player.scalar." + MetricKillParticipation}, CellDraft), f.MatchID, false)
				}

				// Per-hero cells for the same scalar metrics (player-hero
				// dimension) so draft player/hero cross-cells exist.
				if hid != "" {
					for _, m := range []string{MetricKills, MetricDeaths, MetricAssists, MetricGPM} {
						v := scalarPtr(p, m)
						addScalar(ensure(aggKey{rosterID, pid, "", hid, in.Patch.PatchID, m, string(w), "player_hero.scalar." + m}, CellDraft), f.MatchID, v)
					}
				}

				// Distribution cells: farm checkpoints (lane) and key item
				// timings. Each bucket is one observation; the cell minimum
				// applies per bucket.
				emitBuckets(ensure, mark, f.MatchID, aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricFarmCheckpoint, string(w), "player.distribution." + MetricFarmCheckpoint}, CellDistribution, copyMap(p.FarmCheckpoints))
				emitBuckets(ensure, mark, f.MatchID, aggKey{rosterID, pid, "", "", in.Patch.PatchID, MetricKeyItemTiming, string(w), "player.distribution." + MetricKeyItemTiming}, CellDistribution, itemTimingsMap(p))
			}

			// Team-level match counts and player-aggregate means.
			for teamID, rosterID := range teamsInMatch {
				won := (teamID == f.RadiantTeamID && radiantWin) || (teamID == f.DireTeamID && !radiantWin)
				emitCount(ensure, mark, aggKey{rosterID, "", "", "", in.Patch.PatchID, MetricGames, string(w), "team.scalar.games"}, f.MatchID, true)
				emitCount(ensure, mark, aggKey{rosterID, "", "", "", in.Patch.PatchID, MetricWins, string(w), "team.scalar.wins"}, f.MatchID, won)
				for _, m := range []string{MetricGPM, MetricXPM, MetricKills, MetricDeaths, MetricAssists} {
					gm := ensure(aggKey{rosterID, "", "", "", in.Patch.PatchID, m, string(w), "team.scalar." + m}, CellTeamComparative)
					var teamPresent bool
					for _, p := range f.Participants {
						if p.TeamID != teamID {
							continue
						}
						v := scalarPtr(p, m)
						if v != nil {
							gm.sum += *v
							gm.count++
							teamPresent = true
						}
					}
					mark(gm, f.MatchID, teamPresent)
				}
			}

			// Patch is encoded as Key.Patch on every emitted cell, so patch is
			// already a reproducible aggregate dimension; no empty-roster patch
			// cell is emitted (the contract baseline key requires a RosterID).
			_ = teamsInMatch
		}
	}

	cells := buildCells(groups, in)
	sort.Slice(cells, func(i, j int) bool { return cellLess(cells[i].Key, cells[j].Key) })
	return cells, nil
}

func emitCount(ensure func(aggKey, CellKind) *aggGroup, mark func(*aggGroup, string, bool), k aggKey, matchID string, present bool) {
	kind := CellDraft
	if k.sampleDef == "team.scalar.games" || k.sampleDef == "team.scalar.wins" || k.sampleDef == "patch.scalar.games" {
		kind = CellTeamComparative
	}
	g := ensure(k, kind)
	mark(g, matchID, present)
}

func emitBuckets(ensure func(aggKey, CellKind) *aggGroup, mark func(*aggGroup, string, bool), matchID string, k aggKey, kind CellKind, buckets map[string]string) {
	g := ensure(k, kind)
	any := false
	for bucket, val := range buckets {
		b := g.buckets[bucket]
		if b == nil {
			b = &bucketAgg{}
			g.buckets[bucket] = b
		}
		b.eligibleObs++
		if num, ok := parseDecString(val); ok {
			b.sum += num
			b.count++
			b.presentObs++
			any = true
		}
	}
	mark(g, matchID, any)
}

func buildCells(groups map[aggKey]*aggGroup, in AggregateInput) []BaselineCell {
	var out []BaselineCell
	for k, g := range groups {
		periodStart, periodEnd := windowBounds(in.Windows, Window(k.window))
		eligible := uint64(len(g.eligibleMatches))
		present := uint64(len(g.presentMatches))
		coveragePPM := uint32(0)
		if eligible > 0 {
			coveragePPM = uint32((present * 1_000_000) / eligible)
		}
		min := minForKind(g.kind)
		if len(g.buckets) > 0 {
			for bucket, b := range g.buckets {
				val := meanFromInt(b.sum, b.count)
				out = append(out, makeCell(k, periodStart, periodEnd, eligible, uint64(b.presentObs), coveragePPM, val, g.kind, min, bucket))
			}
			continue
		}
		// Count metrics (games/wins) publish the present count as the value;
		// mean metrics publish the mean of present observations.
		var val contracts.Decimal
		if k.metric == MetricGames || k.metric == MetricWins {
			val = contracts.Decimal(decimalFromInt(int64(present)))
		} else {
			val = meanFromInt(g.sum, g.count)
		}
		out = append(out, makeCell(k, periodStart, periodEnd, eligible, present, coveragePPM, val, g.kind, min, ""))
	}
	return out
}

func makeCell(k aggKey, start, end time.Time, eligible, present uint64, coveragePPM uint32, mean contracts.Decimal, kind CellKind, min uint64, bucket string) BaselineCell {
	key := contracts.HistoricalBaselineKeyV1{
		RosterID:         k.rosterID,
		PlayerID:         k.playerID,
		Role:             k.role,
		HeroID:           k.heroID,
		Patch:            k.patch,
		Metric:           k.metric,
		Window:           k.window,
		SampleDefinition: sampleDefinition(k.sampleDef, bucket),
	}
	val := contracts.HistoricalValueV1{
		PeriodStart:       start,
		PeriodEnd:         end,
		SampleSize:        present,
		SourceCoveragePPM: coveragePPM,
	}
	if present >= min && present > 0 && mean.Valid() {
		val.State = contracts.ValuePresent
		v := mean
		val.Value = &v
	} else {
		val.State = contracts.ValueAbsent
	}
	return BaselineCell{Key: key, Value: val}
}

func sampleDefinition(base, bucket string) string {
	if bucket == "" {
		return base
	}
	return base + ":" + bucket
}

// meanFromInt returns a fixed-scale (2 decimals, truncated) mean. Mean is
// floor(sum*100/count)/100. A 2-decimal fixed scale keeps the value lossless
// and deterministic for the canonical encoder.
func meanFromInt(sum, count int64) contracts.Decimal {
	if count <= 0 {
		return ""
	}
	return formatScaled(sum * 100 / count)
}

func formatScaled(scaled int64) contracts.Decimal {
	neg := scaled < 0
	if neg {
		scaled = -scaled
	}
	whole := scaled / 100
	frac := scaled % 100
	s := decimalFromInt(whole) + "." + twoDigits(frac)
	if neg {
		s = "-" + s
	}
	return contracts.Decimal(s)
}

func twoDigits(v int64) string {
	if v < 10 {
		return "0" + decimalFromInt(v)
	}
	return decimalFromInt(v)
}

func windowBounds(w CutoffWindow, window Window) (time.Time, time.Time) {
	switch window {
	case WindowCurrentPatch:
		return w.CurrentPatchStart, w.HistoryCutoff
	case WindowTrailing90:
		return w.Trailing90Start, w.HistoryCutoff
	case WindowTrailing180:
		return w.Trailing180Start, w.HistoryCutoff
	}
	return time.Time{}, w.HistoryCutoff
}

func scalarPtr(p ParticipantFacts, metric string) *int64 {
	switch metric {
	case MetricKills:
		return p.Kills
	case MetricDeaths:
		return p.Deaths
	case MetricAssists:
		return p.Assists
	case MetricGPM:
		return p.GPM
	case MetricXPM:
		return p.XPM
	case MetricLastHits:
		return p.LastHits
	case MetricDenies:
		return p.Denies
	case MetricLevel:
		return p.Level
	}
	return nil
}

func participantsOf(f NormalizedMatchFacts, teamID string) []ParticipantFacts {
	out := []ParticipantFacts{}
	for _, p := range f.Participants {
		if p.TeamID == teamID {
			out = append(out, p)
		}
	}
	return out
}

func killParticipationValue(p ParticipantFacts, team []ParticipantFacts) contracts.Decimal {
	if p.Kills == nil || p.Assists == nil {
		return ""
	}
	var teamKills int64
	complete := true
	for _, tp := range team {
		if tp.Kills == nil {
			complete = false
			break
		}
		teamKills += *tp.Kills
	}
	if !complete || teamKills == 0 {
		return ""
	}
	// KP = (kills+assists)/teamKills. Express as a 2-decimal ratio (percent*100
	// is integer; we store the ratio itself, e.g. "0.42").
	num := (*p.Kills + *p.Assists) * 10000 / teamKills
	return formatScaled(num)
}

func itemTimingsMap(p ParticipantFacts) map[string]string {
	out := map[string]string{}
	for k, v := range p.KeyItemTimings {
		out[k] = decimalFromInt(v)
	}
	return out
}

func copyMap(in map[int64]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[decimalFromInt(k)] = v
	}
	return out
}

func decOrEmpty(s *string) contracts.Decimal {
	if s == nil {
		return ""
	}
	return contracts.Decimal(*s)
}

func parseDec(d contracts.Decimal) (int64, bool) { return parseDecString(string(d)) }

func parseDecString(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	var whole int64
	for ; i < len(s) && s[i] != '.'; i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		whole = whole*10 + int64(s[i]-'0')
	}
	var frac int64
	if i < len(s) && s[i] == '.' {
		i++
		for j := 0; i < len(s) && j < 2; j++ {
			if s[i] < '0' || s[i] > '9' {
				return 0, false
			}
			frac = frac*10 + int64(s[i]-'0')
			i++
		}
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	total := whole*100 + frac
	if neg {
		total = -total
	}
	return total, true
}

func cellLess(a, b contracts.HistoricalBaselineKeyV1) bool {
	if a.RosterID != b.RosterID {
		return a.RosterID < b.RosterID
	}
	if a.PlayerID != b.PlayerID {
		return a.PlayerID < b.PlayerID
	}
	if a.Role != b.Role {
		return a.Role < b.Role
	}
	if a.HeroID != b.HeroID {
		return a.HeroID < b.HeroID
	}
	if a.Patch != b.Patch {
		return a.Patch < b.Patch
	}
	if a.Metric != b.Metric {
		return a.Metric < b.Metric
	}
	if a.Window != b.Window {
		return a.Window < b.Window
	}
	return a.SampleDefinition < b.SampleDefinition
}