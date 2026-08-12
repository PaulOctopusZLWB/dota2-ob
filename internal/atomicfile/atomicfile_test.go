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
