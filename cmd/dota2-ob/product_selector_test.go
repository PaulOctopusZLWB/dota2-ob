package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductSelectorIsClosedAndExplicit(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		mode      string
		delegated []string
		wantErr   bool
	}{
		{name: "default", args: []string{"--doctor"}, mode: productModeSnapshotV2, delegated: []string{"--doctor"}},
		{name: "double dash separated", args: []string{"--policy-mode", productModeSnapshotV2, "--doctor"}, mode: productModeSnapshotV2, delegated: []string{"--doctor"}},
		{name: "double dash equals", args: []string{"--policy-mode=" + productModeLiveOnlyV3, "--doctor"}, mode: productModeLiveOnlyV3, delegated: []string{"--doctor"}},
		{name: "single dash separated", args: []string{"-policy-mode", productModeLiveOnlyV3, "-doctor"}, mode: productModeLiveOnlyV3, delegated: []string{"-doctor"}},
		{name: "single dash equals", args: []string{"-policy-mode=" + productModeLiveOnlyV3, "-doctor"}, mode: productModeLiveOnlyV3, delegated: []string{"-doctor"}},
		{name: "terminator stops selection", args: []string{"--", "--policy-mode=" + productModeLiveOnlyV3}, mode: productModeSnapshotV2, delegated: []string{"--", "--policy-mode=" + productModeLiveOnlyV3}},
		{name: "terminator after parsed flag", args: []string{"-doctor", "--", "-policy-mode=" + productModeLiveOnlyV3}, mode: productModeSnapshotV2, delegated: []string{"-doctor", "--", "-policy-mode=" + productModeLiveOnlyV3}},
		{name: "positional stops selection", args: []string{"capture", "--policy-mode=" + productModeLiveOnlyV3}, mode: productModeSnapshotV2, delegated: []string{"capture", "--policy-mode=" + productModeLiveOnlyV3}},
		{name: "repeated last wins", args: []string{"-policy-mode=" + productModeSnapshotV2, "--policy-mode", productModeLiveOnlyV3, "-doctor"}, mode: productModeLiveOnlyV3, delegated: []string{"-doctor"}},
		{name: "missing", args: []string{"--policy-mode"}, wantErr: true},
		{name: "invalid value is parsed before contract validation", args: []string{"--policy-mode=other"}, mode: "other"},
		{name: "unknown flag before stop", args: []string{"--not-a-product-flag"}, wantErr: true},
		{name: "unknown flag after positional ignored", args: []string{"capture", "--not-a-product-flag"}, mode: productModeSnapshotV2, delegated: []string{"capture", "--not-a-product-flag"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			options, delegated, err := parseRunOptions(test.args, &output)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v output=%q", err, output.String())
			}
			if err == nil && (options.policyMode.value != test.mode || !equalStrings(delegated, test.delegated)) {
				t.Fatalf("mode=%q delegated=%q", options.policyMode.value, delegated)
			}
		})
	}
}

func TestProductSelectorExecutableDispatchProbes(t *testing.T) {
	tests := []struct {
		name, wantRoute string
		args            []string
		wantDelegated   []string
		wantDoctor      bool
	}{
		{name: "review terminator reproduction", args: []string{"--", "--policy-mode=v3-live-only"}, wantRoute: "v2", wantDelegated: []string{"--", "--policy-mode=v3-live-only"}},
		{name: "review single dash reproduction", args: []string{"-policy-mode=v3-live-only", "-doctor"}, wantRoute: "v3", wantDoctor: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var route string
			var delegated []string
			code := runProducts(test.args, &bytes.Buffer{}, func(args []string, _ io.Writer) int {
				route, delegated = "v2", append([]string(nil), args...)
				return 22
			}, func(options runOptions, _ io.Writer) int {
				route = "v3"
				if *options.doctorMode != test.wantDoctor {
					t.Fatalf("doctor=%t want %t", *options.doctorMode, test.wantDoctor)
				}
				return 33
			})
			if route != test.wantRoute || (route == "v2" && code != 22) || (route == "v3" && code != 33) || !equalStrings(delegated, test.wantDelegated) {
				t.Fatalf("route=%s code=%d delegated=%q", route, code, delegated)
			}
		})
	}
}

