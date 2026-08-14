package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const m4FixtureSHA256 = "2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e"
const m4Token = "m4-production-composition-token"

type m4Schedule struct {
	SchemaVersion string `json:"schema_version"`
	SessionID     string `json:"session_id"`
	Updates       []struct {
		ReceivedAt string          `json:"received_at"`
		PolicyTime int64           `json:"policy_time_ms"`
		Body       json.RawMessage `json:"body"`
	} `json:"updates"`
}

type m4ProductionOutput struct {
	FixtureSHA256 string                            `json:"fixture_sha256"`
	RawSHA256     []string                          `json:"raw_sha256"`
	CommitSHA256  []string                          `json:"commit_sha256"`
	Operator      delivery.OperatorState            `json:"operator_state"`
	Visible       contracts.OverlayStateV1          `json:"visible"`
	Saturated     contracts.OverlayStateV1          `json:"saturated"`
	Recovered     contracts.OverlayStateV1          `json:"recovered"`
	Command       contracts.OperatorCommandResultV1 `json:"command"`
}

type lockedClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *lockedClock) Set(value time.Time) { c.mu.Lock(); c.value = value; c.mu.Unlock() }
func (c *lockedClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.value }

func TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts(t *testing.T) {
	fixture := readM4Schedule(t)
	first := runM4Production(t, fixture)
	second := runM4Production(t, fixture)
	a := mustCanonicalTest(t, first)
	b := mustCanonicalTest(t, second)
	if !bytes.Equal(a, b) {
		t.Fatal("two clean production-composition executions differ")
	}
	goldenPath := filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "replay_output.golden.json")
	if os.Getenv("UPDATE_M4_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, a, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if golden, err := os.ReadFile(goldenPath); err != nil || !bytes.Equal(golden, a) {
		t.Fatalf("production replay golden mismatch: %v", err)
	}
	if first.Visible.Visibility != "visible" || first.Saturated.Visibility != "hidden" || first.Saturated.HealthCode != "candidate_queue_saturated" || first.Recovered.Visibility != "hidden" || first.Recovered.HealthCode != "candidate_queue_saturated" || first.Command.Status != contracts.CommandAccepted {
		t.Fatalf("production publish/recovery mismatch: %#v", first)
	}
}

func readM4Schedule(t *testing.T) m4Schedule {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "integration", "m4", "testdata", "captured_gsi_schedule.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != m4FixtureSHA256 {
		t.Fatalf("fixture identity changed: %x", sum)
	}
	var value m4Schedule
	if err := json.Unmarshal(payload, &value); err != nil || len(value.Updates) != 4 {
		t.Fatalf("invalid fixture: %v", err)
	}
	return value
}

