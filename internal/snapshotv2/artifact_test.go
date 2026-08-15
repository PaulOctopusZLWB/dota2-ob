package snapshotv2

import (
	"reflect"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	snapshotproduct "github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/product"
)

const acceptedRulesSHA256 = "0c62db7212ed7d6bc0e17351c14571803899ee4ff67ce378cf265c6fd3dafa67"

var acceptedReferenceDigests = map[string]string{
	"insight_engine.go":       "03fe0d238c1bb1eb166414e1baa060362356fcdfe964cc968808107e63e7f55f",
	"policy_application.go":   "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771",
	"policy_engine.go":        "805386b3fd50c58aa1435f791932336c6aeb40ff6d28feb1cdceeaa8d25cf6cb",
	"presentation_catalog.go": "643bbab16fe6576f5be16ee0e71127a58c799730e6754b5ef99178fd91693183",
	"product_lineage.go":      "426f51938697789b97f7599339ba73585d3e64684a444320493b7d4a9eb5e572",
	"product_main.go":         "658cc654f9284889a07dc9c49a070529283f60b013b526544fc5a3fedb503c5f",
	"product_ports.go":        "238b095c9a9196feb438d3180caccf302f3eb646ecf7a21e9ee3c048ea3d3d66",
	"product_recovery.go":     "a8600fb5ebb6acf74e96dec17219effc4bf89c9cc6fca126a475f72e0da602c0",
	"product_runtime.go":      "6c9c390d3e72596a95d7c0ae7189ed2bf77c1fbc93ddc5342d6bcfaa2f055410",
	"session_follower.go":     "022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435",
	"session_highwater.go":    "8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7",
}

func TestEmbeddedReferenceAndAcceptedIdentitiesArePinned(t *testing.T) {
	digests, err := ReferenceDigests()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(digests, acceptedReferenceDigests) {
		t.Fatalf("embedded snapshot V2 semantic reference changed:\n got %#v\nwant %#v", digests, acceptedReferenceDigests)
	}
	rulesSHA256 := insight.RulesArtifact().ContentSHA256
	if rulesSHA256 != acceptedRulesSHA256 {
		t.Fatalf("snapshot V2 rules identity=%s want %s", rulesSHA256, acceptedRulesSHA256)
	}
	catalog, terminology, localization, engine, err := Artifacts(rulesSHA256)
	if err != nil {
		t.Fatal(err)
	}
	assertArtifact(t, catalog.ContentSHA256, "a7815d69b3ffbfe035912c8783639da17cf364ab765302515c4c607acc8177b4")
	assertArtifact(t, terminology.ContentSHA256, "f94b0aa4baba38873a8e1974abea94bee2c503013fd8ab3bfc4295546dba4116")
	assertArtifact(t, localization.ContentSHA256, "920569d53c0dd15fe3838b291accd6d63f6f2657cd70166450722f692404ddaa")
	assertArtifact(t, engine.ContentSHA256, "642c6e838f843fb63b7e4faf8c0ed7a680d58f8b6856a079903a52493903e64b")
}

func TestEmbeddedReferenceSubstitutionChangesRuntimeIdentity(t *testing.T) {
	digests := snapshotproduct.SemanticDigests()
	_, _, _, accepted, err := artifactsFromDigests(digests, insight.RulesArtifact().ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	digests["product_runtime.go"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, _, _, substituted, err := artifactsFromDigests(digests, insight.RulesArtifact().ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if substituted == accepted {
		t.Fatal("substituted embedded V2 semantics retained the accepted EngineBuild identity")
	}
}

func TestCurrentRulesSubstitutionCannotEnterCompiledV2Identity(t *testing.T) {
	if _, _, _, _, err := Artifacts("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("substituted current rules identity entered compiled V2 lineage")
	}
}

func assertArtifact(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("artifact identity=%s want %s", got, want)
	}
}
