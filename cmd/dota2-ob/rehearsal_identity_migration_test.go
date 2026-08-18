package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/m4match"
)

type rehearsalIdentitySet struct {
	EngineBuild string `json:"engine_build"`
	Lineage     string `json:"lineage"`
	Release     string `json:"release"`
	Golden      string `json:"golden"`
}

type rehearsalSourceRotation struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type rehearsalIdentityMigration struct {
	SchemaVersion     string                    `json:"schema_version"`
	AcceptedSpec      string                    `json:"accepted_spec"`
	AcceptedP4Spec    string                    `json:"accepted_p4_spec"`
	Old               rehearsalIdentitySet      `json:"old"`
	New               rehearsalIdentitySet      `json:"new"`
	SourceRotations   []rehearsalSourceRotation `json:"source_rotations"`
	AllowedDiffFields []string                  `json:"allowed_diff_fields"`
	Preserved         []string                  `json:"preserved"`
}

func TestRehearsalSuccessorIdentityMigrationIsCausalAndSemanticDiffClosed(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "rehearsal_identity_migration_v1.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest rehearsalIdentityMigration
	if json.Unmarshal(payload, &manifest) != nil {
		t.Fatal("migration manifest decode")
	}
	canonical, err := contracts.MarshalCanonical(manifest)
	if err != nil || !bytes.Equal(bytes.TrimSuffix(payload, []byte("\n")), canonical) {
		t.Fatal("migration manifest is not canonical")
	}
	current := testLiveOnlyArtifacts("m4-live-only")
	releaseID, _ := current.Release.ContentID()
	if manifest.SchemaVersion != "rehearsal_identity_migration.v1" || manifest.AcceptedSpec != m4match.AcceptedRehearsalSpec || manifest.AcceptedP4Spec != m4match.AcceptedP4Spec ||
		manifest.New.EngineBuild != current.Lineage.EngineBuild.ContentSHA256 || manifest.New.Lineage != current.Lineage.MustContentID() || manifest.New.Release != releaseID {
		t.Fatalf("migration identity mismatch: %#v", manifest)
	}
	for _, rotation := range manifest.SourceRotations {
		if rotation.New == "absent" {
			t.Fatalf("invalid successor source: %#v", rotation)
		}
		bytes, readErr := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rotation.Path)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		sum := sha256.Sum256(bytes)
		if hex.EncodeToString(sum[:]) != rotation.New {
			t.Fatalf("source rotation stale: %#v", rotation)
		}
	}
	oldGolden := mustReadHash(t, filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "replay_output.golden.json"), manifest.Old.Golden)
	newGolden := mustReadHash(t, filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "replay_output.rehearsal-successor.golden.json"), manifest.New.Golden)
	changes := scalarRotations(t, oldGolden, newGolden)
	if len(changes) == 0 {
		t.Fatal("successor golden did not rotate")
	}
	for _, change := range changes {
		if !strings.HasSuffix(change.Field, ".lineage_manifest_id") && !strings.HasSuffix(change.Field, ".lineage_manifest_sha256") {
			t.Fatalf("semantic field changed outside closed allowlist: %#v", change)
		}
		if change.Old != manifest.Old.Lineage || change.New != manifest.New.Lineage {
			t.Fatalf("non-causal lineage change: %#v", change)
		}
	}
}
