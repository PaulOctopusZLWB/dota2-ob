package gsi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

type failingProjection struct{}

func (failingProjection) Apply(context.Context, *session.Record) error {
	return errors.New("do not expose this path /home/private")
}

type barrierProjection struct{ entered, release chan struct{} }

func (p barrierProjection) Apply(ctx context.Context, _ *session.Record) error {
	close(p.entered)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type stubbornProjection struct{ entered, release chan struct{} }

func (p stubbornProjection) Apply(context.Context, *session.Record) error {
	close(p.entered)
	<-p.release
	return nil
}

type countingProjection struct{ calls int }

func (p *countingProjection) Apply(context.Context, *session.Record) error { p.calls++; return nil }

type sealingRawFile struct {
	writes int
	offset int64
}

func (f *sealingRawFile) Write([]byte) (int, error) {
	f.writes++
	f.offset++
	return 1, io.ErrShortWrite
}
func (f *sealingRawFile) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart {
		f.offset = offset
	}
	return f.offset, nil
}
func (f *sealingRawFile) Truncate(int64) error       { return errors.New("rollback failed") }
func (f *sealingRawFile) Stat() (os.FileInfo, error) { return nil, errors.New("unused") }
func (f *sealingRawFile) Close() error               { return nil }

func TestHealthzReturnsOK(t *testing.T) {
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("health"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	server := httptest.NewServer(gsi.NewServer(store))
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz returned error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, body)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("body = %q, want ok", string(body))
	}
}

func TestGSIPostStoresValidJSON(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("valid-gsi"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	server := httptest.NewServer(gsi.NewServer(store))
	defer server.Close()

	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"provider":{"name":"Dota 2","appid":570},"map":{"game_time":123}}`))
	if err != nil {
		t.Fatalf("POST /gsi returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, body)
	}

	rawPath := filepath.Join(root, "valid-gsi", "raw.jsonl")
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw JSONL: %v", err)
	}
	if strings.Count(strings.TrimSpace(string(data)), "\n") != 0 {
		t.Fatalf("expected one JSONL line, got %q", string(data))
	}
}

func TestAcceptedPostReturnsOKWhenProjectionFailsAndStatusIsDegraded(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("degraded"), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	tracker := operator.NewTracker(store.SessionID(), now, 15*time.Second, func() time.Time { return now })
	handler := gsi.NewServer(store, gsi.WithTracker(tracker), gsi.WithLiveProjections(failingProjection{}))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Wait()

	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"map":{"game_time":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("accepted response status=%d type=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	statusResp, err := http.Get(server.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	var status struct {
		operator.Snapshot
		LiveProjection liveprojection.Health `json:"live_projection"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !status.LiveProjection.Degraded && time.Now().Before(deadline) {
		statusResp.Body.Close()
		time.Sleep(time.Millisecond)
		statusResp, err = http.Get(server.URL + "/api/status")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
			t.Fatal(err)
		}
	}
	if status.State != operator.StateDegraded || status.AcceptedCount != 1 || !status.LiveProjection.Degraded || status.LiveProjection.ProjectedSequence != 0 {
		t.Fatalf("status = %#v", status)
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), "/home/private") {
		t.Fatalf("status leaked internal error: %s", encoded)
	}
}

