package contracts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func TestGoldenContractsStrictRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file string
		new  func() contracts.Contract
	}{
		{"live_observation_v1.json", func() contracts.Contract { return &contracts.LiveObservationV1{} }},
		{"tournament_scope_v1.json", func() contracts.Contract { return &contracts.TournamentScopeV1{} }},
		{"historical_snapshot_manifest_v1.json", func() contracts.Contract { return &contracts.HistoricalSnapshotManifestV1{} }},
		{"historical_baseline_v1.json", func() contracts.Contract { return &contracts.HistoricalBaselineV1{} }},
		{"insight_candidate_v1.json", func() contracts.Contract { return &contracts.InsightCandidateV1{} }},
		{"broadcast_decision_v1.json", func() contracts.Contract { return &contracts.BroadcastDecisionV1{} }},
		{"overlay_state_v1.json", func() contracts.Contract { return &contracts.OverlayStateV1{} }},
		{"operator_command_v1.json", func() contracts.Contract { return &contracts.OperatorCommandV1{} }},
		{"operator_command_result_v1.json", func() contracts.Contract { return &contracts.OperatorCommandResultV1{} }},
		{"audit_event_v1.json", func() contracts.Contract { return &contracts.AuditEventV1{} }},
		{"policy_commit_v1.json", func() contracts.Contract { return &contracts.PolicyCommitV1{} }},
		{"policy_checkpoint_v1.json", func() contracts.Contract { return &contracts.PolicyCheckpointV1{} }},
		{"policy_lineage_manifest_v2.json", func() contracts.Contract { return &contracts.PolicyLineageManifestV2{} }},
		{"policy_commit_v2.json", func() contracts.Contract { return &contracts.PolicyCommitV2{} }},
		{"policy_checkpoint_v2.json", func() contracts.Contract { return &contracts.PolicyCheckpointV2{} }},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			value := tc.new()
			if err := contracts.DecodeStrict(raw, value); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if err := value.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			got, err := contracts.MarshalCanonical(value)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.TrimSpace(raw), got) {
				t.Fatalf("golden bytes are not canonical\nwant %s\n got %s", bytes.TrimSpace(raw), got)
			}
			digest := sha256.Sum256(got)
			wantHash, err := os.ReadFile(filepath.Join("testdata", tc.file+".sha256"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(wantHash)) != hex.EncodeToString(digest[:]) {
				t.Fatalf("golden hash mismatch")
			}
			t.Logf("canonical_sha256=%s", hex.EncodeToString(digest[:]))
			roundTripped := tc.new()
			if err := contracts.DecodeStrict(got, roundTripped); err != nil {
				t.Fatalf("canonical decode: %v", err)
			}
			gotAgain, err := contracts.MarshalCanonical(roundTripped)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, gotAgain) {
				t.Fatal("canonical encoding is not stable")
			}
		})
	}
}

func TestCanonicalReaderCompatibilityAndFloatRejection(t *testing.T) {
	var decision contracts.BroadcastDecisionV1
	input := []byte(`{"session_id":"s","schema_version":"broadcast_decision.v1","resulting_state":"shown","reason":"approved","prior_state":"queued","policy_time_ms":1,"policy_revision":1,"decision_id":"d","command_id":"c"}`)
	if err := contracts.DecodeStrict(input, &decision); err != nil {
		t.Fatal(err)
	}
	if err := decision.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := contracts.MarshalCanonical(decision)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte(`{"command_id":"c","decision_id":"d"`)) {
		t.Fatalf("not RFC 8785 property order: %s", got)
	}
	if _, err := contracts.MarshalCanonical(struct {
		Value float64 `json:"value"`
	}{1.5}); err == nil {
		t.Fatal("float accepted")
	}
}

