package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
		var purpose, matchClass uniqueString
		flags.Var(&purpose, "purpose", "closed run purpose")
		flags.Var(&matchClass, "match-class", "closed match class")
		root := flags.String("data-root", "", "fresh absolute evidence root outside the repository")
		repoRoot := flags.String("repo-root", repo, "repository root")
		width := flags.Int("width", 1920, "OBS canvas width")
		height := flags.Int("height", 1080, "OBS canvas height")
		if flags.Parse(args[1:]) != nil || *root == "" {
			return 2
		}
		classification, classifyErr := m4match.ParseRunClassification(purpose.value, matchClass.value)
		if classifyErr != nil {
			fmt.Fprintln(os.Stderr, classifyErr)
			return 2
		}
		result, err := m4match.Preflight(context.Background(), m4match.PreflightConfig{DataRoot: *root, RepoRoot: *repoRoot, Width: *width, Height: *height, Classification: classification})
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
		var purpose, matchClass uniqueString
		flags.Var(&purpose, "purpose", "closed run purpose")
		flags.Var(&matchClass, "match-class", "closed match class")
		root := flags.String("data-root", "", "evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		expect := flags.String("expect", "preflight", "preflight or live")
		if flags.Parse(args[1:]) != nil || *root == "" || (*expect != "preflight" && *expect != "live" && *expect != "rehearsal_complete" && *expect != "rehearsal_failed") {
			return 2
		}
		classification, classifyErr := m4match.ParseRunClassification(purpose.value, matchClass.value)
		if classifyErr != nil {
			fmt.Fprintln(os.Stderr, classifyErr)
			return 2
		}
		result, err := m4match.VerifyClassification(context.Background(), *root, *repoRoot, *expect, classification)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		if *expect == "preflight" && !result.Ready {
			return 1
		}
		return 0
	case "authority-preflight":
		flags := flag.NewFlagSet("m4-match authority-preflight", flag.ContinueOnError)
		root := flags.String("data-root", "", "fresh authority evidence root")
		readinessRoot := flags.String("readiness-root", "", "verified public-tournament readiness root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		selection := flags.String("selection", "", "canonical selected-match JSON")
		credentialFD := flags.String("webapi-key-fd", "3", "external secret-channel file descriptor")
		if flags.Parse(args[1:]) != nil || *root == "" || *readinessRoot == "" || *selection == "" {
			return 2
		}
		fd, fdErr := strconv.Atoi(*credentialFD)
		if fdErr != nil || fd < 3 {
			fmt.Fprintln(os.Stderr, "webapi-key-fd must be an inherited descriptor >= 3")
			return 2
		}
		secret := os.NewFile(uintptr(fd), "webapi-key-channel")
		if secret == nil {
			fmt.Fprintln(os.Stderr, "webapi key channel unavailable")
			return 2
		}
		defer secret.Close()
		result, err := m4match.PreflightMatchAuthority(context.Background(), m4match.AuthorityPreflightConfig{DataRoot: *root, ReadinessRoot: *readinessRoot, RepoRoot: *repoRoot, SelectionPath: *selection, Credential: secret})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return 0
	case "cleanup":
		flags := flag.NewFlagSet("m4-match cleanup", flag.ContinueOnError)
		var purpose, matchClass uniqueString
		flags.Var(&purpose, "purpose", "closed run purpose")
		flags.Var(&matchClass, "match-class", "closed match class")
		root := flags.String("data-root", "", "exact evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		confirm := flags.String("confirm-index-sha256", "", "required evidence index SHA-256")
		if flags.Parse(args[1:]) != nil || *root == "" || *confirm == "" {
			return 2
		}
		classification, classifyErr := m4match.ParseRunClassification(purpose.value, matchClass.value)
		if classifyErr != nil {
			fmt.Fprintln(os.Stderr, classifyErr)
			return 2
		}
		if err := m4match.CleanupClassification(*root, *repoRoot, *confirm, classification); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("cleanup_complete")
		return 0
	case "live":
		flags := flag.NewFlagSet("m4-match live", flag.ContinueOnError)
		var purpose, matchClass uniqueString
		flags.Var(&purpose, "purpose", "closed run purpose")
		flags.Var(&matchClass, "match-class", "closed match class")
		root := flags.String("data-root", "", "fresh live evidence root")
		readinessRoot := flags.String("readiness-root", "", "verified preflight evidence root")
		repoRoot := flags.String("repo-root", repo, "repository root")
		identity := flags.String("identity", "", "public match identity JSON file")
		authorityRoot := flags.String("authority-root", "", "descriptor-confined sealed authority evidence root for public_tournament")
		if flags.Parse(args[1:]) != nil || *root == "" || *readinessRoot == "" || *identity == "" {
			return 2
		}
		classification, classifyErr := m4match.ParseRunClassification(purpose.value, matchClass.value)
		if classifyErr != nil {
			fmt.Fprintln(os.Stderr, classifyErr)
			return 2
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		cleanAuthorityRoot := ""
		if *authorityRoot != "" {
			cleanAuthorityRoot = filepath.Clean(*authorityRoot)
		}
		result, err := m4match.Live(ctx, m4match.LiveConfig{DataRoot: *root, ReadinessRoot: *readinessRoot, RepoRoot: *repoRoot, IdentityPath: filepath.Clean(*identity), AuthorityRoot: cleanAuthorityRoot, Input: os.Stdin, Output: os.Stdout, Classification: classification})
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
	fmt.Fprintln(os.Stderr, "usage: m4-match {preflight|authority-preflight|live|verify|cleanup} [flags]")
}

type uniqueString struct {
	value string
	set   bool
}

func (value *uniqueString) String() string { return value.value }
func (value *uniqueString) Set(next string) error {
	if value.set {
		return errors.New("flag may be specified exactly once")
	}
	value.value, value.set = next, true
	return nil
}
