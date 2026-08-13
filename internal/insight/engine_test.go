package insight_test

import (
	"sort"
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
	manifest, lineage, baselines := validHistory(t, observation, map[string]uint64{"draft_hero_performance": 5, "lane_10_net_worth": 8, "item_item_blink_timing_ms": 8, "teamfight_readiness": 10})
	got := insight.Evaluate(insight.Input{Observation: observation, Previous: &previous, Manifest: &manifest, Lineage: &lineage, Baselines: baselines, PolicyTimeMS: now.UnixMilli()}, insight.DefaultConfig())
	want := map[string]bool{"draft": false, "lane": false, "item": false, "objective": false, "readiness": false}
	for _, candidate := range got {
		want[insight.Family(candidate.RuleVersion)] = true
		if candidate.Availability != "available" {
			t.Errorf("%s unexpectedly suppressed: %s", candidate.RuleVersion, candidate.Reason)
		}
		if err := candidate.Validate(); err != nil {
			t.Fatalf("invalid candidate: %v", err)
		}
		if candidate.CreatedTimeMS != now.UnixMilli() || candidate.ExpiryTimeMS <= candidate.CreatedTimeMS {
			t.Fatal("candidate clock is not explicit")
		}
	}
	for family, found := range want {
		if !found {
			t.Errorf("missing %s family", family)
		}
	}

	manifest, lineage, baselines = validHistory(t, observation, map[string]uint64{"draft_hero_performance": 5, "lane_10_net_worth": 7, "item_item_blink_timing_ms": 8, "teamfight_readiness": 10})
	got = insight.Evaluate(insight.Input{Observation: observation, Previous: &previous, Manifest: &manifest, Lineage: &lineage, Baselines: baselines, PolicyTimeMS: now.UnixMilli()}, insight.DefaultConfig())
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

func TestHistoryRequiresSealedManifestAndLineageMembership(t *testing.T) {
	observation := completeObservation(time.Unix(1_700_000_000, 0).UTC())
	previous := observation
	previous.Evidence.Sequence = 1
	value := contracts.Decimal("5000")
	fabricated := contracts.HistoricalBaselineV1{SnapshotID: hash('a'), SessionID: "session", Key: contracts.HistoricalBaselineKeyV1{RosterID: "r1", PlayerID: "p1", HeroID: "1", Patch: "7.41", Metric: "draft_hero_performance", Window: "current_patch", SampleDefinition: "completed_matches"}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: 99}}
	got := insight.Evaluate(insight.Input{Observation: observation, Previous: &previous, Baselines: []contracts.HistoricalBaselineV1{fabricated}, PolicyTimeMS: observation.Evidence.ReceiveTime.UnixMilli()}, insight.DefaultConfig())
	for _, candidate := range got {
		if insight.Family(candidate.RuleVersion) == "draft" && candidate.Availability != "suppressed" {
			t.Fatal("contract-invalid baseline authorized draft claim")
		}
	}
}

func TestHistoryBindingRejectsAdversarialInputsWithoutSuppressingLiveRule(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	observation := completeObservation(now)
	previous := completeObservation(now.Add(-time.Second))
	previous.Evidence.Sequence = 1
	previous.Map.RadiantScore = contracts.Present(int64(1))
	cases := []struct {
		name   string
		mutate func(*contracts.HistoricalSnapshotManifestV1, *contracts.PolicyLineageManifestV2, []contracts.HistoricalBaselineV1, *insight.Config)
	}{
		{"wrong player", func(_ *contracts.HistoricalSnapshotManifestV1, _ *contracts.PolicyLineageManifestV2, b []contracts.HistoricalBaselineV1, _ *insight.Config) {
			b[0].Key.PlayerID = "other-player"
		}},
		{"wrong snapshot", func(_ *contracts.HistoricalSnapshotManifestV1, l *contracts.PolicyLineageManifestV2, _ []contracts.HistoricalBaselineV1, _ *insight.Config) {
			l.HistoricalSnapshotID, l.HistoricalSnapshotSHA256 = hash('f'), hash('f')
		}},
		{"current match included", func(m *contracts.HistoricalSnapshotManifestV1, _ *contracts.PolicyLineageManifestV2, _ []contracts.HistoricalBaselineV1, _ *insight.Config) {
			m.IncludedMatches[0].MatchID = "active-match"
		}},
		{"incomplete contract", func(_ *contracts.HistoricalSnapshotManifestV1, _ *contracts.PolicyLineageManifestV2, b []contracts.HistoricalBaselineV1, _ *insight.Config) {
			b[0].SchemaVersion = ""
		}},
		{"mismatched key", func(_ *contracts.HistoricalSnapshotManifestV1, _ *contracts.PolicyLineageManifestV2, b []contracts.HistoricalBaselineV1, _ *insight.Config) {
			b[0].Key.Window = "trailing_90_days"
		}},
		{"stale snapshot", func(_ *contracts.HistoricalSnapshotManifestV1, _ *contracts.PolicyLineageManifestV2, _ []contracts.HistoricalBaselineV1, c *insight.Config) {
			c.MaximumHistoryAgeMS = 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, lineage, baselines := validHistory(t, observation, map[string]uint64{"draft_hero_performance": 5})
			config := insight.DefaultConfig()
			tc.mutate(&manifest, &lineage, baselines, &config)
			got := insight.Evaluate(insight.Input{Observation: observation, Previous: &previous, Manifest: &manifest, Lineage: &lineage, Baselines: baselines, PolicyTimeMS: now.UnixMilli()}, config)
			draftSuppressed, objectiveAvailable := false, false
			for _, candidate := range got {
				if insight.Family(candidate.RuleVersion) == "draft" {
					draftSuppressed = candidate.Availability == "suppressed"
				}
				if insight.Family(candidate.RuleVersion) == "objective" {
					objectiveAvailable = candidate.Availability == "available"
				}
			}
			if !draftSuppressed || !objectiveAvailable {
				t.Fatalf("history isolation failed: %#v", got)
			}
		})
	}
}

