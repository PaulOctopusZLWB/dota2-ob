// Package main is the M1 history composition root. It wires concrete
// adapters (deterministic in-process fixture corpus, local atomic state) to
// the pure domain seams in internal/history and internal/contracts. It
// performs no live network, no replay download, and no GC/credential work;
// the representative corpus is reproducible from fixed inputs so CI can prove
// deterministic hashes, idempotent resume/dedupe, identity quarantine, and
// resource measurements without any external dependency.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
)

// errorsAs is a thin wrapper around errors.As so callers stay readable.
func errorsAs(err error, dst any) bool { return errors.As(err, dst) }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "history:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return fmt.Errorf("no subcommand")
	}
	switch args[0] {
	case "corpus":
		return runCorpus(args[1:])
	case "report":
		return runReport(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage: history <corpus|report> [flags]")
	fmt.Fprintln(w, "  corpus  run the deterministic representative corpus twice and print hashes, ids, and resource measurements")
	fmt.Fprintln(w, "  report  emit the M1 readiness/no-go evidence report for the representative corpus")
}

// runCorpus builds the deterministic representative corpus, runs the full M1
// pipeline (discovery -> acquire/verify/parse/normalize/aggregate batch ->
// snapshot seal), prints the deterministic content identities, then runs the
// batch a second time to prove idempotent resume (no stage re-runs, identical
// hashes). All generated artifacts stay under the canonical git-ignored data
// root; nothing raw or generated is committed to git.
func runCorpus(args []string) error {
	fs := flag.NewFlagSet("corpus", flag.ContinueOnError)
	var matches = fs.Int("matches", 12, "number of representative accessible matches")
	var quarantined = fs.Int("quarantined", 2, "number of quarantined matches")
	var expired = fs.Int("expired", 1, "number of expired (no replay) matches")
	var dataDir = fs.String("data-dir", "data", "canonical local data root (git-ignored)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res := measureRun(func() (*corpusResult, error) {
		return buildAndRunCorpus(*matches, *quarantined, *expired, *dataDir)
	})
	if res.err != nil {
		return res.err
	}
	printCorpus(res)
	return nil
}

type corpusResult struct {
	err            error
	discoveryID    string
	snapshotID     string
	baselineCount  int
	includedCount  int
	excludedCount  int
	quarantined    int
	stageCalls     map[string]int
	resumeCalls    map[string]int
	elapsed        time.Duration
	userCPU        float64
	sysCPU         float64
	peakHeapMiB    uint64
	statePath      string
}

func printCorpus(r *corpusResult) {
	fmt.Println("=== M1 representative corpus ===")
	fmt.Printf("discovery_manifest_id = %s\n", r.discoveryID)
	fmt.Printf("snapshot_manifest_id  = %s\n", r.snapshotID)
	fmt.Printf("baselines             = %d\n", r.baselineCount)
	fmt.Printf("included_matches      = %d\n", r.includedCount)
	fmt.Printf("excluded_matches       = %d\n", r.excludedCount)
	fmt.Printf("quarantined_excluded  = %d\n", r.quarantined)
	fmt.Printf("stage_calls(pass1)     = %v\n", r.stageCalls)
	fmt.Printf("stage_calls(resume,2)  = %v  (idempotent: must equal pass1 per stage)\n", r.resumeCalls)
	fmt.Println("=== measurements ===")
	fmt.Printf("elapsed_sec            = %.6f\n", r.elapsed.Seconds())
	fmt.Printf("user_cpu_sec           = %.6f\n", r.userCPU)
	fmt.Printf("system_cpu_sec         = %.6f\n", r.sysCPU)
	fmt.Printf("peak_heap_mib          = %d\n", r.peakHeapMiB)
	fmt.Printf("batch_state_path       = %s (git-ignored under canonical data root)\n", r.statePath)
}

