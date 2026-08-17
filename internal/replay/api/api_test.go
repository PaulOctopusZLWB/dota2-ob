package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a verified match artifact tree.
	st.WriteJSON("m1", store.ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	st.WriteJSON("m1", store.ArtifactIdentity, map[string]interface{}{
		"state": "verified", "match_id": "m1",
		"participants": []map[string]interface{}{
			{"slot": 0, "account_id": "1000", "player_name": "p1", "hero_name": "npc_dota_hero_kez", "hero_id": 145, "side": "radiant"},
		},
		"teams": []map[string]interface{}{{"team_id": "T1", "team_name": "Team One", "side": "radiant"}},
	})
	st.WriteJSON("m1", store.ArtifactClock, map[string]interface{}{"state": "calibrated", "game_duration_seconds": 100})
	st.WriteJSON("m1", store.ArtifactFactsSummary, map[string]interface{}{"families": []interface{}{}})
	st.WriteJSON("m1", store.ArtifactPhases, map[string]interface{}{
		"state": "complete", "intervals": []map[string]interface{}{{"global_phase": "laning", "start_game_second": 0, "end_game_second": 100}},
	})
	st.WriteJSON("m1", store.ArtifactEpisodes, map[string]interface{}{
		"episodes":    []interface{}{},
		"unavailable": []interface{}{map[string]interface{}{"kind": "lane_segment", "reason": "no_geometry"}},
	})
	st.WriteJSON("m1", store.ArtifactMetrics, map[string]interface{}{
		"values":      []interface{}{map[string]interface{}{"metric_id": "kills", "account_id": "1000", "value": 3}},
		"unavailable": []interface{}{map[string]interface{}{"metric_id": "wards_placed", "account_id": "1000", "unavailable_reason": "no_observations"}},
	})
	st.WriteJSON("m1", store.ArtifactInput, map[string]interface{}{"category": "test"})
	st.WriteJSON("m1", store.ArtifactCanonical, map[string]interface{}{
		"schema_version": "replay.report.v1", "tree_sha256": "abc123", "input_fingerprint": map[string]interface{}{"match_id": "m1"},
	})
	if _, err := st.RebuildCatalog("2026-08-17T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	return st
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	st := testStore(t)
	reg := &roles.Registry{SchemaVersion: "ti2026.roles.v1", TournamentID: "ti2026", Matches: []roles.RoleMatch{{
		MatchID: "m1", Teams: []roles.RoleTeam{{
			TeamID: "T1", TeamName: "Team One", Side: "radiant",
			SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "2026-08-17T00:00:00Z",
			Participants: []roles.RoleRecord{{AccountID: "1000", NominalRole: "1", RoleConfidence: "high"}},
		}},
	}}}
	srv := New(st, reg, nil, "")
	ts := httptest.NewServer(srv.Handler())
	return srv, ts
}

func getJSON(t *testing.T, url string, v interface{}) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestEndpoints(t *testing.T) {
	_, ts := testServer(t)
	defer ts.Close()

	var corpus struct {
		Data struct {
			Matches []store.CatalogRow `json:"matches"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/corpus", &corpus); code != 200 {
		t.Fatalf("corpus status %d", code)
	}
	if len(corpus.Data.Matches) != 1 {
		t.Fatalf("corpus matches=%d", len(corpus.Data.Matches))
	}

	var matches struct {
		Data []store.CatalogRow `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches", &matches); code != 200 {
		t.Fatalf("matches status %d", code)
	}
	if matches.Data[0].Status != store.StatusVerified {
		t.Fatalf("catalog status=%s", matches.Data[0].Status)
	}

	var report struct {
		Data struct {
			Status         string `json:"status"`
			Publication    string `json:"publication_state"`
			Participants   []struct {
				AccountID   string `json:"account_id"`
				NominalRole string `json:"nominal_role"`
				RoleSource  string `json:"role_source_kind"`
			} `json:"participants"`
			UnavailableReasons []string `json:"unavailable_reasons"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1", &report); code != 200 {
		t.Fatalf("match status %d", code)
	}
	if report.Data.Status != store.StatusVerified || report.Data.Publication != "published" {
		t.Fatalf("match status=%s pub=%s", report.Data.Status, report.Data.Publication)
	}
	if len(report.Data.Participants) != 1 {
		t.Fatalf("participants=%d", len(report.Data.Participants))
	}
	p := report.Data.Participants[0]
	if p.AccountID != "1000" || p.NominalRole != "1" || p.RoleSource != "reliable_public_database" {
		t.Fatalf("participant=%+v", p)
	}
	if len(report.Data.UnavailableReasons) == 0 {
		t.Fatal("expected unavailable reasons")
	}

	var timeline struct {
		Data struct {
			Phases struct {
				Intervals []json.RawMessage `json:"intervals"`
			} `json:"phases"`
			Episodes struct {
				Unavailable []json.RawMessage `json:"unavailable"`
			} `json:"episodes"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1/timeline", &timeline); code != 200 {
		t.Fatalf("timeline status %d", code)
	}
	if len(timeline.Data.Phases.Intervals) != 1 {
		t.Fatalf("phase intervals=%d", len(timeline.Data.Phases.Intervals))
	}

	var metrics struct {
		Data struct {
			Definitions []struct{ ID string `json:"id"` } `json:"definitions"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/metrics/registry", &metrics); code != 200 {
		t.Fatalf("metrics status %d", code)
	}
	if len(metrics.Data.Definitions) == 0 {
		t.Fatal("no metric definitions")
	}

	var roleResp struct {
		Data struct {
			SchemaVersion string `json:"schema_version"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/roles", &roleResp); code != 200 {
		t.Fatalf("roles status %d", code)
	}
	if roleResp.Data.SchemaVersion != "ti2026.roles.v1" {
		t.Fatalf("roles schema=%s", roleResp.Data.SchemaVersion)
	}

	var players struct {
		Data map[string]interface{} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/players/1000", &players); code != 200 {
		t.Fatalf("players status %d", code)
	}

	var teams struct {
		Data map[string]interface{} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/teams/T1", &teams); code != 200 {
		t.Fatalf("teams status %d", code)
	}

	// Method not allowed on POST.
	resp, err := http.Post(ts.URL+Version+"/matches", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST matches status=%d", resp.StatusCode)
	}
}

var _ = filepath.Join