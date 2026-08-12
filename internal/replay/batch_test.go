package replay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

func fixedFacts() *ReplayFactsV1 {
	return BuildFacts(&Collected{
		GameBuild:     6896,
		ServerName:    "srv",
		MaxTimestamp:  10,
		MessageCounts: map[string]uint64{"CDemoPacket": 1},
		Combat: []CombatEvent{
			{Type: "DOTA_COMBATLOG_FIRST_BLOOD", Timestamp: 1, Attacker: "npc_dota_hero_a", Target: "npc_dota_hero_b"},
		},
	})
}

func writeDemFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRunner(t *testing.T, parse ParseFunc) (*Runner, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	factsDir := filepath.Join(dir, "facts")
	r := &Runner{
		StatePath:  state,
		FactsDir:   factsDir,
		MaxRetries:  1,
		Parse:      parse,
		Now:        func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	return r, dir, state, factsDir
}

func TestBatchSucceedsAndPersistsFacts(t *testing.T) {
	calls := 0
	r, dir, statePath, factsDir := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A", Metrics: Metrics{Outcome: "ok"}}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")

	st, err := r.Run(&Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.Entries["m1"].Status != StatusSucceeded {
		t.Fatalf("status %v want succeeded", st.Entries["m1"])
	}
	if st.Entries["m1"].FactsHash != "HASH_A" {
		t.Fatalf("facts hash %v", st.Entries["m1"].FactsHash)
	}
	if calls != 1 {
		t.Fatalf("parse calls %d want 1", calls)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state not persisted: %v", err)
	}
	entries, _ := os.ReadDir(factsDir)
	if len(entries) != 1 {
		t.Fatalf("facts files %d want 1", len(entries))
	}
}

func TestBatchResumeSkipsSucceededNoDuplicateWork(t *testing.T) {
	calls := 0
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	if _, err := r.Run(m); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(m); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("parse calls %d want 1 (resume must not reparse succeeded)", calls)
	}
}

func TestBatchDuplicateInputSharesOneFactsArtifact(t *testing.T) {
	calls := 0
	r, dir, _, factsDir := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "shared-dem-bytes")
	m := &Manifest{Entries: []ManifestEntry{
		{MatchID: "m1", DemPath: dem},
		{MatchID: "m1_dup", DemPath: dem},
	}}

	st, err := r.Run(m)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("parse calls %d want 1 (dedupe by content)", calls)
	}
	if st.Entries["m1"].Status != StatusSucceeded || st.Entries["m1_dup"].Status != StatusSucceeded {
		t.Fatalf("both should succeed: %+v", st.Entries)
	}
	if st.Entries["m1"].ContentSHA256 != st.Entries["m1_dup"].ContentSHA256 {
		t.Fatalf("content sha should match")
	}
	if st.Entries["m1_dup"].FactsHash != st.Entries["m1"].FactsHash {
		t.Fatalf("dup should share facts hash")
	}
	entries, _ := os.ReadDir(factsDir)
	if len(entries) != 1 {
		t.Fatalf("facts files %d want 1 (no duplicate artifact)", len(entries))
	}
}

func TestBatchParseFailureTerminalAfterRetries(t *testing.T) {
	calls := 0
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Metrics: Metrics{Outcome: "parse_error", ParseError: "boom"}}, errBoom
	})
	r.MaxRetries = 2
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bad")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	st1, err := r.Run(m)
	if err != nil {
		t.Fatalf("first run should not error before retries exhausted: %v", err)
	}
	if st1.Entries["m1"].Status != StatusQueued {
		t.Fatalf("status %v want queued after first attempt", st1.Entries["m1"])
	}
	if st1.Entries["m1"].Attempts != 1 {
		t.Fatalf("attempts %d want 1", st1.Entries["m1"].Attempts)
	}
	if calls != 1 {
		t.Fatalf("calls %d want 1", calls)
	}

	_, err = r.Run(m)
	if err == nil {
		t.Fatalf("expected terminal error after retries exhausted")
	}
	st, _ := LoadState(r.StatePath)
	if st.Entries["m1"].Status != StatusFailedTerminal {
		t.Fatalf("status %v want failed_terminal", st.Entries["m1"])
	}
	if st.Entries["m1"].Attempts != 2 {
		t.Fatalf("attempts %d want 2", st.Entries["m1"].Attempts)
	}
	if st.Entries["m1"].LastError != "boom" {
		t.Fatalf("last error %v want boom", st.Entries["m1"].LastError)
	}
	if calls != 2 {
		t.Fatalf("calls %d want 2", calls)
	}

	third, err := r.Run(m)
	if err != nil {
		t.Fatalf("rerun over terminal should not error: %v", err)
	}
	if third.Entries["m1"].Status != StatusFailedTerminal {
		t.Fatalf("terminal must not auto-retry: %+v", third.Entries["m1"])
	}
	if calls != 2 {
		t.Fatalf("terminal must not be retried, calls %d", calls)
	}
}

func TestBatchRetryAllReprocesses(t *testing.T) {
	calls := 0
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}
	if _, err := r.Run(m); err != nil {
		t.Fatal(err)
	}
	r.RetryAll = true
	if _, err := r.Run(m); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("parse calls %d want 2 with RetryAll", calls)
	}
}

func TestBatchRunIsIdempotentAcrossRestore(t *testing.T) {
	calls := 0
	makeRunner := func(state string) *Runner {
		return &Runner{
			StatePath: state, FactsDir: filepath.Join(t.TempDir(), "f"),
			MaxRetries: 1, Parse: func(p string) (*ParseResult, error) {
				calls++
				return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
			}, Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
		}
	}

	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	if _, err := makeRunner(state).Run(m); err != nil {
		t.Fatal(err)
	}
	if _, err := makeRunner(state).Run(m); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("restore from persisted state must not reparse, calls %d", calls)
	}
}