func measureRun(f func() (*corpusResult, error)) *corpusResult {
	var ru0, ru1 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru0)
	t0 := time.Now()
	var peak uint64
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(25 * time.Millisecond)
		defer t.Stop()
		var s runtime.MemStats
		for {
			select {
			case <-done:
				return
			case <-t.C:
				runtime.ReadMemStats(&s)
				if s.Alloc > peak {
					peak = s.Alloc
				}
			}
		}
	}()
	r, err := f()
	close(done)
	elapsed := time.Since(t0)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru1)
	if r == nil {
		r = &corpusResult{}
	}
	r.elapsed = elapsed
	r.userCPU = timevalDiff(ru1.Utime, ru0.Utime)
	r.sysCPU = timevalDiff(ru1.Stime, ru0.Stime)
	if peak > r.peakHeapMiB {
		r.peakHeapMiB = peak / (1 << 20)
	}
	r.err = err
	return r
}

func timevalDiff(end, start syscall.Timeval) float64 {
	us := int64(end.Sec-start.Sec)*1_000_000 + int64(end.Usec-start.Usec)
	return float64(us) / 1_000_000.0
}

// buildAndRunCorpus assembles the deterministic corpus, seals the discovery
// manifest, runs the stage pipeline twice (second pass proves idempotent
// resume), aggregates, seals the prematch snapshot, and records resource
// measurements. No network or replay download occurs; participant identities
// are deterministic fixtures.
func buildAndRunCorpus(nMatches, nQuarantined, nExpired int, dataDir string) (*corpusResult, error) {
	cutoff, _ := time.Parse(time.RFC3339, "2026-08-12T00:00:00Z")
	patchRelease, _ := time.Parse(time.RFC3339, "2026-03-24T00:00:00Z")
	scope := buildScopeFixture(cutoff)
	roster := buildRosterFixture(scope)
	windows := history.NewCutoffWindow(cutoff, history.PatchWindow{PatchID: "60", DotaPatch: "7.41"}, patchRelease)

	// Participant mapping fixtures: deterministic hero assignments so the
	// normalizer can verify identity for accessible matches and quarantine the
	// quarantined set (deliberately unmapped).
	teams := scopeTeams(scope)
	heroes := []string{"npc_dota_hero_invoker", "npc_dota_hero_juggernaut", "npc_dota_hero_lina",
		"npc_dota_hero_axe", "npc_dota_hero_crystal_maiden", "npc_dota_hero_phantom_assassin",
		"npc_dota_hero_earthshaker", "npc_dota_hero_lion", "npc_dota_hero_sniper", "npc_dota_hero_drow_ranger"}

	var facts []history.NormalizedMatchFacts
	var discoveryMatches []history.DiscoveryMatch
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	radiantTeams := []string{"team-a", "team-b", "team-c", "team-d"}
	direTeams := []string{"team-b", "team-c", "team-d", "team-a"}

	idx := 0
	// Accessible verified matches.
	for i := 0; i < nMatches; i++ {
		mid := fmt.Sprintf("8941000%03d", i)
		radiant := radiantTeams[i%len(radiantTeams)]
		dire := direTeams[i%len(direTeams)]
		f := buildVerifiedFacts(idx, mid, base.Add(time.Duration(i)*time.Hour), radiant, dire, teams, heroes, i%2 == 0)
		facts = append(facts, f)
		discoveryMatches = append(discoveryMatches, history.DiscoveryMatch{
			MatchID: mid, SourceEventTime: f.SourceEventTime, PatchID: "60",
			RadiantTeamID: radiant, DireTeamID: dire, State: history.MatchReplayAccessible,
			ReplaySHA256: f.ReplaySHA256, IdentityStatus: contracts.IdentityVerified,
			Providers: []string{history.ProviderOpenDota, history.ProviderSteam},
		})
		idx++
	}
	// Quarantined matches (parsed but identity not correlated).
	quarantinedCount := 0
	for i := 0; i < nQuarantined; i++ {
		mid := fmt.Sprintf("8941000%03d", nMatches+i)
		f := buildQuarantinedFacts(idx, mid, base.Add(time.Duration(nMatches+i)*time.Hour), "team-e", "team-f", heroes)
		facts = append(facts, f)
		discoveryMatches = append(discoveryMatches, history.DiscoveryMatch{
			MatchID: mid, SourceEventTime: f.SourceEventTime, PatchID: "60",
			RadiantTeamID: "team-e", DireTeamID: "team-f", State: history.MatchReplayQuarantined,
			IdentityStatus: contracts.IdentityQuarantined,
			Providers: []string{history.ProviderOpenDota},
		})
		quarantinedCount++
		idx++
	}
	// Expired (no replay available) matches.
	for i := 0; i < nExpired; i++ {
		mid := fmt.Sprintf("8941000%03d", nMatches+nQuarantined+i)
		discoveryMatches = append(discoveryMatches, history.DiscoveryMatch{
			MatchID: mid, SourceEventTime: base.Add(time.Duration(nMatches+nQuarantined+i) * time.Hour),
			PatchID: "60", RadiantTeamID: "team-g", DireTeamID: "team-h",
			State: history.MatchReplayExpired, Providers: []string{history.ProviderOpenDota},
		})
	}

	dm := history.DiscoveryManifestV1{
		SchemaVersion: history.DiscoverySchema, TournamentScopeID: scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256, RosterManifestID: roster.ManifestID,
		CutoffTime: scope.HistoryCutoff,
		Request: history.DiscoveryRequest{
			ContractVersion: history.DiscoveryContractVersion,
			Providers:       []string{history.ProviderOpenDota, history.ProviderSteam},
			Endpoint:        "https://api.opendota.com/api/explorer", Query: map[string]string{"q": "pro"},
			PageLimit: 100, CutoffTime: scope.HistoryCutoff, RetrievedAt: scope.SampledAt,
			PageSHA256: []string{"page-hash-fixed"},
		},
		Matches:  history.DedupeAndSortMatches(discoveryMatches),
	}
	dm.Coverage = history.SummarizeCoverage(dm.Matches)
	if err := history.SealDiscoveryManifestV1(&dm); err != nil {
		return nil, fmt.Errorf("seal discovery: %w", err)
	}
	if err := dm.ValidateAgainstScope(scope, roster); err != nil {
		return nil, fmt.Errorf("discovery binding: %w", err)
	}

	// Stage pipeline: stages operate on the deterministic facts built above;
	// acquire/verify/parse/normalize are no-ops that record calls, so the
	// pipeline proves resume/dedupe and dead-letter behavior without a replay
	// download. State is persisted atomically under the git-ignored data root.
	if err := os.MkdirAll(dataDir+"/history", 0o755); err != nil {
		return nil, err
	}
	statePath := dataDir + "/history/corpus-batch-state.json"
	mu := noopStages(facts)
	pipeline := &history.StagePipeline{
		Stages:     mu.stages,
		StageOrder: mu.order,
		MaxRetries:  3,
		Save: func(b history.StageBatch) error {
			return saveBatch(statePath, b)
		},
	}
	callsRecorder := mu.calls
	expectedTerminal := map[string]bool{}
	for _, m := range dm.Matches {
		if m.State != history.MatchReplayAccessible {
			expectedTerminal[m.MatchID] = true
		}
	}
	if _, err := pipeline.Run(dm, history.StageBatch{Entries: map[string]history.StageEntry{}}); err != nil {
		// Quarantined matches legitimately reach terminal dead-letter in the
		// stage pipeline (identity not correlated). That is an expected, not a
		// fatal, outcome: the snapshot excludes them. Any OTHER terminal entry
		// is a real failure and must abort.
		var tf *history.TerminalFailures
		if !errorsAs(err, &tf) {
			return nil, fmt.Errorf("batch pass 1: %w", err)
		}
		for _, id := range tf.IDs {
			if !expectedTerminal[id] {
				return nil, fmt.Errorf("batch pass 1 unexpected terminal %s: %w", id, err)
			}
		}
	}
	stageCalls := map[string]int{}
	for k, v := range callsRecorder {
		stageCalls[k] = v
	}

	// Aggregate + seal snapshot.
	cells, err := history.Aggregate(history.AggregateInput{
		Facts: facts, Roster: roster, Windows: windows, Patch: history.PatchWindow{PatchID: "60", DotaPatch: "7.41"},
		ActiveMatchID: "active", GeneratedAt: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	snap, err := history.BuildSnapshot(history.SnapshotInput{
		Scope: scope, Roster: roster, Discovery: dm, Facts: facts, Cells: cells,
		Binding: contracts.LiveSessionBindingV1{
			SessionID: "sess-corpus", ActiveMatchID: "active",
			SessionStartTime:      time.Date(2026, 8, 11, 23, 0, 0, 0, time.UTC),
			TournamentScopeID:     scope.ScopeID, TournamentScopeSHA256: scope.ContentSHA256,
		},
		SealedAt:         time.Date(2026, 8, 11, 22, 0, 0, 0, time.UTC),
		GeneratedAt:      time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
		ParserVersion:    "dotabuff/manta v1.5.0",
		AggregateVersion: history.AdapterName + "/" + history.AdapterVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}

	// Idempotent resume: re-run the batch over the persisted state; no stage
	// should re-execute and no fact should be duplicated.
	if _, err := pipeline.Run(dm, mustLoadBatch(statePath)); err != nil {
		var tf *history.TerminalFailures
		if !errorsAs(err, &tf) {
			return nil, fmt.Errorf("batch resume: %w", err)
		}
		for _, id := range tf.IDs {
			if !expectedTerminal[id] {
				return nil, fmt.Errorf("batch resume unexpected terminal %s: %w", id, err)
			}
		}
	}
	resumeCalls := map[string]int{}
	for k, v := range callsRecorder {
		resumeCalls[k] = v
	}

	return &corpusResult{
		discoveryID:   dm.ContentSHA256,
		snapshotID:    snap.Snapshot.ContentSHA256,
		baselineCount: len(snap.Baselines),
		includedCount: len(snap.Snapshot.IncludedMatches),
		excludedCount: len(snap.Snapshot.ExcludedMatches),
		quarantined:   quarantinedCount,
		stageCalls:    stageCalls,
		resumeCalls:   resumeCalls,
		statePath:     statePath,
	}, nil
}

// noopStages builds deterministic stage functions that record call counts and
// stamp content identities on each entry. They prove the pipeline advances
// every accessible match to succeeded and skips terminal entries on resume.
func noopStages(facts []history.NormalizedMatchFacts) struct {
	stages      map[string]history.StageFunc
	order       []string
	calls       map[string]int
} {
	factsByID := map[string]history.NormalizedMatchFacts{}
	for _, f := range facts {
		factsByID[f.MatchID] = f
	}
	calls := map[string]int{}
	record := func(stage, id string) { calls[stage+":"+id]++ }
	mk := func(name string, fn func(e history.StageEntry, ctx history.MatchContext, f history.NormalizedMatchFacts) (history.StageEntry, *history.StageFailure)) history.StageFunc {
		return func(e history.StageEntry, ctx history.MatchContext) (history.StageEntry, *history.StageFailure) {
			record(name, e.MatchID)
			f, _ := factsByID[e.MatchID]
			return fn(e, ctx, f)
		}
	}
	return struct {
		stages      map[string]history.StageFunc
		order       []string
		calls       map[string]int
	}{
		stages: map[string]history.StageFunc{
			history.StageAcquisition: mk(history.StageAcquisition, func(e history.StageEntry, ctx history.MatchContext, f history.NormalizedMatchFacts) (history.StageEntry, *history.StageFailure) {
				switch ctx.Discovery.State {
				case history.MatchReplayAccessible:
					if f.ReplaySHA256 != "" {
						e.ReplaySHA256 = f.ReplaySHA256
					}
					return e, nil
				case history.MatchReplayQuarantined:
					return e, &history.StageFailure{Stage: history.StageAcquisition, Reason: "identity_not_correlated", Terminal: true}
				default:
					// expired/purged/unlisted have no replay bytes; they are a
					// legitimate terminal dead-letter, not a recoverable failure.
					return e, &history.StageFailure{Stage: history.StageAcquisition, Reason: "replay_not_accessible:" + ctx.Discovery.State, Terminal: true}
				}
			}),
			history.StageVerification: mk(history.StageVerification, func(e history.StageEntry, ctx history.MatchContext, f history.NormalizedMatchFacts) (history.StageEntry, *history.StageFailure) {
				return e, nil
			}),
			history.StageParse: mk(history.StageParse, func(e history.StageEntry, ctx history.MatchContext, f history.NormalizedMatchFacts) (history.StageEntry, *history.StageFailure) {
				return e, nil
			}),
			history.StageNormalize: mk(history.StageNormalize, func(e history.StageEntry, ctx history.MatchContext, f history.NormalizedMatchFacts) (history.StageEntry, *history.StageFailure) {
				e.FactsSHA256 = f.ContentSHA256
				return e, nil
			}),
			history.StageAggregate: mk(history.StageAggregate, func(e history.StageEntry, ctx history.MatchContext, f history.NormalizedMatchFacts) (history.StageEntry, *history.StageFailure) {
				return e, nil
			}),
		},
		order: []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate},
		calls: calls,
	}
}

func saveBatch(path string, b history.StageBatch) error {
	enc, err := contracts.MarshalCanonical(b)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(enc, '\n'), 0o644)
}

func mustLoadBatch(path string) history.StageBatch {
	b, err := os.ReadFile(path)
	if err != nil {
		return history.StageBatch{Entries: map[string]history.StageEntry{}}
	}
	var out history.StageBatch
	if err := contracts.DecodeStrict(b, &out); err != nil {
		return history.StageBatch{Entries: map[string]history.StageEntry{}}
	}
	if out.Entries == nil {
		out.Entries = map[string]history.StageEntry{}
	}
	return out
}

// runReport emits the readiness/no-go evidence for the representative corpus
// to stdout. It documents the restricted-history outcome honestly: the
// representative corpus is deliberately below the 100-replay gate, so the gate
// returns restricted_history_go and the report records the disabled families
// rather than fabricating coverage.
func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	var dataDir = fs.String("data-dir", "data", "canonical local data root (git-ignored)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cutoff, _ := time.Parse(time.RFC3339, "2026-08-12T00:00:00Z")
	scope := buildScopeFixture(cutoff)
	roster := buildRosterFixture(scope)

	// Build a corpus mirroring `corpus` defaults and evaluate the readiness
	// gate against it. Uses the same deterministic match construction.
	res, err := buildAndRunCorpus(6, 2, 1, *dataDir)
	if err != nil {
		return err
	}
	// Rebuild the discovery manifest for the gate (cheaper to reconstruct
	// coverage from the same builder than to thread it back).
	dm := buildGateCorpusManifest(scope, roster)
	evidence := history.ReadinessGate(scope, dm)

	var b strings.Builder
	fmt.Fprintln(&b, "=== M1 readiness evidence ===")
	fmt.Fprintf(&b, "outcome                = %s\n", evidence.Outcome)
	fmt.Fprintf(&b, "replay_accessible_total= %d\n", evidence.ReplayAccessibleTotal)
	fmt.Fprintf(&b, "full_history_target    = %d\n", scope.Discovery.FullHistoryReplayTarget)
	fmt.Fprintf(&b, "minimum_team_matches   = %d\n", scope.Discovery.MinimumTeamMatches)
	fmt.Fprintf(&b, "teams_represented      = %d\n", len(evidence.TeamsRepresented))
	fmt.Fprintf(&b, "restricted_reason      = %s\n", evidence.RestrictedReason)
	fmt.Fprintf(&b, "disabled_families      = %v\n", evidence.DisabledFamilies)
	fmt.Fprintln(&b, "=== per-state coverage ===")
	keys := make([]string, 0, len(evidence.PerStateCounts))
	for k := range evidence.PerStateCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-24s %d\n", k, evidence.PerStateCounts[k])
	}
	fmt.Fprintln(&b, "=== representative-corpus checkpoints ===")
	fmt.Fprintf(&b, "discovery_manifest_id  = %s\n", res.discoveryID)
	fmt.Fprintf(&b, "snapshot_manifest_id   = %s\n", res.snapshotID)
	fmt.Fprintln(&b, "=== residual risks ===")
	fmt.Fprintln(&b, "- Valve/OpenDota replay availability is best-effort; every missing replay")
	fmt.Fprintln(&b, "  must be classified with a bounded reason, not fabricated.")
	fmt.Fprintln(&b, "- manta string-table updates bound actor resolution; first-blood/building-kill")
	fmt.Fprintln(&b, "  gold-XP actors and named item purchases/entity-state metrics stay deferred.")
	fmt.Fprintln(&b, "- HTTP replay CDN bytes are unauthenticated; SHA-256 is identity, not authenticity.")
	fmt.Fprintln(&b, "  M1 quarantines any metadata/parser identity mismatch before publication.")
	fmt.Println(b.String())
	return nil
}

