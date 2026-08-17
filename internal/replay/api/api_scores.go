package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/review"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/scoring"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// requireSessionToken rejects mutations without the loopback session token.
// Read-only pages are unaffected; review mutations are authenticated mutations.
func (s *Server) requireSessionToken(w http.ResponseWriter, r *http.Request) bool {
	if s.SessionToken == "" {
		writeErr(w, http.StatusForbidden, "mutation_disabled_no_session_token")
		return false
	}
	tok := r.Header.Get("X-Dota2-OB-Token")
	if tok == "" || tok != s.SessionToken {
		writeErr(w, http.StatusForbidden, "missing_or_invalid_session_token")
		return false
	}
	return true
}

// handleReviewsQueue serves the per-match review queue: machine pre-annotation
// targets (phase boundaries and disputed events) plus the current review
// state, corrections, and machine output preserved for comparison. This is a
// read of persisted review + phase/episode artifacts.
func (s *Server) handleReviewsQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var cat store.Catalog
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cat); err != nil {
		writeErr(w, http.StatusInternalServerError, "catalog_unavailable")
		return
	}
	queue := []interface{}{}
	reviews := []interface{}{}
	for _, row := range cat.Matches {
		if row.Status != store.StatusVerified {
			continue
		}
		queue = append(queue, map[string]interface{}{
			"match_id": row.MatchID, "category": row.Category,
			"status": row.Status, "publication": row.Publication,
		})
		var rv *review.Review
		if s.Reviews != nil {
			r, err := s.Reviews.Load(row.MatchID)
			if err != nil {
				continue
			}
			rv = r
		}
		if rv != nil {
			reviews = append(reviews, rv)
		}
	}
	writeJSON(w, http.StatusOK, envelope{
		SchemaVersion: version.CorrectionSchema,
		Data: map[string]interface{}{
			"queue":        queue,
			"reviews":      reviews,
			"review_store": s.Reviews != nil,
			"stage":        "3",
		},
	})
}

// handleReviewsAudit serves the immutable audit log (newest first).
func (s *Server) handleReviewsAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if s.Reviews == nil {
		writeErr(w, http.StatusNotFound, "review_store_unavailable")
		return
	}
	audit, err := s.Reviews.LoadAudit()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "audit_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: audit})
}

// phaseCorrectionReq is the phase-correction mutation payload.
type phaseCorrectionReq struct {
	MatchID          string          `json:"match_id"`
	Author           string          `json:"author"`
	Reason           string          `json:"reason"`
	AlgorithmVersion string          `json:"algorithm_version"`
	EventRef         string          `json:"event_ref"`
	PreviousValue    json.RawMessage `json:"previous_value"`
	EffectiveValue   json.RawMessage `json:"effective_value"`
	EvidenceIDs      []string        `json:"evidence_ids"`
}

