package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsUnsafeNormalAddressBeforeCreatingDataRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	var output bytes.Buffer
	code := run([]string{"--addr", "0.0.0.0:43210", "--data-dir", root}, &output)
	if code != 1 {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("data root was touched: %v", err)
	}
	if !strings.Contains(output.String(), "listen_address_not_loopback") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestRunInvalidFlagReturnsTwo(t *testing.T) {
	var output bytes.Buffer
	if code := run([]string{"--does-not-exist"}, &output); code != 2 {
		t.Fatalf("exit=%d", code)
	}
}

func TestRunDoctorInvalidAddressStillReturnsOrderedJSON(t *testing.T) {
	var output bytes.Buffer
	code := run([]string{"--doctor", "--addr", "0.0.0.0:43210", "--data-dir", t.TempDir()}, &output)
	if code != 1 {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	var result struct {
		Checks []struct {
			ID string `json:"id"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("doctor output is not JSON: %v; %q", err, output.String())
	}
	want := []string{"listen_address", "listen_available", "data_root", "dashboard_asset", "gsi_config"}
	if len(result.Checks) != len(want) {
		t.Fatalf("checks=%#v", result.Checks)
	}
	for i := range want {
		if result.Checks[i].ID != want[i] {
			t.Fatalf("check %d=%q want %q", i, result.Checks[i].ID, want[i])
		}
	}
}
