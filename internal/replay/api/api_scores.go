package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/review"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
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

// machinePhaseTruth resolves the authoritative machine value for a phase
// interval correction: the machine interval (from phases.json) referenced by
// event_ref (canonical `interval@START-END` or `START-END`), the replay
// content hash, and the machine phase rule version. It never trusts the
// caller-supplied previous value.
func (s *Server) machinePhaseTruth(matchID, eventRef string) (review.MachineTruth, error) {
	var ph phase.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactPhases, &ph); err != nil {
		return review.MachineTruth{}, fmt.Errorf("machine_phases_unavailable: %w", err)
	}
	st, _, ok := review.ParseEventRef(eventRef)
	if !ok {
		return review.MachineTruth{}, fmt.Errorf("invalid_event_ref:%s", eventRef)
	}
	var machine json.RawMessage
	for _, iv := range ph.Intervals {
		if iv.StartGameSecond == st {
			ref := review.CanonicalEventRef(review.PhaseInterval{StartGameSecond: iv.StartGameSecond, EndGameSecond: iv.EndGameSecond, GlobalPhase: string(iv.GlobalPhase), RoundIndex: iv.RoundIndex})
			b, _ := json.Marshal(map[string]interface{}{
				"start_game_second": iv.StartGameSecond, "end_game_second": iv.EndGameSecond,
				"global_phase": iv.GlobalPhase, "round_index": iv.RoundIndex,
				"rule_version": iv.RuleVersion, "event_ref": ref,
			})
			machine = b
			break
		}
	}
	if len(machine) == 0 {
		return review.MachineTruth{}, fmt.Errorf("machine_interval_not_found: %s", eventRef)
	}
	return review.MachineTruth{
		ReplaySHA256:     s.replaySHA(matchID),
		AlgorithmVersion: ph.RuleVersion,
		MachineValue:     machine,
	}, nil
}

// machineEventTruth resolves the authoritative machine value for a behavior
// event correction from the episodes artifact.
func (s *Server) machineEventTruth(matchID, eventRef string) (review.MachineTruth, error) {
	var ep episodes.Output
	if err := s.Store.ReadJSON(matchID, store.ArtifactEpisodes, &ep); err != nil {
		return review.MachineTruth{}, fmt.Errorf("machine_episodes_unavailable: %w", err)
	}
	var machine json.RawMessage
	for _, e := range ep.Episodes {
		if e.ID == eventRef || fmt.Sprintf("%s:%d", e.Kind, int64(e.StartGameSecond)) == eventRef {
			b, _ := json.Marshal(map[string]interface{}{
				"id": e.ID, "kind": e.Kind, "start_game_second": e.StartGameSecond,
				"end_game_second": e.EndGameSecond, "participants": e.Participants,
				"detail": e.Detail, "rule_version": e.RuleVersion,
			})
			machine = b
			break
		}
	}
	if len(machine) == 0 {
		return review.MachineTruth{}, fmt.Errorf("machine_event_not_found: %s", eventRef)
	}
	return review.MachineTruth{
		ReplaySHA256:     s.replaySHA(matchID),
		AlgorithmVersion: ep.RuleVersion,
		MachineValue:     machine,
	}, nil
}

// replaySHA resolves the immutable replay content hash for a match.
func (s *Server) replaySHA(matchID string) string {
	var ver archive.Verification
	if err := s.Store.ReadJSON(matchID, store.ArtifactVerification, &ver); err == nil && ver.DemoSHA256Actual != "" {
		return ver.DemoSHA256Actual
	}
	return ""
}

