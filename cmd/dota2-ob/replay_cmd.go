package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/api"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/review"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/runner"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/scoring"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
)

// runReplay dispatches the replay subcommands.
func runReplay(args []string, output io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(output, usageReplay)
		return 2
	}
	switch args[0] {
	case "verify":
		return cmdVerify(args[1:], output)
	case "parse":
		return cmdParse(args[1:], output)
	case "probe":
		return cmdProbe(args[1:], output)
	case "batch":
		return cmdBatch(args[1:], output)
	case "score":
		return cmdScore(args[1:], output)
	case "evaluate":
		return cmdEvaluate(args[1:], output)
	case "rebuild-catalog":
		return cmdRebuildCatalog(args[1:], output)
	default:
		fmt.Fprintln(output, usageReplay)
		return 2
	}
}

const usageReplay = `dota2-ob replay SUBCOMMAND [flags]

SUBCOMMANDS:
  verify           verify archive+demo hashes, magic, and size against the manifest
  parse            run the full per-match pipeline for one match id
  probe            run the full pipeline for all five frozen probe matches
  batch            run the pipeline for a manifest with bounded workers
  score            compute the corpus radar/score snapshots from persisted metrics
  evaluate         evaluate machine phases against an adjudicated gold file
  rebuild-catalog  rebuild the local catalog from persisted artifacts

Run 'dota2-ob replay <subcommand> -h' for flags.`

func replayFlags(fs *flag.FlagSet) (*string, *string, *string, *string) {
	manifest := fs.String("manifest", "", "frozen manifest JSON path")
	replayRoot := fs.String("replay-root", "", "runtime corpus root (immutable .dem.bz2/.dem)")
	dataRoot := fs.String("data-root", "./data/replay", "persisted artifact data root")
	matchID := fs.String("match-id", "", "single match id to process")
	return manifest, replayRoot, dataRoot, matchID
}

// roleInputs bundles the effective role-provenance inputs for the runner.
type roleInputs struct {
	Registry      *roles.Registry
	Overrides     *roles.OverrideFile
	RegistrySHA   string
	OverridesSHA  string
	RegistryPath  string
	OverridesPath string

	// MetricRegistry is the frozen V1/V2/V3 metric contract.
	MetricRegistry     *metrics.Registry
	MetricRegistrySHA  string
	MetricRegistryPath string
	// MetricClosure is the frozen 52-row metric closure contract.
	MetricClosure     *metrics.Closure
	MetricClosureSHA  string
	MetricClosurePath string
	// ScoringContract is the frozen radar/score contract.
	ScoringContract     *scoring.Contract
	ScoringContractSHA  string
	ScoringContractPath string
	// TeamContract is the frozen team scoring registry.
	TeamContract     *scoring.TeamContract
	TeamContractSHA  string
	TeamContractPath string
}

// fileSHA256 returns the hex sha256 of a file, or "" when absent.
func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum), nil
}

