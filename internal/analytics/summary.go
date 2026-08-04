package analytics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Snapshot is the bounded live API response for /api/analytics.
type Snapshot struct {
	Status                   string          `json:"status"`
	SessionID                string          `json:"session_id,omitempty"`
	TickCount                uint64          `json:"tick_count"`
	StartedAt                *time.Time      `json:"started_at,omitempty"`
	LastSeenAt               *time.Time      `json:"last_seen_at,omitempty"`
	CompleteTenPlayerFrames  uint64          `json:"complete_ten_player_frames"`
	EventCounts              map[string]int  `json:"event_counts"`
	RecentEvents             []Event         `json:"recent_events"`
	RoshanObservations       []Event         `json:"roshan_observations,omitempty"`
	RoshanObservationsTotal  int             `json:"roshan_observations_total"`
	BuildingDestroyed        []Event         `json:"building_destroyed,omitempty"`
	BuildingDestroyedTotal   int             `json:"building_destroyed_total"`
	WardObservations         []Event         `json:"ward_observations,omitempty"`
	WardObservationsTotal    int             `json:"ward_observations_total"`
	ObservationsTruncated    bool            `json:"observations_truncated,omitempty"`
	WardCoordinateConclusion string          `json:"ward_coordinate_conclusion"`
	LatestPlayerEconomy      []PlayerEconomy `json:"latest_player_economy,omitempty"`
}

// PlayerEconomy is the latest observed per-player economy projection used by
// the summary economy section. Only observed values are populated; missing
// fields stay nil so the summary never fabricates unobserved state.
type PlayerEconomy struct {
	Slot           string   `json:"slot"`
	TeamKey        string   `json:"team_key,omitempty"`
	TeamName       string   `json:"team_name,omitempty"`
	PlayerName     string   `json:"player_name,omitempty"`
	HeroName       string   `json:"hero_name,omitempty"`
	HeroID         *int64   `json:"hero_id,omitempty"`
	NetWorth       *float64 `json:"net_worth,omitempty"`
	Gold           *float64 `json:"gold,omitempty"`
	GoldReliable   *float64 `json:"gold_reliable,omitempty"`
	GoldUnreliable *float64 `json:"gold_unreliable,omitempty"`
	GPM            *int64   `json:"gpm,omitempty"`
	XPM            *int64   `json:"xpm,omitempty"`
	LastHits       *int64   `json:"last_hits,omitempty"`
	Denies         *int64   `json:"denies,omitempty"`
	Kills          *int64   `json:"kills,omitempty"`
	Deaths         *int64   `json:"deaths,omitempty"`
	Assists        *int64   `json:"assists,omitempty"`
}

// Snapshot returns the bounded analytics state for the API.
func (e *Engine) Snapshot(sessionID string) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Observation arrays are bounded by the engine (most recent entries
	// retained). The *_total counters and ObservationsTruncated flag preserve
	// traceability so /api/analytics stays bounded for long sessions even
	// though the full count is reported.
	roshan := cloneEvents(e.roshanObservations)
	buildings := cloneEvents(e.buildingDestroyedEvents)
	wards := cloneEvents(e.wardObservations)

	snap := Snapshot{
		Status:                  "empty",
		SessionID:               sessionID,
		TickCount:               e.tickCount,
		CompleteTenPlayerFrames: e.completeTenPlayerFrames,
		EventCounts:             e.eventCountsSnapshot(),
		RecentEvents:            recentEventSlice(e.events),
		RoshanObservations:      roshan,
		RoshanObservationsTotal: e.roshanObservationsTotal,
		BuildingDestroyed:       buildings,
		BuildingDestroyedTotal:  e.buildingDestroyedTotal,
		WardObservations:        wards,
		WardObservationsTotal:   e.wardObservationsTotal,
		ObservationsTruncated: e.roshanObservationsTotal > len(roshan) ||
			e.buildingDestroyedTotal > len(buildings) ||
			e.wardObservationsTotal > len(wards),
		WardCoordinateConclusion: WardCoordinateConclusion,
		LatestPlayerEconomy:      economyFromTick(e.latestPlayerTick),
	}
	if e.tickCount > 0 {
		snap.Status = "ok"
		started := e.startedAt.UTC()
		last := e.lastSeenAt.UTC()
		snap.StartedAt = &started
		snap.LastSeenAt = &last
	}
	if snap.EventCounts == nil {
		snap.EventCounts = map[string]int{}
	}
	return snap
}

