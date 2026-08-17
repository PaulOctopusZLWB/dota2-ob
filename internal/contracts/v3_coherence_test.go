package contracts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
)

func TestV3GoldenCrossObjectCoherence(t *testing.T) {
	binding := readV3Golden[contracts.HistoryAvailabilityBindingV1](t, "history_availability_binding_v1.json")
	lineage := readV3Golden[contracts.PolicyLineageManifestV3](t, "policy_lineage_manifest_v3.json")
	commit := readV3Golden[contracts.PolicyCommitV3](t, "policy_commit_v3.json")
	checkpoint := readV3Golden[contracts.PolicyCheckpointV3](t, "policy_checkpoint_v3.json")
	fixture := readV3Golden[contracts.HistoricalUnavailableFixtureV1](t, "historical_unavailable_fixture_v1.json")
	release := readV3Golden[contracts.LiveOnlyReleaseBindingV1](t, "live_only_release_binding_v1.json")
	if err := fixture.ValidateAgainst(binding, lineage); err != nil {
		t.Fatal(err)
	}
	if err := release.ValidateAgainst(binding, lineage); err != nil {
		t.Fatal(err)
	}
	commitHash, err := contracts.CanonicalSHA256(commit)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.ValidateAgainstCommit(commit, commitHash); err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.CommandLocators) != 1 || checkpoint.CommandLocators[0].SegmentID != "00000000000000000001.pcl3" || checkpoint.CommandLocators[0].FrameSHA256 != commitHash {
		t.Fatal("V3 checkpoint locator is not the canonical V3 frame")
	}
	values := insight.EvaluateLiveOnly(insight.LiveOnlyInput{Observation: fixture.Observation, History: binding, Lineage: lineage, PolicyTimeMS: fixture.Observation.Evidence.ReceiveTime.UnixMilli()}, insight.DefaultConfig())
	for _, candidate := range values {
		family := insight.Family(candidate.RuleVersion)
		if family != "objective" && family != "live" {
			t.Fatalf("history family escaped fixture: %s", family)
		}
	}
	copySet := contracts.HistoricalDisabledFamiliesV1()
	copySet[0] = "mutated"
	if contracts.HistoricalDisabledFamiliesV1()[0] != "hero" {
		t.Fatal("normative disabled set is externally mutable")
	}
}

func TestLiveOnlyReleaseRejectsCoherentSnapshotBindingSubstitution(t *testing.T) {
	lineage := readV3Golden[contracts.PolicyLineageManifestV3](t, "policy_lineage_manifest_v3.json")
	release := readV3Golden[contracts.LiveOnlyReleaseBindingV1](t, "live_only_release_binding_v1.json")
	binding := contracts.HistoryAvailabilityBindingV1{
		SchemaVersion:            contracts.HistoryAvailabilityBindingSchemaV1,
		Mode:                     contracts.HistoryModeSnapshotBaseline,
		HistoricalSnapshotID:     strings.Repeat("b", 64),
		HistoricalSnapshotSHA256: strings.Repeat("b", 64),
		EligibleBaselineSHA256:   []string{strings.Repeat("c", 64)},
	}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	bindingID := binding.MustContentID()
	lineage.HistoryAvailabilityBindingID = bindingID
	lineage.HistoryAvailabilityBindingSHA256 = bindingID
	if err := lineage.Validate(); err != nil {
		t.Fatal(err)
	}
	lineageID := lineage.MustContentID()
	release.HistoryAvailabilityBindingID = bindingID
	release.HistoryAvailabilityBindingSHA256 = bindingID
	release.LineageManifestID = lineageID
	release.LineageManifestSHA256 = lineageID
	if err := release.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := release.ValidateAgainst(binding, lineage); err == nil {
		t.Fatal("live-only release accepted coherent snapshot-backed binding substitution")
	}
}

