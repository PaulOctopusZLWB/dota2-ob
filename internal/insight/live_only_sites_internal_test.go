package insight

import (
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func TestEachProductionFamilySiteConsumesItsOwnTypedDependency(t *testing.T) {
	base := productionSiteInput(t)
	mutations := []struct {
		family string
		apply  func(*LiveOnlyAvailabilityV1)
	}{
		{"hero", func(v *LiveOnlyAvailabilityV1) { v.hero.dependency.mode = "available" }},
		{"item", func(v *LiveOnlyAvailabilityV1) { v.item.dependency.mode = "available" }},
		{"lane", func(v *LiveOnlyAvailabilityV1) { v.lane.dependency.mode = "available" }},
		{"patch", func(v *LiveOnlyAvailabilityV1) { v.patch.dependency.mode = "available" }},
		{"player", func(v *LiveOnlyAvailabilityV1) { v.player.dependency.mode = "available" }},
		{"player_hero", func(v *LiveOnlyAvailabilityV1) { v.playerHero.dependency.mode = "available" }},
		{"role", func(v *LiveOnlyAvailabilityV1) { v.role.dependency.mode = "available" }},
		{"team", func(v *LiveOnlyAvailabilityV1) { v.team.dependency.mode = "available" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.family, func(t *testing.T) {
			candidate := base
			candidate.Availability = base.Availability
			mutation.apply(&candidate.Availability)
			result := EvaluateLiveOnlyV2(candidate, DefaultConfig())
			if result.TerminalReason != FamilySitesNotEvaluated || len(result.Executions) != 0 || len(result.Audits) != 0 {
				t.Fatalf("dependency bypass emitted evidence: %#v", result)
			}
			if ValidateLiveOnlyEvaluationV2(result, candidate) != nil {
				t.Fatal("closed family-sites terminal did not validate")
			}
		})
	}
}

func TestFamilySiteExecutionHashIsCausalToFamilyObservation(t *testing.T) {
	base := productionSiteInput(t)
	baseline := EvaluateLiveOnlyV2(base, DefaultConfig())
	if err := ValidateLiveOnlyEvaluationV2(baseline, base); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		family string
		apply  func(*contracts.LiveObservationV1)
	}{
		{"hero", func(o *contracts.LiveObservationV1) {
			o.Participants[0].HeroName = contracts.Present("npc_dota_hero_axe")
		}},
		{"item", func(o *contracts.LiveObservationV1) {
			o.Participants[0].Items = append(o.Participants[0].Items, contracts.ItemObservationV1{Slot: "slot0"})
		}},
		{"lane", func(o *contracts.LiveObservationV1) { o.ClockBasis = "game_time" }},
		{"patch", func(o *contracts.LiveObservationV1) { o.Evidence.ProviderVersion = contracts.Present(int64(2)) }},
		{"player", func(o *contracts.LiveObservationV1) {
			o.Participants = append(o.Participants, contracts.ParticipantObservationV1{SessionSlot: "p1", TeamKey: "radiant", VerifiedIdentity: &contracts.VerifiedParticipantIdentityV1{TournamentScopeID: strings.Repeat("c", 64), RosterID: "r", PlayerID: "p"}})
		}},
		{"player_hero", func(o *contracts.LiveObservationV1) {
			o.Participants[0].VerifiedIdentity = &contracts.VerifiedParticipantIdentityV1{TournamentScopeID: strings.Repeat("c", 64), RosterID: "r", PlayerID: "p"}
			o.Participants[0].HeroID = contracts.Present(int64(2))
		}},
		{"role", func(o *contracts.LiveObservationV1) {
			o.Participants[0].XPos = contracts.Present(contracts.Decimal("1"))
			o.Participants[0].YPos = contracts.Present(contracts.Decimal("2"))
		}},
		{"team", func(o *contracts.LiveObservationV1) {
			o.Buildings = append(o.Buildings, contracts.BuildingObservationV1{Team: "radiant", Name: "tower"})
		}},
	}
	for _, test := range tests {
		t.Run(test.family, func(t *testing.T) {
			candidate := base
			candidate.Observation = base.Observation
			candidate.Observation.Participants = append([]contracts.ParticipantObservationV1(nil), base.Observation.Participants...)
			test.apply(&candidate.Observation)
			result := EvaluateLiveOnlyV2(candidate, DefaultConfig())
			if err := ValidateLiveOnlyEvaluationV2(result, candidate); err != nil {
				t.Fatal(err)
			}
			index := familyIndex(test.family)
			if result.Executions[index].DependencyInputSHA256 == baseline.Executions[index].DependencyInputSHA256 {
				t.Fatal("family observation did not causally change its site execution")
			}
		})
	}
}

func familyIndex(family string) int {
	for index, value := range contracts.HistoricalDisabledFamiliesV1() {
		if value == family {
			return index
		}
	}
	return -1
}

func productionSiteInput(t *testing.T) LiveOnlyInputV2 {
	t.Helper()
	now := time.Unix(10, 0).UTC()
	observation := contracts.LiveObservationV1{
		SchemaVersion: contracts.LiveObservationSchemaV1, MappingVersion: "mapping.v1",
		Evidence:   contracts.EvidenceRefV1{RecordSchemaVersion: 3, SessionID: "session", Sequence: 1, ReceiveTime: now, Source: "gsi", RawPayloadSHA256: strings.Repeat("a", 64)},
		ClockBasis: "clock_time", Participants: []contracts.ParticipantObservationV1{{SessionSlot: "p0", TeamKey: "radiant"}},
		Quality: contracts.SourceQualityV1{Confidence: "medium"},
	}
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
	artifact := contracts.PolicyArtifactIdentityV2{Version: "v1", ContentSHA256: strings.Repeat("b", 64)}
	lineage := contracts.PolicyLineageManifestV3{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: "session", RawRecordSchema: artifact, RawRecordFraming: artifact,
		RawPayloadSchema: artifact, LiveObservationSchema: artifact, ProjectionMapping: artifact, TournamentScopeID: strings.Repeat("c", 64),
		TournamentScopeSHA256: strings.Repeat("c", 64), HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID,
		Rules: RulesArtifact(), Config: ConfigArtifact(DefaultConfig()), Catalog: artifact, Terminology: artifact, LocalizationParameterMapping: artifact, EngineBuild: artifact,
	}
	availability, err := ProductLiveOnlyAvailabilityV1(history)
	if err != nil {
		t.Fatal(err)
	}
	return LiveOnlyInputV2{Observation: observation, History: history, Lineage: lineage, Availability: availability, RawRecordSHA256: strings.Repeat("d", 64), PolicyTimeMS: now.UnixMilli()}
}
