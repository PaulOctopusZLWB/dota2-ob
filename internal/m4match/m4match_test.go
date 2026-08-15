package m4match

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
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

func TestBoundariesRequirePregameZeroAndNormalPostgame(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "raw.jsonl")
	states := []struct {
		clock int64
		state string
	}{{-10, "DOTA_GAMERULES_STATE_PRE_GAME"}, {0, "DOTA_GAMERULES_STATE_GAME_IN_PROGRESS"}, {2200, "DOTA_GAMERULES_STATE_POST_GAME"}}
	file, _ := os.Create(path)
	for index, state := range states {
		raw, _ := json.Marshal(map[string]any{"map": map[string]any{"clock_time": state.clock, "game_state": state.state, "matchid": 123}})
		frame := map[string]any{"sequence": index + 1, "raw_base64": base64.StdEncoding.EncodeToString(raw)}
		_ = json.NewEncoder(file).Encode(frame)
	}
	_ = file.Close()
	boundary, err := inspectBoundaries(path, "123", liveBoundary{})
	if err != nil {
		t.Fatal(err)
	}
	if !boundary.Pregame || !boundary.Zero || !boundary.Post || boundary.Last != 3 {
		t.Fatalf("boundary=%#v", boundary)
	}
}

func TestVerifierRejectsMutationAndNeverAcceptsP4(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	artifactPath := filepath.Join(root, "config/example")
	if err := writePrivate(artifactPath, []byte("safe\n")); err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{SchemaVersion: SchemaVersion, Mode: "preflight", CandidateCommit: AcceptedFunctionalBase, CandidateParent: "parent", AcceptedBase: AcceptedFunctionalBase, AcceptedSpec: AcceptedP4Spec, FixtureSHA256: CapturedScheduleSHA256, GoldenSHA256: ProductionGoldenSHA256, Bounds: AcceptedBounds(), Checks: []Check{{ID: "all", Passed: true, Detail: "ok"}}, Faults: append([]string(nil), RequiredFaults...), Artifacts: []Artifact{{Path: "config/example", SHA256: payloadSHA([]byte("safe\n")), Bytes: 5}}, NonResumable: true, SyntheticOnly: true, ClaimsP4: false, OperatorScript: HumanInstruction, ReadinessIssueText: HumanInstruction}
	sortEvidence(&evidence)
	index, _ := canonical(evidence)
	if err := writePrivate(filepath.Join(root, "evidence/canonical/evidence-index.json"), index); err != nil {
		t.Fatal(err)
	}
	readiness := Readiness{SchemaVersion: ReadinessSchemaVersion, Ready: true, Mode: "preflight", CandidateCommit: AcceptedFunctionalBase, EvidenceIndexSHA256: payloadSHA(index), ClaimsP4: false, HumanInstruction: HumanInstruction}
	if err := writeJSON(filepath.Join(root, "evidence/readiness.json"), readiness, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), root, repo, "preflight"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("mutated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), root, repo, "preflight"); err == nil {
		t.Fatal("verifier accepted mutated evidence")
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
