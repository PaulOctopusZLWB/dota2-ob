package contracts

import (
	"bytes"
	"strings"
	"testing"
)

func validNoGoBindingV1() HistoryAvailabilityBindingV1 {
	return HistoryAvailabilityBindingV1{
		SchemaVersion: HistoryAvailabilityBindingSchemaV1, Mode: HistoryModeNoGo,
		TerminalOutcome: HistoricalNoGoOutcome, CodeFoundationCommit: AcceptedHistoryCodeCommit,
		EvidenceCommit: AcceptedHistoryEvidenceCommit, EvidenceIndexSHA256: AcceptedEvidenceIndexSHA256,
		ArtifactTreeSHA256: AcceptedArtifactTreeSHA256, ReplayGateAuditSHA256: AcceptedReplayGateAuditSHA256,
		SourceProvenanceSHA256: AcceptedSourceProvenanceSHA256,
		DisabledFamilies:       HistoricalDisabledFamiliesV1(),
		TournamentScopeID:      AcceptedTournamentScopeID, TournamentScopeSHA256: AcceptedTournamentScopeSHA256,
		Cutoff:           "2026-08-12T00:00:00Z",
		Trailing90Start:  "2026-05-14T00:00:00Z",
		Trailing180Start: "2026-02-13T00:00:00Z", PatchID: "60", DotaPatch: "7.41",
	}
}

func TestHistoryAvailabilityBindingV1CanonicalClosedUnion(t *testing.T) {
	v := validNoGoBindingV1()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	id, err := v.ContentID()
	if err != nil || len(id) != 64 {
		t.Fatalf("content ID %q: %v", id, err)
	}
	b, _ := MarshalCanonical(v)
	got, err := DecodeCanonicalHistoryAvailabilityBindingV1(b)
	if err != nil || got.MustContentID() != id {
		t.Fatalf("round trip: %v", err)
	}
	for name, data := range map[string][]byte{
		"unknown":      append(bytes.TrimSuffix(b, []byte("}")), []byte(",\"unknown\":true}")...),
		"duplicate":    bytes.Replace(b, []byte("\"mode\":\"historical_no_go\""), []byte("\"mode\":\"historical_no_go\",\"mode\":\"historical_no_go\""), 1),
		"noncanonical": append([]byte(" "), b...),
	} {
		if _, err := DecodeCanonicalHistoryAvailabilityBindingV1(data); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	bad := v
	bad.DisabledFamilies = bad.DisabledFamilies[:7]
	if bad.Validate() == nil {
		t.Fatal("incomplete family set accepted")
	}
	bad = v
	bad.HistoricalSnapshotID = strings.Repeat("a", 64)
	bad.HistoricalSnapshotSHA256 = bad.HistoricalSnapshotID
	if bad.Validate() == nil {
		t.Fatal("snapshot substitution accepted")
	}
}

func TestPolicyLineageManifestV3BindsNoGoContent(t *testing.T) {
	binding := validNoGoBindingV1()
	bindingID := binding.MustContentID()
	h := strings.Repeat("a", 64)
	a := PolicyArtifactIdentityV2{Version: "v1", ContentSHA256: h}
	v := PolicyLineageManifestV3{SchemaVersion: PolicyLineageManifestSchemaV3, SessionID: "session", RawRecordSchema: a, RawRecordFraming: a, RawPayloadSchema: a, LiveObservationSchema: a, ProjectionMapping: a, TournamentScopeID: h, TournamentScopeSHA256: h, HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID, Rules: a, Config: a, Catalog: a, Terminology: a, LocalizationParameterMapping: a, EngineBuild: a}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	changed := v
	changed.HistoryAvailabilityBindingSHA256 = strings.Repeat("b", 64)
	if changed.Validate() == nil {
		t.Fatal("binding mismatch accepted")
	}
}

func TestV3CommitDoesNotBroadenV2Semantics(t *testing.T) {
	v := PolicyCommitV3{SchemaVersion: PolicyCommitSchemaV3}
	if v.Validate() == nil {
		t.Fatal("empty V3 commit accepted")
	}
	v.SchemaVersion = PolicyCommitSchemaV2
	if v.Validate() == nil {
		t.Fatal("V2 tag accepted by V3")
	}
}
