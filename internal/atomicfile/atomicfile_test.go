package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFilePropagatesDurabilityFailures(t *testing.T) {
	tests := []struct {
		name string
		ops  Ops
		want string
	}{
		{
			name: "close",
			ops:  Ops{CloseFile: func(*os.File) error { return errors.New("close-file") }},
			want: "close-file",
		},
		{
			name: "file sync",
			ops:  Ops{SyncFile: func(*os.File) error { return errors.New("sync-file") }},
			want: "sync-file",
		},
		{
			name: "rename",
			ops:  Ops{Rename: func(string, string) error { return errors.New("rename-file") }},
			want: "rename-file",
		},
		{
			name: "directory sync",
			ops:  Ops{SyncDir: func(string) error { return errors.New("sync-dir") }},
			want: "sync-dir",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "state.json")
			err := WriteFileWithOps(dest, []byte("state"), 0o644, tt.ops)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want injected %q failure", err, tt.want)
			}
		})
	}
}

func TestWriteFileUsesRandomExclusiveTempAndCleansIt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "state.json")
	predictable := dest + ".tmp"
	if err := os.WriteFile(predictable, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(dest, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(predictable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sentinel" {
		t.Fatalf("predictable temp was reused: %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".atomic-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestDirectorySyncFailureIsTypedUncertainAndRetryConverges(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "state.json")
	if err := os.WriteFile(dest, []byte("old-complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []byte("new-complete")
	err := WriteFileWithOps(dest, want, 0o644, Ops{
		SyncDir: func(string) error { return errors.New("injected directory sync") },
	})
	var uncertain *CommitOutcomeUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("got %T %v, want typed commit outcome uncertainty", err, err)
	}
	if !uncertain.CanonicalMatchesExpected {
		t.Fatalf("canonical reconciliation did not recognize complete intended payload: %+v", uncertain)
	}
	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old-complete" && string(got) != string(want) {
		t.Fatalf("canonical is neither complete old nor complete new payload: %q", got)
	}
	if err := WriteFile(dest, want, 0o644); err != nil {
		t.Fatalf("idempotent retry did not converge: %v", err)
	}
	got, _ = os.ReadFile(dest)
	if string(got) != string(want) {
		t.Fatalf("retry payload = %q, want %q", got, want)
	}
}

func TestFileSyncAndRenameFailuresPreservePriorCanonical(t *testing.T) {
	for _, tt := range []struct {
		name string
		ops  Ops
	}{
		{name: "file sync", ops: Ops{SyncFile: func(*os.File) error { return errors.New("sync") }}},
		{name: "close", ops: Ops{CloseFile: func(*os.File) error { return errors.New("close") }}},
		{name: "rename", ops: Ops{Rename: func(string, string) error { return errors.New("rename") }}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "artifact")
			if err := os.WriteFile(dest, []byte("prior-good"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := WriteFileWithOps(dest, []byte("replacement"), 0o644, tt.ops); err == nil {
				t.Fatal("expected injected failure")
			}
			got, _ := os.ReadFile(dest)
			if string(got) != "prior-good" {
				t.Fatalf("pre-rename failure replaced canonical: %q", got)
			}
		})
	}
}

func TestDirectorySyncUncertaintyDoesNotAcceptInvalidCanonical(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "artifact")
	f, err := NewTemp(dest, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("complete-intended")); err != nil {
		t.Fatal(err)
	}
	err = CommitWithOps(f, dest, func(path string) error {
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(b) != "complete-intended" {
			return errors.New("invalid or truncated canonical")
		}
		return nil
	}, Ops{
		Rename: func(_, target string) error {
			return os.WriteFile(target, []byte("truncated"), 0o644)
		},
		SyncDir: func(string) error { return errors.New("sync-dir") },
	})
	var uncertain *CommitOutcomeUncertainError
	if !errors.As(err, &uncertain) || uncertain.CanonicalMatchesExpected || uncertain.ReconcileError == nil {
		t.Fatalf("invalid canonical was accepted: %+v err=%v", uncertain, err)
	}
}
