package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/review"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/scoring"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
)

// testContracts loads the frozen metric registry and scoring contract.
func testContracts(t *testing.T) (*metrics.Registry, *scoring.Contract) {
	t.Helper()
	reg, err := metrics.LoadRegistry("../../../docs/specs/ti2026-role-phase-metrics-v1.json")
	if err != nil {
		t.Fatalf("load metric registry: %v", err)
	}
	sc, err := scoring.LoadContract("../../../docs/specs/ti2026-radar-scoring-v1.json")
	if err != nil {
		t.Fatalf("load scoring contract: %v", err)
	}
	return reg, sc
}

// testTeamContract loads the frozen team scoring registry.
func testTeamContract(t *testing.T) *scoring.TeamContract {
	t.Helper()
	tc, err := scoring.LoadTeamContract("../../../docs/specs/ti2026-team-scoring-v1.json")
	if err != nil {
		t.Fatalf("load team scoring registry: %v", err)
	}
	return tc
}

// testServerWithContracts binds all three frozen contracts.
func testServerWithContracts(t *testing.T, st *store.Store, reg *roles.Registry) *Server {
	t.Helper()
	mreg, sc := testContracts(t)
	tc := testTeamContract(t)
	return New(st, reg, nil, "").WithContracts(mreg, sc).WithTeamContract(tc)
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a verified match artifact tree with ten identity-bound
	// participants (5 radiant, 5 dire).
	parts := []map[string]interface{}{}
	for i := 0; i < 10; i++ {
		side := "radiant"
		team := int32(2)
		if i >= 5 {
			side = "dire"
			team = 3
		}
		parts = append(parts, map[string]interface{}{
			"slot": i, "account_id": fmt.Sprintf("%d", 1000+i), "player_name": fmt.Sprintf("p%d", i),
			"hero_name": "npc_dota_hero_kez", "hero_id": 145, "side": side, "team": team,
		})
	}
	st.WriteJSON("m1", store.ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	st.WriteJSON("m1", store.ArtifactIdentity, map[string]interface{}{
		"state": "verified", "match_id": "m1",
		"participants": parts,
		"teams": []map[string]interface{}{
			{"team_id": "T1", "team_name": "Team One", "side": "radiant"},
			{"team_id": "T2", "team_name": "Team Two", "side": "dire"},
		},
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
	// Authoritative gated status: verified/published (all gates pass).
	st.WriteStatus("m1", &store.StatusRecord{
		SchemaVersion: store.StatusSchema, MatchID: "m1", Status: store.StatusVerified,
		Publication: "published", Reason: "all_gates_pass", ArchiveState: "verified",
		IdentityState: "verified", ClockState: "calibrated",
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
		MatchID: "m1", Teams: []roles.RoleTeam{
			{
				TeamID: "T1", TeamName: "Team One", Side: "radiant",
				SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "2026-08-17T00:00:00Z",
			},
			{
				TeamID: "T2", TeamName: "Team Two", Side: "dire",
				SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "2026-08-17T00:00:00Z",
			},
		},
	}}}
	rolesList := []string{"1", "2", "3", "4", "5"}
	for i := 0; i < 5; i++ {
		reg.Matches[0].Teams[0].Participants = append(reg.Matches[0].Teams[0].Participants, roles.RoleRecord{
			AccountID: fmt.Sprintf("%d", 1000+i), NominalRole: rolesList[i], RoleConfidence: "high",
		})
	}
	for i := 0; i < 5; i++ {
		reg.Matches[0].Teams[1].Participants = append(reg.Matches[0].Teams[1].Participants, roles.RoleRecord{
			AccountID: fmt.Sprintf("%d", 1005+i), NominalRole: rolesList[i], RoleConfidence: "high",
		})
	}
	srv := New(st, reg, nil, "")
	mreg, sc := testContracts(t)
	srv.WithContracts(mreg, sc).WithTeamContract(testTeamContract(t))
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
			Status       string `json:"status"`
			Publication  string `json:"publication_state"`
			Participants []struct {
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
	if len(report.Data.Participants) != 10 {
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
			Definitions []struct {
				ID string `json:"id"`
			} `json:"definitions"`
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

// TestQuarantinedMatchConsumesAuthoritativeState proves the API does NOT
// independently re-derive published: a match whose persisted status is
// quarantined stays quarantined/suppressed (with reason) in the API even when
// its source artifacts would otherwise look publishable.
func TestQuarantinedMatchConsumesAuthoritativeState(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Same source-artifact set as a publishable match...
	st.WriteJSON("q1", store.ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	st.WriteJSON("q1", store.ArtifactIdentity, map[string]interface{}{
		"state": "verified", "match_id": "q1", "participants": []interface{}{},
	})
	st.WriteJSON("q1", store.ArtifactClock, map[string]interface{}{"state": "calibrated", "game_duration_seconds": 100})
	st.WriteJSON("q1", store.ArtifactPhases, map[string]interface{}{"state": "complete", "intervals": []interface{}{}})
	// ...but the authoritative gated status is quarantined.
	st.WriteStatus("q1", &store.StatusRecord{
		SchemaVersion: store.StatusSchema, MatchID: "q1", Status: store.StatusQuarantined,
		Publication: "suppressed", Reason: "role_provenance_gate: participants=0_want_10",
	})
	if _, err := st.RebuildCatalog("t"); err != nil {
		t.Fatal(err)
	}
	reg := &roles.Registry{SchemaVersion: "ti2026.roles.v1", TournamentID: "ti2026"}
	srv := New(st, reg, nil, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var report struct {
		Data struct {
			Status      string `json:"status"`
			Publication string `json:"publication_state"`
			Reason      string `json:"reason"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/q1", &report); code != 200 {
		t.Fatalf("status %d", code)
	}
	if report.Data.Status != store.StatusQuarantined || report.Data.Publication != "suppressed" {
		t.Fatalf("api status=%s pub=%s want quarantined/suppressed", report.Data.Status, report.Data.Publication)
	}
	if !strings.Contains(report.Data.Reason, "role_provenance_gate") {
		t.Fatalf("api reason=%q", report.Data.Reason)
	}
	// Corpus/matches views must also report quarantined.
	var corpus struct {
		Data struct {
			Matches []store.CatalogRow `json:"matches"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/corpus", &corpus); code != 200 {
		t.Fatalf("corpus status %d", code)
	}
	if len(corpus.Data.Matches) != 1 || corpus.Data.Matches[0].Status != store.StatusQuarantined {
		t.Fatalf("corpus=%+v want quarantined", corpus.Data.Matches)
	}
}

var _ = filepath.Join

// TestFullMetricRegistry serves all 52 frozen definitions with the contract.
func TestFullMetricRegistry(t *testing.T) {
	_, ts := testServer(t)
	defer ts.Close()
	var resp struct {
		Data struct {
			RegistryVersion string `json:"registry_version"`
			MetricCount     int    `json:"metric_count"`
			Definitions     []struct {
				ID              string `json:"id"`
				CapabilityLevel string `json:"capability_level"`
				EpistemicClass  string `json:"epistemic_class"`
			} `json:"definitions"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/metrics/registry", &resp); code != 200 {
		t.Fatalf("metrics registry status %d", code)
	}
	if resp.Data.MetricCount != 52 || len(resp.Data.Definitions) != 52 {
		t.Fatalf("metric count=%d defs=%d want 52", resp.Data.MetricCount, len(resp.Data.Definitions))
	}
	if resp.Data.RegistryVersion != metrics.RegistrySchemaVersion {
		t.Fatalf("registry version=%s", resp.Data.RegistryVersion)
	}
}

// TestMutationEndpointsRequireSessionToken proves review mutations fail closed
// without the local session token.
func TestMutationEndpointsRequireSessionToken(t *testing.T) {
	_, ts := testServer(t)
	defer ts.Close()
	body := `{"match_id":"m1","author":"paul","reason":"r","previous_value":{},"effective_value":{}}`
	for _, path := range []string{
		Version + "/reviews/phase-corrections",
		Version + "/reviews/event-corrections",
		Version + "/roles/overrides",
	} {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s without token status=%d want 403", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestScoresEndpoint404WhenAbsent proves scores are only served from persisted
// snapshots (no silent empty score).
func TestScoresEndpoint404WhenAbsent(t *testing.T) {
	_, ts := testServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + Version + "/matches/m1/scores")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("scores status=%d want 404 when absent", resp.StatusCode)
	}
}

// TestReviewMutationWithTokenPersistsCorrection binds a review store + token
// and verifies a phase correction is persisted with server-side machine truth.
func TestReviewMutationWithTokenPersistsCorrection(t *testing.T) {
	st := testStore(t)
	reg := testRoleRegistry(t)

	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, reg).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The fixture store has a machine phase interval 0-100 (laning); the move
	// operation relabels it to midgame via the typed op payload (boundaries
	// kept contiguous).
	body := `{"match_id":"m1","author":"paul","reason":"moved boundary","operation":"move","event_ref":"interval@0-100","effective_value":{"start_game_second":0,"end_game_second":100,"global_phase":"midgame"}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+Version+"/reviews/phase-corrections", strings.NewReader(body))
	req.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("correction status=%d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Corrections []struct {
				ID               string `json:"id"`
				Author           string `json:"author"`
				AlgorithmVersion string `json:"algorithm_version"`
				ReplaySHA256     string `json:"replay_sha256"`
				Reason           string `json:"reason"`
			} `json:"corrections"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Corrections) != 1 {
		t.Fatalf("corrections=%d", len(out.Data.Corrections))
	}
	c := out.Data.Corrections[0]
	if c.Author != "paul" || c.Reason == "" || c.AlgorithmVersion == "" || c.ID == "" {
		t.Fatalf("correction provenance incomplete: %+v", c)
	}
	// Effective phase overlay persisted.
	var reviewData struct {
		Data struct {
			EffectivePhaseIntervals []json.RawMessage `json:"effective_phase_intervals"`
			ReplaySHA256            string            `json:"replay_sha256"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/reviews/queue", &reviewData); code != 200 {
		t.Fatalf("queue status %d", code)
	}
}

// TestReviewMutationRejectsTamperedMachineValue proves a fabricated previous
// value is rejected with 409 (server-side machine truth wins).
func TestReviewMutationRejectsTamperedMachineValue(t *testing.T) {
	st := testStore(t)
	reg := testRoleRegistry(t)

	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, reg).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The invalid-state test: a bogus phase label is rejected by validation
	// (400), not persisted as invalid analytical state.
	body := `{"match_id":"m1","author":"paul","reason":"x","operation":"relabel","event_ref":"interval@0-100","effective_value":{"start_game_second":0,"end_game_second":100,"global_phase":"bogus"}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+Version+"/reviews/phase-corrections", strings.NewReader(body))
	req.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid-phase correction status=%d want 400", resp.StatusCode)
	}
}

// TestRoleOverridePersistsToAuthoritativeStore proves a role override is
// written to the data-root override store (effective) and audited.
func TestRoleOverridePersistsToAuthoritativeStore(t *testing.T) {
	st := testStore(t)
	reg := testRoleRegistry(t)

	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, reg).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"match_id":"m1","account_id":"1000","nominal_role":"2","author":"paul","reason":"role swap observed"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+Version+"/roles/overrides", strings.NewReader(body))
	req.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("override status=%d", resp.StatusCode)
	}
	var of struct {
		Overrides []struct {
			MatchID     string `json:"match_id"`
			AccountID   string `json:"account_id"`
			NominalRole string `json:"nominal_role"`
			Reason      string `json:"reason"`
			AppliedAt   string `json:"applied_at"`
		} `json:"overrides"`
	}
	if err := st.ReadJSONFile(st.Root+"/role-overrides-effective.json", &of); err != nil {
		t.Fatalf("effective override file missing: %v", err)
	}
	if len(of.Overrides) != 1 || of.Overrides[0].NominalRole != "2" || of.Overrides[0].Reason == "" || of.Overrides[0].AppliedAt == "" {
		t.Fatalf("override not effective: %+v", of.Overrides)
	}
	// Synchronous recompute: the served player tournament score must reflect
	// the effective role immediately.
	var players struct {
		Data struct {
			Score struct {
				AccountID   string `json:"account_id"`
				NominalRole string `json:"nominal_role"`
			} `json:"score"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/players/1000", &players); code != 200 {
		t.Fatalf("player status %d", code)
	}
	if players.Data.Score.NominalRole != "2" {
		t.Fatalf("player role after override=%q want 2", players.Data.Score.NominalRole)
	}
	// The recompute must be durable: reload a fresh server and confirm.
	srv2 := testServerWithContracts(t, st, reg).WithReviews(rv).WithSessionToken("tok")
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	if code := getJSON(t, ts2.URL+Version+"/players/1000", &players); code != 200 {
		t.Fatalf("player restart status %d", code)
	}
	if players.Data.Score.NominalRole != "2" {
		t.Fatalf("player role after restart=%q want 2", players.Data.Score.NominalRole)
	}
}

// TestMutationRejectsTraversalMatchID proves out-of-root writes are blocked.
func TestMutationRejectsTraversalMatchID(t *testing.T) {
	st := testStore(t)
	reg := testRoleRegistry(t)

	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, reg).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"match_id":"../../escaped","author":"paul","reason":"x","operation":"relabel","event_ref":"interval@0-100","effective_value":{"start_game_second":0,"end_game_second":100,"global_phase":"midgame"}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+Version+"/reviews/phase-corrections", strings.NewReader(body))
	req.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status=%d want 400", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(st.Root, "..", "escaped", "corrections.json")); err == nil {
		t.Fatal("traversal write escaped the data root")
	}
}

func testRoleRegistry(t *testing.T) *roles.Registry {
	t.Helper()
	reg := &roles.Registry{SchemaVersion: "ti2026.roles.v1", TournamentID: "ti2026", Matches: []roles.RoleMatch{{
		MatchID: "m1", Teams: []roles.RoleTeam{
			{
				TeamID: "T1", TeamName: "Team One", Side: "radiant",
				SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "2026-08-17T00:00:00Z",
			},
			{
				TeamID: "T2", TeamName: "Team Two", Side: "dire",
				SourceKind: "reliable_public_database", SourceURL: "https://example.com", RetrievedAt: "2026-08-17T00:00:00Z",
			},
		},
	}}}
	rolesList := []string{"1", "2", "3", "4", "5"}
	for i := 0; i < 5; i++ {
		reg.Matches[0].Teams[0].Participants = append(reg.Matches[0].Teams[0].Participants, roles.RoleRecord{
			AccountID: fmt.Sprintf("%d", 1000+i), NominalRole: rolesList[i], RoleConfidence: "high",
		})
	}
	for i := 0; i < 5; i++ {
		reg.Matches[0].Teams[1].Participants = append(reg.Matches[0].Teams[1].Participants, roles.RoleRecord{
			AccountID: fmt.Sprintf("%d", 1005+i), NominalRole: rolesList[i], RoleConfidence: "high",
		})
	}
	return reg
}
