package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
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

type failingDeliveryListener struct {
	address net.Addr
	failed  chan struct{}
	once    sync.Once
}

func (l *failingDeliveryListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.failed) })
	return nil, errors.New("delivery serve failed")
}
func (l *failingDeliveryListener) Close() error   { return nil }
func (l *failingDeliveryListener) Addr() net.Addr { return l.address }

type retryingProjection struct {
	mu        sync.Mutex
	failing   bool
	sequences []uint64
}

func (p *retryingProjection) Apply(_ context.Context, record *session.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing {
		return errors.New("projection unavailable")
	}
	p.sequences = append(p.sequences, record.Sequence)
	return nil
}

func (p *retryingProjection) setFailing(failing bool) {
	p.mu.Lock()
	p.failing = failing
	p.mu.Unlock()
}

func (p *retryingProjection) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sequences)
}

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
			deps.newStore = func(string, string) (*session.Store, error) { storeCalls++; return nil, errors.New("must not run") }
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
	deps.newStore = func(string, string) (*session.Store, error) { storeCalls++; return nil, errors.New("must not run") }
	deps.listen = func(string, string) (net.Listener, error) { listenCalls++; return nil, errors.New("must not run") }
	var output bytes.Buffer
	code := runWithDependencies([]string{"--addr", "127.0.0.1:43210", "--delivery-addr", "localhost:43210"}, &output, deps)
	if code != 1 || storeCalls != 0 || listenCalls != 0 || !strings.Contains(output.String(), "listener_addresses_must_be_distinct") {
		t.Fatalf("code=%d store/listen=%d/%d output=%q", code, storeCalls, listenCalls, output.String())
	}
}

func TestRunDeliveryBindFailureLeavesCapturePersistingGSI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	deps := defaultRunDependencies()
	deps.newTokenFile = func(string) (string, string, func(), error) {
		return "test-only-operator-token", "", func() {}, nil
	}
	listenCalls := 0
	deps.listen = func(_ string, address string) (net.Listener, error) {
		listenCalls++
		if listenCalls == 2 {
			return nil, errors.New("delivery address already in use")
		}
		return commandListener{address: commandAddress(address)}, nil
	}
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		store := appender.(*session.Store)
		request := httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(`{"map":{"game_time":41}}`))
		response := httptest.NewRecorder()
		server.(*pairedHTTPServer).capture.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("POST /gsi status=%d body=%q", response.Code, response.Body.String())
		}
		if data, err := os.ReadFile(store.RawPath()); err != nil || !rawV3Contains(data, `{"map":{"game_time":41}}`) {
			t.Fatalf("persisted raw=%q err=%v", data, err)
		}
		waiter.Wait()
		return appender.Close()
	}

	var output bytes.Buffer
	if code := runWithDependencies([]string{"--data-dir", root}, &output, deps); code != 0 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	if !strings.Contains(output.String(), "delivery_listen_failed") {
		t.Fatalf("missing observable delivery failure: %q", output.String())
	}
}

