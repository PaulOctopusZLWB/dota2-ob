package main

import (
	"fmt"
	"io"
	"os"
	"strings"

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
	mode, delegated, err := selectProductMode(args)
	if err != nil {
		fmt.Fprintln(output, "policy_mode_invalid")
		return 1
	}
	switch mode {
	case productModeSnapshotV2:
		return snapshotproduct.Run(delegated, output)
	case productModeLiveOnlyV3:
		return runWithDependencies(args, output, defaultRunDependencies())
	default:
		fmt.Fprintln(output, "policy_mode_invalid")
		return 1
	}
}

// selectProductMode defaults explicitly to snapshot V2 and removes only the
// selector flag before delegation because the immutable V2 entry predates it.
func selectProductMode(args []string) (string, []string, error) {
	mode := productModeSnapshotV2
	delegated := make([]string, 0, len(args))
	seen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--policy-mode":
			if seen || index+1 >= len(args) {
				return "", nil, fmt.Errorf("policy mode is missing or repeated")
			}
			seen = true
			mode = args[index+1]
			index++
		case strings.HasPrefix(argument, "--policy-mode="):
			if seen {
				return "", nil, fmt.Errorf("policy mode is repeated")
			}
			seen = true
			mode = strings.TrimPrefix(argument, "--policy-mode=")
		default:
			delegated = append(delegated, argument)
		}
	}
	if mode != productModeSnapshotV2 && mode != productModeLiveOnlyV3 {
		return "", nil, fmt.Errorf("policy mode is invalid")
	}
	return mode, delegated, nil
}
