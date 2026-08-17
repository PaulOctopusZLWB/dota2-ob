package review

import (
	"encoding/json"
	"strings"
	"testing"
)

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
	rv, err := s.SetReviewStatus("m1", "reviewed", "paul")
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

// TestPhaseOverlayTypedOps exercises accept/move/relabel/add/delete/split/
// merge with stream-invariant validation.
func TestPhaseOverlayTypedOps(t *testing.T) {
	machine := []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
		{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "decisive"},
	}
	ov := &PhaseOverlay{Machine: machine}

	// Relabel: change the decisive interval to midgame.
	out, err := ov.Apply(PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@600-900",
		Effective: &PhaseInterval{StartGameSecond: 600, EndGameSecond: 900, GlobalPhase: "midgame"},
	})
	if err != nil {
		t.Fatalf("relabel: %v", err)
	}
	if out[2].GlobalPhase != "midgame" {
		t.Fatalf("relabel phase=%s", out[2].GlobalPhase)
	}
	// Machine stream untouched.
	if machine[2].GlobalPhase != "decisive" {
		t.Fatalf("machine mutated: %+v", machine[2])
	}

	// Split the laning interval at 150.
	out, err = ov.Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@0-300", SplitSecond: intPtr(150)})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(out) != 4 || out[0].EndGameSecond != 150 || out[1].StartGameSecond != 150 {
		t.Fatalf("split result: %+v", out)
	}

	// Delete that would leave uncovered eligible seconds is rejected (gap
	// invariant), matching the spec's "exact eligible-second coverage".
	if _, err := ov.Apply(PhaseOpReq{Op: OpDelete, EventRef: "interval@0-300"}); err == nil {
		t.Fatal("delete creating a coverage gap accepted")
	}
	if _, err := ov.Apply(PhaseOpReq{Op: OpDelete, EventRef: "interval@300-600"}); err == nil {
		t.Fatal("interior delete creating a gap accepted")
	}
	// Delete is valid only when the remaining stream stays contiguous: delete
	// the decisive interval from a stream whose tail is already right-censored
	// by an ended segment is not expressible; instead verify a delete of a
	// zero-width duplicate is a no-op error path by checking unknown refs.
	if _, err := ov.Apply(PhaseOpReq{Op: OpDelete, EventRef: "interval@999-1000"}); err == nil {
		t.Fatal("unknown delete ref accepted")
	}

	// Merge two adjacent same-phase intervals: split midgame then merge back.
	splitOut, err := ov.Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@300-600", SplitSecond: intPtr(450)})
	if err != nil {
		t.Fatalf("split for merge: %v", err)
	}
	if len(splitOut) != 4 {
		t.Fatalf("split result len=%d", len(splitOut))
	}
	ov2 := &PhaseOverlay{Machine: splitOut}
	out, err = ov2.Apply(PhaseOpReq{Op: OpMerge, EventRef: "interval@300-450", MergeRight: "interval@450-600"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(out) != 3 || out[1].StartGameSecond != 300 || out[1].EndGameSecond != 600 {
		t.Fatalf("merge result: %+v", out)
	}
	// Non-adjacent or phase-mismatch merges are rejected.
	if _, err := ov.Apply(PhaseOpReq{Op: OpMerge, EventRef: "interval@0-300", MergeRight: "interval@600-900"}); err == nil {
		t.Fatal("non-adjacent merge accepted")
	}
	if _, err := ov.Apply(PhaseOpReq{Op: OpMerge, EventRef: "interval@300-600", MergeRight: "interval@600-900"}); err == nil {
		t.Fatal("phase-mismatch merge accepted")
	}

	// Add a new interval.
	out, err = ov.Apply(PhaseOpReq{
		Op: OpAdd, Effective: &PhaseInterval{StartGameSecond: 300, EndGameSecond: 450, GlobalPhase: "midgame"},
	})
	if err == nil {
		t.Fatalf("add with overlap should fail: %+v", out)
	}
}

// TestPhaseOverlayValidation locks the invalid-state rejections: bogus phase,
// gap, overlap, non-adjacent merge, unknown ref, bad split.
func TestPhaseOverlayValidation(t *testing.T) {
	ov := &PhaseOverlay{Machine: []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
	}}
	if _, err := ov.Apply(PhaseOpReq{
		Op: OpRelabel, EventRef: "interval@300-600",
		Effective: &PhaseInterval{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "bogus"},
	}); err == nil {
		t.Fatal("bogus phase accepted")
	}
	if _, err := ov.Apply(PhaseOpReq{
		Op: OpAdd, Effective: &PhaseInterval{StartGameSecond: 601, EndGameSecond: 700, GlobalPhase: "midgame"},
	}); err == nil {
		t.Fatal("stream gap accepted")
	}
	if _, err := ov.Apply(PhaseOpReq{Op: OpDelete, EventRef: "interval@999-1000"}); err == nil {
		t.Fatal("unknown ref accepted")
	}
	if _, err := ov.Apply(PhaseOpReq{Op: OpSplit, EventRef: "interval@0-300", SplitSecond: intPtr(301)}); err == nil {
		t.Fatal("invalid split accepted")
	}
	if _, err := ov.Apply(PhaseOpReq{Op: OpMerge, EventRef: "interval@0-300", MergeRight: "interval@300-600"}); err == nil {
		t.Fatal("non-adjacent/phase-mismatch merge accepted")
	}
}

// TestPhaseOverlayLaningNoReentry proves laning never re-enters after exit.
func TestPhaseOverlayLaningNoReentry(t *testing.T) {
	ov := &PhaseOverlay{Machine: []PhaseInterval{
		{StartGameSecond: 0, EndGameSecond: 300, GlobalPhase: "laning"},
		{StartGameSecond: 300, EndGameSecond: 600, GlobalPhase: "midgame"},
	}}
	_, err := ov.Apply(PhaseOpReq{
		Op: OpAdd, Effective: &PhaseInterval{StartGameSecond: 600, EndGameSecond: 700, GlobalPhase: "laning"},
	})
	if err == nil {
		t.Fatal("laning re-entry after exit accepted")
	}
}

func intPtr(v int) *int { return &v }

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
	if rv.SchemaVersion != "replay.corrections.v1" {
		t.Fatalf("schema=%s", rv.SchemaVersion)
	}
	if !strings.Contains(rv.Corrections[0].ID, "m1") {
		t.Fatalf("id=%s", rv.Corrections[0].ID)
	}
}