func TestRunConfiguresCommittedOverlayPort(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	const sessionID = "configured-product"
	lineagePath := filepath.Join(t.TempDir(), "lineage.v2.json")
	writeTestLineage(t, lineagePath, testLineage(sessionID))
	deps := defaultRunDependencies()
	deps.newTokenFile = func(string) (string, string, func(), error) {
		return "test-only-operator-token", "", func() {}, nil
	}
	deps.listen = func(_ string, address string) (net.Listener, error) {
		return commandListener{address: commandAddress(address)}, nil
	}
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, _ lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		product := server.(*pairedHTTPServer)
		if product.delivery == nil {
			t.Fatal("configured product has no delivery server")
		}
		response := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("configured overlay status=%d body=%s", response.Code, response.Body.String())
		}
		operatorRequest := httptest.NewRequest(http.MethodGet, "/v1/operator/state", nil)
		operatorRequest.Header.Set("Authorization", "Bearer test-only-operator-token")
		operatorRequest.Header.Set("Origin", "http://127.0.0.1:43211")
		operatorResponse := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(operatorResponse, operatorRequest)
		if operatorResponse.Code != http.StatusOK {
			t.Fatalf("configured operator status=%d body=%s", operatorResponse.Code, operatorResponse.Body.String())
		}
		command := contracts.OperatorCommandV1{
			SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "product-hide", SessionID: sessionID,
			Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: 0, PolicyTimeMS: time.Now().UTC().UnixMilli(),
		}
		commandBody, _ := json.Marshal(command)
		commandRequest := httptest.NewRequest(http.MethodPost, "/v1/operator/commands", bytes.NewReader(commandBody))
		commandRequest.Header.Set("Authorization", "Bearer test-only-operator-token")
		commandRequest.Header.Set("Origin", "http://127.0.0.1:43211")
		commandRequest.Header.Set("Content-Type", delivery.JSONContentType)
		commandRequest.Header.Set(delivery.CSRFHeader, delivery.CSRFValue)
		commandResponse := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(commandResponse, commandRequest)
		if commandResponse.Code != http.StatusOK {
			t.Fatalf("configured command status=%d body=%s", commandResponse.Code, commandResponse.Body.String())
		}
		return appender.Close()
	}

	var output bytes.Buffer
	if code := runWithDependencies([]string{"--data-dir", root, "--session-id", sessionID, "--policy-lineage-file", lineagePath}, &output, deps); code != 0 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
}

func TestRunMissingLineageDisablesDeliveryWithoutBlockingRawCapture(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	deps := defaultRunDependencies()
	deps.newTokenFile = func(string) (string, string, func(), error) {
		return "test-only-operator-token", "", func() {}, nil
	}
	deps.listen = func(_ string, address string) (net.Listener, error) {
		return commandListener{address: commandAddress(address)}, nil
	}
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		product := server.(*pairedHTTPServer)
		if product.delivery == nil || product.deliveryListener == nil {
			t.Fatal("misconfigured product removed the fail-closed delivery surface")
		}
		overlay := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(overlay, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
		if overlay.Code != http.StatusOK || !strings.Contains(overlay.Body.String(), `"visibility":"hidden"`) || strings.Contains(overlay.Body.String(), `"claim"`) {
			t.Fatalf("fail-closed overlay status=%d body=%s", overlay.Code, overlay.Body.String())
		}
		request := httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(`{"map":{"game_time":41}}`))
		response := httptest.NewRecorder()
		product.capture.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("capture status=%d body=%s", response.Code, response.Body.String())
		}
		waiter.Wait()
		return appender.Close()
	}

	var output bytes.Buffer
	if code := runWithDependencies([]string{"--data-dir", root}, &output, deps); code != 0 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	if !strings.Contains(output.String(), "broadcast_policy_config_failed") {
		t.Fatalf("missing bounded configuration failure: %q", output.String())
	}
}

