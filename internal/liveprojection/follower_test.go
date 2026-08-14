package liveprojection_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type recordingProjection struct {
	sequences []uint64
	failAt    uint64
}

type recoverableProjection struct {
	mu        sync.Mutex
	failing   bool
	calls     []uint64
	successes []uint64
}

func (p *recoverableProjection) Apply(_ context.Context, record *session.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, record.Sequence)
	if p.failing {
		return errors.New("temporary failure")
	}
	p.successes = append(p.successes, record.Sequence)
	return nil
}
func (p *recoverableProjection) recover() { p.mu.Lock(); p.failing = false; p.mu.Unlock() }
func (p *recoverableProjection) snapshot() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.calls...)
}
func (p *recoverableProjection) successful() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.successes...)
}

type blockingProjection struct{ entered, release chan struct{} }

type controlSink struct {
	mu        sync.Mutex
	restoring bool
	events    []string
}

func (s *controlSink) BeginRestore(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restoring = true
	s.events = append(s.events, "begin")
	return nil
}
func (s *controlSink) CompleteRestore(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "complete")
	s.restoring = false
	return nil
}
func (s *controlSink) ProjectionHealth(_ context.Context, transition liveprojection.RejectionTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if transition.Active {
		s.events = append(s.events, "hide")
	} else {
		s.events = append(s.events, "clear")
	}
	return nil
}

type controlAwareProjection struct {
	sink                   *controlSink
	sequences              []uint64
	calledWhileRestoreOpen bool
}

type scanRecorder struct {
	mu     sync.Mutex
	ranges []liveprojection.ScanRange
}

func (r *scanRecorder) ScanStarted(value liveprojection.ScanRange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ranges = append(r.ranges, value)
}
func (r *scanRecorder) snapshot() []liveprojection.ScanRange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]liveprojection.ScanRange(nil), r.ranges...)
}

func (p *controlAwareProjection) Apply(_ context.Context, record *session.Record) error {
	p.sink.mu.Lock()
	defer p.sink.mu.Unlock()
	p.sequences = append(p.sequences, record.Sequence)
	if p.sink.restoring {
		p.calledWhileRestoreOpen = true
	}
	return nil
}

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

func TestFollowerConsumesNoOutputWithoutAdaptersAndRestoresBoundedRejectionHealth(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("no-output"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"map":{"game_time":1}}`, `null`, `{"items":{"t":{"p":{` + manyItems(33) + `}}}}`} {
		if _, err := store.Append([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	projection := &recordingProjection{}
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection})
	if err := follower.CatchUp(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1}) {
		t.Fatalf("no-output reached adapters: %v", projection.sequences)
	}
	health := follower.Health()
	if health.ProjectedSequence != 3 || health.LagCount != 0 || !health.ProjectionRejectionActive || health.ProjectionRejectionCount != 2 || health.LastProjectionRejectionSequence != 3 || health.LastProjectionRejectionCode != "gsi_projection_bounds_exceeded" || health.LastProjectionRejectionReason != "item_count" {
		t.Fatalf("health=%#v", health)
	}

	// A valid cursor restores diagnostics and active suppression without adapter replay.
	restarted := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil)
	if err := restarted.CatchUp(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	restored := restarted.Health()
	if !restored.ProjectionRejectionActive || restored.ProjectionRejectionCount != 2 || restored.LastProjectionRejectionSequence != 3 {
		t.Fatalf("restored=%#v", restored)
	}

	if _, err := store.Append([]byte(`{"map":{"game_time":4}}`)); err != nil {
		t.Fatal(err)
	}
	if err := follower.CatchUp(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	cleared := follower.Health()
	if cleared.ProjectionRejectionActive || cleared.ProjectionRejectionCount != 2 || cleared.LastProjectionRejectionSequence != 3 || cleared.Degraded {
		t.Fatalf("cleared=%#v", cleared)
	}
}

func manyItems(count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"i%d":{"name":"x"}`, i)
	}
	return strings.Join(parts, ",")
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
		{name: "old-version", cursor: `{"schema_version":1,"session_id":"restart","sequence":2}`},
		{name: "semantic-inconsistency", cursor: `{"schema_version":2,"session_id":"restart","sequence":2,"projection_rejection_active":true,"projection_rejection_count":0,"last_projection_rejection_sequence":2,"last_projection_rejection_code":"gsi_projection_non_object","last_projection_rejection_reason":"top_level_non_object"}`},
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
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2, 3}) {
		t.Fatalf("fresh process rebuild/deduplicate = %v", projection.sequences)
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

func TestFollowerReplaysLargeLegacyBelowPreservedCap(t *testing.T) {
	const reviewerV2Size = 14_680_223
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "large-legacy")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			frame := largeLegacyFollowerFrame(t, version, reviewerV2Size)
			rawPath := filepath.Join(dir, "raw.jsonl")
			if err := os.WriteFile(rawPath, frame, 0o600); err != nil {
				t.Fatal(err)
			}
			projection := &recordingProjection{}
			follower := liveprojection.New("large-legacy", rawPath, filepath.Join(dir, "cursor.json"), []liveprojection.Projection{projection})
			if err := follower.CatchUp(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(projection.sequences, []uint64{1}) {
				t.Fatalf("legacy replay=%v", projection.sequences)
			}
		})
	}
}

