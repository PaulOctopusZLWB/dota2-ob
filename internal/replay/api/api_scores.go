package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/report"
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
// (accept/move/relabel/add/delete/split/merge) cumulatively against the
// current persisted effective phase stream. The machine output (phases.json)
// is never mutated; it is only the fallback base for a review with no
// effective overlay. Every event_ref resolves against that current stream, not
// against phases.json after the first correction. The v2 correction and the
// resulting effective stream are persisted together under one serialized
// store operation before a 2xx is returned; a stale/tampered current-state
// precondition fails closed with 409, and an invalid operation leaves both the
// persisted state and the audit unchanged.
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
	// Resolve the authoritative machine phase context (eligible_seconds, rule
	// version, replay SHA, immutable machine intervals) from phases.json.
	var ph phase.Output
	if err := s.Store.ReadJSON(req.MatchID, store.ArtifactPhases, &ph); err != nil {
		writeErr(w, http.StatusBadRequest, "machine_phases_unavailable")
		return
	}
	machine := make([]review.PhaseInterval, 0, len(ph.Intervals))
	for _, iv := range ph.Intervals {
		machine = append(machine, review.PhaseInterval{
			StartGameSecond: iv.StartGameSecond, EndGameSecond: iv.EndGameSecond,
			GlobalPhase: string(iv.GlobalPhase), RoundIndex: iv.RoundIndex,
		})
	}
	ctx := review.PhaseContext{
		EligibleSeconds:  ph.EligibleSeconds,
		RuleVersion:      ph.RuleVersion,
		ReplaySHA256:     s.replaySHA(req.MatchID),
		MachineIntervals: machine,
	}
	// One serialized store operation persists the v2 correction + effective
	// stream together. Errors change neither persisted state nor the audit.
	rv, err := s.Reviews.ApplyPhaseOp(req.MatchID, ctx, req)
	if err != nil {
		if review.IsStale(err) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if review.IsStorageError(err) {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
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
	MatchID          string `json:"match_id"`
	Status           string `json:"status"`
	Author           string `json:"author"`
	ExpectedRevision string `json:"expected_revision"`
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
	rv, err := s.Reviews.SetReviewStatus(req.MatchID, req.Status, req.Author, req.ExpectedRevision)
	if err != nil {
		if review.IsStale(err) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
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
//
// The mutation is transactional: the override file, the affected report, the
// score corpus, the correction record, and the audit are persisted together
// under staged atomic writes, and any failure restores every touched file to
// its pre-mutation bytes so no visible mixed successful state can remain.
// Reads and the correction previous_value always resolve from the persisted
// effective override store (never a stale serve-time snapshot).
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
	// Resolve the current effective role from the PERSISTED effective override
	// store (never the serve-time snapshot) so a later override after restart
	// continues from the persisted effective value, and the correction's
	// previous_value records that effective role.
	if s.RoleReg == nil {
		writeErr(w, http.StatusInternalServerError, "role_registry_unavailable")
		return
	}
	eff, ok := s.RoleReg.Effective(req.MatchID, req.AccountID, s.effectiveOverrides())
	if !ok {
		writeErr(w, http.StatusBadRequest, "account_not_in_role_registry:"+req.AccountID)
		return
	}
	prevRole := eff.NominalRole
	sourceRole := eff.SourceNominalRole

	// A role override is a score-recomputation transaction, not a migration
	// escape hatch. Refuse to mutate when the current authoritative corpus is
	// current-tagged but internally corrupt; rollback must preserve those exact
	// bytes for explicit repair/re-score rather than silently blessing them.
	var currentScores scoring.CorpusScores
	if err := s.Store.ReadJSONFile(filepath.Join(s.Store.Root, "scores-corpus.json"), &currentScores); err == nil {
		if err := scoring.ValidateCorpusScores(&currentScores, s.MetricReg); err != nil {
			writeErr(w, http.StatusInternalServerError, "score_corpus_invalid_before_override:"+err.Error())
			return
		}
	} else if !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, "score_corpus_unreadable_before_override")
		return
	}

	// Snapshot every authoritative file this mutation may touch so a failure
	// can roll back to the exact pre-mutation bytes.
	paths := s.roleMutationPaths(req.MatchID)
	snaps, err := snapshotFiles(paths)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "override_snapshot_failed")
		return
	}
	rollback := func() {
		_ = restoreSnapshots(snaps)
	}

	// Persist to the authoritative override file under the data root. The
	// latest override for a (match, account) pair REPLACES any earlier one so
	// a new review decision always takes effect (roles.Effective applies the
	// first matching override).
	override := roles.Override{
		MatchID: req.MatchID, AccountID: req.AccountID, NominalRole: req.NominalRole,
		Reason: req.Reason, AppliedAt: time.Now().UTC().Format(time.RFC3339),
		Author: req.Author,
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
		rollback()
		writeErr(w, http.StatusInternalServerError, "override_persist_failed")
		return
	}
	if s.injectFail == "report" {
		rollback()
		writeErr(w, http.StatusInternalServerError, "injected_report_failure")
		return
	}
	// Rebuild + persist the authoritative affected report with the current
	// effective roles (gate evaluated on immutable source roles). Preserve
	// report/status as the gate determines; a failure restores everything.
	if err := s.rebuildAndPersistReport(req.MatchID); err != nil {
		rollback()
		writeErr(w, http.StatusInternalServerError, "override_report_rebuild_failed")
		return
	}
	if s.injectFail == "score" {
		rollback()
		writeErr(w, http.StatusInternalServerError, "injected_score_failure")
		return
	}
	// Synchronously recompute the corpus scores so match/tournament APIs
	// immediately reflect the effective role, and so same-role cohorts and
	// percentiles are recomputed from the effective roles.
	if s.ScoringContract != nil {
		cs, err := scoring.ComputeAndPersist(s.Store, s.ScoringContract, s.TeamContract, s.MetricReg, s.RoleReg, s.effectiveOverrides())
		if err != nil {
			rollback()
			writeErr(w, http.StatusInternalServerError, "override_recompute_failed")
			return
		}
		_ = cs
	}
	if s.injectFail == "correction" {
		rollback()
		writeErr(w, http.StatusInternalServerError, "injected_correction_failure")
		return
	}
	prevB, _ := json.Marshal(map[string]interface{}{
		"account_id": req.AccountID, "nominal_role": prevRole, "source_nominal_role": sourceRole,
	})
	effB, _ := json.Marshal(map[string]interface{}{
		"match_id": req.MatchID, "account_id": req.AccountID,
		"nominal_role": req.NominalRole, "source_nominal_role": sourceRole,
		"source_kind": "manual_override",
		"author":      req.Author, "reason": req.Reason, "applied_at": override.AppliedAt,
		"schema_version": version.RoleSchema,
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
		rollback()
		writeErr(w, http.StatusInternalServerError, "override_correction_failed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{SchemaVersion: version.CorrectionSchema, Data: rv})
}

// roleMutationPaths returns every authoritative file a role override may touch.
func (s *Server) roleMutationPaths(matchID string) []string {
	root := s.Store.Root
	paths := []string{
		filepath.Join(root, "role-overrides-effective.json"),
		s.Store.ArtifactPath(matchID, store.ArtifactReport),
		s.Store.ArtifactPath(matchID, store.ArtifactStatus),
		s.Store.ArtifactPath(matchID, store.ArtifactScores),
		filepath.Join(root, "scores-corpus.json"),
	}
	if s.Reviews != nil {
		paths = append(paths, s.Reviews.Path(matchID), s.Reviews.AuditPath())
	}
	return paths
}

// rebuildAndPersistReport rebuilds the report with the current effective roles
// and atomically persists report.json (and status.json when the gate outcome
// changes) for the affected match.
func (s *Server) rebuildAndPersistReport(matchID string) error {
	rep, err := report.Build(s.Store, matchID, s.RoleReg, s.effectiveOverrides())
	if err != nil {
		return err
	}
	rep.SortParticipants()
	if err := s.Store.WriteJSON(matchID, store.ArtifactReport, rep); err != nil {
		return err
	}
	// Keep status.json authoritative and in agreement with the rebuilt report.
	var prev store.StatusRecord
	havePrev := s.Store.ReadJSON(matchID, store.ArtifactStatus, &prev) == nil
	if !havePrev || prev.Status != rep.Status || prev.Publication != rep.Publication || prev.Reason != rep.Reason {
		sr := &store.StatusRecord{
			SchemaVersion: store.StatusSchema,
			MatchID:       matchID,
			Status:        rep.Status,
			Publication:   rep.Publication,
			Reason:        rep.Reason,
		}
		if havePrev {
			sr.ArchiveState = prev.ArchiveState
			sr.IdentityState = prev.IdentityState
			sr.ClockState = prev.ClockState
			sr.InputHash = prev.InputHash
			sr.ParseState = prev.ParseState
			sr.DurationSec = prev.DurationSec
			sr.GameStartUnix = prev.GameStartUnix
		}
		if err := s.Store.WriteStatus(matchID, sr); err != nil {
			return err
		}
	}
	return nil
}

// fileSnapshot is the pre-mutation bytes of one authoritative file.
type fileSnapshot struct {
	path    string
	data    []byte
	existed bool
}

// snapshotFiles reads the bytes of a set of paths (missing -> nil, existed=false).
func snapshotFiles(paths []string) ([]fileSnapshot, error) {
	snaps := make([]fileSnapshot, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				snaps = append(snaps, fileSnapshot{path: p})
				continue
			}
			return nil, err
		}
		snaps = append(snaps, fileSnapshot{path: p, data: b, existed: true})
	}
	return snaps, nil
}

// restoreSnapshots restores each snapshot path to its pre-mutation bytes.
func restoreSnapshots(snaps []fileSnapshot) error {
	for _, sn := range snaps {
		if !sn.existed {
			_ = os.Remove(sn.path)
			continue
		}
		if err := store.WriteAtomic(sn.path, sn.data); err != nil {
			return err
		}
	}
	return nil
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
	if ms.SchemaVersion != version.ScoreSchema || ms.RuleVersion != version.ScoreRuleVersion {
		return nil
	}
	if err := scoring.ValidateMatchScores(&ms, s.MetricReg); err != nil {
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