func TestLoadPolicyLineageAssemblesOwnedCaptureAndPolicyArtifacts(t *testing.T) {
	const sessionID = "assembled-lineage"
	seed := testLineage(sessionID)
	foreign := contracts.PolicyArtifactIdentityV2{Version: "foreign.v1", ContentSHA256: strings.Repeat("f", 64)}
	seed.RawRecordSchema = foreign
	seed.RawRecordFraming = foreign
	seed.RawPayloadSchema = foreign
	seed.LiveObservationSchema = foreign
	seed.ProjectionMapping = foreign
	seed.Rules = foreign
	seed.Config = foreign
	seed.Catalog = foreign
	seed.Terminology = foreign
	seed.LocalizationParameterMapping = foreign
	seed.EngineBuild = foreign
	path := filepath.Join(t.TempDir(), "lineage-input.json")
	writeTestLineage(t, path, seed)

	got, err := loadPolicyLineage(path, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !matchesProductLineage(got, sessionID) {
		t.Fatalf("assembled lineage does not bind owned artifacts: %#v", got)
	}
	if got.TournamentScopeID != seed.TournamentScopeID || got.HistoricalSnapshotID != seed.HistoricalSnapshotID {
		t.Fatalf("assembly replaced external history identities: %#v", got)
	}
}

func TestRunConfiguredProductProjectsCommittedRawIntoDurablePolicy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	const sessionID = "configured-projection"
	lineagePath := filepath.Join(t.TempDir(), "lineage.v2.json")
	writeTestLineage(t, lineagePath, testLineage(sessionID))
	deps := defaultRunDependencies()
	deps.newStore = func(root, _ string) (*session.Store, error) {
		return session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return time.UnixMilli(10_000).UTC() }))
	}
	deps.newTokenFile = func(string) (string, string, func(), error) {
		return "test-only-operator-token", "", func() {}, nil
	}
	deps.listen = func(_ string, address string) (net.Listener, error) {
		return commandListener{address: commandAddress(address)}, nil
	}
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		product := server.(*pairedHTTPServer)
		response := httptest.NewRecorder()
		product.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(`{}`)))
		if response.Code != http.StatusOK {
			t.Fatalf("capture status=%d body=%s", response.Code, response.Body.String())
		}
		policyDir := filepath.Join(root, sessionID)
		deadline := time.Now().Add(time.Second)
		committed := false
		for time.Now().Before(deadline) {
			entries, _ := os.ReadDir(policyDir)
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".pcl2") {
					committed = true
					break
				}
			}
			if committed {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if !committed {
			t.Fatal("configured product did not durably commit the accepted observation")
		}
		overlay := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(overlay, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
		if overlay.Code != http.StatusOK || strings.Contains(overlay.Body.String(), "unavailable") {
			t.Fatalf("overlay status=%d body=%s", overlay.Code, overlay.Body.String())
		}
		waiter.Wait()
		return appender.Close()
	}

	var output bytes.Buffer
	if code := runWithDependencies([]string{"--data-dir", root, "--policy-lineage-file", lineagePath}, &output, deps); code != 0 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
}

func TestRunConfiguredProductConsumesNoOutputWithoutPolicyCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	const sessionID = "projection-rejection"
	lineagePath := filepath.Join(t.TempDir(), "lineage.v2.json")
	writeTestLineage(t, lineagePath, testLineage(sessionID))
	deps := defaultRunDependencies()
	deps.newStore = func(root, _ string) (*session.Store, error) {
		return session.NewStore(root, session.WithSessionID(sessionID), session.WithClock(func() time.Time { return time.UnixMilli(10_000).UTC() }))
	}
	deps.newTokenFile = func(string) (string, string, func(), error) {
		return "test-only-operator-token", "", func() {}, nil
	}
	deps.listen = func(_ string, address string) (net.Listener, error) {
		return commandListener{address: commandAddress(address)}, nil
	}
	deps.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		product := server.(*pairedHTTPServer)
		postGSI := func(body string) {
			t.Helper()
			response := httptest.NewRecorder()
			product.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(body)))
			if response.Code != http.StatusOK {
				t.Fatalf("GSI body=%q status=%d response=%s", body, response.Code, response.Body.String())
			}
		}
		waitProjection := func(sequence uint64, rejected bool) liveprojection.Health {
			t.Helper()
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				response := httptest.NewRecorder()
				product.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
				var status struct {
					LiveProjection liveprojection.Health `json:"live_projection"`
				}
				if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &status) == nil && status.LiveProjection.ProjectedSequence >= sequence && status.LiveProjection.ProjectionRejectionActive == rejected {
					return status.LiveProjection
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatalf("projection did not reach sequence=%d rejected=%t", sequence, rejected)
			return liveprojection.Health{}
		}
		overlay := func() contracts.OverlayStateV1 {
			t.Helper()
			response := httptest.NewRecorder()
			product.delivery.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
			var state contracts.OverlayStateV1
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &state) != nil {
				t.Fatalf("overlay status=%d body=%s", response.Code, response.Body.String())
			}
			return state
		}

		postGSI(`{}`)
		waitProjection(1, false)
		policyDir := filepath.Join(root, sessionID)
		before := policyLogBytes(t, policyDir)
		postGSI(`null`)
		health := waitProjection(2, true)
		if health.LastProjectionRejectionCode != "gsi_projection_non_object" || health.LastProjectionRejectionReason != "top_level_non_object" {
			t.Fatalf("rejection health=%#v", health)
		}
		if after := policyLogBytes(t, policyDir); after != before {
			t.Fatalf("consumed-no-output record appended policy bytes: before=%d after=%d", before, after)
		}
		if state := overlay(); state.Visibility != "hidden" || state.HealthCode != "gsi_projection_non_object" || state.Claim != nil {
			t.Fatalf("terminal no-output did not hide overlay: %#v", state)
		}

		postGSI(`{}`)
		waitProjection(3, false)
		if after := policyLogBytes(t, policyDir); after <= before {
			t.Fatalf("later produced record did not append policy commit: before=%d after=%d", before, after)
		}
		if state := overlay(); state.HealthCode == "gsi_projection_non_object" {
			t.Fatalf("later produced record did not clear rejection: %#v", state)
		}
		waiter.Wait()
		return appender.Close()
	}

	var output bytes.Buffer
	if code := runWithDependencies([]string{"--data-dir", root, "--session-id", sessionID, "--policy-lineage-file", lineagePath}, &output, deps); code != 0 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
}