func TestStatusRejectsUnsupportedMethod(t *testing.T) {
	store, _ := session.NewStore(t.TempDir(), session.WithSessionID("status-method"))
	req := httptest.NewRequest(http.MethodPost, "/api/status", nil)
	rec := httptest.NewRecorder()
	gsi.NewServer(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestServerWaitDrainsAcceptedRequestThroughResponse(t *testing.T) {
	store, _ := session.NewStore(t.TempDir(), session.WithSessionID("drain"))
	now := time.Now().UTC()
	tracker := operator.NewTracker("drain", now, time.Minute, time.Now)
	entered, release := make(chan struct{}), make(chan struct{})
	handler := gsi.NewServer(store, gsi.WithTracker(tracker), gsi.WithLiveProjections(barrierProjection{entered, release}))
	server := httptest.NewServer(handler)
	defer server.Close()
	response := make(chan string, 1)
	go func() {
		resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{}`))
		if err != nil {
			response <- "error"
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		response <- string(body)
	}()
	<-entered
	select {
	case body := <-response:
		if body != "ok\n" {
			t.Fatalf("body=%q", body)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted response waited for blocked projection")
	}
	drained := make(chan struct{})
	go func() { handler.Wait(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("Wait returned before projector drain")
	default:
	}
	close(release)
	<-drained
}

func TestServerWaitBoundsBlockedProjectorDrain(t *testing.T) {
	store, _ := session.NewStore(t.TempDir(), session.WithSessionID("bounded-drain"))
	entered, release := make(chan struct{}), make(chan struct{})
	handler := gsi.NewServer(store, gsi.WithLiveProjections(stubbornProjection{entered, release}), gsi.WithProjectionDrainTimeout(20*time.Millisecond))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	<-entered
	started := time.Now()
	handler.Wait()
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("bounded drain took %s", elapsed)
	}
	close(release)
	cursorPath := filepath.Join(store.SessionDir(), "live_projection_cursor.json")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(cursorPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("projector did not finish after release")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGSIPostRejectsMalformedJSONWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("invalid-gsi"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	server := httptest.NewServer(gsi.NewServer(store))
	defer server.Close()

	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"provider":`))
	if err != nil {
		t.Fatalf("POST /gsi returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 4xx; body=%s", resp.StatusCode, body)
	}

	rawPath := filepath.Join(root, "invalid-gsi", "raw.jsonl")
	if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw file stat error = %v, want not exist", err)
	}
}

func TestGSIPostRejectsOversizedBodyWithoutRawOrProjection(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("oversized"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tracker := operator.NewTracker(store.SessionID(), now, time.Minute, time.Now)
	projection := &countingProjection{}
	processor := capture.NewProcessor(store, tracker)
	handler := gsi.NewServer(store, gsi.WithTracker(tracker), gsi.WithProcessor(processor), gsi.WithLiveProjections(projection))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Wait()
	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(strings.Repeat("x", (10<<20)+1)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if projection.calls != 0 {
		t.Fatalf("projection calls=%d", projection.calls)
	}
	if _, err := os.Stat(store.RawPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw file exists: %v", err)
	}
	snap := tracker.Snapshot()
	if snap.RequestCount != 1 || snap.RejectedCount != 1 || snap.AcceptedCount != 0 {
		t.Fatalf("status=%#v", snap)
	}
}

func TestRepeatedPostsAfterSealedFailureReturn503WithoutWritesOrProjections(t *testing.T) {
	rawFile := &sealingRawFile{}
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("sealed-http"), session.WithRawFile(func(string) (session.RawFile, error) { return rawFile, nil }))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tracker := operator.NewTracker(store.SessionID(), now, time.Minute, time.Now)
	projection := &countingProjection{}
	processor := capture.NewProcessor(store, tracker, capture.WithFailureLogger(func(string, string) {}))
	handler := gsi.NewServer(store, gsi.WithTracker(tracker), gsi.WithProcessor(processor), gsi.WithLiveProjections(projection))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Wait()
	for i := 0; i < 3; i++ {
		resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"attempt":1}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d status=%d", i+1, resp.StatusCode)
		}
	}
	if rawFile.writes != 1 {
		t.Fatalf("raw write attempts=%d, want 1", rawFile.writes)
	}
	if projection.calls != 0 {
		t.Fatalf("projection calls=%d", projection.calls)
	}
	snap := tracker.Snapshot()
	if snap.State != operator.StateDegraded || snap.ActiveFailures[operator.SubsystemRaw].Code != "raw_store_sealed" || snap.AcceptedCount != 0 || snap.RawWriteFailureCount != 3 {
		t.Fatalf("status=%#v", snap)
	}
}