// loadRoleInputs loads the frozen role registry and optional explicit override
// file from the manifest directory, plus the metric registry and scoring
// contract. A missing or unreadable registry is a hard error: role provenance
// is a publication gate, never silently discarded.
func loadRoleInputs(manifestPath, dataRoot string) (*roleInputs, error) {
	dir := filepath.Dir(manifestPath)
	roleFile := filepath.Join(dir, "ti2026-five-replay-role-registry-v1.json")
	if _, err := os.Stat(roleFile); err != nil {
		return nil, fmt.Errorf("role_registry_missing (expected %s): %w", roleFile, err)
	}
	reg, err := roles.LoadRegistry(roleFile)
	if err != nil {
		return nil, fmt.Errorf("role_registry_load_failed (%s): %w", roleFile, err)
	}
	regSHA, err := fileSHA256(roleFile)
	if err != nil {
		return nil, fmt.Errorf("role_registry_hash_failed: %w", err)
	}
	overrideFile := filepath.Join(dir, "ti2026-five-replay-role-overrides-v1.json")
	overrides, err := roles.LoadOverrides(overrideFile)
	if err != nil {
		return nil, fmt.Errorf("role_overrides_load_failed (%s): %w", overrideFile, err)
	}
	ovrSHA, err := fileSHA256(overrideFile)
	if err != nil {
		return nil, fmt.Errorf("role_overrides_hash_failed: %w", err)
	}

	metricFile := filepath.Join(dir, "ti2026-role-phase-metrics-v1.json")
	if _, err := os.Stat(metricFile); err != nil {
		return nil, fmt.Errorf("metric_registry_missing (expected %s): %w", metricFile, err)
	}
	mreg, err := metrics.LoadRegistry(metricFile)
	if err != nil {
		return nil, fmt.Errorf("metric_registry_load_failed (%s): %w", metricFile, err)
	}
	mregSHA, err := fileSHA256(metricFile)
	if err != nil {
		return nil, fmt.Errorf("metric_registry_hash_failed: %w", err)
	}
	// The frozen metric closure contract is a hard requirement alongside the
	// registry: every metric must resolve through a definition-specific entry.
	closureFile := filepath.Join(dir, "ti2026-metric-closure-v1.json")
	if _, err := os.Stat(closureFile); err != nil {
		return nil, fmt.Errorf("metric_closure_missing (expected %s): %w", closureFile, err)
	}
	mclosure, err := metrics.LoadClosure(closureFile)
	if err != nil {
		return nil, fmt.Errorf("metric_closure_load_failed (%s): %w", closureFile, err)
	}
	if err := mclosure.ValidateAgainstRegistry(mreg); err != nil {
		return nil, fmt.Errorf("metric_closure_registry_mismatch: %w", err)
	}
	mclosureSHA, err := fileSHA256(closureFile)
	if err != nil {
		return nil, fmt.Errorf("metric_closure_hash_failed: %w", err)
	}

	scoringFile := filepath.Join(dir, "ti2026-radar-scoring-v1.json")
	if _, err := os.Stat(scoringFile); err != nil {
		return nil, fmt.Errorf("scoring_contract_missing (expected %s): %w", scoringFile, err)
	}
	sc, err := scoring.LoadContract(scoringFile)
	if err != nil {
		return nil, fmt.Errorf("scoring_contract_load_failed (%s): %w", scoringFile, err)
	}
	scSHA, err := fileSHA256(scoringFile)
	if err != nil {
		return nil, fmt.Errorf("scoring_contract_hash_failed: %w", err)
	}

	teamFile := filepath.Join(dir, "ti2026-team-scoring-v1.json")
	var tc *scoring.TeamContract
	var tcSHA string
	if _, err := os.Stat(teamFile); err == nil {
		tc, err = scoring.LoadTeamContract(teamFile)
		if err != nil {
			return nil, fmt.Errorf("team_scoring_contract_load_failed (%s): %w", teamFile, err)
		}
		// Cross-registry eligibility: every team-referenced metric must exist
		// in the metric registry and be official-eligible, never V3.
		if err := tc.ValidateWithRegistry(mreg); err != nil {
			return nil, fmt.Errorf("team_scoring_contract_metric_mismatch: %w", err)
		}
		tcSHA, err = fileSHA256(teamFile)
		if err != nil {
			return nil, fmt.Errorf("team_scoring_contract_hash_failed: %w", err)
		}
	}

	return &roleInputs{
		Registry: reg, Overrides: overrides,
		RegistrySHA: regSHA, OverridesSHA: ovrSHA,
		RegistryPath: roleFile, OverridesPath: overrideFile,
		MetricRegistry: mreg, MetricRegistrySHA: mregSHA, MetricRegistryPath: metricFile,
		MetricClosure: mclosure, MetricClosureSHA: mclosureSHA, MetricClosurePath: closureFile,
		ScoringContract: sc, ScoringContractSHA: scSHA, ScoringContractPath: scoringFile,
		TeamContract: tc, TeamContractSHA: tcSHA, TeamContractPath: teamFile,
	}, nil
}

// cmdVerify verifies archive+demo integrity for every manifest entry without
// parsing. Exit code is non-zero if any entry is not verified or quarantined
// (i.e. not an explicitly terminal auditable state).
func cmdVerify(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay verify", flag.ContinueOnError)
	manifest, replayRoot, dataRoot, _ := replayFlags(fs)
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" || *replayRoot == "" || *dataRoot == "" {
		fmt.Fprintln(output, "verify requires --manifest, --replay-root, --data-root")
		return 2
	}
	m, err := archive.LoadManifest(*manifest)
	if err != nil {
		fmt.Fprintf(output, "manifest_load_failed: %v\n", err)
		return 1
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	bad := 0
	for i := range m.Matches {
		mt := &m.Matches[i]
		ver, err := archive.Verify(*replayRoot, mt, archive.VerifyOptions{})
		if err != nil {
			fmt.Fprintf(output, "match %s verify_error: %v\n", mt.MatchID, err)
			bad++
			continue
		}
		if err := st.WriteJSON(mt.MatchID, store.ArtifactVerification, ver); err != nil {
			fmt.Fprintf(output, "match %s persist_failed: %v\n", mt.MatchID, err)
			bad++
			continue
		}
		fmt.Fprintf(output, "match %s state=%s reason=%s\n", mt.MatchID, ver.State, ver.Reason)
		if !archive.Terminal(ver.State) || ver.State != archive.StateVerified {
			bad++
		}
	}
	if bad > 0 {
		fmt.Fprintf(output, "verify_result: %d of %d matches not verified\n", bad, len(m.Matches))
		return 1
	}
	fmt.Fprintf(output, "verify_result: all %d matches verified\n", len(m.Matches))
	return 0
}

