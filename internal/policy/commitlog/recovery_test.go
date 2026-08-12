package commitlog_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

func testFrame(t *testing.T, commit contracts.PolicyCommitV1) []byte {
	t.Helper()
	payload, err := contracts.MarshalCanonical(commit)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	frame := make([]byte, 4+len(payload)+32+8)
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	copy(frame[4+len(payload):], sum[:])
	copy(frame[len(frame)-8:], []byte{'P', 'C', 'O', 'M', 'M', 'I', 'T', 1})
	return frame
}

func onlySegment(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "session"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".pcl" {
			return filepath.Join(root, "session", entry.Name())
		}
	}
	t.Fatal("segment absent")
	return ""
}

func TestRecoveryTruncatesIncompleteTailsAndReconstructsState(t *testing.T) {
	root := t.TempDir()
	store, _, err := commitlog.Open(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	path := onlySegment(t, root)
	clean, _ := os.ReadFile(path)
	other := t.TempDir()
	second, _, _ := commitlog.Open(other, "session")
	frame, err := second.Append(validCommit(1, 0, 1, "command-2"))
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	full, _ := os.ReadFile(onlySegment(t, other))
	cuts := []int{1, 4, 4 + len(frame.Payload)/2, 4 + len(frame.Payload) + 8, len(full) - 1}
	for i, cut := range cuts {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			if err := os.WriteFile(path, append(append([]byte(nil), clean...), full[:cut]...), 0o600); err != nil {
				t.Fatal(err)
			}
			reopened, state, err := commitlog.Open(root, "session")
			if err != nil {
				t.Fatal(err)
			}
			_ = reopened.Close()
			if state.CommitSequence != 1 || state.PolicyRevision != 1 || state.CommandResults["command-1"].Reason != "accepted" {
				t.Fatalf("state = %#v", state)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, clean) {
				t.Fatalf("tail not truncated: %d != %d", len(got), len(clean))
			}
		})
	}
}

func TestRecoveryRejectsTerminatedCorruption(t *testing.T) {
	for _, at := range []string{"payload", "hash", "marker"} {
		t.Run(at, func(t *testing.T) {
			root := t.TempDir()
			store, _, _ := commitlog.Open(root, "session")
			committed, err := store.Append(validCommit(1, 0, 1, "command-1"))
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			path := onlySegment(t, root)
			data, _ := os.ReadFile(path)
			index := 4
			if at == "hash" {
				index = 4 + len(committed.Payload)
			}
			if at == "marker" {
				index = len(data) - 1
			}
			data[index] ^= 0xff
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := commitlog.Open(root, "session"); !errors.Is(err, commitlog.ErrCorrupt) {
				t.Fatalf("Open error = %v", err)
			}
		})
	}
}

func TestAppendFailuresRollbackOrSealDeterministically(t *testing.T) {
	t.Run("short write rolls back", func(t *testing.T) {
		root := t.TempDir()
		calls := 0
		hooks := commitlog.Hooks{Write: func(f *os.File, p []byte) (int, error) {
			calls++
			if calls == 1 {
				return f.Write(p[:len(p)/2])
			}
			return f.Write(p)
		}}
		store, _, err := commitlog.Open(root, "session", commitlog.WithHooks(hooks))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err = store.Append(validCommit(1, 0, 1, "command-1")); !errors.Is(err, commitlog.ErrAppendFailed) {
			t.Fatalf("error=%v", err)
		}
		info, _ := os.Stat(onlySegment(t, root))
		if info.Size() != 0 {
			t.Fatalf("rollback size=%d", info.Size())
		}
		if _, err = store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rollback failure seals", func(t *testing.T) {
		hooks := commitlog.Hooks{Write: func(f *os.File, p []byte) (int, error) { n, _ := f.Write(p[:4]); return n, io.ErrShortWrite }, Truncate: func(*os.File, int64) error { return errors.New("truncate failed") }}
		store, _, err := commitlog.Open(t.TempDir(), "session", commitlog.WithHooks(hooks))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err = store.Append(validCommit(1, 0, 1, "command-1")); !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("error=%v", err)
		}
		if _, err = store.Append(validCommit(1, 0, 1, "command-2")); !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("sealed error=%v", err)
		}
	})
}

