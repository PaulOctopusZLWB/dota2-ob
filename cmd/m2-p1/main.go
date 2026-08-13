// Command m2-p1 runs the normative deterministic pure-engine measurement.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
)

const warmups, measured = 10_000, 100_000

type report struct {
	Warmups, Evaluations                                                                     int
	P50NS, P95NS, P99NS, MaxNS                                                               int64
	AllocationsPerEvaluation                                                                 float64
	AllocatedBytesPerEvaluation                                                              float64
	BaselineRSSBytes, PeakRSSBytes, PostWarmupRSSBytes, FinalRSSBytes, PostWarmupGrowthBytes uint64
	FixtureID, ConfigVersion, RuleVersions, LineageID, CandidateSHA256, StateSHA256          string
	Runtime, GOOS, GOARCH, Compiler                                                          string
	CPUs                                                                                     int
	Exclusions                                                                               []string
}

func main() {
	input, config := fixture()
	engine := policy.New("p1-session", policy.DefaultConfig())
	baselineRSS := rss()
	for i := 1; i <= warmups; i++ {
		evaluate(engine, input, config, uint64(i), int64(i))
	}
	runtime.GC()
	postWarmup := rss()
	peak := postWarmup
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	durations := make([]int64, measured)
	var finalCandidates []contracts.InsightCandidateV1
	for i := 0; i < measured; i++ {
		sequence := uint64(warmups + i + 1)
		policyTime := int64(warmups + i + 1)
		start := time.Now()
		finalCandidates = evaluate(engine, input, config, sequence, policyTime)
		durations[i] = time.Since(start).Nanoseconds()
		if i%1000 == 0 {
			if current := rss(); current > peak {
				peak = current
			}
		}
	}
	runtime.ReadMemStats(&after)
	finalRSS := rss()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	candidateHash, _ := contracts.CanonicalSHA256(finalCandidates)
	output := report{Warmups: warmups, Evaluations: measured, P50NS: nearest(durations, 50), P95NS: nearest(durations, 95), P99NS: nearest(durations, 99), MaxNS: durations[len(durations)-1], AllocationsPerEvaluation: float64(after.Mallocs-before.Mallocs) / measured, AllocatedBytesPerEvaluation: float64(after.TotalAlloc-before.TotalAlloc) / measured, BaselineRSSBytes: baselineRSS, PeakRSSBytes: peak, PostWarmupRSSBytes: postWarmup, FinalRSSBytes: finalRSS, FixtureID: "m2-complete-ten-player.v1", ConfigVersion: config.Version, RuleVersions: "draft.v1,item.v1,lane.v1,objective.v1,readiness.v1", LineageID: strings.Repeat("f", 64), CandidateSHA256: candidateHash, StateSHA256: engine.StateHash(), Runtime: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Compiler: runtime.Compiler, CPUs: runtime.NumCPU(), Exclusions: []string{"GSI capture and DotaTV delay", "filesystem commit log and sync", "network, localization, rendering, delivery HTTP, OBS"}}
	if finalRSS > postWarmup {
		output.PostWarmupGrowthBytes = finalRSS - postWarmup
	}
	data, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(data))
	if output.P99NS >= 20_000_000 || output.PeakRSSBytes >= 128<<20 || output.PostWarmupGrowthBytes > 16<<20 {
		os.Exit(1)
	}
}

func evaluate(engine *policy.Engine, input insight.Input, config insight.Config, sequence uint64, policyTime int64) []contracts.InsightCandidateV1 {
	input.Observation.Evidence.Sequence = sequence
	input.PolicyTimeMS = policyTime
	values := insight.Evaluate(input, config)
	liveHash, _ := contracts.CanonicalSHA256(input.Observation)
	engine.EvaluateObservation(sequence, strings.Repeat("e", 64), liveHash, input.Observation.Evidence, values, policyTime)
	return values
}
func nearest(v []int64, p int) int64 {
	rank := (p*len(v) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return v[rank-1]
}
func rss() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(fields[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}

func fixture() (insight.Input, insight.Config) {
	now := time.Unix(1_700_000_000, 0).UTC()
	participants := make([]contracts.ParticipantObservationV1, 10)
	for i := range participants {
		team := "radiant"
		if i >= 5 {
			team = "dire"
		}
		participants[i] = contracts.ParticipantObservationV1{SessionSlot: fmt.Sprintf("%s-%d", team, i), TeamKey: team, VerifiedIdentity: &contracts.VerifiedParticipantIdentityV1{RosterID: "roster", PlayerID: fmt.Sprintf("player-%d", i)}, HeroID: contracts.Present(int64(i + 1)), NetWorth: contracts.Present(contracts.Decimal("6000")), Alive: contracts.Present(true), HealthPercent: contracts.Present(contracts.Decimal("80")), ManaPercent: contracts.Present(contracts.Decimal("70"))}
	}
	participants[0].Items = []contracts.ItemObservationV1{{Slot: "slot0", Name: contracts.Present("item_blink")}}
	evidence := contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "p1-session", Sequence: 1, ReceiveTime: now, Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: strings.Repeat("b", 64)}
	observation := contracts.LiveObservationV1{SchemaVersion: contracts.LiveObservationSchemaV1, MappingVersion: "map.v1", Evidence: evidence, ClockBasis: "game_time", Map: contracts.MapObservationV1{ClockTime: contracts.Present(int64(600)), GameTime: contracts.Present(int64(600)), Paused: contracts.Present(false), RadiantScore: contracts.Present(int64(2)), DireScore: contracts.Present(int64(0))}, Roshan: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Tormentor: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Buildings: []contracts.BuildingObservationV1{{Team: "dire", Name: "tower1_mid", Health: contracts.Present(contracts.Decimal("0")), MaxHealth: contracts.Present(contracts.Decimal("1000"))}}, Participants: participants, Quality: contracts.SourceQualityV1{Confidence: "high"}}
	previous := observation
	previous.Evidence.Sequence = 1
	previous.Participants = append([]contracts.ParticipantObservationV1(nil), participants...)
	previous.Participants[0].Items = nil
	previous.Map.RadiantScore = contracts.Present(int64(1))
	previous.Buildings = []contracts.BuildingObservationV1{{Team: "dire", Name: "tower1_mid", Health: contracts.Present(contracts.Decimal("1000")), MaxHealth: contracts.Present(contracts.Decimal("1000"))}}
	value := contracts.Decimal("5000")
	baseline := func(metric string, sample uint64) contracts.HistoricalBaselineV1 {
		return contracts.HistoricalBaselineV1{SnapshotID: strings.Repeat("a", 64), SessionID: "p1-session", Key: contracts.HistoricalBaselineKeyV1{RosterID: "roster", Patch: "7.41", Metric: metric}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: sample}}
	}
	return insight.Input{Observation: observation, Previous: &previous, Baselines: []contracts.HistoricalBaselineV1{baseline("draft_hero_performance", 5), baseline("lane_10_net_worth", 8), baseline("item_item_blink_timing_ms", 8), baseline("teamfight_readiness", 10)}}, insight.DefaultConfig()
}
