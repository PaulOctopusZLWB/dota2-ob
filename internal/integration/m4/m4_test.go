package m4_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureSHA256 = "2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e"

// Product composition is exercised from cmd/dota2-ob, where the real startup
// graph and its private production ports are available. This package keeps the
// captured evidence immutable and independently checks that neither fixture nor
// golden contains credentials.
func TestCapturedEvidenceIdentityAndPrivacy(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "captured_gsi_schedule.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(fixture)
	if hex.EncodeToString(sum[:]) != fixtureSHA256 {
		t.Fatalf("fixture identity changed: %x", sum)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "replay_output.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{fixture, golden} {
		lower := strings.ToLower(string(payload))
		for _, forbidden := range []string{"authorization", "bearer ", "steam_api_key", "password", "cookie"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("captured evidence contains forbidden token %q", forbidden)
			}
		}
	}
}
