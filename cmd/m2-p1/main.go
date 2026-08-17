// Command m2-p1 runs the normative deterministic pure-engine measurement.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	Warmups, Evaluations                                                                            int
	P50NS, P95NS, P99NS, MaxNS                                                                      int64
	AllocationsPerEvaluation                                                                        float64
	AllocatedBytesPerEvaluation                                                                     float64
	BaselineRSSBytes, PeakRSSBytes, PostWarmupRSSBytes, FinalRSSBytes, PostWarmupGrowthBytes        uint64
	FixtureID, ConfigVersion, RuleVersions, LineageID, CandidateSHA256, DecisionSHA256, StateSHA256 string
	Runtime, GOOS, GOARCH, Compiler                                                                 string
	CPUs                                                                                            int
	Exclusions                                                                                      []string
	Failures, ExcludedSamples                                                                       int
	RawSamples, SummarizationCommand, Hostname, Kernel, CPUModel, GPUContext, PowerMode, HostLoad   string
	Mode                                                                                            string
	HistoryDependentCandidates                                                                      int
	HistoryDependentDecisions, HistoryDependentOverlayClaims, HistoryDependentAudits                int
	HistoryBindingID, CanonicalOutputSHA256                                                         string
}

func main() {
	samplesPath := flag.String("samples", "m2-p1-samples.txt", "raw nanosecond samples output")
	summarizePath := flag.String("summarize", "", "summarize an existing raw sample file")
	liveOnly := flag.Bool("live-only", false, "run the historical-no-go typed-unavailable V3 fixture")
	fixtureDir := flag.String("fixture-dir", "internal/contracts/testdata", "directory containing the immutable live-only contract goldens")
	flag.Parse()
	if *summarizePath != "" {
		summarize(*summarizePath)
		return
	}
	config := insight.DefaultConfig()
	fixtureID, historyBindingID := "m2-complete-ten-player.v3-fixed-state", ""
	var run func(uint64) evaluation
	mode := "snapshot_baseline"
	lineageID := ""
	if *liveOnly {
		var err error
		liveInput, loadedFixtureID, loadedBindingID, err := loadLiveOnlyFixture(*fixtureDir, config)
		if err != nil {
			panic(err)
		}
		fixtureID, historyBindingID = loadedFixtureID, loadedBindingID
		run = func(sequence uint64) evaluation { return evaluateLiveOnly(liveInput, config, sequence) }
		mode = "historical_no_go_accepted_live_only"
		lineageID = liveInput.Lineage.MustContentID()
	} else {
		input, snapshotConfig := fixture()
		config = snapshotConfig
		run = func(sequence uint64) evaluation { return evaluate(input, config, sequence) }
		lineageID = input.Lineage.MustContentID()
	}
	baselineRSS := rss()
	for i := 1; i <= warmups; i++ {
		run(uint64(i))
	}
	runtime.GC()
	postWarmup := rss()
	peak := postWarmup
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	durations := make([]int64, measured)
	var final evaluation
	semanticHash := sha256.New()
	historyCandidates, historyDecisions, historyClaims, historyAudits, failures := 0, 0, 0, 0, 0
	for i := 0; i < measured; i++ {
		sequence := uint64(warmups + i + 1)
		start := time.Now()
		final = run(sequence)
		durations[i] = time.Since(start).Nanoseconds()
		c, d, cx, a, f := auditLiveOnlyEvaluation(final)
		historyCandidates += c
		historyDecisions += d
		historyClaims += cx
		historyAudits += a
		failures += f
		canonical, err := contracts.MarshalCanonical(struct {
			Candidates    []contracts.InsightCandidateV1  `json:"candidates"`
			Decisions     []contracts.BroadcastDecisionV1 `json:"decisions"`
			Audits        []contracts.AuditEventV1        `json:"audits"`
			OverlayClaims []contracts.OverlayClaimV1      `json:"overlay_claims"`
			StateHash     string                          `json:"state_hash"`
		}{final.candidates, final.decisions, final.audits, final.overlayClaims, final.stateHash})
		if err != nil {
			panic(err)
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(canonical)))
		_, _ = semanticHash.Write(size[:])
		_, _ = semanticHash.Write(canonical)
		if i%1000 == 0 {
			if current := rss(); current > peak {
				peak = current
			}
		}
	}
	runtime.ReadMemStats(&after)
	finalRSS := rss()
	writeSamples(*samplesPath, durations)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	candidateHash, _ := contracts.CanonicalSHA256(final.candidates)
	decisionHash, _ := contracts.CanonicalSHA256(final.decisions)
	host, _ := os.Hostname()
	output := report{Warmups: warmups, Evaluations: measured, P50NS: nearest(durations, 50), P95NS: nearest(durations, 95), P99NS: nearest(durations, 99), MaxNS: durations[len(durations)-1], AllocationsPerEvaluation: float64(after.Mallocs-before.Mallocs) / measured, AllocatedBytesPerEvaluation: float64(after.TotalAlloc-before.TotalAlloc) / measured, BaselineRSSBytes: baselineRSS, PeakRSSBytes: peak, PostWarmupRSSBytes: postWarmup, FinalRSSBytes: finalRSS, FixtureID: fixtureID, ConfigVersion: config.Version, RuleVersions: "draft.v1,item.v1,lane.v1,objective.v1,readiness.v1", LineageID: lineageID, CandidateSHA256: candidateHash, DecisionSHA256: decisionHash, StateSHA256: final.stateHash, Runtime: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Compiler: runtime.Compiler, CPUs: runtime.NumCPU(), Exclusions: []string{"GSI capture and DotaTV delay", "filesystem commit log and sync", "network, localization, rendering, delivery HTTP, OBS"}, RawSamples: *samplesPath, SummarizationCommand: "m2-p1 --summarize " + *samplesPath, Hostname: host, Kernel: readFirst("/proc/sys/kernel/osrelease"), CPUModel: cpuModel(), GPUContext: readFirst("/proc/driver/nvidia/version"), PowerMode: readFirst("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"), HostLoad: readFirst("/proc/loadavg"), Mode: mode, HistoryDependentCandidates: historyCandidates, HistoryDependentDecisions: historyDecisions, HistoryDependentOverlayClaims: historyClaims, HistoryDependentAudits: historyAudits, Failures: failures, HistoryBindingID: historyBindingID, CanonicalOutputSHA256: hex.EncodeToString(semanticHash.Sum(nil))}
	if finalRSS > postWarmup {
		output.PostWarmupGrowthBytes = finalRSS - postWarmup
	}
	data, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(data))
	if output.P99NS >= 20_000_000 || output.PeakRSSBytes >= 128<<20 || output.PostWarmupGrowthBytes > 16<<20 || (*liveOnly && (historyCandidates != 0 || historyDecisions != 0 || historyClaims != 0 || historyAudits != 0 || failures != 0)) {
		os.Exit(1)
	}
}

