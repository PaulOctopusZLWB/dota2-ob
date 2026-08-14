package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
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
	FixtureSHA256   string                              `json:"fixture_sha256"`
	ExternalDelay   string                              `json:"dotatv_delay_external"`
	ClockOrder      []m4ClockEvidence                   `json:"clock_order"`
	RawSHA256       []string                            `json:"raw_sha256"`
	ObservationSHA  []string                            `json:"observation_sha256"`
	Candidates      [][]contracts.InsightCandidateV1    `json:"candidates"`
	Commits         []contracts.PolicyCommitV3          `json:"terminal_commits"`
	OperatorStates  []delivery.OperatorState            `json:"operator_states"`
	OverlayStates   []contracts.OverlayStateV1          `json:"overlay_states"`
	Commands        []contracts.OperatorCommandResultV1 `json:"command_results"`
	ReplayBaselines []m4ReplayBaseline                  `json:"replay_baselines"`
	Visible         contracts.OverlayStateV1            `json:"visible"`
	Saturated       contracts.OverlayStateV1            `json:"saturated"`
	Recovered       contracts.OverlayStateV1            `json:"recovered"`
	Command         contracts.OperatorCommandResultV1   `json:"command"`
	LifecycleClose  int                                 `json:"lifecycle_close_count"`
	HighWaterOne    bool                                `json:"high_water_capacity_one"`
	FaultStates     []contracts.OverlayStateV1          `json:"fault_overlay_states"`
	GatewayFailures []string                            `json:"gateway_failures"`
	Boundaries      []m4BoundaryEvidence                `json:"boundaries"`
}

type m4BoundaryEvidence struct {
	Name                string `json:"name"`
	RawSequence         uint64 `json:"raw_sequence"`
	CanonicalBytes      int    `json:"canonical_bytes"`
	RawAccepted         bool   `json:"raw_accepted"`
	PolicySequence      uint64 `json:"policy_sequence"`
	CandidateBytes      int    `json:"candidate_bytes,omitempty"`
	OverlayBytes        int    `json:"overlay_bytes,omitempty"`
	Visibility          string `json:"visibility"`
	HealthCode          string `json:"health_code"`
	GatewayOutcome      string `json:"gateway_outcome"`
	OperatorRevision    uint64 `json:"operator_revision"`
	NoTransientReappear bool   `json:"no_transient_reappearance"`
}

type m4ReplayBaseline struct {
	Restart                int    `json:"restart"`
	RecoveredLastSequence  uint64 `json:"recovered_last_sequence"`
	RecoveredPrevious      uint64 `json:"recovered_previous_sequence"`
	PostReplayLastSequence uint64 `json:"post_replay_last_sequence"`
	PostReplayPrevious     uint64 `json:"post_replay_previous_sequence"`
}

