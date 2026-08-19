package review

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

func atomicTestContext() PhaseContext {
	return PhaseContext{EligibleSeconds: 100, RuleVersion: "phase.v1", ReplaySHA256: "sha", MachineIntervals: []PhaseInterval{{
		StartGameSecond: 0, EndGameSecond: 100, GlobalPhase: "laning",
	}}}
}

func TestPhaseMutationCorruptAuditLeavesAllBytesUnchanged(t *testing.T) {
	s, _ := New(t.TempDir())
	corrupt := []byte(`{"broken"`)
	if err := os.WriteFile(s.AuditPath(), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	r0, _ := s.Load("m1")
	_, err := s.ApplyPhaseOp("m1", atomicTestContext(), PhaseOpReq{
		Op: OpAccept, EventRef: "interval@0-100", ExpectedRevision: r0.ReviewRevision,
	})
	if !IsStorageError(err) {
		t.Fatalf("err=%v want storage error", err)
	}
	if _, statErr := os.Stat(s.Path("m1")); !os.IsNotExist(statErr) {
		t.Fatalf("review file changed/created: %v", statErr)
	}
	got, _ := os.ReadFile(s.AuditPath())
	if string(got) != string(corrupt) {
		t.Fatal("corrupt audit bytes changed")
	}
}

func TestPhaseMutationAuditWriteFailureLeavesAllBytesUnchanged(t *testing.T) {
	s, _ := New(t.TempDir())
	initialAudit := []byte(`{"schema_version":"replay.corrections.v2","entries":[]}`)
	if err := store.WriteAtomic(s.AuditPath(), initialAudit); err != nil {
		t.Fatal(err)
	}
	r0, _ := s.Load("m1")
	s.writeAtomic = func(path string, b []byte) error {
		if path == s.AuditPath() {
			return fmt.Errorf("injected unwritable audit")
		}
		return store.WriteAtomic(path, b)
	}
	_, err := s.ApplyPhaseOp("m1", atomicTestContext(), PhaseOpReq{
		Op: OpAccept, EventRef: "interval@0-100", ExpectedRevision: r0.ReviewRevision,
	})
	if !IsStorageError(err) {
		t.Fatalf("err=%v want storage error", err)
	}
	if _, statErr := os.Stat(s.Path("m1")); !os.IsNotExist(statErr) {
		t.Fatalf("review file changed/created: %v", statErr)
	}
	got, _ := os.ReadFile(s.AuditPath())
	if string(got) != string(initialAudit) {
		t.Fatal("audit bytes changed after injected write failure")
	}
}

func TestAddAuthoritativePreservesMachineValue(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	machine := json.RawMessage(`{"global_phase":"laning","start":0,"end":600}`)
	eff := json.RawMessage(`{"global_phase":"midgame","start":0,"end":600}`)
	rv, err := s.AddAuthoritative("m1", Correction{
		MatchID: "m1", Kind: KindPhaseInterval, Author: "paul",
		Reason: "boundary moved on evidence", PreviousValue: machine, EffectiveValue: eff,
		EventRef: "interval@0-600",
	}, MachineTruth{ReplaySHA256: "abc123", AlgorithmVersion: "ti2026.phase.v1", MachineValue: machine})
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Corrections) != 1 {
		t.Fatalf("corrections=%d", len(rv.Corrections))
	}
	c := rv.Corrections[0]
	if c.Author != "paul" || c.Reason == "" || c.AppliedAt == "" {
		t.Fatalf("correction missing provenance: %+v", c)
	}
	if c.AlgorithmVersion != "ti2026.phase.v1" {
		t.Fatalf("algorithm version=%q", c.AlgorithmVersion)
	}
	if c.ReplaySHA256 != "abc123" {
		t.Fatalf("replay sha=%q", c.ReplaySHA256)
	}
	if string(c.PreviousValue) != `{"global_phase":"laning","start":0,"end":600}` {
		t.Fatalf("previous value changed: %s", c.PreviousValue)
	}
	loaded, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Corrections) != 1 || loaded.ReviewStatus != "in_progress" {
		t.Fatalf("reload: %+v", loaded)
	}
}

func TestAddRejectsTamperedPreviousValue(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	machine := json.RawMessage(`{"global_phase":"laning","start":0,"end":600}`)
	fabricated := json.RawMessage(`{"global_phase":"decisive","start":0,"end":600}`)
	eff := json.RawMessage(`{"global_phase":"midgame","start":0,"end":600}`)
	_, err = s.AddAuthoritative("m1", Correction{
		MatchID: "m1", Kind: KindPhaseInterval, Author: "paul", Reason: "r",
		PreviousValue: fabricated, EffectiveValue: eff,
	}, MachineTruth{ReplaySHA256: "abc", AlgorithmVersion: "ti2026.phase.v1", MachineValue: machine})
	if err == nil {
		t.Fatal("expected tamper rejection")
	}
	if !strings.Contains(err.Error(), "tamper") {
		t.Fatalf("error=%q want tamper", err)
	}
}

func TestAuditAppendOnly(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := json.RawMessage(`{"p":"x"}`)
	eff := json.RawMessage(`{"p":"y"}`)
	for i := 0; i < 3; i++ {
		if _, err := s.AddAuthoritative("m1", Correction{
			MatchID: "m1", Kind: KindEvent, Author: "paul", Reason: "r",
			PreviousValue: prev, EffectiveValue: eff,
		}, MachineTruth{AlgorithmVersion: "ti2026.phase.v1", MachineValue: prev}); err != nil {
			t.Fatal(err)
		}
	}
	audit, err := s.LoadAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Entries) != 3 {
		t.Fatalf("audit entries=%d", len(audit.Entries))
	}
	if audit.Entries[0].Seq != 3 || audit.Entries[2].Seq != 1 {
		t.Fatalf("audit order wrong: %d,%d", audit.Entries[0].Seq, audit.Entries[2].Seq)
	}
	for _, e := range audit.Entries {
		if e.AppliedAt.IsZero() {
			t.Fatalf("audit entry %d has zero applied_at", e.Seq)
		}
	}
}