// handlePhaseCorrections accepts/moves/relabels a machine phase interval. The
// machine output (phases.json) is never mutated; the correction overlay
// retains the machine value alongside the effective value.
func (s *Server) handlePhaseCorrections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !s.requireSessionToken(w, r) {
		return
	}
	var req phaseCorrectionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	if req.MatchID == "" || req.Author == "" || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "match_id_author_reason_required")
		return
	}
	if len(req.PreviousValue) == 0 || len(req.EffectiveValue) == 0 {
		writeErr(w, http.StatusBadRequest, "previous_and_effective_value_required")
		return
	}
	rv, err := s.Reviews.Add(req.MatchID, review.Correction{
		MatchID: req.MatchID, Kind: review.KindPhaseInterval, Author: req.Author,
		Reason: req.Reason, AlgorithmVersion: req.AlgorithmVersion,
		PreviousValue: req.PreviousValue, EffectiveValue: req.EffectiveValue,
		EvidenceIDs: req.EvidenceIDs, EventRef: req.EventRef,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "correction_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// eventCorrectionReq is the behavior-event correction mutation payload.
type eventCorrectionReq struct {
	MatchID          string          `json:"match_id"`
	Author           string          `json:"author"`
	Reason           string          `json:"reason"`
	AlgorithmVersion string          `json:"algorithm_version"`
	EventRef         string          `json:"event_ref"`
	PreviousValue    json.RawMessage `json:"previous_value"`
	EffectiveValue   json.RawMessage `json:"effective_value"`
	EvidenceIDs      []string        `json:"evidence_ids"`
}

// handleEventCorrections accepts/rejects/edits a disputed behavior event.
func (s *Server) handleEventCorrections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !s.requireSessionToken(w, r) {
		return
	}
	var req eventCorrectionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	if req.MatchID == "" || req.Author == "" || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "match_id_author_reason_required")
		return
	}
	if len(req.PreviousValue) == 0 || len(req.EffectiveValue) == 0 {
		writeErr(w, http.StatusBadRequest, "previous_and_effective_value_required")
		return
	}
	rv, err := s.Reviews.Add(req.MatchID, review.Correction{
		MatchID: req.MatchID, Kind: review.KindEvent, Author: req.Author,
		Reason: req.Reason, AlgorithmVersion: req.AlgorithmVersion,
		PreviousValue: req.PreviousValue, EffectiveValue: req.EffectiveValue,
		EvidenceIDs: req.EvidenceIDs, EventRef: req.EventRef,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "correction_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// reviewStatusReq is the review-status transition payload.
type reviewStatusReq struct {
	MatchID string `json:"match_id"`
	Status  string `json:"status"`
	Author  string `json:"author"`
}

// handleReviewStatus transitions a match's review workflow state.
func (s *Server) handleReviewStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !s.requireSessionToken(w, r) {
		return
	}
	var req reviewStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	if req.MatchID == "" || req.Author == "" {
		writeErr(w, http.StatusBadRequest, "match_id_author_required")
		return
	}
	switch req.Status {
	case "pending", "in_progress", "reviewed":
	default:
		writeErr(w, http.StatusBadRequest, "invalid_review_status")
		return
	}
	rv, err := s.Reviews.SetReviewStatus(req.MatchID, req.Status, req.Author)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "review_status_failed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// roleOverrideReq is the role-override mutation payload. The override is
// stored in the review/correction store and surfaced as an effective manual
// override with full provenance; it never rewrites the frozen registry.
type roleOverrideReq struct {
	MatchID     string `json:"match_id"`
	AccountID   string `json:"account_id"`
	NominalRole string `json:"nominal_role"`
	Author      string `json:"author"`
	Reason      string `json:"reason"`
}

// handleRoleOverrides records a manual nominal-role override with author and
// reason. The frozen registry is untouched; the override is audited and the
// report surfaces it as effective provenance.
func (s *Server) handleRoleOverrides(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !s.requireSessionToken(w, r) {
		return
	}
	var req roleOverrideReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	if req.MatchID == "" || req.AccountID == "" || req.Author == "" || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "match_id_account_author_reason_required")
		return
	}
	valid := false
	switch req.NominalRole {
	case "1", "2", "3", "4", "5":
		valid = true
	}
	if !valid {
		writeErr(w, http.StatusBadRequest, "nominal_role_must_be_1_5")
		return
	}
	prev := map[string]interface{}{"account_id": req.AccountID, "nominal_role": ""}
	if s.RoleReg != nil {
		if eff, ok := s.RoleReg.Effective(req.MatchID, req.AccountID, s.Overrides); ok {
			prev["nominal_role"] = eff.NominalRole
		}
	}
	prevB, _ := json.Marshal(prev)
	effB, _ := json.Marshal(map[string]interface{}{
		"match_id": req.MatchID, "account_id": req.AccountID,
		"nominal_role": req.NominalRole, "source_kind": "manual_override",
		"author": req.Author, "reason": req.Reason,
	})
	rv, err := s.Reviews.Add(req.MatchID, review.Correction{
		MatchID: req.MatchID, Kind: review.KindRoleOverride, Author: req.Author,
		Reason: req.Reason, PreviousValue: prevB, EffectiveValue: effB,
		EventRef: "role:" + req.AccountID,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "override_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// handleScoreCorpus serves the corpus-level scoring snapshot if present.
func (s *Server) handleScoreCorpus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var cs struct {
		SchemaVersion string                 `json:"schema_version"`
		RuleVersion   string                 `json:"rule_version"`
		Matches       map[string]interface{} `json:"matches"`
	}
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cs); err != nil && !strings.Contains(err.Error(), "no such file") {
		// corpus scores live in a separate catalog file, not catalog.json.
	}
	_ = cs
	var csFile struct {
		SchemaVersion string `json:"schema_version"`
		RuleVersion   string `json:"rule_version"`
		CorpusMatches int    `json:"corpus_matches"`
	}
	if err := s.Store.ReadJSONFile(s.Store.Root+"/scores-corpus.json", &csFile); err != nil {
		writeErr(w, http.StatusNotFound, "scores_corpus_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ScoreSchema, Data: csFile})
}

// matchScoresFor returns the persisted scoring snapshot for a match, or nil.
func (s *Server) matchScoresFor(matchID string) *scoring.MatchScores {
	var ms scoring.MatchScores
	if err := s.Store.ReadJSON(matchID, store.ArtifactScores, &ms); err != nil {
		return nil
	}
	return &ms
}

// SortScores orders players by account id for deterministic output.
func SortScores(ms *scoring.MatchScores) {
	if ms == nil {
		return
	}
	sort.Slice(ms.Players, func(i, j int) bool {
		return ms.Players[i].AccountID < ms.Players[j].AccountID
	})
}

var _ = fmt.Sprintf