func TestProductSelectorInvalidModePreservesExactContract(t *testing.T) {
	for _, args := range [][]string{{"--policy-mode=other"}, {"-policy-mode=other"}} {
		var output bytes.Buffer
		code := runProducts(args, &output, func([]string, io.Writer) int {
			t.Fatal("invalid mode reached V2")
			return 0
		}, func(runOptions, io.Writer) int {
			t.Fatal("invalid mode reached V3")
			return 0
		})
		if code != 1 || output.String() != "policy_mode_invalid\n" {
			t.Fatalf("args=%q code=%d stderr=%q", args, code, output.String())
		}
	}
}

func TestProductSelectorExecutableProcessProbes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "terminator retains V2", args: []string{"--", "--policy-mode=v3-live-only"}, want: `route=v2 delegated=["--","--policy-mode=v3-live-only"]`},
		{name: "single dash selects V3 doctor", args: []string{"-policy-mode=v3-live-only", "-doctor"}, want: "route=v3 doctor=true"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.args)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestProductSelectorProbeHelper$")
			command.Env = append(os.Environ(), "DOTA2_OB_SELECTOR_PROBE=1", "DOTA2_OB_SELECTOR_ARGS="+string(encoded))
			output, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("probe err=%v output=%s", err, output)
			}
		})
	}
}

func TestProductSelectorInvalidModeExecutableProcessProbes(t *testing.T) {
	for _, argument := range []string{"--policy-mode=other", "-policy-mode=other"} {
		command := exec.Command(os.Args[0], "-test.run=^TestProductSelectorInvalidModeProcessHelper$")
		command.Env = append(os.Environ(), "DOTA2_OB_INVALID_SELECTOR_PROBE="+argument)
		output, err := command.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 || string(output) != "policy_mode_invalid\n" {
			t.Fatalf("argument=%s err=%v output=%q", argument, err, output)
		}
	}
}

func TestProductSelectorInvalidModeProcessHelper(t *testing.T) {
	argument := os.Getenv("DOTA2_OB_INVALID_SELECTOR_PROBE")
	if argument == "" {
		t.Skip("invalid selector executable probe helper")
	}
	os.Exit(runProducts([]string{argument}, os.Stderr, func([]string, io.Writer) int { return 20 }, func(runOptions, io.Writer) int { return 30 }))
}

func TestProductSelectorProbeHelper(t *testing.T) {
	if os.Getenv("DOTA2_OB_SELECTOR_PROBE") != "1" {
		t.Skip("selector executable probe helper")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("DOTA2_OB_SELECTOR_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	code := runProducts(args, os.Stderr, func(delegated []string, _ io.Writer) int {
		encoded, _ := json.Marshal(delegated)
		fmt.Printf("route=v2 delegated=%s\n", encoded)
		return 0
	}, func(options runOptions, _ io.Writer) int {
		fmt.Printf("route=v3 doctor=%t\n", *options.doctorMode)
		return 0
	})
	if code != 0 {
		t.Fatalf("selector probe code=%d", code)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestActualBinarySelectorIsIdentityBoundAndDelegatesCompleteV2(t *testing.T) {
	payload, err := os.ReadFile("product_selector.go")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if got := hex.EncodeToString(sum[:]); got != productSelectorSourceSHA256 {
		t.Fatalf("selector source identity=%s want %s", got, productSelectorSourceSHA256)
	}
	text := string(payload)
	for _, required := range []string{"runProducts(args, output, snapshotproduct.Run", "runWithParsedDependencies(options, output, defaultRunDependencies())"} {
		if !strings.Contains(text, required) {
			t.Fatalf("actual selector missing delegation %q", required)
		}
	}
	for _, forbidden := range []string{"loadPolicyLineage(", "newBroadcastRuntime(", "newPolicyProjectionRunner("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("actual selector retained partial V2 path %q", forbidden)
		}
	}
	mainSource, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mainSource), "func main()") || strings.Contains(string(mainSource), "func run(") {
		t.Fatal("mutable current startup still owns the production entry")
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "product_selector.go" {
			continue
		}
		candidate, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(candidate), "snapshotproduct.Run(") {
			t.Fatalf("unbound production V2 entry found in %s", file)
		}
	}
}