type evaluation struct {
	candidates    []contracts.InsightCandidateV1
	decisions     []contracts.BroadcastDecisionV1
	audits        []contracts.AuditEventV1
	overlayClaims []contracts.OverlayClaimV1
	stateHash     string
}

func evaluate(input insight.Input, config insight.Config, sequence uint64) evaluation {
	input.Observation.Evidence.Sequence = sequence
	input.Observation.Evidence.ReceiveTime = time.Unix(1_700_000_000, 0).UTC().Add(time.Duration(sequence) * time.Millisecond)
	input.PolicyTimeMS = input.Observation.Evidence.ReceiveTime.UnixMilli()
	values := insight.Evaluate(input, config)
	liveHash, _ := contracts.CanonicalSHA256(input.Observation)
	policyConfig := policy.DefaultConfig()
	policyConfig.LineageID = input.Lineage.MustContentID()
	policyConfig.CandidateConfigVersion = config.Version
	policyConfig.CandidateConfigArtifact, policyConfig.CandidateRulesArtifact = input.Lineage.Config, input.Lineage.Rules
	engine := policy.New("p1-session", policyConfig)
	commit := engine.EvaluateObservation(sequence, strings.Repeat("e", 64), liveHash, input.Observation.Evidence, values, input.PolicyTimeMS)
	return evaluation{candidates: values, decisions: commit.Decisions, audits: commit.AuditEvents, overlayClaims: []contracts.OverlayClaimV1{}, stateHash: engine.StateHash()}
}