func TestSetReviewStatus(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	rv, err := s.SetReviewStatus("m1", "reviewed", "paul", before.ReviewRevision)
	if err != nil {
		t.Fatal(err)
	}
	if rv.ReviewStatus != "reviewed" {
		t.Fatalf("status=%s", rv.ReviewStatus)
	}
	loaded, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ReviewStatus != "reviewed" {
		t.Fatalf("reload status=%s", loaded.ReviewStatus)
	}
}

type reviewMutator func(*Store) error

func seededAtomicStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPhaseOp("m1", atomicTestContext(), PhaseOpReq{
		Op: OpAccept, EventRef: "interval@0-100", ExpectedRevision: r.ReviewRevision, Author: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	return s, root
}

func atomicMutators() map[string]reviewMutator {
	return map[string]reviewMutator{
		"phase_op": func(s *Store) error {
			r, err := s.Load("m1")
			if err != nil {
				return err
			}
			_, err = s.ApplyPhaseOp("m1", atomicTestContext(), PhaseOpReq{
				Op: OpAccept, EventRef: "interval@0-100", ExpectedRevision: r.ReviewRevision, Author: "test",
			})
			return err
		},
		"review_status": func(s *Store) error {
			r, err := s.Load("m1")
			if err != nil {
				return err
			}
			_, err = s.SetReviewStatus("m1", "reviewed", "test", r.ReviewRevision)
			return err
		},
		"effective_overlay": func(s *Store) error {
			_, err := s.SetEffectivePhases("m1", []json.RawMessage{json.RawMessage(`{"global_phase":"laning","start_game_second":0,"end_game_second":100}`)}, "test")
			return err
		},
	}
}

func authoritativeBytes(t *testing.T, s *Store) ([]byte, []byte) {
	t.Helper()
	reviewBytes, err := os.ReadFile(s.Path("m1"))
	if err != nil {
		t.Fatal(err)
	}
	auditBytes, err := os.ReadFile(s.AuditPath())
	if err != nil {
		t.Fatal(err)
	}
	return reviewBytes, auditBytes
}

func assertAuthoritativeBytes(t *testing.T, s *Store, wantReview, wantAudit []byte) {
	t.Helper()
	gotReview, gotAudit := authoritativeBytes(t, s)
	if string(gotReview) != string(wantReview) || string(gotAudit) != string(wantAudit) {
		t.Fatal("review/audit bytes diverged from the committed revision")
	}
}

func TestEveryReviewMutationFailedSecondPromotionRollsBack(t *testing.T) {
	for name, mutate := range atomicMutators() {
		t.Run(name, func(t *testing.T) {
			s, _ := seededAtomicStore(t)
			beforeReview, beforeAudit := authoritativeBytes(t, s)
			s.writeAtomic = func(path string, b []byte) error {
				if path == s.Path("m1") {
					return fmt.Errorf("injected failed second promotion")
				}
				return store.WriteAtomic(path, b)
			}
			if err := mutate(s); !IsStorageError(err) {
				t.Fatalf("err=%v want storage error", err)
			}
			assertAuthoritativeBytes(t, s, beforeReview, beforeAudit)
			if _, err := os.Stat(s.journalPath()); !os.IsNotExist(err) {
				t.Fatalf("completed rollback left journal: %v", err)
			}
		})
	}
}

func TestEveryReviewMutationFailedRollbackRecoversOnRestart(t *testing.T) {
	for name, mutate := range atomicMutators() {
		t.Run(name, func(t *testing.T) {
			s, root := seededAtomicStore(t)
			beforeReview, beforeAudit := authoritativeBytes(t, s)
			s.writeAtomic = func(path string, b []byte) error {
				if path == s.Path("m1") {
					return fmt.Errorf("injected failed second promotion")
				}
				return store.WriteAtomic(path, b)
			}
			s.recoveryAtomic = func(string, []byte) error { return fmt.Errorf("injected failed rollback") }
			if err := mutate(s); !IsStorageError(err) {
				t.Fatalf("err=%v want storage error", err)
			}
			if _, err := os.Stat(s.journalPath()); err != nil {
				t.Fatalf("durable recovery journal missing: %v", err)
			}
			restarted, err := New(root)
			if err != nil {
				t.Fatalf("restart recovery: %v", err)
			}
			assertAuthoritativeBytes(t, restarted, beforeReview, beforeAudit)
			if _, err := os.Stat(restarted.journalPath()); !os.IsNotExist(err) {
				t.Fatalf("restart recovery left journal: %v", err)
			}
		})
	}
}

func TestEveryReviewMutationCorruptOrUnwritableAuditIsAtomic(t *testing.T) {
	for name, mutate := range atomicMutators() {
		t.Run(name+"_corrupt", func(t *testing.T) {
			s, _ := seededAtomicStore(t)
			corrupt := []byte(`{"broken"`)
			if err := os.WriteFile(s.AuditPath(), corrupt, 0o644); err != nil {
				t.Fatal(err)
			}
			beforeReview, _ := os.ReadFile(s.Path("m1"))
			if err := mutate(s); !IsStorageError(err) {
				t.Fatalf("err=%v want storage error", err)
			}
			assertAuthoritativeBytes(t, s, beforeReview, corrupt)
		})
		t.Run(name+"_unwritable", func(t *testing.T) {
			s, _ := seededAtomicStore(t)
			beforeReview, beforeAudit := authoritativeBytes(t, s)
			s.writeAtomic = func(path string, b []byte) error {
				if path == s.AuditPath() {
					return fmt.Errorf("injected unwritable audit")
				}
				return store.WriteAtomic(path, b)
			}
			if err := mutate(s); !IsStorageError(err) {
				t.Fatalf("err=%v want storage error", err)
			}
			assertAuthoritativeBytes(t, s, beforeReview, beforeAudit)
		})
	}
}

func TestEveryReviewMutationJournalDirectorySyncFailureIsAtomic(t *testing.T) {
	for name, mutate := range atomicMutators() {
		t.Run(name, func(t *testing.T) {
			s, root := seededAtomicStore(t)
			beforeReview, beforeAudit := authoritativeBytes(t, s)
			calls := 0
			s.removeAtomicLog = func(path string) error {
				calls++
				if calls == 1 {
					// Model unlink succeeding but its parent-directory fsync
					// failing. commitReviewAndAudit must not acknowledge or
					// leave the promoted pair split.
					if err := os.Remove(path); err != nil {
						return err
					}
					return fmt.Errorf("injected journal parent sync failure")
				}
				return store.RemoveDurable(path)
			}
			if err := mutate(s); !IsStorageError(err) {
				t.Fatalf("err=%v want storage error", err)
			}
			assertAuthoritativeBytes(t, s, beforeReview, beforeAudit)
			restarted, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			assertAuthoritativeBytes(t, restarted, beforeReview, beforeAudit)
		})
	}
}

func TestReviewStatusStaleRevisionIsMutationFree(t *testing.T) {
	s, _ := seededAtomicStore(t)
	current, _ := s.Load("m1")
	staleRevision := current.ReviewRevision
	if _, err := s.SetReviewStatus("m1", "reviewed", "a", staleRevision); err != nil {
		t.Fatal(err)
	}
	beforeReview, beforeAudit := authoritativeBytes(t, s)
	if _, err := s.SetReviewStatus("m1", "pending", "b", staleRevision); !IsStale(err) {
		t.Fatalf("err=%v want stale", err)
	}
	assertAuthoritativeBytes(t, s, beforeReview, beforeAudit)
}

func TestReviewCrashBarrierChild(t *testing.T) {
	root := os.Getenv("DOT75_REVIEW_CRASH_ROOT")
	if root == "" {
		return
	}
	barrier := os.Getenv("DOT75_REVIEW_CRASH_BARRIER")
	mutation := os.Getenv("DOT75_REVIEW_CRASH_MUTATION")
	mode := os.Getenv("DOT75_REVIEW_CRASH_MODE")
	crash := func(name string) {
		if name == barrier {
			os.Exit(77)
		}
	}
	var s *Store
	var err error
	if mode == "recovery" {
		s = &Store{Root: root, crashBarrier: crash}
		err = s.recoverPending()
	} else {
		s, err = New(root)
		if err == nil {
			s.crashBarrier = crash
		}
	}
	if err != nil {
		t.Fatalf("child open/recover: %v", err)
	}
	if mode == "recovery" {
		t.Fatalf("recovery barrier %q was not reached", barrier)
	}
	mutate := atomicMutators()[mutation]
	if mutate == nil {
		t.Fatalf("unknown mutation %q", mutation)
	}
	if mode == "rollback" {
		s.writeAtomic = func(path string, b []byte) error {
			if path == s.Path("m1") {
				return fmt.Errorf("subprocess failed second promotion")
			}
			return store.WriteAtomic(path, b)
		}
	}
	_ = mutate(s)
	if barrier != "" {
		t.Fatalf("commit/rollback barrier %q was not reached", barrier)
	}
}

func runCrashChild(t *testing.T, root, mutation, mode, barrier string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewCrashBarrierChild$")
	cmd.Env = append(os.Environ(),
		"DOT75_REVIEW_CRASH_ROOT="+root,
		"DOT75_REVIEW_CRASH_MUTATION="+mutation,
		"DOT75_REVIEW_CRASH_MODE="+mode,
		"DOT75_REVIEW_CRASH_BARRIER="+barrier,
	)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 77 {
		t.Fatalf("child mode=%s mutation=%s barrier=%s: err=%v", mode, mutation, barrier, err)
	}
}

func assertFullyCommittedMutation(t *testing.T, s *Store, mutation string, beforeReview, beforeAudit []byte) {
	t.Helper()
	gotReview, gotAudit := authoritativeBytes(t, s)
	if string(gotReview) == string(beforeReview) || string(gotAudit) == string(beforeAudit) {
		t.Fatalf("%s did not promote both authoritative documents", mutation)
	}
	var audit Audit
	if err := json.Unmarshal(gotAudit, &audit); err != nil || len(audit.Entries) != 2 {
		t.Fatalf("%s audit is not the fully committed pair: entries=%d err=%v", mutation, len(audit.Entries), err)
	}
	var review Review
	if err := json.Unmarshal(gotReview, &review); err != nil || review.ReviewRevision == "" {
		t.Fatalf("%s review is not the fully committed pair: revision=%q err=%v", mutation, review.ReviewRevision, err)
	}
	switch mutation {
	case "phase_op":
		if len(review.PhaseCorrections) != 2 {
			t.Fatalf("phase operation not committed: %+v", review.PhaseCorrections)
		}
	case "review_status":
		if review.ReviewStatus != "reviewed" {
			t.Fatalf("review status=%q", review.ReviewStatus)
		}
	case "effective_overlay":
		if len(review.EffectivePhaseIntervals) != 1 {
			t.Fatalf("effective overlay not committed: %+v", review.EffectivePhaseIntervals)
		}
	}
}

func TestReviewCrashBarrierSubprocessMatrix(t *testing.T) {
	commitBarriers := []string{
		"commit_after_durable_prepare",
		"commit_after_audit_promotion",
		"commit_after_review_promotion",
		"commit_before_journal_remove",
		"commit_after_journal_remove",
	}
	rollbackBarriers := []string{
		"rollback_begin",
		"rollback_after_review_restore",
		"rollback_after_audit_restore",
		"rollback_before_journal_remove",
		"rollback_after_journal_remove",
	}
	recoveryBarriers := []string{
		"recovery_begin",
		"recovery_after_review_restore",
		"recovery_after_audit_restore",
		"recovery_before_journal_remove",
		"recovery_after_journal_remove",
	}
	for mutation := range atomicMutators() {
		mutation := mutation
		t.Run(mutation, func(t *testing.T) {
			for _, barrier := range commitBarriers {
				barrier := barrier
				t.Run(barrier, func(t *testing.T) {
					s, root := seededAtomicStore(t)
					beforeReview, beforeAudit := authoritativeBytes(t, s)
					runCrashChild(t, root, mutation, "commit", barrier)
					restarted, err := New(root)
					if err != nil {
						t.Fatal(err)
					}
					if barrier == "commit_after_journal_remove" {
						assertFullyCommittedMutation(t, restarted, mutation, beforeReview, beforeAudit)
					} else {
						assertAuthoritativeBytes(t, restarted, beforeReview, beforeAudit)
					}
				})
			}
			for _, barrier := range rollbackBarriers {
				barrier := barrier
				t.Run(barrier, func(t *testing.T) {
					s, root := seededAtomicStore(t)
					beforeReview, beforeAudit := authoritativeBytes(t, s)
					runCrashChild(t, root, mutation, "rollback", barrier)
					restarted, err := New(root)
					if err != nil {
						t.Fatal(err)
					}
					assertAuthoritativeBytes(t, restarted, beforeReview, beforeAudit)
				})
			}
			for _, barrier := range recoveryBarriers {
				barrier := barrier
				t.Run(barrier, func(t *testing.T) {
					s, root := seededAtomicStore(t)
					beforeReview, beforeAudit := authoritativeBytes(t, s)
					// Crash at rollback start to leave a durable prepared journal
					// and a split promotion for the recovery subprocess to repair.
					runCrashChild(t, root, mutation, "rollback", "rollback_begin")
					runCrashChild(t, root, mutation, "recovery", barrier)
					restarted, err := New(root)
					if err != nil {
						t.Fatal(err)
					}
					assertAuthoritativeBytes(t, restarted, beforeReview, beforeAudit)
				})
			}
			// A returned mutation is acknowledged only after durable journal
			// removal. Restart must preserve that fully committed pair.
			s, root := seededAtomicStore(t)
			beforeReview, beforeAudit := authoritativeBytes(t, s)
			if err := atomicMutators()[mutation](s); err != nil {
				t.Fatalf("acknowledged mutation: %v", err)
			}
			restarted, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			assertFullyCommittedMutation(t, restarted, mutation, beforeReview, beforeAudit)
		})
	}
}

// TestPhaseOverlayTypedOps exercises accept/move/relabel/add/delete/split/
// merge as atomic partition transforms against the current effective stream
// (the v2 contract), with exact [0, eligible] coverage enforced at every step.
func TestPhaseOverlayTypedOps(t *testing.T) {
	machine := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	eligible := 900
	base := append([]PhaseInterval(nil), machine...)

	// accept: records acceptance without changing the stream.
	out, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{Op: OpAccept, EventRef: "interval@0-300", EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("accept changed the stream: %+v", out)
	}

	// relabel: changes only the selected label; machine untouched.
	out, err = (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@600-900", EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "midgame"},
	})
	if err != nil {
		t.Fatalf("relabel: %v", err)
	}
	if out[2].GlobalPhase != "midgame" {
		t.Fatalf("relabel phase=%s", out[2].GlobalPhase)
	}
	if machine[2].GlobalPhase != "decisive" {
		t.Fatalf("machine mutated: %+v", machine[2])
	}

	// split: divides the laning interval at 150.
	out, err = (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@0-300", SplitSecond: intPtr(150), EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(out) != 4 || out[0].EndGameSecond != 150 || out[1].StartGameSecond != 150 {
		t.Fatalf("split result: %+v", out)
	}
	// Deterministic canonical refs on the split halves.
	if out[0].EventRef != "interval@0-150" || out[1].EventRef != "interval@150-300" {
		t.Fatalf("split canonical refs: %+v", out[:2])
	}

	// move: change one shared boundary, adjusting both neighbors.
	moveBase := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 150, GlobalPhase: "laning"},
		{StartGameSecond: 150, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	out, err = (&PhaseOverlay{Base: moveBase}).Apply(PhaseOpReq{
		Op: OpMove, EventRef: "interval@150-300", EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 150, EndGameSecond: 250, GlobalPhase: "laning"},
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if out[1].EndGameSecond != 250 || out[2].StartGameSecond != 250 {
		t.Fatalf("move did not adjust both neighbors: %+v", out)
	}

	// add: subdivide an already covered range.
	out, err = (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpAdd, EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 400, EndGameSecond: 500, GlobalPhase: "decisive"},
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(out) != 5 || out[2].GlobalPhase != "decisive" {
		t.Fatalf("add result: %+v", out)
	}

	// delete: absorbs into an explicitly selected adjacent interval.
	out, err = (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpDelete, EventRef: "interval@600-900", AbsorbInto: "interval@300-600", EligibleSeconds: eligible,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(out) != 2 || out[1].StartGameSecond != 300 || out[1].EndGameSecond != 900 || out[1].GlobalPhase != "midgame" {
		t.Fatalf("delete absorb result: %+v", out)
	}

	// merge: join two adjacent intervals with an explicit deterministic label.
	mergeBase := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 450, GlobalPhase: "midgame"},
		{StartGameSecond: 450, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	out, err = (&PhaseOverlay{Base: mergeBase}).Apply(PhaseOpReq{
		Op: OpMerge, EventRef: "interval@300-450", MergeRight: "interval@450-600",
		Effective: &PhaseInterval{GlobalPhase: "decisive"}, EligibleSeconds: eligible,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if out[1].StartGameSecond != 300 || out[1].EndGameSecond != 600 || out[1].GlobalPhase != "decisive" {
		t.Fatalf("merge result: %+v", out)
	}
}

// TestPhaseOverlayCumulativeChain proves operations compose against the prior
// effective stream: each op is applied to the previous op's result, and
// coverage stays exactly [0, eligible].
func TestPhaseOverlayCumulativeChain(t *testing.T) {
	machine := []PhaseInterval{
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
	eligible := 2705
	ov := &PhaseOverlay{Base: machine}
	cur := machine

	// 1. split interval@0-594 at 100.
	cur, err := ov.Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@0-594", SplitSecond: intPtr(100), EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("step1 split: %v", err)
	}
	assertCoverage(t, cur, eligible)
	// The split child must be addressable on the current stream.
	if _, ok := findStart(cur, "interval@100-594"); !ok {
		t.Fatalf("split child missing on current stream: %+v", cur[:3])
	}

	// 2. relabel the resulting current interval.
	ov2 := &PhaseOverlay{Base: cur}
	cur, err = ov2.Apply(PhaseOpReq{Op: OpRelabel, EventRef: "interval@100-594", EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 100, EndGameSecond: 594, GlobalPhase: "midgame"}})
	if err != nil {
		t.Fatalf("step2 relabel: %v", err)
	}
	assertCoverage(t, cur, eligible)

	// 3. move the shared boundary between the split child and its neighbor.
	ov3 := &PhaseOverlay{Base: cur}
	cur, err = ov3.Apply(PhaseOpReq{Op: OpMove, EventRef: "interval@100-594", EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 100, EndGameSecond: 700, GlobalPhase: "midgame"}})
	if err != nil {
		t.Fatalf("step3 move: %v", err)
	}
	assertCoverage(t, cur, eligible)

	// 4. add a bounded decisive interval inside an existing midgame interval.
	ov4 := &PhaseOverlay{Base: cur}
	cur, err = ov4.Apply(PhaseOpReq{Op: OpAdd, EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 1700, EndGameSecond: 1750, GlobalPhase: "decisive"}})
	if err != nil {
		t.Fatalf("step4 add: %v", err)
	}
	assertCoverage(t, cur, eligible)

	// 5. delete the added interval by absorbing into its left neighbor.
	ov5 := &PhaseOverlay{Base: cur}
	cur, err = ov5.Apply(PhaseOpReq{Op: OpDelete, EventRef: "interval@1700-1750", AbsorbInto: "interval@1609-1700", EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("step5 delete: %v", err)
	}
	assertCoverage(t, cur, eligible)

	// 6. split then merge adjacent intervals.
	ov6 := &PhaseOverlay{Base: cur}
	cur, err = ov6.Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@780-820", SplitSecond: intPtr(800), EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("step6 split: %v", err)
	}
	assertCoverage(t, cur, eligible)
	ov7 := &PhaseOverlay{Base: cur}
	cur, err = ov7.Apply(PhaseOpReq{Op: OpMerge, EventRef: "interval@780-800", MergeRight: "interval@800-820",
		Effective: &PhaseInterval{GlobalPhase: "midgame"}, EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("step7 merge: %v", err)
	}
	assertCoverage(t, cur, eligible)

	// 7. accept a current boundary (no change, still validated).
	ov8 := &PhaseOverlay{Base: cur}
	cur, err = ov8.Apply(PhaseOpReq{Op: OpAccept, EventRef: "interval@2454-2633", EligibleSeconds: eligible})
	if err != nil {
		t.Fatalf("step8 accept: %v", err)
	}
	assertCoverage(t, cur, eligible)
}

func assertCoverage(t *testing.T, intervals []PhaseInterval, eligible int) {
	t.Helper()
	if len(intervals) == 0 {
		t.Fatal("empty stream")
	}
	if intervals[0].StartGameSecond != 0 {
		t.Fatalf("coverage start=%d want 0", intervals[0].StartGameSecond)
	}
	if intervals[len(intervals)-1].EndGameSecond != eligible {
		t.Fatalf("coverage end=%d want %d", intervals[len(intervals)-1].EndGameSecond, eligible)
	}
	prevEnd := intervals[0].StartGameSecond
	for i := range intervals {
		iv := &intervals[i]
		if iv.StartGameSecond != prevEnd {
			t.Fatalf("gap at %d-%d", iv.StartGameSecond, iv.EndGameSecond)
		}
		prevEnd = iv.EndGameSecond
	}
}

// TestPhaseOverlayValidation locks the invalid-state rejections: empty and
// final-gap streams, overlap, zero width, illegal phase, laning re-entry, and
// malformed operations.
func TestPhaseOverlayValidation(t *testing.T) {
	base := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
	}
	eligible := 900

	// Bogus phase label.
	if _, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@300-600", EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "bogus"},
	}); err == nil {
		t.Fatal("bogus phase accepted")
	}
	// Relabel that changes boundaries is rejected.
	if _, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@300-600", EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 300, EndGameSecond: 650, GlobalPhase: "midgame"},
	}); err == nil {
		t.Fatal("relabel changing boundaries accepted")
	}
	// Add spanning a boundary (no single covering interval) is stale.
	if _, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpAdd, EligibleSeconds: eligible,
		Effective: &PhaseInterval{StartGameSecond: 200, EndGameSecond: 400, GlobalPhase: "midgame"},
	}); !IsStale(err) {
		t.Fatalf("add spanning boundary err=%v want stale", err)
	}
	// Delete without an explicit absorber is rejected.
	if _, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{Op: OpDelete, EventRef: "interval@300-600", EligibleSeconds: eligible}); err == nil {
		t.Fatal("delete without absorber accepted")
	}
	// Delete with a non-adjacent absorber is rejected.
	if _, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpDelete, EventRef: "interval@300-600", AbsorbInto: "interval@0-300", EligibleSeconds: eligible,
	}); err == nil {
		t.Fatal("non-adjacent absorber accepted")
	}
	// Invalid split second.
	if _, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@0-300", SplitSecond: intPtr(301), EligibleSeconds: eligible}); err == nil {
		t.Fatal("invalid split accepted")
	}
	// Merge adjacent intervals with full coverage must succeed.
	fullBase := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	if _, err := (&PhaseOverlay{Base: fullBase}).Apply(PhaseOpReq{
		Op: OpMerge, EventRef: "interval@300-600", MergeRight: "interval@600-900", EligibleSeconds: eligible,
	}); err != nil {
		// 300-600 + 600-900 are adjacent and mergeable with full coverage.
		t.Fatalf("adjacent merge rejected: %v", err)
	}

	// Empty stream fails closed.
	if _, err := (&PhaseOverlay{Base: nil}).Apply(PhaseOpReq{Op: OpAccept, EventRef: "interval@0-300", EligibleSeconds: eligible}); err == nil {
		t.Fatal("empty stream accepted")
	}
	// Final-gap stream (does not reach eligible) is rejected even though its
	// maximum interval end is well-formed — validation must not infer success
	// from the max end alone.
	finalGap := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
	}
	if _, err := (&PhaseOverlay{Base: finalGap}).Apply(PhaseOpReq{Op: OpAccept, EventRef: "interval@300-600", EligibleSeconds: eligible}); err == nil {
		t.Fatal("final-gap stream accepted")
	}
}

