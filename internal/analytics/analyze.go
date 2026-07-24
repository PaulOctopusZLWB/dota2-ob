package analytics

import (
	"bufio"
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
	for i, rec := range records {
		tick := Normalize(rec.ReceivedAt, rec.Payload)
		tick.TickIndex = int64(i)
		engine.Observe(tick)
		ticks = append(ticks, tick)
	}

	snap := engine.Snapshot(sessionID)
	if err := WriteArtifacts(sessionDir, sessionID, ticks, engine.AllEvents(), snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// rawRecord is the on-disk shape written by the session store.
type rawRecord struct {
	ReceivedAt time.Time `json:"received_at"`
	Payload    any       `json:"payload"`
}

func readRawJSONL(path string) ([]rawRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open raw jsonl: %w", err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 1<<20)
	dec := json.NewDecoder(reader)
	dec.UseNumber()

	var records []rawRecord
	for {
		var rec rawRecord
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return records, fmt.Errorf("decode raw record %d: %w", len(records), err)
		}
		records = append(records, rec)
	}
	return records, nil
}