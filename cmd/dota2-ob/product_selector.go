package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	snapshotproduct "github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/product"
)

const (
	productModeSnapshotV2 = "v2-snapshot"
	productModeLiveOnlyV3 = "v3-live-only"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

// run is the complete closed product selector. Snapshot V2 leaves the current
// command package before any startup side effect and executes the generated,
// identity-bearing product entry. Live-only V3 retains the current production
// composition. This exact source digest is bound into both product builds.
func run(args []string, output io.Writer) int {
	return runProducts(args, output, snapshotproduct.Run, func(options runOptions, output io.Writer) int {
		return runWithParsedDependencies(options, output, defaultRunDependencies())
	})
}

func runProducts(args []string, output io.Writer, runV2 func([]string, io.Writer) int, runV3 func(runOptions, io.Writer) int) int {
	options, delegated, err := parseRunOptions(args, output)
	if err != nil {
		return 2
	}
	switch options.policyMode.value {
	case productModeSnapshotV2:
		return runV2(delegated, output)
	case productModeLiveOnlyV3:
		return runV3(options, output)
	default:
		fmt.Fprintln(output, "policy_mode_invalid")
		return 1
	}
}

type productModeValue struct{ value string }

func (v *productModeValue) String() string { return v.value }
func (v *productModeValue) Set(value string) error {
	v.value = value
	return nil
}

type runOptions struct {
	addr, deliveryAddr, dataDir, sessionID, operatorTokenFile *string
	policyMode                                                *productModeValue
	policyLineageFile, historyBindingFile                     *string
	liveOnlyLineageFile, liveOnlyReleaseFile                  *string
	analyzeSession, gsiConfig                                 *string
	diagnosticMode, doctorMode                                *bool
	staleThreshold                                            *time.Duration
}

// parseRunOptions is the sole command grammar used by product selection and
// current V3 startup. It is the standard Go flag parser, so terminators,
// positional-stop behavior, single/double dashes, and repeated flags cannot
// diverge between dispatch and execution.
func parseRunOptions(args []string, output io.Writer) (runOptions, []string, error) {
	flags := flag.NewFlagSet("dota2-ob", flag.ContinueOnError)
	flags.SetOutput(output)
	options := runOptions{}
	options.addr = flags.String("addr", "127.0.0.1:43210", "HTTP listen address")
	options.deliveryAddr = flags.String("delivery-addr", "127.0.0.1:43211", "operator/overlay loopback listen address")
	options.dataDir = flags.String("data-dir", "./data/sessions", "directory for captured session data")
	options.sessionID = flags.String("session-id", "", "explicit safe session identity for sealed policy lineage and restart")
	options.diagnosticMode = flags.Bool("diagnostic-mode", false, "enable authenticated legacy capture diagnostics")
	options.operatorTokenFile = flags.String("operator-token-file", "", "explicit external 0600 token handoff path for the operator process")
	options.policyMode = &productModeValue{value: productModeSnapshotV2}
	flags.Var(options.policyMode, "policy-mode", "explicit policy mode: v2-snapshot or v3-live-only")
	options.policyLineageFile = flags.String("policy-lineage-file", "", "sealed PolicyLineageManifestV2 for the broadcast policy plane")
	options.historyBindingFile = flags.String("history-binding-file", "", "strict canonical HistoryAvailabilityBindingV1 for v3-live-only")
	options.liveOnlyLineageFile = flags.String("live-only-lineage-file", "", "strict canonical PolicyLineageManifestV3 for v3-live-only")
	options.liveOnlyReleaseFile = flags.String("live-only-release-file", "", "strict canonical LiveOnlyReleaseBindingV1 for v3-live-only")
	options.analyzeSession = flags.String("analyze-session", "", "offline: analyze a session directory and exit")
	options.doctorMode = flags.Bool("doctor", false, "run one-shot operator readiness checks and exit")
	options.gsiConfig = flags.String("gsi-config", "", "explicit Dota 2 GSI config path for doctor mode")
	options.staleThreshold = flags.Duration("stale-threshold", 15*time.Second, "duration without accepted GSI before status becomes stale")
	if err := flags.Parse(args); err != nil {
		return runOptions{}, nil, err
	}
	return options, removeConsumedPolicyFlags(args, len(args)-flags.NArg()), nil
}

// removeConsumedPolicyFlags changes no parsing decision. It only removes the
// already parsed selector flag before immutable V2 delegation; arguments at or
// after the Go parser's terminator/positional stop remain byte-for-byte intact.
func removeConsumedPolicyFlags(args []string, consumed int) []string {
	delegated := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if index < consumed && (argument == "-policy-mode" || argument == "--policy-mode") {
			index++
			continue
		}
		if index < consumed && (strings.HasPrefix(argument, "-policy-mode=") || strings.HasPrefix(argument, "--policy-mode=")) {
			continue
		}
		delegated = append(delegated, argument)
	}
	return delegated
}
