package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/m4match"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	repo, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch args[0] {
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
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
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
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
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
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("cleanup_complete")
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
		result, err := m4match.Live(ctx, m4match.LiveConfig{DataRoot: *root, ReadinessRoot: *readinessRoot, RepoRoot: *repoRoot, IdentityPath: filepath.Clean(*identity), Input: os.Stdin, Output: os.Stdout})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return 0
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: m4-match {preflight|live|verify|cleanup} [flags]")
}
