// Package snapshotv2 owns the immutable semantic reference for the accepted
// snapshot-backed V2 product. The reference sources are embedded in the binary
// and are used at runtime to derive the accepted catalog and EngineBuild
// identities. Current V3 implementation sources are deliberately not part of
// this artifact.
package snapshotv2

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const (
	ProductVersion             = "dota2-ob.product.v1"
	CatalogVersion             = "ti15.broadcast-messages.v1"
	TerminologyVersion         = "dota.zh-cn.broadcast.v1"
	LocalizationMappingVersion = "localization_parameter_mapping.v1"
)

//go:embed reference/*.src
var referenceFS embed.FS

var engineOrder = []string{
	"product_main.go", "product_ports.go", "product_recovery.go",
	"product_runtime.go", "product_lineage.go", "session_highwater.go",
	"session_follower.go", "insight_engine.go", "policy_engine.go",
	"policy_application.go",
}

// ReferenceDigests recomputes every semantic-reference digest from the exact
// bytes embedded in this binary. Callers receive a copy.
func ReferenceDigests() (map[string]string, error) {
	entries, err := referenceFS.ReadDir("reference")
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		payload, readErr := referenceFS.ReadFile("reference/" + entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		sum := sha256.Sum256(payload)
		name := entry.Name()[:len(entry.Name())-len(".src")]
		result[name] = hex.EncodeToString(sum[:])
	}
	return result, nil
}

// Artifacts derives the accepted V2 identities from the immutable semantic
// reference. The legacy source_sha256 field name is retained solely because it
// is part of the accepted canonical identity algorithm.
func Artifacts(rulesSHA256 string) (catalog, terminology, localization, engine contracts.PolicyArtifactIdentityV2, err error) {
	digests, err := ReferenceDigests()
	if err != nil {
		return catalog, terminology, localization, engine, err
	}
	return artifactsFromDigests(digests, rulesSHA256)
}

func artifactsFromDigests(digests map[string]string, rulesSHA256 string) (catalog, terminology, localization, engine contracts.PolicyArtifactIdentityV2, err error) {
	catalogDigest, ok := digests["presentation_catalog.go"]
	if !ok {
		return catalog, terminology, localization, engine, fmt.Errorf("snapshot V2 catalog reference missing")
	}
	catalog = sourceArtifact(CatalogVersion, catalogDigest)
	terminology = sourceArtifact(TerminologyVersion, catalogDigest)
	localization = sourceArtifact(LocalizationMappingVersion, catalogDigest)
	ordered := make([]string, 0, len(engineOrder)+1)
	for _, name := range engineOrder {
		digest, exists := digests[name]
		if !exists {
			return catalog, terminology, localization, engine, fmt.Errorf("snapshot V2 semantic reference missing %s", name)
		}
		ordered = append(ordered, digest)
	}
	ordered = append(ordered, rulesSHA256)
	engine = sourceArtifact(ProductVersion, ordered...)
	return catalog, terminology, localization, engine, nil
}

func sourceArtifact(version string, semanticSHA256 ...string) contracts.PolicyArtifactIdentityV2 {
	content, _ := contracts.CanonicalSHA256(struct {
		Version      string   `json:"version"`
		SourceSHA256 []string `json:"source_sha256"`
	}{Version: version, SourceSHA256: append([]string(nil), semanticSHA256...)})
	return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: content}
}

// ReferenceNames returns the stable sorted reference set for audit tooling.
func ReferenceNames() []string {
	digests, err := ReferenceDigests()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(digests))
	for name := range digests {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