type m4ClockEvidence struct {
	SourceProviderMS int64 `json:"source_provider_ms"`
	ReceiptMS        int64 `json:"receipt_ms"`
	GameMS           int64 `json:"game_ms"`
	PolicyMS         int64 `json:"policy_ms"`
	DisplayMS        int64 `json:"display_ms"`
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

func TestM4ProductionCompositionFailsClosedForMissingAndSubstitutedBinding(t *testing.T) {
	fixture := readM4Schedule(t)
	for _, scenario := range []string{"missing", "substituted"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			artifacts := testLiveOnlyArtifacts(fixture.SessionID)
			artifactDir := t.TempDir()
			history := writeCanonicalTestContract(t, artifactDir, "history.json", artifacts.History)
			lineage := writeCanonicalTestContract(t, artifactDir, "lineage.json", artifacts.Lineage)
			release := writeCanonicalTestContract(t, artifactDir, "release.json", artifacts.Release)
			if scenario == "missing" {
				history = filepath.Join(artifactDir, "missing.json")
			} else {
				artifacts.History.SourceProvenanceSHA256 = strings.Repeat("a", 64)
				history = writeCanonicalTestContract(t, artifactDir, "substituted.json", artifacts.History)
			}
			deps := defaultRunDependencies()
			deps.newTokenFile = func(string) (string, string, func(), error) { return m4Token, "", func() {}, nil }
			deps.listen = func(_, _ string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
			deps.runLifecycle = func(server lifecycle.Server, listener net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, factory lifecycle.ContextFactory) error {
				paired := server.(*pairedHTTPServer)
				state := getM4Overlay(t, paired.delivery.Handler)
				if state.Visibility != "hidden" || state.HealthCode != "policy_unconfigured" {
					t.Fatalf("artifact fault did not fail closed: %#v", state)
				}
				raw := httptest.NewRecorder()
				paired.capture.Handler.ServeHTTP(raw, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(fixture.Updates[0].Body)))
				if raw.Code != http.StatusOK {
					t.Fatalf("artifact fault blocked healthy raw capture: %d %s", raw.Code, raw.Body.String())
				}
				shutdown := make(chan os.Signal, 1)
				shutdown <- os.Interrupt
				return lifecycle.Run(server, listener, appender, waiter, shutdown, factory)
			}
			args := []string{"--addr", "127.0.0.1:43210", "--delivery-addr", "127.0.0.1:43211", "--data-dir", root, "--session-id", fixture.SessionID, "--policy-mode", "v3-live-only", "--history-binding-file", history, "--live-only-lineage-file", lineage, "--live-only-release-file", release}
			var logs bytes.Buffer
			if code := runWithDependencies(args, &logs, deps); code != 0 || !strings.Contains(logs.String(), "broadcast_policy_config_failed") {
				t.Fatalf("artifact fault startup=%d logs=%s", code, logs.String())
			}
		})
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
	receiptClock := &lockedClock{value: time.UnixMilli(fixture.Updates[0].PolicyTime).UTC()}
	policyClock := &lockedClock{value: time.UnixMilli(fixture.Updates[0].PolicyTime).UTC()}
	displayClock := &lockedClock{value: time.UnixMilli(fixture.Updates[0].PolicyTime + 100).UTC()}
	gatewayClock := &lockedClock{value: displayClock.Now()}
	var active *broadcastRuntimeV3
	var activeRaw *session.Store
	var faultMu sync.Mutex
	presentationFailures := 0
	auditFailure := false
	candidateBoundaryBytes := 0
	overlayBoundaryBytes := 0
	observationBoundaryBytes := 0
	lastCandidateBytes := 0
	lastOverlayBytes := 0
	lastObservationBytes := 0
	observationTargets := make(map[uint64]int)
	candidateTargets := make(map[uint64]int)
	var output m4ProductionOutput
	args := []string{"--addr", "127.0.0.1:43210", "--delivery-addr", "127.0.0.1:43211", "--data-dir", root, "--session-id", fixture.SessionID, "--policy-mode", "v3-live-only", "--history-binding-file", history, "--live-only-lineage-file", lineage, "--live-only-release-file", release}

	makeDeps := func(restart int) runDependencies {
		deps := defaultRunDependencies()
		deps.now = receiptClock.Now
		deps.policyNow = policyClock.Now
		deps.displayNow = displayClock.Now
		deps.gatewayNow = gatewayClock.Now
		deps.newStore = func(dir, id string) (*session.Store, error) {
			store, err := session.NewStore(dir, session.WithSessionID(id), session.WithClock(receiptClock.Now))
			activeRaw = store
			return store, err
		}
		deps.newTokenFile = func(string) (string, string, func(), error) { return m4Token, "", func() {}, nil }
		deps.listen = func(_, _ string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
		deps.mapPolicyObservation = func(record *session.Record) (contracts.LiveObservationV1, error) {
			observation, err := capture.MapLiveObservationV1(record)
			faultMu.Lock()
			defer faultMu.Unlock()
			target := observationTargets[record.Sequence]
			if target == 0 && observationBoundaryBytes != 0 {
				target = observationBoundaryBytes
				observationTargets[record.Sequence] = target
			}
			if err != nil || target == 0 {
				return observation, err
			}
			observation = exactM4Observation(t, observation, target)
			lastObservationBytes = len(mustCanonicalTest(t, observation))
			return observation, nil
		}
		deps.newBroadcastV3 = func(config broadcastConfigV3) (*broadcastRuntimeV3, error) {
			policyConfig := policy.DefaultConfig()
			policyConfig.CooldownMS = 1
			config.PolicyConfig = &policyConfig
			config.BuildOverlay = func(input presentation.BuildInput) (contracts.OverlayStateV1, error) {
				faultMu.Lock()
				defer faultMu.Unlock()
				if presentationFailures > 0 {
					presentationFailures--
					return contracts.OverlayStateV1{}, errors.New("injected production presentation failure")
				}
				state, err := presentation.Build(input)
				if err != nil || overlayBoundaryBytes == 0 {
					return state, err
				}
				state = exactM4OverlayState(t, state, overlayBoundaryBytes)
				lastOverlayBytes = len(mustCanonicalTest(t, state))
				return state, nil
			}
			config.EvaluateLiveOnly = func(input insight.LiveOnlyInput, cfg insight.Config) []contracts.InsightCandidateV1 {
				candidates := insight.EvaluateLiveOnly(input, cfg)
				faultMu.Lock()
				defer faultMu.Unlock()
				target := candidateTargets[input.Observation.Evidence.Sequence]
				if target == 0 && candidateBoundaryBytes != 0 {
					target = candidateBoundaryBytes
					candidateTargets[input.Observation.Evidence.Sequence] = target
				}
				if target == 0 || len(candidates) == 0 {
					return candidates
				}
				candidates[0] = exactM4Candidate(t, candidates[0], target)
				lastCandidateBytes = len(mustCanonicalTest(t, candidates[0]))
				return candidates
			}
			config.StoreOptions = append(config.StoreOptions, commitlog.WithV3Hooks(commitlog.Hooks{SyncFile: func(file *os.File) error {
				faultMu.Lock()
				defer faultMu.Unlock()
				if auditFailure {
					return errors.New("injected production audit sync failure")
				}
				return file.Sync()
			}}))
			r, err := newBroadcastRuntimeV3(config)
			if err != nil {
				t.Fatalf("production V3 startup failed: %v", err)
			}
			active = r
			return r, err
		}
		deps.runDoctor = func(preflight.DoctorConfig) preflight.Result { return preflight.Result{} }
		deps.runLifecycle = func(server lifecycle.Server, listener net.Listener, appender lifecycle.Closer, waiter lifecycle.Waiter, _ <-chan os.Signal, contextFactory lifecycle.ContextFactory) error {
			paired := server.(*pairedHTTPServer)
			assertM4BrowserAndOBSRestart(t, paired.delivery.Handler)
			if restart > 0 {
				recoveredLast, recoveredPrevious := m4RuntimeBaseline(active)
				waitRestoreComplete(t, active)
				postLast, postPrevious := m4RuntimeBaseline(active)
				if recoveredLast != postLast || recoveredPrevious != postPrevious || postLast != postPrevious {
					t.Fatalf("restart %d rewound equality/older replay baseline: recovered=%d/%d replayed=%d/%d", restart, recoveredLast, recoveredPrevious, postLast, postPrevious)
				}
				output.ReplayBaselines = append(output.ReplayBaselines, m4ReplayBaseline{Restart: restart, RecoveredLastSequence: recoveredLast, RecoveredPrevious: recoveredPrevious, PostReplayLastSequence: postLast, PostReplayPrevious: postPrevious})
				if restart == 1 {
					postM4NextNewerBoundary(t, paired.capture.Handler, active, fixture, receiptClock, policyClock)
				}
				output.Recovered = getM4Overlay(t, paired.delivery.Handler)
				output.OverlayStates = append(output.OverlayStates, output.Recovered)
				if restart == 2 {
					faultMu.Lock()
					auditFailure = true
					faultMu.Unlock()
					postM4AuditFailure(t, paired.capture.Handler, paired.delivery.Handler, fixture.Updates[3].Body, receiptClock, policyClock, &output)
					collectM4OrderedEvidence(t, active, activeRaw, fixture, root, &output)
				}
			} else {
				highWater, unsubscribe := activeRaw.HighWater().Subscribe()
				defer unsubscribe()
				if cap(highWater) != 1 {
					t.Fatalf("production high-water capacity=%d want 1", cap(highWater))
				}
				output.HighWaterOne = true
				for i, update := range fixture.Updates {
					received, err := time.Parse(time.RFC3339Nano, update.ReceivedAt)
					if err != nil {
						t.Fatal(err)
					}
					receiptClock.Set(received)
					policyClock.Set(time.UnixMilli(update.PolicyTime).UTC())
					displayClock.Set(time.UnixMilli(fixture.Updates[0].PolicyTime + 100 + int64(i)).UTC())
					gatewayClock.Set(displayClock.Now())
					response := httptest.NewRecorder()
					paired.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(update.Body)))
					if response.Code != http.StatusOK {
						t.Fatalf("gsi[%d]=%d %s", i, response.Code, response.Body.String())
					}
					waitObservation(t, active, uint64(i+1))
					output.ClockOrder = append(output.ClockOrder, m4ClockFromBody(t, update.Body, received.UnixMilli(), update.PolicyTime, displayClock.Now().UnixMilli()))
					output.OperatorStates = append(output.OperatorStates, getM4Operator(t, paired.delivery.Handler))
					output.OverlayStates = append(output.OverlayStates, getM4Overlay(t, paired.delivery.Handler))
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
				command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "m4-approve", SessionID: fixture.SessionID, Action: contracts.ActionApprove, TargetCandidateID: preview.CandidateID, ExpectedPolicyRevision: operator.PolicyRevision, PolicyTimeMS: policyClock.Now().UnixMilli() + 1}
				output.Command = postM4Command(t, paired.delivery.Handler, command)
				duplicate := postM4Command(t, paired.delivery.Handler, command)
				if !bytes.Equal(mustCanonicalTest(t, duplicate), mustCanonicalTest(t, output.Command)) {
					t.Fatalf("durable duplicate changed: got %#v want %#v", duplicate, output.Command)
				}
				conflict := command
				conflict.CommandID = "m4-revision-conflict"
				conflictResult := postM4Command(t, paired.delivery.Handler, conflict)
				if conflictResult.Status != contracts.CommandRejected || conflictResult.Reason != "stale_revision" {
					t.Fatalf("revision conflict=%#v", conflictResult)
				}
				output.Commands = append(output.Commands, output.Command, duplicate, conflictResult)
				displayClock.Set(time.UnixMilli(command.PolicyTimeMS + 1).UTC())
				gatewayClock.Set(displayClock.Now())
				output.Visible = getM4Overlay(t, paired.delivery.Handler)
				output.OverlayStates = append(output.OverlayStates, output.Visible)
				output.OperatorStates = append(output.OperatorStates, getM4Operator(t, paired.delivery.Handler))
				exerciseM4ProductionBoundaries(t, paired, active, activeRaw, fixture, receiptClock, policyClock, displayClock, gatewayClock, &faultMu, &observationBoundaryBytes, &candidateBoundaryBytes, &overlayBoundaryBytes, &lastObservationBytes, &lastCandidateBytes, &lastOverlayBytes, &output)
				faultMu.Lock()
				presentationFailures = 1
				faultMu.Unlock()
				exerciseM4ProjectionAndSaturation(t, paired, active, activeRaw, fixture, root, receiptClock, policyClock, &output)
			}
			shutdown := make(chan os.Signal, 1)
			shutdown <- os.Interrupt
			if err := lifecycle.Run(server, listener, appender, waiter, shutdown, contextFactory); err != nil {
				return err
			}
			output.LifecycleClose++
			return nil
		}
		return deps
	}
	var logs bytes.Buffer
	if code := runWithDependencies(args, &logs, makeDeps(0)); code != 0 {
		t.Fatalf("first production run=%d logs=%s", code, logs.String())
	}
	sessionDir := filepath.Join(root, fixture.SessionID)
	appendM4CrashTail(t, filepath.Join(sessionDir, "raw.jsonl"), `{"partial_raw_tail":`)
	segments, err := filepath.Glob(filepath.Join(sessionDir, "*.pcl3"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("production policy segments missing: %v", err)
	}
	appendM4CrashTail(t, segments[len(segments)-1], "\x01")
	if err := os.WriteFile(filepath.Join(sessionDir, "broadcast_policy_projection_cursor.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(sessionDir, "checkpoint.v3.json"))
	logs.Reset()
	if code := runWithDependencies(args, &logs, makeDeps(1)); code != 0 {
		t.Fatalf("restart production run=%d logs=%s", code, logs.String())
	}
	if err := os.Remove(filepath.Join(sessionDir, "broadcast_policy_projection_cursor.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "checkpoint.v3.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if code := runWithDependencies(args, &logs, makeDeps(2)); code != 0 {
		t.Fatalf("second restart production run=%d logs=%s", code, logs.String())
	}
	output.ExternalDelay = "explicit_external_not_inferred"
	return output
}

func appendM4CrashTail(t *testing.T, path, tail string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(tail); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertM4BrowserAndOBSRestart(t *testing.T, handler http.Handler) {
	t.Helper()
	for _, path := range []string{"/operator/", "/overlay/"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("production browser/OBS asset %s=%d bytes=%d", path, response.Code, response.Body.Len())
		}
	}
}

func m4RuntimeBaseline(runtimeV3 *broadcastRuntimeV3) (last, previous uint64) {
	runtimeV3.mu.Lock()
	defer runtimeV3.mu.Unlock()
	last = runtimeV3.app.State().LastObservationSequence
	if runtimeV3.previous != nil {
		previous = runtimeV3.previous.Evidence.Sequence
	}
	return last, previous
}

func postM4NextNewerBoundary(t *testing.T, handler http.Handler, active *broadcastRuntimeV3, fixture m4Schedule, receiptClock, policyClock *lockedClock) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(fixture.Updates[3].Body, &body); err != nil {
		t.Fatal(err)
	}
	body["provider"].(map[string]any)["timestamp"] = float64(fixture.Updates[0].PolicyTime/1000 - 30)
	body["map"].(map[string]any)["clock_time"] = float64(599)
	body["map"].(map[string]any)["game_time"] = float64(699)
	body["player"].(map[string]any)["team2"].(map[string]any)["player0"].(map[string]any)["net_worth"] = float64(20_000)
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	receiptClock.Set(receiptClock.Now().Add(time.Second))
	policyClock.Set(policyClock.Now().Add(time.Second))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("next-newer delayed/older boundary=%d %s", response.Code, response.Body.String())
	}
	lastBefore, _ := m4RuntimeBaseline(active)
	expected := lastBefore + 1
	waitObservation(t, active, expected)
	last, previous := m4RuntimeBaseline(active)
	if last != expected || previous != expected {
		t.Fatalf("next-newer observation not accepted after replay: last=%d previous=%d", last, previous)
	}
}

func exerciseM4ProductionBoundaries(t *testing.T, paired *pairedHTTPServer, active *broadcastRuntimeV3, raw *session.Store, fixture m4Schedule, receiptClock, policyClock, displayClock, gatewayClock *lockedClock, faultMu *sync.Mutex, observationBoundaryBytes, candidateBoundaryBytes, overlayBoundaryBytes, lastObservationBytes, lastCandidateBytes, lastOverlayBytes *int, output *m4ProductionOutput) {
	t.Helper()

	faultMu.Lock()
	*observationBoundaryBytes = contracts.MaxLiveObservationBytes
	faultMu.Unlock()
	receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	displayClock.Set(displayClock.Now().Add(3 * time.Millisecond))
	gatewayClock.Set(displayClock.Now())
	exactSequence := raw.HighWater().Current().Sequence + 1
	postM4Raw(t, paired.capture.Handler, fixture.Updates[3].Body)
	waitObservation(t, active, exactSequence)
	faultMu.Lock()
	exactBytes := *lastObservationBytes
	faultMu.Unlock()
	if exactBytes != contracts.MaxLiveObservationBytes {
		t.Fatalf("exact observation request/response bytes=%d", exactBytes)
	}
	exactOperator := getM4Operator(t, paired.delivery.Handler)
	exactOverlay := getM4Overlay(t, paired.delivery.Handler)
	output.OperatorStates = append(output.OperatorStates, exactOperator)
	output.OverlayStates = append(output.OverlayStates, exactOverlay)
	output.Boundaries = append(output.Boundaries, m4BoundaryEvidence{Name: "live_observation_exact_1_mib", RawSequence: exactSequence, CanonicalBytes: exactBytes, RawAccepted: true, PolicySequence: exactSequence, Visibility: exactOverlay.Visibility, HealthCode: exactOverlay.HealthCode, GatewayOutcome: "accepted", OperatorRevision: exactOperator.PolicyRevision})

	faultMu.Lock()
	*observationBoundaryBytes = contracts.MaxLiveObservationBytes + 1
	faultMu.Unlock()
	overSequence := raw.HighWater().Current().Sequence + 1
	receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	displayClock.Set(displayClock.Now().Add(3 * time.Millisecond))
	gatewayClock.Set(displayClock.Now())
	postM4Raw(t, paired.capture.Handler, fixture.Updates[3].Body)
	waitObservationHealth(t, paired.delivery.Handler, "observation_contract_exceeded")
	faultMu.Lock()
	overBytes := *lastObservationBytes
	*observationBoundaryBytes = 0
	faultMu.Unlock()
	overOverlay := getM4Overlay(t, paired.delivery.Handler)
	overOperator := getM4Operator(t, paired.delivery.Handler)
	if last, _ := m4RuntimeBaseline(active); overBytes != contracts.MaxLiveObservationBytes+1 || last != exactSequence || overOverlay.Visibility != "hidden" || overOverlay.HealthCode != "observation_contract_exceeded" {
		t.Fatalf("over-limit observation was not fail-closed: last=%d overlay=%#v", last, overOverlay)
	}
	output.OperatorStates = append(output.OperatorStates, overOperator)
	output.OverlayStates = append(output.OverlayStates, overOverlay)
	output.Boundaries = append(output.Boundaries, m4BoundaryEvidence{Name: "live_observation_over_1_mib", RawSequence: overSequence, CanonicalBytes: overBytes, RawAccepted: true, PolicySequence: exactSequence, Visibility: overOverlay.Visibility, HealthCode: overOverlay.HealthCode, GatewayOutcome: "suppressed", OperatorRevision: overOperator.PolicyRevision})

	boundaryBody := m4NetWorthBody(t, fixture.Updates[2].Body, 31_000)
	faultMu.Lock()
	*candidateBoundaryBytes = contracts.MaxInsightCandidateBytes
	*overlayBoundaryBytes = contracts.MaxOverlayBytes
	faultMu.Unlock()
	receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	displayClock.Set(displayClock.Now().Add(3 * time.Millisecond))
	gatewayClock.Set(displayClock.Now())
	exactCandidateSequence := raw.HighWater().Current().Sequence + 1
	postM4Raw(t, paired.capture.Handler, boundaryBody)
	waitObservation(t, active, exactCandidateSequence)
	exactCandidateCommit := m4ObservationCommit(t, active, exactCandidateSequence)
	if len(exactCandidateCommit.AuditEvents) == 0 || exactCandidateCommit.AuditEvents[0].EventType != "candidate_queued" || exactCandidateCommit.AuditEvents[0].Reason != "approval_required" {
		t.Fatalf("exact candidate was not admitted through production policy: %#v", exactCandidateCommit.AuditEvents)
	}
	exactBoundaryOverlay, exactOverlayBody := getM4OverlayBody(t, paired.delivery.Handler)
	exactBoundaryOperator := getM4Operator(t, paired.delivery.Handler)
	faultMu.Lock()
	exactCandidateBytes, exactOverlayBytes := *lastCandidateBytes, *lastOverlayBytes
	faultMu.Unlock()
	if exactCandidateBytes != contracts.MaxInsightCandidateBytes || exactOverlayBytes != contracts.MaxOverlayBytes || len(exactOverlayBody) != contracts.MaxOverlayBytes {
		t.Fatalf("exact 64 KiB production boundary candidate=%d overlay=%d response=%d", exactCandidateBytes, exactOverlayBytes, len(exactOverlayBody))
	}
	output.OperatorStates = append(output.OperatorStates, exactBoundaryOperator)
	output.OverlayStates = append(output.OverlayStates, exactBoundaryOverlay)
	output.Boundaries = append(output.Boundaries, m4BoundaryEvidence{Name: "candidate_overlay_exact_64_kib", RawSequence: exactCandidateSequence, CanonicalBytes: exactOverlayBytes, RawAccepted: true, PolicySequence: exactCandidateSequence, CandidateBytes: exactCandidateBytes, OverlayBytes: exactOverlayBytes, Visibility: exactBoundaryOverlay.Visibility, HealthCode: exactBoundaryOverlay.HealthCode, GatewayOutcome: "accepted", OperatorRevision: exactBoundaryOperator.PolicyRevision})

	faultMu.Lock()
	*candidateBoundaryBytes = contracts.MaxInsightCandidateBytes + 1
	*overlayBoundaryBytes = contracts.MaxOverlayBytes + 1
	faultMu.Unlock()
	unsafeBody := m4NetWorthBody(t, fixture.Updates[2].Body, 42_000)
	receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	displayClock.Set(displayClock.Now().Add(3 * time.Millisecond))
	gatewayClock.Set(displayClock.Now())
	unsafeDisplayMS := displayClock.Now().UnixMilli()
	unsafeSequence := raw.HighWater().Current().Sequence + 1
	postM4Raw(t, paired.capture.Handler, unsafeBody)
	waitObservation(t, active, unsafeSequence)
	unsafeCandidateCommit := m4ObservationCommit(t, active, unsafeSequence)
	if len(unsafeCandidateCommit.AuditEvents) == 0 || unsafeCandidateCommit.AuditEvents[0].EventType != "candidate_suppressed" || unsafeCandidateCommit.AuditEvents[0].Reason != "candidate_evidence_mismatch" {
		t.Fatalf("oversize candidate was not suppressed through production policy: %#v", unsafeCandidateCommit.AuditEvents)
	}
	gatewayFailure := getM4OverlayFailure(t, paired.delivery.Handler)
	unsafeOperator := getM4Operator(t, paired.delivery.Handler)
	faultMu.Lock()
	unsafeCandidateBytes, unsafeOverlayBytes := *lastCandidateBytes, *lastOverlayBytes
	faultMu.Unlock()
	if gatewayFailure != "overlay_unavailable" || unsafeCandidateBytes != contracts.MaxInsightCandidateBytes+1 || unsafeOverlayBytes != contracts.MaxOverlayBytes+1 {
		t.Fatalf("oversize candidate/overlay boundary candidate=%d overlay=%d gateway=%s", unsafeCandidateBytes, unsafeOverlayBytes, gatewayFailure)
	}
	noReappearance := true
	for i := 0; i < 3; i++ {
		receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
		policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
		displayClock.Set(displayClock.Now().Add(3 * time.Millisecond))
		gatewayClock.Set(displayClock.Now())
		sequence := raw.HighWater().Current().Sequence + 1
		postM4Raw(t, paired.capture.Handler, unsafeBody)
		waitObservation(t, active, sequence)
		if reason := getM4OverlayFailure(t, paired.delivery.Handler); reason != "overlay_unavailable" {
			noReappearance = false
		}
	}
	if !noReappearance || displayClock.Now().UnixMilli()-unsafeDisplayMS > 2_000 {
		t.Fatalf("unsafe output reappeared or exceeded hide bound: no_reappearance=%t elapsed=%d", noReappearance, displayClock.Now().UnixMilli()-unsafeDisplayMS)
	}
	output.OperatorStates = append(output.OperatorStates, unsafeOperator)
	output.GatewayFailures = append(output.GatewayFailures, gatewayFailure)
	output.Boundaries = append(output.Boundaries, m4BoundaryEvidence{Name: "candidate_overlay_over_64_kib", RawSequence: unsafeSequence, CanonicalBytes: unsafeOverlayBytes, RawAccepted: true, PolicySequence: unsafeSequence, CandidateBytes: unsafeCandidateBytes, OverlayBytes: unsafeOverlayBytes, Visibility: "hidden", HealthCode: "overlay_unavailable", GatewayOutcome: gatewayFailure, OperatorRevision: unsafeOperator.PolicyRevision, NoTransientReappear: noReappearance})

	// A newer valid state is unsafe to the independently controlled gateway
	// clock. A subsequent delayed state older than the last accepted publication
	// must not rewind either delivery surface.
	faultMu.Lock()
	*candidateBoundaryBytes = 0
	*overlayBoundaryBytes = 0
	faultMu.Unlock()
	operatorBefore := getM4Operator(t, paired.delivery.Handler)
	displayClock.Set(time.UnixMilli(unsafeDisplayMS + 20).UTC())
	gatewayClock.Set(time.UnixMilli(unsafeDisplayMS + 10).UTC())
	receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	newerUnsafeSequence := raw.HighWater().Current().Sequence + 1
	postM4Raw(t, paired.capture.Handler, unsafeBody)
	waitObservation(t, active, newerUnsafeSequence)
	newerUnsafeFailure := getM4OverlayFailure(t, paired.delivery.Handler)
	if newerUnsafeFailure != "overlay_unsafe" {
		t.Fatalf("newer gateway state was not unsafe: %s", newerUnsafeFailure)
	}
	output.GatewayFailures = append(output.GatewayFailures, newerUnsafeFailure)
	displayClock.Set(time.UnixMilli(exactBoundaryOverlay.PublicationTimeMS - 1).UTC())
	gatewayClock.Set(time.UnixMilli(unsafeDisplayMS + 30).UTC())
	receiptClock.Set(receiptClock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	delayedSequence := raw.HighWater().Current().Sequence + 1
	postM4Raw(t, paired.capture.Handler, unsafeBody)
	waitObservation(t, active, delayedSequence)
	reorderFailure := getM4OverlayFailure(t, paired.delivery.Handler)
	operatorAfter := getM4Operator(t, paired.delivery.Handler)
	if reorderFailure != "overlay_out_of_order" || !bytes.Equal(mustCanonicalTest(t, operatorBefore), mustCanonicalTest(t, operatorAfter)) {
		t.Fatalf("delayed gateway state rewound publication/operator: reason=%s before=%#v after=%#v", reorderFailure, operatorBefore, operatorAfter)
	}
	output.OperatorStates = append(output.OperatorStates, operatorBefore, operatorAfter)
	output.GatewayFailures = append(output.GatewayFailures, reorderFailure)
	output.Boundaries = append(output.Boundaries, m4BoundaryEvidence{Name: "delayed_gateway_reorder", RawSequence: delayedSequence, RawAccepted: true, PolicySequence: delayedSequence, Visibility: "hidden", HealthCode: "overlay_out_of_order", GatewayOutcome: reorderFailure, OperatorRevision: operatorAfter.PolicyRevision, NoTransientReappear: true})

	displayClock.Set(time.UnixMilli(unsafeDisplayMS + 40).UTC())
	gatewayClock.Set(displayClock.Now())
}

func postM4Raw(t *testing.T, handler http.Handler, body []byte) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("production raw capture=%d %s", response.Code, response.Body.String())
	}
}

func m4ObservationCommit(t *testing.T, active *broadcastRuntimeV3, sequence uint64) contracts.PolicyCommitV3 {
	t.Helper()
	var found contracts.PolicyCommitV3
	if err := active.store.VisitAll(func(value commitlog.CommittedV3) error {
		if value.Commit.ObservationSequence == sequence {
			found = value.Commit
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found.ObservationSequence != sequence {
		t.Fatalf("missing terminal commit for observation %d", sequence)
	}
	return found
}

func exactM4Observation(t *testing.T, observation contracts.LiveObservationV1, target int) contracts.LiveObservationV1 {
	t.Helper()
	observation.Quality.Flags = append(observation.Quality.Flags, "")
	for attempts := 0; attempts < 4; attempts++ {
		encoded := mustCanonicalTest(t, observation)
		if len(encoded) == target {
			if target <= contracts.MaxLiveObservationBytes && observation.Validate() != nil {
				t.Fatal("exact-at-limit live observation did not validate")
			}
			if target > contracts.MaxLiveObservationBytes && observation.Validate() == nil {
				t.Fatal("over-limit live observation unexpectedly validated")
			}
			return observation
		}
		delta := target - len(encoded)
		if delta < 0 {
			t.Fatal("live observation fixture exceeds target")
		}
		observation.Quality.Flags[len(observation.Quality.Flags)-1] += strings.Repeat("x", delta)
	}
	t.Fatalf("could not construct exact live observation size %d", target)
	return contracts.LiveObservationV1{}
}

func exactM4Candidate(t *testing.T, candidate contracts.InsightCandidateV1, target int) contracts.InsightCandidateV1 {
	t.Helper()
	padding := ""
	candidate.Parameters = append(candidate.Parameters, contracts.TypedParameterV1{Name: "boundary_padding", Type: "string", StringValue: &padding})
	for attempts := 0; attempts < 4; attempts++ {
		candidate.CandidateID = ""
		id, err := contracts.InsightCandidateContentID(candidate)
		if err != nil {
			t.Fatal(err)
		}
		candidate.CandidateID = id
		encoded := mustCanonicalTest(t, candidate)
		if len(encoded) == target {
			if target <= contracts.MaxInsightCandidateBytes && candidate.Validate() != nil {
				t.Fatal("exact-at-limit candidate did not validate")
			}
			if target > contracts.MaxInsightCandidateBytes && candidate.Validate() == nil {
				t.Fatal("over-limit candidate unexpectedly validated")
			}
			return candidate
		}
		padding += strings.Repeat("x", target-len(encoded))
		candidate.Parameters[len(candidate.Parameters)-1].StringValue = &padding
	}
	t.Fatalf("could not construct exact candidate size %d", target)
	return contracts.InsightCandidateV1{}
}

func exactM4OverlayState(t *testing.T, state contracts.OverlayStateV1, target int) contracts.OverlayStateV1 {
	t.Helper()
	if len(state.Evidence) == 0 {
		t.Fatal("cannot size overlay without production evidence")
	}
	for attempts := 0; attempts < 4; attempts++ {
		encoded := mustCanonicalTest(t, state)
		if len(encoded) == target {
			if target <= contracts.MaxOverlayBytes && state.Validate() != nil {
				t.Fatal("exact-at-limit overlay did not validate")
			}
			if target > contracts.MaxOverlayBytes && state.Validate() == nil {
				t.Fatal("over-limit overlay unexpectedly validated")
			}
			return state
		}
		state.Evidence[0].Source += strings.Repeat("x", target-len(encoded))
	}
	t.Fatalf("could not construct exact overlay size %d", target)
	return contracts.OverlayStateV1{}
}

func m4NetWorthBody(t *testing.T, source json.RawMessage, netWorth int) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(source, &body); err != nil {
		t.Fatal(err)
	}
	body["player"].(map[string]any)["team2"].(map[string]any)["player0"].(map[string]any)["net_worth"] = netWorth
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func exerciseM4ProjectionAndSaturation(t *testing.T, paired *pairedHTTPServer, active *broadcastRuntimeV3, raw *session.Store, fixture m4Schedule, root string, clock, policyClock *lockedClock, output *m4ProductionOutput) {
	t.Helper()
	malformed := httptest.NewRecorder()
	paired.capture.Handler.ServeHTTP(malformed, httptest.NewRequest(http.MethodPost, "/gsi", strings.NewReader("{")))
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed gsi=%d %s", malformed.Code, malformed.Body.String())
	}
	exactPayload := exactM4GSIBody(t, fixture.Updates[3].Body, 10<<20)
	clock.Set(clock.Now().Add(time.Millisecond))
	policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
	exact := httptest.NewRecorder()
	paired.capture.Handler.ServeHTTP(exact, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(exactPayload)))
	if exact.Code != http.StatusOK {
		t.Fatalf("exact 10 MiB gsi=%d %s", exact.Code, exact.Body.String())
	}
	waitObservation(t, active, raw.HighWater().Current().Sequence)
	presentationFault := getM4Overlay(t, paired.delivery.Handler)
	if presentationFault.Visibility != "hidden" || presentationFault.HealthCode != "presentation_invalid" {
		t.Fatalf("production presentation failure=%#v", presentationFault)
	}
	output.FaultStates = append(output.FaultStates, presentationFault)
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
		policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
		response := httptest.NewRecorder()
		paired.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(payload)))
		if response.Code != http.StatusOK {
			t.Fatalf("saturation gsi[%d]=%d %s", i, response.Code, response.Body.String())
		}
		waitObservation(t, active, raw.HighWater().Current().Sequence)
	}
	active.mu.Lock()
	saturated := active.candidateSaturated
	active.mu.Unlock()
	if !saturated {
		t.Fatal("production composition did not latch candidate saturation")
	}
	output.Saturated = getM4Overlay(t, paired.delivery.Handler)
	gatewayFailure := getM4OperatorFailure(t, paired.delivery.Handler)
	if gatewayFailure != "operator_state_unavailable" {
		t.Fatalf("production gateway saturation failure=%s", gatewayFailure)
	}
	output.GatewayFailures = append(output.GatewayFailures, gatewayFailure)
	for i := 0; i < 3; i++ {
		clock.Set(clock.Now().Add(time.Millisecond))
		policyClock.Set(policyClock.Now().Add(2 * time.Millisecond))
		response := httptest.NewRecorder()
		paired.capture.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(fixture.Updates[3].Body)))
		if response.Code != http.StatusOK {
			t.Fatalf("post-saturation raw[%d]=%d", i, response.Code)
		}
		waitObservation(t, active, raw.HighWater().Current().Sequence)
		state := getM4Overlay(t, paired.delivery.Handler)
		if state.Visibility != "hidden" || state.HealthCode != "candidate_queue_saturated" {
			t.Fatalf("post-saturation claim reappeared[%d]=%#v", i, state)
		}
	}
	output.OverlayStates = append(output.OverlayStates, output.Saturated)
	output.FixtureSHA256 = m4FixtureSHA256
}

