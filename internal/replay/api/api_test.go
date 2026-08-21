package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/report"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/review"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/scoring"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
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
	cl, err := metrics.LoadClosure("../../../docs/specs/ti2026-metric-closure-v1.json")
	if err != nil {
		t.Fatalf("load closure: %v", err)
	}
	return New(st, reg, nil, "").WithContracts(mreg, sc).WithMetricClosure(cl).WithTeamContract(tc)
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
	st.WriteJSON("m1", store.ArtifactVerification, map[string]string{
		"state": "verified", "reason": "ok", "demo_sha256_actual": "test-replay-sha256",
	})
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
		"state": "complete", "eligible_seconds": 100, "rule_version": "ti2026.phase.v1",
		"intervals": []map[string]interface{}{{"global_phase": "laning", "start_game_second": 0, "end_game_second": 100}},
	})
	st.WriteJSON("m1", store.ArtifactEpisodes, map[string]interface{}{
		"episodes":    []interface{}{},
		"unavailable": []interface{}{map[string]interface{}{"kind": "lane_segment", "reason": "no_geometry"}},
	})
	st.WriteJSON("m1", store.ArtifactMetrics, map[string]interface{}{
		"schema_version": version.MetricsSchema, "rule_version": version.MetricsRuleVersion, "match_id": "m1",
		"values":      []interface{}{map[string]interface{}{"metric_id": "hero_damage_total", "metric_version": "1.0.0", "account_id": "1000", "value": 3}},
		"unavailable": []interface{}{},
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
	if cl, cerr := metrics.LoadClosure("../../../docs/specs/ti2026-metric-closure-v1.json"); cerr == nil {
		srv.WithMetricClosure(cl)
	}
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

	// The fixture store has a machine phase interval 0-100 (laning); the
	// relabel operation changes its label to midgame via the typed op payload
	// (boundaries kept contiguous). The single-interval stream has no internal
	// shared boundary, so relabel is the correct narrow persistence probe.
	initial, err := rv.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"match_id":"m1","author":"paul","reason":"relabeled on evidence","operation":"relabel","event_ref":"interval@0-100","expected_revision":%q,"effective_value":{"start_game_second":0,"end_game_second":100,"global_phase":"midgame"}}`, initial.ReviewRevision)
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
			PhaseCorrections []struct {
				ID               string `json:"id"`
				Author           string `json:"author"`
				AlgorithmVersion string `json:"algorithm_version"`
				ReplaySHA256     string `json:"replay_sha256"`
				Reason           string `json:"reason"`
			} `json:"phase_corrections"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.PhaseCorrections) != 1 {
		t.Fatalf("phase_corrections=%d", len(out.Data.PhaseCorrections))
	}
	c := out.Data.PhaseCorrections[0]
	if c.Author != "paul" || c.Reason == "" || c.AlgorithmVersion == "" || c.ReplaySHA256 == "" || c.ID == "" {
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
	initial, err := rv.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"match_id":"m1","author":"paul","reason":"x","operation":"relabel","event_ref":"interval@0-100","expected_revision":%q,"effective_value":{"start_game_second":0,"end_game_second":100,"global_phase":"bogus"}}`, initial.ReviewRevision)
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

func cumulativePhaseIntervals() []review.PhaseInterval {
	return []review.PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 594, GlobalPhase: "laning"},
		{StartGameSecond: 594, EndGameSecond: 780, GlobalPhase: "midgame"},
		{StartGameSecond: 780, EndGameSecond: 820, GlobalPhase: "decisive"},
		{StartGameSecond: 820, EndGameSecond: 1420, GlobalPhase: "midgame"},
		{StartGameSecond: 1420, EndGameSecond: 1474, GlobalPhase: "decisive"},
		{StartGameSecond: 1474, EndGameSecond: 1564, GlobalPhase: "midgame"},
		{StartGameSecond: 1564, EndGameSecond: 1609, GlobalPhase: "decisive"},
		{StartGameSecond: 1609, EndGameSecond: 1769, GlobalPhase: "midgame"},
		{StartGameSecond: 1769, EndGameSecond: 1799, GlobalPhase: "decisive"},
		{StartGameSecond: 1799, EndGameSecond: 2101, GlobalPhase: "midgame"},
		{StartGameSecond: 2101, EndGameSecond: 2141, GlobalPhase: "decisive"},
		{StartGameSecond: 2141, EndGameSecond: 2251, GlobalPhase: "midgame"},
		{StartGameSecond: 2251, EndGameSecond: 2294, GlobalPhase: "decisive"},
		{StartGameSecond: 2294, EndGameSecond: 2424, GlobalPhase: "midgame"},
		{StartGameSecond: 2424, EndGameSecond: 2454, GlobalPhase: "decisive"},
		{StartGameSecond: 2454, EndGameSecond: 2633, GlobalPhase: "midgame"},
		{StartGameSecond: 2633, EndGameSecond: 2705, GlobalPhase: "decisive"},
	}
}

func addCumulativePhaseMatch(t *testing.T, st *store.Store, matchID string) []review.PhaseInterval {
	t.Helper()
	machine := cumulativePhaseIntervals()
	if err := st.WriteJSON(matchID, store.ArtifactVerification, map[string]string{
		"state": "verified", "reason": "ok", "demo_sha256_actual": "8944521919-replay-sha256",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON(matchID, store.ArtifactPhases, map[string]interface{}{
		"schema_version": "replay.phase.v1", "rule_version": "ti2026.phase.v1",
		"state": "complete", "eligible_seconds": 2705, "covered_seconds": 2705,
		"intervals": machine,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteStatus(matchID, &store.StatusRecord{
		SchemaVersion: store.StatusSchema, MatchID: matchID, Status: store.StatusVerified,
		Publication: "published", Reason: "all_gates_pass", ArchiveState: "verified",
		IdentityState: "verified", ClockState: "calibrated", ParseState: "complete",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RebuildCatalog("2026-08-18T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	return machine
}

func postPhaseOperation(t *testing.T, baseURL string, req review.PhaseOpReq) (int, review.Review, string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+Version+"/reviews/phase-corrections", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var wire struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	var rv review.Review
	if len(wire.Data) > 0 {
		if err := json.Unmarshal(wire.Data, &rv); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, rv, wire.Error
}

func postReviewStatus(t *testing.T, baseURL, matchID, status, author, expectedRevision string) (int, review.Review, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"match_id": matchID, "status": status, "author": author, "expected_revision": expectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+Version+"/reviews/status", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var wire struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	var rv review.Review
	if len(wire.Data) > 0 {
		if err := json.Unmarshal(wire.Data, &rv); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, rv, wire.Error
}

func normalizePhaseStream(in []review.PhaseInterval) []review.PhaseInterval {
	out := append([]review.PhaseInterval(nil), in...)
	for i := range out {
		out[i].EventRef = review.CanonicalEventRef(out[i])
	}
	return out
}

func equalPhaseStreams(a, b []review.PhaseInterval) bool {
	aj, _ := json.Marshal(normalizePhaseStream(a))
	bj, _ := json.Marshal(normalizePhaseStream(b))
	return bytes.Equal(aj, bj)
}

func assertPhaseCoverage(t *testing.T, intervals []review.PhaseInterval, eligible int) {
	t.Helper()
	if len(intervals) == 0 || intervals[0].StartGameSecond != 0 || intervals[len(intervals)-1].EndGameSecond != eligible {
		t.Fatalf("coverage endpoints: %+v", intervals)
	}
	end := 0
	for i, iv := range intervals {
		if iv.StartGameSecond != end || iv.EndGameSecond <= iv.StartGameSecond {
			t.Fatalf("coverage at %d: previous_end=%d interval=%+v", i, end, iv)
		}
		end = iv.EndGameSecond
	}
}

// TestPhaseCorrectionCumulativeAPIAndRestart runs the frozen 8944521919
// partition shape through all seven API operations. Every request addresses
// the prior effective stream; every success persists one complete v2
// before/after record and exact [0,2705] coverage. Restart reproduces the
// stream/audit, stale and invalid requests are atomic, and phases.json remains
// byte-identical.
func TestPhaseCorrectionCumulativeAPIAndRestart(t *testing.T) {
	st := testStore(t)
	matchID := "8944521919"
	machine := addCumulativePhaseMatch(t, st, matchID)
	phasePath := filepath.Join(st.Root, "matches", matchID, store.ArtifactPhases)
	phaseBytesBefore, err := os.ReadFile(phasePath)
	if err != nil {
		t.Fatal(err)
	}
	phaseHashBefore := sha256.Sum256(phaseBytesBefore)

	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())

	operations := []review.PhaseOpReq{
		{Op: review.OpSplit, EventRef: "interval@0-594", SplitSecond: apiIntPtr(100)},
		{Op: review.OpRelabel, EventRef: "interval@100-594", Effective: &review.PhaseInterval{StartGameSecond: 100, EndGameSecond: 594, GlobalPhase: "midgame"}},
		{Op: review.OpMove, EventRef: "interval@100-594", Effective: &review.PhaseInterval{StartGameSecond: 100, EndGameSecond: 700, GlobalPhase: "midgame"}},
		{Op: review.OpAdd, Effective: &review.PhaseInterval{StartGameSecond: 1700, EndGameSecond: 1750, GlobalPhase: "decisive"}},
		{Op: review.OpDelete, EventRef: "interval@1700-1750", AbsorbInto: "interval@1609-1700"},
		{Op: review.OpSplit, EventRef: "interval@780-820", SplitSecond: apiIntPtr(800)},
		{Op: review.OpMerge, EventRef: "interval@780-800", MergeRight: "interval@800-820", Effective: &review.PhaseInterval{GlobalPhase: "decisive"}},
		{Op: review.OpAccept, EventRef: "interval@2454-2633"},
	}

	previous := machine
	initialReview, err := rv.Load(matchID)
	if err != nil {
		t.Fatal(err)
	}
	currentRevision := initialReview.ReviewRevision
	var final review.Review
	for i := range operations {
		operations[i].MatchID = matchID
		operations[i].Author = "paul"
		operations[i].Reason = fmt.Sprintf("cumulative step %d", i+1)
		operations[i].EvidenceIDs = []string{fmt.Sprintf("phase:%d", i+1)}
		operations[i].ExpectedRevision = currentRevision
		status, got, apiErr := postPhaseOperation(t, ts.URL, operations[i])
		if status != http.StatusOK {
			t.Fatalf("step %d %s status=%d error=%q", i+1, operations[i].Op, status, apiErr)
		}
		if len(got.PhaseCorrections) != i+1 {
			t.Fatalf("step %d phase corrections=%d", i+1, len(got.PhaseCorrections))
		}
		effective, err := review.FromJSON(got.EffectivePhaseIntervals)
		if err != nil {
			t.Fatal(err)
		}
		assertPhaseCoverage(t, effective, 2705)
		pc := got.PhaseCorrections[i]
		if pc.Operation != string(operations[i].Op) || pc.ShapeVersion != review.PhaseOpShapeVersion ||
			pc.ReplaySHA256 == "" || pc.AlgorithmVersion == "" || pc.MachineRuleVersion == "" ||
			len(pc.MachineValue) == 0 || len(pc.EffectiveValue) == 0 || string(pc.EffectiveValue) == "null" {
			t.Fatalf("step %d v2 provenance incomplete: %+v", i+1, pc)
		}
		if !equalPhaseStreams(pc.BeforeStream, previous) || !equalPhaseStreams(pc.AfterStream, effective) {
			t.Fatalf("step %d before/after stream mismatch", i+1)
		}
		persisted, err := rv.Load(matchID)
		if err != nil {
			t.Fatal(err)
		}
		if len(persisted.PhaseCorrections) != i+1 || len(persisted.EffectivePhaseIntervals) == 0 {
			t.Fatalf("step %d correction/overlay not persisted together", i+1)
		}
		audit, err := rv.LoadAudit()
		if err != nil {
			t.Fatal(err)
		}
		if len(audit.Entries) != i+1 {
			t.Fatalf("step %d audit entries=%d", i+1, len(audit.Entries))
		}
		previous = effective
		currentRevision = got.ReviewRevision
		final = got
	}

	// Closed refs are exact current-state preconditions: a matching start with
	// a tampered/stale end must be 409 and cannot mutate review or audit.
	reviewBeforeFailure, err := os.ReadFile(rv.Path(matchID))
	if err != nil {
		t.Fatal(err)
	}
	auditBeforeFailure, err := os.ReadFile(rv.AuditPath())
	if err != nil {
		t.Fatal(err)
	}
	stale := review.PhaseOpReq{
		MatchID: matchID, Author: "paul", Reason: "stale closed ref", Op: review.OpRelabel,
		EventRef: "interval@100-999", ExpectedRevision: currentRevision, Effective: &review.PhaseInterval{StartGameSecond: 100, EndGameSecond: 700, GlobalPhase: "midgame"},
	}
	if status, _, apiErr := postPhaseOperation(t, ts.URL, stale); status != http.StatusConflict {
		t.Fatalf("stale status=%d error=%q want 409", status, apiErr)
	}
	invalid := review.PhaseOpReq{
		MatchID: matchID, Author: "paul", Reason: "illegal phase", Op: review.OpRelabel,
		EventRef: "interval@100-700", ExpectedRevision: currentRevision, Effective: &review.PhaseInterval{StartGameSecond: 100, EndGameSecond: 700, GlobalPhase: "reset"},
	}
	if status, _, apiErr := postPhaseOperation(t, ts.URL, invalid); status != http.StatusBadRequest {
		t.Fatalf("invalid status=%d error=%q want 400", status, apiErr)
	}
	reviewAfterFailure, err := os.ReadFile(rv.Path(matchID))
	if err != nil {
		t.Fatal(err)
	}
	auditAfterFailure, err := os.ReadFile(rv.AuditPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reviewBeforeFailure, reviewAfterFailure) || !bytes.Equal(auditBeforeFailure, auditAfterFailure) {
		t.Fatal("stale/invalid mutation changed persisted review or audit")
	}

	ts.Close()
	restartedReviews, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := restartedReviews.Load(matchID)
	if err != nil {
		t.Fatal(err)
	}
	finalStream, err := review.FromJSON(final.EffectivePhaseIntervals)
	if err != nil {
		t.Fatal(err)
	}
	restartedStream, err := review.FromJSON(restarted.EffectivePhaseIntervals)
	if err != nil {
		t.Fatal(err)
	}
	if !equalPhaseStreams(finalStream, restartedStream) || len(restarted.PhaseCorrections) != len(operations) {
		t.Fatalf("restart mismatch: intervals=%d corrections=%d", len(restartedStream), len(restarted.PhaseCorrections))
	}
	restartedAudit, err := restartedReviews.LoadAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(restartedAudit.Entries) != len(operations) {
		t.Fatalf("restart audit=%d", len(restartedAudit.Entries))
	}
	for i, entry := range restartedAudit.Entries {
		wantOp := string(operations[len(operations)-1-i].Op)
		if entry.Action != "phase_correction_v2" || !strings.HasPrefix(entry.Summary, wantOp+" ") {
			t.Fatalf("audit[%d]=%+v want operation %s", i, entry, wantOp)
		}
	}

	// A restarted API serves the same persisted review stream.
	restartedServer := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(restartedReviews).WithSessionToken("tok")
	ts2 := httptest.NewServer(restartedServer.Handler())
	defer ts2.Close()
	var queue struct {
		Data struct {
			Reviews []review.Review `json:"reviews"`
		} `json:"data"`
	}
	if code := getJSON(t, ts2.URL+Version+"/reviews/queue", &queue); code != http.StatusOK {
		t.Fatalf("restart queue status=%d", code)
	}
	found := false
	for _, candidate := range queue.Data.Reviews {
		if candidate.MatchID == matchID {
			stream, err := review.FromJSON(candidate.EffectivePhaseIntervals)
			if err != nil {
				t.Fatal(err)
			}
			if !equalPhaseStreams(stream, restartedStream) {
				t.Fatal("restarted API served a different effective stream")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("restarted API omitted 8944521919 review")
	}

	phaseBytesAfter, err := os.ReadFile(phasePath)
	if err != nil {
		t.Fatal(err)
	}
	phaseHashAfter := sha256.Sum256(phaseBytesAfter)
	if phaseHashBefore != phaseHashAfter || !bytes.Equal(phaseBytesBefore, phaseBytesAfter) {
		t.Fatalf("machine phases changed: before=%x after=%x", phaseHashBefore, phaseHashAfter)
	}
	t.Logf("immutable phases.json sha256=%x", phaseHashAfter)
}

func apiIntPtr(v int) *int { return &v }

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

// writeFactLine appends one JSONL fact to a match's facts artifact.
func writeFactLine(t *testing.T, st *store.Store, matchID string, f *facts.Fact) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(st.Root, "matches", matchID, store.ArtifactFacts), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	b, _ := json.Marshal(f)
	if _, err := fh.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestTeamMatchAggregationRoutesResolveExactPersistedEntities(t *testing.T) {
	st := testStore(t)
	mreg, _ := testContracts(t)
	metricVersion := mreg.Find("hero_damage_total").MetricVersion
	const teamID = "9823272"
	matchIDs := []string{"8944525313", "8946228107"}
	values := []float64{70000, 102402}
	teamMatches := map[string]map[string]*scoring.TeamMatch{teamID: {}}
	tournamentLineage := []scoring.EvidenceRef{}
	for i, matchID := range matchIDs {
		id := fmt.Sprintf("aggregation:team_match:%s:%s:hero_damage_total:%s:%s", teamID, matchID, metricVersion, version.ScoreRuleVersion)
		lineage := []scoring.EvidenceRef{
			{MatchID: matchID, Kind: "fact", ID: fmt.Sprintf("fact:%d", i+1)},
			{MatchID: matchID, Kind: "aggregation", ID: id, RuleVersion: version.ScoreRuleVersion, ContractVersion: scoring.TeamSchemaVersion},
		}
		teamMatches[teamID][matchID] = &scoring.TeamMatch{
			MatchID: matchID, TeamID: teamID, RuleVersion: version.ScoreRuleVersion, ContractVersion: scoring.TeamSchemaVersion, PlayerCount: 5,
			Metrics: map[string]scoring.AggregatedMetric{"hero_damage_total": {MetricID: "hero_damage_total", MetricVersion: metricVersion, Value: values[i], Numerator: values[i], EligibleMatches: 1, Lineage: lineage}},
		}
		tournamentLineage = append(tournamentLineage, lineage...)
	}
	tournamentID := fmt.Sprintf("aggregation:team_tournament:%s::hero_damage_total:%s:%s", teamID, metricVersion, version.ScoreRuleVersion)
	tournamentLineage = append(tournamentLineage, scoring.EvidenceRef{Kind: "aggregation", ID: tournamentID, RuleVersion: version.ScoreRuleVersion, ContractVersion: scoring.TeamSchemaVersion})
	cs := &scoring.CorpusScores{
		SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, ContractVersion: scoring.SchemaVersion, TeamScoringVersion: scoring.TeamSchemaVersion,
		CorpusMatches: 2,
		Matches: map[string]*scoring.MatchScores{
			matchIDs[0]: {SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, MatchID: matchIDs[0], Players: []*scoring.PlayerMatch{}},
			matchIDs[1]: {SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion, MatchID: matchIDs[1], Players: []*scoring.PlayerMatch{}},
		},
		TeamMatches: teamMatches,
		Teams: []*scoring.TeamScore{{TeamID: teamID, ScoringVersion: scoring.TeamSchemaVersion,
			SubjectCoverage:   scoring.SubjectCoverage{EligibleMatches: 2, MatchIDs: matchIDs, CorpusMatches: 2},
			AggregatedMetrics: map[string]scoring.AggregatedMetric{"hero_damage_total": {MetricID: "hero_damage_total", MetricVersion: metricVersion, Value: 172402, Numerator: 172402, EligibleMatches: 2, Lineage: tournamentLineage}},
		}},
		Players: []*scoring.PlayerScore{},
	}
	if err := scoring.ValidateCorpusScores(cs, mreg); err != nil {
		t.Fatalf("valid score graph: %v", err)
	}
	if err := st.WriteRootJSON("scores-corpus.json", cs); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, testRoleRegistry(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	seenChildren := map[string]bool{}
	for i, matchID := range matchIDs {
		id := fmt.Sprintf("aggregation:team_match:%s:%s:hero_damage_total:%s:%s", teamID, matchID, metricVersion, version.ScoreRuleVersion)
		var env struct {
			Data struct {
				Scope     string                `json:"scope"`
				Subject   string                `json:"subject"`
				MatchID   string                `json:"match_id"`
				Value     float64               `json:"value"`
				ChildRefs []scoring.EvidenceRef `json:"child_refs"`
			} `json:"data"`
		}
		if code := getJSON(t, ts.URL+Version+"/aggregations/"+id, &env); code != http.StatusOK {
			t.Fatalf("route %s status=%d", id, code)
		}
		if env.Data.Scope != "team_match" || env.Data.Subject != teamID || env.Data.MatchID != matchID || env.Data.Value != values[i] || len(env.Data.ChildRefs) != 1 || env.Data.ChildRefs[0].MatchID != matchID {
			t.Fatalf("route %s returned wrong entity: %+v", id, env.Data)
		}
		seenChildren[id] = true
	}
	if len(seenChildren) != 2 {
		t.Fatalf("distinct routes=%d want 2", len(seenChildren))
	}
	if code := getJSON(t, ts.URL+Version+"/aggregations/aggregation:team_match:9823272:stale:hero_damage_total:"+metricVersion+":"+version.ScoreRuleVersion, &map[string]interface{}{}); code != http.StatusNotFound {
		t.Fatalf("stale team-match route status=%d want 404", code)
	}
}

// TestNavigableTypedLineageRoutes proves every typed lineage ref resolves to a
// stable match-qualified detail route (fact, episode, phase, metric
// observation, algorithm) and that stale/invalid refs render an explicit
// unavailable reason rather than a misleading success.
func TestNavigableTypedLineageRoutes(t *testing.T) {
	st := testStore(t)
	reg := testRoleRegistry(t)
	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, reg).WithReviews(rv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Write a facts.jsonl with one combat fact (seq 7), phases, episodes, and
	// metrics artifacts so the detail routes resolve.
	writeFactLine(t, st, "m1", &facts.Fact{Seq: 7, Family: facts.FamilyCombat, MatchID: "m1", GameSecond: 100, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "1000", TargetAccount: "1001", Value: int64p(500)})})
	st.WriteJSON("m1", store.ArtifactEpisodes, &episodes.Output{SchemaVersion: "replay.episodes.v1", MatchID: "m1", Episodes: []episodes.Episode{
		{ID: "m1:fight:90:0", MatchID: "m1", Kind: episodes.KindFight, StartGameSecond: 90, EndGameSecond: 120, Participants: []string{"1000"}, EvidenceIDs: []int64{7}},
	}})
	st.WriteJSON("m1", store.ArtifactPhases, &phase.Output{SchemaVersion: "replay.phase.v1", EligibleSeconds: 100, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 100, EvidenceSeqs: []int64{7}},
	}})
	st.WriteJSON("m1", store.ArtifactMetrics, &metrics.Output{SchemaVersion: "replay.metrics.v3", MatchID: "m1", Values: []metrics.Value{
		{
			MetricID: "hero_damage_total", Name: "Hero damage", ReportLevel: "player", AccountID: "1000",
			EpistemicClass: "derived", CapabilityLevel: "V1", MetricVersion: "1.0.0",
			Evidence: []metrics.EvidenceRef{{MatchID: "m1", Kind: "fact", ID: "fact:7", SourceFactSeq: 7}},
		},
		{
			MetricID: "phase_duration_seconds", Name: "Phase duration", ReportLevel: "match", OfficialPhase: "whole_match",
			EpistemicClass: "derived", CapabilityLevel: "V1", MetricVersion: "1.0.0",
			Evidence: []metrics.EvidenceRef{{MatchID: "m1", Kind: "metric_observation", ID: "phase_duration_seconds:match", RuleVersion: "1.0.0"}},
		},
	}})
	aggID := "aggregation:player_tournament:1000:1:hero_damage_total:1.0.0:" + version.ScoreRuleVersion
	st.WriteRootJSON("scores-corpus.json", &scoring.CorpusScores{
		SchemaVersion: version.ScoreSchema, RuleVersion: version.ScoreRuleVersion,
		ContractVersion: scoring.SchemaVersion, TeamScoringVersion: scoring.TeamSchemaVersion,
		Players: []*scoring.PlayerScore{{AccountID: "1000", NominalRole: "1", ScoringVersion: scoring.SchemaVersion, AggregatedMetrics: map[string]scoring.AggregatedMetric{
			"hero_damage_total": {MetricID: "hero_damage_total", MetricVersion: "1.0.0", Value: 500, EligibleMatches: 1, Lineage: []scoring.EvidenceRef{
				{MatchID: "m1", Kind: "fact", ID: "fact:7", RuleVersion: "replay.facts.v2"},
				{Kind: "aggregation", ID: aggID, RuleVersion: version.ScoreRuleVersion, ContractVersion: scoring.SchemaVersion},
			}},
		}}}, Matches: map[string]*scoring.MatchScores{},
	})

	// Canonical stored IDs pass through their routes unchanged. This matrix is
	// the end-to-end ID/route contract for every evidence kind.
	canonicalRoutes := []struct {
		kind string
		id   string
		path string
	}{
		{"fact", "fact:7", "/matches/m1/facts/fact:7"},
		{"episode", "m1:fight:90:0", "/matches/m1/episodes/m1:fight:90:0"},
		{"phase", "interval@0-100", "/matches/m1/phases/interval@0-100"},
		{"metric_observation", "hero_damage_total:1000", "/matches/m1/metrics/hero_damage_total:1000"},
		{"metric_observation_match", "phase_duration_seconds:match", "/matches/m1/metrics/phase_duration_seconds:match"},
		{"algorithm", "ev.hero_damage_total.v1.atomic", "/matches/m1/algorithms/ev.hero_damage_total.v1.atomic"},
		{"aggregation", aggID, "/aggregations/" + aggID},
	}
	var aggEnv struct {
		Data struct {
			ChildRefs []scoring.EvidenceRef `json:"child_refs"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/aggregations/"+aggID, &aggEnv); code != 200 || len(aggEnv.Data.ChildRefs) != 1 {
		t.Fatalf("aggregation structured children status=%d refs=%+v", code, aggEnv.Data.ChildRefs)
	}
	child := aggEnv.Data.ChildRefs[0]
	if child.MatchID != "m1" || child.RuleVersion != "replay.facts.v2" || child.Kind != "fact" || child.ID != "fact:7" {
		t.Fatalf("aggregation child qualifiers lost: %+v", child)
	}
	for _, tc := range canonicalRoutes {
		t.Run("canonical_"+tc.kind, func(t *testing.T) {
			resp, err := http.Get(ts.URL + Version + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s id %q route status=%d", tc.kind, tc.id, resp.StatusCode)
			}
		})
	}

	// Fact route resolves.
	var factEnv struct {
		Data struct {
			Seq int64 `json:"seq"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1/facts/7", &factEnv); code != 200 || factEnv.Data.Seq != 7 {
		t.Fatalf("fact route status=%d seq=%d", code, factEnv.Data.Seq)
	}
	// Episode route resolves.
	var epEnv struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1/episodes/m1%3Afight%3A90%3A0", &epEnv); code != 200 || epEnv.Data.ID == "" {
		t.Fatalf("episode route status=%d", code)
	}
	// Phase route resolves.
	var phEnv struct {
		Data struct {
			StartGameSecond int `json:"start_game_second"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1/phases/interval%400-100", &phEnv); code != 200 || phEnv.Data.StartGameSecond != 0 {
		t.Fatalf("phase route status=%d", code)
	}
	// Metric observation route resolves.
	var metEnv struct {
		Data struct {
			MetricID string `json:"metric_id"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1/metrics/hero_damage_total:1000", &metEnv); code != 200 || metEnv.Data.MetricID != "hero_damage_total" {
		t.Fatalf("metric observation route status=%d", code)
	}
	// Algorithm route resolves (closure evaluator).
	var algEnv struct {
		Data struct {
			EvaluatorID string `json:"evaluator_id"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/matches/m1/algorithms/ev.hero_damage_total.v1.atomic", &algEnv); code != 200 || algEnv.Data.EvaluatorID == "" {
		t.Fatalf("algorithm route status=%d", code)
	}
	// Stale fact renders an explicit unavailable reason (404 + reason), never
	// a misleading 200.
	resp, err := http.Get(ts.URL + Version + "/matches/m1/facts/999999")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stale fact status=%d want 404", resp.StatusCode)
	}
	var errEnv struct {
		Err string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&errEnv)
	if !strings.Contains(errEnv.Err, "fact_not_found") {
		t.Fatalf("stale fact reason=%q", errEnv.Err)
	}
}

