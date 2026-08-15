package snapshotv2

//go:generate go run ./cmd/snapshotv2gen -root ../..

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/format"
	"io/fs"
	"sort"
	"strings"
)

type generatedFile struct {
	Source string
	Target string
	Pkg    string
}

var generatedFiles = []generatedFile{
	{Source: "insight_engine.go", Target: "compiled/insight/engine_generated.go", Pkg: "insight"},
	{Source: "policy_engine.go", Target: "compiled/policy/engine_generated.go", Pkg: "policy"},
	{Source: "policy_application.go", Target: "compiled/policy/application_generated.go", Pkg: "policy"},
	{Source: "presentation_catalog.go", Target: "compiled/presentation/catalog_generated.go", Pkg: "presentation"},
	{Source: "product_main.go", Target: "compiled/product/product_main_generated.go", Pkg: "product"},
	{Source: "product_ports.go", Target: "compiled/product/product_ports_generated.go", Pkg: "product"},
	{Source: "product_recovery.go", Target: "compiled/product/product_recovery_generated.go", Pkg: "product"},
	{Source: "product_runtime.go", Target: "compiled/product/product_runtime_generated.go", Pkg: "product"},
	{Source: "product_lineage.go", Target: "compiled/product/product_lineage_generated.go", Pkg: "product"},
}

// generateCompiled converts the identity-bearing source artifacts into the
// isolated packages built and selected by the V2 product seam. It is pure so
// tests can adversarially mutate an input and prove both code and identity move.
func GenerateCompiled(source fs.FS, fixed map[string]string) (map[string][]byte, map[string]string, error) {
	output := make(map[string][]byte, len(generatedFiles)+1)
	digests := make(map[string]string, len(generatedFiles)+len(fixed)+2)
	for _, item := range generatedFiles {
		payload, err := fs.ReadFile(source, "reference/"+item.Source+".src")
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(payload)
		digests[item.Source] = hex.EncodeToString(sum[:])
		generated := strings.Replace(string(payload), "package main", "package "+item.Pkg, 1)
		if item.Pkg == "product" {
			generated = strings.ReplaceAll(generated, `"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"`, `"github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/insight"`)
			generated = strings.ReplaceAll(generated, `"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"`, `"github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/policy"`)
			generated = strings.ReplaceAll(generated, `"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"`, `"github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/presentation"`)
			if item.Source == "product_lineage.go" {
				generated = strings.Replace(generated,
					`sourceArtifact("dota2-ob.product.v1", productMainSourceSHA256,`,
					`sourceArtifact("dota2-ob.product.v1", productSelectorSourceSHA256, productMainSourceSHA256,`, 1)
			}
			if item.Source == "product_main.go" {
				generated = strings.Replace(generated, "func main() { os.Exit(run(", "func main() { os.Exit(Run(", 1)
				generated = strings.Replace(generated, "func run(args []string, output io.Writer) int", "func Run(args []string, output io.Writer) int", 1)
			}
		}
		formatted, err := format.Source([]byte(generated))
		if err != nil {
			return nil, nil, fmt.Errorf("format %s: %w", item.Source, err)
		}
		output[item.Target] = formatted
	}
	for name, digest := range fixed {
		digests[name] = digest
	}
	fingerprints, err := generateFingerprints(digests)
	if err != nil {
		return nil, nil, err
	}
	output["compiled/product/fingerprints_generated.go"] = fingerprints
	return output, digests, nil
}

func generateFingerprints(digests map[string]string) ([]byte, error) {
	names := []struct{ constant, source string }{
		{"contractsSourceSHA256", "contracts.go"},
		{"liveMappingSourceSHA256", "live_mapping.go"},
		{"productSelectorSourceSHA256", "product_selector.go"},
		{"presentationCatalogSHA256", "presentation_catalog.go"},
		{"productMainSourceSHA256", "product_main.go"},
		{"productPortsSourceSHA256", "product_ports.go"},
		{"productRecoverySourceSHA256", "product_recovery.go"},
		{"productRuntimeSourceSHA256", "product_runtime.go"},
		{"productLineageSourceSHA256", "product_lineage.go"},
		{"sessionHighWaterSourceSHA256", "session_highwater.go"},
		{"sessionFollowerSourceSHA256", "session_follower.go"},
		{"insightEngineSourceSHA256", "insight_engine.go"},
		{"policyEngineSourceSHA256", "policy_engine.go"},
		{"policyApplicationSourceSHA256", "policy_application.go"},
	}
	var body strings.Builder
	body.WriteString("package product\n\nconst (\n")
	for _, name := range names {
		digest, ok := digests[name.source]
		if !ok {
			return nil, fmt.Errorf("missing semantic source %s", name.source)
		}
		fmt.Fprintf(&body, "\t%s = %q\n", name.constant, digest)
	}
	body.WriteString(")\n")
	return format.Source([]byte(body.String()))
}

func GeneratedTargets() []string {
	targets := make([]string, 0, len(generatedFiles)+1)
	for _, item := range generatedFiles {
		targets = append(targets, item.Target)
	}
	targets = append(targets, "compiled/product/fingerprints_generated.go")
	sort.Strings(targets)
	return targets
}
