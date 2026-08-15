package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
)

const (
	productIdentityMigrationSpec  = "f7e44aea83a9e70b26565c43541ffc78338e66c4"
	rejectedProductIdentityParent = "e37a96f842031d4781e7d26542583a314a56c2b5"
	oldV2EngineBuild              = "743ab914d0cd7111c06257124574b5c9c8e94e46e4435f70629395327059ebed"
	oldV2SampleLineage            = "16b476f7fb63ad0103093914f89acace2f002718be817b329452368d5da19f55"
	oldV3FixtureLineage           = "478a81724a29ab648a9d7e1141a9417902fec7364b56701c50660cac0dc6d145"
	oldProductionGolden           = "538264076c4e5f05b0d97485e3a35c8d41b3bd27369544666de9534bec16e3bf"
	newProductionGolden           = "480ef715e7c04c22315ba8d81369708a430abe6706bc29a221220b813c0275bd"
	immediateRejectedPredecessor  = "d658045e2d30bb757a4e03de96b004844d4499fb"
)

type identityRotation struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Cause string `json:"cause"`
}

type productIdentityMigration struct {
	SchemaVersion        string             `json:"schema_version"`
	AcceptedSpec         string             `json:"accepted_spec"`
	RejectedParent       string             `json:"rejected_parent"`
	ImmediatePredecessor string             `json:"immediate_predecessor"`
	SourceRotations      []identityRotation `json:"source_rotations"`
	DerivedRotations     []identityRotation `json:"derived_rotations"`
	SuccessorRotations   []identityRotation `json:"successor_rotations"`
	GoldenFieldChanges   []identityRotation `json:"golden_field_changes"`
	PreservedIdentities  map[string]string  `json:"preserved_identities"`
}

func TestProductIdentityMigrationManifestIsCompleteAndDeterministic(t *testing.T) {
	manifest := buildProductIdentityMigration(t)
	payload, err := contracts.MarshalCanonical(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "product_identity_migration_manifest.json")
	if os.Getenv("UPDATE_PRODUCT_IDENTITY_MIGRATION") == "1" {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checked, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checked, payload) {
		t.Fatal("product identity migration manifest is stale; regenerate explicitly")
	}
}

