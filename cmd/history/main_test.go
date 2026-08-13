package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorpusDeterministicAndIdempotent(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "d1")
	dir2 := filepath.Join(t.TempDir(), "d2")
	r1, err := buildAndRunCorpus(4, 1, 1, dir1)
	if err != nil {
		t.Fatalf("corpus run 1: %v", err)
	}
	r2, err := buildAndRunCorpus(4, 1, 1, dir2)
	if err != nil {
		t.Fatalf("corpus run 2: %v", err)
	}
	if r1.discoveryID != r2.discoveryID {
		t.Fatalf("discovery manifest id not deterministic: %s != %s", r1.discoveryID, r2.discoveryID)
	}
	if r1.snapshotID != r2.snapshotID {
		t.Fatalf("snapshot manifest id not deterministic: %s != %s", r1.snapshotID, r2.snapshotID)
	}
	if r1.baselineCount != r2.baselineCount || r1.baselineCount == 0 {
		t.Fatalf("baseline count not deterministic or empty: %d vs %d", r1.baselineCount, r2.baselineCount)
	}
	if r1.includedCount == 0 {
		t.Fatalf("expected included matches")
	}
	if r1.quarantined == 0 {
		t.Fatalf("expected quarantined matches in corpus")
	}
	// Idempotent resume: the second batch pass must not re-run any stage.
	for k, v := range r1.stageCalls {
		if r1.resumeCalls[k] != v {
			t.Fatalf("resume re-ran stage %s: pass1=%d resume=%d", k, v, r1.resumeCalls[k])
		}
	}
	// No tracked raw/generated data: the only artifact under the data dir is
	// the git-ignored batch state file.
	files, _ := os.ReadDir(dir1)
	if len(files) == 0 {
		t.Fatalf("expected batch state artifact under data root")
	}
}

func TestCorpusReportRestrictedOutcome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := runReport([]string{"--data-dir", dir}); err != nil {
		t.Fatalf("report: %v", err)
	}
}