func TestCanonicalRejectsInvalidUTF8BeforeEncodingOrHashing(t *testing.T) {
	bad := string([]byte{0xff})
	decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: "d", SessionID: "s", PolicyRevision: 1, PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionShown, PolicyTimeMS: 1, Reason: bad}
	value := contracts.PolicyCommitV1{Decisions: []contracts.BroadcastDecisionV1{decision}}
	if _, err := contracts.MarshalCanonical(value); err == nil {
		t.Fatal("invalid UTF-8 was rewritten during canonical encoding")
	}
	if _, err := contracts.CanonicalSHA256(value); err == nil {
		t.Fatal("invalid UTF-8 received a canonical identity")
	}
	if _, err := contracts.MarshalCanonical(map[string]string{bad: "value"}); err == nil {
		t.Fatal("invalid UTF-8 map key was rewritten")
	}
	if err := decision.Validate(); err == nil {
		t.Fatal("invalid UTF-8 contract was accepted for persistence")
	}
}

func TestGoldenCrossContractBindingsAndRecovery(t *testing.T) {
	var scope contracts.TournamentScopeV1
	readGolden(t, "tournament_scope_v1.json", &scope)
	var manifest contracts.HistoricalSnapshotManifestV1
	readGolden(t, "historical_snapshot_manifest_v1.json", &manifest)
	if err := manifest.ValidateAgainstScope(scope); err != nil {
		t.Fatal(err)
	}
	var baseline contracts.HistoricalBaselineV1
	readGolden(t, "historical_baseline_v1.json", &baseline)
	if err := baseline.ValidateAgainst(manifest); err != nil {
		t.Fatal(err)
	}
	var commit contracts.PolicyCommitV1
	readGolden(t, "policy_commit_v1.json", &commit)
	var checkpoint contracts.PolicyCheckpointV1
	readGolden(t, "policy_checkpoint_v1.json", &checkpoint)
	if err := checkpoint.ValidateAgainstCommit(commit); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.EmergencyHide || len(checkpoint.CommandResults) != 1 || len(checkpoint.Pins) != 1 {
		t.Fatal("recovery fixture omits emergency-hide/idempotency/pin state")
	}
	var lineageV2 contracts.PolicyLineageManifestV2
	readGolden(t, "policy_lineage_manifest_v2.json", &lineageV2)
	var commitV2 contracts.PolicyCommitV2
	readGolden(t, "policy_commit_v2.json", &commitV2)
	var checkpointV2 contracts.PolicyCheckpointV2
	readGolden(t, "policy_checkpoint_v2.json", &checkpointV2)
	lineageID, err := lineageV2.ContentID()
	if err != nil {
		t.Fatal(err)
	}
	commitHash, err := contracts.CanonicalSHA256(commitV2)
	if err != nil {
		t.Fatal(err)
	}
	if commitV2.LineageManifestID != lineageID || checkpointV2.LineageManifestID != lineageID {
		t.Fatal("V2 recovery goldens are not bound to the lineage content")
	}
	if err := checkpointV2.ValidateAgainstCommit(commitV2, commitHash); err != nil {
		t.Fatal(err)
	}
}

func TestPersistedContractTypesContainNoFloatsOrMaps(t *testing.T) {
	types := []reflect.Type{reflect.TypeOf(contracts.TournamentScopeV1{}), reflect.TypeOf(contracts.LiveObservationV1{}), reflect.TypeOf(contracts.HistoricalSnapshotManifestV1{}), reflect.TypeOf(contracts.HistoricalBaselineV1{}), reflect.TypeOf(contracts.InsightCandidateV1{}), reflect.TypeOf(contracts.BroadcastDecisionV1{}), reflect.TypeOf(contracts.OverlayStateV1{}), reflect.TypeOf(contracts.OperatorCommandV1{}), reflect.TypeOf(contracts.OperatorCommandResultV1{}), reflect.TypeOf(contracts.AuditEventV1{}), reflect.TypeOf(contracts.PolicyCommitV1{}), reflect.TypeOf(contracts.PolicyCheckpointV1{}), reflect.TypeOf(contracts.PolicyLineageManifestV2{}), reflect.TypeOf(contracts.PolicyCommitV2{}), reflect.TypeOf(contracts.PolicyCheckpointV2{}), reflect.TypeOf(contracts.PolicyStateV2{})}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		if typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64 || typ.Kind() == reflect.Map {
			t.Errorf("persisted contract contains forbidden %s: %s", typ.Kind(), typ)
			return
		}
		if typ.Kind() == reflect.Struct && typ.PkgPath() == "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts" {
			for i := 0; i < typ.NumField(); i++ {
				visit(typ.Field(i).Type)
			}
		}
	}
	for _, typ := range types {
		visit(typ)
	}
}

