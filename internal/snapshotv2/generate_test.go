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
	selector, err := os.ReadFile("../../cmd/dota2-ob/product_selector.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(selector)
	if !strings.Contains(text, "internal/snapshotv2/compiled/product") || !strings.Contains(text, "runProducts(args, output, snapshotproduct.Run") {
		t.Fatal("actual binary V2 entry does not select the complete compiled snapshot product")
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
	generated := mustGenerate(t)
	completePath := string(generated["compiled/product/product_main_generated.go"]) + string(generated["compiled/product/product_ports_generated.go"])
	for _, required := range []string{"func Run(args []string, output io.Writer) int", "func runWithDependencies(", "loadPolicyLineage(", "newBroadcastRuntime(", "newPolicyProjectionRunner(", "session.NewLiveFollower(", "deps.runLifecycle("} {
		if !strings.Contains(completePath, required) {
			t.Fatalf("generated V2 product omits executable semantic %q", required)
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
	originalCode, originalDigests, err := GenerateCompiled(mapFS(t), fixedRuntimeDigests(t))
	if err != nil {
		t.Fatal(err)
	}
	originalCatalog, originalTerminology, originalLocalization, originalEngine, err := artifactsFromDigests(originalDigests, "rules")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, source, target string
		fixed                bool
	}{
		{name: "actual binary selector", source: "product_selector.go", target: "compiled/product/fingerprints_generated.go", fixed: true},
		{name: "entry and lifecycle", source: "product_main.go", target: "compiled/product/product_main_generated.go"},
		{name: "lineage", source: "product_lineage.go", target: "compiled/product/product_lineage_generated.go"},
		{name: "projection follower and ports", source: "product_ports.go", target: "compiled/product/product_ports_generated.go"},
		{name: "runtime", source: "product_runtime.go", target: "compiled/product/product_runtime_generated.go"},
		{name: "recovery", source: "product_recovery.go", target: "compiled/product/product_recovery_generated.go"},
		{name: "policy", source: "policy_engine.go", target: "compiled/policy/engine_generated.go"},
		{name: "presentation", source: "presentation_catalog.go", target: "compiled/presentation/catalog_generated.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedFS := mapFS(t)
			fixed := fixedRuntimeDigests(t)
			if test.fixed {
				fixed[test.source] = strings.Repeat("a", 64)
			} else {
				path := "reference/" + test.source + ".src"
				payload := append([]byte(nil), mutatedFS[path].Data...)
				payload = append(payload, []byte("\n// adversarial executable semantic mutation\n")...)
				mutatedFS[path] = &fstest.MapFile{Data: payload}
			}
			mutatedCode, mutatedDigests, generateErr := GenerateCompiled(mutatedFS, fixed)
			if generateErr != nil {
				t.Fatal(generateErr)
			}
			if bytes.Equal(originalCode[test.target], mutatedCode[test.target]) {
				t.Fatalf("%s mutation retained compiled V2 bytes", test.source)
			}
			if originalDigests[test.source] == mutatedDigests[test.source] {
				t.Fatalf("%s mutation retained V2 identity input", test.source)
			}
			mutatedCatalog, mutatedTerminology, mutatedLocalization, mutatedEngine, artifactErr := artifactsFromDigests(mutatedDigests, "rules")
			if artifactErr != nil {
				t.Fatal(artifactErr)
			}
			if originalCatalog == mutatedCatalog && originalTerminology == mutatedTerminology && originalLocalization == mutatedLocalization && originalEngine == mutatedEngine {
				t.Fatalf("%s mutation retained every V2 identity", test.source)
			}
		})
	}
}

func fixedRuntimeDigests(t *testing.T) map[string]string {
	t.Helper()
	paths := map[string]string{
		"contracts.go":         "../contracts/contracts.go",
		"live_mapping.go":      "../capture/live_observation.go",
		"product_selector.go":  "../../cmd/dota2-ob/product_selector.go",
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