// WriteSummaryFiles writes analytics_summary.json and analytics_summary.md under
// sessionDir from the live engine state. It is called after each accepted live
// snapshot so the on-disk summary reflects the latest derivation.
func WriteSummaryFiles(sessionDir, sessionID string, engine *Engine) error {
	if engine == nil {
		return nil
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	snap := engine.Snapshot(sessionID)
	summaryJSON := buildSummaryJSON(sessionID, nil, nil, snap)
	if err := writeJSON(filepath.Join(sessionDir, "analytics_summary.json"), summaryJSON); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "analytics_summary.md"), []byte(RenderSummary(summaryJSON)), 0o644); err != nil {
		return fmt.Errorf("write analytics summary md: %w", err)
	}
	return nil
}

// WardCoordinateConclusion states that GSI exposes ward counters/cooldowns but
// no ward entity coordinates, so exact ward coordinates cannot be derived.
const WardCoordinateConclusion = "not available from GSI (ward counters and purchase cooldowns observed; no ward entity coordinates exposed)"

// WriteArtifacts writes the four offline analysis artifacts under sessionDir:
// normalized_ticks.jsonl, derived_events.jsonl, analytics_summary.json, and
// analytics_summary.md.
func WriteArtifacts(sessionDir, sessionID string, ticks []NormalizedTick, events []Event, snap Snapshot) error {
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	if err := writeJSONL(filepath.Join(sessionDir, "normalized_ticks.jsonl"), ticks); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(sessionDir, "derived_events.jsonl"), events); err != nil {
		return err
	}
	summaryJSON := buildSummaryJSON(sessionID, ticks, events, snap)
	if err := writeJSON(filepath.Join(sessionDir, "analytics_summary.json"), summaryJSON); err != nil {
		return err
	}
	summaryMD := RenderSummary(summaryJSON)
	if err := os.WriteFile(filepath.Join(sessionDir, "analytics_summary.md"), []byte(summaryMD), 0o644); err != nil {
		return fmt.Errorf("write analytics summary md: %w", err)
	}
	fmt.Println(summaryMD)
	return nil
}

type SummaryJSON struct {
	SessionID                 string          `json:"session_id"`
	TickCount                 int             `json:"tick_count"`
	StartedAt                 *time.Time      `json:"started_at,omitempty"`
	LastSeenAt                *time.Time      `json:"last_seen_at,omitempty"`
	ObservedDurationSeconds   float64         `json:"observed_duration_seconds,omitempty"`
	CompleteTenPlayerFrames   int             `json:"complete_ten_player_frames"`
	EventCounts               map[string]int  `json:"event_counts"`
	EventTotal                int             `json:"event_total"`
	RoshanObservations        []Event         `json:"roshan_observations,omitempty"`
	RoshanObservationsTotal   int             `json:"roshan_observations_total"`
	BuildingObservations      []Event         `json:"building_observations,omitempty"`
	BuildingObservationsTotal int             `json:"building_observations_total"`
	WardObservations          []Event         `json:"ward_observations,omitempty"`
	WardObservationsTotal     int             `json:"ward_observations_total"`
	ObservationsTruncated     bool            `json:"observations_truncated,omitempty"`
	WardCoordinateConclusion  string          `json:"ward_coordinate_conclusion"`
	LatestPlayerEconomy       []PlayerEconomy `json:"latest_player_economy,omitempty"`
}

func buildSummaryJSON(sessionID string, ticks []NormalizedTick, events []Event, snap Snapshot) SummaryJSON {
	// TickCount is authoritative from the live engine snapshot. The live
	// WriteSummaryFiles path passes nil ticks (it does not re-read raw.jsonl),
	// so len(ticks) would always report 0 there; snap.TickCount reflects the
	// actual number of accepted live ticks. For the offline path the two agree.
	tickCount := int(snap.TickCount)
	if tickCount == 0 && len(ticks) > 0 {
		tickCount = len(ticks)
	}
	// Observation arrays: the live path (WriteSummaryFiles, events == nil)
	// carries the bounded snapshot tail plus its totals/truncation flag so the
	// persisted live summary stays bounded like /api/analytics. The offline
	// path (WriteArtifacts, events != nil) reconstructs the full observation
	// slices from the complete derived-event list so analytics_summary.json
	// stays complete for long sessions; derived_events.jsonl is also complete.
	roshan := snap.RoshanObservations
	buildings := snap.BuildingDestroyed
	wards := snap.WardObservations
	roshanTotal := snap.RoshanObservationsTotal
	buildingsTotal := snap.BuildingDestroyedTotal
	wardsTotal := snap.WardObservationsTotal
	truncated := snap.ObservationsTruncated
	if events != nil {
		roshan, buildings, wards = observationEvents(events)
		roshanTotal = len(roshan)
		buildingsTotal = len(buildings)
		wardsTotal = len(wards)
		truncated = false
	}
	s := SummaryJSON{
		SessionID:                 sessionID,
		TickCount:                 tickCount,
		CompleteTenPlayerFrames:   int(snap.CompleteTenPlayerFrames),
		EventCounts:               snap.EventCounts,
		RoshanObservations:        roshan,
		RoshanObservationsTotal:   roshanTotal,
		BuildingObservations:      buildings,
		BuildingObservationsTotal: buildingsTotal,
		WardObservations:          wards,
		WardObservationsTotal:     wardsTotal,
		ObservationsTruncated:     truncated,
		WardCoordinateConclusion:  WardCoordinateConclusion,
		LatestPlayerEconomy:       snap.LatestPlayerEconomy,
	}
	if snap.StartedAt != nil {
		st := *snap.StartedAt
		s.StartedAt = &st
	}
	if snap.LastSeenAt != nil {
		lt := *snap.LastSeenAt
		s.LastSeenAt = &lt
		if s.StartedAt != nil {
			s.ObservedDurationSeconds = s.LastSeenAt.Sub(*s.StartedAt).Seconds()
		}
	}
	for _, n := range s.EventCounts {
		s.EventTotal += n
	}
	if s.EventCounts == nil {
		s.EventCounts = map[string]int{}
	}
	return s
}

