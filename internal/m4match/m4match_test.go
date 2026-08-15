package m4match

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func TestLiveArtifactsMatchAcceptedProductIdentity(t *testing.T) {
	history, lineage, release, err := liveArtifacts("m4-live-only")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lineage.EngineBuild.ContentSHA256, "52d7c46c55932b581aea29cd24d623ab604897c752479f9bc7bbd43bac62fc5c"; got != want {
		t.Fatalf("engine build=%s want=%s", got, want)
	}
	if got, want := lineage.MustContentID(), "a256e20aa0c9f7ef457ad4e9b3a2cfc8ec5d83af18fb919490955c0412ed2585"; got != want {
		t.Fatalf("lineage=%s want=%s", got, want)
	}
	if got, _ := release.ContentID(); got != "3ee8b789a4046b9adc2aff2d13958663831b7e134329b4d83cea6700673edccc" {
		t.Fatalf("release=%s", got)
	}
	if err := release.ValidateAgainst(history, lineage); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareIsRootIndependentAndNativeGeometry(t *testing.T) {
	first, second := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	a, err := Prepare(first, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Prepare(second, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	sortArtifacts := func(items []Artifact) map[string]Artifact {
		result := map[string]Artifact{}
		for _, item := range items {
			result[item.Path] = item
		}
		return result
	}
	if !reflect.DeepEqual(sortArtifacts(a), sortArtifacts(b)) {
		t.Fatalf("prepared artifacts depend on root\n%#v\n%#v", a, b)
	}
	payload, err := os.ReadFile(filepath.Join(first, "config/obs-studio/basic/scenes/DOT65-P4.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{[]byte(`"width": 750`), []byte(`"height": 640`), []byte(`"x": 1130`), []byte(`"y": 60`), []byte(`"url": "http://127.0.0.1:43911/overlay/"`)} {
		if !bytes.Contains(payload, required) {
			t.Fatalf("OBS collection missing %s", required)
		}
	}
	if !bytes.Contains(payload, []byte(`"id": "xcomposite_input"`)) || !bytes.Contains(payload, []byte(`"capture_window": "Dota 2"`)) {
		t.Fatal("isolated OBS collection lacks a Dota-window-only source")
	}
	gsi, _ := os.ReadFile(filepath.Join(first, "config/dota/gamestate_integration_dota2_ob_m4.cfg"))
	if bytes.Contains(gsi, []byte(`"provider"  "1"`)) || !bytes.Contains(gsi, []byte(`"provider"  "0"`)) {
		t.Fatal("GSI provider identity is not disabled")
	}
}

func TestBoundariesRequireContinuousIdentityCadenceAndNormalPostgame(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "raw.jsonl")
	states := []struct {
		clock  int64
		state  string
		winner string
	}{{-2, "DOTA_GAMERULES_STATE_PRE_GAME", ""}, {-1, "DOTA_GAMERULES_STATE_PRE_GAME", ""}, {0, "DOTA_GAMERULES_STATE_GAME_IN_PROGRESS", ""}, {1, "DOTA_GAMERULES_STATE_POST_GAME", "radiant"}}
	file, _ := os.Create(path)
	start := time.Now().UTC().Add(time.Second)
	for index, state := range states {
		raw, _ := json.Marshal(map[string]any{"map": map[string]any{"clock_time": state.clock, "game_state": state.state, "matchid": 123, "win_team": state.winner}})
		frame := map[string]any{"sequence": index + 1, "received_at": start.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano), "raw_base64": base64.StdEncoding.EncodeToString(raw)}
		_ = json.NewEncoder(file).Encode(frame)
	}
	_ = file.Close()
	boundary, err := inspectBoundaryRecords(path, LiveIdentity{MatchID: "123"}, liveBoundary{ArmedAt: time.Now().UTC(), IdentityContinuous: true})
	if err != nil {
		t.Fatal(err)
	}
	if !boundary.Pregame || !boundary.Zero || !boundary.Post || !boundary.WinnerObserved || boundary.Last != 4 {
		t.Fatalf("boundary=%#v", boundary)
	}
}

func TestVerifierRejectsReadinessCandidateMutationAndMissingLog(t *testing.T) {
	identity := CandidateIdentity{Commit: "1111111111111111111111111111111111111111", SoleParent: RequiredSuccessorParent, EnvironmentSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
	identitySHA, _ := candidateIdentitySHA(identity)
	evidence := Evidence{CandidateCommit: identity.Commit, CandidateParent: RequiredSuccessorParent, CandidateIdentity: identity, Faults: append([]string(nil), RequiredFaults...)}
	for _, id := range []string{"accepted_ancestry", "candidate_commit", "candidate_parent", "candidate_identity", "candidate_binary_hash", "captured_schedule", "production_golden", "clean_tree", "clean_tree_final", "isolated_process_state", "focused_twice", "m4_fault_matrix", "full_go", "full_race", "vet", "build_all", "module_verify", "browser_install", "browser_tests", "obs_overlay_install", "obs_overlay_tests", "production_endpoints", "product_sigkill_restart", "privacy_and_source_boundary", "secret_generated_scan", "dependency_boundary", "diff_check"} {
		evidence.Checks = append(evidence.Checks, Check{ID: id, Passed: true})
	}
	for _, fault := range RequiredFaults {
		evidence.Checks = append(evidence.Checks, Check{ID: "fault_" + fault, Passed: true})
	}
	for _, path := range []string{"evidence/canonical/fault-proof-manifest.json", "evidence/logs/focused_twice.log", "evidence/logs/m4_fault_matrix.log", "evidence/logs/full_go.log", "evidence/logs/full_race.log", "evidence/logs/vet.log", "evidence/logs/build_all.log", "evidence/logs/module_verify.log", "evidence/logs/browser_tests.log", "evidence/logs/obs_overlay_tests.log", "evidence/logs/product-probe.log", "evidence/logs/product-sigkill-restart.log"} {
		evidence.Artifacts = append(evidence.Artifacts, Artifact{Path: path})
	}
	readiness := Readiness{CandidateCommit: identity.Commit, CandidateIdentitySHA256: identitySHA, EnvironmentSHA256: identity.EnvironmentSHA256}
	if err := validateIdentityBindings(readiness, evidence, identity); err != nil {
		t.Fatal(err)
	}
	readiness.CandidateCommit = strings.Repeat("0", 40)
	if err := validateIdentityBindings(readiness, evidence, identity); err == nil {
		t.Fatal("mutated readiness candidate was accepted")
	}
	if err := validateTransitiveEvidence(evidence, "preflight"); err != nil {
		t.Fatal(err)
	}
	evidence.Artifacts = evidence.Artifacts[:len(evidence.Artifacts)-1]
	if err := validateTransitiveEvidence(evidence, "preflight"); err == nil {
		t.Fatal("missing selected fault log was accepted")
	}
}

func TestSafeRootRejectsRepositoryCanonicalAndHome(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	_ = os.MkdirAll(repo, 0o700)
	for _, path := range []string{repo, filepath.Join(repo, "evidence"), "/home/paul-zhang/文档/dota2_ob", os.Getenv("HOME")} {
		if _, err := safeRoot(path, repo); err == nil {
			t.Fatalf("safeRoot accepted %s", path)
		}
	}
}

func TestRepositoryPrivacySourceSecretAndGeneratedScans(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := scanPrivacyAndSources(repo); err != nil {
		t.Fatal(err)
	}
	if err := scanSecretsAndGenerated(repo); err != nil {
		t.Fatal(err)
	}
}

func TestExactV3ProductAcceptsPreparedArtifactsAndEndpoints(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	if _, err := writeLiveArtifacts(root, "dot65-preflight"); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "application/dota2-ob")
	command := exec.Command("go", "build", "-trimpath", "-ldflags=-buildid=", "-o", binary, "./cmd/dota2-ob")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	if err := probeProduct(context.Background(), root, binary); err != nil {
		t.Fatal(err)
	}
	if count, err := readRawSequences(filepath.Join(root, "data/sessions/dot65-preflight/raw.jsonl")); err != nil || count != 1 {
		t.Fatalf("raw count=%d err=%v", count, err)
	}
}

func TestExactV3ProductSIGKILLsAndRecoversRetainedRaw(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	if _, err := writeLiveArtifacts(root, "dot65-preflight"); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "application/dota2-ob")
	command := exec.Command("go", "build", "-trimpath", "-ldflags=-buildid=", "-o", binary, "./cmd/dota2-ob")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	if err := probeProductSIGKILLRestart(context.Background(), root, binary); err != nil {
		log, _ := os.ReadFile(filepath.Join(root, "evidence/logs/product-sigkill-restart.log"))
		t.Fatalf("%v\n%s", err, log)
	}
	if count, err := readRawSequences(filepath.Join(root, "data/sigkill-sessions/dot65-preflight/raw.jsonl")); err != nil || count != 1 {
		t.Fatalf("raw count=%d err=%v", count, err)
	}
}

func TestSyntheticThreeFrameAndMissingOrChangingMatchIdentityFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		matches []any
		clocks  []int64
	}{
		{name: "synthetic three frame", matches: []any{123, 123, 123}, clocks: []int64{-1, 0, 2200}},
		{name: "missing identity", matches: []any{123, nil, 123}, clocks: []int64{-1, 0, 1}},
		{name: "changing identity", matches: []any{123, 124, 124}, clocks: []int64{-1, 0, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			file, _ := os.Create(path)
			start := time.Now().UTC().Add(time.Second)
			for index := range tc.clocks {
				match := tc.matches[index]
				value := map[string]any{"clock_time": tc.clocks[index], "game_state": "DOTA_GAMERULES_STATE_GAME_IN_PROGRESS"}
				if index == 0 {
					value["game_state"] = "DOTA_GAMERULES_STATE_PRE_GAME"
				}
				if index == len(tc.clocks)-1 {
					value["game_state"], value["win_team"] = "DOTA_GAMERULES_STATE_POST_GAME", "radiant"
				}
				if match != nil {
					value["matchid"] = match
				}
				raw, _ := json.Marshal(map[string]any{"map": value})
				_ = json.NewEncoder(file).Encode(map[string]any{"sequence": index + 1, "received_at": start.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano), "raw_base64": base64.StdEncoding.EncodeToString(raw)})
			}
			_ = file.Close()
			if _, err := inspectBoundaryRecords(path, LiveIdentity{MatchID: "123"}, liveBoundary{ArmedAt: time.Now().UTC(), IdentityContinuous: true}); err == nil {
				t.Fatal("adversarial live fixture was accepted")
			}
		})
	}
}

