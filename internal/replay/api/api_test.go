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
	body := `{"match_id":"m1","author":"paul","reason":"relabeled on evidence","operation":"relabel","event_ref":"interval@0-100","effective_value":{"start_game_second":0,"end_game_second":100,"global_phase":"midgame"}}`
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
	var final review.Review
	for i := range operations {
		operations[i].MatchID = matchID
		operations[i].Author = "paul"
		operations[i].Reason = fmt.Sprintf("cumulative step %d", i+1)
		operations[i].EvidenceIDs = []string{fmt.Sprintf("phase:%d", i+1)}
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
		EventRef: "interval@100-999", Effective: &review.PhaseInterval{StartGameSecond: 100, EndGameSecond: 700, GlobalPhase: "midgame"},
	}
	if status, _, apiErr := postPhaseOperation(t, ts.URL, stale); status != http.StatusConflict {
		t.Fatalf("stale status=%d error=%q want 409", status, apiErr)
	}
	invalid := review.PhaseOpReq{
		MatchID: matchID, Author: "paul", Reason: "illegal phase", Op: review.OpRelabel,
		EventRef: "interval@100-700", Effective: &review.PhaseInterval{StartGameSecond: 100, EndGameSecond: 700, GlobalPhase: "reset"},
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
	st.WriteJSON("m1", store.ArtifactMetrics, &metrics.Output{SchemaVersion: "replay.metrics.v3", MatchID: "m1", Values: []metrics.Value{{
		MetricID: "hero_damage_total", Name: "Hero damage", ReportLevel: "player", AccountID: "1000",
		EpistemicClass: "derived", CapabilityLevel: "V1", MetricVersion: "1.0.0",
		Evidence: []metrics.EvidenceRef{{MatchID: "m1", Kind: "fact", ID: "fact:7", SourceFactSeq: 7}},
	}}})

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
	if code := getJSON(t, ts.URL+Version+"/matches/m1/metrics/hero_damage_total", &metEnv); code != 200 || metEnv.Data.MetricID != "hero_damage_total" {
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
		vals = append(vals, map[string]interface{}{"metric_id": "hero_damage_total", "account_id": a.acct, "value": 1000, "unit": "damage", "report_level": "player"})
		vals = append(vals, map[string]interface{}{"metric_id": "kill_count", "account_id": a.acct, "value": 5, "unit": "count", "report_level": "player"})
	}
	if err := st.WriteJSON("8944521919", store.ArtifactMetrics, map[string]interface{}{"values": vals, "unavailable": []interface{}{}}); err != nil {
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
			}
		}
		if !found {
			t.Fatalf("step %d player has no 8944521919 match row", step)
		}
		// /matches/{id}/scores agrees.
		var ms struct {
			Data struct {
				Players []struct {
					AccountID   string `json:"account_id"`
					NominalRole string `json:"nominal_role"`
				} `json:"players"`
			} `json:"data"`
		}
		if code := getJSON(t, baseURL+Version+"/matches/8944521919/scores", &ms); code != 200 {
			t.Fatalf("scores status %d", code)
		}
		for _, p := range ms.Data.Players {
			if p.AccountID == ACCT && p.NominalRole != wantEffective {
				t.Fatalf("step %d match scores effective=%s want %s", step, p.NominalRole, wantEffective)
			}
		}
		// Corpus / player tournament score agrees and cohort is recomputed.
		var cs struct {
			Data struct {
				Players []struct {
					AccountID   string `json:"account_id"`
					NominalRole string `json:"nominal_role"`
				} `json:"players"`
			} `json:"data"`
		}
		if code := getJSON(t, baseURL+Version+"/scores/corpus", &cs); code != 200 {
			t.Fatalf("corpus status %d", code)
		}
		for _, p := range cs.Data.Players {
			if p.AccountID == ACCT && p.NominalRole != wantEffective {
				t.Fatalf("step %d corpus effective=%s want %s", step, p.NominalRole, wantEffective)
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
		SchemaVersion:        "replay.score.v2",
		RuleVersion:          "ti2026.scoring.v2",
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
