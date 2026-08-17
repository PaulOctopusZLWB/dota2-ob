package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAddCorrectionPreservesMachineValue(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := json.RawMessage(`{"global_phase":"laning","start":0,"end":600}`)
	eff := json.RawMessage(`{"global_phase":"midgame","start":0,"end":600}`)
	rv, err := s.Add("m1", Correction{
		MatchID: "m1", Kind: KindPhaseInterval, Author: "paul",
		Reason: "boundary moved on evidence", PreviousValue: prev, EffectiveValue: eff,
	})
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
	if c.AlgorithmVersion == "" {
		t.Fatal("algorithm version empty")
	}
	if c.ID == "" {
		t.Fatal("correction id empty")
	}
	// Machine value preserved verbatim.
	if string(c.PreviousValue) != `{"global_phase":"laning","start":0,"end":600}` {
		t.Fatalf("previous value changed: %s", c.PreviousValue)
	}
	// Reload persists.
	loaded, err := s.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Corrections) != 1 || loaded.ReviewStatus != "in_progress" {
		t.Fatalf("reload: %+v", loaded)
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
		if _, err := s.Add("m1", Correction{
			MatchID: "m1", Kind: KindEvent, Author: "paul", Reason: "r",
			PreviousValue: prev, EffectiveValue: eff,
		}); err != nil {
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
	// Newest first; seq monotonically increasing.
	if audit.Entries[0].Seq != 3 || audit.Entries[2].Seq != 1 {
		t.Fatalf("audit order wrong: %d,%d", audit.Entries[0].Seq, audit.Entries[2].Seq)
	}
	// Every audit entry must carry a real applied timestamp.
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

func TestCorrectionSchemaVersion(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := json.RawMessage(`{"p":"x"}`)
	eff := json.RawMessage(`{"p":"y"}`)
	rv, err := s.Add("m1", Correction{MatchID: "m1", Kind: KindRoleOverride, Author: "paul", Reason: "r", PreviousValue: prev, EffectiveValue: eff})
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