func TestRotationUsesBoundedProtectedSegments(t *testing.T) {
	root := t.TempDir()
	const limit = 1200
	store, _, err := commitlog.Open(root, "session", commitlog.WithSegmentLimit(limit))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(validCommit(2, 1, 2, "command-2")); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	entries, _ := os.ReadDir(filepath.Join(root, "session"))
	segments := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".pcl" {
			segments++
			info, _ := entry.Info()
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("segment mode=%o", info.Mode().Perm())
			}
			if info.Size() > limit {
				t.Fatalf("segment too large: %d", info.Size())
			}
		}
	}
	if segments != 2 {
		t.Fatalf("segments=%d", segments)
	}
	dirInfo, _ := os.Stat(filepath.Join(root, "session"))
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode=%o", dirInfo.Mode().Perm())
	}
}

func TestRestartContinuesLastSegmentAndRejectsSequenceGap(t *testing.T) {
	root := t.TempDir()
	store, _, _ := commitlog.Open(root, "session")
	if _, err := store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	path := onlySegment(t, root)
	before, _ := os.Stat(path)
	reopened, state, err := commitlog.Open(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	if state.CommitSequence != 1 {
		t.Fatalf("sequence=%d", state.CommitSequence)
	}
	if _, err = reopened.Append(validCommit(2, 1, 2, "command-2")); err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()
	entries, _ := os.ReadDir(filepath.Join(root, "session"))
	segments := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".pcl" {
			segments++
		}
	}
	if segments != 1 {
		t.Fatalf("restart rotated early: %d segments", segments)
	}
	after, _ := os.Stat(path)
	if after.Size() <= before.Size() {
		t.Fatal("last segment was not continued")
	}

	gap := validCommit(4, 2, 3, "command-gap")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write(testFrame(t, gap))
	_ = f.Close()
	if _, _, err = commitlog.Open(root, "session"); !errors.Is(err, commitlog.ErrCorrupt) {
		t.Fatalf("sequence gap error=%v", err)
	}
}

func TestAppendRejectsRevisionOrStateDiscontinuity(t *testing.T) {
	store, _, _ := commitlog.Open(t.TempDir(), "session")
	defer store.Close()
	if _, err := store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
		t.Fatal(err)
	}
	bad := validCommit(2, 1, 2, "command-2")
	bad.PriorStateHash = strings.Repeat("b", 64)
	if _, err := store.Append(bad); !errors.Is(err, commitlog.ErrSequence) {
		t.Fatalf("state discontinuity error=%v", err)
	}
}

func TestSessionCapacityExactBoundaryAndFirstOverFailClosed(t *testing.T) {
	t.Run("aggregate bytes", func(t *testing.T) {
		root := t.TempDir()
		seed, _, err := commitlog.Open(root, "session")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = seed.Append(validCommit(1, 0, 1, "command-1")); err != nil {
			t.Fatal(err)
		}
		_ = seed.Close()
		info, err := os.Stat(onlySegment(t, root))
		if err != nil {
			t.Fatal(err)
		}

		store, state, err := commitlog.Open(root, "session", commitlog.WithSessionLimits(info.Size(), commitlog.MaxSessionSegments))
		if err != nil {
			t.Fatalf("exact aggregate boundary rejected: %v", err)
		}
		if state.CommitSequence != 1 {
			t.Fatalf("recovered sequence=%d", state.CommitSequence)
		}
		if _, err = store.Append(validCommit(2, 1, 2, "command-2")); !errors.Is(err, commitlog.ErrCapacity) || !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("first byte over error=%v", err)
		}
		if _, err = store.Append(validCommit(2, 1, 2, "command-2")); !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("sealed retry error=%v", err)
		}
	})

	t.Run("segments", func(t *testing.T) {
		root := t.TempDir()
		store, _, err := commitlog.Open(root, "session", commitlog.WithSegmentLimit(1200), commitlog.WithSessionLimits(commitlog.MaxSessionBytes, 2))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Append(validCommit(2, 1, 2, "command-2")); err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
		store, state, err := commitlog.Open(root, "session", commitlog.WithSegmentLimit(1200), commitlog.WithSessionLimits(commitlog.MaxSessionBytes, 2))
		if err != nil {
			t.Fatalf("exact segment boundary rejected on restart: %v", err)
		}
		if state.CommitSequence != 2 {
			t.Fatalf("recovered sequence=%d", state.CommitSequence)
		}
		if _, err = store.Append(validCommit(3, 2, 3, "command-3")); !errors.Is(err, commitlog.ErrCapacity) || !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("first segment over error=%v", err)
		}
		entries, _ := os.ReadDir(filepath.Join(root, "session"))
		segments := 0
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".pcl" {
				segments++
			}
		}
		if segments != 2 {
			t.Fatalf("segments=%d, want exact limit 2", segments)
		}
	})
}