func buildProductIdentityMigration(t *testing.T) productIdentityMigration {
	t.Helper()
	oldEngine := sourceArtifact("dota2-ob.product.v3", "e3692639044a02336923228ebefc8cdbced7c98e05157d6e2e06c2587c5c533f", productPortsSourceSHA256,
		productRecoverySourceSHA256, productRuntimeSourceSHA256, "101b80bbacfb33802a1bfa3c23d36e6a9da6f93c491455703390002efe62556c",
		productLiveOnlySourceSHA256, productRecoveryV3SourceSHA256, "811f55a7b63ae532a3738702ee664694bb9b1aa0aeefe04792e6f5efe29a3173",
		sessionHighWaterSourceSHA256, sessionFollowerSourceSHA256, insightEngineSourceSHA256,
		policyEngineSourceSHA256, policyApplicationSourceSHA256, insight.RulesArtifact().ContentSHA256)
	current := testLiveOnlyArtifacts("m4-live-only")
	oldLineage := current.Lineage
	oldLineage.EngineBuild = oldEngine
	oldLineageID := oldLineage.MustContentID()
	if oldLineageID != oldV3FixtureLineage {
		t.Fatalf("reconstructed rejected V3 lineage=%s want %s", oldLineageID, oldV3FixtureLineage)
	}
	newLineageID := current.Lineage.MustContentID()
	oldRelease := current.Release
	oldRelease.LineageManifestID, oldRelease.LineageManifestSHA256 = oldLineageID, oldLineageID
	oldReleaseID, err := oldRelease.ContentID()
	if err != nil {
		t.Fatal(err)
	}
	newReleaseID, err := current.Release.ContentID()
	if err != nil {
		t.Fatal(err)
	}

	oldGoldenPath := filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "replay_output.e37a96f.rejected.golden.json")
	newGoldenPath := filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "replay_output.golden.json")
	oldGolden := mustReadHash(t, oldGoldenPath, oldProductionGolden)
	newGolden := mustReadHash(t, newGoldenPath, newProductionGolden)
	goldenChanges := scalarRotations(t, oldGolden, newGolden)
	if len(goldenChanges) != 168 {
		t.Fatalf("golden scalar changes=%d want 168", len(goldenChanges))
	}
	for _, change := range goldenChanges {
		if change.Old != oldLineageID || change.New != newLineageID {
			t.Fatalf("non-lineage golden rotation: %#v", change)
		}
	}

	return productIdentityMigration{
		SchemaVersion:        "product_identity_migration.v1",
		AcceptedSpec:         productIdentityMigrationSpec,
		RejectedParent:       rejectedProductIdentityParent,
		ImmediatePredecessor: immediateRejectedPredecessor,
		SourceRotations: []identityRotation{
			{Field: "cmd/dota2-ob/product_selector.go", Old: "absent", New: productSelectorSourceSHA256, Cause: "closed production dispatch and current V3 execution share the exact Go flag parser bound to both engine identities"},
			{Field: "cmd/dota2-ob/main.go", Old: "e3692639044a02336923228ebefc8cdbced7c98e05157d6e2e06c2587c5c533f", New: productMainSourceSHA256, Cause: "the unbound product entry moved into the identity-bearing selector and consumes its parsed options"},
			{Field: "cmd/dota2-ob/broadcast_lineage.go", Old: "101b80bbacfb33802a1bfa3c23d36e6a9da6f93c491455703390002efe62556c", New: productLineageSourceSHA256, Cause: "the V3 engine identity now also binds the shared product selector"},
			{Field: "cmd/dota2-ob/broadcast_runtime_v3.go", Old: "811f55a7b63ae532a3738702ee664694bb9b1aa0aeefe04792e6f5efe29a3173", New: productRuntimeV3SourceSHA256, Cause: "startup restore and committed-observation progress publish bounded causal readiness signals"},
		},
		DerivedRotations: []identityRotation{
			{Field: "snapshot_v2.engine_build", Old: oldV2EngineBuild, New: expectedProductLineageArtifacts().engineBuild.ContentSHA256, Cause: "V2 now binds and executes the production selector"},
			{Field: "snapshot_v2.sample_lineage", Old: oldV2SampleLineage, New: "70e39b1abe0e5ffbe6f19bec3c7b3ec58686d61315f871cd358209b8d7ac8851", Cause: "transitive V2 EngineBuild rotation"},
			{Field: "snapshot_v2.sample_durable_commit_1", Old: "2d744a1c4490cddfe3e55112f265df2c89e449694e3336318477989abb2d7927", New: "b2b94a30ed4032a974e816f561da667e2ee21c7f1c22b6872a21f00a67567ef9", Cause: "transitive V2 lineage rotation"},
			{Field: "snapshot_v2.sample_durable_commit_2", Old: "6111f75a1cd19c3772c5f0b4f9c4e43f4e6091146130686628fd5564367f5666", New: "20595c7ef7bab387e06d911a105cdc70ff1bb3eae20c85587306f7edaae38292", Cause: "transitive V2 lineage rotation"},
			{Field: "live_only_v3.engine_build", Old: oldEngine.ContentSHA256, New: current.Lineage.EngineBuild.ContentSHA256, Cause: "V3 now binds the shared product selector and moved entry source"},
			{Field: "m4_fixture.lineage_manifest", Old: oldLineageID, New: newLineageID, Cause: "transitive V3 EngineBuild rotation"},
			{Field: "m4_fixture.release_binding", Old: oldReleaseID, New: newReleaseID, Cause: "transitive V3 lineage rotation"},
			{Field: "m4.production_golden", Old: oldProductionGolden, New: newProductionGolden, Cause: "terminal commits contain the rotated V3 lineage identity"},
		},
		SuccessorRotations: []identityRotation{
			{Field: "cmd/dota2-ob/product_selector.go", Old: "dba26a22bdc96d5f47d4c4d18afaa9694166f85d491f73266a723de5f09ab284", New: "f95cc72da5a97de4060156576e17c77ab694547c866cd00a448b7a216aeac07f", Cause: "selector parity with Go flag parsing"},
			{Field: "cmd/dota2-ob/main.go", Old: "7696fcc6ccbe4843775869568d71236826739082ae34e8e1091c5a156704d8a5", New: productMainSourceSHA256, Cause: "current V3 consumes selector-parsed options without reparsing"},
			{Field: "cmd/dota2-ob/broadcast_runtime_v3.go", Old: "811f55a7b63ae532a3738702ee664694bb9b1aa0aeefe04792e6f5efe29a3173", New: "b51c8d0b9cb830fa9050407fadabfd3c3a999ef1830cc4f1147ec151011050d3", Cause: "causal restore readiness signal"},
			{Field: "snapshot_v2.engine_build", Old: "61746bc2646361125ff75824018b67290da62d6ff0653c9e1e1f850056a46e96", New: "642c6e838f843fb63b7e4faf8c0ed7a680d58f8b6856a079903a52493903e64b", Cause: "transitive selector rotation"},
			{Field: "snapshot_v2.sample_lineage", Old: "8292e6a8deff171829f1a4a443826b54a6b8ffc38219d3d71e1e0f4daf4e4198", New: "7a73872f7476ea20e43804bfd441cb93cf4c4fa99c014860b7dd0af07a3e3725", Cause: "transitive V2 EngineBuild rotation"},
			{Field: "snapshot_v2.sample_durable_commit_1", Old: "921b64e821b094b20337753137e1c28e39623b973173c92e10e6f90ee5aaaf8b", New: "f98a1fb81838492ee516940e94d4b15a1d57a8ff716dd415890fb660e51514e2", Cause: "transitive V2 lineage rotation"},
			{Field: "snapshot_v2.sample_durable_commit_2", Old: "8c0adf5a594ba2013d7115715c01f05a75c88e258e3369fdc4f7b3b36248f61a", New: "9eb044fb3ea3f44c0a8aec8c91b0f3f0871b65ce8e11d645c85944d83fc4b239", Cause: "transitive V2 lineage rotation"},
			{Field: "live_only_v3.engine_build", Old: "71b911ab473bbba9f334c6e2d23b438d9203d960c0441d1ba0408ed7ec3bd2aa", New: "419f5a6e215c1629a7f5060429fa2f40f52619e9abc4849c9db16e3ab719715e", Cause: "selector, current parser consumer, and restore readiness rotations"},
			{Field: "m4_fixture.lineage_manifest", Old: "afd756eb475a23eb8bdf27c7e1a302e8adfac4efeb6d7cfd3f432c9454c68a46", New: "4318bb65fdb32be4bd1de8bae74e6c2ac32cb605bf3d6f58beb38823959d491c", Cause: "transitive V3 EngineBuild rotation"},
			{Field: "m4_fixture.release_binding", Old: "463943630328251da753a103531ec6ac3cf4ffbc19a23897f50e7f3332a37f48", New: "2371d7d2794ccffa24498300b50a8e933e7e3bf0e526840fc1b498b46af95fe7", Cause: "transitive V3 lineage rotation"},
			{Field: "m4.production_golden", Old: "91cb288bec4048f6ae1e6497a433fc0e4c9b4041d5f289f65945ca365cb3d67c", New: "db7794eced282b514bc17e311f8b6e516e3a79738b5b7e01549b85e025f87388", Cause: "terminal commits contain the rotated V3 lineage identity"},
			{Field: "cmd/dota2-ob/product_selector.go", Old: "f95cc72da5a97de4060156576e17c77ab694547c866cd00a448b7a216aeac07f", New: productSelectorSourceSHA256, Cause: "post-parse invalid-mode validation restores exit/stderr compatibility"},
			{Field: "cmd/dota2-ob/broadcast_runtime_v3.go", Old: "b51c8d0b9cb830fa9050407fadabfd3c3a999ef1830cc4f1147ec151011050d3", New: productRuntimeV3SourceSHA256, Cause: "bounded causal readiness uses an authoritative durable-sequence mirror without mutex contention"},
			{Field: "snapshot_v2.engine_build", Old: "642c6e838f843fb63b7e4faf8c0ed7a680d58f8b6856a079903a52493903e64b", New: expectedProductLineageArtifacts().engineBuild.ContentSHA256, Cause: "transitive selector rotation"},
			{Field: "snapshot_v2.sample_lineage", Old: "7a73872f7476ea20e43804bfd441cb93cf4c4fa99c014860b7dd0af07a3e3725", New: "70e39b1abe0e5ffbe6f19bec3c7b3ec58686d61315f871cd358209b8d7ac8851", Cause: "transitive V2 EngineBuild rotation"},
			{Field: "snapshot_v2.sample_durable_commit_1", Old: "f98a1fb81838492ee516940e94d4b15a1d57a8ff716dd415890fb660e51514e2", New: "b2b94a30ed4032a974e816f561da667e2ee21c7f1c22b6872a21f00a67567ef9", Cause: "transitive V2 lineage rotation"},
			{Field: "snapshot_v2.sample_durable_commit_2", Old: "9eb044fb3ea3f44c0a8aec8c91b0f3f0871b65ce8e11d645c85944d83fc4b239", New: "20595c7ef7bab387e06d911a105cdc70ff1bb3eae20c85587306f7edaae38292", Cause: "transitive V2 lineage rotation"},
			{Field: "live_only_v3.engine_build", Old: "419f5a6e215c1629a7f5060429fa2f40f52619e9abc4849c9db16e3ab719715e", New: current.Lineage.EngineBuild.ContentSHA256, Cause: "selector compatibility and bounded readiness rotations"},
			{Field: "m4_fixture.lineage_manifest", Old: "4318bb65fdb32be4bd1de8bae74e6c2ac32cb605bf3d6f58beb38823959d491c", New: newLineageID, Cause: "transitive V3 EngineBuild rotation"},
			{Field: "m4_fixture.release_binding", Old: "2371d7d2794ccffa24498300b50a8e933e7e3bf0e526840fc1b498b46af95fe7", New: newReleaseID, Cause: "transitive V3 lineage rotation"},
			{Field: "m4.production_golden", Old: "db7794eced282b514bc17e311f8b6e516e3a79738b5b7e01549b85e025f87388", New: newProductionGolden, Cause: "terminal commits contain only the rotated V3 lineage identity"},
		},
		GoldenFieldChanges: goldenChanges,
		PreservedIdentities: map[string]string{
			"captured_gsi_schedule":              "2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e",
			"snapshot_v2.sample_candidate":       "596c8f7aa26bec6cb86a6d5f786ec1612855738005d08a61320d792516fbd4e5",
			"snapshot_v2.sample_candidate_bytes": "15732d334ac324e194042c5111aa51246d6aa1f11bb6469c880f39fb1ea8b613",
			"snapshot_v2.sample_command_result":  "7c22fbd3afe98f930d9c7b4206548bbdb01beece31ea6cf40e8a69038249dfa5",
			"contracts":                          "v1/v2/v3 canonical schemas and encodings unchanged",
			"functional_production_evidence":     "all non-lineage golden scalar fields byte-identical",
		},
	}
}