func evaluateLiveOnly(input insight.LiveOnlyInput, config insight.Config, sequence uint64) evaluation {
	input.Observation.Evidence.Sequence = sequence
	input.Observation.Evidence.ReceiveTime = time.Unix(1_700_000_000, 0).UTC().Add(time.Duration(sequence) * time.Millisecond)
	input.PolicyTimeMS = input.Observation.Evidence.ReceiveTime.UnixMilli()
	values := insight.EvaluateLiveOnly(input, config)
	liveHash, _ := contracts.CanonicalSHA256(input.Observation)
	policyConfig := policy.DefaultConfig()
	policyConfig.LineageID = input.Lineage.MustContentID()
	policyConfig.CandidateConfigVersion = config.Version
	policyConfig.CandidateConfigArtifact, policyConfig.CandidateRulesArtifact = input.Lineage.Config, input.Lineage.Rules
	engine := policy.New(input.Lineage.SessionID, policyConfig)
	commit := engine.EvaluateObservationV3(sequence, strings.Repeat("e", 64), liveHash, input.Observation.Evidence, values, input.PolicyTimeMS)
	return evaluation{candidates: values, decisions: commit.Decisions, audits: commit.AuditEvents, overlayClaims: []contracts.OverlayClaimV1{}, stateHash: engine.StateHash()}
}

func auditLiveOnlyEvaluation(value evaluation) (candidates, decisions, claims, audits, failures int) {
	historyIDs := map[string]bool{}
	for _, candidate := range value.candidates {
		if err := candidate.Validate(); err != nil {
			failures++
		}
		family := insight.Family(candidate.RuleVersion)
		if family != "objective" && family != "live" {
			candidates++
			historyIDs[candidate.CandidateID] = true
		}
	}
	for _, decision := range value.decisions {
		if err := decision.Validate(); err != nil {
			failures++
		}
		if historyIDs[decision.CandidateID] {
			decisions++
		}
	}
	for range value.overlayClaims {
		claims++
	}
	for _, audit := range value.audits {
		if err := audit.Validate(); err != nil {
			failures++
		}
		if historyIDs[audit.CandidateID] {
			audits++
		}
	}
	if value.stateHash == "" {
		failures++
	}
	return
}

func loadLiveOnlyFixture(dir string, config insight.Config) (insight.LiveOnlyInput, string, string, error) {
	var fixture contracts.HistoricalUnavailableFixtureV1
	var binding contracts.HistoryAvailabilityBindingV1
	var lineage contracts.PolicyLineageManifestV3
	for name, dst := range map[string]any{"historical_unavailable_fixture_v1.json": &fixture, "history_availability_binding_v1.json": &binding, "policy_lineage_manifest_v3.json": &lineage} {
		if err := loadCanonicalGolden(filepath.Join(dir, name), dst); err != nil {
			return insight.LiveOnlyInput{}, "", "", err
		}
	}
	if err := fixture.ValidateAgainst(binding, lineage); err != nil {
		return insight.LiveOnlyInput{}, "", "", err
	}
	if lineage.Config != insight.ConfigArtifact(config) || lineage.Rules != insight.RulesArtifact() {
		return insight.LiveOnlyInput{}, "", "", fmt.Errorf("fixture policy artifacts mismatch")
	}
	fixtureID, err := fixture.ContentID()
	if err != nil {
		return insight.LiveOnlyInput{}, "", "", err
	}
	bindingID, err := binding.ContentID()
	if err != nil {
		return insight.LiveOnlyInput{}, "", "", err
	}
	return insight.LiveOnlyInput{Observation: fixture.Observation, History: binding, Lineage: lineage}, fixtureID, bindingID, nil
}

func loadCanonicalGolden(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := contracts.DecodeStrict(raw, dst); err != nil {
		return err
	}
	canonical, err := contracts.MarshalCanonical(dst)
	if err != nil || !bytes.Equal(raw, canonical) {
		return fmt.Errorf("noncanonical fixture %s", path)
	}
	want, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if strings.TrimSpace(string(want)) != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("fixture hash mismatch %s", path)
	}
	return nil
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

