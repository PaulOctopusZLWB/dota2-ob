package gsi_test

import (
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

	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

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
