package commitlog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

func TestOpenV3SealsExactBindingBeforeManifestAndRejectsSubstitution(t *testing.T) {
	root := t.TempDir()
	binding, manifest := validLineageV3()
	store, _, err := commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "session")
	for _, name := range []string{"history-binding.v1.json", "lineage.v3.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("sealed %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("sealed %s mode %#o", name, info.Mode().Perm())
		}
	}
	store, _, err = commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3()))
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	_ = store.Close()
	corrupt := append([]byte(nil), []byte("{}")...)
	if err := os.WriteFile(filepath.Join(dir, "history-binding.v1.json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3())); err == nil {
		t.Fatal("binding substitution accepted")
	}
}

func validLineageV3() (contracts.HistoryAvailabilityBindingV1, contracts.PolicyLineageManifestV3) {
	b := contracts.HistoryAvailabilityBindingV1{SchemaVersion: contracts.HistoryAvailabilityBindingSchemaV1, Mode: contracts.HistoryModeNoGo, TerminalOutcome: contracts.HistoricalNoGoOutcome, CodeFoundationCommit: contracts.AcceptedHistoryCodeCommit, EvidenceCommit: contracts.AcceptedHistoryEvidenceCommit, EvidenceIndexSHA256: contracts.AcceptedEvidenceIndexSHA256, ArtifactTreeSHA256: contracts.AcceptedArtifactTreeSHA256, ReplayGateAuditSHA256: contracts.AcceptedReplayGateAuditSHA256, SourceProvenanceSHA256: contracts.AcceptedSourceProvenanceSHA256, DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(), TournamentScopeID: contracts.AcceptedTournamentScopeID, TournamentScopeSHA256: contracts.AcceptedTournamentScopeSHA256, Cutoff: "2026-08-12T00:00:00Z", Trailing90Start: "2026-05-14T00:00:00Z", Trailing180Start: "2026-02-13T00:00:00Z", PatchID: "60", DotaPatch: "7.41"}
	v2 := validManifestForStore()
	id := b.MustContentID()
	v3 := contracts.PolicyLineageManifestV3{SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: v2.SessionID, RawRecordSchema: v2.RawRecordSchema, RawRecordFraming: v2.RawRecordFraming, RawPayloadSchema: v2.RawPayloadSchema, LiveObservationSchema: v2.LiveObservationSchema, ProjectionMapping: v2.ProjectionMapping, TournamentScopeID: v2.TournamentScopeID, TournamentScopeSHA256: v2.TournamentScopeSHA256, HistoryAvailabilityBindingID: id, HistoryAvailabilityBindingSHA256: id, Rules: v2.Rules, Config: v2.Config, Catalog: v2.Catalog, Terminology: v2.Terminology, LocalizationParameterMapping: v2.LocalizationParameterMapping, EngineBuild: v2.EngineBuild}
	return b, v3
}

func trustedVerifierV3() commitlog.ReplayVerifierV3 {
	return commitlog.ReplayVerifierV3{VerifyObservation: func(contracts.PolicyCommitV3) error { return nil }, VerifyCommand: func(contracts.PolicyCommitV3) error { return nil }, Reevaluate: func(contracts.PolicyCommitV3) error { return nil }}
}