func largeLegacyFollowerFrame(t *testing.T, version, target int) []byte {
	t.Helper()
	payloadPrefix, payloadSuffix := `{"padding":"`, `","slash":"/"}`
	rawPrefix, rawSuffix := `{"padding":"`, `","slash":"\/"}`
	header := `{"received_at":"2026-08-05T12:00:00Z","payload":`
	if version == 2 {
		header = `{"schema_version":2,"session_id":"large-legacy","sequence":1,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":`
	}
	base := header + payloadPrefix + payloadSuffix + `,"raw":` + rawPrefix + rawSuffix + "}\n"
	remaining := target - len(base)
	if remaining < 0 {
		t.Fatalf("target %d below base %d", target, len(base))
	}
	filler := strings.Repeat("x", remaining/2)
	frame := header + payloadPrefix + filler + payloadSuffix + `,"raw":` + rawPrefix + filler + rawSuffix + "}"
	if remaining%2 != 0 {
		frame += " "
	}
	frame += "\n"
	if len(frame) != target {
		t.Fatalf("legacy fixture size=%d want=%d", len(frame), target)
	}
	return []byte(frame)
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

func TestFollowerInternalRetryCatchesUpAfterHealthRefreshesNewerHighWater(t *testing.T) {
	root := t.TempDir()
	wake := session.NewHighWater("retry-health", 0)
	store, err := session.NewStore(root, session.WithSessionID("retry-health"), session.WithHighWater(wake))
	if err != nil {
		t.Fatal(err)
	}
	projection := &recoverableProjection{failing: true}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), []liveprojection.Projection{projection}, liveprojection.WithHighWater(store.HighWater()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- follower.Run(ctx, wake.C()) }()
	if _, err := store.Append([]byte(`{"sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(projection.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := store.Append([]byte(`{"sequence":2}`)); err != nil {
		t.Fatal(err)
	}
	health := follower.Health()
	if health.HighWater != 2 || health.ProjectedSequence != 0 || health.LagCount != 2 {
		t.Fatalf("health=%#v", health)
	}
	projection.recover()
	deadline = time.Now().Add(time.Second)
	for follower.Health().ProjectedSequence != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	health = follower.Health()
	if health.HighWater != 2 || health.ProjectedSequence != 2 || health.LagCount != 0 || health.Degraded || health.LastError == liveprojection.ErrOutOfOrderHighWater.Error() {
		t.Fatalf("recovered health=%#v calls=%v", health, projection.snapshot())
	}
	if successes := projection.successful(); !reflect.DeepEqual(successes, []uint64{1, 2}) {
		t.Fatalf("successful order=%v all calls=%v", successes, projection.snapshot())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
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

func TestFollowerHealthUsesCommittedStoreHighWaterAndAuthoritativeOldestLagAge(t *testing.T) {
	root := t.TempDir()
	receivedAt := time.Now().Add(-2 * time.Hour).UTC()
	store, err := session.NewStore(root, session.WithSessionID("saturated"), session.WithClock(func() time.Time { return receivedAt }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), []liveprojection.Projection{blockingProjection{entered, release}}, liveprojection.WithHighWater(store.HighWater()))
	done := make(chan error, 1)
	go func() { done <- follower.CatchUp(context.Background(), 1) }()
	<-entered
	if _, err := store.Append([]byte(`{"sequence":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"sequence":3}`)); err != nil {
		t.Fatal(err)
	}
	health := follower.Health()
	if health.HighWater != 3 || health.ProjectedSequence != 0 || health.LagCount != 3 || health.OldestLagAge < 90*time.Minute || !health.Degraded {
		t.Fatalf("saturated health=%#v", health)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFollowerRestartBacklogHealthUsesRawReceiveTimeBeforeProjectionStarts(t *testing.T) {
	root := t.TempDir()
	receivedAt := time.Now().Add(-3 * time.Hour).UTC()
	store, err := session.NewStore(root, session.WithSessionID("restart-backlog"), session.WithClock(func() time.Time { return receivedAt }))
	if err != nil {
		t.Fatal(err)
	}
	appendRecordsUsing(t, store, 2)
	follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), []liveprojection.Projection{&recordingProjection{}}, liveprojection.WithHighWater(store.HighWater()))
	health := follower.Health()
	if health.HighWater != 2 || health.ProjectedSequence != 0 || health.LagCount != 2 || health.OldestLagAge < 150*time.Minute || !health.Degraded {
		t.Fatalf("restart health=%#v", health)
	}
}

type cursorFaultIO struct {
	fault  string
	target string
	data   []byte
}
type cursorFaultFile struct{ owner *cursorFaultIO }

func (f cursorFaultFile) Name() string            { return f.owner.target + ".tmp" }
func (f cursorFaultFile) Chmod(os.FileMode) error { return nil }
func (f cursorFaultFile) Write(p []byte) (int, error) {
	if f.owner.fault == "write" {
		return 0, errors.New("write failed")
	}
	if f.owner.fault == "short-write" {
		f.owner.data = append([]byte(nil), p[:len(p)/2]...)
		return len(p) / 2, nil
	}
	f.owner.data = append([]byte(nil), p...)
	return len(p), nil
}
func (f cursorFaultFile) Close() error {
	if f.owner.fault == "close" {
		return errors.New("close failed")
	}
	return nil
}
func (o *cursorFaultIO) CreateTemp(string, string) (liveprojection.CursorFile, error) {
	if o.fault == "create" {
		return nil, errors.New("create failed")
	}
	return cursorFaultFile{o}, nil
}
func (o *cursorFaultIO) Remove(string) error { return nil }
func (o *cursorFaultIO) Rename(string, string) error {
	if o.fault == "rename" {
		return errors.New("rename failed")
	}
	if o.fault == "interrupt" {
		_ = os.Remove(o.target)
		return errors.New("interrupted")
	}
	return os.WriteFile(o.target, o.data, 0o600)
}

func TestFollowerCursorReplacementFailuresLeaveOnlyOldNewOrMissingCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "create", want: "old"}, {name: "write", want: "old"}, {name: "short-write", want: "old"}, {name: "close", want: "old"}, {name: "rename", want: "old"}, {name: "interrupt", want: "missing"}, {name: "success", want: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := appendRecords(t, root, "cursor-fault", 1)
			cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
			old := []byte(`{"session_id":"cursor-fault","sequence":0}`)
			if err := os.WriteFile(cursorPath, old, 0o600); err != nil {
				t.Fatal(err)
			}
			ops := &cursorFaultIO{fault: tc.name, target: cursorPath}
			follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{&recordingProjection{}}, liveprojection.WithCursorIO(ops))
			err := follower.CatchUp(context.Background(), 1)
			if tc.want != "new" && err == nil {
				t.Fatal("cursor fault succeeded")
			}
			data, readErr := os.ReadFile(cursorPath)
			switch tc.want {
			case "old":
				if readErr != nil || !reflect.DeepEqual(data, old) {
					t.Fatalf("outcome data=%q err=%v", data, readErr)
				}
			case "missing":
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("outcome data=%q err=%v", data, readErr)
				}
			case "new":
				if err != nil || readErr != nil || reflect.DeepEqual(data, old) {
					t.Fatalf("outcome data=%q read=%v catchup=%v", data, readErr, err)
				}
			}
		})
	}
}

func TestFollowerCountsRetriedNoOutputSequenceOnceAfterCursorFailure(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("retry-rejection"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`null`)); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	ops := &cursorFaultIO{fault: "create", target: cursorPath}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil, liveprojection.WithCursorIO(ops))
	if err := follower.CatchUp(context.Background(), 1); err == nil {
		t.Fatal("cursor failure succeeded")
	}
	ops.fault = ""
	if err := follower.CatchUp(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	health := follower.Health()
	if health.ProjectionRejectionCount != 1 || health.LastProjectionRejectionSequence != 1 {
		t.Fatalf("health=%#v", health)
	}
}

func TestFollowerReconstructsThroughValidCursorWithoutReplacingIt(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "valid-cache", 2)
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	seed := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil)
	if err := seed.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatal(err)
	}
	projection := &recordingProjection{}
	ops := &cursorFaultIO{fault: "create", target: cursorPath}
	sink := &controlSink{}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection}, liveprojection.WithCursorIO(ops), liveprojection.WithStartupBarrier(sink))
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(projection.sequences) != 0 {
		t.Fatalf("reconstruction=%v", projection.sequences)
	}
	data, err := os.ReadFile(cursorPath)
	if err != nil || !reflect.DeepEqual(data, old) {
		t.Fatalf("cursor data=%q err=%v", data, err)
	}
	sink.mu.Lock()
	events := append([]string(nil), sink.events...)
	sink.mu.Unlock()
	if !reflect.DeepEqual(events, []string{"begin", "complete"}) {
		t.Fatalf("startup publication events=%v", events)
	}
}

func TestFollowerRejectsRawInconsistentCursorBehindPublicationBarrier(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "false-cache", 2)
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	falseSummary := `{"schema_version":2,"session_id":"false-cache","sequence":1,"projection_rejection_active":true,"projection_rejection_count":1,"last_projection_rejection_sequence":1,"last_projection_rejection_code":"gsi_projection_non_object","last_projection_rejection_reason":"top_level_non_object"}`
	if err := os.WriteFile(cursorPath, []byte(falseSummary), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &controlSink{}
	projection := &controlAwareProjection{sink: sink}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection}, liveprojection.WithStartupBarrier(sink), liveprojection.WithRejectionHealthSink(sink))
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2}) || !projection.calledWhileRestoreOpen {
		t.Fatalf("rebuild=%v behind_barrier=%v", projection.sequences, projection.calledWhileRestoreOpen)
	}
	if h := follower.Health(); h.ProjectionRejectionCount != 0 || h.ProjectionRejectionActive {
		t.Fatalf("false summary survived: %#v", h)
	}
}

func TestFollowerValidCacheResumesAfterPrefixAndTransitionsHealth(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("valid-resume"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`null`)); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	seed := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil)
	if err := seed.CatchUp(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"map":{"game_time":2}}`)); err != nil {
		t.Fatal(err)
	}
	sink := &controlSink{}
	projection := &controlAwareProjection{sink: sink}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection}, liveprojection.WithStartupBarrier(sink), liveprojection.WithRejectionHealthSink(sink))
	started := time.Now()
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("health transition exceeded two seconds")
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{2}) || projection.calledWhileRestoreOpen {
		t.Fatalf("sequences=%v before_barrier=%v", projection.sequences, projection.calledWhileRestoreOpen)
	}
	sink.mu.Lock()
	events := append([]string(nil), sink.events...)
	sink.mu.Unlock()
	if !reflect.DeepEqual(events, []string{"begin", "hide", "complete", "clear"}) {
		t.Fatalf("events=%v", events)
	}
}