func mustReadHash(t *testing.T, path, want string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("%s sha256=%s want %s", path, got, want)
	}
	return payload
}

func scalarRotations(t *testing.T, oldPayload, newPayload []byte) []identityRotation {
	t.Helper()
	var oldValue, newValue any
	if err := json.Unmarshal(oldPayload, &oldValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(newPayload, &newValue); err != nil {
		t.Fatal(err)
	}
	var result []identityRotation
	collectScalarRotations(t, "$", oldValue, newValue, &result)
	return result
}

func collectScalarRotations(t *testing.T, path string, oldValue, newValue any, result *[]identityRotation) {
	t.Helper()
	if reflect.DeepEqual(oldValue, newValue) {
		return
	}
	switch oldTyped := oldValue.(type) {
	case map[string]any:
		newTyped, ok := newValue.(map[string]any)
		if !ok || len(oldTyped) != len(newTyped) {
			t.Fatalf("golden shape changed at %s", path)
		}
		keys := make([]string, 0, len(oldTyped))
		for key := range oldTyped {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			newChild, exists := newTyped[key]
			if !exists {
				t.Fatalf("golden key removed at %s.%s", path, key)
			}
			collectScalarRotations(t, path+"."+key, oldTyped[key], newChild, result)
		}
	case []any:
		newTyped, ok := newValue.([]any)
		if !ok || len(oldTyped) != len(newTyped) {
			t.Fatalf("golden array shape changed at %s", path)
		}
		for index := range oldTyped {
			collectScalarRotations(t, fmt.Sprintf("%s[%d]", path, index), oldTyped[index], newTyped[index], result)
		}
	default:
		*result = append(*result, identityRotation{Field: path, Old: fmt.Sprint(oldValue), New: fmt.Sprint(newValue), Cause: "transitive V3 lineage rotation in terminal commit"})
	}
}
