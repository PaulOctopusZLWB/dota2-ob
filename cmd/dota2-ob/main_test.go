package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type commandListener struct{ address net.Addr }

func (l commandListener) Accept() (net.Conn, error) { return nil, errors.New("unused") }
func (l commandListener) Close() error              { return nil }
func (l commandListener) Addr() net.Addr            { return l.address }

type commandAddress string

func (a commandAddress) Network() string { return "tcp" }
func (a commandAddress) String() string  { return string(a) }

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

func TestRunNormalAddressMatrixUsesNormalizedListener(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"127.0.0.1:43210", "127.0.0.1:43210"},
		{"localhost:43210", "127.0.0.1:43210"},
		{"[::1]:43210", "[::1]:43210"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			var listened string
			deps := defaultRunDependencies()
			deps.listen = func(_ string, address string) (net.Listener, error) {
				listened = address
				return commandListener{address: commandAddress(address)}, nil
			}
			deps.runLifecycle = func(_ lifecycle.Server, _ net.Listener, appender lifecycle.Closer, _ lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
				return appender.Close()
			}
			var output bytes.Buffer
			if code := runWithDependencies([]string{"--addr", tc.input, "--data-dir", root}, &output, deps); code != 0 {
				t.Fatalf("exit=%d output=%q", code, output.String())
			}
			if listened != tc.want {
				t.Fatalf("listener address=%q want %q", listened, tc.want)
			}
		})
	}
}

func TestRunNormalRejectsAddressBeforeAnySideEffect(t *testing.T) {
	for _, address := range []string{":43210", "10.0.0.1:43210", "0.0.0.0:43210", "[::]:43210", "127.0.0.1:0"} {
		t.Run(address, func(t *testing.T) {
			deps := defaultRunDependencies()
			storeCalls, listenCalls, lifecycleCalls := 0, 0, 0
			deps.newStore = func(string) (*session.Store, error) { storeCalls++; return nil, errors.New("must not run") }
			deps.listen = func(string, string) (net.Listener, error) { listenCalls++; return nil, errors.New("must not run") }
			deps.runLifecycle = func(lifecycle.Server, net.Listener, lifecycle.Closer, lifecycle.Waiter, <-chan os.Signal, lifecycle.ContextFactory) error {
				lifecycleCalls++
				return nil
			}
			var output bytes.Buffer
			if code := runWithDependencies([]string{"--addr", address}, &output, deps); code != 1 {
				t.Fatalf("exit=%d", code)
			}
			if storeCalls != 0 || listenCalls != 0 || lifecycleCalls != 0 {
				t.Fatalf("side effects store/listen/lifecycle=%d/%d/%d", storeCalls, listenCalls, lifecycleCalls)
			}
		})
	}
}

func TestRunDoctorExplicitInvalidConfigReturnsOne(t *testing.T) {
	deps := defaultRunDependencies()
	deps.runDoctor = func(config preflight.DoctorConfig) preflight.Result {
		if config.GSIConfig != "invalid.cfg" {
			t.Fatalf("gsi config=%q", config.GSIConfig)
		}
		return preflight.Result{OK: false, Checks: []preflight.Check{{ID: "gsi_config", Status: preflight.Fail, Message: "GSI config URI does not match"}}}
	}
	var output bytes.Buffer
	if code := runWithDependencies([]string{"--doctor", "--gsi-config", "invalid.cfg"}, &output, deps); code != 1 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	var result preflight.Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.OK || result.Checks[0].ID != "gsi_config" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
