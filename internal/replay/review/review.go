// Package review implements the gold-review workflow: a per-match correction
// overlay store and an immutable audit log. Machine output is never mutated;
// every human correction retains the machine value, the effective value, the
// author, reason, timestamp, evidence, and the algorithm version that produced
// the machine value. Machine truth is resolved server-side from persisted
// artifacts — a caller-supplied previous value that disagrees with the machine
// value is rejected (tamper detection). Read-only pages can never mutate
// analytical state; review mutations bind to the loopback server and require a
// local session token.
package review

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Kind is the correction target kind.
type Kind string

const (
	// KindPhaseInterval corrects an official phase boundary.
	KindPhaseInterval Kind = "phase_interval"
	// KindEvent corrects a disputed behavior event.
	KindEvent Kind = "behavior_event"
	// KindRoleOverride corrects a nominal role (source-backed manual override).
	KindRoleOverride Kind = "role_override"
)

// MachineTruth is the authoritative machine-side facts for one correction:
// the immutable machine value, the replay content identity, and the machine
// algorithm/rule version that produced it. These are resolved server-side and
// never accepted from the mutation payload.
type MachineTruth struct {
	ReplaySHA256     string          `json:"replay_sha256,omitempty"`
	AlgorithmVersion string          `json:"algorithm_version"`
	MachineValue     json.RawMessage `json:"machine_value"`
}

// Correction is one human review mutation for non-phase kinds (behavior
// events, role overrides). The machine output remains immutable and
// inspectable; this record retains the full before/after pair. Phase
// corrections use PhaseCorrectionV2, which additionally carries the complete
// operation parameters and the complete current/resulting phase streams.
type Correction struct {
	ID               string `json:"id"`
	SchemaVersion    string `json:"schema_version"`
	RuleVersion      string `json:"rule_version"`
	MatchID          string `json:"match_id"`
	ReplaySHA256     string `json:"replay_sha256,omitempty"`
	Kind             Kind   `json:"kind"`
	Author           string `json:"author"`
	Reason           string `json:"reason"`
	AppliedAt        string `json:"applied_at"`
	AlgorithmVersion string `json:"algorithm_version"`
	// PreviousValue is the authoritative machine value (resolved server-side).
	PreviousValue json.RawMessage `json:"previous_value"`
	// EffectiveValue is the corrected value.
	EffectiveValue json.RawMessage `json:"effective_value"`
	EvidenceIDs    []string        `json:"evidence_ids,omitempty"`
	// EventRef points to the corrected phase interval / episode / metric row.
	EventRef string `json:"event_ref,omitempty"`
}

// PhaseCorrectionV2 is one v2 phase-review mutation. It retains the
// operation, every operation-specific parameter, the complete current
// before-stream, the complete resulting after-stream, the target refs, and
// full provenance (author, reason, applied time, evidence, replay SHA,
// machine rule/algorithm version, and the immutable machine value/source).
// The machine stream is never mutated; it is preserved verbatim as the
// MachineValue source of truth.
type PhaseCorrectionV2 struct {
	ID               string   `json:"id"`
	SchemaVersion    string   `json:"schema_version"`
	RuleVersion      string   `json:"rule_version"`
	MatchID          string   `json:"match_id"`
	ReplaySHA256     string   `json:"replay_sha256,omitempty"`
	Kind             Kind     `json:"kind"`
	Operation        string   `json:"operation"`
	ShapeVersion     string   `json:"shape_version"`
	Author           string   `json:"author"`
	Reason           string   `json:"reason"`
	AppliedAt        string   `json:"applied_at"`
	EventRef         string   `json:"event_ref"`
	SplitSecond      *int     `json:"split_second,omitempty"`
	MergeRight       string   `json:"merge_right,omitempty"`
	AbsorbInto       string   `json:"absorb_into,omitempty"`
	EvidenceIDs      []string `json:"evidence_ids,omitempty"`
	AlgorithmVersion string   `json:"algorithm_version"`
	// BeforeStream is the complete current effective stream before the op.
	BeforeStream []PhaseInterval `json:"before_stream"`
	// AfterStream is the complete resulting effective stream after the op.
	AfterStream []PhaseInterval `json:"after_stream"`
	// MachineValue is the immutable machine value/source for the corrected
	// region (the full machine phase stream), resolved server-side.
	MachineValue json.RawMessage `json:"machine_value"`
	// MachineRuleVersion is the machine rule/algorithm version that produced
	// the machine value.
	MachineRuleVersion string `json:"machine_rule_version"`
	// EffectiveValue is a meaningful operation delta (never null-only); the
	// complete after-state lives in AfterStream.
	EffectiveValue json.RawMessage `json:"effective_value"`
}