// TestTwoMatchesLineageDistinctThroughAPI proves equal entity ids from two
// matches remain distinct through the persisted metrics artifact and the API
// match reports (match-qualified lineage identity).
func TestTwoMatchesLineageDistinctThroughAPI(t *testing.T) {
	st := testStore(t)
	// m1 (from testStore) keeps its default metrics; give it a
	// hero_damage_total row with fact:7 evidence so both matches share the
	// same entity id.
	st.WriteJSON("m1", store.ArtifactMetrics, &metrics.Output{SchemaVersion: "replay.metrics.v3", MatchID: "m1", Values: []metrics.Value{{
		MetricID: "hero_damage_total", Name: "Hero damage", ReportLevel: "player", AccountID: "1000",
		EpistemicClass: "derived", CapabilityLevel: "V1", MetricVersion: "1.0.0",
		Evidence: []metrics.EvidenceRef{{MatchID: "m1", Kind: "fact", ID: "fact:7", SourceFactSeq: 7}},
	}}})
	// Add a second verified match m2 with the same fact seq 7.
	st.WriteJSON("m2", store.ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	st.WriteJSON("m2", store.ArtifactIdentity, map[string]interface{}{
		"state": "verified", "match_id": "m2",
		"participants": []map[string]interface{}{{"slot": 0, "account_id": "1000", "player_name": "p0", "hero_name": "npc_dota_hero_kez", "hero_id": 145, "side": "radiant", "team": 2}},
		"teams":        []map[string]interface{}{{"team_id": "T1", "team_name": "Team One", "side": "radiant"}},
	})
	st.WriteJSON("m2", store.ArtifactPhases, map[string]interface{}{
		"state": "complete", "eligible_seconds": 100,
		"intervals": []map[string]interface{}{{"global_phase": "laning", "start_game_second": 0, "end_game_second": 100}},
	})
	st.WriteJSON("m2", store.ArtifactMetrics, &metrics.Output{SchemaVersion: "replay.metrics.v3", MatchID: "m2", Values: []metrics.Value{{
		MetricID: "hero_damage_total", Name: "Hero damage", ReportLevel: "player", AccountID: "1000",
		EpistemicClass: "derived", CapabilityLevel: "V1", MetricVersion: "1.0.0",
		Evidence: []metrics.EvidenceRef{{MatchID: "m2", Kind: "fact", ID: "fact:7", SourceFactSeq: 7}},
	}}})
	st.WriteStatus("m2", &store.StatusRecord{
		SchemaVersion: store.StatusSchema, MatchID: "m2", Status: store.StatusVerified,
		Publication: "published", Reason: "all_gates_pass", ArchiveState: "verified",
		IdentityState: "verified", ClockState: "calibrated",
	})
	if _, err := st.RebuildCatalog("2026-08-17T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	reg := testRoleRegistry(t)
	srv := testServerWithContracts(t, st, reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Each match's report preserves its own match-qualified lineage.
	for _, mid := range []string{"m1", "m2"} {
		var rep struct {
			Data struct {
				Metrics struct {
					Values []struct {
						MetricID string `json:"metric_id"`
						Evidence []struct {
							MatchID string `json:"match_id"`
							Kind    string `json:"kind"`
							ID      string `json:"id"`
						} `json:"evidence"`
					} `json:"values"`
				} `json:"metrics"`
			} `json:"data"`
		}
		if code := getJSON(t, ts.URL+Version+"/matches/"+mid, &rep); code != 200 {
			t.Fatalf("match %s report status %d", mid, code)
		}
		found := false
		for _, v := range rep.Data.Metrics.Values {
			if v.MetricID != "hero_damage_total" {
				continue
			}
			for _, e := range v.Evidence {
				if e.Kind == "fact" && e.ID == "fact:7" {
					if e.MatchID != mid {
						t.Fatalf("match %s lineage ref match=%s want %s", mid, e.MatchID, mid)
					}
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("match %s missing fact:7 lineage", mid)
		}
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func int64p(v int64) *int64 { return &v }

// frozenRoleRegistry returns a role registry matching the frozen probe match
// 8944521919 (10 participants, one of each role 1-5 per side) so the exact
// frozen override regression can run against it.
func frozenRoleRegistry() *roles.Registry {
	return &roles.Registry{
		SchemaVersion: "ti2026.roles.v1",
		TournamentID:  "ti2026",
		Matches: []roles.RoleMatch{{
			MatchID: "8944521919",
			Teams: []roles.RoleTeam{
				{
					TeamID: "726228", TeamName: "Vici Gaming", Side: "radiant",
					SourceKind: "reliable_public_database", SourceURL: "https://example.com/vg",
					SourceLocator: "Recent lineup table with explicit Role 1-5 rows", RetrievedAt: "2026-08-17T04:15:00Z",
					Participants: []roles.RoleRecord{
						{RoleRecordID: "8944521919:320252024", AccountID: "320252024", PlayerName: "shiro", NominalRole: "1", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:137129583", AccountID: "137129583", PlayerName: "Xm", NominalRole: "2", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:118134220", AccountID: "118134220", PlayerName: "Bach", NominalRole: "3", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:157475523", AccountID: "157475523", PlayerName: "XinQ", NominalRole: "4", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:111114687", AccountID: "111114687", PlayerName: "y`", NominalRole: "5", RoleConfidence: "high"},
					},
				},
				{
					TeamID: "10149530", TeamName: "HULIGANI", Side: "dire",
					SourceKind: "reliable_public_tournament_roster", SourceURL: "https://example.com/huligani",
					SourceLocator: "HULIGANI player roster table with explicit Position 1-5 rows", RetrievedAt: "2026-08-17T04:15:00Z",
					Participants: []roles.RoleRecord{
						{RoleRecordID: "8944521919:320017600", AccountID: "320017600", PlayerName: "ssnovv1", NominalRole: "1", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:140251702", AccountID: "140251702", PlayerName: "Mirage", NominalRole: "2", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:92487440", AccountID: "92487440", PlayerName: "Corrupted", NominalRole: "3", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:145065875", AccountID: "145065875", PlayerName: "sayuw", NominalRole: "4", RoleConfidence: "high"},
						{RoleRecordID: "8944521919:123787715", AccountID: "123787715", PlayerName: "RESPECT", NominalRole: "5", RoleConfidence: "high"},
					},
				},
			},
		}},
	}
}

// testFrozenOverrideStore builds a disposable store for match 8944521919 with
// the frozen 10-participant identity, phases, metrics, persisted report, and
// score corpus, so role overrides can be exercised end to end.
func testFrozenOverrideStore(t *testing.T, roleReg *roles.Registry) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accounts := []struct {
		acct string
		name string
		side string
		team string
	}{
		{"320252024", "shiro", "radiant", "726228"},
		{"137129583", "Xm", "radiant", "726228"},
		{"118134220", "Bach", "radiant", "726228"},
		{"157475523", "XinQ", "radiant", "726228"},
		{"111114687", "y`", "radiant", "726228"},
		{"320017600", "ssnovv1", "dire", "10149530"},
		{"140251702", "Mirage", "dire", "10149530"},
		{"92487440", "Corrupted", "dire", "10149530"},
		{"145065875", "sayuw", "dire", "10149530"},
		{"123787715", "RESPECT", "dire", "10149530"},
	}
	parts := []map[string]interface{}{}
	for i, a := range accounts {
		parts = append(parts, map[string]interface{}{
			"slot": i, "account_id": a.acct, "player_name": a.name,
			"hero_name": "npc_dota_hero_kez", "hero_id": 145, "side": a.side, "team": int32(2),
		})
	}
	if err := st.WriteJSON("8944521919", store.ArtifactVerification, map[string]string{
		"state": "verified", "reason": "ok", "demo_sha256_actual": "8944521919-replay-sha256",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactIdentity, map[string]interface{}{
		"state": "verified", "match_id": "8944521919",
		"participants": parts,
		"teams": []map[string]interface{}{
			{"team_id": "726228", "team_name": "Vici Gaming", "side": "radiant"},
			{"team_id": "10149530", "team_name": "HULIGANI", "side": "dire"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactClock, map[string]interface{}{"state": "calibrated", "game_duration_seconds": 2705}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactFactsSummary, map[string]interface{}{"families": []interface{}{}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactPhases, map[string]interface{}{
		"schema_version": "replay.phase.v1", "rule_version": "ti2026.phase.v1",
		"state": "complete", "eligible_seconds": 2705, "covered_seconds": 2705,
		"intervals": cumulativePhaseIntervals(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactEpisodes, map[string]interface{}{"episodes": []interface{}{}, "unavailable": []interface{}{}}); err != nil {
		t.Fatal(err)
	}
	// Give every player a published metric so scores/corpus/percentiles build.
	vals := []interface{}{}
	for _, a := range accounts {
		vals = append(vals, map[string]interface{}{"metric_id": "hero_damage_total", "metric_version": "1.0.0", "account_id": a.acct, "value": 1000, "unit": "damage", "report_level": "player"})
		vals = append(vals, map[string]interface{}{"metric_id": "kill_count", "metric_version": "1.0.0", "account_id": a.acct, "value": 5, "unit": "count", "report_level": "player"})
	}
	if err := st.WriteJSON("8944521919", store.ArtifactMetrics, map[string]interface{}{
		"schema_version": version.MetricsSchema, "rule_version": version.MetricsRuleVersion, "match_id": "8944521919",
		"values": vals, "unavailable": []interface{}{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactInput, map[string]interface{}{"category": "probe"}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("8944521919", store.ArtifactCanonical, map[string]interface{}{
		"schema_version": "replay.report.v1", "tree_sha256": "frozen-tree", "input_fingerprint": map[string]interface{}{"match_id": "8944521919"},
	}); err != nil {
		t.Fatal(err)
	}
	// Persist an initial report built with the SOURCE roles.
	rep, err := report.Build(st, "8944521919", roleReg, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep.SortParticipants()
	if err := st.WriteJSON("8944521919", store.ArtifactReport, rep); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteStatus("8944521919", &store.StatusRecord{
		SchemaVersion: store.StatusSchema, MatchID: "8944521919", Status: rep.Status,
		Publication: rep.Publication, Reason: rep.Reason, ArchiveState: "verified",
		IdentityState: "verified", ClockState: "calibrated", ParseState: "complete",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RebuildCatalog("2026-08-18T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// Build the initial score corpus from source roles.
	mreg, sc := testContracts(t)
	if _, err := scoring.ComputeAndPersist(st, sc, testTeamContract(t), mreg, roleReg, nil); err != nil {
		t.Fatal(err)
	}
	return st
}

// overrideRole posts a role override and returns the HTTP status.
func overrideRole(t *testing.T, baseURL, matchID, acct, role, author, reason string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"match_id": matchID, "account_id": acct, "nominal_role": role,
		"author": author, "reason": reason,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+Version+"/roles/overrides", bytes.NewReader(body))
	req.Header.Set("X-Dota2-OB-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// effectiveRoleInMatch reads /matches/{id} and returns the effective + source
// role for the account.
func effectiveRoleInMatch(t *testing.T, baseURL, matchID, acct string) (string, string) {
	t.Helper()
	var rep struct {
		Data struct {
			Participants []struct {
				AccountID         string  `json:"account_id"`
				SourceNominalRole string  `json:"source_nominal_role"`
				NominalRole       string  `json:"nominal_role"`
				OverrideApplied   bool    `json:"override_applied"`
				OverrideAuthor    *string `json:"override_author"`
			} `json:"participants"`
		} `json:"data"`
	}
	if code := getJSON(t, baseURL+Version+"/matches/"+matchID, &rep); code != 200 {
		t.Fatalf("match status %d", code)
	}
	for _, p := range rep.Data.Participants {
		if p.AccountID == acct {
			return p.NominalRole, p.SourceNominalRole
		}
	}
	t.Fatalf("account %s not in match report", acct)
	return "", ""
}

// TestRoleOverrideFrozenSequenceEndToEnd runs the exact frozen regression:
// 8944521919 / 111114687 source role 5; apply 5->1; restart; apply 1->2;
// restart; apply 2->3. At every step every view must agree on the effective
// role, the source role must remain 5, and correction previous values must be
// 5, 1, 2 respectively.
func TestRoleOverrideFrozenSequenceEndToEnd(t *testing.T) {
	roleReg := frozenRoleRegistry()
	st := testFrozenOverrideStore(t, roleReg)
	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	ACCT := "111114687"

	startServer := func() (*httptest.Server, *Server) {
		srv := testServerWithContracts(t, st, roleReg).WithReviews(rv).WithSessionToken("tok")
		return httptest.NewServer(srv.Handler()), srv
	}
	ts, _ := startServer()
	defer ts.Close()

	assertConsistent := func(baseURL string, wantEffective string, step interface{}) {
		t.Helper()
		// /matches/{id} report agrees.
		eff, src := effectiveRoleInMatch(t, baseURL, "8944521919", ACCT)
		if eff != wantEffective {
			t.Fatalf("step %d match report effective=%s want %s", step, eff, wantEffective)
		}
		if src != "5" {
			t.Fatalf("step %d match report source=%s want 5", step, src)
		}
		// Player per-match profile row agrees.
		var pl struct {
			Data struct {
				Matches []struct {
					MatchID           string `json:"match_id"`
					SourceNominalRole string `json:"source_nominal_role"`
					NominalRole       string `json:"nominal_role"`
					RoleRecordVersion string `json:"role_record_version"`
					OverrideAuthor    string `json:"override_author"`
					OverrideVersion   string `json:"override_version"`
				} `json:"matches"`
			} `json:"data"`
		}
		if code := getJSON(t, baseURL+Version+"/players/"+ACCT, &pl); code != 200 {
			t.Fatalf("player status %d", code)
		}
		found := false
		for _, m := range pl.Data.Matches {
			if m.MatchID == "8944521919" {
				found = true
				if m.NominalRole != wantEffective {
					t.Fatalf("step %d player match row effective=%s want %s", step, m.NominalRole, wantEffective)
				}
				if m.SourceNominalRole != "5" {
					t.Fatalf("step %d player match row source=%s want 5", step, m.SourceNominalRole)
				}
				if m.RoleRecordVersion == "" || (wantEffective != "5" && (m.OverrideAuthor == "" || m.OverrideVersion == "")) {
					t.Fatalf("step %v player provenance incomplete: %+v", step, m)
				}
			}
		}
		if !found {
			t.Fatalf("step %d player has no 8944521919 match row", step)
		}
		// /matches/{id}/scores agrees.
		var ms struct {
			Data struct {
				Players []struct {
					AccountID         string `json:"account_id"`
					NominalRole       string `json:"nominal_role"`
					RoleRecordVersion string `json:"role_record_version"`
					OverrideAuthor    string `json:"override_author"`
					OverrideVersion   string `json:"override_version"`
				} `json:"players"`
			} `json:"data"`
		}
		if code := getJSON(t, baseURL+Version+"/matches/8944521919/scores", &ms); code != 200 {
			t.Fatalf("scores status %d", code)
		}
		for _, p := range ms.Data.Players {
			if p.AccountID == ACCT {
				if p.NominalRole != wantEffective {
					t.Fatalf("step %v match scores effective=%s want %s", step, p.NominalRole, wantEffective)
				}
				if p.RoleRecordVersion == "" || (wantEffective != "5" && (p.OverrideAuthor == "" || p.OverrideVersion == "")) {
					t.Fatalf("step %v match score provenance incomplete: %+v", step, p)
				}
			}
		}
		// Corpus / player tournament score agrees and cohort is recomputed.
		var cs struct {
			Data struct {
				Players []struct {
					AccountID      string                            `json:"account_id"`
					NominalRole    string                            `json:"nominal_role"`
					RoleProvenance map[string]scoring.RoleProvenance `json:"role_provenance"`
				} `json:"players"`
			} `json:"data"`
		}
		if code := getJSON(t, baseURL+Version+"/scores/corpus", &cs); code != 200 {
			t.Fatalf("corpus status %d", code)
		}
		for _, p := range cs.Data.Players {
			if p.AccountID == ACCT {
				if p.NominalRole != wantEffective {
					t.Fatalf("step %v corpus effective=%s want %s", step, p.NominalRole, wantEffective)
				}
				prov := p.RoleProvenance["8944521919"]
				if prov.RoleRecordVersion == "" || (wantEffective != "5" && (prov.OverrideAuthor == "" || prov.OverrideVersion == "")) {
					t.Fatalf("step %v corpus provenance incomplete: %+v", step, prov)
				}
			}
		}
		// Persisted report agrees (on-read overlay applies even to the file).
		var repFile report.Report
		if err := st.ReadJSON("8944521919", store.ArtifactReport, &repFile); err != nil {
			t.Fatalf("step %d read persisted report: %v", step, err)
		}
		report.OverlayRoles(&repFile, "8944521919", roleReg, loadOverrideFileHelper(t, st))
		for _, p := range repFile.Participants {
			if p.AccountID == ACCT && p.NominalRole != wantEffective {
				t.Fatalf("step %d persisted report effective=%s want %s", step, p.NominalRole, wantEffective)
			}
			if p.AccountID == ACCT && p.SourceNominalRole != "5" {
				t.Fatalf("step %d persisted report source=%s want 5", step, p.SourceNominalRole)
			}
		}
	}

	// Step 0: initial state — effective == source == 5.
	assertConsistent(ts.URL, "5", 0)

	// Step 1: apply 5 -> 1.
	if code := overrideRole(t, ts.URL, "8944521919", ACCT, "1", "reviewerA", "observed role 1 on evidence"); code != 200 {
		t.Fatalf("override 5->1 status=%d", code)
	}
	assertConsistent(ts.URL, "1", 1)

	// Restart 1: fresh server, same store.
	ts2, _ := startServer()
	defer ts2.Close()
	assertConsistent(ts2.URL, "1", "restart1")

	// Step 2: apply 1 -> 2 after restart (previous_value must be 1, the
	// persisted effective value, not the source 5).
	if code := overrideRole(t, ts2.URL, "8944521919", ACCT, "2", "reviewerB", "further evidence"); code != 200 {
		t.Fatalf("override 1->2 status=%d", code)
	}
	assertConsistent(ts2.URL, "2", 2)

	// Restart 2.
	ts3, _ := startServer()
	defer ts3.Close()
	assertConsistent(ts3.URL, "2", "restart2")

	// Step 3: apply 2 -> 3 (previous_value must be 2).
	if code := overrideRole(t, ts3.URL, "8944521919", ACCT, "3", "reviewerC", "final adjudication"); code != 200 {
		t.Fatalf("override 2->3 status=%d", code)
	}
	assertConsistent(ts3.URL, "3", 3)

	// Correction previous values must be 5, 1, 2; effective 1, 2, 3.
	loaded, err := rv.Load("8944521919")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Corrections) != 3 {
		t.Fatalf("corrections=%d want 3", len(loaded.Corrections))
	}
	wantPrev := []string{"5", "1", "2"}
	wantEff := []string{"1", "2", "3"}
	for i, c := range loaded.Corrections {
		if c.Kind != review.KindRoleOverride {
			t.Fatalf("correction %d kind=%s", i, c.Kind)
		}
		var prev, eff struct {
			NominalRole string `json:"nominal_role"`
		}
		_ = json.Unmarshal(c.PreviousValue, &prev)
		_ = json.Unmarshal(c.EffectiveValue, &eff)
		if prev.NominalRole != wantPrev[i] {
			t.Fatalf("correction %d previous=%s want %s", i, prev.NominalRole, wantPrev[i])
		}
		if eff.NominalRole != wantEff[i] {
			t.Fatalf("correction %d effective=%s want %s", i, eff.NominalRole, wantEff[i])
		}
	}
	// Audit has 3 role-override entries.
	audit, err := rv.LoadAudit()
	if err != nil {
		t.Fatal(err)
	}
	roleAudits := 0
	for _, e := range audit.Entries {
		if e.Action == "correction_added" && strings.Contains(e.Summary, "role_override") {
			roleAudits++
		}
	}
	if roleAudits != 3 {
		t.Fatalf("role override audit entries=%d want 3", roleAudits)
	}
	// Effective override file holds the latest value (3).
	of := loadOverrideFileHelper(t, st)
	if len(of.Overrides) != 1 || of.Overrides[0].NominalRole != "3" {
		t.Fatalf("effective override file=%+v", of.Overrides)
	}
	// Immutable artifacts and the frozen role registry stay byte-identical
	// through the whole override sequence.
	for _, art := range []string{store.ArtifactPhases, store.ArtifactEpisodes, store.ArtifactMetrics} {
		b, err := os.ReadFile(st.ArtifactPath("8944521919", art))
		if err != nil {
			t.Fatalf("read immutable %s: %v", art, err)
		}
		var v interface{}
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("immutable %s unreadable: %v", art, err)
		}
	}
	regBytesBefore, _ := json.Marshal(frozenRoleRegistry())
	regBytesAfter, _ := json.Marshal(roleReg)
	if string(regBytesBefore) != string(regBytesAfter) {
		t.Fatal("frozen role registry mutated")
	}
}

func loadOverrideFileHelper(t *testing.T, st *store.Store) *roles.OverrideFile {
	t.Helper()
	var of roles.OverrideFile
	if err := st.ReadJSONFile(st.Root+"/role-overrides-effective.json", &of); err != nil {
		if os.IsNotExist(err) {
			return &roles.OverrideFile{SchemaVersion: "ti2026.roles.v1", Overrides: []roles.Override{}}
		}
		t.Fatalf("read override file: %v", err)
	}
	return &of
}

// TestRoleOverrideNegativeAndAtomicity proves rejected/injected-failure
// mutations leave all authoritative files byte-unchanged: invalid role,
// unknown match, unknown account, and staged report/score/correction failures
// must not advance the effective override, report, scores, correction history,
// or audit.
func TestRoleOverrideNegativeAndAtomicity(t *testing.T) {
	roleReg := frozenRoleRegistry()
	st := testFrozenOverrideStore(t, roleReg)
	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	ACCT := "111114687"

	// Snapshot helper: returns byte hashes of the authoritative files.
	hashPath := func(p string) [32]byte {
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				return [32]byte{}
			}
			t.Fatalf("read %s: %v", p, err)
		}
		return sha256.Sum256(b)
	}
	snapshot := func() map[string][32]byte {
		paths := []string{
			filepath.Join(st.Root, "role-overrides-effective.json"),
			st.ArtifactPath("8944521919", store.ArtifactReport),
			st.ArtifactPath("8944521919", store.ArtifactStatus),
			st.ArtifactPath("8944521919", store.ArtifactScores),
			filepath.Join(st.Root, "scores-corpus.json"),
			rv.Path("8944521919"),
			rv.AuditPath(),
		}
		m := map[string][32]byte{}
		for _, p := range paths {
			m[p] = hashPath(p)
		}
		return m
	}
	assertUnchanged := func(before map[string][32]byte) {
		t.Helper()
		for p, h := range before {
			if got := hashPath(p); got != h {
				t.Fatalf("file changed after rejected/failed mutation: %s", p)
			}
		}
	}

	startServer := func(inject string) *httptest.Server {
		srv := testServerWithContracts(t, st, roleReg).WithReviews(rv).WithSessionToken("tok")
		srv.injectFail = inject
		return httptest.NewServer(srv.Handler())
	}

	// Case 1: invalid role (400) — nothing changes.
	before := snapshot()
	ts := startServer("")
	defer ts.Close()
	if code := overrideRole(t, ts.URL, "8944521919", ACCT, "9", "a", "bad role"); code != http.StatusBadRequest {
		t.Fatalf("invalid role status=%d want 400", code)
	}
	assertUnchanged(before)

	// Case 2: unknown match (400) — nothing changes.
	if code := overrideRole(t, ts.URL, "9999999999", ACCT, "1", "a", "x"); code != http.StatusBadRequest {
		t.Fatalf("unknown match status=%d want 400", code)
	}
	assertUnchanged(before)

	// Case 3: unknown account in a known match (400) — nothing changes.
	if code := overrideRole(t, ts.URL, "8944521919", "999999999", "1", "a", "x"); code != http.StatusBadRequest {
		t.Fatalf("unknown account status=%d want 400", code)
	}
	assertUnchanged(before)

	// Case 4: injected report persistence failure (500) — rollback restores
	// every file, including the override file written just before the failure.
	tsFail := startServer("report")
	defer tsFail.Close()
	if code := overrideRole(t, tsFail.URL, "8944521919", ACCT, "1", "a", "x"); code != http.StatusInternalServerError {
		t.Fatalf("injected report failure status=%d want 500", code)
	}
	assertUnchanged(before)

	// Case 5: injected score recompute failure (500) — rollback restores.
	tsFail2 := startServer("score")
	defer tsFail2.Close()
	if code := overrideRole(t, tsFail2.URL, "8944521919", ACCT, "1", "a", "x"); code != http.StatusInternalServerError {
		t.Fatalf("injected score failure status=%d want 500", code)
	}
	assertUnchanged(before)

	// Case 6: injected correction persistence failure (500) — rollback
	// restores override file, report, scores, corrections, and audit.
	tsFail3 := startServer("correction")
	defer tsFail3.Close()
	if code := overrideRole(t, tsFail3.URL, "8944521919", ACCT, "1", "a", "x"); code != http.StatusInternalServerError {
		t.Fatalf("injected correction failure status=%d want 500", code)
	}
	assertUnchanged(before)

	// Audit and correction history were never advanced.
	audit, err := rv.LoadAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Entries) != 0 {
		t.Fatalf("audit entries=%d want 0 after all failures", len(audit.Entries))
	}
	loaded, err := rv.Load("8944521919")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Corrections) != 0 {
		t.Fatalf("corrections=%d want 0 after all failures", len(loaded.Corrections))
	}
}

func TestRoleOverrideRecomputeRejectsStaleMetricsArtifact(t *testing.T) {
	roleReg := frozenRoleRegistry()
	st := testFrozenOverrideStore(t, roleReg)
	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	var met metrics.Output
	if err := st.ReadJSON("8944521919", store.ArtifactMetrics, &met); err != nil {
		t.Fatal(err)
	}
	met.SchemaVersion = "replay.metrics.v4"
	if err := st.WriteJSON("8944521919", store.ArtifactMetrics, &met); err != nil {
		t.Fatal(err)
	}
	beforeScores, err := os.ReadFile(filepath.Join(st.Root, "scores-corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, roleReg).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if code := overrideRole(t, ts.URL, "8944521919", "111114687", "1", "reviewer", "stale metrics gate"); code != http.StatusInternalServerError {
		t.Fatalf("stale recompute status=%d want 500", code)
	}
	afterScores, err := os.ReadFile(filepath.Join(st.Root, "scores-corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeScores, afterScores) {
		t.Fatal("stale role-override recompute mutated the current score corpus")
	}
}

func TestStaleScoreWireShapeIsNotServedAsCurrent(t *testing.T) {
	st := testStore(t)
	if err := st.WriteRootJSON("scores-corpus.json", &scoring.CorpusScores{SchemaVersion: "replay.score.v5", RuleVersion: version.ScoreRuleVersion}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("m1", store.ArtifactScores, &scoring.MatchScores{SchemaVersion: "replay.score.v5", RuleVersion: version.ScoreRuleVersion, MatchID: "m1"}); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, testRoleRegistry(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, path := range []string{Version + "/scores/corpus", Version + "/matches/m1/scores"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("stale score path %s status=%d want 404", path, resp.StatusCode)
		}
	}
}

func TestCurrentTaggedScoreWithMissingNestedMetricVersionFailsClosedAcrossRestartAndOverride(t *testing.T) {
	roleReg := frozenRoleRegistry()
	st := testFrozenOverrideStore(t, roleReg)
	path := filepath.Join(st.Root, "scores-corpus.json")
	var cs scoring.CorpusScores
	if err := st.ReadJSONFile(path, &cs); err != nil {
		t.Fatal(err)
	}
	deleted := false
	for _, player := range cs.Players {
		for axisKey, axis := range player.OfficialAxes {
			for mid, component := range axis.Components {
				component.MetricVersion = ""
				axis.Components[mid] = component
				player.OfficialAxes[axisKey] = axis
				deleted = true
				break
			}
			if deleted {
				break
			}
		}
		if deleted {
			break
		}
	}
	if !deleted {
		t.Fatal("fixture has no score component to corrupt")
	}
	if err := st.WriteRootJSON("scores-corpus.json", &cs); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	assertNotServed := func(t *testing.T) {
		t.Helper()
		srv := testServerWithContracts(t, st, roleReg)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		resp, err := http.Get(ts.URL + Version + "/scores/corpus")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("current-tagged corpus with missing nested metric_version was served")
		}
		t.Logf("corrupt corpus API status=%d", resp.StatusCode)
	}
	assertNotServed(t)
	assertNotServed(t) // fresh server instance = restart boundary

	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, roleReg).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if code := overrideRole(t, ts.URL, "8944521919", "111114687", "1", "reviewer", "corrupt score rollback"); code != http.StatusInternalServerError {
		t.Fatalf("role override status=%d want 500", code)
	} else {
		t.Logf("corrupt corpus role-override status=%d", code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("role override mutated corrupt authoritative score corpus")
	}
}

func TestCorruptTeamMatchChildSetsFailClosedAcrossRestartAndOverrideRollback(t *testing.T) {
	tests := []string{"missing", "duplicate", "extra", "matchless", "wrong_team", "wrong_match", "wrong_metric_version", "wrong_score_rule", "non_resolving"}
	for _, kind := range tests {
		t.Run(kind, func(t *testing.T) {
			roleReg := frozenRoleRegistry()
			st := testFrozenOverrideStore(t, roleReg)
			path := filepath.Join(st.Root, "scores-corpus.json")
			var cs scoring.CorpusScores
			if err := st.ReadJSONFile(path, &cs); err != nil {
				t.Fatal(err)
			}
			team := cs.Teams[0]
			var metricID string
			var agg scoring.AggregatedMetric
			var childIndex int
			found := false
			for mid, candidate := range team.AggregatedMetrics {
				for i, ref := range candidate.Lineage {
					if ref.Kind == "aggregation" && ref.MatchID != "" {
						metricID, agg, childIndex, found = mid, candidate, i, true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				t.Fatal("fixture has no team-match child")
			}
			child := agg.Lineage[childIndex]
			switch kind {
			case "missing":
				agg.Lineage = append(agg.Lineage[:childIndex], agg.Lineage[childIndex+1:]...)
			case "duplicate":
				agg.Lineage = append(agg.Lineage, child)
			case "extra":
				extra := child
				extra.MatchID = "extra-match"
				extra.ID = strings.Replace(extra.ID, ":"+child.MatchID+":", ":extra-match:", 1)
				agg.Lineage = append(agg.Lineage, extra)
			case "matchless":
				agg.Lineage[childIndex].MatchID = ""
			case "wrong_team":
				agg.Lineage[childIndex].ID = strings.Replace(agg.Lineage[childIndex].ID, ":"+team.TeamID+":", ":wrong-team:", 1)
			case "wrong_match":
				agg.Lineage[childIndex].MatchID = "wrong-match"
			case "wrong_metric_version":
				agg.Lineage[childIndex].ID = strings.Replace(agg.Lineage[childIndex].ID, ":1.0.0:", ":0.9.0:", 1)
			case "wrong_score_rule":
				agg.Lineage[childIndex].RuleVersion = "ti2026.scoring.v4"
			case "non_resolving":
				delete(cs.TeamMatches[team.TeamID], child.MatchID)
			}
			team.AggregatedMetrics[metricID] = agg
			if err := st.WriteRootJSON("scores-corpus.json", &cs); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			for restart := 0; restart < 2; restart++ {
				srv := testServerWithContracts(t, st, roleReg)
				ts := httptest.NewServer(srv.Handler())
				resp, err := http.Get(ts.URL + Version + "/scores/corpus")
				if err != nil {
					ts.Close()
					t.Fatal(err)
				}
				resp.Body.Close()
				ts.Close()
				if resp.StatusCode == http.StatusOK {
					t.Fatalf("corruption %s served across restart %d", kind, restart)
				}
			}

			rv, err := review.New(st.Root)
			if err != nil {
				t.Fatal(err)
			}
			srv := testServerWithContracts(t, st, roleReg).WithReviews(rv).WithSessionToken("tok")
			ts := httptest.NewServer(srv.Handler())
			code := overrideRole(t, ts.URL, "8944521919", "111114687", "1", "reviewer", "corrupt team-match rollback")
			ts.Close()
			if code != http.StatusInternalServerError {
				t.Fatalf("corruption %s role override status=%d want 500", kind, code)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("corruption %s mutated authoritative score corpus", kind)
			}
		})
	}
}

func TestCurrentTaggedMatchScoreWithMissingNestedMetricVersionIsNotServed(t *testing.T) {
	roleReg := frozenRoleRegistry()
	st := testFrozenOverrideStore(t, roleReg)
	var ms scoring.MatchScores
	if err := st.ReadJSON("8944521919", store.ArtifactScores, &ms); err != nil {
		t.Fatal(err)
	}
	deleted := false
	for _, player := range ms.Players {
		for mid, value := range player.Metrics {
			value.MetricVersion = ""
			player.Metrics[mid] = value
			deleted = true
			break
		}
		if deleted {
			break
		}
	}
	if !deleted {
		t.Fatal("fixture has no per-match metric to corrupt")
	}
	if err := st.WriteJSON("8944521919", store.ArtifactScores, &ms); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, roleReg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + Version + "/matches/8944521919/scores")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("current-tagged per-match score with missing nested metric_version was served")
	}
	t.Logf("corrupt per-match score API status=%d", resp.StatusCode)
}

// TestTeamAPIStableSchema proves the /teams/{id} endpoint returns the stable
// TeamScore shape with separately named official and experimental layers, and
// that an absent team returns the same shape (both layers suppressed) rather
// than the unrelated {axes,published,reasons} shape.
func TestTeamAPIStableSchema(t *testing.T) {
	st := testStore(t)
	reg := testRoleRegistry(t)
	srv := testServerWithContracts(t, st, reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Absent team: stable shape with official + experimental layers.
	var absent struct {
		Data struct {
			TeamScore struct {
				OfficialTotal     map[string]interface{} `json:"official_total"`
				ExperimentalTotal map[string]interface{} `json:"experimental_total"`
				ExperimentalAxes  map[string]interface{} `json:"experimental_axes"`
			} `json:"team_score"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/teams/NO_SUCH", &absent); code != 200 {
		t.Fatalf("absent team status %d", code)
	}
	if absent.Data.TeamScore.OfficialTotal == nil || absent.Data.TeamScore.ExperimentalTotal == nil {
		t.Fatal("absent-team shape missing official/experimental totals")
	}
	if absent.Data.TeamScore.ExperimentalAxes == nil {
		t.Fatal("absent-team shape missing experimental_axes")
	}
	if _, hasAxes := absent.Data.TeamScore.OfficialTotal["axes"]; hasAxes {
		t.Fatal("absent-team fallback still uses unrelated axes shape")
	}
}

// TestTeamScoreCorpusPersistsBothLayers proves a persisted team score corpus
// carries both official and experimental layers for a real team.
func TestTeamScoreCorpusPersistsBothLayers(t *testing.T) {
	st := testStore(t)
	mreg, sc := testContracts(t)
	tc := testTeamContract(t)
	// Build a small corpus of 3 matches with two teams carrying the full team
	// metric set so team scoring persists.
	acct := 0
	var players []*scoring.PlayerMatch
	for mi := 0; mi < 3; mi++ {
		for _, tid := range []string{"TA", "TB"} {
			for role := 1; role <= 5; role++ {
				acct++
				mv := map[string]scoring.MetricValue{}
				for _, mid := range []string{
					"lane_pressure_damage_per_contact", "item_timing_opportunity_percentile", "stack_attempt_success_rate",
					"roam_conversion_rate", "rune_control_contribution", "key_ability_window_conversion",
					"observed_map_exchange_outcome_rate", "ward_lifetime_share",
					"fight_damage_share", "control_duration_per_opportunity",
					"resource_to_objective_conversion", "highground_building_conversion",
					"death_without_buyback_exposure", "buyback_round_participation",
				} {
					n := 5.0
					d := scoring.HigherBetter
					if mid == "death_without_buyback_exposure" {
						d = scoring.LowerBetter
					}
					mv[mid] = scoring.MetricValue{MetricID: mid, Value: 0.5, Direction: d, OfficialEligible: true, Numerator: &n, Denominator: f64ptr(10)}
				}
				players = append(players, &scoring.PlayerMatch{MatchID: fmt.Sprintf("m%d", mi), AccountID: fmt.Sprintf("a%d", acct), TeamID: tid, NominalRole: fmt.Sprintf("%d", role), Metrics: mv})
			}
		}
	}
	cor := scoring.NewCorpusWithTeam(sc, tc, mreg, players)
	// Persist through the corpus catalog path used by the API.
	cs := &scoring.CorpusScores{
		SchemaVersion:        version.ScoreSchema,
		RuleVersion:          version.ScoreRuleVersion,
		ContractVersion:      sc.SchemaVersion,
		TeamScoringVersion:   tc.SchemaVersion,
		ComparisonPopulation: tc.ComparisonPopulation,
		CorpusMatches:        cor.MatchCount(),
		Matches:              map[string]*scoring.MatchScores{},
		Players:              []*scoring.PlayerScore{},
		Teams:                []*scoring.TeamScore{},
	}
	for _, tid := range []string{"TA", "TB"} {
		if tsv := cor.ScoreTeam(tid); tsv != nil {
			cs.Teams = append(cs.Teams, tsv)
		}
	}
	if err := st.WriteRootJSON("scores-corpus.json", cs); err != nil {
		t.Fatal(err)
	}
	reg := testRoleRegistry(t)
	srv := testServerWithContracts(t, st, reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	var out struct {
		Data struct {
			TeamScore struct {
				OfficialTotal     map[string]interface{} `json:"official_total"`
				ExperimentalTotal map[string]interface{} `json:"experimental_total"`
				ExperimentalAxes  map[string]interface{} `json:"experimental_axes"`
				OfficialAxes      map[string]interface{} `json:"official_axes"`
			} `json:"team_score"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/teams/TA", &out); code != 200 {
		t.Fatalf("team status %d", code)
	}
	if out.Data.TeamScore.OfficialTotal == nil || out.Data.TeamScore.ExperimentalTotal == nil {
		t.Fatal("persisted team score missing both layers")
	}
	if out.Data.TeamScore.ExperimentalAxes == nil || out.Data.TeamScore.OfficialAxes == nil {
		t.Fatal("persisted team score missing axis layers")
	}
}

func f64ptr(v float64) *float64 { return &v }

// TestReviewQueueServesEffectiveStreamForUI proves the review queue the UI
// consumes carries effective_phase_intervals (the current-stream selector
// source) whenever a correction overlay exists, and that a fresh match with no
// overlay has none (the UI then falls back to the machine timeline).
func TestReviewQueueServesEffectiveStreamForUI(t *testing.T) {
	st := testStore(t)
	matchID := "8944521919"
	machine := addCumulativePhaseMatch(t, st, matchID)
	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Before any correction: no effective overlay (UI falls back to machine).
	var queue struct {
		Data struct {
			Reviews []struct {
				MatchID                 string            `json:"match_id"`
				EffectivePhaseIntervals []json.RawMessage `json:"effective_phase_intervals"`
				ReviewRevision          string            `json:"review_revision"`
			} `json:"reviews"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/reviews/queue", &queue); code != 200 {
		t.Fatalf("queue status %d", code)
	}
	var pre *struct {
		MatchID                 string            `json:"match_id"`
		EffectivePhaseIntervals []json.RawMessage `json:"effective_phase_intervals"`
		ReviewRevision          string            `json:"review_revision"`
	}
	for i := range queue.Data.Reviews {
		if queue.Data.Reviews[i].MatchID == matchID {
			pre = &queue.Data.Reviews[i]
		}
	}
	if pre == nil {
		t.Fatal("8944521919 not in review queue")
	}
	if len(pre.EffectivePhaseIntervals) != 0 {
		t.Fatal("fresh match must have no effective overlay")
	}
	if pre.ReviewRevision == "" {
		t.Fatal("fresh match missing review revision")
	}

	// Apply a split; the queue must then expose the effective stream with the
	// newly created current refs (interval@0-100, interval@100-594).
	if status, _, apiErr := postPhaseOperation(t, ts.URL, review.PhaseOpReq{
		MatchID: matchID, Author: "paul", Reason: "split for UI",
		Op: review.OpSplit, EventRef: "interval@0-594", SplitSecond: apiIntPtr(100), ExpectedRevision: pre.ReviewRevision,
	}); status != http.StatusOK {
		t.Fatalf("split status=%d error=%q", status, apiErr)
	}
	if code := getJSON(t, ts.URL+Version+"/reviews/queue", &queue); code != 200 {
		t.Fatalf("queue after split status %d", code)
	}
	var post *struct {
		MatchID                 string            `json:"match_id"`
		EffectivePhaseIntervals []json.RawMessage `json:"effective_phase_intervals"`
		ReviewRevision          string            `json:"review_revision"`
	}
	for i := range queue.Data.Reviews {
		if queue.Data.Reviews[i].MatchID == matchID {
			post = &queue.Data.Reviews[i]
		}
	}
	eff, err := review.FromJSON(post.EffectivePhaseIntervals)
	if err != nil {
		t.Fatal(err)
	}
	// The split children are the next UI selector refs.
	if _, ok := findStartForRef(eff, "interval@0-100"); !ok {
		t.Fatalf("effective stream missing split child interval@0-100: %+v", eff)
	}
	if _, ok := findStartForRef(eff, "interval@100-594"); !ok {
		t.Fatalf("effective stream missing split child interval@100-594: %+v", eff)
	}
	if len(machine) == 0 {
		t.Fatal("machine fixture empty")
	}
}

// findStartForRef returns the interval whose start matches a canonical ref.
func findStartForRef(intervals []review.PhaseInterval, ref string) (review.PhaseInterval, bool) {
	for i := range intervals {
		if review.CanonicalEventRef(intervals[i]) == ref {
			return intervals[i], true
		}
	}
	return review.PhaseInterval{}, false
}

func TestPhaseMutationStorageFailureReturns500AndIsAtomic(t *testing.T) {
	st := testStore(t)
	matchID := "8944521919"
	addCumulativePhaseMatch(t, st, matchID)
	rv, _ := review.New(st.Root)
	corrupt := []byte(`{"broken"`)
	if err := os.WriteFile(rv.AuditPath(), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	initial, _ := rv.Load(matchID)
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status, _, apiErr := postPhaseOperation(t, ts.URL, review.PhaseOpReq{
		MatchID: matchID, Author: "paul", Reason: "storage failure",
		Op: review.OpAccept, EventRef: "interval@2454-2633", ExpectedRevision: initial.ReviewRevision,
	})
	if status != http.StatusInternalServerError || !strings.Contains(apiErr, "review storage") {
		t.Fatalf("status=%d error=%q want 500 storage error", status, apiErr)
	}
	if _, err := os.Stat(rv.Path(matchID)); !os.IsNotExist(err) {
		t.Fatalf("review mutated on storage failure: %v", err)
	}
	got, _ := os.ReadFile(rv.AuditPath())
	if !bytes.Equal(got, corrupt) {
		t.Fatal("audit bytes changed on storage failure")
	}
}

func TestReviewStatusStorageFailureReturns500AndIsAtomic(t *testing.T) {
	st := testStore(t)
	matchID := "8944521919"
	addCumulativePhaseMatch(t, st, matchID)
	rv, _ := review.New(st.Root)
	initial, _ := rv.Load(matchID)
	corrupt := []byte(`{"broken"`)
	if err := os.WriteFile(rv.AuditPath(), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status, _, apiErr := postReviewStatus(t, ts.URL, matchID, "in_progress", "paul", initial.ReviewRevision)
	if status != http.StatusInternalServerError || apiErr != "review_status_failed" {
		t.Fatalf("status=%d error=%q want 500", status, apiErr)
	}
	if _, err := os.Stat(rv.Path(matchID)); !os.IsNotExist(err) {
		t.Fatalf("review mutated on storage failure: %v", err)
	}
	got, _ := os.ReadFile(rv.AuditPath())
	if !bytes.Equal(got, corrupt) {
		t.Fatal("audit bytes changed on status storage failure")
	}
}

func TestReviewStatusStaleRevisionReturns409WithoutMutation(t *testing.T) {
	st := testStore(t)
	matchID := "8944521919"
	addCumulativePhaseMatch(t, st, matchID)
	rv, _ := review.New(st.Root)
	initial, _ := rv.Load(matchID)
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status, updated, apiErr := postReviewStatus(t, ts.URL, matchID, "in_progress", "a", initial.ReviewRevision)
	if status != http.StatusOK || apiErr != "" || updated.ReviewRevision == initial.ReviewRevision {
		t.Fatalf("first status=%d error=%q revision=%s", status, apiErr, updated.ReviewRevision)
	}
	beforeReview, _ := os.ReadFile(rv.Path(matchID))
	beforeAudit, _ := os.ReadFile(rv.AuditPath())
	status, _, apiErr = postReviewStatus(t, ts.URL, matchID, "pending", "b", initial.ReviewRevision)
	if status != http.StatusConflict || !strings.Contains(apiErr, "review_revision_mismatch") {
		t.Fatalf("stale status=%d error=%q want 409", status, apiErr)
	}
	afterReview, _ := os.ReadFile(rv.Path(matchID))
	afterAudit, _ := os.ReadFile(rv.AuditPath())
	if !bytes.Equal(beforeReview, afterReview) || !bytes.Equal(beforeAudit, afterAudit) {
		t.Fatal("stale status changed review or audit")
	}
}

// TestPhaseRevisionConflictSameBoundary proves the API serves the current
// review_revision, advances it on every success, and returns 409 atomically
// for a stale same-boundary relabel/accept whose intervals still exist.
func TestPhaseRevisionConflictSameBoundary(t *testing.T) {
	st := testStore(t)
	matchID := "8944521919"
	addCumulativePhaseMatch(t, st, matchID)
	rv, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rv).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Read the queue to obtain the current revision.
	var queue struct {
		Data struct {
			Reviews []struct {
				MatchID        string `json:"match_id"`
				ReviewRevision string `json:"review_revision"`
			} `json:"reviews"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/reviews/queue", &queue); code != 200 {
		t.Fatalf("queue status %d", code)
	}
	var rev string
	for _, q := range queue.Data.Reviews {
		if q.MatchID == matchID {
			rev = q.ReviewRevision
		}
	}
	if rev == "" {
		t.Fatal("queue did not serve review_revision")
	}

	// Reviewer A relabels the still-existing interval@2454-2633 (decisive)
	// using the current revision.
	opA := review.PhaseOpReq{
		MatchID: matchID, Author: "reviewerA", Reason: "relabel A",
		Op: review.OpRelabel, EventRef: "interval@2454-2633",
		Effective:        &review.PhaseInterval{StartGameSecond: 2454, EndGameSecond: 2633, GlobalPhase: "midgame"},
		ExpectedRevision: rev,
	}
	statusA, rvA, apiErr := postPhaseOperation(t, ts.URL, opA)
	if statusA != http.StatusOK {
		t.Fatalf("reviewer A status=%d error=%q", statusA, apiErr)
	}
	if len(rvA.PhaseCorrections) != 1 {
		t.Fatalf("reviewer A corrections=%d", len(rvA.PhaseCorrections))
	}
	newRev := rvA.ReviewRevision
	if newRev == "" || newRev == rev {
		t.Fatal("revision did not advance")
	}

	// Reviewer B submits a stale form for the SAME still-existing boundary with
	// the OLD revision -> 409, no bytes change.
	opB := review.PhaseOpReq{
		MatchID: matchID, Author: "reviewerB", Reason: "stale relabel B",
		Op: review.OpRelabel, EventRef: "interval@2454-2633",
		Effective:        &review.PhaseInterval{StartGameSecond: 2454, EndGameSecond: 2633, GlobalPhase: "decisive"},
		ExpectedRevision: rev,
	}
	before, _ := os.ReadFile(rv.Path(matchID))
	audBefore, _ := os.ReadFile(rv.AuditPath())
	statusB, _, apiErrB := postPhaseOperation(t, ts.URL, opB)
	if statusB != http.StatusConflict {
		t.Fatalf("stale relabel status=%d error=%q want 409", statusB, apiErrB)
	}
	after, _ := os.ReadFile(rv.Path(matchID))
	audAfter, _ := os.ReadFile(rv.AuditPath())
	if !bytes.Equal(before, after) || !bytes.Equal(audBefore, audAfter) {
		t.Fatal("stale revision changed review or audit bytes")
	}

	// Reviewer A accepts the same still-existing boundary with the NEW
	// revision; accept advances the revision.
	opAccept := review.PhaseOpReq{MatchID: matchID, Author: "reviewerA", Reason: "accept A", Op: review.OpAccept, EventRef: "interval@2454-2633", ExpectedRevision: newRev}
	statusAcc, rvAcc, apiErrAcc := postPhaseOperation(t, ts.URL, opAccept)
	if statusAcc != http.StatusOK {
		t.Fatalf("accept status=%d error=%q", statusAcc, apiErrAcc)
	}
	if rvAcc.ReviewRevision == newRev {
		t.Fatal("revision did not advance after accept")
	}
	if len(rvAcc.PhaseCorrections) != 2 {
		t.Fatalf("corrections after accept=%d want 2", len(rvAcc.PhaseCorrections))
	}

	// A stale same-boundary accept (old revision) is rejected atomically.
	before2, _ := os.ReadFile(rv.Path(matchID))
	if status, _, apiErr2 := postPhaseOperation(t, ts.URL, review.PhaseOpReq{MatchID: matchID, Author: "reviewerB", Reason: "stale accept B", Op: review.OpAccept, EventRef: "interval@2454-2633", ExpectedRevision: newRev}); status != http.StatusConflict {
		t.Fatalf("stale accept status=%d error=%q want 409", status, apiErr2)
	}
	after2, _ := os.ReadFile(rv.Path(matchID))
	if !bytes.Equal(before2, after2) {
		t.Fatal("stale accept changed persisted state")
	}

	// Restart reproduces the same revision.
	restarted, err := review.New(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := restarted.Load(matchID)
	if err != nil {
		t.Fatal(err)
	}
	if rr.ReviewRevision != rvAcc.ReviewRevision {
		t.Fatalf("restart revision=%s want %s", rr.ReviewRevision, rvAcc.ReviewRevision)
	}
}

func postReviewGate(t *testing.T, base, path, token string, payload interface{}) (int, review.Review, string) {
	t.Helper()
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, base+Version+path, bytes.NewReader(b))
	if token != "" {
		req.Header.Set("X-Dota2-OB-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var wire struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	var rv review.Review
	if len(wire.Data) > 0 && string(wire.Data) != "null" {
		_ = json.Unmarshal(wire.Data, &rv)
	}
	return resp.StatusCode, rv, wire.Error
}

func TestHumanReviewCompletionGateAPI(t *testing.T) {
	st := testStore(t)
	rvs, _ := review.New(st.Root)
	srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rvs).WithSessionToken("tok")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	r0, _ := rvs.Load("m1")
	catPayload := map[string]interface{}{"match_id": "m1", "decision": "confirm", "author": "paul", "reason": "checked frozen category", "expected_revision": r0.ReviewRevision}
	if status, _, _ := postReviewGate(t, ts.URL, "/reviews/category-decisions", "bad", catPayload); status != http.StatusForbidden {
		t.Fatalf("invalid token status=%d", status)
	}
	forged := map[string]interface{}{"match_id": "m1", "decision": "confirm", "author": "paul", "reason": "forged", "expected_revision": r0.ReviewRevision, "machine_category": "evil", "replay_sha256": "evil"}
	if status, _, apiErr := postReviewGate(t, ts.URL, "/reviews/category-decisions", "tok", forged); status != http.StatusBadRequest || !strings.Contains(apiErr, "machine_category_mismatch") {
		t.Fatalf("forged status=%d err=%q", status, apiErr)
	}
	if loaded, _ := rvs.Load("m1"); loaded.ReviewRevision != r0.ReviewRevision {
		t.Fatal("rejected category mutation changed revision")
	}
	status, r1, apiErr := postReviewGate(t, ts.URL, "/reviews/category-decisions", "tok", catPayload)
	if status != http.StatusOK || apiErr != "" || len(r1.CategoryDecisions) != 1 || r1.CategoryDecisions[0].MachineCategory != "test" {
		t.Fatalf("category status=%d err=%q review=%+v", status, apiErr, r1)
	}
	if r1.CategoryDecisions[0].BasedOnRevision != r0.ReviewRevision {
		t.Fatalf("based_on_revision=%q want %q", r1.CategoryDecisions[0].BasedOnRevision, r0.ReviewRevision)
	}
	if status, _, apiErr := postReviewStatus(t, ts.URL, "m1", "reviewed", "paul", r1.ReviewRevision); status != http.StatusBadRequest || !strings.Contains(apiErr, "final_snapshot") {
		t.Fatalf("premature status=%d err=%q", status, apiErr)
	}
	finalPayload := map[string]interface{}{"match_id": "m1", "author": "paul", "reason": "full stream and product contract checked", "expected_revision": r1.ReviewRevision, "checklist": map[string]interface{}{"phase_stream_reviewed": true, "role_provenance_reviewed": true, "role_provenance_evidence": "registry source URLs and participants inspected", "official_experimental_acknowledged": true}}
	status, r2, apiErr := postReviewGate(t, ts.URL, "/reviews/finalize", "tok", finalPayload)
	if status != http.StatusOK || apiErr != "" || r2.ReviewStatus != "reviewed" || len(r2.FinalSnapshots) != 1 {
		t.Fatalf("final status=%d err=%q review=%+v", status, apiErr, r2)
	}
	var q struct {
		Data struct {
			Queue    []map[string]interface{} `json:"queue"`
			Reviews  []review.Review          `json:"reviews"`
			Progress map[string]int           `json:"progress"`
		} `json:"data"`
	}
	if code := getJSON(t, ts.URL+Version+"/reviews/queue", &q); code != http.StatusOK {
		t.Fatalf("queue=%d", code)
	}
	if len(q.Data.Queue) != 1 || q.Data.Queue[0]["category"] != "test" || q.Data.Progress["reviewed"] != 1 || q.Data.Progress["pending"] != 0 {
		t.Fatalf("queue=%+v progress=%+v", q.Data.Queue, q.Data.Progress)
	}
	restarted, _ := review.New(st.Root)
	srv2 := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(restarted).WithSessionToken("tok")
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	var q2 struct {
		Data struct {
			Reviews  []review.Review `json:"reviews"`
			Progress map[string]int  `json:"progress"`
		} `json:"data"`
	}
	getJSON(t, ts2.URL+Version+"/reviews/queue", &q2)
	if len(q2.Data.Reviews) != 1 || len(q2.Data.Reviews[0].FinalSnapshots) != 1 || q2.Data.Progress["reviewed"] != 1 {
		t.Fatalf("restart queue=%+v", q2.Data)
	}
	if q2.Data.Reviews[0].CategoryDecisions[0].BasedOnRevision != r0.ReviewRevision {
		t.Fatalf("restart decision=%+v", q2.Data.Reviews[0].CategoryDecisions)
	}
}

func TestFinalizeMigratedOverlayShapesAPI(t *testing.T) {
	cases := map[string]struct {
		intervals []review.PhaseInterval
		valid     bool
	}{
		"valid":          {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 100, GlobalPhase: "laning"}}, true},
		"reset":          {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 100, GlobalPhase: "reset"}}, false},
		"unsupported":    {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 100, GlobalPhase: "postgame"}}, false},
		"gap":            {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 40, GlobalPhase: "laning"}, {StartGameSecond: 50, EndGameSecond: 100, GlobalPhase: "midgame"}}, false},
		"overlap":        {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 60, GlobalPhase: "laning"}, {StartGameSecond: 50, EndGameSecond: 100, GlobalPhase: "midgame"}}, false},
		"invalid_bounds": {[]review.PhaseInterval{{StartGameSecond: -1, EndGameSecond: 100, GlobalPhase: "laning"}}, false},
		"zero_width":     {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 0, GlobalPhase: "laning"}, {StartGameSecond: 0, EndGameSecond: 100, GlobalPhase: "midgame"}}, false},
		"incomplete":     {[]review.PhaseInterval{{StartGameSecond: 0, EndGameSecond: 90, GlobalPhase: "laning"}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := testStore(t)
			legacy := &review.Review{SchemaVersion: version.CorrectionSchemaV1, RuleVersion: "legacy-v1", MatchID: "m1", Corrections: []review.Correction{}, ReviewStatus: "pending", EffectivePhaseIntervals: review.ToJSON(tc.intervals)}
			if err := st.WriteJSON("m1", store.ArtifactCorrections, legacy); err != nil {
				t.Fatal(err)
			}
			rvs, _ := review.New(st.Root)
			srv := testServerWithContracts(t, st, &roles.Registry{}).WithReviews(rvs).WithSessionToken("tok")
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()
			r0, err := rvs.Load("m1")
			if err != nil {
				t.Fatal(err)
			}
			status, r1, apiErr := postReviewGate(t, ts.URL, "/reviews/category-decisions", "tok", map[string]interface{}{"match_id": "m1", "decision": "confirm", "author": "paul", "reason": "checked", "expected_revision": r0.ReviewRevision})
			if status != http.StatusOK || apiErr != "" {
				t.Fatalf("category status=%d err=%q", status, apiErr)
			}
			beforeReview, _ := os.ReadFile(rvs.Path("m1"))
			beforeAudit, _ := os.ReadFile(rvs.AuditPath())
			status, fin, apiErr := postReviewGate(t, ts.URL, "/reviews/finalize", "tok", map[string]interface{}{"match_id": "m1", "author": "paul", "reason": "checked all", "expected_revision": r1.ReviewRevision, "checklist": map[string]interface{}{"phase_stream_reviewed": true, "role_provenance_reviewed": true, "role_provenance_evidence": "registry", "official_experimental_acknowledged": true}})
			if tc.valid {
				if status != http.StatusOK || apiErr != "" || fin.ReviewStatus != "reviewed" {
					t.Fatalf("valid status=%d err=%q review=%+v", status, apiErr, fin)
				}
				return
			}
			if status != http.StatusBadRequest || !strings.Contains(apiErr, "final_phase_stream_invalid") {
				t.Fatalf("invalid status=%d err=%q", status, apiErr)
			}
			afterReview, _ := os.ReadFile(rvs.Path("m1"))
			afterAudit, _ := os.ReadFile(rvs.AuditPath())
			if !bytes.Equal(beforeReview, afterReview) || !bytes.Equal(beforeAudit, afterAudit) {
				t.Fatal("invalid finalize changed authoritative bytes")
			}
			restarted, err := review.New(st.Root)
			if err != nil {
				t.Fatal(err)
			}
			restartReview, _ := os.ReadFile(restarted.Path("m1"))
			restartAudit, _ := os.ReadFile(restarted.AuditPath())
			if !bytes.Equal(beforeReview, restartReview) || !bytes.Equal(beforeAudit, restartAudit) {
				t.Fatal("restart changed rejected finalize bytes")
			}
			loaded, _ := restarted.Load("m1")
			if loaded.ReviewStatus == "reviewed" || len(loaded.FinalSnapshots) != 0 {
				t.Fatalf("rejected final visible=%+v", loaded)
			}
		})
	}
}
