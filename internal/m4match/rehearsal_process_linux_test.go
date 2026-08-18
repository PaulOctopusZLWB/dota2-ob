//go:build linux

package m4match

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRehearsalDotaIdentityHashesOpenedDescriptorAndRejectsPathDrift(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "dota-a")
	second := filepath.Join(root, "dota-b")
	content := []byte("same executable bytes")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	procRoot := filepath.Join(root, "proc")
	writeFakeProcIdentity(t, procRoot, 42, first, 9001)
	a, err := readRehearsalDotaIdentityAt(procRoot, 42)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(content)
	if a.ExecutableSHA256 != hex.EncodeToString(want[:]) || a.ProcessStartTicks != 9001 || a.ExecutablePath != first {
		t.Fatalf("identity=%#v", a)
	}
	if err := os.Remove(filepath.Join(procRoot, "42", "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, filepath.Join(procRoot, "42", "exe")); err != nil {
		t.Fatal(err)
	}
	b, err := readRehearsalDotaIdentityAt(procRoot, 42)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExecutableSHA256 != b.ExecutableSHA256 || sameRehearsalDotaIdentity(a, b) {
		t.Fatalf("same-content path transition escaped: a=%#v b=%#v", a, b)
	}
}

func TestRehearsalDotaIdentityRejectsStartTickAndPIDTransitions(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "dota2")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	procRoot := filepath.Join(root, "proc")
	writeFakeProcIdentity(t, procRoot, 7, binary, 11)
	first, err := readRehearsalDotaIdentityAt(procRoot, 7)
	if err != nil {
		t.Fatal(err)
	}
	writeFakeProcStat(t, procRoot, 7, 12)
	second, err := readRehearsalDotaIdentityAt(procRoot, 7)
	if err != nil {
		t.Fatal(err)
	}
	if sameRehearsalDotaIdentity(first, second) {
		t.Fatal("PID reuse/start-tick drift accepted")
	}
	writeFakeProcIdentity(t, procRoot, 8, binary, 11)
	third, err := readRehearsalDotaIdentityAt(procRoot, 8)
	if err != nil {
		t.Fatal(err)
	}
	if sameRehearsalDotaIdentity(first, third) {
		t.Fatal("PID transition accepted")
	}
}

func writeFakeProcIdentity(t *testing.T, procRoot string, pid int, executable string, ticks uint64) {
	t.Helper()
	dir := filepath.Join(procRoot, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("dota2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "exe"))
	if err := os.Symlink(executable, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
	writeFakeProcStat(t, procRoot, pid, ticks)
}

func writeFakeProcStat(t *testing.T, procRoot string, pid int, ticks uint64) {
	t.Helper()
	fields := append([]string{"S"}, make([]string, 18)...)
	for index := 1; index < len(fields); index++ {
		fields[index] = "0"
	}
	fields = append(fields, fmt.Sprint(ticks))
	payload := fmt.Sprintf("%d (dota2) %s\n", pid, strings.Join(fields, " "))
	if err := os.WriteFile(filepath.Join(procRoot, fmt.Sprint(pid), "stat"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}
