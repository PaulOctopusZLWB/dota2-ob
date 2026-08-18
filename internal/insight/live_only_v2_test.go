package insight_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
)

func TestLiveOnlyV2ExecutesEightTypedFamilySitesWithoutSemanticDrift(t *testing.T) {
	input, legacy := liveOnlyV2Input(t)
	result := insight.EvaluateLiveOnlyV2(input, insight.DefaultConfig())
	if err := insight.ValidateLiveOnlyEvaluationV2(result, input); err != nil {
		t.Fatal(err)
	}
	wantCandidates := insight.EvaluateLiveOnly(legacy, insight.DefaultConfig())
	if !reflect.DeepEqual(result.Candidates, wantCandidates) {
		t.Fatalf("semantic candidate drift\nnew=%#v\nold=%#v", result.Candidates, wantCandidates)
	}
	wantFamilies := contracts.HistoricalDisabledFamiliesV1()
	if len(result.Executions) != len(wantFamilies) || len(result.Audits) != len(wantFamilies) || result.TerminalReason != "" {
		t.Fatalf("result=%#v", result)
	}
	for index, family := range wantFamilies {
		audit := result.Audits[index]
		execution := result.Executions[index]
		if execution.Family != family || execution.SiteVersion != family+".history_eligibility.v2" || audit.Family != family || audit.SiteVersion != execution.SiteVersion || audit.ExecutionSHA256 == "" || audit.RawSequence != input.Observation.Evidence.Sequence || audit.RawRecordSHA256 != input.RawRecordSHA256 || audit.CandidateEmitted || audit.DecisionEmitted || audit.OverlayOrDisplayEmitted {
			t.Fatalf("audit[%d]=%#v", index, audit)
		}
	}
}

func TestLiveOnlyV2RejectsEveryPopulationAndSpliceAttack(t *testing.T) {
	input, _ := liveOnlyV2Input(t)
	valid := insight.EvaluateLiveOnlyV2(input, insight.DefaultConfig())
	attacks := map[string]func(*insight.LiveOnlyEvaluationV2){
		"missing":                 func(v *insight.LiveOnlyEvaluationV2) { v.Audits = v.Audits[:7] },
		"extra":                   func(v *insight.LiveOnlyEvaluationV2) { v.Audits = append(v.Audits, v.Audits[0]) },
		"duplicate":               func(v *insight.LiveOnlyEvaluationV2) { v.Audits[1] = v.Audits[0] },
		"reordered":               func(v *insight.LiveOnlyEvaluationV2) { v.Audits[0], v.Audits[1] = v.Audits[1], v.Audits[0] },
		"unexecuted":              func(v *insight.LiveOnlyEvaluationV2) { v.Audits[3].SiteVersion = "" },
		"spliced_sequence":        func(v *insight.LiveOnlyEvaluationV2) { v.Audits[4].RawSequence++ },
		"spliced_raw":             func(v *insight.LiveOnlyEvaluationV2) { v.Audits[5].RawRecordSHA256 = strings.Repeat("b", 64) },
		"fallback":                func(v *insight.LiveOnlyEvaluationV2) { v.Audits[6].Reason = "fallback_history" },
		"candidate":               func(v *insight.LiveOnlyEvaluationV2) { v.Audits[7].CandidateEmitted = true },
		"decision":                func(v *insight.LiveOnlyEvaluationV2) { v.Audits[0].DecisionEmitted = true },
		"display":                 func(v *insight.LiveOnlyEvaluationV2) { v.Audits[0].OverlayOrDisplayEmitted = true },
		"audit_without_execution": func(v *insight.LiveOnlyEvaluationV2) { v.Executions = v.Executions[:7] },
		"manufactured_execution":  func(v *insight.LiveOnlyEvaluationV2) { v.Executions[2].DependencyInputSHA256 = strings.Repeat("c", 64) },
		"manufactured_execution_and_audit": func(v *insight.LiveOnlyEvaluationV2) {
			v.Executions[2].DependencyInputSHA256 = strings.Repeat("c", 64)
			v.Audits[2].ExecutionSHA256, _ = contracts.CanonicalSHA256(v.Executions[2])
		},
		"spliced_execution": func(v *insight.LiveOnlyEvaluationV2) { v.Executions[4].RawSequence++ },
	}
	for name, mutate := range attacks {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Audits = append([]insight.FamilySuppressionAuditV1(nil), valid.Audits...)
			candidate.Executions = append([]insight.FamilySiteExecutionV1(nil), valid.Executions...)
			mutate(&candidate)
			if insight.ValidateLiveOnlyEvaluationV2(candidate, input) == nil {
				t.Fatal("attack accepted")
			}
		})
	}
}