func TestMissingObjectiveAndReadinessTelemetrySuppresses(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	current := completeObservation(now)
	previous := completeObservation(now.Add(-time.Second))
	previous.Evidence.Sequence = 1
	current.Map.RadiantScore = contracts.Absent[int64]()
	current.Buildings[0].Health = contracts.Absent[contracts.Decimal]()
	for i := range current.Participants {
		current.Participants[i].HealthPercent = contracts.Absent[contracts.Decimal]()
		current.Participants[i].ManaPercent = contracts.Absent[contracts.Decimal]()
		current.Participants[i].Alive = contracts.Absent[bool]()
	}
	got := insight.Evaluate(insight.Input{Observation: current, Previous: &previous, PolicyTimeMS: now.UnixMilli()}, insight.DefaultConfig())
	for _, candidate := range got {
		family := insight.Family(candidate.RuleVersion)
		if (family == "objective" || family == "readiness") && candidate.Availability != "suppressed" {
			t.Fatalf("missing telemetry authorized %s", family)
		}
	}
}

func TestMaximumLiveAgeSuppressesAllClaims(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	observation := completeObservation(now)
	got := insight.Evaluate(insight.Input{Observation: observation, PolicyTimeMS: now.Add(6 * time.Second).UnixMilli()}, insight.DefaultConfig())
	if len(got) != 1 || got[0].Reason != "stale_live_input" {
		t.Fatalf("stale observation not failed closed: %#v", got)
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
		if insight.Family(candidate.RuleVersion) == "lane" && candidate.Reason != "invalid_history_binding" {
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
		parts[i] = contracts.ParticipantObservationV1{SessionSlot: team + string(rune('0'+i)), TeamKey: team, HeroID: contracts.Present(int64(i + 1)), NetWorth: contracts.Present(contracts.Decimal("6000")), Level: contracts.Present(int64(8)), Alive: contracts.Present(true), HealthPercent: contracts.Present(contracts.Decimal("80")), ManaPercent: contracts.Present(contracts.Decimal("70")), VerifiedIdentity: &contracts.VerifiedParticipantIdentityV1{RosterID: "r1", PlayerID: "p1"}}
	}
	parts[0].Items = []contracts.ItemObservationV1{{Slot: "slot0", Name: contracts.Present("item_blink"), Cooldown: contracts.Present(contracts.Decimal("0")), CanCast: contracts.Present(true)}}
	return contracts.LiveObservationV1{SchemaVersion: contracts.LiveObservationSchemaV1, MappingVersion: "map.v1", Evidence: contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "session", Sequence: 2, ReceiveTime: received, Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: hash('b')}, MatchID: contracts.Present("active-match"), ClockBasis: "game_time", Map: contracts.MapObservationV1{ClockTime: contracts.Present(int64(600)), GameTime: contracts.Present(int64(600)), Paused: contracts.Present(false), RadiantScore: contracts.Present(int64(2)), DireScore: contracts.Present(int64(0))}, Roshan: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Tormentor: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Buildings: []contracts.BuildingObservationV1{{Team: "dire", Name: "tower1_mid", Health: contracts.Present(contracts.Decimal("0")), MaxHealth: contracts.Present(contracts.Decimal("1000"))}}, Participants: parts, Quality: contracts.SourceQualityV1{Confidence: "high"}}
}