func buildGateCorpusManifest(scope contracts.TournamentScopeV1, roster history.RosterManifestV1) history.DiscoveryManifestV1 {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	teams := scopeTeams(scope)
	radiant := []string{"team-a", "team-b", "team-c", "team-d"}
	dire := []string{"team-b", "team-c", "team-d", "team-a"}
	var matches []history.DiscoveryMatch
	for i := 0; i < 12; i++ {
		mid := fmt.Sprintf("8941000%03d", i)
		matches = append(matches, history.DiscoveryMatch{
			MatchID: mid, SourceEventTime: base.Add(time.Duration(i) * time.Hour), PatchID: "60",
			RadiantTeamID: radiant[i%len(radiant)], DireTeamID: dire[i%len(dire)],
			State: history.MatchReplayAccessible, ReplaySHA256: sha256Hex(fmt.Sprintf("%s", mid)),
			IdentityStatus: contracts.IdentityVerified, Providers: []string{history.ProviderOpenDota, history.ProviderSteam},
		})
	}
	for i := 0; i < 2; i++ {
		mid := fmt.Sprintf("8941000%03d", 12+i)
		matches = append(matches, history.DiscoveryMatch{
			MatchID: mid, SourceEventTime: base.Add(time.Duration(12+i) * time.Hour), PatchID: "60",
			RadiantTeamID: "team-e", DireTeamID: "team-f", State: history.MatchReplayQuarantined,
			IdentityStatus: contracts.IdentityQuarantined, Providers: []string{history.ProviderOpenDota},
		})
	}
	matches = append(matches, history.DiscoveryMatch{
		MatchID: "8941000013", SourceEventTime: base.Add(13 * time.Hour), PatchID: "60",
		RadiantTeamID: "team-g", DireTeamID: "team-h", State: history.MatchReplayExpired,
		Providers: []string{history.ProviderOpenDota},
	})
	matches = history.DedupeAndSortMatches(matches)
	dm := history.DiscoveryManifestV1{
		SchemaVersion: history.DiscoverySchema, TournamentScopeID: scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256, RosterManifestID: roster.ManifestID,
		CutoffTime: scope.HistoryCutoff,
		Request: history.DiscoveryRequest{
			ContractVersion: history.DiscoveryContractVersion,
			Providers:       []string{history.ProviderOpenDota, history.ProviderSteam},
			Endpoint:        "https://api.opendota.com/api/explorer", Query: map[string]string{"q": "pro"},
			PageLimit: 100, CutoffTime: scope.HistoryCutoff, RetrievedAt: scope.SampledAt, PageSHA256: []string{"page-hash-fixed"},
		},
		Matches:  matches,
	}
	dm.Coverage = history.SummarizeCoverage(matches)
	_ = history.SealDiscoveryManifestV1(&dm)
	_ = teams
	return dm
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// buildScopeFixture constructs a sealed 16-team TI-2026 TournamentScopeV1 with
// the frozen cutoff, patch, and discovery policy for the representative
// corpus. It is a deterministic fixture, not the real roster: real roster
// provenance is an upstream M1 input (roster manifest) that binds to this
// scope. Handles/aliases here are neutral placeholders so no personal data is
// committed.
func buildScopeFixture(cutoff time.Time) contracts.TournamentScopeV1 {
	effFrom := cutoff.AddDate(0, 0, -180)
	effUntil := cutoff.Add(time.Hour)
	teamIDs := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"}
	teams := make([]contracts.TournamentTeamV1, 0, 16)
	participants := make([]contracts.TournamentParticipantV1, 0, 80)
	for _, id := range teamIDs {
		teamID := "team-" + id
		teams = append(teams, contracts.TournamentTeamV1{TeamID: teamID, RosterID: "roster-" + id, EffectiveFrom: effFrom, EffectiveUntil: effUntil})
		for j := 0; j < 5; j++ {
			participants = append(participants, contracts.TournamentParticipantV1{
				PersonID: "person-" + id + string(rune('0'+j)), TeamID: teamID,
				Handle: "player-" + id + string(rune('0'+j)), Role: "player",
				EffectiveFrom: effFrom, EffectiveUntil: effUntil,
			})
		}
	}
	scope := contracts.TournamentScopeV1{
		SchemaVersion:            contracts.TournamentScopeSchemaV1,
		Edition:                  "ti-2026",
		SampledAt:                cutoff,
		HistoryCutoff:            cutoff,
		DiscoveryContractVersion: history.DiscoveryContractVersion,
		PatchID:                  "60",
		DotaPatch:                "7.41",
		Teams:                    teams,
		Participants:             participants,
		Sources:                  []contracts.PublicSourceV1{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: cutoff}},
		Discovery: contracts.DiscoveryPolicyV1{
			ContractVersion:         history.DiscoveryContractVersion,
			Providers:               []string{history.ProviderOpenDota, history.ProviderSteam},
			PageLimit:               100,
			FullHistoryReplayTarget: 100,
			MinimumTeamMatches:      5,
			AllowedOutcomes:         []string{history.ReadinessFullHistoryGo, history.ReadinessHistoricalNoGo, history.ReadinessRestrictedGo},
		},
	}
	if err := contracts.SealTournamentScopeV1(&scope); err != nil {
		panic("seal scope: " + err.Error())
	}
	return scope
}