func TestGenerateCoherentV3Goldens(t *testing.T) {
	if os.Getenv("UPDATE_V3_GOLDENS") != "1" {
		t.Skip("set UPDATE_V3_GOLDENS=1 to regenerate")
	}
	binding := readV3Golden[contracts.HistoryAvailabilityBindingV1](t, "history_availability_binding_v1.json")
	fixture := readV3Golden[contracts.HistoricalUnavailableFixtureV1](t, "historical_unavailable_fixture_v1.json")
	fixture.Observation.Evidence.SessionID = "session"
	a := contracts.PolicyArtifactIdentityV2{Version: "v1", ContentSHA256: strings.Repeat("a", 64)}
	bindingID := binding.MustContentID()
	lineage := contracts.PolicyLineageManifestV3{SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: "session", RawRecordSchema: a, RawRecordFraming: a, RawPayloadSchema: a, LiveObservationSchema: a, ProjectionMapping: a, TournamentScopeID: strings.Repeat("a", 64), TournamentScopeSHA256: strings.Repeat("a", 64), HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID, Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(insight.DefaultConfig()), Catalog: a, Terminology: a, LocalizationParameterMapping: a, EngineBuild: a}
	lineageID := lineage.MustContentID()
	config := policy.DefaultConfig()
	config.LineageID = lineageID
	config.CandidateConfigArtifact, config.CandidateRulesArtifact = lineage.Config, lineage.Rules
	config.CandidateConfigVersion = lineage.Config.Version
	engine := policy.New("session", config)
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "disable-objective", SessionID: "session", Action: contracts.ActionDisableRule, TargetRuleID: insight.ObjectiveRule, ExpectedPolicyRevision: 0, PolicyTimeMS: 1}
	commit := engine.ApplyCommandV3(command)
	commitHash, _ := contracts.CanonicalSHA256(commit)
	state := engine.State()
	checkpoint := contracts.PolicyCheckpointV3{SchemaVersion: contracts.PolicyCheckpointSchemaV3, LineageManifestID: lineageID, LineageManifestSHA256: lineageID, SessionID: "session", CommitSequence: 1, ReferencedCommitSHA256: commitHash, LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: engine.StateHash(), CreatedTimeMS: 1, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CommandLocators: []contracts.PolicyCommandLocatorV2{{CommandID: command.CommandID, SegmentID: "00000000000000000001.pcl3", FrameOffset: 0, CommitSequence: 1, FrameSHA256: commitHash}}, CandidateTombstones: state.CandidateTombstones}
	fixture.HistoryAvailabilityBindingID = bindingID
	fixture.HistoryAvailabilityBindingSHA256 = bindingID
	fixture.LineageManifestID = lineageID
	fixture.LineageManifestSHA256 = lineageID
	fixture.DisabledFamilies = contracts.HistoricalDisabledFamiliesV1()
	release := readV3Golden[contracts.LiveOnlyReleaseBindingV1](t, "live_only_release_binding_v1.json")
	release.HistoryAvailabilityBindingID = bindingID
	release.HistoryAvailabilityBindingSHA256 = bindingID
	release.LineageManifestID = lineageID
	release.LineageManifestSHA256 = lineageID
	release.DisabledFamilies = contracts.HistoricalDisabledFamiliesV1()
	release.Rules = lineage.Rules
	release.Config = lineage.Config
	release.Catalog = lineage.Catalog
	release.Terminology = lineage.Terminology
	release.LocalizationParameterMapping = lineage.LocalizationParameterMapping
	for name, value := range map[string]any{"history_availability_binding_v1.json": binding, "policy_lineage_manifest_v3.json": lineage, "policy_commit_v3.json": commit, "policy_checkpoint_v3.json": checkpoint, "historical_unavailable_fixture_v1.json": fixture, "live_only_release_binding_v1.json": release} {
		writeV3Golden(t, name, value)
	}
}

func readV3Golden[T any](t *testing.T, name string) T {
	t.Helper()
	var value T
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := contracts.DecodeStrict(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func writeV3Golden(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := contracts.MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if err := os.WriteFile(filepath.Join("testdata", name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", name+".sha256"), []byte(hex.EncodeToString(sum[:])+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