func runM4Production(t *testing.T, fixture m4Schedule) m4ProductionOutput {
	t.Helper()
	root := t.TempDir()
	artifacts := testLiveOnlyArtifacts(fixture.SessionID)
	artifactDir := t.TempDir()
	history := writeCanonicalTestContract(t, artifactDir, "history.json", artifacts.History)
	lineage := writeCanonicalTestContract(t, artifactDir, "lineage.json", artifacts.Lineage)
	release := writeCanonicalTestContract(t, artifactDir, "release.json", artifacts.Release)
	clock := &lockedClock{value: time.UnixMilli(fixture.Updates[0].PolicyTime).UTC()}
	var active *broadcastRuntimeV3
	var output m4ProductionOutput
	args := []string{"--addr", "127.0.0.1:43210", "--delivery-addr", "127.0.0.1:43211", "--data-dir", root, "--session-id", fixture.SessionID, "--policy-mode", "v3-live-only", "--history-binding-file", history, "--live-only-lineage-file", lineage, "--live-only-release-file", release}

	makeDeps := func(restart bool) runDependencies {
		deps := defaultRunDependencies()
		deps.now = clock.Now
		deps.newStore = func(dir, id string) (*session.Store, error) {
			return session.NewStore(dir, session.WithSessionID(id), session.WithClock(clock.Now))
		}
		deps.newTokenFile = func(string) (string, string, func(), error) { return m4Token, "", func() {}, nil }
		deps.listen = func(_, _ string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
		deps.newBroadcastV3 = func(config broadcastConfigV3) (*broadcastRuntimeV3, error) {
			policyConfig := policy.DefaultConfig()
			policyConfig.CooldownMS = 1
			config.PolicyConfig = &policyConfig
			r, err := newBroadcastRuntimeV3(config)
			active = r
			return r, err
		}
		deps.runDoctor = func(preflight.DoctorConfig) preflight.Result { return preflight.Result{} }
		deps.runLifecycle = func(server lifecycle.Server, listener net.Listener, _ lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, _ lifecycle.ContextFactory) error {
			defer listener.Close()
			paired := server.(*pairedHTTPServer)
			if restart {
				waitRestoreComplete(t, active)
				output.Recovered = getM4Overlay(t, paired.delivery.Handler)
				waiter.Wait()
				return nil
			}
			for i, update := range fixture.Updates {
				received, err := time.Parse(time.RFC3339Nano, update.ReceivedAt)
				if err != nil {
					t.Fatal(err)
				}
				clock.Set(received)
				response := httptest.NewRecorder()
				paired.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(update.Body)))
				if response.Code != http.StatusOK {
					t.Fatalf("gsi[%d]=%d %s", i, response.Code, response.Body.String())
				}
				clock.Set(time.UnixMilli(update.PolicyTime).UTC())
				waitObservation(t, active, uint64(i+1))
			}
			operator := getM4Operator(t, paired.delivery.Handler)
			if len(operator.Previews) == 0 {
				t.Fatal("production path produced no preview")
			}
			preview := operator.Previews[0]
			for _, candidate := range operator.Previews[1:] {
				if candidate.ExpiresAtMS > preview.ExpiresAtMS {
					preview = candidate
				}
			}
			command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "m4-approve", SessionID: fixture.SessionID, Action: contracts.ActionApprove, TargetCandidateID: preview.CandidateID, ExpectedPolicyRevision: operator.PolicyRevision, PolicyTimeMS: clock.Now().UnixMilli() + 1}
			output.Command = postM4Command(t, paired.delivery.Handler, command)
			clock.Set(time.UnixMilli(command.PolicyTimeMS).UTC())
			output.Visible = getM4Overlay(t, paired.delivery.Handler)
			output.Operator = getM4Operator(t, paired.delivery.Handler)
			malformed := httptest.NewRecorder()
			paired.capture.Handler.ServeHTTP(malformed, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader("{")))
			if malformed.Code != http.StatusBadRequest {
				t.Fatalf("malformed gsi=%d %s", malformed.Code, malformed.Body.String())
			}
			oversized := httptest.NewRecorder()
			paired.capture.Handler.ServeHTTP(oversized, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader(strings.Repeat("x", 11<<20))))
			if oversized.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized gsi=%d %s", oversized.Code, oversized.Body.String())
			}
			clock.Set(clock.Now().Add(time.Millisecond))
			nonObject := httptest.NewRecorder()
			paired.capture.Handler.ServeHTTP(nonObject, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader("null")))
			if nonObject.Code != http.StatusOK {
				t.Fatalf("non-object gsi=%d %s", nonObject.Code, nonObject.Body.String())
			}
			waitProjectionRejection(t, active, "gsi_projection_non_object")
			if state := getM4Overlay(t, paired.delivery.Handler); state.Visibility != "hidden" || state.HealthCode != "gsi_projection_non_object" {
				t.Fatalf("non-object overlay=%#v", state)
			}
			var boundsBody map[string]any
			if err := json.Unmarshal(fixture.Updates[2].Body, &boundsBody); err != nil {
				t.Fatal(err)
			}
			boundsPlayers := boundsBody["player"].(map[string]any)["team2"].(map[string]any)
			for i := 10; i < 75; i++ {
				boundsPlayers[fmt.Sprintf("player%d", i)] = map[string]any{"player_slot": i, "net_worth": i}
			}
			boundsPayload, err := json.Marshal(boundsBody)
			if err != nil {
				t.Fatal(err)
			}
			clock.Set(clock.Now().Add(time.Millisecond))
			bounds := httptest.NewRecorder()
			paired.capture.Handler.ServeHTTP(bounds, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(boundsPayload)))
			if bounds.Code != http.StatusOK {
				t.Fatalf("bounds gsi=%d %s", bounds.Code, bounds.Body.String())
			}
			waitProjectionRejection(t, active, "gsi_projection_bounds_exceeded")
			if state := getM4Overlay(t, paired.delivery.Handler); state.Visibility != "hidden" || state.HealthCode != "gsi_projection_bounds_exceeded" {
				t.Fatalf("bounds overlay=%#v", state)
			}
			var saturationBody map[string]any
			if err := json.Unmarshal(fixture.Updates[2].Body, &saturationBody); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 65; i++ {
				players := saturationBody["player"].(map[string]any)["team2"].(map[string]any)
				players["player0"].(map[string]any)["net_worth"] = float64(10_000 + i*i)
				payload, err := json.Marshal(saturationBody)
				if err != nil {
					t.Fatal(err)
				}
				clock.Set(clock.Now().Add(time.Millisecond))
				response := httptest.NewRecorder()
				paired.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(payload)))
				if response.Code != http.StatusOK {
					t.Fatalf("saturation gsi[%d]=%d %s", i, response.Code, response.Body.String())
				}
				waitObservation(t, active, uint64(len(fixture.Updates)+2+i+1))
			}
			active.mu.Lock()
			saturated := active.candidateSaturated
			active.mu.Unlock()
			if !saturated {
				t.Fatal("production composition did not latch candidate saturation")
			}
			output.Saturated = getM4Overlay(t, paired.delivery.Handler)
			output.FixtureSHA256 = m4FixtureSHA256
			if err := active.store.VisitAll(func(value commitlog.CommittedV3) error {
				output.CommitSHA256 = append(output.CommitSHA256, value.Hash)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rawCount := 0
			err = session.StreamRecords(filepath.Join(root, fixture.SessionID, "raw.jsonl"), fixture.SessionID, func(record *session.Record) error {
				sum := sha256.Sum256(record.Raw)
				output.RawSHA256 = append(output.RawSHA256, hex.EncodeToString(sum[:]))
				rawCount++
				return nil
			})
			if err != nil || rawCount != len(fixture.Updates)+2+65 {
				t.Fatalf("raw replay count=%d err=%v", rawCount, err)
			}
			waiter.Wait()
			return nil
		}
		return deps
	}
	var logs bytes.Buffer
	if code := runWithDependencies(args, &logs, makeDeps(false)); code != 0 {
		t.Fatalf("first production run=%d logs=%s", code, logs.String())
	}
	logs.Reset()
	if code := runWithDependencies(args, &logs, makeDeps(true)); code != 0 {
		t.Fatalf("restart production run=%d logs=%s", code, logs.String())
	}
	return output
}