func writeSamples(path string, values []int64) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, value := range values {
		fmt.Fprintln(w, value)
	}
	if err := w.Flush(); err != nil {
		panic(err)
	}
}
func summarize(path string) {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	values := []int64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		value, err := strconv.ParseInt(strings.TrimSpace(scanner.Text()), 10, 64)
		if err != nil {
			panic(err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
	if len(values) == 0 {
		panic("no samples")
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	fmt.Printf("samples=%d p50_ns=%d p95_ns=%d p99_ns=%d max_ns=%d\n", len(values), nearest(values, 50), nearest(values, 95), nearest(values, 99), values[len(values)-1])
}
func readFirst(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(data))
}
func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "unavailable"
}

func fixture() (insight.Input, insight.Config) {
	now := time.Unix(1_700_000_000, 0).UTC()
	participants := make([]contracts.ParticipantObservationV1, 10)
	for i := range participants {
		team := "radiant"
		if i >= 5 {
			team = "dire"
		}
		participants[i] = contracts.ParticipantObservationV1{SessionSlot: fmt.Sprintf("%s-%d", team, i), TeamKey: team, VerifiedIdentity: &contracts.VerifiedParticipantIdentityV1{RosterID: "roster", PlayerID: fmt.Sprintf("player-%d", i)}, HeroID: contracts.Present(int64(i + 1)), NetWorth: contracts.Present(contracts.Decimal("6000")), Level: contracts.Present(int64(8)), Alive: contracts.Present(true), HealthPercent: contracts.Present(contracts.Decimal("80")), ManaPercent: contracts.Present(contracts.Decimal("70"))}
	}
	participants[0].Items = []contracts.ItemObservationV1{{Slot: "slot0", Name: contracts.Present("item_blink"), Cooldown: contracts.Present(contracts.Decimal("0")), CanCast: contracts.Present(true)}}
	evidence := contracts.EvidenceRefV1{RecordSchemaVersion: 1, SessionID: "p1-session", Sequence: 1, ReceiveTime: now, Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: strings.Repeat("b", 64)}
	observation := contracts.LiveObservationV1{SchemaVersion: contracts.LiveObservationSchemaV1, MappingVersion: "map.v1", Evidence: evidence, MatchID: contracts.Present("active-match"), ClockBasis: "game_time", Map: contracts.MapObservationV1{ClockTime: contracts.Present(int64(600)), GameTime: contracts.Present(int64(600)), Paused: contracts.Present(false), RadiantScore: contracts.Present(int64(2)), DireScore: contracts.Present(int64(0))}, Roshan: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Tormentor: contracts.ObjectiveObservationV1{State: contracts.Present("alive")}, Buildings: []contracts.BuildingObservationV1{{Team: "dire", Name: "tower1_mid", Health: contracts.Present(contracts.Decimal("0")), MaxHealth: contracts.Present(contracts.Decimal("1000"))}}, Participants: participants, Quality: contracts.SourceQualityV1{Confidence: "high"}}
	previous := observation
	previous.Evidence.Sequence = 1
	previous.Participants = append([]contracts.ParticipantObservationV1(nil), participants...)
	previous.Participants[0].Items = nil
	previous.Map.RadiantScore = contracts.Present(int64(1))
	previous.Buildings = []contracts.BuildingObservationV1{{Team: "dire", Name: "tower1_mid", Health: contracts.Present(contracts.Decimal("1000")), MaxHealth: contracts.Present(contracts.Decimal("1000"))}}
	manifest, lineage, baselines := historyFixture(observation)
	return insight.Input{Observation: observation, Previous: &previous, Manifest: &manifest, Lineage: &lineage, Baselines: baselines}, insight.DefaultConfig()
}

func historyFixture(observation contracts.LiveObservationV1) (contracts.HistoricalSnapshotManifestV1, contracts.PolicyLineageManifestV2, []contracts.HistoricalBaselineV1) {
	now, value := observation.Evidence.ReceiveTime, contracts.Decimal("5000")
	metrics := []struct {
		name   string
		sample uint64
	}{{"draft_hero_performance", 5}, {"lane_10_net_worth", 8}, {"item_item_blink_timing_ms", 8}, {"teamfight_readiness", 10}}
	baselines := make([]contracts.HistoricalBaselineV1, 0, len(metrics))
	for _, metric := range metrics {
		b := contracts.HistoricalBaselineV1{SchemaVersion: contracts.HistoricalBaselineSchemaV1, SnapshotID: strings.Repeat("a", 64), SnapshotContentSHA256: strings.Repeat("a", 64), TournamentScopeID: strings.Repeat("c", 64), TournamentScopeSHA256: strings.Repeat("c", 64), SessionID: "p1-session", ActiveMatchID: "active-match", GeneratedTime: now.Add(-30 * time.Minute), Key: contracts.HistoricalBaselineKeyV1{RosterID: "roster", PlayerID: "player-0", HeroID: "1", Role: "carry", Patch: "7.41", Metric: metric.name, Window: "current_patch", SampleDefinition: "completed_matches"}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: metric.sample, PeriodStart: now.Add(-48 * time.Hour), PeriodEnd: now.Add(-2 * time.Hour), SourceCoveragePPM: 1_000_000}}
		if err := contracts.SealHistoricalBaselineV1(&b); err != nil {
			panic(err)
		}
		baselines = append(baselines, b)
	}
	hashes := make([]string, len(baselines))
	for i := range baselines {
		hashes[i] = baselines[i].BaselineID
	}
	sort.Strings(hashes)
	manifest := contracts.HistoricalSnapshotManifestV1{SchemaVersion: contracts.HistoricalSnapshotManifestSchemaV1, TournamentScopeID: strings.Repeat("c", 64), TournamentScopeSHA256: strings.Repeat("c", 64), Binding: contracts.LiveSessionBindingV1{SessionID: "p1-session", ActiveMatchID: "active-match", SessionStartTime: now.Add(time.Hour), TournamentScopeID: strings.Repeat("c", 64), TournamentScopeSHA256: strings.Repeat("c", 64)}, CutoffTime: now.Add(-time.Hour), MaximumSourceEventTime: now.Add(-2 * time.Hour), SealedAt: now.Add(-30 * time.Minute), DiscoveryManifestSHA256: strings.Repeat("d", 64), InputManifestSHA256: strings.Repeat("e", 64), ParserVersion: "parser.v1", AggregateVersion: "aggregate.v1", IncludedMatches: []contracts.HistoricalMatchRefV1{{MatchID: "historical-match", Completed: true, SourceEventTime: now.Add(-2 * time.Hour), ReplaySHA256: strings.Repeat("f", 64), IdentityStatus: contracts.IdentityVerified}}, ExcludedMatches: []contracts.ExcludedMatchV1{{MatchID: "active-match", Reason: contracts.ExclusionActiveMatch}}, BaselineContentSHA256: hashes}
	if err := contracts.SealHistoricalSnapshotManifestV1(&manifest); err != nil {
		panic(err)
	}
	for i := range baselines {
		baselines[i].SnapshotID, baselines[i].SnapshotContentSHA256 = manifest.SnapshotID, manifest.ContentSHA256
		if err := baselines[i].ValidateAgainst(manifest); err != nil {
			panic(err)
		}
	}
	artifact := contracts.PolicyArtifactIdentityV2{Version: "v1", ContentSHA256: strings.Repeat("9", 64)}
	lineage := contracts.PolicyLineageManifestV2{SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: "p1-session", RawRecordSchema: artifact, RawRecordFraming: artifact, RawPayloadSchema: artifact, LiveObservationSchema: artifact, ProjectionMapping: artifact, TournamentScopeID: strings.Repeat("c", 64), TournamentScopeSHA256: strings.Repeat("c", 64), HistoricalSnapshotID: manifest.SnapshotID, HistoricalSnapshotSHA256: manifest.ContentSHA256, EligibleBaselineSHA256: hashes, Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(insight.DefaultConfig()), Catalog: artifact, Terminology: artifact, LocalizationParameterMapping: artifact, EngineBuild: artifact}
	if err := lineage.Validate(); err != nil {
		panic(err)
	}
	return manifest, lineage, baselines
}
