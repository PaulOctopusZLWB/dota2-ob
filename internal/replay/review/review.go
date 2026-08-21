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
	"strings"
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

// CategoryDecision is an immutable human decision over the server-resolved
// frozen input category. MachineCategory and ReplaySHA256 are never accepted
// from the client as truth.
type CategoryDecision struct {
	ID                string `json:"id"`
	MachineCategory   string `json:"machine_category"`
	EffectiveCategory string `json:"effective_category"`
	Decision          string `json:"decision"` // confirm|reclassify|replacement
	MatchID           string `json:"match_id"`
	ReplaySHA256      string `json:"replay_sha256"`
	Author            string `json:"author"`
	Reason            string `json:"reason"`
	AppliedAt         string `json:"applied_at"`
}

// FinalChecklist records the required human inspection evidence. Booleans are
// intentionally never defaulted by the server or UI.
type FinalChecklist struct {
	PhaseStreamReviewed              bool   `json:"phase_stream_reviewed"`
	RoleProvenanceReviewed           bool   `json:"role_provenance_reviewed"`
	RoleProvenanceEvidence           string `json:"role_provenance_evidence"`
	OfficialExperimentalAcknowledged bool   `json:"official_experimental_acknowledged"`
}

// FinalReviewSnapshot is an immutable sign-off of one exact review revision.
type FinalReviewSnapshot struct {
	ID               string           `json:"id"`
	MatchID          string           `json:"match_id"`
	ReplaySHA256     string           `json:"replay_sha256"`
	SignedRevision   string           `json:"signed_revision"`
	PhaseRuleVersion string           `json:"phase_rule_version"`
	FinalPhaseStream []PhaseInterval  `json:"final_phase_stream"`
	CategoryDecision CategoryDecision `json:"category_decision"`
	Checklist        FinalChecklist   `json:"checklist"`
	Author           string           `json:"author"`
	Reason           string           `json:"reason"`
	SignedAt         string           `json:"signed_at"`
}

// CategoryContext and FinalizeContext are authoritative server-side inputs.
type CategoryContext struct{ MachineCategory, ReplaySHA256 string }
type FinalizeContext struct {
	ReplaySHA256     string
	MachineCategory  string
	PhaseRuleVersion string
	MachineIntervals []PhaseInterval
}

type CategoryDecisionRequest struct {
	Decision          string `json:"decision"`
	EffectiveCategory string `json:"effective_category"`
	Author            string `json:"author"`
	Reason            string `json:"reason"`
	ExpectedRevision  string `json:"expected_revision"`
	MachineCategory   string `json:"machine_category,omitempty"`
	ReplaySHA256      string `json:"replay_sha256,omitempty"`
}

type FinalizeRequest struct {
	Author           string         `json:"author"`
	Reason           string         `json:"reason"`
	ExpectedRevision string         `json:"expected_revision"`
	ReplaySHA256     string         `json:"replay_sha256,omitempty"`
	Checklist        FinalChecklist `json:"checklist"`
}

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
	SchemaVersion string `json:"schema_version"`
	RuleVersion   string `json:"rule_version"`
	MatchID       string `json:"match_id"`
	ReplaySHA256  string `json:"replay_sha256,omitempty"`
	// MachineCategory and MissingRequirements are resolved for queue reads.
	// They are informational and excluded from revision derivation.
	MachineCategory     string       `json:"machine_category,omitempty"`
	MissingRequirements []string     `json:"missing_requirements,omitempty"`
	Corrections         []Correction `json:"corrections"`
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
	ReviewStatus      string                `json:"review_status"` // pending|in_progress|reviewed
	CategoryDecisions []CategoryDecision    `json:"category_decisions,omitempty"`
	FinalSnapshots    []FinalReviewSnapshot `json:"final_snapshots,omitempty"`
	// CurrentFinalSnapshotID is cleared by every later analytical/review
	// mutation. Historical snapshots remain immutable for audit.
	CurrentFinalSnapshotID string `json:"current_final_snapshot_id,omitempty"`
	// RecomputeVersion is the scoring contract version that a synchronous
	// recompute ran under (set by the API after role-override recompute).
	RecomputeVersion string `json:"recompute_version,omitempty"`
}

