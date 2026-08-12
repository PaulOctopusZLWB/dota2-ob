package contracts_test

import (
	"bytes"
	"os"
	"path/filepath"
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
		{"audit", &contracts.AuditEventV1{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "e", SessionID: "s", EventType: "candidate_created", PolicyTimeMS: 1, Reason: strings.Repeat("x", contracts.MaxAuditEventBytes)}},
		{"policy", &contracts.PolicyCommitV1{SchemaVersion: contracts.PolicyCommitSchemaV1, SessionID: "s", CommitSequence: 1, ResultingStateHash: strings.Repeat("a", 64), Publication: "unchanged", AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "e", SessionID: "s", EventType: "candidate_created", PolicyTimeMS: 1, Reason: strings.Repeat("x", contracts.MaxPolicyCommitBytes)}}}},
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
		StaleDeadlineMS: 2, Visibility: "visible", DecisionID: "d", Claim: &contracts.OverlayClaimV1{Title: "safe", Body: "safe"},
		Evidence: []contracts.EvidenceRefV1{{
			RecordSchemaVersion: 2, SessionID: "s", Sequence: 1, ReceiveTime: time.Unix(1, 0).UTC(), Source: "gsi",
			ProviderVersion: contracts.ObservedV1[int64]{State: contracts.ValuePresent}, RawPayloadSHA256: strings.Repeat("a", 64),
		}},
	}
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "observed") {
		t.Fatalf("want observed state error, got %v", err)
	}
}