// observationEvents partitions a complete derived-event list into the three
// observation categories the summary tracks. Used by the offline summary path
// so analytics_summary.json stays complete for long sessions even though the
// live /api/analytics snapshot bounds the same arrays.
func observationEvents(events []Event) (roshan, buildings, wards []Event) {
	for _, ev := range events {
		switch ev.Type {
		case EventRoshanStateChanged:
			roshan = append(roshan, ev)
		case EventBuildingDestroyed:
			buildings = append(buildings, ev)
		case EventWardCounterChanged, EventWardPurchaseCooldown, EventWardPurchaseStarted:
			wards = append(wards, ev)
		}
	}
	return roshan, buildings, wards
}

// RenderSummary produces the markdown analytics summary required by the spec.
func RenderSummary(s SummaryJSON) string {
	var b strings.Builder
	b.WriteString("# Analytics Summary\n\n")
	b.WriteString(fmt.Sprintf("- Session: %s\n", nonEmpty(s.SessionID, "(none)")))
	b.WriteString(fmt.Sprintf("- Normalized tick count: %d\n", s.TickCount))
	if s.StartedAt != nil {
		b.WriteString(fmt.Sprintf("- Observed start: %s\n", s.StartedAt.Format(time.RFC3339Nano)))
	}
	if s.LastSeenAt != nil {
		b.WriteString(fmt.Sprintf("- Observed last update: %s\n", s.LastSeenAt.Format(time.RFC3339Nano)))
	}
	if s.ObservedDurationSeconds > 0 {
		b.WriteString(fmt.Sprintf("- Observed time range: %.2f seconds\n", s.ObservedDurationSeconds))
	}
	b.WriteString(fmt.Sprintf("- Complete ten-player frames: %d\n", s.CompleteTenPlayerFrames))

	b.WriteString("\n## Event Counts By Type\n\n")
	if len(s.EventCounts) == 0 {
		b.WriteString("- none observed\n")
	} else {
		for _, t := range sortedEventTypes(s.EventCounts) {
			b.WriteString(fmt.Sprintf("- %s: %d\n", t, s.EventCounts[t]))
		}
	}
	b.WriteString(fmt.Sprintf("- total: %d\n", s.EventTotal))

	b.WriteString(renderEconomySummary(s.LatestPlayerEconomy))

	b.WriteString("\n## Objective / Roshan / Building Observations\n\n")
	if s.RoshanObservationsTotal == 0 && s.BuildingObservationsTotal == 0 {
		b.WriteString("- none observed\n")
	} else {
		if n := s.RoshanObservationsTotal; n > 0 {
			b.WriteString(fmt.Sprintf("- Roshan state changes: %d\n", n))
		} else {
			b.WriteString("- Roshan state changes: 0 (state remained stable after first observation)\n")
		}
		if n := s.BuildingObservationsTotal; n > 0 {
			b.WriteString(fmt.Sprintf("- Building destroyed observations: %d\n", n))
		} else {
			b.WriteString("- Building destroyed observations: 0\n")
		}
	}

	b.WriteString("\n## Ward Observations\n\n")
	if s.WardObservationsTotal == 0 {
		b.WriteString("- Ward counter / purchase-cooldown changes: 0\n")
	} else {
		b.WriteString(fmt.Sprintf("- Ward-related events: %d\n", s.WardObservationsTotal))
	}
	b.WriteString(fmt.Sprintf("- Exact ward coordinates: %s\n", s.WardCoordinateConclusion))
	if s.ObservationsTruncated {
		b.WriteString("\n_Note: live observation arrays are truncated to the most recent entries; totals above reflect the full count. Regenerate offline via `--analyze-session` for the complete record._\n")
	}

	return b.String()
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func sortedEventTypes(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for k := range counts {
		out = append(out, k)
	}
	// stable order
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// economyFromTick builds the latest per-player economy projection from a
// normalized tick. It never fabricates values: only fields observed in the
// latest tick are reported. Returns nil if the tick has no players.
func economyFromTick(tick *NormalizedTick) []PlayerEconomy {
	if tick == nil || len(tick.Players) == 0 {
		return nil
	}
	out := make([]PlayerEconomy, 0, len(tick.Players))
	for _, p := range tick.Players {
		out = append(out, PlayerEconomy{
			Slot:           p.Slot,
			TeamKey:        p.TeamKey,
			TeamName:       p.TeamName,
			PlayerName:     p.Name,
			HeroName:       p.HeroName,
			HeroID:         p.HeroID,
			NetWorth:       p.NetWorth,
			Gold:           p.Gold,
			GoldReliable:   p.GoldReliable,
			GoldUnreliable: p.GoldUnreliable,
			GPM:            p.GPM,
			XPM:            p.XPM,
			LastHits:       p.LastHits,
			Denies:         p.Denies,
			Kills:          p.Kills,
			Deaths:         p.Deaths,
			Assists:        p.Assists,
		})
	}
	return out
}

// renderEconomySummary produces the per-team/per-player economy summary section.
// It groups players by their observed team, rolling up team net worth / gold, and
// lists each player's latest observed economy. Only observed values are shown; a
// missing value renders as `-` so the summary never fabricates state.
func renderEconomySummary(players []PlayerEconomy) string {
	if len(players) == 0 {
		return "\n## Economy Summary\n\n- none observed\n"
	}
	// Group players by team while preserving observed team order.
	teamOrder := make([]string, 0, 2)
	teams := make(map[string][]PlayerEconomy)
	for _, p := range players {
		team := p.TeamName
		if team == "" {
			team = p.TeamKey
		}
		if team == "" {
			team = "(unknown)"
		}
		if _, ok := teams[team]; !ok {
			teamOrder = append(teamOrder, team)
		}
		teams[team] = append(teams[team], p)
	}

	var b strings.Builder
	b.WriteString("\n## Economy Summary\n\n")
	b.WriteString("Latest observed per-player economy, grouped by team.\n\n")
	for _, team := range teamOrder {
		roster := teams[team]
		b.WriteString(fmt.Sprintf("### %s\n\n", team))
		// Team roll-up of observed net worth / gold.
		var teamNetWorth, teamGold *float64
		for _, p := range roster {
			if p.NetWorth != nil {
				v := ptrVal(p.NetWorth)
				teamNetWorth = addFloat(teamNetWorth, v)
			}
			if p.Gold != nil {
				v := ptrVal(p.Gold)
				teamGold = addFloat(teamGold, v)
			}
		}
		b.WriteString(fmt.Sprintf("- Team net worth: %s\n", fmtFloatPtr(teamNetWorth)))
		b.WriteString(fmt.Sprintf("- Team gold: %s\n", fmtFloatPtr(teamGold)))
		for _, p := range roster {
			label := p.Slot
			if p.PlayerName != "" {
				label = fmt.Sprintf("%s (%s)", p.Slot, p.PlayerName)
			}
			if p.HeroName != "" {
				label = fmt.Sprintf("%s — %s", label, p.HeroName)
			}
			b.WriteString(fmt.Sprintf("  - %s: net worth %s, gold %s (reliable %s, unreliable %s), gpm %s, xpm %s, LH %s, denies %s, K/D/A %s/%s/%s\n",
				label,
				fmtFloatPtr(p.NetWorth), fmtFloatPtr(p.Gold),
				fmtFloatPtr(p.GoldReliable), fmtFloatPtr(p.GoldUnreliable),
				fmtIntPtr(p.GPM), fmtIntPtr(p.XPM),
				fmtIntPtr(p.LastHits), fmtIntPtr(p.Denies),
				fmtIntPtr(p.Kills), fmtIntPtr(p.Deaths), fmtIntPtr(p.Assists),
			))
		}
	}
	return b.String()
}

func ptrVal(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func addFloat(acc *float64, v float64) *float64 {
	if acc == nil {
		x := v
		return &x
	}
	x := *acc + v
	return &x
}

func fmtFloatPtr(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f", *p)
}

func fmtIntPtr(p *int64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}

// ---- file writers -------------------------------------------------------------

func writeJSONL(path string, rows any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	switch v := rows.(type) {
	case []NormalizedTick:
		for _, t := range v {
			if err := enc.Encode(t); err != nil {
				return fmt.Errorf("encode normalized tick: %w", err)
			}
		}
	case []Event:
		for _, e := range v {
			if err := enc.Encode(e); err != nil {
				return fmt.Errorf("encode event: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported jsonl row type %T", rows)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
