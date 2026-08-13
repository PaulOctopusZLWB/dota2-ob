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
	dec             bool // true once a decimal (cent-scaled) value is recorded; never mixed with int scalars in one group
	sum             int64 // int scalars: integer units; decimals: cent units
	count           int64 // count of present scalar observations
	eligibleMatches map[string]bool
	presentMatches  map[string]bool
	winCount        int64 // matches won by the entity's team (RadiantWin known)
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
		g.dec = true // decimal values are stored cent-scaled; the mean divides once, not twice
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
		// An unknown win outcome is a missing observation, not a Dire win: wins are
	// only accumulated when RadiantWin is known, and a wins cell is published
	// only when every eligible game contributes a known result.
	winKnown := f.RadiantWin != nil
	radiantWin := false
	if winKnown {
		radiantWin = *f.RadiantWin
	}
	curPatch := in.Patch.PatchID
	effectiveMember := func(personID, teamID string, at time.Time) bool {
		rp, ok := rosterByID[personID]
		if !ok || rp.TeamID != teamID {
			return false
		}
		return !at.Before(rp.EffectiveFrom) && at.Before(rp.EffectiveUntil)
	}
	for _, w := range AllWindows() {
		if !in.Windows.Includes(w, f.SourceEventTime) {
			continue
		}
		patchKey := f.PatchID
		if w == WindowCurrentPatch && patchKey != curPatch {
			continue
		}
		teamsInMatch := map[string]string{}
		for _, p := range f.Participants {
			rosterID := teamRoster[p.TeamID]
			if rosterID == "" {
				continue
			}
			if !effectiveMember(p.PersonID, p.TeamID, f.SourceEventTime) {
				continue
			}
			teamsInMatch[p.TeamID] = rosterID
			role := p.Role
			if rp, ok := rosterByID[p.PersonID]; ok && role == "" {
				role = rp.Role
			}
			hid := p.HeroName
			pid := p.PersonID
			won := winKnown && ((p.TeamID == f.RadiantTeamID && radiantWin) || (p.TeamID == f.DireTeamID && !radiantWin))

			emitCount(ensure, mark, aggKey{rosterID, pid, "", "", patchKey, MetricGames, string(w), "player.scalar.games"}, f.MatchID, true)
			if hid != "" {
				emitCount(ensure, mark, aggKey{rosterID, "", "", hid, patchKey, MetricGames, string(w), "hero.scalar.games"}, f.MatchID, true)
			}
			if role != "" {
				emitCount(ensure, mark, aggKey{rosterID, "", role, "", patchKey, MetricGames, string(w), "role.scalar.games"}, f.MatchID, true)
			}
			emitWins(ensure, winKnown, won, aggKey{rosterID, pid, "", "", patchKey, MetricWins, string(w), "player.scalar.wins"}, f.MatchID, "player.scalar.wins")
			if hid != "" {
				emitWins(ensure, winKnown, won, aggKey{rosterID, "", "", hid, patchKey, MetricWins, string(w), "hero.scalar.wins"}, f.MatchID, "hero.scalar.wins")
			}

			for _, m := range []string{MetricKills, MetricDeaths, MetricAssists, MetricGPM, MetricXPM, MetricLastHits, MetricDenies, MetricLevel} {
				v := scalarPtr(p, m)
				addScalar(ensure(aggKey{rosterID, pid, "", "", patchKey, m, string(w), "player.scalar." + m}, CellDraft), f.MatchID, v)
				if hid != "" {
					addScalar(ensure(aggKey{rosterID, "", "", hid, patchKey, m, string(w), "hero.scalar." + m}, CellDraft), f.MatchID, v)
				}
				if role != "" {
					addScalar(ensure(aggKey{rosterID, "", role, "", patchKey, m, string(w), "role.scalar." + m}, CellDraft), f.MatchID, v)
				}
			}
			addDec(ensure(aggKey{rosterID, pid, "", "", patchKey, MetricNetWorth, string(w), "player.scalar." + MetricNetWorth}, CellDraft), f.MatchID, decOrEmpty(p.NetWorth))
			if p.Kills != nil && p.Assists != nil {
				kp := killParticipationValue(p, participantsOf(f, p.TeamID))
				g := ensure(aggKey{rosterID, pid, "", "", patchKey, MetricKillParticipation, string(w), "player.scalar." + MetricKillParticipation}, CellDraft)
				if kp.Valid() {
					addDec(g, f.MatchID, kp)
				} else {
					mark(g, f.MatchID, false)
				}
			} else {
				mark(ensure(aggKey{rosterID, pid, "", "", patchKey, MetricKillParticipation, string(w), "player.scalar." + MetricKillParticipation}, CellDraft), f.MatchID, false)
			}

			if hid != "" {
				for _, m := range []string{MetricKills, MetricDeaths, MetricAssists, MetricGPM} {
					v := scalarPtr(p, m)
					addScalar(ensure(aggKey{rosterID, pid, "", hid, patchKey, m, string(w), "player_hero.scalar." + m}, CellDraft), f.MatchID, v)
				}
			}

			emitBuckets(ensure, mark, f.MatchID, aggKey{rosterID, pid, "", "", patchKey, MetricFarmCheckpoint, string(w), "player.distribution." + MetricFarmCheckpoint}, CellDistribution, copyMap(p.FarmCheckpoints))
			emitBuckets(ensure, mark, f.MatchID, aggKey{rosterID, pid, "", "", patchKey, MetricKeyItemTiming, string(w), "player.distribution." + MetricKeyItemTiming}, CellDistribution, itemTimingsMap(p))
		}

		for teamID, rosterID := range teamsInMatch {
			won := winKnown && ((teamID == f.RadiantTeamID && radiantWin) || (teamID == f.DireTeamID && !radiantWin))
			emitCount(ensure, mark, aggKey{rosterID, "", "", "", patchKey, MetricGames, string(w), "team.scalar.games"}, f.MatchID, true)
			emitWins(ensure, winKnown, won, aggKey{rosterID, "", "", "", patchKey, MetricWins, string(w), "team.scalar.wins"}, f.MatchID, "team.scalar.wins")
			for _, m := range []string{MetricGPM, MetricXPM, MetricKills, MetricDeaths, MetricAssists} {
				gm := ensure(aggKey{rosterID, "", "", "", patchKey, m, string(w), "team.scalar." + m}, CellTeamComparative)
				var teamPresent bool
				for _, p := range f.Participants {
					if p.TeamID != teamID || !effectiveMember(p.PersonID, p.TeamID, f.SourceEventTime) {
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
			b.sum += num // cent-scaled; mean divides once via meanDec
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
				pobs := uint64(b.presentObs)
				val := meanDec(b.sum, b.count)
				out = append(out, makeCell(k, periodStart, periodEnd, pobs, coveragePPM, val, pobs >= min && pobs > 0 && val.Valid(), bucket))
			}
			continue
		}
		switch k.metric {
		case MetricGames:
			val := contracts.Decimal(decimalFromInt(int64(eligible)))
			out = append(out, makeCell(k, periodStart, periodEnd, eligible, coveragePPM, val, eligible >= min && eligible > 0 && val.Valid(), ""))
		case MetricWins:
			val := contracts.Decimal(decimalFromInt(int64(g.winCount)))
			out = append(out, makeCell(k, periodStart, periodEnd, eligible, coveragePPM, val, eligible >= min && present == eligible && eligible > 0 && val.Valid(), ""))
		default:
			var val contracts.Decimal
			if g.dec {
				val = meanDec(g.sum, g.count)
			} else {
				val = meanFromInt(g.sum, g.count)
			}
			out = append(out, makeCell(k, periodStart, periodEnd, present, coveragePPM, val, present >= min && present > 0 && val.Valid(), ""))
		}
	}
	return out
}

func makeCell(k aggKey, start, end time.Time, sampleSize uint64, coveragePPM uint32, val contracts.Decimal, statePresent bool, bucket string) BaselineCell {
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
	out := contracts.HistoricalValueV1{
		PeriodStart:       start,
		PeriodEnd:         end,
		SampleSize:        sampleSize,
		SourceCoveragePPM: coveragePPM,
	}
	if statePresent {
		out.State = contracts.ValuePresent
		v := val
		out.Value = &v
	} else {
		out.State = contracts.ValueAbsent
	}
	return BaselineCell{Key: key, Value: out}
}

// meanDec returns the mean of cent-scaled decimal accumulations as a 2-decimal
// value. Decimal sums (net_worth, kill-participation, distribution buckets)
// are already in cents, so the mean divides once — not the integer mean path
// (meanFromInt), which multiplies integer-unit sums by 100.
func meanDec(sum, count int64) contracts.Decimal {
	if count <= 0 {
		return ""
	}
	return formatScaled(sum / count)
}

// emitWinsEmit accumulates wins for an entity. eligibleMatches (games) is the
// sample/coverage denominator; an unknown RadiantWin records a missing
// observation (present only when the result is known), never a fabricated Dire win.
func emitWins(ensure func(aggKey, CellKind) *aggGroup, winKnown, won bool, k aggKey, matchID string, _ string) {
	kind := CellDraft
	if k.sampleDef == "team.scalar.wins" || k.sampleDef == "patch.scalar.games" || k.sampleDef == "team.scalar.games" {
		kind = CellTeamComparative
	}
	g := ensure(k, kind)
	g.eligibleMatches[matchID] = true
	if winKnown {
		g.presentMatches[matchID] = true
		if won {
			g.winCount++
		}
	}
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
	// KP = (kills+assists)/teamKills expressed as a 2-decimal ratio in [0,1]
	// (e.g. "0.42"), not a percent. The numerator is scaled to cents so
	// meanDec divides once rather than double-scaling through meanFromInt.
	num := (*p.Kills + *p.Assists) * 100 / teamKills
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