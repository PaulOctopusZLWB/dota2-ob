package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// helperManifest builds a minimal manifest entry pointing at files under a
// temp corpus root. sha256s are computed over the actual bytes.
func helperManifest(t *testing.T, root string, archiveBytes, demoBytes []byte) (*Match, string) {
	t.Helper()
	ah := sha256.Sum256(archiveBytes)
	dh := sha256.Sum256(demoBytes)
	arcName := "m.dem.bz2"
	demoName := filepath.Join("dem", "m.dem")
	if err := os.MkdirAll(filepath.Join(root, "dem"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, arcName), archiveBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, demoName), demoBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	mt := &Match{
		MatchID:             "1000000001",
		ArchiveRelativePath: arcName,
		ArchiveBytes:        int64(len(archiveBytes)),
		ArchiveSHA256:       hex.EncodeToString(ah[:]),
		DemoRelativePath:    demoName,
		DemoBytes:           int64(len(demoBytes)),
		DemoSHA256:          hex.EncodeToString(dh[:]),
	}
	return mt, filepath.Join(root, arcName)
}

// makeZstd compresses data with the zstd encoder.
func makeZstd(t *testing.T, data []byte) []byte {
	t.Helper()
	return zstd.EncodeTo(nil, data)
}

func demoBytes() []byte {
	// A minimal Source 2 demo: PBDEMS2 magic plus a few payload bytes. The
	// container gate only checks magic + hash + size; parsing is not run here.
	b := []byte{'P', 'B', 'D', 'E', 'M', 'S', '2', 0x00, 0x01, 0x02, 0x03}
	return b
}

func TestVerifyOK(t *testing.T) {
	root := t.TempDir()
	demo := demoBytes()
	arc := makeZstd(t, demo)
	mt, _ := helperManifest(t, root, arc, demo)
	v, err := Verify(root, mt, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateVerified {
		t.Fatalf("state=%s reason=%s", v.State, v.Reason)
	}
	if !Terminal(v.State) {
		t.Fatalf("verified must be terminal")
	}
}

func TestVerifyMissingArchive(t *testing.T) {
	root := t.TempDir()
	demo := demoBytes()
	mt, _ := helperManifest(t, root, makeZstd(t, demo), demo)
	// Remove the archive.
	os.Remove(filepath.Join(root, mt.ArchiveRelativePath))
	v, err := Verify(root, mt, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateMissing {
		t.Fatalf("state=%s want missing", v.State)
	}
	if !Terminal(v.State) {
		t.Fatalf("missing must be terminal")
	}
}

func TestVerifyCorruptHash(t *testing.T) {
	root := t.TempDir()
	demo := demoBytes()
	arc := makeZstd(t, demo)
	mt, _ := helperManifest(t, root, arc, demo)
	// Corrupt the archive bytes (append garbage) so the size/hash mismatch.
	f, err := os.OpenFile(filepath.Join(root, mt.ArchiveRelativePath), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("garbage")
	f.Close()
	v, err := Verify(root, mt, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateCorrupt {
		t.Fatalf("state=%s want corrupt", v.State)
	}
	if !Terminal(v.State) {
		t.Fatalf("corrupt must be terminal")
	}
}

func TestVerifyMagicMismatch(t *testing.T) {
	root := t.TempDir()
	demo := demoBytes()
	// Archive with wrong magic: not zstd.
	arc := append([]byte("NOTZSTD!"), []byte("payload")...)
	mt, _ := helperManifest(t, root, arc, demo)
	// Fix hashes to match the (wrong-magic) archive bytes.
	ah := sha256.Sum256(arc)
	mt.ArchiveSHA256 = hex.EncodeToString(ah[:])
	mt.ArchiveBytes = int64(len(arc))
	v, err := Verify(root, mt, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateCorrupt {
		t.Fatalf("state=%s want corrupt", v.State)
	}
	if v.ArchiveMagicOK {
		t.Fatalf("archive magic must be false")
	}
}

func TestVerifyDemoMagicMismatch(t *testing.T) {
	root := t.TempDir()
	demo := []byte("NOTAPBDEM") // wrong demo magic
	arc := makeZstd(t, demo)
	mt, _ := helperManifest(t, root, arc, demo)
	v, err := Verify(root, mt, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateCorrupt || v.DemoMagicOK {
		t.Fatalf("state=%s demo_magic=%v want corrupt demo_magic=false", v.State, v.DemoMagicOK)
	}
}

func TestVerifyWriteDemo(t *testing.T) {
	root := t.TempDir()
	demo := demoBytes()
	arc := makeZstd(t, demo)
	mt, _ := helperManifest(t, root, arc, demo)
	out := filepath.Join(t.TempDir(), "out.dem")
	v, err := Verify(root, mt, VerifyOptions{WriteDemo: true, DemoOut: out})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateVerified {
		t.Fatalf("state=%s", v.State)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(demo) {
		t.Fatalf("written demo mismatch")
	}
}

func TestManifestValidate(t *testing.T) {
	m := &Manifest{SchemaVersion: "x", Matches: []Match{
		{MatchID: "1", ExpectedTeams: make([]ExpectedTeam, 2), ExpectedParticipants: make([]ExpectedPlayer, 10), ArchiveSHA256: "a", DemoSHA256: "b"},
		{MatchID: "2", ExpectedTeams: make([]ExpectedTeam, 2), ExpectedParticipants: make([]ExpectedPlayer, 10), ArchiveSHA256: "a", DemoSHA256: "b"},
	}}
	if err := m.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Duplicate match id must fail.
	m.Matches[1].MatchID = "1"
	if err := m.Validate(); err == nil {
		t.Fatalf("duplicate match id accepted")
	}
	// Bad team/participant counts must fail.
	m.Matches[1].MatchID = "2"
	m.Matches[0].ExpectedTeams = make([]ExpectedTeam, 1)
	if err := m.Validate(); err == nil {
		t.Fatalf("bad team count accepted")
	}
	m.Matches[0].ExpectedTeams = make([]ExpectedTeam, 2)
	m.Matches[0].ExpectedParticipants = make([]ExpectedPlayer, 9)
	if err := m.Validate(); err == nil {
		t.Fatalf("bad participant count accepted")
	}
}

func TestFind(t *testing.T) {
	m := &Manifest{Matches: []Match{{MatchID: "abc"}}}
	mt, ok := m.Find("abc")
	if !ok || mt.MatchID != "abc" {
		t.Fatalf("find failed")
	}
	if _, ok := m.Find("nope"); ok {
		t.Fatalf("found missing match")
	}
}

func TestTerminalStates(t *testing.T) {
	for _, s := range []string{StateMissing, StateCorrupt, StateVerified, StateParseFailed, StateQuarantined} {
		if !Terminal(s) {
			t.Fatalf("%s not terminal", s)
		}
	}
	if Terminal("pending") {
		t.Fatalf("pending must not be terminal")
	}
}
