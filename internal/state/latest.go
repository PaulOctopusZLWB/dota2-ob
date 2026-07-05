package state

import (
	"encoding/json"
	"sync"
	"time"
)

var knownSections = []string{
	"provider",
	"map",
	"player",
	"hero",
	"items",
	"abilities",
	"buildings",
	"draft",
}

type Latest struct {
	mu            sync.RWMutex
	receivedAt    time.Time
	snapshotCount uint64
	sections      map[string]any
}

type Snapshot struct {
	Status        string     `json:"status"`
	SessionID     string     `json:"session_id"`
	ReceivedAt    *time.Time `json:"received_at,omitempty"`
	SnapshotCount uint64     `json:"snapshot_count"`
	Provider      any        `json:"provider,omitempty"`
	Map           any        `json:"map,omitempty"`
	Player        any        `json:"player,omitempty"`
	Hero          any        `json:"hero,omitempty"`
	Items         any        `json:"items,omitempty"`
	Abilities     any        `json:"abilities,omitempty"`
	Buildings     any        `json:"buildings,omitempty"`
	Draft         any        `json:"draft,omitempty"`
}

func NewLatest() *Latest {
	return &Latest{
		sections: make(map[string]any),
	}
}

func (l *Latest) Update(receivedAt time.Time, payload any) {
	projected := projectSections(payload)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.receivedAt = receivedAt.UTC()
	l.snapshotCount++
	l.sections = projected
}

func (l *Latest) Snapshot(sessionID string) Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()

	snapshot := Snapshot{
		Status:        "empty",
		SessionID:     sessionID,
		SnapshotCount: l.snapshotCount,
	}
	if l.snapshotCount == 0 {
		return snapshot
	}

	receivedAt := l.receivedAt
	snapshot.Status = "ok"
	snapshot.ReceivedAt = &receivedAt
	snapshot.Provider = clone(l.sections["provider"])
	snapshot.Map = clone(l.sections["map"])
	snapshot.Player = clone(l.sections["player"])
	snapshot.Hero = clone(l.sections["hero"])
	snapshot.Items = clone(l.sections["items"])
	snapshot.Abilities = clone(l.sections["abilities"])
	snapshot.Buildings = clone(l.sections["buildings"])
	snapshot.Draft = clone(l.sections["draft"])
	return snapshot
}

func projectSections(payload any) map[string]any {
	object, ok := payload.(map[string]any)
	if !ok {
		return map[string]any{}
	}

	projected := make(map[string]any)
	for _, section := range knownSections {
		if value, ok := object[section]; ok {
			projected[section] = clone(value)
		}
	}
	return projected
}

func clone(value any) any {
	if value == nil {
		return nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}