func waitProjectionRejection(t *testing.T, runtimeV3 *broadcastRuntimeV3, code string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtimeV3 != nil {
			runtimeV3.mu.Lock()
			active, health := runtimeV3.projectionRejected, runtimeV3.projectionHealthCode
			runtimeV3.mu.Unlock()
			if active && health == code {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("projection rejection did not reach %s", code)
}

func waitRestoreComplete(t *testing.T, runtimeV3 *broadcastRuntimeV3) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtimeV3 != nil {
			runtimeV3.mu.Lock()
			restoring := runtimeV3.restoringProjection
			runtimeV3.mu.Unlock()
			if !restoring {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("production projection restore did not complete")
}

func waitObservation(t *testing.T, runtimeV3 *broadcastRuntimeV3, sequence uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtimeV3 != nil {
			runtimeV3.mu.Lock()
			observed := runtimeV3.app.State().LastObservationSequence
			runtimeV3.mu.Unlock()
			if observed >= sequence {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("policy projection did not reach observation %d", sequence)
}

func postM4Command(t *testing.T, handler http.Handler, command contracts.OperatorCommandV1) contracts.OperatorCommandResultV1 {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/operator/commands", bytes.NewReader(mustCanonicalTest(t, command)))
	request.Header.Set("Authorization", "Bearer "+m4Token)
	request.Header.Set("Origin", "http://127.0.0.1:43211")
	request.Header.Set("Content-Type", delivery.JSONContentType)
	request.Header.Set(delivery.CSRFHeader, delivery.CSRFValue)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("command=%d %s", response.Code, response.Body.String())
	}
	var result contracts.OperatorCommandResultV1
	if err := contracts.DecodeStrict(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func getM4Operator(t *testing.T, handler http.Handler) delivery.OperatorState {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/operator/state", nil)
	request.Header.Set("Authorization", "Bearer "+m4Token)
	request.Header.Set("Origin", "http://127.0.0.1:43211")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("operator=%d %s", response.Code, response.Body.String())
	}
	var value delivery.OperatorState
	if err := contracts.DecodeStrict(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func getM4Overlay(t *testing.T, handler http.Handler) contracts.OverlayStateV1 {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("overlay=%d %s", response.Code, response.Body.String())
	}
	var value contracts.OverlayStateV1
	if err := contracts.DecodeStrict(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