func TestFollowerUsesExactlyOneAuthoritativeScanAtRequiredRange(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mutate       func(t *testing.T, cursorPath string)
		wantFrom     uint64
		recoverStore bool
	}{
		{name: "missing", mutate: func(t *testing.T, cursorPath string) {
			if err := os.Remove(cursorPath); err != nil {
				t.Fatal(err)
			}
		}, wantFrom: 1},
		{name: "corrupt", mutate: func(t *testing.T, cursorPath string) {
			if err := os.WriteFile(cursorPath, []byte("bad"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantFrom: 1},
		{name: "false-summary", mutate: func(t *testing.T, cursorPath string) {
			data, err := os.ReadFile(cursorPath)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data,
				[]byte(`"projection_rejection_count":0,"last_projection_rejection_sequence":0,"next_offset"`),
				[]byte(`"projection_rejection_count":1,"last_projection_rejection_sequence":1,"last_projection_rejection_code":"gsi_projection_non_object","last_projection_rejection_reason":"top_level_non_object","next_offset"`), 1)
			var cursor map[string]any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&cursor); err != nil {
				t.Fatal(err)
			}
			delete(cursor, "proof_sha256")
			unsigned, err := json.Marshal(cursor)
			if err != nil {
				t.Fatal(err)
			}
			proof := sha256.Sum256(unsigned)
			cursor["proof_sha256"] = hex.EncodeToString(proof[:])
			data, err = json.Marshal(cursor)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cursorPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantFrom: 1},
		{name: "stale-offset", mutate: func(t *testing.T, cursorPath string) {
			data, err := os.ReadFile(cursorPath)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`"next_offset":`), []byte(`"next_offset":9`), 1)
			if err := os.WriteFile(cursorPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantFrom: 1},
		{name: "raw-inconsistent-authority", mutate: func(t *testing.T, cursorPath string) {
			path := filepath.Join(filepath.Dir(cursorPath), "raw.jsonl.authority-v3")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[2*72-1] ^= 0xff
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantFrom: 1},
		{name: "valid", mutate: func(*testing.T, string) {}, wantFrom: 3},
		{name: "valid-after-store-recovery", mutate: func(*testing.T, string) {}, wantFrom: 3, recoverStore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := appendRecords(t, root, "scan-range", 2)
			cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
			seed := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil)
			if err := seed.CatchUp(context.Background(), 2); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append([]byte(`{"map":{"game_time":3}}`)); err != nil {
				t.Fatal(err)
			}
			if tc.recoverStore {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := session.NewStore(root, session.WithSessionID("scan-range"))
				if err != nil {
					t.Fatal(err)
				}
				store = reopened
			}
			tc.mutate(t, cursorPath)
			recorder := &scanRecorder{}
			projection := &recordingProjection{}
			follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection}, liveprojection.WithScanObserver(recorder))
			if err := follower.CatchUp(context.Background(), 3); err != nil {
				t.Fatal(err)
			}
			ranges := recorder.snapshot()
			if len(ranges) != 1 || ranges[0].FromSequence != tc.wantFrom || ranges[0].ThroughSequence != 3 {
				t.Fatalf("scan ranges=%#v want one %d..3", ranges, tc.wantFrom)
			}
			if (tc.wantFrom == 1 && ranges[0].StartOffset != 0) || (tc.wantFrom == 3 && ranges[0].StartOffset <= 0) {
				t.Fatalf("scan offset=%d for from=%d", ranges[0].StartOffset, tc.wantFrom)
			}
			wantCalls := []uint64{1, 2, 3}
			if tc.wantFrom == 3 {
				wantCalls = []uint64{3}
			}
			if !reflect.DeepEqual(projection.sequences, wantCalls) {
				t.Fatalf("adapter calls=%v want=%v", projection.sequences, wantCalls)
			}
		})
	}
}

