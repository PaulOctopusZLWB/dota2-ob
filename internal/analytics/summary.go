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
	Status                   string         `json:"status"`
	SessionID                string         `json:"session_id,omitempty"`
	TickCount                uint64         `json:"tick_count"`
	StartedAt                *time.Time     `json:"started_at,omitempty"`
	LastSeenAt               *time.Time     `json:"last_seen_at,omitempty"`
	CompleteTenPlayerFrames  uint64         `json:"complete_ten_player_frames"`
	EventCounts              map[string]int `json:"event_counts"`
	RecentEvents             []Event        `json:"recent_events"`
	RoshanObservations       []Event        `json:"roshan_observations,omitempty"`
	BuildingDestroyed        []Event        `json:"building_destroyed,omitempty"`
	WardObservations         []Event        `json:"ward_observations,omitempty"`
	WardCoordinateConclusion  string         `json:"ward_coordinate_conclusion"`
}

// Snapshot returns the bounded analytics state for the API.
func (e *Engine) Snapshot(sessionID string) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	snap := Snapshot{
		Status:                  "empty",
		SessionID:               sessionID,
		TickCount:               e.tickCount,
		CompleteTenPlayerFrames: e.completeTenPlayerFrames,
		EventCounts:             e.eventCountsSnapshot(),
		RecentEvents:            recentEventSlice(e.events),
		RoshanObservations:      e.roshanObservationsSnapshot(),
		BuildingDestroyed:       e.buildingDestroyedSnapshot(),
		WardObservations:        e.wardObservationsSnapshot(),
		WardCoordinateConclusion: WardCoordinateConclusion,
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
	SessionID                string         `json:"session_id"`
	TickCount                int            `json:"tick_count"`
	StartedAt                *time.Time     `json:"started_at,omitempty"`
	LastSeenAt               *time.Time     `json:"last_seen_at,omitempty"`
	ObservedDurationSeconds  float64        `json:"observed_duration_seconds,omitempty"`
	CompleteTenPlayerFrames  int            `json:"complete_ten_player_frames"`
	EventCounts              map[string]int `json:"event_counts"`
	EventTotal               int            `json:"event_total"`
	RoshanObservations       []Event        `json:"roshan_observations,omitempty"`
	BuildingObservations     []Event        `json:"building_observations,omitempty"`
	WardObservations         []Event        `json:"ward_observations,omitempty"`
	WardCoordinateConclusion string         `json:"ward_coordinate_conclusion"`
}

func buildSummaryJSON(sessionID string, ticks []NormalizedTick, events []Event, snap Snapshot) SummaryJSON {
	s := SummaryJSON{
		SessionID:                sessionID,
		TickCount:                len(ticks),
		CompleteTenPlayerFrames:  int(snap.CompleteTenPlayerFrames),
		EventCounts:              snap.EventCounts,
		RoshanObservations:       snap.RoshanObservations,
		BuildingObservations:     snap.BuildingDestroyed,
		WardObservations:         snap.WardObservations,
		WardCoordinateConclusion: WardCoordinateConclusion,
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

	b.WriteString("\n## Objective / Roshan / Building Observations\n\n")
	if len(s.RoshanObservations) == 0 && len(s.BuildingObservations) == 0 {
		b.WriteString("- none observed\n")
	} else {
		if n := len(s.RoshanObservations); n > 0 {
			b.WriteString(fmt.Sprintf("- Roshan state changes: %d\n", n))
		} else {
			b.WriteString("- Roshan state changes: 0 (state remained stable after first observation)\n")
		}
		if n := len(s.BuildingObservations); n > 0 {
			b.WriteString(fmt.Sprintf("- Building destroyed observations: %d\n", n))
		} else {
			b.WriteString("- Building destroyed observations: 0\n")
		}
	}

	b.WriteString("\n## Ward Observations\n\n")
	if len(s.WardObservations) == 0 {
		b.WriteString("- Ward counter / purchase-cooldown changes: 0\n")
	} else {
		b.WriteString(fmt.Sprintf("- Ward-related events: %d\n", len(s.WardObservations)))
	}
	b.WriteString(fmt.Sprintf("- Exact ward coordinates: %s\n", s.WardCoordinateConclusion))

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