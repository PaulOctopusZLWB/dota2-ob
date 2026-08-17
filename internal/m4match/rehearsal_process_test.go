package m4match

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRehearsalDotaIdentityBindsPathContentAndStart(t *testing.T) {
	procRoot := t.TempDir()
	binDir := t.TempDir()
	firstPath := filepath.Join(binDir, "dota2-a")
	secondPath := filepath.Join(binDir, "dota2-b")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("same executable content"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pid := 4242
	writeFakeDotaProcess(t, procRoot, pid, firstPath, 100)
	bound, err := acquireRehearsalDota(procRoot)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ExecutablePathSHA256 == "" || bound.ExecutableSHA256 == "" || bound.StartTicks != 100 {
		t.Fatalf("incomplete acquisition: %+v", bound)
	}
	if _, err = observeBoundRehearsalDota(procRoot, bound, 1, "raw"); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(procRoot, strconv.Itoa(pid), "exe")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(secondPath, filepath.Join(procRoot, strconv.Itoa(pid), "exe")); err != nil {
		t.Fatal(err)
	}
	if _, err = observeBoundRehearsalDota(procRoot, bound, 2, "raw"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("same-content path drift admitted: %v", err)
	}
}

func TestRehearsalDotaIdentityRejectsPIDReuseAndAmbiguity(t *testing.T) {
	procRoot := t.TempDir()
	binary := filepath.Join(t.TempDir(), "dota2")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFakeDotaProcess(t, procRoot, 100, binary, 10)
	bound, err := acquireRehearsalDota(procRoot)
	if err != nil {
		t.Fatal(err)
	}
	writeFakeDotaProcess(t, procRoot, 100, binary, 11)
	if _, err = observeBoundRehearsalDota(procRoot, bound, 1, "raw"); err == nil {
		t.Fatal("PID reuse admitted")
	}
	writeFakeDotaProcess(t, procRoot, 101, binary, 12)
	if _, err = acquireRehearsalDota(procRoot); err == nil {
		t.Fatal("ambiguous Dota population admitted")
	}
}

func writeFakeDotaProcess(t *testing.T, procRoot string, pid int, executable string, start uint64) {
	t.Helper()
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("dota2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 20)
	for index := range fields {
		fields[index] = "0"
	}
	fields[0] = "S"
	fields[19] = strconv.FormatUint(start, 10)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(strconv.Itoa(pid)+" (dota2) "+strings.Join(fields, " ")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "exe")
	_ = os.Remove(link)
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
}
