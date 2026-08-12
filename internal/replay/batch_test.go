package replay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
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
		MaxRetries: 1,
		Parse:      parse,
		Now:        func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	return r, dir, state, factsDir
}

// factsFiles returns the persisted "<sha>.facts.json" artifact count, ignoring
// the matching "<sha>.provenance.json" sidecar that is written alongside it.
func factsFiles(t *testing.T, factsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(factsDir)
	if err != nil {
		t.Fatalf("read facts dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".facts.json") {
			out = append(out, e.Name())
		}
	}
	return out
}

func sidecarFiles(t *testing.T, factsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(factsDir)
	if err != nil {
		t.Fatalf("read facts dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".provenance.json") {
			out = append(out, e.Name())
		}
	}
	return out
}

func runNoErr(t *testing.T, r *Runner, m *Manifest) *BatchState {
	t.Helper()
	st, errs := r.Run(m)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	return st
}

func TestBatchSucceedsAndPersistsFactsAndSidecar(t *testing.T) {
	calls := 0
	r, dir, statePath, factsDir := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A", Metrics: Metrics{Outcome: "ok"}}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")

	st := runNoErr(t, r, &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}})
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
	if ff := factsFiles(t, factsDir); len(ff) != 1 {
		t.Fatalf("facts files %d want 1: %v", len(ff), ff)
	}
	if sf := sidecarFiles(t, factsDir); len(sf) != 1 {
		t.Fatalf("sidecar files %d want 1: %v", len(sf), sf)
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

	runNoErr(t, r, m)
	runNoErr(t, r, m)
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

	st := runNoErr(t, r, m)
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
	if ff := factsFiles(t, factsDir); len(ff) != 1 {
		t.Fatalf("facts files %d want 1 (no duplicate artifact): %v", len(ff), ff)
	}
}

// F2 regression: a succeeded entry whose bytes change must be reprocessed,
// not silently skipped. Content identity is enforced by comparing the fresh
// SHA-256 with the stored digest, not assumed.
func TestBatchResumeReprocessesChangedContent(t *testing.T) {
	calls := 0
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	runNoErr(t, r, m)
	if calls != 1 {
		t.Fatalf("first run calls %d want 1", calls)
	}

	// Mutate the demo content under the same path/identity and re-run.
	writeDemFile(t, dem, "dem-bytes-CHANGED")
	runNoErr(t, r, m)
	if calls != 2 {
		t.Fatalf("changed content was silently skipped: parse calls=%d, want 2", calls)
	}
}

// F2 validation: a manifest with duplicate match_id values is rejected.
func TestBatchManifestRejectsDuplicateMatchID(t *testing.T) {
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		return &ParseResult{Facts: fixedFacts(), Hash: "H"}, nil
	})
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{
		{MatchID: "m1", DemPath: dem},
		{MatchID: "m1", DemPath: dem},
	}}
	_, errs := r.Run(m)
	if len(errs) == 0 {
		t.Fatalf("expected duplicate match_id error")
	}
	if !strings.Contains(errs[0].Error(), "duplicate match_id") {
		t.Fatalf("unexpected error %v", errs[0])
	}
}

