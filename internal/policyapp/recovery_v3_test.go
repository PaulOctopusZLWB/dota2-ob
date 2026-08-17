package policyapp_test

import (
	"bytes"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policyapp"
)

func TestRecoverV3RestoresCheckpointContinuationAndDurableDuplicate(t *testing.T) {
	binding, lineage := recoveryLineageV3()
	config := policy.DefaultConfig()
	config.LineageID = lineage.MustContentID()
	config.CandidateConfigArtifact, config.CandidateRulesArtifact = lineage.Config, lineage.Rules
	config.CandidateConfigVersion = lineage.Config.Version
	verifier := commitlog.WithV3ReplayVerifier(commitlog.ReplayVerifierV3{VerifyObservation: func(contracts.PolicyCommitV3) error { return nil }, VerifyCommand: func(contracts.PolicyCommitV3) error { return nil }, Reevaluate: func(contracts.PolicyCommitV3) error { return nil }})
	root := t.TempDir()
	store, _, err := commitlog.OpenV3(root, "session", binding, lineage, verifier)
	if err != nil {
		t.Fatal(err)
	}
	app, err := policy.NewBoundApplicationV3(policy.New("session", config), store, binding, lineage)
	if err != nil {
		t.Fatal(err)
	}
	candidate := recoveryCandidate()
	observation, err := app.EvaluateObservation(1, recoveryHash('1'), recoveryHash('2'), recoveryEvidence(1), []contracts.InsightCandidateV1{candidate}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var anchor commitlog.CommittedV3
	if err := store.VisitAll(func(c commitlog.CommittedV3) error { anchor = c; return nil }); err != nil {
		t.Fatal(err)
	}
	state := app.State()
	cp := contracts.PolicyCheckpointV3{SchemaVersion: contracts.PolicyCheckpointSchemaV3, LineageManifestID: config.LineageID, LineageManifestSHA256: config.LineageID, SessionID: "session", CommitSequence: observation.CommitSequence, ReferencedCommitSHA256: anchor.Hash, LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: app.StateHash(), CreatedTimeMS: 1, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: state.CandidateTombstones}
	if err := store.WriteCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	cmd := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "show-v3", SessionID: "session", Action: contracts.ActionShow, TargetCandidateID: candidate.CandidateID, ExpectedPolicyRevision: state.PolicyRevision, PolicyTimeMS: 2}
	original, err := app.ApplyCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	wantState := app.StateHash()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := commitlog.OpenV3(root, "session", binding, lineage, verifier)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := policyapp.RecoverV3(reopened, "session", binding, lineage, config, nil)
	if err != nil || recovered.StateHash() != wantState {
		t.Fatalf("recover V3: %v", err)
	}
	duplicate, err := recovered.ApplyCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := contracts.MarshalCanonical(original)
	b, _ := contracts.MarshalCanonical(duplicate)
	if !bytes.Equal(a, b) {
		t.Fatal("recovered duplicate changed")
	}
	next, err := recovered.EvaluateObservation(2, recoveryHash('3'), recoveryHash('4'), recoveryEvidence(2), nil, 3)
	if err != nil || next.CommitSequence != original.CommitSequence+1 {
		t.Fatalf("continuation: %#v %v", next, err)
	}
}

func recoveryLineageV3() (contracts.HistoryAvailabilityBindingV1, contracts.PolicyLineageManifestV3) {
	b := contracts.HistoryAvailabilityBindingV1{SchemaVersion: contracts.HistoryAvailabilityBindingSchemaV1, Mode: contracts.HistoryModeNoGo, TerminalOutcome: contracts.HistoricalNoGoOutcome, CodeFoundationCommit: contracts.AcceptedHistoryCodeCommit, EvidenceCommit: contracts.AcceptedHistoryEvidenceCommit, EvidenceIndexSHA256: contracts.AcceptedEvidenceIndexSHA256, ArtifactTreeSHA256: contracts.AcceptedArtifactTreeSHA256, ReplayGateAuditSHA256: contracts.AcceptedReplayGateAuditSHA256, SourceProvenanceSHA256: contracts.AcceptedSourceProvenanceSHA256, DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(), TournamentScopeID: contracts.AcceptedTournamentScopeID, TournamentScopeSHA256: contracts.AcceptedTournamentScopeSHA256, Cutoff: "2026-08-12T00:00:00Z", Trailing90Start: "2026-05-14T00:00:00Z", Trailing180Start: "2026-02-13T00:00:00Z", PatchID: "60", DotaPatch: "7.41"}
	v2 := recoveryManifest()
	id := b.MustContentID()
	return b, contracts.PolicyLineageManifestV3{SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: v2.SessionID, RawRecordSchema: v2.RawRecordSchema, RawRecordFraming: v2.RawRecordFraming, RawPayloadSchema: v2.RawPayloadSchema, LiveObservationSchema: v2.LiveObservationSchema, ProjectionMapping: v2.ProjectionMapping, TournamentScopeID: v2.TournamentScopeID, TournamentScopeSHA256: v2.TournamentScopeSHA256, HistoryAvailabilityBindingID: id, HistoryAvailabilityBindingSHA256: id, Rules: v2.Rules, Config: v2.Config, Catalog: v2.Catalog, Terminology: v2.Terminology, LocalizationParameterMapping: v2.LocalizationParameterMapping, EngineBuild: v2.EngineBuild}
}