// TestPhaseOverlayStreamInvariants locks validateStream directly for states no
// single operation can produce from a valid stream: overlap, zero width,
// first-start-not-zero, final-end-not-eligible, and laning re-entry.
func TestPhaseOverlayStreamInvariants(t *testing.T) {
	if err := validateStream([]PhaseInterval{}, 900); err == nil {
		t.Fatal("empty stream accepted")
	}
	overlap := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 400, GlobalPhase: "laning"},
		{StartGameSecond: 200, EndGameSecond: 600, GlobalPhase: "midgame"},
	}
	if err := validateStream(overlap, 900); err == nil {
		t.Fatal("overlap accepted")
	}
	zeroWidth := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 300, GlobalPhase: "midgame"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
	}
	if err := validateStream(zeroWidth, 900); err == nil {
		t.Fatal("zero width accepted")
	}
	notZeroStart := []PhaseInterval{
		{StartGameSecond: 10, EndGameSecond: 300, GlobalPhase: "laning"},
	}
	if err := validateStream(notZeroStart, 900); err == nil {
		t.Fatal("first start not zero accepted")
	}
	notEligibleEnd := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 600, GlobalPhase: "laning"},
	}
	if err := validateStream(notEligibleEnd, 900); err == nil {
		t.Fatal("final end not eligible accepted")
	}
	illegalPhase := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "bogus"},
	}
	if err := validateStream(illegalPhase, 600); err == nil {
		t.Fatal("illegal phase accepted")
	}
	laningReentry := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "laning"},
	}
	if err := validateStream(laningReentry, 900); err == nil {
		t.Fatal("laning re-entry accepted")
	}
	// midgame <-> decisive re-entry is permitted.
	reentry := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
		{StartGameSecond: 900, EndGameSecond: 1200, GlobalPhase: "midgame"},
		{StartGameSecond: 1200, EndGameSecond: 1500, GlobalPhase: "decisive"},
	}
	if err := validateStream(reentry, 1500); err != nil {
		t.Fatalf("midgame<->decisive re-entry rejected: %v", err)
	}
}