func TestBatchManifestRejectsEmpty(t *testing.T) {
	r, _, _, _ := newRunner(t, nil)
	_, errs := r.Run(&Manifest{})
	if len(errs) == 0 {
		t.Fatalf("expected empty-manifest error")
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

	st1, errs := r.Run(m)
	if len(errs) != 0 {
		t.Fatalf("first run should not error before retries exhausted: %v", errs)
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

	_, errs = r.Run(m)
	if len(errs) == 0 {
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

	third, errs := r.Run(m)
	// Resume over a final state that still holds a terminal entry surfaces an
	// aggregate terminal error (operator/CI safety: failed=1 must not read as
	// success), but the terminal entry itself is not retried.
	if len(errs) == 0 {
		t.Fatalf("rerun over a final state with a terminal entry must signal the terminal failure")
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
	runNoErr(t, r, m)
	r.RetryAll = true
	runNoErr(t, r, m)
	if calls != 2 {
		t.Fatalf("parse calls %d want 2 with RetryAll", calls)
	}
}

func TestBatchRetryAllStillReportsFinalTerminalState(t *testing.T) {
	r, dir, _, _ := newRunner(t, nil)
	r.RetryAll = true
	r.MaxRetries = 1
	m := &Manifest{Entries: []ManifestEntry{{
		MatchID: "missing",
		DemPath: filepath.Join(dir, "missing.dem"),
	}}}

	st, errs := r.Run(m)
	if st == nil || st.Entries["missing"].Status != StatusFailedTerminal {
		t.Fatalf("missing entry must be terminal: %+v", st)
	}
	if len(errs) == 0 || !strings.Contains(errs[len(errs)-1].Error(), "terminal failures") {
		t.Fatalf("retry-all must still report final terminal state, got %v", errs)
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

	runNoErr(t, makeRunner(state), m)
	runNoErr(t, makeRunner(state), m)
	if calls != 1 {
		t.Fatalf("restore from persisted state must not reparse, calls %d", calls)
	}
}

// F3 regression: a failing state checkpoint (e.g. state path under a nonexistent
// parent that the runner is forbidden to create) must surface an error, not be
// silently ignored while the CLI reports success.
func TestBatchCheckpointFailurePropagates(t *testing.T) {
	calls := 0
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
	})
	// Point state at a path whose parent is a regular file, so MkdirAll+rename
	// cannot succeed. This simulates an unwritable/nonexistent state location.
	badParent := filepath.Join(dir, "notadir")
	if err := os.WriteFile(badParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.StatePath = filepath.Join(badParent, "state.json") // parent is a file
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	st, errs := r.Run(m)
	if len(errs) == 0 {
		t.Fatalf("checkpoint failure must be propagated, got no errors")
	}
	// The state path is unusable (parent is a regular file); any error naming
	// the state path or checkpoint confirms a silently-ignored checkpoint was
	// not the outcome.
	var sawStateErr bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "state") || strings.Contains(e.Error(), "checkpoint") || strings.Contains(e.Error(), "not a directory") {
			sawStateErr = true
		}
	}
	if !sawStateErr {
		t.Fatalf("expected a state/checkpoint error, got %v", errs)
	}
	if st != nil && st.Entries["m1"].Status == StatusSucceeded {
		t.Fatalf("must not claim succeeded when checkpoint failed: %+v", st.Entries["m1"])
	}
}

func TestBatchCheckpointDirectorySyncUncertaintyReconcilesAndRetryConverges(t *testing.T) {
	calls := 0
	r, dir, statePath, _ := newRunner(t, func(string) (*ParseResult, error) {
		calls++
		return &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}, nil
	})
	failSync := true
	r.AtomicOps.SyncDir = func(path string) error {
		if failSync && path == filepath.Dir(statePath) {
			failSync = false
			return errors.New("injected state directory sync")
		}
		return nil
	}
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	_, errs := r.Run(m)
	if len(errs) == 0 || !hasUncertainCommit(errs) {
		t.Fatalf("checkpoint uncertainty not surfaced: %v", errs)
	}
	if _, err := LoadState(statePath); err != nil {
		t.Fatalf("uncertain canonical state is not complete/valid: %v", err)
	}
	st, errs := r.Run(m)
	if len(errs) != 0 || st.Entries["m1"].Status != StatusSucceeded || calls != 1 {
		t.Fatalf("retry did not converge: state=%+v calls=%d errs=%v", st, calls, errs)
	}
}

func TestFactsAndSidecarDirectorySyncUncertaintyReconcileAndRetry(t *testing.T) {
	r, _, _, factsDir := newRunner(t, nil)
	pr := &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}
	contentSHA := strings.Repeat("a", 64)
	failSync := true
	r.AtomicOps.SyncDir = func(string) error {
		if failSync {
			failSync = false
			return errors.New("injected facts directory sync")
		}
		return nil
	}
	err := r.persistFactsAndSidecar("m1", contentSHA, pr)
	var uncertain *atomicfile.CommitOutcomeUncertainError
	if !errors.As(err, &uncertain) || !uncertain.CanonicalMatchesExpected {
		t.Fatalf("facts uncertainty not reconciled: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(factsDir, contentSHA+".facts.json")); err != nil {
		t.Fatalf("complete facts not recoverable: %v", err)
	}
	if err := r.persistFactsAndSidecar("m1", contentSHA, pr); err != nil {
		t.Fatalf("facts/sidecar retry did not converge: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(factsDir, contentSHA+".provenance.json")); err != nil {
		t.Fatalf("sidecar absent after retry: %v", err)
	}
}

func TestSidecarDirectorySyncUncertaintyReconcilesAndRetry(t *testing.T) {
	r, _, _, factsDir := newRunner(t, nil)
	pr := &ParseResult{Facts: fixedFacts(), Hash: "HASH_A"}
	contentSHA := strings.Repeat("b", 64)
	syncCalls := 0
	r.AtomicOps.SyncDir = func(string) error {
		syncCalls++
		if syncCalls == 2 {
			return errors.New("injected sidecar directory sync")
		}
		return nil
	}
	err := r.persistFactsAndSidecar("m1", contentSHA, pr)
	var uncertain *atomicfile.CommitOutcomeUncertainError
	if !errors.As(err, &uncertain) || !uncertain.CanonicalMatchesExpected || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("sidecar uncertainty not reconciled: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(factsDir, contentSHA+".provenance.json")); err != nil {
		t.Fatalf("uncertain sidecar is not complete/readable: %v", err)
	}
	if err := r.persistFactsAndSidecar("m1", contentSHA, pr); err != nil {
		t.Fatalf("sidecar retry did not converge: %v", err)
	}
}

func hasUncertainCommit(errs []error) bool {
	for _, err := range errs {
		var uncertain *atomicfile.CommitOutcomeUncertainError
		if errors.As(err, &uncertain) {
			return true
		}
	}
	return false
}

// F4 regression: a terminal entry failure in a multi-entry manifest does not
// skip later entries; the runner processes the whole bounded manifest and
// returns an aggregate terminal error. Missing-path and parse failures are
// tested in different orders.
func TestBatchAggregateTerminalProcessesAllEntries(t *testing.T) {
	calls := map[string]int{}
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls[p]++
		if p == "parsefail" {
			return &ParseResult{Metrics: Metrics{Outcome: "parse_error", ParseError: "boom"}}, errBoom
		}
		return &ParseResult{Facts: fixedFacts(), Hash: "H_" + filepath.Base(p)}, nil
	})
	good := filepath.Join(dir, "good.dem")
	good2 := filepath.Join(dir, "good2.dem")
	writeDemFile(t, good, "good-bytes")
	writeDemFile(t, good2, "good2-bytes")
	m := &Manifest{Entries: []ManifestEntry{
		{MatchID: "good", DemPath: good},
		{MatchID: "missing", DemPath: filepath.Join(dir, "does_not_exist.dem")},
		{MatchID: "parsefail", DemPath: "parsefail"},
		{MatchID: "good2", DemPath: good2},
	}}

	st, errs := r.Run(m)
	if len(errs) == 0 {
		t.Fatalf("expected aggregate terminal error")
	}
	// Both good entries must have been processed despite earlier terminal failures.
	if st.Entries["good"].Status != StatusSucceeded || st.Entries["good2"].Status != StatusSucceeded {
		t.Fatalf("good entries not processed: %+v", st.Entries)
	}
	// Missing-path and parse-fail both reach terminal with bounded reasons.
	if st.Entries["missing"].Status != StatusFailedTerminal {
		t.Fatalf("missing not terminal: %+v", st.Entries["missing"])
	}
	if st.Entries["parsefail"].Status != StatusFailedTerminal {
		t.Fatalf("parsefail not terminal: %+v", st.Entries["parsefail"])
	}
	if calls[good] != 1 || calls[good2] != 1 {
		t.Fatalf("good entries parse calls good=%d good2=%d, want 1/1", calls[good], calls[good2])
	}
	// Aggregate error references the terminal count.
	var sawAggregate bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "terminal failures for") {
			sawAggregate = true
		}
	}
	if !sawAggregate {
		t.Fatalf("expected aggregate terminal-failure error, got %v", errs)
	}
}

