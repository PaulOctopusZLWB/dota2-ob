package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/m4match"
)

func runRehearsalCommand(args []string, repo string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	ctx := context.Background()
	write := func(value any, err error) int {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err = json.NewEncoder(os.Stdout).Encode(value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	switch args[0] {
	case "rehearsal-preflight":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		root := flags.String("data-root", "", "fresh absolute rehearsal evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		purpose := flags.String("run-purpose", "public_match_rehearsal", "closed rehearsal purpose")
		class := flags.String("match-class", "public_match", "closed match class")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return true, 2
		}
		if _, err := m4match.ParseRehearsalClassification(*purpose, *class); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return true, 2
		}
		result, err := m4match.RehearsalPreflight(ctx, m4match.RehearsalPreflightConfig{DataRoot: *root, RepoRoot: *repoRoot})
		status := write(result, err)
		if err == nil && !result.Ready {
			status = 1
		}
		return true, status
	case "rehearsal-attempt":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		root := flags.String("data-root", "", "exact harness-owned rehearsal root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		expectedMatch := flags.String("expected-match-id", "", "optional expected identity selector; never evidence")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return true, 2
		}
		result, err := m4match.RehearsalAttempt(ctx, m4match.RehearsalAttemptConfig{DataRoot: *root, RepoRoot: *repoRoot, Request: m4match.RehearsalAttemptRequestV1{SchemaVersion: m4match.RehearsalAttemptSchemaVersion, ExpectedMatchID: *expectedMatch}})
		return true, write(result, err)
	case "rehearsal-terminal-verify":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		root := flags.String("data-root", "", "exact harness-owned rehearsal root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return true, 2
		}
		result, err := m4match.VerifyRehearsalTerminal(ctx, *root, *repoRoot)
		return true, write(result, err)
	case "rehearsal-cleanup":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		root := flags.String("data-root", "", "exact harness-owned rehearsal root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		token := flags.String("confirm-terminal-sha256", "", "verified terminal hash cleanup token")
		if flags.Parse(args[1:]) != nil || *root == "" || *token == "" {
			return true, 2
		}
		if err := m4match.CleanupRehearsal(*root, *repoRoot, *token); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return true, 1
		}
		fmt.Fprintln(os.Stdout, "cleanup_complete")
		return true, 0
	default:
		return false, 0
	}
}