func TestStatusHTTPWaitingReceivingStaleAndBoundedErrors(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("status-states"), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	tracker := operator.NewTracker(store.SessionID(), now, 10*time.Second, func() time.Time { return now })
	server := httptest.NewServer(gsi.NewServer(store, gsi.WithTracker(tracker)))
	defer server.Close()
	getStatus := func() (operator.Snapshot, int) {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/status")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var snap operator.Snapshot
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("decode status: %v body=%q", err, body)
		}
		return snap, len(body)
	}
	if snap, _ := getStatus(); snap.State != operator.StateWaiting {
		t.Fatalf("waiting status=%#v", snap)
	}
	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if snap, _ := getStatus(); snap.State != operator.StateReceiving || snap.AcceptedCount != 1 {
		t.Fatalf("receiving status=%#v", snap)
	}
	for i := 0; i < 50; i++ {
		tracker.Failure(operator.SubsystemProfile, "profile_failed", strings.Repeat("safe", 80))
		tracker.Success(operator.SubsystemProfile)
	}
	now = now.Add(10 * time.Second)
	snap, size := getStatus()
	if snap.State != operator.StateStale || len(snap.Errors) != operator.MaxErrors || size > 8192 {
		t.Fatalf("stale/bounded status=%#v size=%d", snap, size)
	}
}

