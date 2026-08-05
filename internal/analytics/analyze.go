package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// AnalyzeSession reads <sessionDir>/raw.jsonl, normalizes every accepted
// snapshot, derives delta events, and writes the four analysis artifacts back
// under sessionDir. It returns the engine snapshot it materialized.
func AnalyzeSession(sessionDir, sessionID string) (Snapshot, error) {
	if sessionDir == "" {
		return Snapshot{}, errors.New("session directory is required")
	}

	records, err := readRawJSONL(filepath.Join(sessionDir, "raw.jsonl"))
	if err != nil {
		return Snapshot{}, err
	}

	engine := NewEngine()
	ticks := make([]NormalizedTick, 0, len(records))
	// Collect every derived event from Observe's per-tick return value so the
	// offline derived_events.jsonl artifact is complete for long sessions. The
	// live engine retains only a bounded ring for /api/events, but offline
	// artifact generation must not silently drop older events.
	events := make([]Event, 0)
	for i, rec := range records {
		tick := Normalize(rec.ReceivedAt, rec.Payload)
		tick.TickIndex = int64(i)
		derived := engine.Observe(tick)
		ticks = append(ticks, tick)
		events = append(events, derived...)
	}

	snap := engine.Snapshot(sessionID)
	if err := WriteArtifacts(sessionDir, sessionID, ticks, events, snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// rawRecord is the on-disk shape written by the session store.
type rawRecord struct {
	SchemaVersion *int      `json:"schema_version"`
	SessionID     string    `json:"session_id"`
	Sequence      uint64    `json:"sequence"`
	ReceivedAt    time.Time `json:"received_at"`
	Source        string    `json:"source"`
	Payload       any       `json:"payload"`
}

func readRawJSONL(path string) ([]rawRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open raw jsonl: %w", err)
	}
	committed := data
	if len(data) > 0 && data[len(data)-1] != '\n' {
		last := bytes.LastIndexByte(data, '\n')
		if last < 0 {
			committed = nil
		} else {
			committed = data[:last+1]
		}
	}
	dec := json.NewDecoder(bytes.NewReader(committed))
	dec.UseNumber()
	var records []rawRecord
	for {
		var rec rawRecord
		if err := dec.Decode(&rec); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return records, fmt.Errorf("decode raw record %d", len(records)+1)
		}
		sequence := uint64(len(records) + 1)
		if rec.SchemaVersion == nil {
			rec.SessionID = filepath.Base(filepath.Dir(path))
			rec.Sequence = sequence
			rec.Source = "gsi"
		} else if *rec.SchemaVersion != 2 {
			return records, fmt.Errorf("unsupported raw schema version at record %d", sequence)
		} else if rec.Sequence != sequence || rec.SessionID == "" || rec.Source != "gsi" || rec.ReceivedAt.IsZero() || rec.Payload == nil {
			return records, fmt.Errorf("invalid version 2 raw record %d", sequence)
		}
		records = append(records, rec)
	}
	return records, nil
}
