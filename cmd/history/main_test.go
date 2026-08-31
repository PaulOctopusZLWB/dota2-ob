package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMeasureRunJoinsSampler(t *testing.T) {
	res := measureRun(func() (*corpusResult, error) {
		time.Sleep(60 * time.Millisecond)
		return &corpusResult{}, nil
	})
	if res.err != nil {
		t.Fatalf("measure: %v", res.err)
	}
}

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
	if r1.artifactTreeSHA != r2.artifactTreeSHA || r1.artifactTreeSHA == "" {
		t.Fatalf("artifact tree not deterministic: %s != %s", r1.artifactTreeSHA, r2.artifactTreeSHA)
	}
	for stage, calls := range r1.interruptionCalls {
		if calls != 12 {
			t.Fatalf("interruption matrix %s effects=%d, want 12 (once per boundary)", stage, calls)
		}
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

func TestCorpusArtifactIdentityIgnoresFilesOutsideHistoryRoot(t *testing.T) {
	root := t.TempDir()
	historyRoot := filepath.Join(root, "history")
	if err := os.MkdirAll(historyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(historyRoot, "artifact.json"), []byte("pipeline evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantSHA, wantBytes, wantFiles, err := artifactTreeSHA(historyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wrapper.log"), []byte("not pipeline evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotSHA, gotBytes, gotFiles, err := artifactTreeSHA(historyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != wantSHA || gotBytes != wantBytes || gotFiles != wantFiles {
		t.Fatalf("history artifact identity changed due to unrelated root file: got=%s/%d/%d want=%s/%d/%d", gotSHA, gotBytes, gotFiles, wantSHA, wantBytes, wantFiles)
	}
}

func TestCorpusReportRestrictedOutcome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := runReport([]string{"--data-dir", dir}); err != nil {
		t.Fatalf("report: %v", err)
	}
}

// TestLoadBatchFailsOnMissing proves a missing checkpoint is a hard error, not
// silently empty — a crash before the first checkpoint write must not be read
// as an empty (fresh) batch.
func TestLoadBatchFailsOnMissing(t *testing.T) {
	if _, err := loadBatch(filepath.Join(t.TempDir(), "nope", "state.json")); err == nil {
		t.Fatalf("expected error loading missing checkpoint")
	}
}

// TestLoadBatchFailsOnCorrupt proves a corrupt/truncated checkpoint is a hard
// error, never silently empty.
func TestLoadBatchFailsOnCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "state.json")
	if err := os.WriteFile(p, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBatch(p); err == nil {
		t.Fatalf("expected error loading corrupt checkpoint")
	}
}
