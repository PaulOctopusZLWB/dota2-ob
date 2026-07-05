package session_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestStoreAppendWritesJSONLRecord(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 5, 12, 30, 45, 123000000, time.UTC)

	store, err := session.NewStore(root,
		session.WithClock(func() time.Time { return now }),
		session.WithSessionID("test-session"),
	)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	record, err := store.Append([]byte(`{"provider":{"name":"Dota 2","appid":570},"map":{"game_time":123}}`))
	if err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	if record.ReceivedAt != now {
		t.Fatalf("record timestamp = %s, want %s", record.ReceivedAt, now)
	}

	rawPath := filepath.Join(root, "test-session", "raw.jsonl")
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw JSONL: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got, want := len(lines), 1; got != want {
		t.Fatalf("line count = %d, want %d; data=%q", got, want, string(data))
	}

	var persisted struct {
		ReceivedAt time.Time       `json:"received_at"`
		Payload    map[string]any  `json:"payload"`
		Raw        json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &persisted); err != nil {
		t.Fatalf("JSONL line did not parse as JSON object: %v", err)
	}

	if !persisted.ReceivedAt.Equal(now) {
		t.Fatalf("persisted timestamp = %s, want %s", persisted.ReceivedAt, now)
	}
	if persisted.Payload["provider"] == nil {
		t.Fatalf("persisted payload missing provider: %#v", persisted.Payload)
	}

	var raw map[string]any
	if err := json.Unmarshal(persisted.Raw, &raw); err != nil {
		t.Fatalf("raw payload did not parse as JSON: %v", err)
	}
	if raw["map"] == nil {
		t.Fatalf("raw payload missing map: %#v", raw)
	}
}

func TestStoreAppendRejectsMalformedJSONWithoutWriting(t *testing.T) {
	root := t.TempDir()

	store, err := session.NewStore(root, session.WithSessionID("bad-session"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	_, err = store.Append([]byte(`{"provider":`))
	if !errors.Is(err, session.ErrInvalidJSON) {
		t.Fatalf("Append error = %v, want ErrInvalidJSON", err)
	}

	rawPath := filepath.Join(root, "bad-session", "raw.jsonl")
	if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw file stat error = %v, want not exist", err)
	}
}
