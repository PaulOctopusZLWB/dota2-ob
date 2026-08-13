package insight_test

import (
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
)

func TestEvaluateFiveFamiliesAndHistorySuppression(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	observation := completeObservation(now)
	previous := completeObservation(now.Add(-time.Second))
	previous.Evidence.Sequence = 1
	previous.Participants[0].Items = nil
	previous.Map.RadiantScore = contracts.Present(int64(1))
	previous.Buildings[0].Health = contracts.Present(contracts.Decimal("1000"))
	baseline := func(metric string, sample uint64) contracts.HistoricalBaselineV1 {
		value := contracts.Decimal("5000")
		return contracts.HistoricalBaselineV1{SnapshotID: hash('a'), SessionID: "session", Key: contracts.HistoricalBaselineKeyV1{RosterID: "r1", PlayerID: "p1", HeroID: "1", Patch: "7.41", Metric: metric, Window: "current_patch"}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: sample}}
	}
	baselines := []contracts.HistoricalBaselineV1{
		baseline("draft_hero_performance", 5), baseline("lane_10_net_worth", 8),
		baseline("item_item_blink_timing_ms", 8), baseline("teamfight_readiness", 10),
	}
	got := insight.Evaluate(insight.Input{Observation: observation, Previous: &previous, Baselines: baselines, PolicyTimeMS: 600_000}, insight.DefaultConfig())
	want := map[string]bool{"draft": false, "lane": false, "item": false, "objective": false, "readiness": false}
	for _, candidate := range got {
		want[insight.Family(candidate.RuleVersion)] = true
		if err := candidate.Validate(); err != nil {
			t.Fatalf("invalid candidate: %v", err)
		}
		if candidate.CreatedTimeMS != 600_000 || candidate.ExpiryTimeMS <= candidate.CreatedTimeMS {
			t.Fatal("candidate clock is not explicit")
		}
	}
	for family, found := range want {
		if !found {
			t.Errorf("missing %s family", family)
		}
	}

	baselines[1].Value.SampleSize = 7
	got = insight.Evaluate(insight.Input{Observation: observation, Previous: &previous, Baselines: baselines, PolicyTimeMS: 600_000}, insight.DefaultConfig())
	for _, candidate := range got {
		if insight.Family(candidate.RuleVersion) == "lane" && candidate.Availability != "suppressed" {
			t.Fatal("low-sample lane history was not suppressed")
		}
		if insight.Family(candidate.RuleVersion) == "objective" && candidate.Availability != "available" {
			t.Fatal("live objective rule was suppressed by history")
		}
	}
}

func TestEvaluateFailsLiveClaimsClosedAndIsDeterministic(t *testing.T) {
	observation := completeObservation(time.Unix(1_700_000_000, 0).UTC())
	observation.Quality.Flags = []string{"stale"}
	input := insight.Input{Observation: observation, PolicyTimeMS: 42}
	first := insight.Evaluate(input, insight.DefaultConfig())
	second := insight.Evaluate(input, insight.DefaultConfig())
	a, _ := contracts.MarshalCanonical(first)
	b, _ := contracts.MarshalCanonical(second)
	if string(a) != string(b) {
		t.Fatal("evaluation is nondeterministic")
	}
	if len(first) != 1 || first[0].Availability != "suppressed" || first[0].Reason != "stale_live_input" {
		t.Fatalf("unexpected suppression: %#v", first)
	}
}

func TestLiveSafetyAndHistoricalEligibilityReasons(t *testing.T) {
	base := completeObservation(time.Unix(1_700_000_000, 0).UTC())
	paused := base
	paused.Map.Paused = contracts.Present(true)
	if got := insight.Evaluate(insight.Input{Observation: paused, PolicyTimeMS: 1}, insight.DefaultConfig()); len(got) != 1 || got[0].Reason != "paused_live_input" {
		t.Fatalf("pause=%#v", got)
	}

	previous := base
	previous.Evidence.Sequence = 1
	value := contracts.Decimal("1")
	wrongPatch := contracts.HistoricalBaselineV1{SnapshotID: hash('a'), SessionID: "session", Key: contracts.HistoricalBaselineKeyV1{RosterID: "r", Patch: "7.40", Metric: "lane_10_net_worth"}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: 100}}
	got := insight.Evaluate(insight.Input{Observation: base, Previous: &previous, Baselines: []contracts.HistoricalBaselineV1{wrongPatch}, PolicyTimeMS: 600_000}, insight.DefaultConfig())
	foundObjective := false
	for _, candidate := range got {
		if insight.Family(candidate.RuleVersion) == "lane" && candidate.Reason != "patch_boundary" {
			t.Fatalf("lane reason=%s", candidate.Reason)
		}
		if insight.Family(candidate.RuleVersion) == "objective" {
			foundObjective = true
		}
	}
	if !foundObjective {
		t.Fatal("patch boundary suppressed live-only objective family")
	}
}

func completeObservation(received time.Time) contracts.LiveObservationV1 {
	parts := make([]contracts.ParticipantObservationV1, 10)
	for i := range parts {
		team := "radiant"
		if i >= 5 {
			team = "dire"
		}
		parts[i] = contracts.ParticipantObservationV1{SessionSlot: team + string(rune('0'+i)), TeamKey: team, HeroID: contracts.Present(int64(i + 1)), NetWorth: contracts.Present(contracts.Decimal("6000")), Alive: contracts.Present(true), HealthPercent: contracts.Present(contracts.Decimal("80")), ManaPercent: contracts.Present(contracts.Decimal("70")), VerifiedIdentity: &contracts.VerifiedParticipantIdentityV1{RosterID: "r1", PlayerID: "p1"}}
	}
	parts[0].Items = []contracts.ItemObservationV1{{Slot: "slot0", Name: contracts.Present("item_blink")}}
	return contracts.LiveObservationV1{SchemaVersion: contracts.LiveObservationSchemaV1, MappingVersion: "map.v1", Evidence: contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "session", Sequence: 2, ReceiveTime: received, Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: hash('b')}, ClockBasis: "game_time", Map: contracts.MapObservationV1{ClockTime: contracts.Present(int64(600)), GameTime: contracts.Present(int64(600)), Paused: contracts.Present(false), RadiantScore: contracts.Present(int64(2)), DireScore: contracts.Present(int64(0))}, Roshan: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Tormentor: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Buildings: []contracts.BuildingObservationV1{{Team: "dire", Name: "tower1_mid", Health: contracts.Present(contracts.Decimal("0")), MaxHealth: contracts.Present(contracts.Decimal("1000"))}}, Participants: parts, Quality: contracts.SourceQualityV1{Confidence: "high"}}
}

func hash(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
