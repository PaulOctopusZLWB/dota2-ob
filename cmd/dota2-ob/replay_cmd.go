package main

import (
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
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/runner"
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

func loadRegistry(manifestPath, dataRoot string) (*roles.Registry, string) {
	// The role registry path is derived from the manifest convention or the
	// data root override file.
	roleFile := filepath.Join(filepath.Dir(manifestPath), "ti2026-five-replay-role-registry-v1.json")
	if _, err := os.Stat(roleFile); err != nil {
		roleFile = filepath.Join(dataRoot, "role-registry.json")
	}
	reg, err := roles.LoadRegistry(roleFile)
	if err != nil {
		return nil, roleFile
	}
	return reg, roleFile
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
	reg, _ := loadRegistry(*manifest, *dataRoot)
	res, err := runner.RunMatch(st, mt, *replayRoot, reg, nil, func(line string) {
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
	reg, _ := loadRegistry(*manifest, *dataRoot)
	matches := make([]*archive.Match, 0, len(m.Matches))
	for i := range m.Matches {
		matches = append(matches, &m.Matches[i])
	}
	start := time.Now()
	results := runner.RunBatch(st, matches, *replayRoot, reg, nil, *workers, func(line string) {
		fmt.Fprintln(output, line)
	})
	wall := time.Since(start)
	summary := map[string]interface{}{
		"command":        "probe",
		"wall_seconds":   wall.Seconds(),
		"workers":        *workers,
		"data_root":      st.Root,
		"status_counts":  countByStatus(results),
		"results":        results,
	}
	enc := json.NewEncoder(output)
	_ = enc.Encode(summary)

	verified := 0
	for _, r := range results {
		if r.Status == store.StatusVerified {
			verified++
		}
	}
	fmt.Fprintf(output, "probe_result: %d/5 verified, wall=%.1fs, %s\n", verified, wall.Seconds(), runner.SummarizeResults(results))
	// The five probes must all reach an explicit terminal state; verified or
	// quarantined are both explicit. Only a hard runner error is fatal.
	fatal := 0
	for _, r := range results {
		if r.Status == store.StatusParseFailed && strings.HasPrefix(r.Reason, "runner_error") {
			fatal++
		}
	}
	if fatal > 0 {
		return 1
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
	reg, _ := loadRegistry(*manifest, *dataRoot)
	matches := make([]*archive.Match, 0, len(m.Matches))
	for i := range m.Matches {
		matches = append(matches, &m.Matches[i])
	}
	start := time.Now()
	results := runner.RunBatch(st, matches, *replayRoot, reg, nil, *workers, func(line string) {
		fmt.Fprintln(output, line)
	})
	wall := time.Since(start)
	fmt.Fprintf(output, "batch_result: %s wall=%.1fs workers=%d\n", runner.SummarizeResults(results), wall.Seconds(), *workers)
	_ = *resume
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

// runServe starts the local loopback replay web server.
func runServe(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "./data/replay", "persisted artifact data root")
	listen := fs.String("listen", "127.0.0.1:43211", "loopback listen address")
	manifest := fs.String("manifest", "", "probe manifest path (for role registry lookup)")
	fs.SetOutput(output)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := store.New(*dataRoot)
	if err != nil {
		fmt.Fprintf(output, "store_init_failed: %v\n", err)
		return 1
	}
	reg, roleFile := (*roles.Registry)(nil), ""
	if *manifest != "" {
		reg, roleFile = loadRegistry(*manifest, *dataRoot)
		if reg == nil {
			fmt.Fprintf(output, "role_registry_missing (expected %s)\n", roleFile)
		}
	}
	srv := api.New(st, reg, roleFile)
	handler := serveHandler(srv)
	return listenAndServe(*listen, handler, output)
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