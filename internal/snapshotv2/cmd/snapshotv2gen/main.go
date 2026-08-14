package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	fixed := map[string]string{}
	for name, path := range map[string]string{
		"contracts.go":         "internal/contracts/contracts.go",
		"live_mapping.go":      "internal/capture/live_observation.go",
		"session_highwater.go": "internal/session/highwater.go",
		"session_follower.go":  "internal/session/live_projector.go",
	} {
		payload, err := os.ReadFile(filepath.Join(*root, path))
		must(err)
		sum := sha256.Sum256(payload)
		fixed[name] = hex.EncodeToString(sum[:])
	}
	snapshotRoot := filepath.Join(*root, "internal", "snapshotv2")
	generated, _, err := snapshotv2.GenerateCompiled(os.DirFS(snapshotRoot), fixed)
	must(err)
	for _, target := range snapshotv2.GeneratedTargets() {
		path := filepath.Join(snapshotRoot, target)
		must(os.MkdirAll(filepath.Dir(path), 0o755))
		must(os.WriteFile(path, generated[target], 0o644))
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