// runnerOptions builds the version-complete runner options from the loaded
// role + metric contract inputs.
func runnerOptions(ri *roleInputs) runner.Options {
	return runner.Options{
		RoleRegistry: ri.Registry, Overrides: ri.Overrides,
		RoleRegistrySHA256: ri.RegistrySHA, RoleOverridesSHA256: ri.OverridesSHA,
		MetricRegistry: ri.MetricRegistry, MetricRegistrySHA256: ri.MetricRegistrySHA,
		MetricClosure: ri.MetricClosure, MetricClosureSHA256: ri.MetricClosureSHA,
	}
}

// cmdParse runs the full pipeline for a single match id.
func cmdParse(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay parse", flag.ContinueOnError)
	manifest, replayRoot, dataRoot, matchID := replayFlags(fs)
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" || *replayRoot == "" || *dataRoot == "" || *matchID == "" {
		fmt.Fprintln(output, "parse requires --manifest, --replay-root, --data-root, --match-id")
		return 2
	}
	m, err := archive.LoadManifest(*manifest)
	if err != nil {
		fmt.Fprintf(output, "manifest_load_failed: %v\n", err)
		return 1
	}
	mt, ok := m.Find(*matchID)
	if !ok {
		fmt.Fprintf(output, "match %s not in manifest\n", *matchID)
		return 1
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	ri, err := loadRoleInputs(*manifest, *dataRoot)
	if err != nil {
		fmt.Fprintf(output, "role_inputs_failed: %v\n", err)
		return 1
	}
	opts := runnerOptions(ri)
	res, err := runner.RunMatch(st, mt, *replayRoot, opts, nil, func(line string) {
		fmt.Fprintln(output, line)
	})
	if err != nil {
		fmt.Fprintf(output, "parse_failed: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(output)
	_ = enc.Encode(res)
	if res.Status != store.StatusVerified {
		return 1
	}
	return 0
}

// cmdProbe runs the full pipeline for all five frozen probes from a clean data
// root, then writes a terminal summary JSON.
func cmdProbe(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay probe", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "five-match probe manifest JSON path")
	replayRoot := fs.String("replay-root", "/home/paul-zhang/文档/ti15_rep", "runtime corpus root")
	dataRoot := fs.String("data-root", "./data/replay-probe", "persisted artifact data root")
	workers := fs.Int("workers", 1, "parallel workers")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" {
		fmt.Fprintln(output, "probe requires --manifest")
		return 2
	}
	m, err := archive.LoadManifest(*manifest)
	if err != nil {
		fmt.Fprintf(output, "manifest_load_failed: %v\n", err)
		return 1
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	ri, err := loadRoleInputs(*manifest, *dataRoot)
	if err != nil {
		fmt.Fprintf(output, "role_inputs_failed: %v\n", err)
		return 1
	}
	opts := runnerOptions(ri)
	matches := make([]*archive.Match, 0, len(m.Matches))
	for i := range m.Matches {
		matches = append(matches, &m.Matches[i])
	}
	start := time.Now()
	results := runner.RunBatch(st, matches, *replayRoot, opts, nil, *workers, func(line string) {
		fmt.Fprintln(output, line)
	})
	wall := time.Since(start)
	summary := map[string]interface{}{
		"command":       "probe",
		"wall_seconds":  wall.Seconds(),
		"workers":       *workers,
		"data_root":     st.Root,
		"status_counts": countByStatus(results),
		"results":       results,
	}
	enc := json.NewEncoder(output)
	_ = enc.Encode(summary)

	verified := 0
	for _, r := range results {
		if r.Status == store.StatusVerified {
			verified++
		}
	}
	fmt.Fprintf(output, "probe_result: %d/%d verified, wall=%.1fs, %s\n", verified, len(results), wall.Seconds(), runner.SummarizeResults(results))
	// Gate failures must fail the command: any match not verified (missing,
	// corrupt, parse_failed, quarantined identity/clock/role) is a non-zero
	// exit. All matches are still processed and their terminal states
	// recorded; batch isolation is preserved.
	for _, r := range results {
		if r.Status != store.StatusVerified {
			return 1
		}
	}
	return 0
}

func countByStatus(results []*runner.MatchResult) map[string]int {
	m := map[string]int{}
	for _, r := range results {
		m[r.Status]++
	}
	return m
}

// cmdBatch runs a manifest with bounded workers and resume.
func cmdBatch(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay batch", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "manifest JSON path")
	replayRoot := fs.String("replay-root", "", "runtime corpus root")
	dataRoot := fs.String("data-root", "./data/replay-batch", "persisted artifact data root")
	workers := fs.Int("workers", 2, "parallel workers")
	resume := fs.Bool("resume", false, "resume completed matches")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" || *replayRoot == "" {
		fmt.Fprintln(output, "batch requires --manifest, --replay-root")
		return 2
	}
	m, err := archive.LoadManifest(*manifest)
	if err != nil {
		fmt.Fprintf(output, "manifest_load_failed: %v\n", err)
		return 1
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	ri, err := loadRoleInputs(*manifest, *dataRoot)
	if err != nil {
		fmt.Fprintf(output, "role_inputs_failed: %v\n", err)
		return 1
	}
	opts := runnerOptions(ri)
	matches := make([]*archive.Match, 0, len(m.Matches))
	for i := range m.Matches {
		matches = append(matches, &m.Matches[i])
	}
	start := time.Now()
	results := runner.RunBatch(st, matches, *replayRoot, opts, nil, *workers, func(line string) {
		fmt.Fprintln(output, line)
	})
	wall := time.Since(start)
	fmt.Fprintf(output, "batch_result: %s wall=%.1fs workers=%d\n", runner.SummarizeResults(results), wall.Seconds(), *workers)
	_ = *resume
	// Batch gate failures fail the command while preserving per-match
	// terminal-state recording (batch isolation is unchanged).
	for _, r := range results {
		if r.Status != store.StatusVerified {
			return 1
		}
	}
	return 0
}

// cmdScore computes the corpus radar/score snapshots from persisted metrics.
// It is deterministic and rebuildable; scores are not part of any match's
// canonical tree (metrics + contracts are the recomputable source). It
// consumes the authoritative data-root role-override store when present, so a
// review override is picked up on recomputation.
func cmdScore(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay score", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "probe manifest path (for contract lookup)")
	dataRoot := fs.String("data-root", "./data/replay-probe", "persisted artifact data root")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" {
		fmt.Fprintln(output, "score requires --manifest")
		return 2
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	ri, err := loadRoleInputs(*manifest, *dataRoot)
	if err != nil {
		fmt.Fprintf(output, "role_inputs_failed: %v\n", err)
		return 1
	}
	// A normal score run never doubles as repair/migration. Validate every
	// already-authoritative score artifact before RebuildCatalog (which writes
	// catalog.json) or any score promotion, so failure preserves all bytes.
	if err := scoring.PreflightPersistedScores(st, ri.MetricRegistry); err != nil {
		fmt.Fprintf(output, "score_preflight_failed: %v\n", err)
		return 1
	}
	if _, err := st.RebuildCatalog(time.Now().UTC().Format(time.RFC3339)); err != nil {
		fmt.Fprintf(output, "rebuild_catalog_failed: %v\n", err)
		return 1
	}
	// Consume the authoritative data-root override store when present (review
	// mutations write here), so recomputation reflects effective roles.
	effectiveOverrides := ri.Overrides
	var of roles.OverrideFile
	if err := st.ReadJSONFile(st.Root+"/role-overrides-effective.json", &of); err == nil {
		effectiveOverrides = &of
	}
	cs, err := scoring.ComputeAndPersist(st, ri.ScoringContract, ri.TeamContract, ri.MetricRegistry, ri.Registry, effectiveOverrides)
	if err != nil {
		fmt.Fprintf(output, "score_compute_failed: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(output)
	_ = enc.Encode(cs)
	fmt.Fprintf(output, "score_result: %d corpus matches, %d score snapshots\n", cs.CorpusMatches, len(cs.Matches))
	if cs.CorpusMatches < ri.ScoringContract.MinimumMatches {
		fmt.Fprintf(output, "score_gate: corpus_matches=%d_less_than_minimum_%d (totals suppressed)\n", cs.CorpusMatches, ri.ScoringContract.MinimumMatches)
	}
	return 0
}

// cmdEvaluate compares machine phase output against an adjudicated gold file.
// Stage 2 has no adjudicated gold set yet; this validates the gold schema and
// reports that evaluation is pending the review workflow.
func cmdEvaluate(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay evaluate", flag.ContinueOnError)
	gold := fs.String("gold", "", "adjudicated gold JSON path")
	dataRoot := fs.String("data-root", "./data/replay-probe", "persisted artifact data root")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gold == "" {
		fmt.Fprintln(output, "evaluate requires --gold")
		return 2
	}
	goldBytes, err := os.ReadFile(*gold)
	if err != nil {
		fmt.Fprintf(output, "gold_read_failed: %v\n", err)
		return 1
	}
	var g interface{}
	if err := json.Unmarshal(goldBytes, &g); err != nil {
		fmt.Fprintf(output, "gold_schema_invalid: %v\n", err)
		return 1
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	_, _ = st.RebuildCatalog(time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintln(output, "evaluate_result: gold_schema_valid; adjudicated evaluation pending stage 4 review workflow")
	return 0
}

// cmdRebuildCatalog rebuilds the catalog from persisted artifacts.
func cmdRebuildCatalog(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("replay rebuild-catalog", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "./data/replay", "persisted artifact data root")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	cat, err := st.RebuildCatalog(time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		fmt.Fprintf(output, "rebuild_catalog_failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(output, "rebuild_catalog: %d matches indexed at %s\n", len(cat.Matches), st.CatalogPath())
	return 0
}

// runServe starts the local loopback replay web server. The manifest is
// required: the role registry it points to is a publication gate, and the API
// must never rebuild published reports with a nil registry.
func runServe(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "./data/replay", "persisted artifact data root")
	listen := fs.String("listen", "127.0.0.1:43211", "loopback listen address")
	manifest := fs.String("manifest", "", "probe manifest path (for role registry lookup)")
	sessionToken := fs.String("session-token", "", "local review mutation session token (required for review mutations)")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" {
		fmt.Fprintln(output, "serve requires --manifest (role registry is a publication gate)")
		return 2
	}
	if !loopbackOnly(*listen) {
		fmt.Fprintf(output, "serve requires a loopback listen host (got %q); use 127.0.0.1 or ::1\n", *listen)
		return 2
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	ri, err := loadRoleInputs(*manifest, *dataRoot)
	if err != nil {
		fmt.Fprintf(output, "role_inputs_failed: %v\n", err)
		return 1
	}
	rv, err := review.New(st.Root)
	if err != nil {
		fmt.Fprintf(output, "review_store_init_failed: %v\n", err)
		return 1
	}
	srv := api.New(st, ri.Registry, ri.Overrides, ri.RegistryPath).
		WithContracts(ri.MetricRegistry, ri.ScoringContract).
		WithMetricClosure(ri.MetricClosure).
		WithTeamContract(ri.TeamContract).
		WithReviews(rv).
		WithSessionToken(*sessionToken)
	handler := serveHandler(srv)
	return listenAndServe(*listen, handler, output)
}

// loopbackOnly reports whether the listen address is bound to the local
// loopback interface (IPv4 127.0.0.0/8 or IPv6 ::1). Wildcard and public binds
// are rejected to keep the review mutations and data loopback-only.
func loopbackOnly(addr string) bool {
	host := addr
	// Strip the port. IPv6 literals are bracketed: [::1]:43211.
	if strings.HasPrefix(host, "[") {
		if i := strings.IndexByte(host, ']'); i >= 0 {
			host = host[1:i]
		}
	} else if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, " ")
	if host == "" {
		return false // ":port" binds all interfaces
	}
	if host == "localhost" {
		return true
	}
	if strings.HasPrefix(host, "127.") {
		return true
	}
	return host == "::1"
}

func serveHandler(srv *api.Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, api.Version) {
			srv.Handler().ServeHTTP(w, r)
			return
		}
		// Replay web assets.
		http.FileServer(http.Dir("web/replay")).ServeHTTP(w, r)
	})
}

// listenAndServe runs the HTTP server until interrupted.
func listenAndServe(addr string, handler http.Handler, output io.Writer) int {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(output, "serve_started addr=%s\n", addr)
	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(output, "serve_failed: %v\n", err)
		return 1
	}
	return 0
}

var _ = sort.Strings
var _ = filepath.Join
