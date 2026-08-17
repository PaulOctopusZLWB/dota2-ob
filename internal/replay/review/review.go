// Package review implements the gold-review workflow: a per-match correction
// overlay store and an immutable audit log. Machine output is never mutated;
// every human correction retains the machine value, the effective value, the
// author, reason, timestamp, evidence, and the algorithm version that produced
// the machine value. Read-only pages can never mutate analytical state; review
// mutations bind to the loopback server and require a local session token.
package review

import (
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

// Correction is one human review mutation. The machine output remains
// immutable and inspectable; this record retains the full before/after pair.
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
	// PreviousValue is the machine (or previously-effective) value.
	PreviousValue json.RawMessage `json:"previous_value"`
	// EffectiveValue is the corrected value.
	EffectiveValue json.RawMessage `json:"effective_value"`
	EvidenceIDs    []string        `json:"evidence_ids,omitempty"`
	// EventRef points to the corrected phase interval / episode / metric row.
	EventRef string `json:"event_ref,omitempty"`
}

// Review is the per-match review document: the machine overlay target plus all
// corrections and the effective (corrected) overlay stream.
type Review struct {
	SchemaVersion string       `json:"schema_version"`
	RuleVersion   string       `json:"rule_version"`
	MatchID       string       `json:"match_id"`
	Corrections   []Correction `json:"corrections"`
	// EffectivePhaseIntervals is the phase stream with accepted corrections
	// applied (machine output preserved separately in phases.json).
	EffectivePhaseIntervals []json.RawMessage `json:"effective_phase_intervals,omitempty"`
	// ReviewStatus is the review workflow state.
	ReviewStatus string `json:"review_status"` // pending|in_progress|reviewed
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

// Path returns the review document path for a match.
func (s *Store) Path(matchID string) string {
	return filepath.Join(s.Root, "matches", matchID, store.ArtifactCorrections)
}

// AuditPath returns the shared audit log path.
func (s *Store) AuditPath() string {
	return filepath.Join(s.Root, "review-audit.json")
}

// Load reads the review document for a match (missing → empty pending).
func (s *Store) Load(matchID string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Review
	if err := s.readJSON(s.Path(matchID), &r); err != nil {
		if os.IsNotExist(err) {
			return &Review{
				SchemaVersion: version.CorrectionSchema,
				RuleVersion:   version.CorrectionRuleVersion,
				MatchID:       matchID,
				Corrections:   []Correction{},
				ReviewStatus:  "pending",
			}, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadByStore loads a review document via a replay store path (same layout).
func LoadByStore(st *store.Store, matchID string) (*Review, error) {
	s, err := New(st.Root)
	if err != nil {
		return nil, err
	}
	return s.Load(matchID)
}

// Add appends one correction and returns its deterministic id. Machine output
// is preserved by construction: the caller supplies the previous (machine)
// value and the effective value.
func (s *Store) Add(matchID string, c Correction) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadUnlocked(matchID)
	if err != nil {
		return nil, err
	}
	seq := int64(len(r.Corrections) + 1)
	c.ID = fmt.Sprintf("corr-%s-%04d", matchID, seq)
	c.SchemaVersion = version.CorrectionSchema
	c.RuleVersion = version.CorrectionRuleVersion
	if c.AppliedAt == "" {
		c.AppliedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if c.AlgorithmVersion == "" {
		c.AlgorithmVersion = version.MetricsRuleVersion
	}
	r.Corrections = append(r.Corrections, c)
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
			return &Review{
				SchemaVersion: version.CorrectionSchema,
				RuleVersion:   version.CorrectionRuleVersion,
				MatchID:       matchID,
				Corrections:   []Correction{},
				ReviewStatus:  "pending",
			}, nil
		}
		return nil, err
	}
	return &r, nil
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
