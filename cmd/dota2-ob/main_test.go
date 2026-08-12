package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
			tokenDirectory := t.TempDir()
			if err := os.Chmod(tokenDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			tokenPath := filepath.Join(tokenDirectory, "operator.token")
			var listened []string
			deps := defaultRunDependencies()
			deps.listen = func(_ string, address string) (net.Listener, error) {
				listened = append(listened, address)
				return commandListener{address: commandAddress(address)}, nil
			}
			deps.runLifecycle = func(_ lifecycle.Server, _ net.Listener, appender lifecycle.Closer, _ lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
				return appender.Close()
			}
			var output bytes.Buffer
			if code := runWithDependencies([]string{"--addr", tc.input, "--data-dir", root, "--operator-token-file", tokenPath}, &output, deps); code != 0 {
				t.Fatalf("exit=%d output=%q", code, output.String())
			}
			if len(listened) != 2 || listened[0] != tc.want || listened[1] != "127.0.0.1:43211" {
				t.Fatalf("listener addresses=%q want capture=%q delivery=127.0.0.1:43211", listened, tc.want)
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

func TestRunRejectsCollidingCaptureAndDeliveryAddressesBeforeDataSideEffects(t *testing.T) {
	deps := defaultRunDependencies()
	storeCalls, listenCalls := 0, 0
	deps.newStore = func(string) (*session.Store, error) { storeCalls++; return nil, errors.New("must not run") }
	deps.listen = func(string, string) (net.Listener, error) { listenCalls++; return nil, errors.New("must not run") }
	var output bytes.Buffer
	code := runWithDependencies([]string{"--addr", "127.0.0.1:43210", "--delivery-addr", "localhost:43210"}, &output, deps)
	if code != 1 || storeCalls != 0 || listenCalls != 0 || !strings.Contains(output.String(), "listener_addresses_must_be_distinct") {
		t.Fatalf("code=%d store/listen=%d/%d output=%q", code, storeCalls, listenCalls, output.String())
	}
}

func TestPairedServerPropagatesDeliveryServeFailure(t *testing.T) {
	captureListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer captureListener.Close()
	servers := &pairedHTTPServer{
		capture:          &http.Server{Handler: http.NotFoundHandler()},
		delivery:         &http.Server{Handler: http.NotFoundHandler()},
		deliveryListener: commandListener{address: commandAddress("127.0.0.1:43211")},
	}
	err = servers.Serve(captureListener)
	if err == nil || !strings.Contains(err.Error(), "unused") {
		t.Fatalf("serve error=%v", err)
	}
	_ = servers.Close()
}

func TestRunCreatesPrivateEphemeralTokenAndProductionCaptureProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	deps := defaultRunDependencies()
	createTokenFile := deps.newTokenFile
	var tokenPath string
	deps.newTokenFile = func(explicit string) (string, string, func(), error) {
		token, path, cleanup, err := createTokenFile(explicit)
		tokenPath = path
		return token, path, cleanup, err
	}
	deps.listen = func(_ string, address string) (net.Listener, error) {
		return commandListener{address: commandAddress(address)}, nil
	}
	var tokenValue string
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, _ lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		store := appender.(*session.Store)
		if strings.HasPrefix(tokenPath, store.SessionDir()+string(os.PathSeparator)) {
			t.Fatalf("token path is inside session root: %s", tokenPath)
		}
		info, err := os.Stat(tokenPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("token stat=%v mode=%v", err, info.Mode())
		}
		data, err := os.ReadFile(tokenPath)
		if err != nil || len(bytes.TrimSpace(data)) < 32 {
			t.Fatalf("token len=%d err=%v", len(data), err)
		}
		tokenValue = strings.TrimSpace(string(data))
		handler := server.(*pairedHTTPServer).capture.Handler
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/latest", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("production latest status=%d", rec.Code)
		}
		return appender.Close()
	}
	var output bytes.Buffer
	tokenDirectory := t.TempDir()
	if err := os.Chmod(tokenDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	explicitTokenPath := filepath.Join(tokenDirectory, "operator.token")
	if code := runWithDependencies([]string{"--data-dir", root, "--operator-token-file", explicitTokenPath}, &output, deps); code != 0 {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("ephemeral token remains: %v", err)
	}
	if tokenValue == "" || strings.Contains(output.String(), tokenValue) {
		t.Fatalf("token leaked to logs: %q", output.String())
	}
}

func TestRunDiagnosticModeUsesSameEphemeralBearer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	deps := defaultRunDependencies()
	createTokenFile := deps.newTokenFile
	var tokenPath string
	deps.newTokenFile = func(explicit string) (string, string, func(), error) {
		token, path, cleanup, err := createTokenFile(explicit)
		tokenPath = path
		return token, path, cleanup, err
	}
	deps.listen = func(_ string, address string) (net.Listener, error) {
		return commandListener{address: commandAddress(address)}, nil
	}
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, _ lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		token, err := os.ReadFile(tokenPath)
		if err != nil {
			t.Fatal(err)
		}
		handler := server.(*pairedHTTPServer).capture.Handler
		unauthorized := httptest.NewRecorder()
		handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/latest", nil))
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized=%d", unauthorized.Code)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/latest", nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		authorized := httptest.NewRecorder()
		handler.ServeHTTP(authorized, req)
		if authorized.Code != http.StatusOK {
			t.Fatalf("authorized=%d body=%s", authorized.Code, authorized.Body.String())
		}
		return appender.Close()
	}
	var output bytes.Buffer
	if code := runWithDependencies([]string{"--data-dir", root, "--diagnostic-mode"}, &output, deps); code != 0 {
		t.Fatalf("code=%d output=%q", code, output.String())
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
