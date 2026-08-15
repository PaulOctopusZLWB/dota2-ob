package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
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
		{name: "snapshot pair", args: []string{"--policy-mode", productModeSnapshotV2, "--doctor"}, mode: productModeSnapshotV2, delegated: []string{"--doctor"}},
		{name: "live equal", args: []string{"--policy-mode=" + productModeLiveOnlyV3, "--doctor"}, mode: productModeLiveOnlyV3, delegated: []string{"--doctor"}},
		{name: "missing", args: []string{"--policy-mode"}, wantErr: true},
		{name: "unknown", args: []string{"--policy-mode=other"}, wantErr: true},
		{name: "repeated", args: []string{"--policy-mode=v2-snapshot", "--policy-mode=v3-live-only"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, delegated, err := selectProductMode(test.args)
			if (err != nil) != test.wantErr || mode != test.mode || !slices.Equal(delegated, test.delegated) {
				t.Fatalf("mode=%q delegated=%q err=%v", mode, delegated, err)
			}
		})
	}
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
	for _, required := range []string{"snapshotproduct.Run(delegated, output)", "runWithDependencies(args, output, defaultRunDependencies())"} {
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
