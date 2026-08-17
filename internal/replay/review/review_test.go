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

func TestApplyPhaseOverlay(t *testing.T) {
	machine := []json.RawMessage{
		json.RawMessage(`{"event_ref":"interval@0-600","global_phase":"laning"}`),
		json.RawMessage(`{"event_ref":"interval@600-1200","global_phase":"midgame"}`),
	}
	eff := json.RawMessage(`{"event_ref":"interval@0-600","global_phase":"decisive"}`)
	corrections := []Correction{
		{Kind: KindPhaseInterval, EventRef: "interval@0-600", EffectiveValue: eff},
	}
	out := ApplyPhaseOverlay(machine, corrections)
	if len(out) != 2 {
		t.Fatalf("overlay len=%d", len(out))
	}
	if !strings.Contains(string(out[0]), "decisive") {
		t.Fatalf("overlay not applied: %s", out[0])
	}
	// Machine output untouched.
	if string(machine[0]) != `{"event_ref":"interval@0-600","global_phase":"laning"}` {
		t.Fatalf("machine mutated: %s", machine[0])
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
	if rv.SchemaVersion != "replay.corrections.v1" {
		t.Fatalf("schema=%s", rv.SchemaVersion)
	}
	if !strings.Contains(rv.Corrections[0].ID, "m1") {
		t.Fatalf("id=%s", rv.Corrections[0].ID)
	}
}