func invalidateFinal(r *Review) {
	if r.CurrentFinalSnapshotID != "" || r.ReviewStatus == "reviewed" {
		r.CurrentFinalSnapshotID = ""
		r.ReviewStatus = "in_progress"
	}
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
	Root            string
	mu              sync.Mutex
	writeAtomic     func(string, []byte) error // test injection; nil uses store.WriteAtomic
	recoveryAtomic  func(string, []byte) error // test injection for rollback/recovery
	removeAtomicLog func(string) error         // test injection for journal removal
	crashBarrier    func(string)               // subprocess crash injection
}

// transactionJournal is a durable prepare record for one review+audit commit.
// If it exists after interruption or a failed rollback, recovery restores both
// authoritative files to their exact pre-transaction bytes before any read or
// subsequent mutation proceeds.
type transactionJournal struct {
	SchemaVersion string `json:"schema_version"`
	ReviewPath    string `json:"review_path"`
	AuditPath     string `json:"audit_path"`
	ReviewExisted bool   `json:"review_existed"`
	AuditExisted  bool   `json:"audit_existed"`
	PriorReview   []byte `json:"prior_review,omitempty"`
	PriorAudit    []byte `json:"prior_audit,omitempty"`
}

type storageError struct{ err error }

func (e *storageError) Error() string { return "review storage: " + e.err.Error() }
func (e *storageError) Unwrap() error { return e.err }

// IsStorageError reports a persistence failure that the API must expose as a
// server error rather than a client validation error.
func IsStorageError(err error) bool {
	_, ok := err.(*storageError)
	return ok
}

// New creates a review store rooted at root.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("review: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("review: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("review: mkdir: %w", err)
	}
	s := &Store{Root: abs}
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
	return s, nil
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

func (s *Store) journalPath() string {
	return filepath.Join(s.Root, "review-transaction.json")
}

