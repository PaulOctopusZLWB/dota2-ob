package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

// RebuildRecord retains the committed evidence needed by a caller-supplied
// canonical normalizer without exposing analytics storage internals.
type RebuildRecord struct {
	SchemaVersion int
	SessionID     string
	Sequence      uint64
	ReceivedAt    time.Time
	Source        string
	Payload       any
	Raw           json.RawMessage
}
type RebuildNormalizer func(index int, record RebuildRecord) (NormalizedTick, error)

// AnalyzeSessionWithNormalizer reads raw.jsonl and materializes the accepted
// artifacts through a required caller-owned normalization boundary.
func AnalyzeSessionWithNormalizer(sessionDir, sessionID string, normalize RebuildNormalizer) (Snapshot, error) {
	if sessionDir == "" {
		return Snapshot{}, errors.New("session directory is required")
	}
	if normalize == nil {
		return Snapshot{}, errors.New("rebuild normalizer is required")
	}

	engine := NewEngine()
	ticks := make([]NormalizedTick, 0)
	// Collect every derived event from Observe's per-tick return value so the
	// offline derived_events.jsonl artifact is complete for long sessions. The
	// live engine retains only a bounded ring for /api/events, but offline
	// artifact generation must not silently drop older events.
	events := make([]Event, 0)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	consume := func(record *session.Record) error {
		if record.ProjectionResult == session.ProjectionConsumedNoOutput {
			return nil
		}
		i := len(ticks)
		rec := RebuildRecord{SchemaVersion: record.SchemaVersion, SessionID: record.SessionID, Sequence: record.Sequence, ReceivedAt: record.ReceivedAt, Source: record.Source, Payload: record.Payload, Raw: record.Raw}
		tick, err := normalize(i, rec)
		if err != nil {
			return fmt.Errorf("normalize raw record %d: %w", i+1, err)
		}
		tick.TickIndex = int64(i)
		derived := engine.Observe(tick)
		ticks = append(ticks, tick)
		events = append(events, derived...)
		return nil
	}
	err := session.StreamRecords(rawPath, sessionID, consume)
	if err != nil {
		return Snapshot{}, err
	}

	snap := engine.Snapshot(sessionID)
	if err := WriteArtifacts(sessionDir, sessionID, ticks, events, snap); err != nil {
		return snap, err
	}
	return snap, nil
}