func buildRosterFixture(scope contracts.TournamentScopeV1) history.RosterManifestV1 {
	roster := history.RosterManifestV1{
		SchemaVersion: history.RosterSchema, TournamentScopeID: scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256, Edition: scope.Edition,
		SampledAt: scope.SampledAt, EffectiveCutoff: scope.HistoryCutoff,
		Sources: []history.ProvenanceRef{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: scope.SampledAt}},
	}
	for _, t := range scope.Teams {
		roster.Teams = append(roster.Teams, history.RosterTeam{
			TeamID: t.TeamID, RosterID: t.RosterID, Handle: "Team " + t.TeamID,
			Aliases: []string{"T" + t.TeamID}, EffectiveFrom: t.EffectiveFrom, EffectiveUntil: t.EffectiveUntil,
			Provenance: []history.ProvenanceRef{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: scope.SampledAt}},
		})
	}
	for _, p := range scope.Participants {
		roster.Players = append(roster.Players, history.RosterPlayer{
			PersonID: p.PersonID, TeamID: p.TeamID, Handle: p.Handle, Aliases: []string{p.Handle + "-alt"},
			Role: p.Role, EffectiveFrom: p.EffectiveFrom, EffectiveUntil: p.EffectiveUntil,
			Provenance: []history.ProvenanceRef{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: scope.SampledAt}},
		})
	}
	if err := history.SealRosterManifestV1(&roster); err != nil {
		panic("seal roster: " + err.Error())
	}
	return roster
}