func collectM4OrderedEvidence(t *testing.T, active *broadcastRuntimeV3, raw *session.Store, fixture m4Schedule, root string, output *m4ProductionOutput) {
	t.Helper()
	resolver := newLiveOnlyObservationResolver(filepath.Join(root, fixture.SessionID, "raw.jsonl"), fixture.SessionID, active.artifacts, active.mapObservation, active.evaluateLiveOnly)
	defer resolver.Close()
	if err := active.store.VisitAll(func(value commitlog.CommittedV3) error {
		output.Commits = append(output.Commits, value.Commit)
		if value.Commit.LiveObservationSHA256 != "" {
			output.ObservationSHA = append(output.ObservationSHA, value.Commit.LiveObservationSHA256)
			candidates, resolveErr := resolver.resolve(value.Commit)
			if resolveErr != nil {
				return resolveErr
			}
			output.Candidates = append(output.Candidates, candidates)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rawCount := 0
	err := session.StreamRecords(filepath.Join(root, fixture.SessionID, "raw.jsonl"), fixture.SessionID, func(record *session.Record) error {
		sum := sha256.Sum256(record.Raw)
		output.RawSHA256 = append(output.RawSHA256, hex.EncodeToString(sum[:]))
		rawCount++
		return nil
	})
	if err != nil || rawCount != int(raw.HighWater().Current().Sequence) {
		t.Fatalf("raw replay count=%d err=%v", rawCount, err)
	}
}

func postM4AuditFailure(t *testing.T, captureHandler, deliveryHandler http.Handler, body json.RawMessage, receiptClock, policyClock *lockedClock, output *m4ProductionOutput) {
	t.Helper()
	receiptClock.Set(receiptClock.Now().Add(time.Second))
	policyClock.Set(policyClock.Now().Add(time.Second))
	response := httptest.NewRecorder()
	captureHandler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("audit-failure raw capture=%d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := getM4Overlay(t, deliveryHandler)
		if state.Visibility == "hidden" && state.HealthCode == "policy_commit_failed" {
			output.FaultStates = append(output.FaultStates, state)
			return
		}
		runtime.Gosched()
	}
	t.Fatal("production audit failure did not fail closed inside two seconds")
}

func getM4OperatorFailure(t *testing.T, handler http.Handler) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/operator/state", nil)
	request.Header.Set("Authorization", "Bearer "+m4Token)
	request.Header.Set("Origin", "http://127.0.0.1:43211")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("operator failure=%d %s", response.Code, response.Body.String())
	}
	var result struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Reason
}

func getM4OverlayFailure(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("overlay failure=%d %s", response.Code, response.Body.String())
	}
	var result struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Reason
}