func TestMissingTelemetryFailsInsteadOfPassingAsZero(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "samples.jsonl")
	if err := writeJSON(path, Sample{At: time.Now().UTC(), Sequence: 1, ProjectedSequence: 1, AcceptedCount: 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSamples(path, root); err == nil {
		t.Fatal("zero-valued missing telemetry passed")
	}
}

func TestRawOnlyRecoveryDoesNotCopyFakePolicyOutput(t *testing.T) {
	root := t.TempDir()
	sessionID := "ti-123"
	sessionDir := filepath.Join(root, "data/sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\"sequence\":1}\n")
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "00000000000000000001.pcl3"), []byte("fake-derived-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := performRawOnlyRecovery(context.Background(), root, sessionID); err == nil {
		t.Fatal("fake recovery unexpectedly succeeded")
	}
	_ = filepath.WalkDir(filepath.Join(root, "evidence/recovery-work/data"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".pcl3") {
			t.Fatalf("derived policy output was copied into recovery: %s", path)
		}
		return nil
	})
}

func TestIgnoredShutdownIsAValidationError(t *testing.T) {
	command := exec.Command("sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	time.Sleep(50 * time.Millisecond)
	if err := stopProcess(command, done, 10*time.Millisecond); err == nil {
		t.Fatal("forced shutdown was ignored")
	}
}

func TestOperatorActionsMustBeDurableAndOrdered(t *testing.T) {
	ordered := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	if !hasOperatorScript(ordered) {
		t.Fatal("ordered script rejected")
	}
	if hasOperatorScript([]string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}) {
		t.Fatal("missing pin ineligibility attempt accepted")
	}
	if hasOperatorScript([]string{contracts.ActionReject, contracts.ActionApprove, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}) {
		t.Fatal("out-of-order actions accepted")
	}
}