func TestProductionSessionLimitsAreNormative(t *testing.T) {
	if commitlog.MaxSessionBytes != 1<<30 {
		t.Fatalf("MaxSessionBytes=%d", commitlog.MaxSessionBytes)
	}
	if commitlog.MaxSessionSegments != 104 {
		t.Fatalf("MaxSessionSegments=%d", commitlog.MaxSessionSegments)
	}
}

func TestProductionSegmentLimitAccepts104AndRejects105AfterRestart(t *testing.T) {
	root := t.TempDir()
	store, _, err := commitlog.Open(root, "session", commitlog.WithSegmentLimit(1200))
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= commitlog.MaxSessionSegments; sequence++ {
		if _, err = store.Append(validCommit(sequence, sequence-1, sequence, fmt.Sprintf("command-%03d", sequence))); err != nil {
			t.Fatalf("append %d: %v", sequence, err)
		}
	}
	_ = store.Close()
	reopened, state, err := commitlog.Open(root, "session", commitlog.WithSegmentLimit(1200))
	if err != nil {
		t.Fatalf("exact 104-segment recovery: %v", err)
	}
	if state.CommitSequence != commitlog.MaxSessionSegments {
		t.Fatalf("recovered sequence=%d", state.CommitSequence)
	}
	next := uint64(commitlog.MaxSessionSegments + 1)
	if _, err = reopened.Append(validCommit(next, next-1, next, "command-105")); !errors.Is(err, commitlog.ErrCapacity) || !errors.Is(err, commitlog.ErrSealed) {
		t.Fatalf("append 105 error=%v", err)
	}
}

func TestRecoveryRejectsAlreadyOverSessionLimits(t *testing.T) {
	t.Run("aggregate bytes", func(t *testing.T) {
		root := t.TempDir()
		store, _, _ := commitlog.Open(root, "session")
		if _, err := store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
		info, _ := os.Stat(onlySegment(t, root))
		if _, _, err := commitlog.Open(root, "session", commitlog.WithSessionLimits(info.Size()-1, commitlog.MaxSessionSegments)); !errors.Is(err, commitlog.ErrCapacity) || !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("over-byte recovery error=%v", err)
		}
	})
	t.Run("segments", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "session")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 3; i++ {
			name := filepath.Join(dir, fmt.Sprintf("%020d.pcl", i))
			if err := os.WriteFile(name, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := commitlog.Open(root, "session", commitlog.WithSessionLimits(commitlog.MaxSessionBytes, 2)); !errors.Is(err, commitlog.ErrCapacity) || !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("over-segment recovery error=%v", err)
		}
	})
}

func TestRecoveryCapacityCountsOnlyCommittedBytesAfterTailTruncation(t *testing.T) {
	root := t.TempDir()
	store, _, _ := commitlog.Open(root, "session")
	if _, err := store.Append(validCommit(1, 0, 1, "command-1")); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	path := onlySegment(t, root)
	clean, _ := os.Stat(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	reopened, state, err := commitlog.Open(root, "session", commitlog.WithSessionLimits(clean.Size(), commitlog.MaxSessionSegments))
	if err != nil {
		t.Fatalf("recover incomplete tail at exact committed bound: %v", err)
	}
	defer reopened.Close()
	if state.CommitSequence != 1 {
		t.Fatalf("sequence=%d", state.CommitSequence)
	}
	info, _ := os.Stat(path)
	if info.Size() != clean.Size() {
		t.Fatalf("tail size=%d, want %d", info.Size(), clean.Size())
	}
}

func TestRecoveryRejectsFullyFramedDuplicateCommand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*contracts.PolicyCommitV1)
	}{
		{name: "identical terminal result"},
		{name: "conflicting terminal result", mutate: func(commit *contracts.PolicyCommitV1) {
			commit.CommandResult.Reason = "session_command_limit"
			commit.AuditEvents[0].Reason = "session_command_limit"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, _, err := commitlog.Open(root, "session")
			if err != nil {
				t.Fatal(err)
			}
			first, err := store.Append(rejectedCommit(1, 0, "duplicate-command"))
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := checkpointFor(first)
			if err = store.WriteCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			_ = store.Close()

			duplicate := rejectedCommit(2, 0, "duplicate-command")
			if tc.mutate != nil {
				tc.mutate(&duplicate)
			}
			path := onlySegment(t, root)
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write(testFrame(t, duplicate)); err != nil {
				t.Fatal(err)
			}
			_ = file.Close()
			before, _ := os.ReadFile(path)

			if _, _, err = commitlog.Open(root, "session"); !errors.Is(err, commitlog.ErrCorrupt) || !strings.Contains(err.Error(), "duplicate command") {
				t.Fatalf("Open error=%v", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, before) {
				t.Fatal("terminated duplicate corruption was mutated")
			}
		})
	}
}
