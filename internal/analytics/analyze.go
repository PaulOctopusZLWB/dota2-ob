package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// AnalyzeSession reads <sessionDir>/raw.jsonl, normalizes every accepted
// snapshot, derives delta events, and writes the four analysis artifacts back
// under sessionDir. It returns the engine snapshot it materialized.
func AnalyzeSession(sessionDir, sessionID string) (Snapshot, error) {
	if sessionDir == "" {
		return Snapshot{}, errors.New("session directory is required")
	}

	records, err := readRawJSONL(filepath.Join(sessionDir, "raw.jsonl"), sessionID)
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
	ReceivedAt time.Time `json:"received_at"`
	Payload    any       `json:"payload"`
}

type diskRawRecord struct {
	SchemaVersion *int            `json:"schema_version"`
	SessionID     string          `json:"session_id"`
	Sequence      uint64          `json:"sequence"`
	ReceivedAt    time.Time       `json:"received_at"`
	Source        string          `json:"source"`
	Payload       json.RawMessage `json:"payload"`
	Raw           json.RawMessage `json:"raw"`
}

func readRawJSONL(path, expectedSessionID string) ([]rawRecord, error) {
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
	cursor := 0
	for {
		for cursor < len(committed) && isJSONWhitespace(committed[cursor]) {
			if committed[cursor] == '\n' {
				return records, fmt.Errorf("blank raw record %d", len(records)+1)
			}
			cursor++
		}
		var disk diskRawRecord
		if err := dec.Decode(&disk); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return records, fmt.Errorf("decode raw record %d", len(records)+1)
		}
		end := int(dec.InputOffset())
		separatorEnd := end
		newlines := 0
		for separatorEnd < len(committed) && isJSONWhitespace(committed[separatorEnd]) {
			if committed[separatorEnd] == '\n' {
				newlines++
			}
			separatorEnd++
		}
		if newlines != 1 {
			return records, fmt.Errorf("invalid jsonl boundary at record %d", len(records)+1)
		}
		cursor = separatorEnd
		sequence := uint64(len(records) + 1)
		if len(disk.Payload) == 0 || len(disk.Raw) == 0 || disk.ReceivedAt.IsZero() {
			return records, fmt.Errorf("invalid raw record %d", sequence)
		}
		if disk.SchemaVersion == nil {
			// Version 1 derives identity from its session directory/order.
		} else if *disk.SchemaVersion != 2 {
			return records, fmt.Errorf("unsupported raw schema version at record %d", sequence)
		} else if disk.Sequence != sequence || disk.SessionID != expectedSessionID || disk.Source != "gsi" {
			return records, fmt.Errorf("invalid version 2 raw record %d", sequence)
		}
		payload, err := decodeRawValue(disk.Payload)
		if err != nil {
			return records, fmt.Errorf("invalid payload at raw record %d", sequence)
		}
		rawValue, err := decodeRawValue(disk.Raw)
		if err != nil || !reflect.DeepEqual(payload, rawValue) {
			return records, fmt.Errorf("raw payload mismatch at record %d", sequence)
		}
		records = append(records, rawRecord{ReceivedAt: disk.ReceivedAt, Payload: payload})
	}
	return records, nil
}

func decodeRawValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return value, nil
}

func isJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}
