package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/m4match"
)

var runRehearsalAttempt = m4match.RehearsalAttempt
var runRehearsalVerify = m4match.VerifyRehearsal

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	repo, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch args[0] {
	case "rehearsal-arm", "rehearsal-preflight":
		flags := flag.NewFlagSet("m4-match rehearsal-preflight", flag.ContinueOnError)
		root := flags.String("data-root", "", "fresh absolute rehearsal evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return 2
		}
		result, err := m4match.RehearsalPreflight(context.Background(), m4match.RehearsalPreflightConfig{DataRoot: *root, RepoRoot: *repoRoot})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		if result.ConsoleState != m4match.RehearsalReady {
			return 1
		}
		return 0
	case "rehearsal-disarm":
		flags := flag.NewFlagSet("m4-match rehearsal-disarm", flag.ContinueOnError)
		root := flags.String("data-root", "", "exact harness-owned rehearsal root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		confirm := flags.String("confirm-arm-sha256", "", "required rehearsal arm cleanup token")
		if flags.Parse(args[1:]) != nil || *root == "" || *confirm == "" {
			return 2
		}
		if err := m4match.DisarmRehearsal(*root, *repoRoot, *confirm); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "disarm_complete")
		return 0
	case "rehearsal-attempt":
		flags := flag.NewFlagSet("m4-match rehearsal-attempt", flag.ContinueOnError)
		root := flags.String("data-root", "", "exact harness-owned rehearsal root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return 2
		}
		result, err := runRehearsalAttempt(context.Background(), m4match.RehearsalAttemptConfig{DataRoot: *root, RepoRoot: *repoRoot})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		if result.Outcome != "rehearsal_completed" {
			return 1
		}
		verified, verifyErr := runRehearsalVerify(context.Background(), *root, *repoRoot)
		if verifyErr != nil || verified.TerminalSHA256 != result.TerminalSHA256 {
			fmt.Fprintln(stderr, "completed rehearsal independent verification failed")
			return 1
		}
		return 0
	case "rehearsal-obs-smoke":
		flags := flag.NewFlagSet("m4-match rehearsal-obs-smoke", flag.ContinueOnError)
		root := flags.String("data-root", "", "exact armed rehearsal root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return 2
		}
		result, err := runRehearsalAttempt(context.Background(), m4match.RehearsalAttemptConfig{DataRoot: *root, RepoRoot: *repoRoot, OBSOnlySmoke: true})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		if result.FailureCode != "zero_frame_attempt" {
			return 1
		}
		return 0
	case "rehearsal-verify":
		flags := flag.NewFlagSet("m4-match rehearsal-verify", flag.ContinueOnError)
		root := flags.String("data-root", "", "exact rehearsal evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return 2
		}
		result, err := m4match.VerifyRehearsal(context.Background(), *root, *repoRoot)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		return 0
	case "rehearsal-cleanup":
		flags := flag.NewFlagSet("m4-match rehearsal-cleanup", flag.ContinueOnError)
		root := flags.String("data-root", "", "exact rehearsal evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		confirm := flags.String("confirm-terminal-sha256", "", "required verified rehearsal cleanup token")
		if flags.Parse(args[1:]) != nil || *root == "" || *confirm == "" {
			return 2
		}
		if err := m4match.CleanupRehearsal(context.Background(), *root, *repoRoot, *confirm); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "cleanup_complete")
		return 0
	case "preflight":
		flags := flag.NewFlagSet("m4-match preflight", flag.ContinueOnError)
		root := flags.String("data-root", "", "fresh absolute evidence root outside the repository")
		repoRoot := flags.String("repo-root", repo, "repository root")
		width := flags.Int("width", 1920, "OBS canvas width")
		height := flags.Int("height", 1080, "OBS canvas height")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return 2
		}
		result, err := m4match.Preflight(context.Background(), m4match.PreflightConfig{DataRoot: *root, RepoRoot: *repoRoot, Width: *width, Height: *height})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		if !result.Ready {
			return 1
		}
		return 0
	case "verify":
		flags := flag.NewFlagSet("m4-match verify", flag.ContinueOnError)
		root := flags.String("data-root", "", "evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		expect := flags.String("expect", "preflight", "preflight or live")
		if flags.Parse(args[1:]) != nil || *root == "" || (*expect != "preflight" && *expect != "live") {
			return 2
		}
		result, err := m4match.Verify(context.Background(), *root, *repoRoot, *expect)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		if *expect == "preflight" && !result.Ready {
			return 1
		}
		return 0
	case "cleanup":
		flags := flag.NewFlagSet("m4-match cleanup", flag.ContinueOnError)
		root := flags.String("data-root", "", "exact evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		confirm := flags.String("confirm-index-sha256", "", "required evidence index SHA-256")
		if flags.Parse(args[1:]) != nil || *root == "" || *confirm == "" {
			return 2
		}
		if err := m4match.Cleanup(*root, *repoRoot, *confirm); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "cleanup_complete")
		return 0
	case "live":
		flags := flag.NewFlagSet("m4-match live", flag.ContinueOnError)
		root := flags.String("data-root", "", "fresh live evidence root")
		readinessRoot := flags.String("readiness-root", "", "verified preflight evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		identity := flags.String("identity", "", "public match identity JSON file")
		if flags.Parse(args[1:]) != nil || *root == "" || *readinessRoot == "" || *identity == "" {
			return 2
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		result, err := m4match.Live(ctx, m4match.LiveConfig{DataRoot: *root, ReadinessRoot: *readinessRoot, RepoRoot: *repoRoot, IdentityPath: filepath.Clean(*identity), Input: os.Stdin, Output: stdout})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(result)
		return 0
	default:
		usage(stderr)
		return 2
	}
}

func usage(output io.Writer) {
	fmt.Fprintln(output, "usage: m4-match {rehearsal-arm|rehearsal-disarm|rehearsal-attempt|rehearsal-obs-smoke|rehearsal-verify|rehearsal-cleanup|preflight|live|verify|cleanup} [flags]")
}