// F3/F4 ordering: parse-fail before good — the good entry still runs and the
// aggregate terminal error is returned at the end.
func TestBatchTerminalParseFailDoesNotSkipLaterGoodEntry(t *testing.T) {
	calls := 0
	r, dir, _, _ := newRunner(t, func(p string) (*ParseResult, error) {
		calls++
		if strings.HasSuffix(p, "bad.dem") {
			return &ParseResult{Metrics: Metrics{Outcome: "parse_error", ParseError: "boom"}}, errBoom
		}
		return &ParseResult{Facts: fixedFacts(), Hash: "H"}, nil
	})
	bad := filepath.Join(dir, "bad.dem")
	good := filepath.Join(dir, "good.dem")
	writeDemFile(t, bad, "bad-bytes")
	writeDemFile(t, good, "good-bytes")
	m := &Manifest{Entries: []ManifestEntry{
		{MatchID: "bad", DemPath: bad},
		{MatchID: "good", DemPath: good},
	}}
	st, errs := r.Run(m)
	if len(errs) == 0 {
		t.Fatalf("expected terminal error for bad entry")
	}
	if st.Entries["bad"].Status != StatusFailedTerminal {
		t.Fatalf("bad not terminal: %+v", st.Entries["bad"])
	}
	if st.Entries["good"].Status != StatusSucceeded {
		t.Fatalf("good entry was skipped after a terminal parse failure: %+v", st.Entries["good"])
	}
}

// F4/F3 persist failure reaching terminal: a persist that always fails must
// record a terminal entry and be part of the aggregate error.
func TestBatchPersistFailureReachesTerminal(t *testing.T) {
	r, dir, _, factsDir := newRunner(t, func(p string) (*ParseResult, error) {
		return &ParseResult{Facts: fixedFacts(), Hash: "H"}, nil
	})
	// Make the facts dir a regular file so persistFactsAndSidecar cannot
	// write into it, forcing a persist terminal failure.
	if err := os.WriteFile(factsDir, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	dem := filepath.Join(dir, "m1.dem")
	writeDemFile(t, dem, "dem-bytes-1")
	m := &Manifest{Entries: []ManifestEntry{{MatchID: "m1", DemPath: dem}}}

	st, errs := r.Run(m)
	if len(errs) == 0 {
		t.Fatalf("expected terminal persist error")
	}
	if st.Entries["m1"].Status != StatusFailedTerminal {
		t.Fatalf("persist failure not terminal: %+v", st.Entries["m1"])
	}
}