func TestDeliveryProxyPersistsRawOperatorInputBeforeForwarding(t *testing.T) {
	listener, err := net.Listen("tcp", ProductDeliveryAddress)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != ProductDeliveryOrigin {
			t.Errorf("origin=%s", request.Header.Get("Origin"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{}"))
	})}
	go func() { _ = upstream.Serve(listener) }()
	defer upstream.Close()
	root := t.TempDir()
	proxy, err := startDeliveryProxy(root)
	if err != nil {
		t.Fatal(err)
	}
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "hide", SessionID: "ti-123", Action: contracts.ActionEmergencyHide, PolicyTimeMS: time.Now().UnixMilli()}
	body, _ := contracts.MarshalCanonical(command)
	request, _ := http.NewRequest(http.MethodPost, DeliveryOrigin+"/v1/operator/commands", bytes.NewReader(body))
	request.Header.Set("Origin", DeliveryOrigin)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if err := proxy.stop(); err != nil {
		t.Fatal(err)
	}
	inputs, err := readOperatorInputs(filepath.Join(root, "evidence/raw-operator-input.jsonl"))
	if err != nil || len(inputs) != 1 || !bytes.Equal(inputs[0], body) {
		t.Fatalf("inputs=%d err=%v", len(inputs), err)
	}
}