// Review is the per-match review document: the machine overlay target plus all
// corrections and the effective (corrected) overlay stream.
type Review struct {
	SchemaVersion string       `json:"schema_version"`
	RuleVersion   string       `json:"rule_version"`
	MatchID       string       `json:"match_id"`
	ReplaySHA256  string       `json:"replay_sha256,omitempty"`
	Corrections   []Correction `json:"corrections"`
	// PhaseCorrections is the ordered v2 audit of phase operations. v1
	// documents are migrated deterministically into this field on load.
	PhaseCorrections []PhaseCorrectionV2 `json:"phase_corrections,omitempty"`
	// EffectivePhaseIntervals is the phase stream with accepted corrections
	// applied (machine output preserved separately in phases.json). When
	// present and non-empty it is the authoritative base for the next
	// operation.
	EffectivePhaseIntervals []json.RawMessage `json:"effective_phase_intervals,omitempty"`
	// ReviewRevision is an opaque optimistic-concurrency token derived from
	// the ordered correction state plus the canonical effective stream. It
	// changes after EVERY successful correction, including accept operations
	// that do not alter interval boundaries. Every phase mutation must carry
	// the currently rendered revision; a mismatch is rejected atomically with
	// 409 and changes no bytes or counts.
	ReviewRevision string `json:"review_revision,omitempty"`
	// ReviewStatus is the review workflow state.
	ReviewStatus string `json:"review_status"` // pending|in_progress|reviewed
	// RecomputeVersion is the scoring contract version that a synchronous
	// recompute ran under (set by the API after role-override recompute).
	RecomputeVersion string `json:"recompute_version,omitempty"`
}

// Audit is the append-only audit log for one review store.
type Audit struct {
	SchemaVersion string       `json:"schema_version"`
	Entries       []AuditEntry `json:"entries"`
}

// AuditEntry is one immutable audit record.
type AuditEntry struct {
	Seq          int64     `json:"seq"`
	AppliedAt    time.Time `json:"applied_at"`
	Author       string    `json:"author"`
	Action       string    `json:"action"`
	MatchID      string    `json:"match_id"`
	CorrectionID string    `json:"correction_id,omitempty"`
	Summary      string    `json:"summary"`
}

// Store persists per-match review documents and the shared audit log under a
// store root. Reads and writes are serialized by a mutex so loopback review
// mutations never interleave partial state.
type Store struct {
	Root string
	mu   sync.Mutex
}

// New creates a review store rooted at root.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("review: empty root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("review: mkdir: %w", err)
	}
	return &Store{Root: root}, nil
}

// Path returns the review document path for a match. The match id is
// validated so no path can escape the data root (defense in depth beyond the
// API's catalog gate).
func (s *Store) Path(matchID string) string {
	if !ValidMatchID(matchID) {
		return filepath.Join(s.Root, "matches", "invalid", store.ArtifactCorrections)
	}
	return filepath.Join(s.Root, "matches", matchID, store.ArtifactCorrections)
}

// ValidMatchID rejects match ids that could traverse or escape the matches
// directory (path separators, dot segments, control chars).
func ValidMatchID(matchID string) bool {
	if matchID == "" {
		return false
	}
	for _, r := range matchID {
		if r == '/' || r == '\\' || r == '.' || r == '\x00' || r < 0x20 {
			return false
		}
	}
	return true
}

// AuditPath returns the shared audit log path.
func (s *Store) AuditPath() string {
	return filepath.Join(s.Root, "review-audit.json")
}