// Load reads the review document for a match (missing → empty pending).
func (s *Store) Load(matchID string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
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
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
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
	invalidateFinal(r)
	if r.ReplaySHA256 == "" {
		r.ReplaySHA256 = truth.ReplaySHA256
	}
	if r.ReviewStatus == "" || r.ReviewStatus == "pending" {
		r.ReviewStatus = "in_progress"
	}
	r.ReviewRevision = revisionOfReview(r)
	if err := s.commitReviewAndAudit(matchID, r, AuditEntry{
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
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
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
	invalidateFinal(r)
	if r.ReplaySHA256 == "" {
		r.ReplaySHA256 = ctx.ReplaySHA256
	}
	if r.ReviewStatus == "" || r.ReviewStatus == "pending" {
		r.ReviewStatus = "in_progress"
	}
	// Advance the revision from the ordered corrections + canonical effective
	// stream AFTER this correction, so every success (including accept)
	// yields a new revision for the next mutation.
	r.ReviewRevision = revisionOfReview(r)
	// Commit correction/effective stream/revision and the immutable audit entry
	// as one logical transaction. Any failure leaves both authoritative files
	// at their exact prior bytes.
	if err := s.commitReviewAndAudit(matchID, r, AuditEntry{
		Author: req.Author, Action: "phase_correction_v2", MatchID: matchID,
		CorrectionID: pc.ID, Summary: fmt.Sprintf("%s %s", req.Op, req.EventRef),
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// revisionOfReview derives an opaque optimistic-concurrency token from every
// mutable review field. Phase operations, authoritative corrections, status
// transitions, and effective-overlay writes therefore invalidate stale forms,
// and restart deterministically reproduces the same token.
func revisionOfReview(r *Review) string {
	h := sha256.New()
	payload := struct {
		Corrections             []Correction          `json:"corrections"`
		PhaseCorrections        []PhaseCorrectionV2   `json:"phase_corrections"`
		EffectivePhaseIntervals []json.RawMessage     `json:"effective_phase_intervals"`
		ReviewStatus            string                `json:"review_status"`
		CategoryDecisions       []CategoryDecision    `json:"category_decisions"`
		FinalSnapshots          []FinalReviewSnapshot `json:"final_snapshots"`
		CurrentFinalSnapshotID  string                `json:"current_final_snapshot_id"`
	}{r.Corrections, r.PhaseCorrections, r.EffectivePhaseIntervals, r.ReviewStatus, r.CategoryDecisions, r.FinalSnapshots, r.CurrentFinalSnapshotID}
	if b, err := json.Marshal(payload); err == nil {
		h.Write(b)
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
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	r.EffectivePhaseIntervals = effective
	invalidateFinal(r)
	r.ReviewRevision = revisionOfReview(r)
	if err := s.commitReviewAndAudit(matchID, r, AuditEntry{
		Author: author, Action: "effective_phase_overlay", MatchID: matchID,
		Summary: fmt.Sprintf("applied %d intervals", len(effective)),
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// SetReviewStatus transitions the review workflow state and audits it.
func (s *Store) SetReviewStatus(matchID, status, author, expectedRevision string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	if expectedRevision == "" {
		return nil, staleErrorf("review_revision_required:current=%s", r.ReviewRevision)
	}
	if expectedRevision != r.ReviewRevision {
		return nil, staleErrorf("review_revision_mismatch:expected=%s current=%s", expectedRevision, r.ReviewRevision)
	}
	if status == "reviewed" && r.CurrentFinalSnapshotID == "" {
		return nil, fmt.Errorf("review_requirements_missing:final_snapshot")
	}
	if status == "reviewed" {
		found := false
		for i := range r.FinalSnapshots {
			if r.FinalSnapshots[i].ID == r.CurrentFinalSnapshotID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("review_requirements_missing:current_final_snapshot_invalid")
		}
	}
	if status != "reviewed" {
		r.CurrentFinalSnapshotID = ""
	}
	r.ReviewStatus = status
	r.ReviewRevision = revisionOfReview(r)
	if err := s.commitReviewAndAudit(matchID, r, AuditEntry{
		Author: author, Action: "review_status", MatchID: matchID, Summary: status,
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// DecideCategory records an auditable classification using only server-side
// machine category and replay identity.
func (s *Store) DecideCategory(matchID string, ctx CategoryContext, req CategoryDecisionRequest) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{err}
	}
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision == "" || req.ExpectedRevision != r.ReviewRevision {
		return nil, staleErrorf("review_revision_mismatch:expected=%s current=%s", req.ExpectedRevision, r.ReviewRevision)
	}
	if strings.TrimSpace(req.Author) == "" || strings.TrimSpace(req.Reason) == "" || strings.TrimSpace(ctx.MachineCategory) == "" || ctx.ReplaySHA256 == "" {
		return nil, fmt.Errorf("category_author_reason_machine_identity_required")
	}
	if req.MachineCategory != "" && req.MachineCategory != ctx.MachineCategory {
		return nil, fmt.Errorf("machine_category_mismatch")
	}
	if req.ReplaySHA256 != "" && req.ReplaySHA256 != ctx.ReplaySHA256 {
		return nil, fmt.Errorf("replay_sha256_mismatch")
	}
	if req.Decision != "confirm" && req.Decision != "reclassify" && req.Decision != "replacement" {
		return nil, fmt.Errorf("invalid_category_decision")
	}
	effective := req.EffectiveCategory
	if req.Decision == "confirm" {
		if effective != "" && effective != ctx.MachineCategory {
			return nil, fmt.Errorf("confirmed_category_must_match_machine")
		}
		effective = ctx.MachineCategory
	}
	if strings.TrimSpace(effective) == "" {
		return nil, fmt.Errorf("effective_category_required")
	}
	d := CategoryDecision{ID: fmt.Sprintf("category-%s-%04d", matchID, len(r.CategoryDecisions)+1), MachineCategory: ctx.MachineCategory, EffectiveCategory: effective, Decision: req.Decision, MatchID: matchID, ReplaySHA256: ctx.ReplaySHA256, Author: req.Author, Reason: req.Reason, AppliedAt: time.Now().UTC().Format(time.RFC3339)}
	r.CategoryDecisions = append(r.CategoryDecisions, d)
	r.ReplaySHA256 = ctx.ReplaySHA256
	invalidateFinal(r)
	if r.ReviewStatus == "pending" || r.ReviewStatus == "" {
		r.ReviewStatus = "in_progress"
	}
	r.ReviewRevision = revisionOfReview(r)
	if err := s.commitReviewAndAudit(matchID, r, AuditEntry{Author: req.Author, Action: "category_decision", MatchID: matchID, CorrectionID: d.ID, Summary: req.Decision + " " + effective}); err != nil {
		return nil, err
	}
	return r, nil
}

// FinalizeReview atomically signs the exact current effective phase stream and
// checklist and transitions to reviewed. No client phase/category/hash is
// trusted.
func (s *Store) FinalizeReview(matchID string, ctx FinalizeContext, req FinalizeRequest) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{err}
	}
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision == "" || req.ExpectedRevision != r.ReviewRevision {
		return nil, staleErrorf("review_revision_mismatch:expected=%s current=%s", req.ExpectedRevision, r.ReviewRevision)
	}
	if strings.TrimSpace(req.Author) == "" || strings.TrimSpace(req.Reason) == "" {
		return nil, fmt.Errorf("final_author_reason_required")
	}
	if req.ReplaySHA256 != "" && req.ReplaySHA256 != ctx.ReplaySHA256 {
		return nil, fmt.Errorf("replay_sha256_mismatch")
	}
	missing := []string{}
	if len(r.CategoryDecisions) == 0 {
		missing = append(missing, "category_decision")
	}
	if !req.Checklist.PhaseStreamReviewed {
		missing = append(missing, "phase_stream_reviewed")
	}
	if !req.Checklist.RoleProvenanceReviewed || strings.TrimSpace(req.Checklist.RoleProvenanceEvidence) == "" {
		missing = append(missing, "role_provenance_evidence")
	}
	if !req.Checklist.OfficialExperimentalAcknowledged {
		missing = append(missing, "official_experimental_acknowledged")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("review_requirements_missing:%s", strings.Join(missing, ","))
	}
	stream := append([]PhaseInterval(nil), ctx.MachineIntervals...)
	if len(r.EffectivePhaseIntervals) > 0 {
		stream, err = FromJSON(r.EffectivePhaseIntervals)
		if err != nil {
			return nil, err
		}
	}
	if len(stream) == 0 || ctx.ReplaySHA256 == "" {
		return nil, fmt.Errorf("review_requirements_missing:phase_stream_or_replay_identity")
	}
	d := r.CategoryDecisions[len(r.CategoryDecisions)-1]
	if d.ReplaySHA256 != ctx.ReplaySHA256 || (ctx.MachineCategory != "" && d.MachineCategory != ctx.MachineCategory) {
		return nil, fmt.Errorf("review_requirements_missing:category_decision_identity_mismatch")
	}
	snap := FinalReviewSnapshot{ID: fmt.Sprintf("final-%s-%04d", matchID, len(r.FinalSnapshots)+1), MatchID: matchID, ReplaySHA256: ctx.ReplaySHA256, SignedRevision: r.ReviewRevision, PhaseRuleVersion: ctx.PhaseRuleVersion, FinalPhaseStream: stream, CategoryDecision: d, Checklist: req.Checklist, Author: req.Author, Reason: req.Reason, SignedAt: time.Now().UTC().Format(time.RFC3339)}
	r.FinalSnapshots = append(r.FinalSnapshots, snap)
	r.CurrentFinalSnapshotID = snap.ID
	r.ReviewStatus = "reviewed"
	r.ReplaySHA256 = ctx.ReplaySHA256
	r.ReviewRevision = revisionOfReview(r)
	if err := s.commitReviewAndAudit(matchID, r, AuditEntry{Author: req.Author, Action: "final_review_snapshot", MatchID: matchID, CorrectionID: snap.ID, Summary: "reviewed"}); err != nil {
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
			rv.ReviewRevision = revisionOfReview(rv)
			return rv, nil
		}
		return nil, err
	}
	if r.SchemaVersion == version.CorrectionSchema || r.SchemaVersion == version.CorrectionSchemaV2 {
		wasV2 := r.SchemaVersion == version.CorrectionSchemaV2
		r.SchemaVersion = version.CorrectionSchema
		r.RuleVersion = version.CorrectionRuleVersion
		if r.PhaseCorrections == nil {
			r.PhaseCorrections = []PhaseCorrectionV2{}
		}
		if r.CategoryDecisions == nil {
			r.CategoryDecisions = []CategoryDecision{}
		}
		if r.FinalSnapshots == nil {
			r.FinalSnapshots = []FinalReviewSnapshot{}
		}
		// A v2 "reviewed" flag had no signed category/stream/checklist proof.
		// Migrate it deterministically to in_progress rather than grandfathering
		// an unauditable completion.
		if r.ReviewStatus == "reviewed" && r.CurrentFinalSnapshotID == "" {
			r.ReviewStatus = "in_progress"
		}
		// Deterministically derive the revision when the persisted document
		// predates the optimistic-concurrency field, so the UI always has a
		// valid token for its first mutation.
		if r.ReviewRevision == "" || wasV2 {
			r.ReviewRevision = revisionOfReview(&r)
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
		migrated.ReviewRevision = revisionOfReview(migrated)
	}
	return migrated, nil
}

// MigrateV1 loads a legacy v1 review document, migrates it to the current v2
// schema, and writes the migrated document back without losing machine truth
// or prior corrections. Missing documents become a fresh pending v2 review.
func (s *Store) MigrateV1(matchID string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
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
	return s.writeBytes(path, b)
}

func (s *Store) writeBytes(path string, b []byte) error {
	if s.writeAtomic != nil {
		return s.writeAtomic(path, b)
	}
	return store.WriteAtomic(path, b)
}

func (s *Store) recoveryWrite(path string, b []byte) error {
	if s.recoveryAtomic != nil {
		return s.recoveryAtomic(path, b)
	}
	return store.WriteAtomic(path, b)
}

func (s *Store) removeJournal() error {
	if s.removeAtomicLog != nil {
		return s.removeAtomicLog(s.journalPath())
	}
	return store.RemoveDurable(s.journalPath())
}

func readPrior(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func (s *Store) restoreFile(path string, existed bool, b []byte) error {
	if existed {
		return s.recoveryWrite(path, b)
	}
	return store.RemoveDurable(path)
}

func (s *Store) hitCrashBarrier(name string) {
	if s.crashBarrier != nil {
		s.crashBarrier(name)
	}
}

// restoreTransaction rolls both authoritative documents back to the exact
// prepared bytes. The journal is removed only after both restorations succeed;
// otherwise it remains durable for the next restart/recovery attempt.
func (s *Store) restoreTransaction(tx *transactionJournal, context string) error {
	s.hitCrashBarrier(context + "_begin")
	if err := s.restoreFile(tx.ReviewPath, tx.ReviewExisted, tx.PriorReview); err != nil {
		return fmt.Errorf("restore review: %w", err)
	}
	s.hitCrashBarrier(context + "_after_review_restore")
	if err := s.restoreFile(tx.AuditPath, tx.AuditExisted, tx.PriorAudit); err != nil {
		return fmt.Errorf("restore audit: %w", err)
	}
	s.hitCrashBarrier(context + "_after_audit_restore")
	s.hitCrashBarrier(context + "_before_journal_remove")
	if err := s.removeJournal(); err != nil {
		return fmt.Errorf("remove transaction journal: %w", err)
	}
	s.hitCrashBarrier(context + "_after_journal_remove")
	return nil
}

// recoverPending runs before every read/mutation and at Store construction.
// A prepared transaction is always rolled back, so an interrupted process or
// double I/O failure can never expose a split review/audit revision.
func (s *Store) recoverPending() error {
	b, err := os.ReadFile(s.journalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var tx transactionJournal
	if err := json.Unmarshal(b, &tx); err != nil {
		return fmt.Errorf("decode transaction journal: %w", err)
	}
	if (tx.SchemaVersion != version.CorrectionRuleVersion && tx.SchemaVersion != version.CorrectionRuleVersionV3) || tx.AuditPath != s.AuditPath() || filepath.Dir(tx.ReviewPath) == "." || !filepath.IsAbs(tx.ReviewPath) || filepath.Clean(tx.ReviewPath) != tx.ReviewPath {
		return fmt.Errorf("invalid transaction journal")
	}
	reviewRoot := filepath.Join(s.Root, "matches") + string(os.PathSeparator)
	if len(tx.ReviewPath) <= len(reviewRoot) || tx.ReviewPath[:len(reviewRoot)] != reviewRoot {
		return fmt.Errorf("transaction review path outside store")
	}
	return s.restoreTransaction(&tx, "recovery")
}

// commitReviewAndAudit prepares both complete documents and durably records
// their exact prior bytes before either promotion. Any failed promotion rolls
// both back. If rollback itself fails, the journal survives so Store restart
// (or the next operation) deterministically completes recovery before serving
// data.
func (s *Store) commitReviewAndAudit(matchID string, r *Review, e AuditEntry) error {
	reviewBytes, err := json.Marshal(r)
	if err != nil {
		return &storageError{fmt.Errorf("marshal review: %w", err)}
	}
	var audit Audit
	auditPath := s.AuditPath()
	priorAudit, readErr := os.ReadFile(auditPath)
	priorAuditExists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return &storageError{fmt.Errorf("read audit: %w", readErr)}
	}
	if priorAuditExists {
		if err := json.Unmarshal(priorAudit, &audit); err != nil {
			return &storageError{fmt.Errorf("decode audit: %w", err)}
		}
	} else {
		audit = Audit{SchemaVersion: version.CorrectionSchema, Entries: []AuditEntry{}}
	}
	audit.SchemaVersion = version.CorrectionSchema
	e.Seq = int64(len(audit.Entries) + 1)
	e.AppliedAt = time.Now().UTC()
	e.MatchID = matchID
	audit.Entries = append(audit.Entries, e)
	auditBytes, err := json.Marshal(&audit)
	if err != nil {
		return &storageError{fmt.Errorf("marshal audit: %w", err)}
	}
	reviewPath := s.Path(matchID)
	priorReview, priorReviewExists, err := readPrior(reviewPath)
	if err != nil {
		return &storageError{fmt.Errorf("read review: %w", err)}
	}
	tx := transactionJournal{
		SchemaVersion: version.CorrectionRuleVersion,
		ReviewPath:    reviewPath, AuditPath: auditPath,
		ReviewExisted: priorReviewExists, AuditExisted: priorAuditExists,
		PriorReview: priorReview, PriorAudit: priorAudit,
	}
	journalBytes, err := json.Marshal(&tx)
	if err != nil {
		return &storageError{fmt.Errorf("marshal transaction journal: %w", err)}
	}
	if err := s.writeBytes(s.journalPath(), journalBytes); err != nil {
		return &storageError{fmt.Errorf("prepare transaction journal: %w", err)}
	}
	s.hitCrashBarrier("commit_after_durable_prepare")
	promote := func(label, path string, b []byte) error {
		if err := s.writeBytes(path, b); err != nil {
			if restoreErr := s.restoreTransaction(&tx, "rollback"); restoreErr != nil {
				return &storageError{fmt.Errorf("%s promotion: %v; durable recovery pending: %w", label, err, restoreErr)}
			}
			return &storageError{fmt.Errorf("%s promotion: %w", label, err)}
		}
		return nil
	}
	if err := promote("audit", auditPath, auditBytes); err != nil {
		return err
	}
	s.hitCrashBarrier("commit_after_audit_promotion")
	if err := promote("review", reviewPath, reviewBytes); err != nil {
		return err
	}
	s.hitCrashBarrier("commit_after_review_promotion")
	s.hitCrashBarrier("commit_before_journal_remove")
	if err := s.removeJournal(); err != nil {
		if restoreErr := s.restoreTransaction(&tx, "rollback"); restoreErr != nil {
			return &storageError{fmt.Errorf("commit journal removal: %v; durable recovery pending: %w", err, restoreErr)}
		}
		return &storageError{fmt.Errorf("commit journal removal: %w", err)}
	}
	s.hitCrashBarrier("commit_after_journal_remove")
	return nil
}

// LoadAudit returns the full audit log, newest first.
func (s *Store) LoadAudit() (*Audit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverPending(); err != nil {
		return nil, &storageError{fmt.Errorf("recover pending transaction: %w", err)}
	}
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