// handlePhaseCorrections applies a typed phase-review operation
// (accept/move/relabel/add/delete/split/merge). The machine output
// (phases.json) is never mutated; the machine value, replay SHA, and rule
// version are resolved server-side, so a fabricated previous value is
// rejected. The effective stream is recomputed, validated, and durably
// persisted before a 2xx is returned.
func (s *Server) handlePhaseCorrections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !s.requireSessionToken(w, r) {
		return
	}
	var req review.PhaseOpReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	if req.MatchID == "" || req.Author == "" || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "match_id_author_reason_required")
		return
	}
	if !s.validMatchID(req.MatchID) {
		writeErr(w, http.StatusBadRequest, "match_id_not_in_catalog")
		return
	}
	// Resolve machine intervals (the immutable source for the overlay).
	var ph phase.Output
	if err := s.Store.ReadJSON(req.MatchID, store.ArtifactPhases, &ph); err != nil {
		writeErr(w, http.StatusBadRequest, "machine_phases_unavailable")
		return
	}
	machine := []review.PhaseInterval{}
	for _, iv := range ph.Intervals {
		machine = append(machine, review.PhaseInterval{
			StartGameSecond: iv.StartGameSecond, EndGameSecond: iv.EndGameSecond,
			GlobalPhase: string(iv.GlobalPhase), RoundIndex: iv.RoundIndex,
		})
	}
	ov := &review.PhaseOverlay{Machine: machine}
	req.EligibleSeconds = ph.EligibleSeconds
	effective, err := ov.Apply(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Resolve the machine value for the correction record (authoritative).
	var truth review.MachineTruth
	if req.Op == review.OpAdd {
		truth = review.MachineTruth{
			ReplaySHA256:     s.replaySHA(req.MatchID),
			AlgorithmVersion: ph.RuleVersion,
			MachineValue:     json.RawMessage(`{}`),
		}
	} else {
		truth, err = s.machinePhaseTruth(req.MatchID, req.EventRef)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	effJSON, _ := json.Marshal(req.Effective)
	if len(effJSON) == 0 {
		effJSON = json.RawMessage(`{}`)
	}
	rv, err := s.Reviews.AddAuthoritative(req.MatchID, review.Correction{
		MatchID: req.MatchID, Kind: review.KindPhaseInterval, Author: req.Author,
		Reason: req.Reason, EffectiveValue: effJSON, EventRef: req.EventRef,
	}, truth)
	if err != nil {
		if strings.Contains(err.Error(), "tamper") {
			writeErr(w, http.StatusConflict, "previous_value_mismatch_machine_truth")
			return
		}
		writeErr(w, http.StatusInternalServerError, "correction_persist_failed")
		return
	}
	// Durably persist the validated effective overlay; propagate errors.
	effRaw := review.ToJSON(effective)
	if _, err := s.Reviews.SetEffectivePhases(req.MatchID, effRaw, req.Author); err != nil {
		writeErr(w, http.StatusInternalServerError, "effective_overlay_persist_failed")
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

// handleEventCorrections accepts/rejects/edits a disputed behavior event with
// server-side machine truth.
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
	if !s.validMatchID(req.MatchID) {
		writeErr(w, http.StatusBadRequest, "match_id_not_in_catalog")
		return
	}
	if req.EventRef == "" || len(req.EffectiveValue) == 0 {
		writeErr(w, http.StatusBadRequest, "event_ref_and_effective_value_required")
		return
	}
	truth, err := s.machineEventTruth(req.MatchID, req.EventRef)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rv, err := s.Reviews.AddAuthoritative(req.MatchID, review.Correction{
		MatchID: req.MatchID, Kind: review.KindEvent, Author: req.Author,
		Reason: req.Reason, PreviousValue: req.PreviousValue, EffectiveValue: req.EffectiveValue,
		EvidenceIDs: req.EvidenceIDs, EventRef: req.EventRef,
	}, truth)
	if err != nil {
		if strings.Contains(err.Error(), "tamper") {
			writeErr(w, http.StatusConflict, "previous_value_mismatch_machine_truth")
			return
		}
		writeErr(w, http.StatusInternalServerError, "correction_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// validMatchID rejects match ids not present in the catalog (path-traversal
// and out-of-root writes).
func (s *Server) validMatchID(matchID string) bool {
	if matchID == "" || strings.ContainsAny(matchID, "/\\..") {
		return false
	}
	var cat store.Catalog
	if err := s.Store.ReadJSONFile(s.Store.CatalogPath(), &cat); err != nil {
		return false
	}
	for _, row := range cat.Matches {
		if row.MatchID == matchID {
			return true
		}
	}
	return false
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
// reason. The frozen registry is untouched; the override is written to the
// authoritative data-root override store (consumed by report/scoring) and
// audited, so the effective role actually takes effect.
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
	if !s.validMatchID(req.MatchID) {
		writeErr(w, http.StatusBadRequest, "match_id_not_in_catalog")
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
	prevRole := ""
	if s.RoleReg != nil {
		if eff, ok := s.RoleReg.Effective(req.MatchID, req.AccountID, s.Overrides); ok {
			prevRole = eff.NominalRole
		}
	}
	// Persist to the authoritative override file under the data root. The
	// latest override for a (match, account) pair REPLACES any earlier one so
	// a new review decision always takes effect (roles.Effective applies the
	// first matching override).
	override := roles.Override{
		MatchID: req.MatchID, AccountID: req.AccountID, NominalRole: req.NominalRole,
		Reason: req.Reason, AppliedAt: time.Now().UTC().Format(time.RFC3339),
	}
	of := s.loadOverrideFile()
	kept := make([]roles.Override, 0, len(of.Overrides)+1)
	for _, o := range of.Overrides {
		if o.MatchID == req.MatchID && o.AccountID == req.AccountID {
			continue // replaced by the new override below
		}
		kept = append(kept, o)
	}
	kept = append(kept, override)
	of.Overrides = kept
	of.SchemaVersion = version.RoleSchema
	if err := s.Store.WriteRootJSON("role-overrides-effective.json", of); err != nil {
		writeErr(w, http.StatusInternalServerError, "override_persist_failed")
		return
	}
	prevB, _ := json.Marshal(map[string]interface{}{"account_id": req.AccountID, "nominal_role": prevRole})
	effB, _ := json.Marshal(map[string]interface{}{
		"match_id": req.MatchID, "account_id": req.AccountID,
		"nominal_role": req.NominalRole, "source_kind": "manual_override",
		"author": req.Author, "reason": req.Reason, "applied_at": override.AppliedAt,
	})
	rv, err := s.Reviews.AddAuthoritative(req.MatchID, review.Correction{
		MatchID: req.MatchID, Kind: review.KindRoleOverride, Author: req.Author,
		Reason: req.Reason, PreviousValue: prevB, EffectiveValue: effB,
		EventRef: "role:" + req.AccountID,
	}, review.MachineTruth{
		ReplaySHA256:     s.replaySHA(req.MatchID),
		AlgorithmVersion: version.RoleSchema,
		MachineValue:     prevB,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "override_correction_failed")
		return
	}
	// Synchronously recompute the corpus scores so match/tournament APIs
	// immediately reflect the effective role. A recompute failure is returned
	// as an error; success is only reported after durable recomputation.
	if s.ScoringContract != nil {
		cs, err := scoring.ComputeAndPersist(s.Store, s.ScoringContract, s.TeamContract, s.MetricReg, s.RoleReg, s.effectiveOverrides())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "override_recompute_failed")
			return
		}
		rv.RecomputeVersion = cs.ContractVersion
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// loadOverrideFile loads the effective override store (data root), falling
// back to the in-memory overrides supplied at serve time.
func (s *Server) loadOverrideFile() *roles.OverrideFile {
	var of roles.OverrideFile
	if err := s.Store.ReadJSONFile(s.Store.Root+"/role-overrides-effective.json", &of); err == nil {
		return &of
	}
	if s.Overrides != nil {
		return s.Overrides
	}
	return &roles.OverrideFile{SchemaVersion: version.RoleSchema, Overrides: []roles.Override{}}
}

// handleScoreCorpus serves the corpus-level scoring catalog (player
// tournament, team tournament, and per-match rows) if present.
func (s *Server) handleScoreCorpus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	cs := s.corpusScoresFor()
	if cs == nil {
		writeErr(w, http.StatusNotFound, "scores_corpus_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.ScoreSchema, Data: cs})
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