// Load reads the review document for a match (missing → empty pending).
func (s *Store) Load(matchID string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked(matchID)
}

// LoadByStore loads a review document via a replay store path (same layout).
func LoadByStore(st *store.Store, matchID string) (*Review, error) {
	s, err := New(st.Root)
	if err != nil {
		return nil, err
	}
	return s.Load(matchID)
}

// AddAuthoritative appends one correction whose machine truth (previous value,
// replay SHA, algorithm version) was resolved server-side. If the caller
// supplied a previous value that disagrees with the authoritative machine
// value, the correction is rejected as tampered. Machine output is preserved
// by construction.
func (s *Store) AddAuthoritative(matchID string, c Correction, truth MachineTruth) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	// Tamper detection: caller-supplied previous value must match machine.
	if len(c.PreviousValue) > 0 && len(truth.MachineValue) > 0 {
		if !jsonEqual(c.PreviousValue, truth.MachineValue) {
			return nil, fmt.Errorf("review: previous value disagrees with machine truth (tamper)")
		}
	}
	// Authoritative fields always come from server-side truth.
	c.PreviousValue = append(json.RawMessage(nil), truth.MachineValue...)
	c.ReplaySHA256 = truth.ReplaySHA256
	c.AlgorithmVersion = truth.AlgorithmVersion
	if c.AlgorithmVersion == "" {
		c.AlgorithmVersion = version.PhaseRuleVersion
	}
	seq := int64(len(r.Corrections) + 1)
	c.ID = fmt.Sprintf("corr-%s-%04d", matchID, seq)
	c.SchemaVersion = version.CorrectionSchema
	c.RuleVersion = version.CorrectionRuleVersion
	if c.AppliedAt == "" {
		c.AppliedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.Corrections = append(r.Corrections, c)
	if r.ReplaySHA256 == "" {
		r.ReplaySHA256 = truth.ReplaySHA256
	}
	if r.ReviewStatus == "" || r.ReviewStatus == "pending" {
		r.ReviewStatus = "in_progress"
	}
	if err := s.writeJSON(s.Path(matchID), r); err != nil {
		return nil, err
	}
	if err := s.appendAudit(matchID, AuditEntry{
		Author: c.Author, Action: "correction_added", MatchID: matchID,
		CorrectionID: c.ID, Summary: fmt.Sprintf("%s %s", c.Kind, c.EventRef),
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// PhaseContext is the authoritative machine phase context for a match,
// resolved server-side from phases.json (never from the mutation payload).
type PhaseContext struct {
	// EligibleSeconds is the authoritative match-end bound.
	EligibleSeconds int
	// RuleVersion is the machine phase rule/algorithm version.
	RuleVersion string
	// ReplaySHA256 is the immutable replay content hash.
	ReplaySHA256 string
	// MachineIntervals is the immutable machine phase stream.
	MachineIntervals []PhaseInterval
}

// ApplyPhaseOp atomically applies one typed phase operation under a single
// serialized store operation: it loads the current review (migrating a v1
// document deterministically), resolves the current effective stream
// (persisted overlay when present, otherwise the immutable machine stream),
// applies and validates the operation against the authoritative
// eligible_seconds, and persists the v2 correction plus the resulting
// effective stream together in one atomic write before appending one audit
// entry. A stale/tampered current-state precondition fails closed (the caller
// maps IsStale to 409); an invalid operation changes neither the persisted
// state nor the audit. It never returns 2xx for a correction-only or
// overlay-only intermediate state.
func (s *Store) ApplyPhaseOp(matchID string, ctx PhaseContext, req PhaseOpReq) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	// Optimistic concurrency: the caller must submit the currently rendered
	// revision. A mismatch (stale form, including a same-boundary edit whose
	// intervals still exist) fails closed with the stale sentinel and changes
	// no bytes or counts. This is checked under the same mutation lock as the
	// write so two concurrent reviewers cannot silently overwrite each other.
	if req.ExpectedRevision == "" {
		return nil, staleErrorf("review_revision_required:current=%s", r.ReviewRevision)
	}
	if req.ExpectedRevision != r.ReviewRevision {
		return nil, staleErrorf("review_revision_mismatch:expected=%s current=%s", req.ExpectedRevision, r.ReviewRevision)
	}
	// Resolve the current stream: effective overlay when present, else machine.
	var base []PhaseInterval
	if len(r.EffectivePhaseIntervals) > 0 {
		base, err = FromJSON(r.EffectivePhaseIntervals)
		if err != nil {
			return nil, err
		}
	} else {
		base = append([]PhaseInterval(nil), ctx.MachineIntervals...)
	}
	req.EligibleSeconds = ctx.EligibleSeconds
	ov := &PhaseOverlay{Base: base}
	after, err := ov.Apply(req)
	if err != nil {
		return nil, err
	}
	// Build the v2 correction with complete before/after streams and the
	// operation parameters.
	pc, err := buildPhaseCorrection(matchID, r, ctx, req, base, after)
	if err != nil {
		return nil, err
	}
	r.PhaseCorrections = append(r.PhaseCorrections, pc)
	r.EffectivePhaseIntervals = ToJSON(after)
	if r.ReplaySHA256 == "" {
		r.ReplaySHA256 = ctx.ReplaySHA256
	}
	if r.ReviewStatus == "" || r.ReviewStatus == "pending" {
		r.ReviewStatus = "in_progress"
	}
	// Advance the revision from the ordered corrections + canonical effective
	// stream AFTER this correction, so every success (including accept)
	// yields a new revision for the next mutation.
	r.ReviewRevision = revisionOf(r.PhaseCorrections, r.EffectivePhaseIntervals)
	// One serialized write: correction + effective stream + revision together.
	if err := s.writeJSON(s.Path(matchID), r); err != nil {
		return nil, err
	}
	if err := s.appendAudit(matchID, AuditEntry{
		Author: req.Author, Action: "phase_correction_v2", MatchID: matchID,
		CorrectionID: pc.ID, Summary: fmt.Sprintf("%s %s", req.Op, req.EventRef),
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// revisionOf derives an opaque optimistic-concurrency token from the ordered
// phase-correction state plus the canonical effective stream. It changes on
// every correction (including accept, which appends a correction) and after
// restart reproduces the same value deterministically from the persisted
// ordered corrections and effective stream.
func revisionOf(corrections []PhaseCorrectionV2, effective []json.RawMessage) string {
	h := sha256.New()
	for i := range corrections {
		c := &corrections[i]
		h.Write([]byte(c.ID))
		h.Write([]byte{0})
		h.Write([]byte(c.Operation))
		h.Write([]byte{0})
		if b, err := json.Marshal(c.BeforeStream); err == nil {
			h.Write(b)
		}
		h.Write([]byte{0})
		if b, err := json.Marshal(c.AfterStream); err == nil {
			h.Write(b)
		}
		h.Write([]byte{0})
	}
	for _, iv := range effective {
		h.Write(iv)
		h.Write([]byte{0})
	}
	return fmt.Sprintf("rev-%x", h.Sum(nil)[:16])
}

// buildPhaseCorrection constructs the v2 audit record for one phase operation.
func buildPhaseCorrection(matchID string, r *Review, ctx PhaseContext, req PhaseOpReq, before, after []PhaseInterval) (PhaseCorrectionV2, error) {
	seq := int64(len(r.PhaseCorrections) + 1)
	shape := req.ShapeVersion
	if shape == "" {
		shape = PhaseOpShapeVersion
	}
	machineJSON, err := json.Marshal(ToJSON(ctx.MachineIntervals))
	if err != nil {
		return PhaseCorrectionV2{}, err
	}
	eff, err := operationDelta(req, after)
	if err != nil {
		return PhaseCorrectionV2{}, err
	}
	return PhaseCorrectionV2{
		ID:                 fmt.Sprintf("corr-%s-%04d", matchID, seq),
		SchemaVersion:      version.CorrectionSchema,
		RuleVersion:        version.CorrectionRuleVersion,
		MatchID:            matchID,
		ReplaySHA256:       ctx.ReplaySHA256,
		Kind:               KindPhaseInterval,
		Operation:          string(req.Op),
		ShapeVersion:       shape,
		Author:             req.Author,
		Reason:             req.Reason,
		AppliedAt:          time.Now().UTC().Format(time.RFC3339),
		EventRef:           req.EventRef,
		SplitSecond:        req.SplitSecond,
		MergeRight:         req.MergeRight,
		AbsorbInto:         req.AbsorbInto,
		EvidenceIDs:        req.EvidenceIDs,
		AlgorithmVersion:   ctx.RuleVersion,
		BeforeStream:       before,
		AfterStream:        after,
		MachineValue:       machineJSON,
		MachineRuleVersion: ctx.RuleVersion,
		EffectiveValue:     eff,
	}, nil
}

// operationDelta produces a meaningful, never-null effective-value delta for
// the operation. The complete after-state is always in AfterStream; this is a
// per-operation summary (for split the two halves, for merge the merged
// interval, for delete the absorbing interval, etc.).
func operationDelta(req PhaseOpReq, after []PhaseInterval) (json.RawMessage, error) {
	switch req.Op {
	case OpAccept:
		iv, ok := findStart(after, req.EventRef)
		if ok {
			return json.Marshal(iv)
		}
	case OpRelabel:
		iv, ok := findStart(after, req.EventRef)
		if ok {
			return json.Marshal(iv)
		}
	case OpSplit:
		st, _, ok := ParseEventRef(req.EventRef)
		if ok {
			for i := range after {
				if after[i].StartGameSecond == st {
					// The two halves produced by the split.
					return json.Marshal(after[i : i+2])
				}
			}
		}
	case OpMerge:
		st, _, ok := ParseEventRef(req.EventRef)
		if ok {
			for i := range after {
				if after[i].StartGameSecond == st {
					return json.Marshal(after[i])
				}
			}
		}
	case OpMove:
		st, _, ok := ParseEventRef(req.EventRef)
		if ok {
			for i := range after {
				if after[i].StartGameSecond == st {
					return json.Marshal(after[i])
				}
			}
		}
	case OpAdd:
		if req.Effective != nil {
			return json.Marshal(req.Effective)
		}
	case OpDelete:
		st, _, ok := ParseEventRef(req.AbsorbInto)
		if ok {
			for i := range after {
				if after[i].StartGameSecond == st {
					return json.Marshal(after[i])
				}
			}
		}
	}
	// Never persist a null-only after-state for split/delete/merge.
	if req.Op == OpSplit || req.Op == OpDelete || req.Op == OpMerge {
		return json.Marshal(map[string]interface{}{"resulting_stream": ToJSON(after)})
	}
	return json.RawMessage(`{}`), nil
}

// findStart returns the interval in a stream whose start matches the ref's
// start second.
func findStart(intervals []PhaseInterval, ref string) (PhaseInterval, bool) {
	if ref == "" {
		return PhaseInterval{}, false
	}
	st, _, ok := ParseEventRef(ref)
	if !ok {
		return PhaseInterval{}, false
	}
	for i := range intervals {
		if intervals[i].StartGameSecond == st {
			return intervals[i], true
		}
	}
	return PhaseInterval{}, false
}

// SetEffectivePhases persists the applied effective phase overlay (computed
// from machine intervals + corrections) and audits the update.
func (s *Store) SetEffectivePhases(matchID string, effective []json.RawMessage, author string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	r.EffectivePhaseIntervals = effective
	if err := s.writeJSON(s.Path(matchID), r); err != nil {
		return nil, err
	}
	if err := s.appendAudit(matchID, AuditEntry{
		Author: author, Action: "effective_phase_overlay", MatchID: matchID,
		Summary: fmt.Sprintf("applied %d intervals", len(effective)),
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// SetReviewStatus transitions the review workflow state and audits it.
func (s *Store) SetReviewStatus(matchID, status, author string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	r.ReviewStatus = status
	if err := s.writeJSON(s.Path(matchID), r); err != nil {
		return nil, err
	}
	if err := s.appendAudit(matchID, AuditEntry{
		Author: author, Action: "review_status", MatchID: matchID, Summary: status,
	}); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) loadUnlocked(matchID string) (*Review, error) {
	var r Review
	if err := s.readJSON(s.Path(matchID), &r); err != nil {
		if os.IsNotExist(err) {
			rv := &Review{
				SchemaVersion:    version.CorrectionSchema,
				RuleVersion:      version.CorrectionRuleVersion,
				MatchID:          matchID,
				Corrections:      []Correction{},
				PhaseCorrections: []PhaseCorrectionV2{},
				ReviewStatus:     "pending",
			}
			rv.ReviewRevision = revisionOf(rv.PhaseCorrections, rv.EffectivePhaseIntervals)
			return rv, nil
		}
		return nil, err
	}
	if r.SchemaVersion == version.CorrectionSchema {
		if r.PhaseCorrections == nil {
			r.PhaseCorrections = []PhaseCorrectionV2{}
		}
		// Deterministically derive the revision when the persisted document
		// predates the optimistic-concurrency field, so the UI always has a
		// valid token for its first mutation.
		if r.ReviewRevision == "" {
			r.ReviewRevision = revisionOf(r.PhaseCorrections, r.EffectivePhaseIntervals)
		}
		return &r, nil
	}
	// Deterministically migrate a legacy (missing/empty or v1) document in
	// memory. The migrated document is written back on the next mutation.
	migrated, err := s.migrateV1(matchID, &r)
	if err != nil {
		return nil, err
	}
	if migrated.ReviewRevision == "" {
		migrated.ReviewRevision = revisionOf(migrated.PhaseCorrections, migrated.EffectivePhaseIntervals)
	}
	return migrated, nil
}

// MigrateV1 loads a legacy v1 review document, migrates it to the current v2
// schema, and writes the migrated document back without losing machine truth
// or prior corrections. Missing documents become a fresh pending v2 review.
func (s *Store) MigrateV1(matchID string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	if err := s.writeJSON(s.Path(matchID), r); err != nil {
		return nil, err
	}
	return r, nil
}

// migrateV1 converts a legacy v1 review document to the v2 schema
// deterministically. It preserves the machine value (previous_value) and all
// prior corrections: non-phase corrections are kept verbatim, and v1
// phase-interval corrections are replayed against the machine stream to
// reconstruct the complete before/after streams that the v2 audit requires.
// EffectivePhaseIntervals from the v1 document are retained when present;
// otherwise the replay result is used.
func (s *Store) migrateV1(matchID string, r *Review) (*Review, error) {
	if r.SchemaVersion == version.CorrectionSchema {
		return r, nil
	}
	// Machine intervals are resolved server-side from the immutable phases
	// artifact; this is the only fallback for a review with no overlay.
	machine, err := s.machinePhaseContext(matchID)
	if err != nil {
		machine = PhaseContext{}
	}
	// Replay v1 phase corrections in order against the machine stream to
	// reconstruct the evolving effective stream per correction.
	stream := append([]PhaseInterval(nil), machine.MachineIntervals...)
	var phaseCorrections []PhaseCorrectionV2
	for _, c := range r.Corrections {
		if c.Kind != KindPhaseInterval {
			continue // non-phase corrections are preserved verbatim in r.Corrections
		}
		before := append([]PhaseInterval(nil), stream...)
		var eff PhaseInterval
		effOK := len(c.EffectiveValue) > 0 && string(c.EffectiveValue) != "null" &&
			json.Unmarshal(c.EffectiveValue, &eff) == nil &&
			eff.StartGameSecond >= 0 && eff.EndGameSecond > eff.StartGameSecond
		if st, _, ok := ParseEventRef(c.EventRef); ok && effOK {
			// Re-apply this v1 correction: replace the referenced interval
			// with the effective value it recorded.
			replaced := false
			for i := range stream {
				if stream[i].StartGameSecond == st {
					eff.EventRef = CanonicalEventRef(eff)
					stream[i] = eff
					replaced = true
					break
				}
			}
			if !replaced {
				stream = append(stream, eff)
				sort.Slice(stream, func(i, j int) bool { return stream[i].StartGameSecond < stream[j].StartGameSecond })
			}
		}
		after := append([]PhaseInterval(nil), stream...)
		machineJSON, _ := json.Marshal(ToJSON(machine.MachineIntervals))
		phaseCorrections = append(phaseCorrections, PhaseCorrectionV2{
			ID:                 c.ID,
			SchemaVersion:      version.CorrectionSchema,
			RuleVersion:        version.CorrectionRuleVersion,
			MatchID:            matchID,
			ReplaySHA256:       c.ReplaySHA256,
			Kind:               KindPhaseInterval,
			Operation:          "v1_migrated",
			ShapeVersion:       "v1",
			Author:             c.Author,
			Reason:             c.Reason,
			AppliedAt:          c.AppliedAt,
			EventRef:           c.EventRef,
			EvidenceIDs:        c.EvidenceIDs,
			AlgorithmVersion:   c.AlgorithmVersion,
			BeforeStream:       before,
			AfterStream:        after,
			MachineValue:       machineJSON,
			MachineRuleVersion: machine.RuleVersion,
			EffectiveValue:     c.EffectiveValue,
		})
	}
	r.SchemaVersion = version.CorrectionSchema
	r.RuleVersion = version.CorrectionRuleVersion
	if r.PhaseCorrections == nil {
		r.PhaseCorrections = []PhaseCorrectionV2{}
	}
	// Retain a v1 overlay when present; otherwise persist the replay result so
	// the next operation composes against the migrated stream.
	if len(r.EffectivePhaseIntervals) == 0 && len(phaseCorrections) > 0 {
		r.EffectivePhaseIntervals = ToJSON(stream)
	}
	r.PhaseCorrections = append(phaseCorrections, r.PhaseCorrections...)
	if r.Corrections == nil {
		r.Corrections = []Correction{}
	}
	return r, nil
}

// machinePhaseContext reads the immutable phases artifact for a match.
func (s *Store) machinePhaseContext(matchID string) (PhaseContext, error) {
	var ph struct {
		RuleVersion     string          `json:"rule_version"`
		EligibleSeconds int             `json:"eligible_seconds"`
		Intervals       []PhaseInterval `json:"intervals"`
	}
	path := filepath.Join(s.Root, "matches", matchID, store.ArtifactPhases)
	if err := s.readJSON(path, &ph); err != nil {
		return PhaseContext{}, err
	}
	return PhaseContext{
		EligibleSeconds:  ph.EligibleSeconds,
		RuleVersion:      ph.RuleVersion,
		MachineIntervals: ph.Intervals,
	}, nil
}

func (s *Store) readJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("review: decode %s: %w", path, err)
	}
	return nil
}

func (s *Store) writeJSON(path string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("review: marshal: %w", err)
	}
	return store.WriteAtomic(path, b)
}

// appendAudit appends one immutable audit entry.
func (s *Store) appendAudit(matchID string, e AuditEntry) error {
	var a Audit
	if err := s.readJSON(s.AuditPath(), &a); err != nil {
		if os.IsNotExist(err) {
			a = Audit{SchemaVersion: version.CorrectionSchema, Entries: []AuditEntry{}}
		} else {
			return err
		}
	}
	e.Seq = int64(len(a.Entries) + 1)
	e.AppliedAt = time.Now().UTC()
	e.MatchID = matchID
	a.Entries = append(a.Entries, e)
	return s.writeJSON(s.AuditPath(), a)
}

// LoadAudit returns the full audit log, newest first.
func (s *Store) LoadAudit() (*Audit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a Audit
	if err := s.readJSON(s.AuditPath(), &a); err != nil {
		if os.IsNotExist(err) {
			return &Audit{SchemaVersion: version.CorrectionSchema, Entries: []AuditEntry{}}, nil
		}
		return nil, err
	}
	entries := append([]AuditEntry(nil), a.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq > entries[j].Seq })
	return &Audit{SchemaVersion: a.SchemaVersion, Entries: entries}, nil
}

// jsonEqual reports deep JSON semantic equality of two raw messages
// (field-order and formatting insensitive).
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return fmt.Sprintf("%#v", av) == fmt.Sprintf("%#v", bv)
}