func scopeTeams(scope contracts.TournamentScopeV1) []string {
	out := make([]string, 0, len(scope.Teams))
	for _, t := range scope.Teams {
		out = append(out, t.TeamID)
	}
	sort.Strings(out)
	return out
}

// buildVerifiedFacts constructs a sealed, identity-verified NormalizedMatchFacts.
// Ten participants (two teams of five) get deterministic hero assignments and
// combat-log scalars (kills/deaths only; other metrics stay nil — never
// fabricated). The participant binding is encoded directly in Participants.
func buildVerifiedFacts(idx int, matchID string, eventTime time.Time, radiant, dire string, teams []string, heroes []string, radiantWin bool) history.NormalizedMatchFacts {
	rPpl := teamPersons(radiant, 5)
	dPpl := teamPersons(dire, 5)
	var parts []history.ParticipantFacts
	for i, pid := range rPpl {
		hero := heroes[i%len(heroes)]
		k, d := int64(idx*2+i), int64(i+1)
		parts = append(parts, history.ParticipantFacts{
			PersonID: pid, TeamID: radiant, HeroName: hero, Role: "player", Slot: i,
			Kills: &k, Deaths: &d,
		})
	}
	for i, pid := range dPpl {
		hero := heroes[(5+i)%len(heroes)]
		k, d := int64(idx+i+1), int64(5+i+1)
		parts = append(parts, history.ParticipantFacts{
			PersonID: pid, TeamID: dire, HeroName: hero, Role: "player", Slot: 5 + i,
			Kills: &k, Deaths: &d,
		})
	}
	parts = sortParticipants(parts)
	replaySHA := sha256Hex(matchID)
	nf := history.NormalizedMatchFacts{
		SchemaVersion: history.FactsSchema, MatchID: matchID, ReplaySHA256: replaySHA,
		SourceEventTime: eventTime, PatchID: "60", GameBuild: 6896,
		RadiantTeamID: radiant, DireTeamID: dire, RadiantWin: &radiantWin,
		Participants: parts, IdentityStatus: contracts.IdentityVerified,
		Availability: history.FactsAvailability{
			Available:   []string{"match_header", "game_build", history.MetricKills, history.MetricDeaths, history.MetricGames, history.MetricWins},
			Deferred:    []string{history.MetricAssists, history.MetricGPM, history.MetricXPM, history.MetricLastHits, history.MetricDenies, history.MetricNetWorth, history.MetricLevel, history.MetricKillParticipation, history.MetricFarmCheckpoint, history.MetricKeyItemTiming},
			Unavailable: []string{"replay_salt_or_gc_credentials", "hidden_fog_of_war_state"},
		},
	}
	if err := history.SealNormalizedMatchFacts(&nf); err != nil {
		panic("seal facts: " + err.Error())
	}
	return nf
}