func validHistory(t *testing.T, observation contracts.LiveObservationV1, samples map[string]uint64) (contracts.HistoricalSnapshotManifestV1, contracts.PolicyLineageManifestV2, []contracts.HistoricalBaselineV1) {
	t.Helper()
	now := observation.Evidence.ReceiveTime
	value := contracts.Decimal("5000")
	baselines := make([]contracts.HistoricalBaselineV1, 0, len(samples))
	for metric, sample := range samples {
		b := contracts.HistoricalBaselineV1{SchemaVersion: contracts.HistoricalBaselineSchemaV1, SnapshotID: hash('a'), SnapshotContentSHA256: hash('a'), TournamentScopeID: hash('c'), TournamentScopeSHA256: hash('c'), SessionID: observation.Evidence.SessionID, ActiveMatchID: "active-match", GeneratedTime: now.Add(-30 * time.Minute), Key: contracts.HistoricalBaselineKeyV1{RosterID: "r1", PlayerID: "p1", HeroID: "1", Patch: "7.41", Metric: metric, Window: "current_patch", SampleDefinition: "completed_matches"}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: sample, PeriodStart: now.Add(-48 * time.Hour), PeriodEnd: now.Add(-2 * time.Hour), SourceCoveragePPM: 1_000_000}}
		if err := contracts.SealHistoricalBaselineV1(&b); err != nil {
			t.Fatal(err)
		}
		baselines = append(baselines, b)
	}
	hashes := make([]string, len(baselines))
	for i := range baselines {
		hashes[i] = baselines[i].BaselineID
	}
	sort.Strings(hashes)
	manifest := contracts.HistoricalSnapshotManifestV1{SchemaVersion: contracts.HistoricalSnapshotManifestSchemaV1, TournamentScopeID: hash('c'), TournamentScopeSHA256: hash('c'), Binding: contracts.LiveSessionBindingV1{SessionID: observation.Evidence.SessionID, ActiveMatchID: "active-match", SessionStartTime: now.Add(time.Hour), TournamentScopeID: hash('c'), TournamentScopeSHA256: hash('c')}, CutoffTime: now.Add(-time.Hour), MaximumSourceEventTime: now.Add(-2 * time.Hour), SealedAt: now.Add(-30 * time.Minute), DiscoveryManifestSHA256: hash('d'), InputManifestSHA256: hash('e'), ParserVersion: "parser.v1", AggregateVersion: "aggregate.v1", IncludedMatches: []contracts.HistoricalMatchRefV1{{MatchID: "historical-match", Completed: true, SourceEventTime: now.Add(-2 * time.Hour), ReplaySHA256: hash('f'), IdentityStatus: contracts.IdentityVerified}}, ExcludedMatches: []contracts.ExcludedMatchV1{{MatchID: "active-match", Reason: contracts.ExclusionActiveMatch}}, BaselineContentSHA256: hashes}
	if err := contracts.SealHistoricalSnapshotManifestV1(&manifest); err != nil {
		t.Fatal(err)
	}
	for i := range baselines {
		baselines[i].SnapshotID, baselines[i].SnapshotContentSHA256 = manifest.SnapshotID, manifest.ContentSHA256
		if err := baselines[i].ValidateAgainst(manifest); err != nil {
			t.Fatal(err)
		}
	}
	artifact := contracts.PolicyArtifactIdentityV2{Version: "v1", ContentSHA256: hash('9')}
	lineage := contracts.PolicyLineageManifestV2{SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: observation.Evidence.SessionID, RawRecordSchema: artifact, RawRecordFraming: artifact, RawPayloadSchema: artifact, LiveObservationSchema: artifact, ProjectionMapping: artifact, TournamentScopeID: hash('c'), TournamentScopeSHA256: hash('c'), HistoricalSnapshotID: manifest.SnapshotID, HistoricalSnapshotSHA256: manifest.ContentSHA256, EligibleBaselineSHA256: hashes, Rules: artifact, Config: artifact, Catalog: artifact, Terminology: artifact, LocalizationParameterMapping: artifact, EngineBuild: artifact}
	if err := lineage.Validate(); err != nil {
		t.Fatal(err)
	}
	return manifest, lineage, baselines
}

func hash(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