func TestRunConfiguredProductRecoversPolicyAcrossRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	const sessionID = "binary-restart"
	lineagePath := filepath.Join(t.TempDir(), "lineage.v2.json")
	writeTestLineage(t, lineagePath, testLineage(sessionID))
	args := []string{"--data-dir", root, "--session-id", sessionID, "--policy-lineage-file", lineagePath}
	newDeps := func() runDependencies {
		deps := defaultRunDependencies()
		deps.newTokenFile = func(string) (string, string, func(), error) {
			return "test-only-operator-token", "", func() {}, nil
		}
		deps.listen = func(_ string, address string) (net.Listener, error) {
			return commandListener{address: commandAddress(address)}, nil
		}
		return deps
	}

	first := newDeps()
	first.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		product := server.(*pairedHTTPServer)
		gsiResponse := httptest.NewRecorder()
		product.capture.Handler.ServeHTTP(gsiResponse, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(`{"map":{"game_time":41}}`)))
		if gsiResponse.Code != http.StatusOK {
			t.Fatalf("first GSI status=%d body=%s", gsiResponse.Code, gsiResponse.Body.String())
		}
		command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "restart-hide", SessionID: sessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: 0, PolicyTimeMS: 10_000}
		body, _ := json.Marshal(command)
		request := httptest.NewRequest(http.MethodPost, "/v1/operator/commands", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-only-operator-token")
		request.Header.Set("Origin", "http://127.0.0.1:43211")
		request.Header.Set("Content-Type", delivery.JSONContentType)
		request.Header.Set(delivery.CSRFHeader, delivery.CSRFValue)
		response := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("first command status=%d body=%s", response.Code, response.Body.String())
		}
		waiter.Wait()
		return appender.Close()
	}
	var firstOutput bytes.Buffer
	if code := runWithDependencies(args, &firstOutput, first); code != 0 {
		t.Fatalf("first exit=%d output=%q", code, firstOutput.String())
	}
	policyBytesBeforeRestart := policyLogBytes(t, filepath.Join(root, sessionID))

	second := newDeps()
	second.runLifecycle = func(server lifecycle.Server, _ net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
		product := server.(*pairedHTTPServer)
		request := httptest.NewRequest(http.MethodGet, "/v1/operator/state", nil)
		request.Header.Set("Authorization", "Bearer test-only-operator-token")
		request.Header.Set("Origin", "http://127.0.0.1:43211")
		response := httptest.NewRecorder()
		product.delivery.Handler.ServeHTTP(response, request)
		var state delivery.OperatorState
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil || response.Code != http.StatusOK || state.PolicyRevision != 1 || !state.EmergencyHidden {
			t.Fatalf("recovered status=%d state=%#v decode=%v", response.Code, state, err)
		}
		waiter.Wait()
		if policyBytesAfterRestart := policyLogBytes(t, filepath.Join(root, sessionID)); policyBytesAfterRestart != policyBytesBeforeRestart {
			t.Fatalf("ordinary product restart duplicated policy log: before=%d after=%d", policyBytesBeforeRestart, policyBytesAfterRestart)
		}
		return appender.Close()
	}
	var secondOutput bytes.Buffer
	if code := runWithDependencies(args, &secondOutput, second); code != 0 {
		t.Fatalf("second exit=%d output=%q", code, secondOutput.String())
	}
}