func TestLatestAPIUpdatesAfterValidGSIOnly(t *testing.T) {
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("latest-gsi"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	latest := state.NewLatest()

	server := httptest.NewServer(gsi.NewServer(store, gsi.WithLatest(latest)))
	defer server.Close()

	validBody := `{"provider":{"name":"Dota 2","appid":570},"map":{"game_time":123},"hero":{"team2":{"player0":{"name":"npc_dota_hero_axe","xpos":100,"ypos":200}}}}`
	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(validBody))
	if err != nil {
		t.Fatalf("POST /gsi returned error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid POST status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	deadline := time.Now().Add(time.Second)
	for latest.Snapshot("latest-gsi").SnapshotCount != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	resp, err = http.Get(server.URL + "/api/latest")
	if err != nil {
		t.Fatalf("GET /api/latest returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("latest status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, body)
	}

	var latestBody struct {
		Status        string         `json:"status"`
		SessionID     string         `json:"session_id"`
		SnapshotCount uint64         `json:"snapshot_count"`
		Provider      map[string]any `json:"provider"`
		Map           map[string]any `json:"map"`
		Hero          map[string]any `json:"hero"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&latestBody); err != nil {
		t.Fatalf("decode latest JSON: %v", err)
	}
	if latestBody.Status != "ok" || latestBody.SessionID != "latest-gsi" || latestBody.SnapshotCount != 1 {
		t.Fatalf("unexpected latest metadata: %#v", latestBody)
	}
	if latestBody.Provider["name"] != "Dota 2" || latestBody.Map["game_time"] != float64(123) || latestBody.Hero["team2"] == nil {
		t.Fatalf("latest did not expose posted sections: %#v", latestBody)
	}

	resp, err = http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"provider":`))
	if err != nil {
		t.Fatalf("POST invalid /gsi returned error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("invalid POST status = %d, want 4xx", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/api/latest")
	if err != nil {
		t.Fatalf("GET /api/latest after invalid returned error: %v", err)
	}
	defer resp.Body.Close()
	var afterInvalid struct {
		SnapshotCount uint64 `json:"snapshot_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&afterInvalid); err != nil {
		t.Fatalf("decode latest after invalid JSON: %v", err)
	}
	if afterInvalid.SnapshotCount != 1 {
		t.Fatalf("snapshot count after invalid = %d, want 1", afterInvalid.SnapshotCount)
	}
}

func TestDashboardLoadsFromSameServer(t *testing.T) {
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("dashboard"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	dashboard := http.FileServer(http.FS(fstest.MapFS{
		"index.html": {Data: []byte(`<html><script>fetch('/api/latest')</script></html>`)},
	}))

	server := httptest.NewServer(gsi.NewServer(store, gsi.WithDashboard(dashboard)))
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / returned error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, body)
	}
	if !strings.Contains(string(body), "/api/latest") {
		t.Fatalf("dashboard did not reference /api/latest: %s", body)
	}
}

func TestProfileAPIAndSummaryUpdateAfterValidGSI(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("profile-gsi"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	profiler := profile.NewProfiler()

	server := httptest.NewServer(gsi.NewServer(store, gsi.WithProfiler(profiler)))
	defer server.Close()

	resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"provider":{"name":"Dota 2"},"map":{"game_time":123},"hero":{"team2":{"player0":{"xpos":100,"ypos":200}}}}`))
	if err != nil {
		t.Fatalf("POST /gsi returned error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /gsi status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(server.URL + "/api/profile")
	if err != nil {
		t.Fatalf("GET /api/profile returned error: %v", err)
	}
	defer resp.Body.Close()

	var profileBody struct {
		SnapshotCount uint64 `json:"snapshot_count"`
		Fields        []struct {
			Path      string `json:"path"`
			SeenCount uint64 `json:"seen_count"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profileBody); err != nil {
		t.Fatalf("decode profile JSON: %v", err)
	}
	if profileBody.SnapshotCount != 1 {
		t.Fatalf("snapshot count = %d, want 1", profileBody.SnapshotCount)
	}
	if !profileHasPath(profileBody.Fields, "hero.team2.player0.xpos") {
		t.Fatalf("profile missing hero.team2.player0.xpos: %#v", profileBody.Fields)
	}

	summaryPath := filepath.Join(root, "profile-gsi", "session_summary.md")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read session summary: %v", err)
	}
	if !strings.Contains(string(data), "Hero positions: available") {
		t.Fatalf("summary missing hero position conclusion:\n%s", data)
	}
}

func profileHasPath(fields []struct {
	Path      string `json:"path"`
	SeenCount uint64 `json:"seen_count"`
}, path string) bool {
	for _, field := range fields {
		if field.Path == path && field.SeenCount > 0 {
			return true
		}
	}
	return false
}

func TestAnalyticsAPIsEmptyValidJSON(t *testing.T) {
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("analytics-empty"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	server := httptest.NewServer(gsi.NewServer(store, gsi.WithAnalytics(analytics.NewEngine())))
	defer server.Close()

	for _, endpoint := range []string{"/api/analytics", "/api/events"} {
		resp, err := http.Get(server.URL + endpoint)
		if err != nil {
			t.Fatalf("GET %s returned error: %v", endpoint, err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("%s status = %d, want %d; body=%s", endpoint, resp.StatusCode, http.StatusOK, body)
		}
		var v any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("%s did not decode as JSON: %v; body=%s", endpoint, err, body)
		}
		resp.Body.Close()
	}
}

func TestAnalyticsUpdatesAfterAcceptedSnapshotOnly(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("analytics-live"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	engine := analytics.NewEngine()
	handler := gsi.NewServer(store, gsi.WithAnalytics(engine))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Wait()

	first := `{"provider":{"name":"Dota 2"},"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},"hero":{"team2":{"player0":{"alive":true,"level":6}}}}`
	second := `{"provider":{"name":"Dota 2"},"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":101},"hero":{"team2":{"player0":{"alive":false,"level":7,"respawn_seconds":12}}}}`

	if resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(first)); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("first POST: err=%v status=%v", err, resp)
	} else {
		resp.Body.Close()
	}
	// First accepted snapshot must update tick count but derive no events.
	waitForTickCount(t, engine, 1)
	if got := engine.TickCount(); got != 1 {
		t.Fatalf("tick count after first = %d, want 1", got)
	}
	if ev := engine.Events(); len(ev) != 0 {
		t.Fatalf("expected no events after first tick, got %d", len(ev))
	}

	if resp, err := http.Post(server.URL+"/gsi", "application/json", strings.NewReader(second)); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("second POST: err=%v status=%v", err, resp)
	} else {
		resp.Body.Close()
	}
	waitForTickCount(t, engine, 2)
	if got := engine.TickCount(); got != 2 {
		t.Fatalf("tick count after second = %d, want 2", got)
	}

	resp, err := http.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()
	var events []analytics.Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if !containsEvent(events, analytics.EventHeroDeath) {
		t.Fatalf("expected hero_death event, got %#v", events)
	}

	// Invalid snapshots must not advance analytics state.
	resp, err = http.Post(server.URL+"/gsi", "application/json", strings.NewReader(`{"provider":`))
	if err != nil {
		t.Fatalf("invalid POST: %v", err)
	}
	resp.Body.Close()
	if got := engine.TickCount(); got != 2 {
		t.Fatalf("tick count after invalid = %d, want 2", got)
	}

	// Analytics summary files are materialized under the session dir.
	if _, err := os.Stat(filepath.Join(root, "analytics-live", "analytics_summary.md")); err != nil {
		t.Fatalf("analytics summary not written: %v", err)
	}
}

func TestRestartRebuildsFreshLiveAdaptersForEveryCursorState(t *testing.T) {
	for _, tc := range []struct{ name, cursor string }{
		{name: "valid", cursor: `{"session_id":"restart-live","sequence":2}`},
		{name: "stale", cursor: `{"session_id":"restart-live","sequence":1}`},
		{name: "missing"},
		{name: "corrupt", cursor: `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := session.NewStore(root, session.WithSessionID("restart-live"))
			if err != nil {
				t.Fatal(err)
			}
			first := `{"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},"hero":{"team2":{"player0":{"alive":true,"level":6}}}}`
			second := `{"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":101},"hero":{"team2":{"player0":{"alive":false,"level":7,"respawn_seconds":12}}}}`
			if _, err := store.Append([]byte(first)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append([]byte(second)); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			cursorPath := filepath.Join(root, "restart-live", "live_projection_cursor.json")
			if tc.cursor != "" {
				if err := os.WriteFile(cursorPath, []byte(tc.cursor), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := session.NewStore(root, session.WithSessionID("restart-live"))
			if err != nil {
				t.Fatal(err)
			}
			latest := state.NewLatest()
			profiler := profile.NewProfiler()
			engine := analytics.NewEngine()
			handler := gsi.NewServer(reopened, gsi.WithLatest(latest), gsi.WithProfiler(profiler), gsi.WithAnalytics(engine))
			waitForTickCount(t, engine, 2)
			if got := latest.Snapshot("restart-live"); got.Status != "ok" || got.SnapshotCount != 2 {
				t.Fatalf("latest=%#v", got)
			}
			if got := profiler.Snapshot(); got.SnapshotCount != 2 {
				t.Fatalf("profile=%#v", got)
			}
			if got := engine.Snapshot("restart-live"); got.TickCount != 2 || !containsEvent(got.RecentEvents, analytics.EventHeroDeath) {
				t.Fatalf("analytics=%#v", got)
			}
			handler.Wait()
			_ = reopened.Close()
		})
	}
}

func TestProfileRetryAfterSummaryFailureDoesNotDoubleApplyEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("profile-retry"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.SessionDir(), "session_summary.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	profiler := profile.NewProfiler()
	handler := gsi.NewServer(store, gsi.WithProfiler(profiler))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(`{"hero":{"team2":{"player0":{"alive":true}}}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for profiler.Snapshot().SnapshotCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := profiler.Snapshot().SnapshotCount; got != 1 {
		t.Fatalf("profile retry count=%d, want 1", got)
	}
	handler.Wait()
	_ = store.Close()
}

func TestAnalyticsRetryAfterSummaryFailureDoesNotDoubleApplyTransition(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("analytics-retry"))
	if err != nil {
		t.Fatal(err)
	}
	engine := analytics.NewEngine()
	handler := gsi.NewServer(store, gsi.WithAnalytics(engine))
	first := `{"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},"hero":{"team2":{"player0":{"alive":true,"level":6}}}}`
	second := `{"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":101},"hero":{"team2":{"player0":{"alive":false,"level":7,"respawn_seconds":12}}}}`
	post := func(body string) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	post(first)
	waitForTickCount(t, engine, 1)
	summaryPath := filepath.Join(store.SessionDir(), "analytics_summary.json")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(summaryPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first summary not written")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.Remove(summaryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(summaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	post(second)
	waitForTickCount(t, engine, 2)
	time.Sleep(30 * time.Millisecond)
	snap := engine.Snapshot("analytics-retry")
	if snap.TickCount != 2 || snap.EventCounts[analytics.EventHeroDeath] != 1 {
		t.Fatalf("analytics retry=%#v", snap)
	}
	handler.Wait()
	_ = store.Close()
}

func waitForTickCount(t *testing.T, engine *analytics.Engine, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for engine.TickCount() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func containsEvent(events []analytics.Event, ty string) bool {
	for _, e := range events {
		if e.Type == ty {
			return true
		}
	}
	return false
}
