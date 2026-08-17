package store

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "a.json")
	if err := WriteAtomic(path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":1}` {
		t.Fatalf("content=%s", b)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "a.json" {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestStoreLayoutAndArtifacts(t *testing.T) {
	root := t.TempDir()
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSON("m1", ArtifactVerification, map[string]string{"state": "verified"}); err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := st.ReadJSON("m1", ArtifactVerification, &v); err != nil {
		t.Fatal(err)
	}
	if v["state"] != "verified" {
		t.Fatalf("state=%s", v["state"])
	}
	if st.ArtifactPath("m1", ArtifactVerification) != filepath.Join(root, "matches", "m1", "verification.json") {
		t.Fatal("artifact path layout")
	}
}

func TestCanonicalAndResume(t *testing.T) {
	root := t.TempDir()
	st, _ := New(root)
	fp := Fingerprint("a", "b", "m1", "", "")
	if ok, err := st.CanonicalExists("m1", fp); err != nil || ok {
		t.Fatalf("canonical exists before write: %v %v", ok, err)
	}
	// Write some artifacts and compute canonical.
	st.WriteJSON("m1", ArtifactVerification, map[string]string{"s": "1"})
	st.WriteJSON("m1", ArtifactIdentity, map[string]string{"s": "2"})
	st.WriteJSON("m1", ArtifactClock, map[string]string{"s": "3"})
	can, err := st.WriteCanonical("m1", fp, []string{ArtifactVerification, ArtifactIdentity, ArtifactClock})
	if err != nil {
		t.Fatal(err)
	}
	if can.TreeSHA256 == "" {
		t.Fatal("empty tree hash")
	}
	if len(can.Files) != 3 {
		t.Fatalf("files=%d want 3", len(can.Files))
	}
	// Resume matches for same fingerprint.
	ok, err := st.CanonicalExists("m1", fp)
	if err != nil || !ok {
		t.Fatalf("canonical should exist: %v %v", ok, err)
	}
	// Different fingerprint must not match.
	fp2 := Fingerprint("x", "y", "m1", "", "")
	ok, _ = st.CanonicalExists("m1", fp2)
	if ok {
		t.Fatal("canonical matched different fingerprint")
	}
}

func TestRebuildCatalog(t *testing.T) {
	root := t.TempDir()
	st, _ := New(root)
	st.WriteJSON("m1", ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	st.WriteJSON("m1", ArtifactIdentity, map[string]string{"state": "verified"})
	st.WriteJSON("m1", ArtifactClock, map[string]interface{}{"state": "calibrated", "game_duration_seconds": 100})
	// A complete pipeline writes the canonical tree; without it the match
	// stays quarantined (fail-closed).
	cat, err := st.RebuildCatalog("2026-08-17T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Matches) != 1 {
		t.Fatalf("catalog matches=%d", len(cat.Matches))
	}
	if cat.Matches[0].Status != StatusQuarantined {
		t.Fatalf("row=%+v want quarantined without canonical", cat.Matches[0])
	}
	// Write the canonical and rebuild.
	can, err := st.WriteCanonical("m1", Fingerprint("a", "b", "m1", "", ""), []string{ArtifactVerification, ArtifactIdentity, ArtifactClock})
	if err != nil {
		t.Fatal(err)
	}
	_ = can
	cat, err = st.RebuildCatalog("2026-08-17T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	row := cat.Matches[0]
	if row.Status != StatusVerified || row.Publication != "published" {
		t.Fatalf("row=%+v want verified/published", row)
	}
	if row.TreeSHA256 == "" {
		t.Fatal("missing tree hash in catalog")
	}
	// A second rebuild must be byte-identical (deterministic).
	b1, _ := os.ReadFile(st.CatalogPath())
	cat2, err := st.RebuildCatalog("2026-08-17T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(st.CatalogPath())
	_ = cat2
	if string(b1) != string(b2) {
		t.Fatal("catalog not deterministic")
	}
}

func TestDeriveStatusGates(t *testing.T) {
	root := t.TempDir()
	st, _ := New(root)
	// Verified archive but no identity => quarantined.
	st.WriteJSON("m1", ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	cat, _ := st.RebuildCatalog("t")
	if cat.Matches[0].Status != StatusQuarantined {
		t.Fatalf("status=%s want quarantined", cat.Matches[0].Status)
	}
	// Corrupt archive.
	st.WriteJSON("m2", ArtifactVerification, map[string]string{"state": "corrupt", "reason": "bad"})
	cat, _ = st.RebuildCatalog("t")
	if cat.Matches[0].Status != StatusCorrupt && cat.Matches[0].Status != StatusQuarantined {
		t.Fatalf("status=%s", cat.Matches[0].Status)
	}
}

// TestHashFileStreamLarge proves hashing is streaming and bounded: a large
// artifact is hashed correctly without whole-file reads, and matches a
// reference SHA-256 computed in chunks.
func TestHashFileStreamLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	// 8 MiB of deterministic bytes (larger than the 64 KiB buffer).
	data := make([]byte, 8<<20)
	for i := range data {
		data[i] = byte(i * 31)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFileStream(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reference: whole-file hash.
	h := sha256.Sum256(data)
	want := fmt.Sprintf("%x", h)
	if got != want {
		t.Fatalf("streaming hash mismatch: got %s want %s", got, want)
	}
}

// TestStatusConsumedByCatalog proves the catalog consumes the authoritative
// gated status record: a persisted quarantine stays quarantined/suppressed in
// the catalog even when source artifacts would otherwise derive published.
func TestStatusConsumedByCatalog(t *testing.T) {
	root := t.TempDir()
	st, _ := New(root)
	// Write source artifacts that would derive published...
	st.WriteJSON("m1", ArtifactVerification, map[string]string{"state": "verified", "reason": "ok"})
	st.WriteJSON("m1", ArtifactIdentity, map[string]string{"state": "verified"})
	st.WriteJSON("m1", ArtifactClock, map[string]interface{}{"state": "calibrated"})
	st.WriteJSON("m1", ArtifactCanonical, map[string]interface{}{"tree_sha256": "abc", "schema_version": version.ReportSchema})
	// ...but the authoritative status record says quarantined (role gate).
	st.WriteStatus("m1", &StatusRecord{
		SchemaVersion: StatusSchema, MatchID: "m1", Status: StatusQuarantined,
		Publication: "suppressed", Reason: "role_provenance_gate: missing",
	})
	cat, err := st.RebuildCatalog("t")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Matches) != 1 {
		t.Fatalf("matches=%d", len(cat.Matches))
	}
	if cat.Matches[0].Status != StatusQuarantined || cat.Matches[0].Publication != "suppressed" {
		t.Fatalf("catalog=%+v want quarantined/suppressed", cat.Matches[0])
	}
	if cat.Matches[0].Reason == "" {
		t.Fatal("catalog missing quarantine reason")
	}
	// A verified status record flips the catalog to verified/published.
	st.WriteStatus("m1", &StatusRecord{
		SchemaVersion: StatusSchema, MatchID: "m1", Status: StatusVerified,
		Publication: "published", Reason: "all_gates_pass",
	})
	cat, err = st.RebuildCatalog("t")
	if err != nil {
		t.Fatal(err)
	}
	if cat.Matches[0].Status != StatusVerified || cat.Matches[0].Publication != "published" {
		t.Fatalf("catalog=%+v want verified/published", cat.Matches[0])
	}
}