// TestPhaseOverlayStalePreconditions proves refs that no longer exist in the
// current stream fail closed with the stale sentinel (mapped to 409 by the
// API).
func TestPhaseOverlayStalePreconditions(t *testing.T) {
	base := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
	}
	eligible := 600
	stale := []PhaseOpReq{
		{Op: OpRelabel, EventRef: "interval@999-1000", EligibleSeconds: eligible,
			Effective: &PhaseInterval{StartGameSecond: 999, EndGameSecond: 1000, GlobalPhase: "midgame"}},
		{Op: OpRelabel, EventRef: "interval@300-999", EligibleSeconds: eligible,
			Effective: &PhaseInterval{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"}},
		{Op: OpDelete, EventRef: "interval@999-1000", AbsorbInto: "interval@300-600", EligibleSeconds: eligible},
		{Op: OpMerge, EventRef: "interval@0-300", MergeRight: "interval@999-1000", EligibleSeconds: eligible},
		{Op: OpSplit, EventRef: "interval@999-1000", SplitSecond: intPtr(50), EligibleSeconds: eligible},
		{Op: OpAccept, EventRef: "interval@999-1000", EligibleSeconds: eligible},
	}
	for i, req := range stale {
		_, err := (&PhaseOverlay{Base: base}).Apply(req)
		if !IsStale(err) {
			t.Fatalf("case %d err=%v want stale sentinel", i, err)
		}
	}
}

// TestPhaseOverlayMergeDeleteExplicitParameters proves the merge resulting
// label and the delete absorber are explicit and deterministic.
func TestPhaseOverlayMergeDeleteExplicitParameters(t *testing.T) {
	eligible := 900
	// merge defaults to the left interval's label when no explicit label.
	base := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 450, GlobalPhase: "midgame"},
		{StartGameSecond: 450, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	out, err := (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpMerge, EventRef: "interval@300-450", MergeRight: "interval@450-600", EligibleSeconds: eligible,
	})
	if err != nil {
		t.Fatalf("merge default label: %v", err)
	}
	if out[1].GlobalPhase != "midgame" {
		t.Fatalf("merge default label=%s want midgame", out[1].GlobalPhase)
	}
	// Explicit label wins.
	out, err = (&PhaseOverlay{Base: base}).Apply(PhaseOpReq{
		Op: OpMerge, EventRef: "interval@300-450", MergeRight: "interval@450-600",
		Effective: &PhaseInterval{GlobalPhase: "decisive"}, EligibleSeconds: eligible,
	})
	if err != nil {
		t.Fatalf("merge explicit label: %v", err)
	}
	if out[1].GlobalPhase != "decisive" {
		t.Fatalf("merge explicit label=%s want decisive", out[1].GlobalPhase)
	}
	// delete absorbing into the right neighbor keeps the right label.
	deleteBase := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	out, err = (&PhaseOverlay{Base: deleteBase}).Apply(PhaseOpReq{
		Op: OpDelete, EventRef: "interval@300-600", AbsorbInto: "interval@600-900", EligibleSeconds: eligible,
	})
	if err != nil {
		t.Fatalf("delete absorb right: %v", err)
	}
	if out[1].StartGameSecond != 300 || out[1].EndGameSecond != 900 || out[1].GlobalPhase != "decisive" {
		t.Fatalf("delete absorb right result: %+v", out)
	}
}