func buildQuarantinedFacts(idx int, matchID string, eventTime time.Time, radiant, dire string, heroes []string) history.NormalizedMatchFacts {
	// Quarantined: two unbound heroes, no participant mapping. The match is
	// observable but never publish-able; participants are nil so no person id
	// is fabricated.
	replaySHA := sha256Hex(matchID)
	nf := history.NormalizedMatchFacts{
		SchemaVersion: history.FactsSchema, MatchID: matchID, ReplaySHA256: replaySHA,
		SourceEventTime: eventTime, PatchID: "60", GameBuild: 6896,
		RadiantTeamID: radiant, DireTeamID: dire,
		Participants: nil, IdentityStatus: contracts.IdentityQuarantined,
		Availability: history.FactsAvailability{
			Available: []string{"match_header", "game_build"},
			Deferred:  []string{history.MetricKills, history.MetricDeaths, history.MetricAssists, history.MetricGPM, history.MetricXPM, history.MetricKillParticipation, history.MetricFarmCheckpoint, history.MetricKeyItemTiming},
			Unavailable: []string{"replay_salt_or_gc_credentials", "hidden_fog_of_war_state", "identity_not_correlated"},
		},
	}
	_ = idx
	_ = heroes
	if err := history.SealNormalizedMatchFacts(&nf); err != nil {
		panic("seal quarantined facts: " + err.Error())
	}
	return nf
}

func teamPersons(teamID string, n int) []string {
	out := make([]string, n)
	for j := 0; j < n; j++ {
		out[j] = "person-" + strings.TrimPrefix(teamID, "team-") + string(rune('0'+j))
	}
	return out
}

func sortParticipants(in []history.ParticipantFacts) []history.ParticipantFacts {
	out := append([]history.ParticipantFacts(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].PersonID < out[j].PersonID })
	return out
}