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
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
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
}

// New creates an API server. The role registry is a publication gate: when it
// is nil, reports still list participants (unassigned with reasons) but never
// claim published roles.
func New(st *store.Store, roleReg *roles.Registry, overrides *roles.OverrideFile, roleFile string) *Server {
	return &Server{Store: st, RoleReg: roleReg, Overrides: overrides, RoleFile: roleFile}
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
		rep, err := report.Build(s.Store, matchID, s.RoleReg, s.Overrides)
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
	default:
		writeErr(w, http.StatusNotFound, "not_found")
	}
}

// timeline merges the phase stream and episodes into one renderable timeline.
type timeline struct {
	MatchID  string             `json:"match_id"`
	Phases   *phase.Output      `json:"phases"`
	Episodes *episodes.Output   `json:"episodes"`
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
	AccountID string     `json:"account_id"`
	HeroName  string     `json:"hero_name"`
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

// handlePlayer aggregates one player's matches from the catalog.
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
	for _, row := range cat.Matches {
		rep, err := report.Build(s.Store, row.MatchID, s.RoleReg, s.Overrides)
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
				"role_source": p.RoleSource, "role_source_url": p.RoleSourceURL,
			}
			if rep.Phases != nil {
				entry["phase_intervals"] = len(rep.Phases.Intervals)
			}
			out["matches"] = append(out["matches"].([]map[string]interface{}), entry)
		}
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: out})
}

// handleTeam aggregates one team's matches from the catalog.
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
	for _, row := range cat.Matches {
		rep, err := report.Build(s.Store, row.MatchID, s.RoleReg, s.Overrides)
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
			out["matches"] = append(out["matches"].([]map[string]interface{}), entry)
			break
		}
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ReportSchema, Data: out})
}

// handleMetricsRegistry serves the frozen V1 metric definitions.
func (s *Server) handleMetricsRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	defs := metrics.Definitions()
	writeJSON(w, http.StatusOK, envelope{
		SchemaVersion: version.MetricsSchema,
		Data: map[string]interface{}{
			"rule_version": version.MetricsRuleVersion,
			"definitions":  defs,
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

// handleReviewsQueue serves the stage-2 review queue (empty; stage 4 adds
// corrections).
func (s *Server) handleReviewsQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{
		SchemaVersion: version.PhaseSchema,
		Data: map[string]interface{}{
			"queue":       []interface{}{},
			"unavailable": "review_workflow_stage4",
			"stage":       "2",
		},
	})
}

// ParseMatchID validates a numeric match id path segment.
func ParseMatchID(v string) (uint64, error) {
	return strconv.ParseUint(v, 10, 64)
}

var _ = fmt.Sprintf