func intPtr(v int) *int { return &v }

func TestReviewDocumentMigrationFixtures(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Missing documents load as a fresh v2 review without inventing history.
	missing, err := s.Load("missing")
	if err != nil {
		t.Fatal(err)
	}
	if missing.SchemaVersion != version.CorrectionSchema || missing.ReviewStatus != "pending" ||
		len(missing.Corrections) != 0 || len(missing.PhaseCorrections) != 0 {
		t.Fatalf("missing fixture: %+v", missing)
	}

	matchID := "migrate1"
	machine := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 100, GlobalPhase: "laning"},
		{StartGameSecond: 100, EndGameSecond: 200, GlobalPhase: "midgame"},
	}
	phases, err := json.Marshal(map[string]interface{}{
		"rule_version": version.PhaseRuleVersion, "eligible_seconds": 200, "intervals": machine,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAtomic(filepath.Join(root, "matches", matchID, store.ArtifactPhases), phases); err != nil {
		t.Fatal(err)
	}
	phaseMachine := json.RawMessage(`{"start_game_second":0,"end_game_second":100,"global_phase":"laning"}`)
	phaseEffective := json.RawMessage(`{"start_game_second":0,"end_game_second":100,"global_phase":"midgame"}`)
	roleMachine := json.RawMessage(`{"role":5}`)
	legacy := Review{
		SchemaVersion: version.CorrectionSchemaV1,
		RuleVersion:   "ti2026.corrections.v1",
		MatchID:       matchID,
		ReviewStatus:  "in_progress",
		Corrections: []Correction{
			{
				ID: "corr-migrate1-0001", SchemaVersion: version.CorrectionSchemaV1,
				MatchID: matchID, Kind: KindPhaseInterval, Author: "paul", Reason: "legacy phase",
				AppliedAt: "2026-08-17T00:00:00Z", AlgorithmVersion: version.PhaseRuleVersion,
				PreviousValue: phaseMachine, EffectiveValue: phaseEffective, EventRef: "interval@0-100",
			},
			{
				ID: "corr-migrate1-0002", SchemaVersion: version.CorrectionSchemaV1,
				MatchID: matchID, Kind: KindRoleOverride, Author: "paul", Reason: "legacy role",
				AppliedAt: "2026-08-17T00:01:00Z", AlgorithmVersion: version.RoleSchema,
				PreviousValue: roleMachine, EffectiveValue: json.RawMessage(`{"role":4}`), EventRef: "player@42",
			},
		},
	}
	legacyJSON, err := json.Marshal(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAtomic(s.Path(matchID), legacyJSON); err != nil {
		t.Fatal(err)
	}

	migrated, err := s.MigrateV1(matchID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != version.CorrectionSchema || migrated.RuleVersion != version.CorrectionRuleVersion {
		t.Fatalf("migrated versions: schema=%q rule=%q", migrated.SchemaVersion, migrated.RuleVersion)
	}
	if len(migrated.Corrections) != 2 || len(migrated.PhaseCorrections) != 1 {
		t.Fatalf("migrated history: corrections=%d phase_corrections=%d", len(migrated.Corrections), len(migrated.PhaseCorrections))
	}
	if !jsonEqual(migrated.Corrections[0].PreviousValue, phaseMachine) ||
		!jsonEqual(migrated.Corrections[1].PreviousValue, roleMachine) {
		t.Fatal("v1 machine truth was not preserved")
	}
	pc := migrated.PhaseCorrections[0]
	if pc.Operation != "v1_migrated" || pc.ShapeVersion != "v1" || len(pc.BeforeStream) != 2 || len(pc.AfterStream) != 2 || len(pc.MachineValue) == 0 {
		t.Fatalf("migrated phase correction incomplete: %+v", pc)
	}
	if len(migrated.EffectivePhaseIntervals) != 2 {
		t.Fatalf("migrated effective stream=%d", len(migrated.EffectivePhaseIntervals))
	}

	// A persisted v2 document is idempotent: reopening/migrating it neither
	// loses nor duplicates prior corrections.
	v2Before, err := os.ReadFile(s.Path(matchID))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := reopened.MigrateV1(matchID)
	if err != nil {
		t.Fatal(err)
	}
	v2After, err := os.ReadFile(s.Path(matchID))
	if err != nil {
		t.Fatal(err)
	}
	if string(v2Before) != string(v2After) || len(v2.Corrections) != 2 || len(v2.PhaseCorrections) != 1 {
		t.Fatalf("v2 fixture was not idempotent: corrections=%d phase=%d bytes_equal=%v",
			len(v2.Corrections), len(v2.PhaseCorrections), string(v2Before) == string(v2After))
	}
}

func TestCorrectionSchemaVersion(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := json.RawMessage(`{"p":"x"}`)
	eff := json.RawMessage(`{"p":"y"}`)
	rv, err := s.AddAuthoritative("m1", Correction{MatchID: "m1", Kind: KindRoleOverride, Author: "paul", Reason: "r", PreviousValue: prev, EffectiveValue: eff},
		MachineTruth{AlgorithmVersion: "ti2026.roles.v1", MachineValue: prev})
	if err != nil {
		t.Fatal(err)
	}
	if rv.SchemaVersion != "replay.corrections.v2" {
		t.Fatalf("schema=%s", rv.SchemaVersion)
	}
	if !strings.Contains(rv.Corrections[0].ID, "m1") {
		t.Fatalf("id=%s", rv.Corrections[0].ID)
	}
}

// TestReviewRevisionAdvancesAndRejectsStale proves the optimistic-concurrency
// revision advances on every correction (including accept, which does not
// change boundaries) and that a stale revision — even for a same-boundary
// relabel whose interval still exists — is rejected atomically with the stale
// sentinel and changes no bytes or counts.
func TestReviewRevisionAdvancesAndRejectsStale(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	machine := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	ctx := PhaseContext{EligibleSeconds: 900, RuleVersion: "ti2026.phase.v1", ReplaySHA256: "abc", MachineIntervals: machine}

	// The initial revision is deterministic and stable across loads.
	r0, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if r0.ReviewRevision == "" {
		t.Fatal("initial revision empty")
	}
	r0b, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if r0.ReviewRevision != r0b.ReviewRevision {
		t.Fatal("revision not stable across loads")
	}
	// The revision is a required mutation precondition, not an optional hint.
	if _, err := s.ApplyPhaseOp("m1", ctx, PhaseOpReq{Op: OpAccept, EventRef: "interval@600-900"}); !IsStale(err) {
		t.Fatalf("missing review revision err=%v want stale sentinel", err)
	}

	// Reviewer A relabels interval@2454... (no: use 600-900 decisive) -> midgame
	// with the initial revision.
	relabel := PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@600-900", EligibleSeconds: 900,
		Effective:        &PhaseInterval{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "midgame"},
		ExpectedRevision: r0.ReviewRevision,
	}
	ra, err := s.ApplyPhaseOp("m1", ctx, relabel)
	if err != nil {
		t.Fatal(err)
	}
	if ra.ReviewRevision == r0.ReviewRevision {
		t.Fatal("revision did not advance after correction")
	}

	// Reviewer B submits a stale form for the SAME still-existing boundary with
	// the OLD revision: must fail closed with the stale sentinel, no bytes
	// change.
	stale := PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@600-900", EligibleSeconds: 900,
		Effective:        &PhaseInterval{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
		ExpectedRevision: r0.ReviewRevision,
	}
	path := s.Path("m1")
	before, _ := os.ReadFile(path)
	if _, err := s.ApplyPhaseOp("m1", ctx, stale); !IsStale(err) {
		t.Fatalf("stale same-boundary relabel err=%v want stale sentinel", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("stale revision changed persisted state")
	}
	// Still 1 correction (reviewer A's), audit still 1 entry.
	rl, _ := s.Load("m1")
	if len(rl.PhaseCorrections) != 1 {
		t.Fatalf("corrections=%d want 1", len(rl.PhaseCorrections))
	}
	aud, _ := s.LoadAudit()
	if len(aud.Entries) != 1 {
		t.Fatalf("audit entries=%d want 1", len(aud.Entries))
	}

	// Reviewer A accepts the same still-existing boundary (600-900 midgame)
	// with the CURRENT revision; accept appends a correction and advances the
	// revision even though no boundary changed.
	accept := PhaseOpReq{Op: OpAccept, EventRef: "interval@600-900", EligibleSeconds: 900, ExpectedRevision: ra.ReviewRevision}
	rc, err := s.ApplyPhaseOp("m1", ctx, accept)
	if err != nil {
		t.Fatal(err)
	}
	if rc.ReviewRevision == ra.ReviewRevision {
		t.Fatal("revision did not advance after accept")
	}
	if len(rc.PhaseCorrections) != 2 {
		t.Fatalf("corrections=%d want 2 after accept", len(rc.PhaseCorrections))
	}
	// Same-boundary accept with a stale revision is rejected atomically.
	before2, _ := os.ReadFile(path)
	if _, err := s.ApplyPhaseOp("m1", ctx, PhaseOpReq{Op: OpAccept, EventRef: "interval@600-900", EligibleSeconds: 900, ExpectedRevision: ra.ReviewRevision}); !IsStale(err) {
		t.Fatalf("stale same-boundary accept err=%v want stale", err)
	}
	after2, _ := os.ReadFile(path)
	if string(before2) != string(after2) {
		t.Fatal("stale accept changed persisted state")
	}

	// Restart reproduces the same revision from persisted ordered state.
	s2, _ := New(s.Root)
	rr, err := s2.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if rr.ReviewRevision != rc.ReviewRevision {
		t.Fatalf("restart revision=%s want %s", rr.ReviewRevision, rc.ReviewRevision)
	}
}