func readGolden(t *testing.T, name string, dst any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := contracts.DecodeStrict(raw, dst); err != nil {
		t.Fatal(err)
	}
}

func TestStrictDecodeRejectsUnknownAndMismatchedVersion(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("testdata", "overlay_state_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(valid, []byte(`"schema_version":"overlay_state.v1"`), []byte(`"schema_version":"overlay_state.v1","raw_payload":{}`), 1)
	if err := contracts.DecodeStrict(unknown, &contracts.OverlayStateV1{}); err == nil {
		t.Fatal("unknown field accepted")
	}
	wrong := bytes.Replace(valid, []byte("overlay_state.v1"), []byte("overlay_state.v2"), 1)
	var state contracts.OverlayStateV1
	if err := contracts.DecodeStrict(wrong, &state); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	if err := state.Validate(); err == nil {
		t.Fatal("schema mismatch accepted")
	}
}

func TestContractSizeBounds(t *testing.T) {
	tests := []struct {
		name  string
		value contracts.Contract
	}{
		{"overlay", &contracts.OverlayStateV1{SchemaVersion: contracts.OverlayStateSchemaV1, SessionID: "s", PublicationTimeMS: 1, StaleDeadlineMS: 2, Visibility: "hidden", HealthCode: strings.Repeat("x", contracts.MaxOverlayBytes)}},
		{"audit", &contracts.AuditEventV1{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "e", SessionID: "s", EventType: "candidate_created", PolicyTimeMS: 1, CandidateID: "c", Reason: strings.Repeat("x", contracts.MaxAuditEventBytes)}},
		{"policy", &contracts.PolicyCommitV1{SchemaVersion: contracts.PolicyCommitSchemaV1, SessionID: "s", CommitSequence: 1, ObservationSequence: 1, ResultingStateHash: strings.Repeat("a", 64), Publication: contracts.PublicationUnchanged, AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "e", SessionID: "s", EventType: "candidate_created", PolicyTimeMS: 1, CandidateID: "c", Reason: strings.Repeat("x", contracts.MaxPolicyCommitBytes)}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.value.Validate(); err == nil || !strings.Contains(err.Error(), "size") {
				t.Fatalf("want size error, got %v", err)
			}
		})
	}
}

func TestOverlayRejectsForbiddenFieldsAndMarkup(t *testing.T) {
	state := contracts.OverlayStateV1{
		SchemaVersion: contracts.OverlayStateSchemaV1, SessionID: "s", PublicationTimeMS: 1,
		StaleDeadlineMS: 2, Visibility: "visible", DecisionID: "d", Claim: &contracts.OverlayClaimV1{
			Title: "<script>alert(1)</script>", Body: "safe", AssetKey: "hero.axe",
		},
	}
	if err := state.Validate(); err == nil {
		t.Fatal("markup accepted")
	}
}

func TestObservedNullabilityRejectsInconsistentStateAndValue(t *testing.T) {
	state := contracts.OverlayStateV1{
		SchemaVersion: contracts.OverlayStateSchemaV1, SessionID: "s", PublicationTimeMS: 1,
		StaleDeadlineMS: 2, Visibility: "visible", DecisionID: "d", Confidence: "observed", SourceReceiveTime: ptrTime(time.Unix(1, 0).UTC()), Claim: &contracts.OverlayClaimV1{Title: "safe", Body: "safe"},
		Evidence: []contracts.EvidenceRefV1{{
			RecordSchemaVersion: 2, SessionID: "s", Sequence: 1, ReceiveTime: time.Unix(1, 0).UTC(), Source: "gsi",
			ProviderVersion: contracts.ObservedV1[int64]{State: contracts.ValuePresent}, RawPayloadSHA256: strings.Repeat("a", 64),
		}},
	}
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "observed") {
		t.Fatalf("want observed state error, got %v", err)
	}
}
