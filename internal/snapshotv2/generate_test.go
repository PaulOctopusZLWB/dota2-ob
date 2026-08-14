package snapshotv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	snapshotproduct "github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/product"
)

func TestCompiledV2IsExactGenerationOfIdentityBearingArtifacts(t *testing.T) {
	generated, digests, err := GenerateCompiled(os.DirFS("."), fixedRuntimeDigests(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range GeneratedTargets() {
		checkedIn, readErr := os.ReadFile(target)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(checkedIn, generated[target]) {
			t.Fatalf("compiled V2 source diverges from identity-bearing generation: %s", target)
		}
	}
	compiled := snapshotproduct.SemanticDigests()
	for name, want := range digests {
		if got := compiled[name]; got != want {
			t.Fatalf("compiled semantic digest %s = %s want %s", name, got, want)
		}
	}
}

func TestProductionV2SeamCannotCrossIntoCurrentSemantics(t *testing.T) {
	facade, err := os.ReadFile("../../cmd/dota2-ob/broadcast_runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(facade)
	if !strings.Contains(text, "internal/snapshotv2/compiled/product") {
		t.Fatal("production V2 runtime does not select compiled snapshot implementation")
	}
	for _, forbidden := range []string{"internal/insight", "internal/policy\"", "internal/presentation"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("production V2 seam imports mutable current semantics: %s", forbidden)
		}
	}
	for target, payload := range mustGenerate(t) {
		if !strings.Contains(target, "compiled/product/") {
			continue
		}
		for _, forbidden := range []string{`"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"`, `"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"`, `"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"`} {
			if strings.Contains(string(payload), forbidden) {
				t.Fatalf("generated V2 product %s imports mutable semantics %s", target, forbidden)
			}
		}
	}
}

func mustGenerate(t *testing.T) map[string][]byte {
	t.Helper()
	generated, _, err := GenerateCompiled(os.DirFS("."), fixedRuntimeDigests(t))
	if err != nil {
		t.Fatal(err)
	}
	return generated
}

func TestSemanticMutationCannotRetainCodeAndIdentity(t *testing.T) {
	base := mapFS(t)
	originalCode, originalDigests, err := GenerateCompiled(base, fixedRuntimeDigests(t))
	if err != nil {
		t.Fatal(err)
	}
	mutated := mapFS(t)
	payload := append([]byte(nil), mutated["reference/policy_engine.go.src"].Data...)
	payload = append(payload, []byte("\n// semantic mutation\n")...)
	mutated["reference/policy_engine.go.src"] = &fstest.MapFile{Data: payload}
	mutatedCode, mutatedDigests, err := GenerateCompiled(mutated, fixedRuntimeDigests(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(originalCode["compiled/policy/engine_generated.go"], mutatedCode["compiled/policy/engine_generated.go"]) {
		t.Fatal("semantic mutation retained compiled V2 bytes")
	}
	if originalDigests["policy_engine.go"] == mutatedDigests["policy_engine.go"] {
		t.Fatal("semantic mutation retained V2 identity input")
	}
	originalArtifacts, _, _, originalEngine, err := artifactsFromDigests(originalDigests, "rules")
	if err != nil {
		t.Fatal(err)
	}
	mutatedArtifacts, _, _, mutatedEngine, err := artifactsFromDigests(mutatedDigests, "rules")
	if err != nil {
		t.Fatal(err)
	}
	if originalArtifacts == mutatedArtifacts && originalEngine == mutatedEngine {
		t.Fatal("semantic mutation retained accepted identities")
	}
}

func fixedRuntimeDigests(t *testing.T) map[string]string {
	t.Helper()
	paths := map[string]string{
		"contracts.go":         "../contracts/contracts.go",
		"live_mapping.go":      "../capture/live_observation.go",
		"session_highwater.go": "../session/highwater.go",
		"session_follower.go":  "../session/live_projector.go",
	}
	result := make(map[string]string, len(paths))
	for name, path := range paths {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(payload)
		result[name] = hex.EncodeToString(sum[:])
	}
	return result
}

func mapFS(t *testing.T) fstest.MapFS {
	t.Helper()
	result := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS("reference"), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		payload, readErr := os.ReadFile("reference/" + path)
		if readErr != nil {
			return readErr
		}
		result["reference/"+path] = &fstest.MapFile{Data: payload}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
