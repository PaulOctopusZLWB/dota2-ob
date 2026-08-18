// Package api implements the local loopback read API for the replay product.
// All endpoints are versioned under /api/replay/v1 and serve persisted
// artifacts directly; the catalog and reports are rebuildable from the
// recomputable artifact tree. Read-only endpoints never mutate analytical
// state. Responses carry schema versions, artifact identities, and explicit
// unavailable reasons.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

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

// Version is the API version prefix.
const Version = "/api/replay/v1"

// Server serves the replay read API from a store.
type Server struct {
	Store     *store.Store
	RoleReg   *roles.Registry
	Overrides *roles.OverrideFile
	RoleFile  string
	// MetricReg is the frozen metric registry (full V1/V2/V3 contract).
	MetricReg *metrics.Registry
	// MetricClosure is the frozen 52-row metric closure contract (definition-
	// specific evaluator/gate mapping, served for algorithm drilldown).
	MetricClosure *metrics.Closure
	// ScoringContract is the frozen radar/score contract.
	ScoringContract *scoring.Contract
	// TeamContract is the frozen team scoring registry.
	TeamContract *scoring.TeamContract
	// Reviews is the gold-review correction store (may be nil for read-only
	// mode, in which case mutation endpoints are rejected).
	Reviews *review.Store
	// SessionToken is the local anti-CSRF/session token required by all
	// mutation endpoints. Empty means mutations are rejected.
	SessionToken string
}

// New creates an API server. The role registry is a publication gate: when it
// is nil, reports still list participants (unassigned with reasons) but never
// claim published roles.
func New(st *store.Store, roleReg *roles.Registry, overrides *roles.OverrideFile, roleFile string) *Server {
	return &Server{Store: st, RoleReg: roleReg, Overrides: overrides, RoleFile: roleFile}
}

// WithContracts binds the frozen metric registry, radar/score contract, and
// team scoring registry.
func (s *Server) WithContracts(mreg *metrics.Registry, sc *scoring.Contract) *Server {
	s.MetricReg = mreg
	s.ScoringContract = sc
	return s
}

// WithMetricClosure binds the frozen 52-row metric closure contract.
func (s *Server) WithMetricClosure(cl *metrics.Closure) *Server {
	s.MetricClosure = cl
	return s
}

// WithTeamContract binds the frozen team scoring registry.
func (s *Server) WithTeamContract(tc *scoring.TeamContract) *Server {
	s.TeamContract = tc
	return s
}

// WithReviews binds the gold-review correction store.
func (s *Server) WithReviews(rv *review.Store) *Server {
	s.Reviews = rv
	return s
}

// WithSessionToken sets the loopback mutation session token.
func (s *Server) WithSessionToken(token string) *Server {
	s.SessionToken = token
	return s
}

// Handler returns the root http.Handler for the replay API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(Version+"/corpus", s.handleCorpus)
	mux.HandleFunc(Version+"/matches", s.handleMatches)
	mux.HandleFunc(Version+"/matches/", s.handleMatchDetail)
	mux.HandleFunc(Version+"/players/", s.handlePlayer)
	mux.HandleFunc(Version+"/teams/", s.handleTeam)
	mux.HandleFunc(Version+"/metrics/registry", s.handleMetricsRegistry)
	mux.HandleFunc(Version+"/roles", s.handleRoles)
	mux.HandleFunc(Version+"/reviews/queue", s.handleReviewsQueue)
	mux.HandleFunc(Version+"/reviews/audit", s.handleReviewsAudit)
	mux.HandleFunc(Version+"/reviews/phase-corrections", s.handlePhaseCorrections)
	mux.HandleFunc(Version+"/reviews/event-corrections", s.handleEventCorrections)
	mux.HandleFunc(Version+"/reviews/status", s.handleReviewStatus)
	mux.HandleFunc(Version+"/roles/overrides", s.handleRoleOverrides)
	mux.HandleFunc(Version+"/scores/corpus", s.handleScoreCorpus)
	return mux
}