func TestFollowerInvalidatesSameLengthCanonicalRawRewrite(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("rewritten-prefix"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	seed := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil)
	if err := seed.CatchUp(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	line, err := os.ReadFile(store.RawPath())
	if err != nil {
		t.Fatal(err)
	}
	oldHash := sha256.Sum256([]byte(`{}`))
	newHash := sha256.Sum256([]byte(`[]`))
	line = bytes.Replace(line, []byte(`"raw_base64":"e30="`), []byte(`"raw_base64":"W10="`), 1)
	line = bytes.Replace(line, []byte(hex.EncodeToString(oldHash[:])), []byte(hex.EncodeToString(newHash[:])), 1)
	if err := os.WriteFile(store.RawPath(), line, 0o644); err != nil {
		t.Fatal(err)
	}

	recorder := &scanRecorder{}
	sink := &controlSink{}
	projection := &controlAwareProjection{sink: sink}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection}, liveprojection.WithScanObserver(recorder), liveprojection.WithStartupBarrier(sink))
	if err := follower.CatchUp(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if ranges := recorder.snapshot(); len(ranges) != 1 || ranges[0].FromSequence != 1 || ranges[0].ThroughSequence != 1 || ranges[0].StartOffset != 0 {
		t.Fatalf("rewritten prefix scans=%#v", ranges)
	}
	if len(projection.sequences) != 0 {
		t.Fatalf("non-object rewrite reached adapters: %v", projection.sequences)
	}
	if health := follower.Health(); !health.ProjectionRejectionActive || health.ProjectionRejectionCount != 1 || health.LastProjectionRejectionSequence != 1 {
		t.Fatalf("rewritten prefix health=%#v", health)
	}
}

