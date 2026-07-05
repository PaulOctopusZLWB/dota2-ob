package state_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

func TestLatestProjectsKnownSections(t *testing.T) {
	latest := state.NewLatest()
	receivedAt := time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC)

	latest.Update(receivedAt, map[string]any{
		"provider": map[string]any{"name": "Dota 2", "appid": float64(570)},
		"map":      map[string]any{"game_time": float64(123)},
		"hero":     map[string]any{"team2": map[string]any{"player0": map[string]any{"name": "npc_dota_hero_axe"}}},
		"ignored":  "not exposed",
	})

	snapshot := latest.Snapshot("test-session")
	if snapshot.Status != "ok" {
		t.Fatalf("status = %q, want ok", snapshot.Status)
	}
	if snapshot.SessionID != "test-session" {
		t.Fatalf("session id = %q, want test-session", snapshot.SessionID)
	}
	if snapshot.SnapshotCount != 1 {
		t.Fatalf("snapshot count = %d, want 1", snapshot.SnapshotCount)
	}
	if snapshot.ReceivedAt == nil || !snapshot.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("received_at = %v, want %s", snapshot.ReceivedAt, receivedAt)
	}
	if snapshot.Provider == nil || snapshot.Map == nil || snapshot.Hero == nil {
		t.Fatalf("missing projected sections: %#v", snapshot)
	}
	if snapshot.Player != nil || snapshot.Items != nil {
		t.Fatalf("unexpected sections projected: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(encoded), "ignored") {
		t.Fatalf("ignored source field leaked into snapshot JSON: %s", encoded)
	}
}

func TestLatestEmptySnapshotIsSafe(t *testing.T) {
	latest := state.NewLatest()

	snapshot := latest.Snapshot("empty-session")
	if snapshot.Status != "empty" {
		t.Fatalf("status = %q, want empty", snapshot.Status)
	}
	if snapshot.SessionID != "empty-session" {
		t.Fatalf("session id = %q, want empty-session", snapshot.SessionID)
	}
	if snapshot.Provider != nil || snapshot.Map != nil || snapshot.Hero != nil {
		t.Fatalf("empty snapshot should not expose sections: %#v", snapshot)
	}
}
