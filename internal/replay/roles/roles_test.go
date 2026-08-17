package roles

import (
	"os"
	"path/filepath"
	"testing"
)

func testRegistry() *Registry {
	return &Registry{
		SchemaVersion: "ti2026.roles.v1",
		TournamentID:  "ti2026",
		Matches: []RoleMatch{{
			MatchID: "1000000001",
			Teams: []RoleTeam{{
				TeamID: "T1", TeamName: "Team One", Side: "radiant",
				SourceKind: "reliable_public_tournament_roster",
				SourceURL:  "https://example.com/roster",
				RetrievedAt: "2026-08-17T04:15:00Z",
				Participants: []RoleRecord{
					{RoleRecordID: "1000000001:1000", AccountID: "1000", PlayerName: "p1", NominalRole: "1", RoleConfidence: "high"},
				},
			}},
		}},
	}
}

func TestEffectiveNoOverride(t *testing.T) {
	r := testRegistry()
	eff, ok := r.Effective("1000000001", "1000", nil)
	if !ok {
		t.Fatal("not found")
	}
	if eff.NominalRole != "1" {
		t.Fatalf("role=%s", eff.NominalRole)
	}
	if eff.SourceKind != "reliable_public_tournament_roster" {
		t.Fatalf("source=%s", eff.SourceKind)
	}
	if eff.OverrideApplied {
		t.Fatal("override unexpectedly applied")
	}
}

func TestEffectiveOverride(t *testing.T) {
	r := testRegistry()
	o := &OverrideFile{Overrides: []Override{
		{MatchID: "1000000001", AccountID: "1000", NominalRole: "5", Reason: "manual adjudication"},
	}}
	eff, ok := r.Effective("1000000001", "1000", o)
	if !ok {
		t.Fatal("not found")
	}
	if eff.NominalRole != "5" {
		t.Fatalf("role=%s want 5", eff.NominalRole)
	}
	if !eff.OverrideApplied {
		t.Fatal("override not applied")
	}
	if eff.OverrideReason == nil || *eff.OverrideReason != "manual adjudication" {
		t.Fatalf("override reason=%v", eff.OverrideReason)
	}
}

func TestMissingRecord(t *testing.T) {
	r := testRegistry()
	if _, ok := r.Effective("1000000001", "9999", nil); ok {
		t.Fatal("found missing account")
	}
	if _, ok := r.Effective("999", "1000", nil); ok {
		t.Fatal("found missing match")
	}
}

func TestRegistryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.json")
	r := testRegistry()
	data, err := r.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TournamentID != "ti2026" {
		t.Fatalf("tournament=%s", loaded.TournamentID)
	}
}

func TestOverrideRoundtripMissing(t *testing.T) {
	dir := t.TempDir()
	o, err := LoadOverrides(filepath.Join(dir, "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Overrides) != 0 {
		t.Fatalf("expected empty overrides")
	}
}