func exactM4GSIBody(t *testing.T, source json.RawMessage, size int) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(source, &body); err != nil {
		t.Fatal(err)
	}
	body["padding"] = ""
	base, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	padding := size - len(base)
	if padding < 0 {
		t.Fatal("fixture exceeds exact body target")
	}
	body["padding"] = strings.Repeat("x", padding)
	payload, err := json.Marshal(body)
	if err != nil || len(payload) != size {
		t.Fatalf("exact body size=%d err=%v", len(payload), err)
	}
	return payload
}

func m4ClockFromBody(t *testing.T, body json.RawMessage, receiptMS, policyMS, displayMS int64) m4ClockEvidence {
	t.Helper()
	var decoded struct {
		Provider struct {
			Timestamp int64 `json:"timestamp"`
		} `json:"provider"`
		Map struct {
			ClockTime int64 `json:"clock_time"`
		} `json:"map"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	return m4ClockEvidence{SourceProviderMS: decoded.Provider.Timestamp * 1000, ReceiptMS: receiptMS, GameMS: decoded.Map.ClockTime * 1000, PolicyMS: policyMS, DisplayMS: displayMS}
}

func waitProjectionRejection(t *testing.T, runtimeV3 *broadcastRuntimeV3, code string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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

func waitObservationHealth(t *testing.T, handler http.Handler, code string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
		if response.Code == http.StatusOK {
			var state contracts.OverlayStateV1
			if contracts.DecodeStrict(response.Body.Bytes(), &state) == nil && state.Visibility == "hidden" && state.HealthCode == code {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("observation boundary did not reach health %s", code)
}

func waitRestoreComplete(t *testing.T, runtimeV3 *broadcastRuntimeV3) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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
	deadline := time.Now().Add(10 * time.Second)
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
	runtimeV3.mu.Lock()
	last, health, visibility := runtimeV3.app.State().LastObservationSequence, runtimeV3.overlay.HealthCode, runtimeV3.overlay.Visibility
	runtimeV3.mu.Unlock()
	t.Fatalf("policy projection did not reach observation %d: last=%d overlay=%s/%s", sequence, last, visibility, health)
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

func getM4OverlayBody(t *testing.T, handler http.Handler) (contracts.OverlayStateV1, []byte) {
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
	return value, response.Body.Bytes()
}