type envelope struct {
	SchemaVersion string      `json:"schema_version"`
	GeneratedAt   string      `json:"generated_at"`
	Data          interface{} `json:"data"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]string{"error": reason})
}

// handleCorpus serves the rebuildable catalog with terminal states.
func (s *Server) handleCorpus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var cat store.Catalog
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cat); err != nil {
		writeErr(w, http.StatusInternalServerError, "catalog_unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: cat})
}

// handleMatches lists match reports from the catalog.
func (s *Server) handleMatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var cat store.Catalog
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cat); err != nil {
		writeErr(w, http.StatusInternalServerError, "catalog_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: cat.Matches})
}

// handleMatchDetail serves one match report, timeline, or tracks.
func (s *Server) handleMatchDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, Version+"/matches/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, http.StatusBadRequest, "match_id_required")
		return
	}
	matchID := parts[0]
	switch {
	case len(parts) == 1:
		rep, err := s.loadReport(matchID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "report_build_failed")
			return
		}
		rep.SortParticipants()
		writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: rep})
	case len(parts) == 2 && parts[1] == "timeline":
		writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.PhaseSchema, Data: s.timeline(matchID)})
	case len(parts) == 2 && parts[1] == "tracks":
		writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.FactsSchema, Data: s.tracks(matchID)})
	case len(parts) == 2 && parts[1] == "scores":
		ms := s.matchScoresFor(matchID)
		if ms == nil {
			writeErr(w, http.StatusNotFound, "scores_unavailable")
			return
		}
		SortScores(ms)
		writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ScoreSchema, Data: ms})
	case len(parts) >= 2 && parts[1] == "facts":
		s.handleFactDetail(w, matchID, parts)
	case len(parts) >= 2 && parts[1] == "episodes":
		s.handleEpisodeDetail(w, matchID, parts)
	case len(parts) >= 2 && parts[1] == "phases":
		s.handlePhaseIntervalDetail(w, matchID, parts)
	case len(parts) >= 2 && parts[1] == "metrics":
		s.handleMetricObservationDetail(w, matchID, parts)
	case len(parts) >= 2 && parts[1] == "algorithms":
		s.handleAlgorithmDetail(w, matchID, parts)
	default:
		writeErr(w, http.StatusNotFound, "not_found")
	}
}

// handleFactDetail serves one match-qualified fact by seq. Stale/unknown refs
// render an explicit unavailable reason, never a misleading link target.
func (s *Server) handleFactDetail(w http.ResponseWriter, matchID string, parts []string) {
	if len(parts) != 3 {
		writeErr(w, http.StatusBadRequest, "fact_seq_required")
		return
	}
	seq, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || seq < 1 {
		writeErr(w, http.StatusBadRequest, "invalid_fact_seq")
		return
	}
	rf, err := s.Store.OpenArtifact(matchID, store.ArtifactFacts)
	if err != nil {
		writeErr(w, http.StatusNotFound, "facts_unavailable")
		return
	}
	defer rf.Close()
	r := facts.NewReader(rf)
	for {
		f, err := r.Next()
		if err != nil {
			writeErr(w, http.StatusNotFound, "fact_not_found:seq="+parts[2])
			return
		}
		if f.Seq == seq {
			writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.FactsSchema, Data: f})
			return
		}
	}
}

// handleEpisodeDetail serves one match-qualified episode by its stable id.
func (s *Server) handleEpisodeDetail(w http.ResponseWriter, matchID string, parts []string) {
	if len(parts) != 3 {
		writeErr(w, http.StatusBadRequest, "episode_id_required")
		return
	}
	id := parts[2]
	var ep episodes.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactEpisodes, &ep); err != nil {
		writeErr(w, http.StatusNotFound, "episodes_unavailable")
		return
	}
	for i := range ep.Episodes {
		if ep.Episodes[i].ID == id {
			writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.EpisodeSchema, Data: ep.Episodes[i]})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "episode_not_found:"+id)
}

// handlePhaseIntervalDetail serves one match-qualified phase interval by
// canonical ref (interval@START-END or START-END).
func (s *Server) handlePhaseIntervalDetail(w http.ResponseWriter, matchID string, parts []string) {
	if len(parts) != 3 {
		writeErr(w, http.StatusBadRequest, "phase_ref_required")
		return
	}
	st, en, ok := review.ParseEventRef(parts[2])
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_phase_ref")
		return
	}
	var ph phase.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactPhases, &ph); err != nil {
		writeErr(w, http.StatusNotFound, "phases_unavailable")
		return
	}
	for i := range ph.Intervals {
		iv := &ph.Intervals[i]
		if iv.StartGameSecond == st && (en < 0 || iv.EndGameSecond == en) {
			writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.PhaseSchema, Data: iv})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "phase_interval_not_found:"+parts[2])
}

// handleMetricObservationDetail serves one match-qualified metric observation
// (all rows for the metric id in this match, published and unavailable).
func (s *Server) handleMetricObservationDetail(w http.ResponseWriter, matchID string, parts []string) {
	if len(parts) != 3 {
		writeErr(w, http.StatusBadRequest, "metric_id_required")
		return
	}
	metricID := parts[2]
	var met metrics.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactMetrics, &met); err != nil {
		writeErr(w, http.StatusNotFound, "metrics_unavailable")
		return
	}
	rows := []metrics.Value{}
	for _, v := range met.Values {
		if v.MetricID == metricID {
			rows = append(rows, v)
		}
	}
	for _, v := range met.Unavailable {
		if v.MetricID == metricID {
			rows = append(rows, v)
		}
	}
	if len(rows) == 0 {
		writeErr(w, http.StatusNotFound, "metric_observation_not_found:"+metricID)
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.MetricsSchema, Data: map[string]interface{}{
		"match_id": matchID, "metric_id": metricID, "rows": rows,
	}})
}

// handleAlgorithmDetail serves the closure-contract evaluator entry for a
// metric's algorithm/evaluator id (match-qualified for audit stability).
func (s *Server) handleAlgorithmDetail(w http.ResponseWriter, matchID string, parts []string) {
	if len(parts) != 3 {
		writeErr(w, http.StatusBadRequest, "evaluator_id_required")
		return
	}
	if s.MetricClosure == nil {
		writeErr(w, http.StatusNotFound, "metric_closure_unavailable")
		return
	}
	for i := range s.MetricClosure.Metrics {
		row := &s.MetricClosure.Metrics[i]
		if row.EvaluatorID == parts[2] {
			writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ClosureSchema, Data: row})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "algorithm_not_found:"+parts[2])
}

// loadReport returns the authoritative persisted report artifact when present.
// The persisted report carries the gated status/reason written by the runner;
// the API consumes that durable state rather than independently re-deriving
// publication from source artifacts. The canonical record (written after the
// report) is attached for display. Only when no persisted report exists (e.g.
// pre-existing data root) does it fall back to a rebuild.
func (s *Server) loadReport(matchID string) (*report.Report, error) {
	var rep report.Report
	if err := s.Store.ReadJSON(matchID, store.ArtifactReport, &rep); err == nil {
		var can store.Canonical
		if err := s.Store.ReadJSON(matchID, store.ArtifactCanonical, &can); err == nil {
			rep.Canonical = &can
		}
		return &rep, nil
	}
	return report.Build(s.Store, matchID, s.RoleReg, s.effectiveOverrides())
}

// effectiveOverrides returns the authoritative data-root override store when
// present (review mutations write here), falling back to the serve-time
// overrides.
func (s *Server) effectiveOverrides() *roles.OverrideFile {
	var of roles.OverrideFile
	if err := s.Store.ReadJSONFile(s.Store.Root+"/role-overrides-effective.json", &of); err == nil {
		return &of
	}
	return s.Overrides
}

// timeline merges the phase stream and episodes into one renderable timeline.
type timeline struct {
	MatchID  string           `json:"match_id"`
	Phases   *phase.Output    `json:"phases"`
	Episodes *episodes.Output `json:"episodes"`
}

func (s *Server) timeline(matchID string) timeline {
	t := timeline{MatchID: matchID}
	var ph phase.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactPhases, &ph); err == nil {
		t.Phases = &ph
	}
	var ep episodes.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactEpisodes, &ep); err == nil {
		t.Episodes = &ep
	}
	return t
}

// tracks returns a downsampled per-player position/economy view for the match
// explorer.
type tracks struct {
	MatchID string        `json:"match_id"`
	Tracks  []playerTrack `json:"tracks"`
}

type playerTrack struct {
	AccountID string       `json:"account_id"`
	HeroName  string       `json:"hero_name"`
	Samples   []trackPoint `json:"samples"`
}

type trackPoint struct {
	GameSecond float64  `json:"game_second"`
	X          *float64 `json:"x"`
	Y          *float64 `json:"y"`
	Health     *int64   `json:"health"`
	Level      *int32   `json:"level"`
}

func (s *Server) tracks(matchID string) tracks {
	t := tracks{MatchID: matchID, Tracks: []playerTrack{}}
	rf, err := s.Store.OpenArtifact(matchID, store.ArtifactFacts)
	if err != nil {
		return t
	}
	defer rf.Close()
	byAcct := map[string]*playerTrack{}
	r := facts.NewReader(rf)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		if f.Family != facts.FamilyHeroState {
			continue
		}
		var hs facts.HeroStateSample
		if err := json.Unmarshal(f.Payload, &hs); err != nil {
			continue
		}
		pt := byAcct[hs.AccountID]
		if pt == nil {
			pt = &playerTrack{AccountID: hs.AccountID, HeroName: hs.HeroName, Samples: []trackPoint{}}
			byAcct[hs.AccountID] = pt
		}
		pt.Samples = append(pt.Samples, trackPoint{GameSecond: f.GameSecond, X: hs.PosX, Y: hs.PosY, Health: hs.Health, Level: hs.Level})
	}
	keys := make([]string, 0, len(byAcct))
	for k := range byAcct {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Tracks = append(t.Tracks, *byAcct[k])
	}
	return t
}

// corpusScoresFor loads the corpus-level scoring catalog (players, teams,
// per-match rows). Returns nil when absent.
func (s *Server) corpusScoresFor() *scoring.CorpusScores {
	var cs scoring.CorpusScores
	if err := s.Store.ReadJSONFile(s.Store.Root+"/scores-corpus.json", &cs); err != nil {
		return nil
	}
	return &cs
}

// handlePlayer serves one player's tournament profile (same-role aggregation,
// radar, totals, subject coverage) plus per-match drilldown rows.
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	accountID := strings.TrimPrefix(r.URL.Path, Version+"/players/")
	accountID = strings.Trim(accountID, "/")
	if accountID == "" {
		writeErr(w, http.StatusBadRequest, "account_id_required")
		return
	}
	var cat store.Catalog
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cat); err != nil {
		writeErr(w, http.StatusInternalServerError, "catalog_unavailable")
		return
	}
	out := map[string]interface{}{
		"account_id": accountID,
		"matches":    []map[string]interface{}{},
	}
	// Player-tournament snapshot (the scoring product grain).
	if cs := s.corpusScoresFor(); cs != nil {
		for _, ps := range cs.Players {
			if ps.AccountID == accountID {
				out["score"] = ps
				break
			}
		}
	}
	for _, row := range cat.Matches {
		rep, err := s.loadReport(row.MatchID)
		if err != nil {
			continue
		}
		for _, p := range rep.Participants {
			if p.AccountID != accountID {
				continue
			}
			entry := map[string]interface{}{
				"match_id": row.MatchID, "status": rep.Status, "publication": rep.Publication,
				"team_id": p.TeamID, "team_name": p.TeamName, "side": p.Side,
				"hero": p.HeroName, "nominal_role": p.NominalRole,
				"role_source": p.RoleSourceKind, "role_source_url": p.RoleSourceURL,
				"override_applied": p.OverrideApplied, "override_reason": p.OverrideReason, "override_at": p.OverrideAt,
			}
			if rep.Phases != nil {
				entry["phase_intervals"] = len(rep.Phases.Intervals)
			}
			if ms := s.matchScoresFor(row.MatchID); ms != nil {
				for _, ps := range ms.Players {
					if ps.AccountID != accountID {
						continue
					}
					entry["match_metrics"] = ps.Metrics
					break
				}
			}
			out["matches"] = append(out["matches"].([]map[string]interface{}), entry)
		}
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: out})
}

// handleTeam serves one team's tournament profile (derived team scoring
// registry) plus per-match player drilldown rows. The team score is computed
// from the persisted corpus catalog, not hard-coded.
func (s *Server) handleTeam(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	teamID := strings.TrimPrefix(r.URL.Path, Version+"/teams/")
	teamID = strings.Trim(teamID, "/")
	if teamID == "" {
		writeErr(w, http.StatusBadRequest, "team_id_required")
		return
	}
	var cat store.Catalog
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cat); err != nil {
		writeErr(w, http.StatusInternalServerError, "catalog_unavailable")
		return
	}
	out := map[string]interface{}{
		"team_id": teamID,
		"matches": []map[string]interface{}{},
	}
	if cs := s.corpusScoresFor(); cs != nil {
		for _, ts := range cs.Teams {
			if ts.TeamID == teamID {
				out["team_score"] = ts
				break
			}
		}
	}
	for _, row := range cat.Matches {
		rep, err := s.loadReport(row.MatchID)
		if err != nil {
			continue
		}
		for _, t := range rep.Teams {
			if t.TeamID != teamID {
				continue
			}
			entry := map[string]interface{}{
				"match_id": row.MatchID, "status": rep.Status, "publication": rep.Publication,
				"team_name": t.TeamName, "side": t.Side,
			}
			if ms := s.matchScoresFor(row.MatchID); ms != nil {
				players := []interface{}{}
				for _, ps := range ms.Players {
					if ps.TeamID != "" && ps.TeamID == teamID {
						players = append(players, ps)
					}
				}
				entry["players"] = players
			}
			out["matches"] = append(out["matches"].([]map[string]interface{}), entry)
			break
		}
	}
	if _, ok := out["team_score"]; !ok {
		out["team_score"] = map[string]interface{}{
			"team_id": teamID, "axes": map[string]interface{}{}, "published": false,
			"reasons": []string{"team_not_in_persisted_corpus"},
		}
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: out})
}

// handleMetricsRegistry serves the full frozen metric registry definitions.
func (s *Server) handleMetricsRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	defs := metrics.RegistryOutput(s.MetricReg)
	if s.MetricReg == nil {
		writeErr(w, http.StatusNotFound, "metric_registry_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{
		SchemaVersion: version.MetricsSchema,
		Data: map[string]interface{}{
			"rule_version":     version.MetricsRuleVersion,
			"registry_version": s.MetricReg.SchemaVersion,
			"metric_count":     len(defs),
			"definitions":      defs,
		},
	})
}

// handleRoles serves the auditable nominal-role registry.
func (s *Server) handleRoles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if s.RoleReg == nil {
		writeErr(w, http.StatusNotFound, "role_registry_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.RoleSchema, Data: s.RoleReg})
}

// ParseMatchID validates a numeric match id path segment.
func ParseMatchID(v string) (uint64, error) {
	return strconv.ParseUint(v, 10, 64)
}

var _ = fmt.Sprintf