func TestFollowerCorruptFutureAuthorityRebuildsAtomicallyAndRetriesIdempotently(t *testing.T) {
	root := t.TempDir()
	store := appendRecords(t, root, "corrupt-future", 1)
	cursorPath := filepath.Join(store.SessionDir(), "cursor.json")
	seed := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, nil)
	if err := seed.CatchUp(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"map":{"game_time":2}}`)); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(store.SessionDir(), "raw.jsonl.authority-v3")
	entry, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(entry, make([]byte, len(entry))...), 0o600); err != nil {
		t.Fatal(err)
	}

	recorder := &scanRecorder{}
	sink := &controlSink{}
	projection := &controlAwareProjection{sink: sink}
	follower := liveprojection.New(store.SessionID(), store.RawPath(), cursorPath, []liveprojection.Projection{projection}, liveprojection.WithScanObserver(recorder), liveprojection.WithStartupBarrier(sink))
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if ranges := recorder.snapshot(); len(ranges) != 1 || ranges[0].FromSequence != 1 || ranges[0].ThroughSequence != 2 || ranges[0].StartOffset != 0 {
		t.Fatalf("corrupt future scans=%#v", ranges)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2}) {
		t.Fatalf("adapter calls=%v", projection.sequences)
	}
	if !projection.calledWhileRestoreOpen {
		t.Fatal("corrupt future index published outside startup barrier")
	}
	if err := follower.CatchUp(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if ranges := recorder.snapshot(); len(ranges) != 1 {
		t.Fatalf("idempotent retry rescanned: %#v", ranges)
	}
	if !reflect.DeepEqual(projection.sequences, []uint64{1, 2}) {
		t.Fatalf("idempotent retry republished: %v", projection.sequences)
	}
}

func TestFollowerUsesFormulaLimitAtStartupAndLiveCatchUp(t *testing.T) {
	prefix := []byte(`{"schema\u005fversion":3,`)
	for _, tc := range []struct {
		name      string
		bytes     int
		wantError string
	}{
		{"exact", session.MaxEncodedRecordBytes(), "unexpected end of JSON input"},
		{"plus-one", session.MaxEncodedRecordBytes() + 1, "V3 frame exceeds encoded limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := append([]byte(nil), prefix...)
			frame = append(frame, bytes.Repeat([]byte{' '}, tc.bytes-len(prefix))...)
			frame = append(frame, '\n')
			assertFollowerFramingFailure(t, frame, tc.wantError)
		})
	}
}

func TestFollowerRejectsUnresolvedCandidatesAtFormulaLimit(t *testing.T) {
	for _, prefix := range []string{
		"null", "[]", "1", `"scalar"`, "{}",
		`{"received_at":null,"payload":null,"raw":null}`,
		`{"schema_version":2,"session_id":null,"sequence":null,"received_at":null,"source":null,"payload":null,"raw":null}`,
		`{"schema_version":2,"schema_version":3,`,
		`{"schema_version":3,"schema_version":2,`,
		`{"schema_version":2,"schema\u005fversion":3,`,
		`{"schema\u005fversion":3,"schema_version":2,`,
	} {
		t.Run(prefix, func(t *testing.T) {
			frame := append([]byte(prefix), bytes.Repeat([]byte{' '}, session.MaxEncodedRecordBytes()+1-len(prefix))...)
			frame = append(frame, '\n')
			assertFollowerFramingFailure(t, frame, "V3 frame exceeds encoded limit")
		})
	}
}

func assertFollowerFramingFailure(t *testing.T, frame []byte, wantError string) {
	t.Helper()
	t.Run("startup", func(t *testing.T) {
		root := t.TempDir()
		rawPath := filepath.Join(root, "raw.jsonl")
		if err := os.WriteFile(rawPath, frame, 0o600); err != nil {
			t.Fatal(err)
		}
		follower := liveprojection.New("startup-limit", rawPath, filepath.Join(root, "cursor.json"), nil)
		if err := follower.CatchUp(context.Background(), 1); err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("startup error=%v", err)
		}
	})

	t.Run("live-suffix", func(t *testing.T) {
		root := t.TempDir()
		store := appendRecords(t, root, "live-limit", 1)
		follower := liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "cursor.json"), nil)
		if err := follower.CatchUp(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(store.RawPath(), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(frame); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := follower.CatchUp(context.Background(), 2); err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("live suffix error=%v", err)
		}
	})
}

var _ io.Writer = cursorFaultFile{}

func appendRecordsUsing(t *testing.T, store *session.Store, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := store.Append([]byte(`{"ok":true}`)); err != nil {
			t.Fatal(err)
		}
	}
}
