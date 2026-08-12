package liveprojection_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type recordingProjection struct {
	sequences []uint64
	failAt    uint64
}

type blockingProjection struct{ entered, release chan struct{} }

func (p blockingProjection) Apply(ctx context.Context, _ *session.Record) error {
	close(p.entered)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *recordingProjection) Apply(_ context.Context, record *session.Record) error {
	if record.Sequence == p.failAt {
		return errors.New("injected downstream failure")
	}
	p.sequences = append(p.sequences, record.Sequence)
	return nil
}

func appendRecords(t *testing.T, root, id string, count int) *session.Store {
	t.Helper()
	store, err := session.NewStore(root, session.WithSessionID(id))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if _, err := store.Append([]byte(`{"map":{"game_time":1}}`)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestFollowerProcessesEveryMissingSequenceInOrderAndPersistsCursor(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "ordered", 3)
	projection := &recordingProjection{}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "live_projection_cursor.json"), []liveprojection.Projection{projection})

	if err := follower.CatchUp(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2, 3}) {
		t.Fatalf("projection order = %v", projection.sequences)
	}
	health := follower.Health()
	if health.HighWater != 3 || health.ProjectedSequence != 3 || health.LagCount != 0 || health.Degraded {
		t.Fatalf("health = %#v", health)
	}
	data, err := os.ReadFile(filepath.Join(store.SessionDir(), "live_projection_cursor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cursor struct {
		SessionID string `json:"session_id"`
		Sequence  uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor.SessionID != "ordered" || cursor.Sequence != 3 {
		t.Fatalf("cursor = %#v", cursor)
	}
}

func TestFollowerRetriesFailedSequenceWithoutAdvancingCursor(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "retry", 2)
	first := &recordingProjection{}
	projection := &recordingProjection{failAt: 2}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), []liveprojection.Projection{first, projection})

	if err := follower.CatchUp(context.Background(), 2); err == nil {
		t.Fatal("CatchUp succeeded")
	}
	health := follower.Health()
	if health.ProjectedSequence != 1 || health.LagCount != 1 || !health.Degraded || health.LastError == "" {
		t.Fatalf("failed health = %#v", health)
	}
	projection.failAt = 0
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2}) {
		t.Fatalf("retry applied sequences = %v", projection.sequences)
	}
	if !reflect.DeepEqual(first.sequences, []uint64{1, 2}) {
		t.Fatalf("successful adapter replayed = %v", first.sequences)
	}
}

func TestFollowerRejectsOutOfOrderHighWater(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "out-of-order", 2)
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), nil)
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if err := follower.CatchUp(context.Background(), 1); !errors.Is(err, liveprojection.ErrOutOfOrderHighWater) {
		t.Fatalf("error = %v", err)
	}
}

func TestFollowerRebuildsForMissingCorruptAndStaleCursor(t *testing.T) {
	for _, tc := range []struct{ name, cursor string }{
		{name: "missing"},
		{name: "corrupt", cursor: "not-json"},
		{name: "wrong-session", cursor: `{"session_id":"other","sequence":2}`},
		{name: "ahead", cursor: `{"session_id":"restart","sequence":9}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := appendRecords(t, root, "restart", 2)
			cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
			if tc.cursor != "" {
				if err := os.WriteFile(cursorPath, []byte(tc.cursor), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			projection := &recordingProjection{}
			follower := liveprojection.New("restart", store.RawPath(), cursorPath, []liveprojection.Projection{projection})
			if err := follower.CatchUp(context.Background(), 2); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(projection.sequences, []uint64{1, 2}) {
				t.Fatalf("rebuild = %v", projection.sequences)
			}
		})
	}
}

func TestFollowerResumesAfterValidCursorAndDeduplicatesHighWater(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "resume", 3)
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	if err := os.WriteFile(cursorPath, []byte(`{"session_id":"resume","sequence":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := &recordingProjection{}
	follower := liveprojection.New("resume", store.RawPath(), cursorPath, []liveprojection.Projection{projection})
	if err := follower.CatchUp(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if err := follower.CatchUp(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{3}) {
		t.Fatalf("resume/deduplicate = %v", projection.sequences)
	}
}

func TestFollowerReplaysAcceptedLegacyRawEnvelope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"received_at":"2026-08-05T12:00:00Z","payload":{"ok":true},"raw":{"ok":true}}` + "\n"
	rawPath := filepath.Join(dir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := &recordingProjection{}
	follower := liveprojection.New("legacy", rawPath, filepath.Join(dir, "cursor.json"), []liveprojection.Projection{projection})
	if err := follower.CatchUp(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1}) {
		t.Fatalf("legacy replay = %v", projection.sequences)
	}
}

func TestFollowerRunDrainsCoalescedHighWaterAndStopsBoundedly(t *testing.T) {
	root := t.TempDir()
	wake := session.NewHighWater("run", 0)
	store, err := session.NewStore(root, session.WithSessionID("run"), session.WithHighWater(wake))
	if err != nil {
		t.Fatal(err)
	}
	projection := &recordingProjection{}
	follower := liveprojection.New("run", store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), []liveprojection.Projection{projection})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- follower.Run(ctx, wake.C()) }()
	appendRecordsUsing(t, store, 3)
	deadline := time.Now().Add(time.Second)
	for follower.Health().ProjectedSequence != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop boundedly")
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2, 3}) {
		t.Fatalf("run order = %v", projection.sequences)
	}
}

func TestFollowerHealthRemainsBoundedWhileProjectionIsBlocked(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "health", 1)
	entered, release := make(chan struct{}), make(chan struct{})
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), []liveprojection.Projection{blockingProjection{entered, release}})
	done := make(chan error, 1)
	go func() { done <- follower.CatchUp(context.Background(), 1) }()
	<-entered
	healthDone := make(chan liveprojection.Health, 1)
	go func() { healthDone <- follower.Health() }()
	select {
	case health := <-healthDone:
		if health.HighWater != 1 || health.ProjectedSequence != 0 || health.LagCount != 1 || !health.Degraded {
			t.Fatalf("health=%#v", health)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("health blocked behind downstream projection")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func appendRecordsUsing(t *testing.T, store *session.Store, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := store.Append([]byte(`{"ok":true}`)); err != nil {
			t.Fatal(err)
		}
	}
}
