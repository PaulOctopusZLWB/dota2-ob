package policyapp_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policyapp"
)

func TestRecoverContinuesCheckpointAndReturnsExactDurableDuplicate(t *testing.T) {
	manifest := recoveryManifest()
	config := policy.DefaultConfig()
	config.LineageID = manifest.MustContentID()
	verifier := commitlog.WithV2ReplayVerifier(commitlog.ReplayVerifierV2{VerifyObservation: func(contracts.PolicyCommitV2) error { return nil }, VerifyCommand: func(contracts.PolicyCommitV2) error { return nil }, Reevaluate: func(contracts.PolicyCommitV2) error { return nil }})
	root := t.TempDir()
	store, _, err := commitlog.OpenV2(root, "session", manifest, verifier)
	if err != nil {
		t.Fatal(err)
	}
	engine := policy.New("session", config)
	candidate := recoveryCandidate()
	evidence := recoveryEvidence(1)
	observation := engine.EvaluateObservation(1, recoveryHash('1'), recoveryHash('2'), evidence, []contracts.InsightCandidateV1{candidate}, 1)
	committed, err := store.Append(observation)
	if err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	checkpoint := contracts.PolicyCheckpointV2{SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: config.LineageID, LineageManifestSHA256: config.LineageID, SessionID: "session", CommitSequence: observation.CommitSequence, ReferencedCommitSHA256: committed.Hash, LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: engine.StateHash(), CreatedTimeMS: 1, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: state.CandidateTombstones}
	if err := store.WriteCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	app := policy.NewApplication(engine, store)
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "show-1", SessionID: "session", Action: contracts.ActionShow, TargetCandidateID: candidate.CandidateID, ExpectedPolicyRevision: state.PolicyRevision, PolicyTimeMS: 2}
	original, err := app.ApplyCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := app.StateHash()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := commitlog.OpenV2(root, "session", manifest, verifier)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := policyapp.Recover(reopened, "session", config, func(contracts.PolicyCommitV2) ([]contracts.InsightCandidateV1, error) {
		return []contracts.InsightCandidateV1{candidate}, nil
	})
	if err != nil || recovered.StateHash() != wantHash {
		t.Fatalf("recovery diverged: %v", err)
	}
	duplicate, err := recovered.ApplyCommand(command)
	want, _ := contracts.MarshalCanonical(original)
	got, _ := contracts.MarshalCanonical(duplicate)
	if err != nil || !bytes.Equal(want, got) || recovered.StateHash() != wantHash {
		t.Fatalf("durable duplicate diverged: %v", err)
	}
}

func recoveryManifest() contracts.PolicyLineageManifestV2 {
	artifact := func(version string, digit byte) contracts.PolicyArtifactIdentityV2 {
		return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: recoveryHash(digit)}
	}
	return contracts.PolicyLineageManifestV2{SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: "session", RawRecordSchema: artifact("raw.v2", '1'), RawRecordFraming: artifact("jsonl.v1", '2'), RawPayloadSchema: artifact("gsi.v1", '3'), LiveObservationSchema: artifact("live.v1", '4'), ProjectionMapping: artifact("mapping.v1", '5'), TournamentScopeID: recoveryHash('6'), TournamentScopeSHA256: recoveryHash('6'), HistoricalSnapshotID: recoveryHash('7'), HistoricalSnapshotSHA256: recoveryHash('7'), EligibleBaselineSHA256: []string{recoveryHash('8')}, Rules: artifact("rules.v1", 'a'), Config: artifact("config.v1", 'b'), Catalog: artifact("catalog.v1", 'c'), Terminology: artifact("terms.v1", 'd'), LocalizationParameterMapping: artifact("params.v1", 'e'), EngineBuild: artifact("build.v1", 'f')}
}
func recoveryCandidate() contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{SchemaVersion: contracts.InsightCandidateSchemaV1, CandidateID: "candidate-1", SessionID: "session", RuleVersion: "draft.v1", ConfigVersion: "config.v1", LocalizationKey: "insight.draft", Evidence: []contracts.EvidenceRefV1{recoveryEvidence(1)}, Confidence: "high", Priority: 10, CreatedTimeMS: 1, ExpiryTimeMS: 100, Availability: "available"}
}
func recoveryEvidence(sequence uint64) contracts.EvidenceRefV1 {
	return contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "session", Sequence: sequence, ReceiveTime: time.Unix(1, 0).UTC(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: recoveryHash('9')}
}
func recoveryHash(digit byte) string { return strings.Repeat(string(digit), 64) }
