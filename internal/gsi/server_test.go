package gsi_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
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