func TestLiveOnlyV2GlobalFailureDoesNotFabricateFamilySites(t *testing.T) {
	input, _ := liveOnlyV2Input(t)
	input.Observation.Quality.Flags = []string{"unsafe"}
	result := insight.EvaluateLiveOnlyV2(input, insight.DefaultConfig())
	if result.TerminalReason != insight.FamilySitesNotEvaluated || len(result.Audits) != 0 || len(result.Executions) != 0 {
		t.Fatalf("global result=%#v", result)
	}
	if err := insight.ValidateLiveOnlyEvaluationV2(result, input); err != nil {
		t.Fatal(err)
	}
	input, _ = liveOnlyV2Input(t)
	input.Availability = insight.LiveOnlyAvailabilityV1{}
	result = insight.EvaluateLiveOnlyV2(input, insight.DefaultConfig())
	if result.TerminalReason != insight.FamilySitesNotEvaluated || len(result.Audits) != 0 {
		t.Fatalf("zero availability result=%#v", result)
	}
}

func liveOnlyV2Input(t *testing.T) (insight.LiveOnlyInputV2, insight.LiveOnlyInput) {
	t.Helper()
	now := time.Date(2026, 8, 12, 0, 0, 1, 0, time.UTC)
	observation := completeObservation(now)
	previous := completeObservation(now.Add(-time.Second))
	previous.Evidence.Sequence = 1
	previous.Map.RadiantScore = contracts.Present(int64(1))
	history := contracts.HistoryAvailabilityBindingV1{
		SchemaVersion: contracts.HistoryAvailabilityBindingSchemaV1, Mode: contracts.HistoryModeNoGo, TerminalOutcome: contracts.HistoricalNoGoOutcome,
		CodeFoundationCommit: contracts.AcceptedHistoryCodeCommit, EvidenceCommit: contracts.AcceptedHistoryEvidenceCommit,
		EvidenceIndexSHA256: contracts.AcceptedEvidenceIndexSHA256, ArtifactTreeSHA256: contracts.AcceptedArtifactTreeSHA256,
		ReplayGateAuditSHA256: contracts.AcceptedReplayGateAuditSHA256, SourceProvenanceSHA256: contracts.AcceptedSourceProvenanceSHA256,
		DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(), TournamentScopeID: contracts.AcceptedTournamentScopeID,
		TournamentScopeSHA256: contracts.AcceptedTournamentScopeSHA256, Cutoff: "2026-08-12T00:00:00Z", Trailing90Start: "2026-05-14T00:00:00Z",
		Trailing180Start: "2026-02-13T00:00:00Z", PatchID: "60", DotaPatch: "7.41",
	}
	bindingID := history.MustContentID()
	artifact := contracts.PolicyArtifactIdentityV2{Version: "v1", ContentSHA256: hash('9')}
	lineage := contracts.PolicyLineageManifestV3{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: observation.Evidence.SessionID,
		RawRecordSchema: artifact, RawRecordFraming: artifact, RawPayloadSchema: artifact, LiveObservationSchema: artifact, ProjectionMapping: artifact,
		TournamentScopeID: hash('c'), TournamentScopeSHA256: hash('c'), HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID,
		Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(insight.DefaultConfig()), Catalog: artifact, Terminology: artifact,
		LocalizationParameterMapping: artifact, EngineBuild: artifact,
	}
	availability, err := insight.ProductLiveOnlyAvailabilityV1(history)
	if err != nil {
		t.Fatal(err)
	}
	legacy := insight.LiveOnlyInput{Observation: observation, Previous: &previous, History: history, Lineage: lineage, PolicyTimeMS: now.UnixMilli()}
	return insight.LiveOnlyInputV2{Observation: observation, Previous: &previous, History: history, Lineage: lineage, Availability: availability, RawRecordSHA256: strings.Repeat("a", 64), PolicyTimeMS: now.UnixMilli()}, legacy
}