func TestDeliveryServeFailureLeavesCapturePersistingGSI(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("delivery-failure"))
	if err != nil {
		t.Fatal(err)
	}
	captureListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deliveryListener := &failingDeliveryListener{
		address: commandAddress("127.0.0.1:43211"),
		failed:  make(chan struct{}),
	}
	deliveryFailures := make(chan string, 1)
	projection := &retryingProjection{failing: true}
	handler := gsi.NewServer(store,
		gsi.WithLiveProjections(projection),
		gsi.WithProjectionDrainTimeout(20*time.Millisecond),
		gsi.WithDiagnostics(gsi.DiagnosticConfig{
			BearerToken: "diagnostic-test-token", AllowedOrigin: "http://127.0.0.1:43210",
		}),
	)
	servers := &pairedHTTPServer{
		capture:               &http.Server{Handler: handler},
		delivery:              &http.Server{Handler: http.NotFoundHandler()},
		deliveryListener:      deliveryListener,
		reportDeliveryFailure: func(code string) { deliveryFailures <- code },
	}
	signals := make(chan os.Signal, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- lifecycle.Run(servers, captureListener, store, handler, signals, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), time.Second)
		})
	}()

	<-deliveryListener.failed
	if code := <-deliveryFailures; code != "delivery_serve_failed" {
		t.Fatalf("delivery failure code=%q", code)
	}
	select {
	case err := <-runDone:
		t.Fatalf("delivery failure terminated capture lifecycle: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	response, err := http.Post("http://"+captureListener.Addr().String()+"/gsi", "application/json", strings.NewReader(`{"map":{"game_time":42}}`))
	if err != nil {
		t.Fatalf("POST /gsi after delivery failure: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /gsi status=%d", response.StatusCode)
	}
	if data, err := os.ReadFile(store.RawPath()); err != nil || !rawV3Contains(data, `{"map":{"game_time":42}}`) {
		t.Fatalf("persisted raw=%q err=%v", data, err)
	}
	unauthorized, err := http.Get("http://" + captureListener.Addr().String() + "/api/latest")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unprotected diagnostic status=%d", unauthorized.StatusCode)
	}

	projection.setFailing(false)
	deadline := time.Now().Add(time.Second)
	for projection.count() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if projection.count() != 1 {
		t.Fatal("ordered projection did not retry after delivery failure")
	}

	projection.setFailing(true)
	response, err = http.Post("http://"+captureListener.Addr().String()+"/gsi", "application/json", strings.NewReader(`{"map":{"game_time":43}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second POST status=%d", response.StatusCode)
	}

	shutdownStarted := time.Now()
	signals <- os.Interrupt
	if err := <-runDone; err != nil {
		t.Fatalf("capture shutdown after isolated delivery failure: %v", err)
	}
	if elapsed := time.Since(shutdownStarted); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown with projector lag and unavailable delivery took %s", elapsed)
	}

	reopened, err := session.NewStore(root, session.WithSessionID("delivery-failure"))
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := &retryingProjection{}
	restarted := gsi.NewServer(reopened, gsi.WithLiveProjections(rebuilt))
	deadline = time.Now().Add(time.Second)
	for rebuilt.count() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if rebuilt.count() != 1 {
		t.Fatalf("valid cursor must resume strictly after cached prefix: count=%d want=1", rebuilt.count())
	}
	restarted.Wait()
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func rawV3Contains(data []byte, want string) bool {
	var frame struct {
		RawBase64 string `json:"raw_base64"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &frame); err != nil {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(frame.RawBase64)
	return err == nil && string(raw) == want
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
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
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

func TestDefaultTokenCollisionDoesNotRemoveAnotherInstanceToken(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	tokenDir := filepath.Join(runtimeDir, "dota2-ob", "runtime")
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(tokenDir, operatorTokenFilename)
	want := []byte("another-live-instance-token\n")
	if err := os.WriteFile(tokenPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := createEphemeralTokenFile(""); err == nil {
		t.Fatal("default token collision unexpectedly succeeded")
	}
	got, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("existing token was removed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("existing token changed: got=%q want=%q", got, want)
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
