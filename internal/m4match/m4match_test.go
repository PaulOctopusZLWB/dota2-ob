package m4match

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestNormalizeLogCanonicalizesOnlyApprovedVolatility(t *testing.T) {
	first := []byte("[WebServer] 2026/08/15 18:30:41 server_started pid=123 session_id=20260815T103041.895384756Z capture_target=20260815T103041.895384756Z/raw.jsonl\n" +
		"duration_ms: 0.404157\n# duration_ms 37.626472\n{\"duration\":12.34}\n68 passed (30.7s)\n[1/68] stable test name\n")
	second := []byte("[WebServer] 2026/08/16 01:02:03 server_started pid=987 session_id=20260816T010203.123Z capture_target=20260816T010203.123Z/raw.jsonl\n" +
		"duration_ms: 9.99\n# duration_ms 88.1\n{\"duration\":56.78}\n68 passed (42.2s)\n[1/68] stable test name\n")
	want := normalizeLog(first, "/work/browser", "/evidence/a", nil)
	if got := normalizeLog(second, "/work/browser", "/evidence/b", nil); !bytes.Equal(got, want) {
		t.Fatalf("approved volatility changed canonical log\n%s\n%s", want, got)
	}
	for name, mutation := range map[string][]byte{
		"test name": bytes.ReplaceAll(second, []byte("stable test name"), []byte("different test name")),
		"count":     bytes.ReplaceAll(second, []byte("68 passed"), []byte("67 passed")),
		"error":     append(append([]byte(nil), second...), []byte("meaningful error\n")...),
	} {
		t.Run(name, func(t *testing.T) {
			if got := normalizeLog(mutation, "/work/browser", "/evidence/b", nil); bytes.Equal(got, want) {
				t.Fatal("meaningful output mutation did not change canonical log")
			}
		})
	}
	if got := normalizeLog(second, "/work/browser", "/evidence/b", errors.New("exit 1")); bytes.Equal(got, want) || !bytes.HasPrefix(got, []byte("status=FAIL\n")) {
		t.Fatal("command failure was not retained")
	}
}

func TestLockedInstallEvidenceDisablesNetworkAuditAndBindsLockfile(t *testing.T) {
	if got := strings.Join(lockedInstallArgs, " "); got != "ci --no-audit --no-fund" {
		t.Fatalf("npm install args=%q", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	logPath := filepath.Join(t.TempDir(), "evidence", "logs", "install.log")
	result := runLockedInstall(context.Background(), dir, logPath)
	if result.err == nil {
		t.Fatal("missing npm unexpectedly passed")
	}
	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte("lockfile_sha256=0c030586945fe504b604ecc2e875c38ede400cd5cd73da9730302162e6b02c6f")) || !bytes.HasPrefix(payload, []byte("status=FAIL\n")) {
		t.Fatalf("locked install transcript=%q", payload)
	}
}

func TestRunbookUsesVCSStampedHarnessInvocations(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "docs", "runbooks", "m4-ti-match.md"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("go run ./cmd/m4-match")) {
		t.Fatal("runbook contains an unstamped harness invocation")
	}
	for _, command := range []string{"preflight", "verify", "live", "cleanup"} {
		if !bytes.Contains(payload, []byte("go run -buildvcs=true ./cmd/m4-match "+command)) {
			t.Fatalf("runbook lacks VCS-stamped %s invocation", command)
		}
	}
}

func TestRemoteSnapshotParserRejectsAbsentDuplicateMalformedAndMismatchedRefs(t *testing.T) {
	branchRef := "refs/heads/" + ExpectedBranch
	prRef := "refs/pull/" + ExpectedPR + "/head"
	head := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	line := func(oid, ref string) string { return oid + "\t" + ref + "\n" }
	for name, test := range map[string]struct {
		payload string
		reason  string
	}{
		"branch absent":    {line(head, prRef), "remote_branch_missing"},
		"branch duplicate": {line(head, branchRef) + line(head, branchRef) + line(head, prRef), "remote_branch_ambiguous"},
		"pr absent":        {line(head, branchRef), "pr_head_missing"},
		"pr duplicate":     {line(head, branchRef) + line(head, prRef) + line(head, prRef), "pr_head_ambiguous"},
		"malformed":        {"not-an-object\t" + branchRef, "remote_malformed"},
		"unexpected":       {line(head, branchRef) + line(head, prRef) + line(head, "refs/heads/other"), "remote_unexpected_ref"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, reason := parseRemoteSnapshot(test.payload, branchRef, prRef); reason != test.reason {
				t.Fatalf("reason=%s want=%s", reason, test.reason)
			}
		})
	}
	branch, pr, reason := parseRemoteSnapshot(line(other, branchRef)+line(head, prRef), branchRef, prRef)
	if reason != "ok" || branch != other || pr != head {
		t.Fatalf("valid mismatched identities were not retained: %s %s %s", branch, pr, reason)
	}
}

func TestCandidateIdentityEvidenceNamesEveryFailureAndBindsComposite(t *testing.T) {
	identity, evidence := successfulIdentityFixture()
	if err := validateCandidateIdentityEvidence(evidence, identity); err != nil {
		t.Fatal(err)
	}
	for index := range evidence.Checks {
		t.Run(evidence.Checks[index].ID, func(t *testing.T) {
			mutated := evidence
			mutated.Checks = append([]CandidateIdentitySubcheck(nil), evidence.Checks...)
			mutated.Checks[index].Passed = false
			mutated.Checks[index].ReasonCode = "command_failed"
			first := validateCandidateIdentityEvidence(mutated, identity)
			second := validateCandidateIdentityEvidence(mutated, identity)
			if first == nil || second == nil || first.Error() != second.Error() || !strings.Contains(first.Error(), mutated.Checks[index].ID) {
				t.Fatalf("failure is not stable and named: %v / %v", first, second)
			}
		})
	}
	mutated := evidence
	mutated.End.Commit = strings.Repeat("f", 40)
	if err := validateCandidateIdentityEvidence(mutated, identity); err == nil {
		t.Fatal("start/end mutation passed")
	}
	mutated = evidence
	mutated.BinarySHA256 = strings.Repeat("0", 64)
	if err := validateCandidateIdentityEvidence(mutated, identity); err == nil {
		t.Fatal("observed product hash mutation passed")
	}
}

func TestCandidateIdentityRemoteRetryIsTransportOnlyAndCanonicalEvidenceIsStable(t *testing.T) {
	head := strings.Repeat("a", 40)
	root := t.TempDir()
	binary := filepath.Join(root, "product")
	if err := os.WriteFile(binary, []byte("product"), 0o700); err != nil {
		t.Fatal(err)
	}
	direct := fakeIdentityCollector(head, []fakeRemoteResult{{}, {}})
	directStart := direct.captureRepository(context.Background(), root, "start")
	directIdentity, directEvidence, directDiagnostics, err := direct.complete(context.Background(), root, binary, Environment{}, directStart)
	if err != nil {
		t.Fatal(err)
	}
	transient := fakeIdentityCollector(head, []fakeRemoteResult{{err: errors.New("transport down"), output: "https://user:secret@example.invalid token=secret"}, {}, {err: errors.New("transport down")}, {}})
	transientStart := transient.captureRepository(context.Background(), root, "start")
	transientIdentity, transientEvidence, diagnostics, err := transient.complete(context.Background(), root, binary, Environment{}, transientStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(directDiagnostics) != 0 || len(diagnostics) != 2 {
		t.Fatalf("diagnostics direct=%v transient=%v", directDiagnostics, diagnostics)
	}
	if directIdentity != transientIdentity {
		t.Fatalf("identity differs after transient recovery\n%#v\n%#v", directIdentity, transientIdentity)
	}
	directPayload, _ := canonical(directEvidence)
	transientPayload, _ := canonical(transientEvidence)
	if !bytes.Equal(directPayload, transientPayload) {
		t.Fatalf("retry attempts leaked into canonical evidence\n%s\n%s", directPayload, transientPayload)
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, "secret") || strings.Contains(diagnostic, "https://") {
			t.Fatalf("diagnostic leaked secret transport text: %q", diagnostic)
		}
	}
}

func TestCandidateIdentityPersistentTransportProductHarnessAndMutationFailClosed(t *testing.T) {
	head := strings.Repeat("a", 40)
	root := t.TempDir()
	binary := filepath.Join(root, "product")
	if err := os.WriteFile(binary, []byte("product"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, configure := range map[string]func(*identityCollector){
		"persistent transport": func(collector *identityCollector) {
			*collector = fakeIdentityCollector(head, []fakeRemoteResult{{err: errors.New("offline")}, {err: errors.New("offline")}, {err: errors.New("offline")}, {err: errors.New("offline")}})
		},
		"product vcs mismatch": func(collector *identityCollector) {
			collector.executable = func(string) (string, bool, error) { return strings.Repeat("b", 40), false, nil }
		},
		"product vcs modified": func(collector *identityCollector) {
			collector.executable = func(string) (string, bool, error) { return head, true, nil }
		},
		"harness vcs mismatch": func(collector *identityCollector) {
			collector.harness = func() (string, bool, error) { return strings.Repeat("b", 40), false, nil }
		},
		"harness vcs modified": func(collector *identityCollector) {
			collector.harness = func() (string, bool, error) { return head, true, nil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			collector := fakeIdentityCollector(head, []fakeRemoteResult{{}, {}})
			configure(&collector)
			start := collector.captureRepository(context.Background(), root, "start")
			identity, evidence, _, err := collector.complete(context.Background(), root, binary, Environment{}, start)
			if err == nil || identity != (CandidateIdentity{}) {
				t.Fatalf("failure populated composite identity: %#v / %v", identity, err)
			}
			failed := 0
			for _, check := range evidence.Checks {
				if !check.Passed {
					failed++
				}
			}
			if failed == 0 {
				t.Fatal("failure lacks named sub-check evidence")
			}
		})
	}
	mutation := fakeIdentityCollector(head, []fakeRemoteResult{{}, {}})
	phase := 0
	baseRun := mutation.run
	mutation.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) == 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			phase++
			if phase > 1 {
				return strings.Repeat("b", 40), nil
			}
		}
		return baseRun(ctx, dir, name, args...)
	}
	start := mutation.captureRepository(context.Background(), root, "start")
	identity, evidence, _, err := mutation.complete(context.Background(), root, binary, Environment{}, start)
	if err == nil || identity != (CandidateIdentity{}) || checkIdentitySubcheck(evidence.Checks, "repository_remote_stability") {
		t.Fatalf("start/end mutation did not fail closed: %#v %v", identity, err)
	}
}

func TestCandidateIdentityRepositoryAndProductHashFailuresAreNamed(t *testing.T) {
	head := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	root := t.TempDir()
	binary := filepath.Join(root, "product")
	if err := os.WriteFile(binary, []byte("product"), 0o700); err != nil {
		t.Fatal(err)
	}
	branchRef := "refs/heads/" + ExpectedBranch
	prRef := "refs/pull/" + ExpectedPR + "/head"
	for name, test := range map[string]struct {
		check  string
		mutate func(identityCommand) identityCommand
	}{
		"local head": {"local_head_start", func(base identityCommand) identityCommand {
			return overrideIdentityCommand(base, "rev-parse HEAD", "short", nil)
		}},
		"sole parent": {"sole_parent_start", func(base identityCommand) identityCommand {
			return overrideIdentityCommand(base, "rev-list --parents -n 1 HEAD", head+" "+other, nil)
		}},
		"repository tree": {"repository_tree_start", func(base identityCommand) identityCommand {
			return overrideIdentityCommand(base, "rev-parse HEAD^{tree}", "invalid", nil)
		}},
		"origin": {"origin_start", func(base identityCommand) identityCommand {
			return overrideIdentityCommand(base, "remote get-url origin", "https://example.invalid/other.git", nil)
		}},
		"remote branch mismatch": {"remote_branch_start", func(base identityCommand) identityCommand {
			return overrideIdentityCommand(base, "ls-remote origin "+branchRef+" "+prRef, other+"\t"+branchRef+"\n"+head+"\t"+prRef, nil)
		}},
		"pr head mismatch": {"pr_head_start", func(base identityCommand) identityCommand {
			return overrideIdentityCommand(base, "ls-remote origin "+branchRef+" "+prRef, head+"\t"+branchRef+"\n"+other+"\t"+prRef, nil)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			collector := fakeIdentityCollector(head, nil)
			collector.run = test.mutate(collector.run)
			start := collector.captureRepository(context.Background(), root, "start")
			identity, evidence, _, err := collector.complete(context.Background(), root, binary, Environment{}, start)
			if err == nil || identity != (CandidateIdentity{}) || checkIdentitySubcheck(evidence.Checks, test.check) {
				t.Fatalf("%s did not fail closed: %#v %v %#v", test.check, identity, err, evidence.Checks)
			}
		})
	}
	collector := fakeIdentityCollector(head, nil)
	start := collector.captureRepository(context.Background(), root, "start")
	identity, evidence, _, err := collector.complete(context.Background(), root, filepath.Join(root, "absent"), Environment{}, start)
	if err == nil || identity != (CandidateIdentity{}) || checkIdentitySubcheck(evidence.Checks, "product_hash") {
		t.Fatalf("missing product hash did not fail closed: %#v %v", identity, err)
	}
}

func TestCandidateParentMismatchFailsClosedAtStartAndEnd(t *testing.T) {
	head := strings.Repeat("a", 40)
	wrongParent := strings.Repeat("b", 40)
	root := t.TempDir()
	binary := filepath.Join(root, "product")
	if err := os.WriteFile(binary, []byte("product"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mismatchPhase := range []string{"start", "end"} {
		t.Run(mismatchPhase, func(t *testing.T) {
			collector := fakeIdentityCollector(head, nil)
			baseRun := collector.run
			parentCapture := 0
			collector.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
				if name == "git" && strings.Join(args, " ") == "rev-list --parents -n 1 HEAD" {
					parentCapture++
					if (mismatchPhase == "start" && parentCapture == 1) || (mismatchPhase == "end" && parentCapture == 2) {
						return head + " " + wrongParent, nil
					}
				}
				return baseRun(ctx, dir, name, args...)
			}
			start := collector.captureRepository(context.Background(), root, "start")
			identity, evidence, _, err := collector.complete(context.Background(), root, binary, Environment{}, start)
			if err == nil || identity != (CandidateIdentity{}) {
				t.Fatalf("%s parent mismatch populated composite identity: %#v / %v", mismatchPhase, identity, err)
			}
			for _, phase := range []string{"start", "end"} {
				wantPassed := phase != mismatchPhase
				found := false
				for _, check := range evidence.Checks {
					if check.ID == "sole_parent_"+phase {
						found = true
						if check.Passed != wantPassed || (!wantPassed && check.ReasonCode != "sole_parent_mismatch") {
							t.Fatalf("%s parent check=%#v want passed=%v", phase, check, wantPassed)
						}
					}
				}
				if !found {
					t.Fatalf("missing sole_parent_%s evidence", phase)
				}
			}
		})
	}
}

func TestIdentityDiagnosticSanitizationIsBounded(t *testing.T) {
	input := strings.Repeat("https://user:password@example.invalid token=topsecret\n", 500)
	got := sanitizeIdentityDiagnostic(input)
	if len(got) > 2048 || strings.Count(got, "\n") >= 8 || strings.Contains(got, "password") || strings.Contains(got, "topsecret") || strings.Contains(got, "https://") {
		t.Fatalf("diagnostic was not bounded and secret-safe: %q", got)
	}
}

type fakeRemoteResult struct {
	output string
	err    error
}

func fakeIdentityCollector(head string, remoteResults []fakeRemoteResult) identityCollector {
	branchRef := "refs/heads/" + ExpectedBranch
	prRef := "refs/pull/" + ExpectedPR + "/head"
	remoteIndex := 0
	collector := identityCollector{
		executable: func(string) (string, bool, error) { return head, false, nil },
		harness:    func() (string, bool, error) { return head, false, nil },
		delay:      func(context.Context) error { return nil },
	}
	collector.run = func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name != "git" {
			return "", errors.New("unexpected command")
		}
		switch strings.Join(args, " ") {
		case "rev-parse HEAD":
			return head, nil
		case "rev-list --parents -n 1 HEAD":
			return head + " " + RequiredSuccessorParent, nil
		case "rev-parse HEAD^{tree}":
			return strings.Repeat("c", 40), nil
		case "remote get-url origin":
			return ExpectedRemoteURL, nil
		case "ls-remote origin " + branchRef + " " + prRef:
			if remoteIndex >= len(remoteResults) {
				return head + "\t" + branchRef + "\n" + head + "\t" + prRef, nil
			}
			result := remoteResults[remoteIndex]
			remoteIndex++
			if result.output == "" && result.err == nil {
				result.output = head + "\t" + branchRef + "\n" + head + "\t" + prRef
			}
			return result.output, result.err
		default:
			return "", errors.New("unexpected git arguments: " + strings.Join(args, " "))
		}
	}
	return collector
}

func overrideIdentityCommand(base identityCommand, expectedArgs, output string, commandErr error) identityCommand {
	return func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "git" && strings.Join(args, " ") == expectedArgs {
			return output, commandErr
		}
		return base(ctx, dir, name, args...)
	}
}

func successfulIdentityFixture() (CandidateIdentity, CandidateIdentityEvidence) {
	head := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	hash := strings.Repeat("c", 64)
	environment := strings.Repeat("d", 64)
	snapshot := CandidateRepositorySnapshot{Commit: head, SoleParent: RequiredSuccessorParent, RepositoryRootSHA: tree, RemoteURL: ExpectedRemoteURL, RemoteBranchCommit: head, PRHeadCommit: head}
	identity := CandidateIdentity{Commit: head, SoleParent: RequiredSuccessorParent, RepositoryRootSHA: tree, RemoteURL: ExpectedRemoteURL, RemoteBranchCommit: head, PRHeadCommit: head, BinarySHA256: hash, BinaryVCSRevision: head, HarnessVCSRevision: head, EnvironmentSHA256: environment}
	evidence := CandidateIdentityEvidence{Start: snapshot, End: snapshot, BinarySHA256: hash, BinaryVCSRevision: head, HarnessVCSRevision: head, EnvironmentSHA256: environment}
	for _, id := range []string{
		"local_head_start", "sole_parent_start", "repository_tree_start", "origin_start", "remote_branch_start", "pr_head_start",
		"local_head_end", "sole_parent_end", "repository_tree_end", "origin_end", "remote_branch_end", "pr_head_end",
		"product_vcs", "product_hash", "harness_vcs", "environment_hash", "repository_remote_stability",
	} {
		evidence.Checks = append(evidence.Checks, CandidateIdentitySubcheck{ID: id, Passed: true, ReasonCode: "ok"})
	}
	return identity, evidence
}

func checkIdentitySubcheck(checks []CandidateIdentitySubcheck, id string) bool {
	for _, check := range checks {
		if check.ID == id {
			return check.Passed
		}
	}
	return false
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
	transportCalls, durableEffects := 0, 0
	upstream := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		transportCalls++
		if transportCalls == 1 {
			durableEffects++
		}
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
	if err != nil || len(inputs) != 2 || !bytes.Equal(inputs[0], body) || !bytes.Equal(inputs[1], body) {
		t.Fatalf("inputs=%d err=%v", len(inputs), err)
	}
	if transportCalls != 2 || durableEffects != 1 {
		t.Fatalf("transport=%d durable=%d", transportCalls, durableEffects)
	}
	if err := validateOperatorReplayProof(filepath.Join(root, "evidence/canonical/operator-replay-proof.json")); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorTerminalSemanticsFailClosed(t *testing.T) {
	actions := []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}
	reasons := []string{"approve", "reject", "pin", "unpin", "emergency_hide", "emergency_hide_cleared"}
	valid := make([]operatorTerminal, len(actions))
	for i, action := range actions {
		valid[i] = operatorTerminal{CommandID: fmt.Sprintf("c%d", i), Action: action, ExpectedRevision: uint64(i), Status: contracts.CommandAccepted, PreviousRevision: uint64(i), ResultingRevision: uint64(i + 1), Reason: reasons[i]}
		if i < 4 {
			valid[i].TargetCandidateID = "candidate"
		}
	}
	if err := validateOperatorTerminals(valid); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*operatorTerminal){
		func(v *operatorTerminal) { v.Status = contracts.CommandRejected },
		func(v *operatorTerminal) { v.PreviousRevision++ },
		func(v *operatorTerminal) { v.ResultingRevision++ },
		func(v *operatorTerminal) { v.TargetCandidateID = "" },
		func(v *operatorTerminal) { v.Reason = "stale_revision" },
		func(v *operatorTerminal) { v.TargetRuleID = "wrong" },
	}
	for i, mutate := range mutations {
		copyTerminals := append([]operatorTerminal(nil), valid...)
		mutate(&copyTerminals[0])
		if validateOperatorTerminals(copyTerminals) == nil {
			t.Fatalf("mutation %d accepted", i)
		}
	}
}

func TestLocalhostGSITrustBoundaryContradictionsFail(t *testing.T) {
	valid := LocalhostGSITrustEvidence{ConfigSHA256: strings.Repeat("a", 64), ConfigURI: "http://" + CaptureAddress + "/gsi", ConfigUserOnly: true, ExclusiveListener: true, ListenerProductOwned: true, ListenerURI: "http://" + CaptureAddress + "/gsi", DotaProcessStable: true, KnownProducerAbsent: true, CorrelationStartSHA256: strings.Repeat("b", 64), CorrelationEndSHA256: strings.Repeat("b", 64), IdentityContinuous: true, Requests: 3, Accepted: 2, Rejected: 1, RawRecords: 2, TerminalOutcomes: 2, RecordingCoextensive: true, PaulConfirmedIdentity: true, ResidualReasonCodes: []string{"localhost_gsi_sender_unattested"}}
	if err := validateLocalhostGSITrust(valid); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*LocalhostGSITrustEvidence){func(v *LocalhostGSITrustEvidence) { v.KnownProducerAbsent = false }, func(v *LocalhostGSITrustEvidence) { v.IdentityContinuous = false }, func(v *LocalhostGSITrustEvidence) { v.Requests++ }, func(v *LocalhostGSITrustEvidence) { v.PerRequestAttested = true }, func(v *LocalhostGSITrustEvidence) { v.RecordingCoextensive = false }, func(v *LocalhostGSITrustEvidence) { v.ResidualReasonCodes = nil }}
	for i, mutate := range mutations {
		changed := valid
		changed.ResidualReasonCodes = append([]string(nil), valid.ResidualReasonCodes...)
		mutate(&changed)
		if validateLocalhostGSITrust(changed) == nil {
			t.Fatalf("trust mutation %d accepted", i)
		}
	}
}

func TestSafeRootRejectsSymlinkComponentsAndCleanupTargets(t *testing.T) {
	base, err := os.MkdirTemp("/var/tmp", "dot65-symlink-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Fatal(err)
	}
	intermediate := filepath.Join(base, "intermediate")
	if err := os.Symlink(target, intermediate); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{rootLink, filepath.Join(intermediate, "child"), dangling, filepath.Join(dangling, "child")} {
		if _, err := safeRoot(candidate, filepath.Join(base, "repo")); err == nil {
			t.Fatalf("symlink root accepted: %s", candidate)
		}
	}
	if _, err := safeRoot(filepath.Join(base, "ordinary", "child"), filepath.Join(base, "repo")); err != nil {
		t.Fatalf("ordinary root rejected: %v", err)
	}
}

func TestOwnedRootWriteAndCleanupRejectPostValidationSwap(t *testing.T) {
	base, err := os.MkdirTemp("/var/tmp", "dot65-owned-swap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	protected := filepath.Join(base, "protected")
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(protected, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(base, "evidence-root")
	lease, err := acquireFreshRoot(rootPath, filepath.Join(base, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := rootMkdirAll(filepath.Join(rootPath, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(rootPath, "evidence")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, filepath.Join(rootPath, "evidence")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(rootPath, "evidence", "escaped"), []byte("bad")); err == nil {
		t.Fatal("swapped write component accepted")
	}
	if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "preserve" {
		t.Fatal("protected write target changed")
	}
	if err := os.Remove(filepath.Join(rootPath, "evidence")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	moved := rootPath + ".moved"
	if err := os.Rename(rootPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, rootPath); err != nil {
		t.Fatal(err)
	}
	if err := lease.removeAll(); err == nil {
		t.Fatal("swapped cleanup root accepted")
	}
	if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "preserve" {
		t.Fatal("protected cleanup target changed")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatal("owned root was deleted after path swap")
	}
}

func TestHumanInstructionIsExactlyFourBoundedActions(t *testing.T) {
	for _, marker := range []string{"1. Manually launch", "2. After the agent reports ARMED", "3. Execute the prescribed operator script", "4. Remain through normal post-game", "before 0:00", "maximum 30 minutes", "Stop and abort"} {
		if !strings.Contains(HumanInstruction, marker) {
			t.Fatalf("missing instruction marker %q", marker)
		}
	}
	if strings.Contains(strings.ToLower(HumanInstruction), "install") || strings.Contains(strings.ToLower(HumanInstruction), "troubleshoot") {
		t.Fatal("human payload contains preparation or troubleshooting")
	}
}

func TestProductListenerOwnershipAndBindReason(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().String()
	if err := proveProductListener(address, os.Getpid()); err != nil {
		t.Fatalf("owned listener rejected: %v", err)
	}
	other := exec.Command("sleep", "10")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	if err := proveProductListener(address, other.Process.Pid); err == nil {
		t.Fatal("non-owned conflicting listener accepted")
	}
	if err := proveProductListener("127.0.0.1:not-a-port", os.Getpid()); err == nil || !strings.Contains(err.Error(), "other than EADDRINUSE") {
		t.Fatalf("non-EADDR bind error accepted: %v", err)
	}
}

func TestProcessCorrelationRejectsRenamedLateProducerAndDrift(t *testing.T) {
	fixture := startProcessCorrelationFixture(t)
	defer fixture.close()

	// A child owned by the outer test runner must not contaminate the fixture's
	// invocation tree. This is the condition that made the complete preflight
	// flaky when the shared m4match.test process was used as the harness.
	outerChild := exec.Command("sleep", "30")
	if err := outerChild.Start(); err != nil {
		t.Fatal(err)
	}
	defer stopTestProcess(outerChild)

	start, err := captureProcessCorrelation(fixture.command.Process.Pid, fixture.productPID, fixture.obsPID)
	if err != nil {
		t.Fatal(err)
	}
	renamedPath := filepath.Join(t.TempDir(), "renamed-source")
	if err := os.Symlink("/usr/bin/sleep", renamedPath); err != nil {
		t.Fatal(err)
	}
	fixture.request("start-renamed " + renamedPath)
	if _, err := captureProcessCorrelation(fixture.command.Process.Pid, fixture.productPID, fixture.obsPID); err == nil {
		t.Fatal("late renamed producer accepted")
	}
	fixture.request("stop-extra")
	fixture.request("start-unexpected")
	if _, err := captureProcessCorrelation(fixture.command.Process.Pid, fixture.productPID, fixture.obsPID); err == nil {
		t.Fatal("unexpected child inside dedicated fixture accepted")
	}
	fixture.request("stop-extra")
	fixture.obsPID = fixture.requestPID("restart-obs")
	end, err := captureProcessCorrelation(fixture.command.Process.Pid, fixture.productPID, fixture.obsPID)
	if err != nil {
		t.Fatal(err)
	}
	if end.SHA256 == start.SHA256 {
		t.Fatal("process-tree drift was not observable")
	}
	if _, err := captureProcessCorrelation(fixture.command.Process.Pid, 99999999, fixture.obsPID); err == nil {
		t.Fatal("unreadable /proc identity accepted")
	}
}

type processCorrelationFixture struct {
	t          *testing.T
	command    *exec.Cmd
	input      io.WriteCloser
	output     *bufio.Scanner
	productPID int
	obsPID     int
}

func startProcessCorrelationFixture(t *testing.T) *processCorrelationFixture {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestProcessCorrelationFixtureParent$")
	command.Env = append(os.Environ(), "DOT65_PROCESS_CORRELATION_FIXTURE=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	fixture := &processCorrelationFixture{t: t, command: command, input: input, output: bufio.NewScanner(stdout)}
	fixture.productPID = fixture.readPID("product")
	fixture.obsPID = fixture.readPID("obs")
	return fixture
}

func (fixture *processCorrelationFixture) readLine() string {
	fixture.t.Helper()
	if !fixture.output.Scan() {
		fixture.t.Fatalf("process-correlation fixture stopped: %v", fixture.output.Err())
	}
	return fixture.output.Text()
}

func (fixture *processCorrelationFixture) readPID(kind string) int {
	fixture.t.Helper()
	fields := strings.Fields(fixture.readLine())
	if len(fields) != 2 || fields[0] != kind {
		fixture.t.Fatalf("unexpected process-correlation fixture response: %q", strings.Join(fields, " "))
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		fixture.t.Fatal(err)
	}
	return pid
}

func (fixture *processCorrelationFixture) request(command string) {
	fixture.t.Helper()
	if _, err := fmt.Fprintln(fixture.input, command); err != nil {
		fixture.t.Fatal(err)
	}
	if response := fixture.readLine(); response != "ok" {
		fixture.t.Fatalf("process-correlation fixture command %q returned %q", command, response)
	}
}

func (fixture *processCorrelationFixture) requestPID(command string) int {
	fixture.t.Helper()
	if _, err := fmt.Fprintln(fixture.input, command); err != nil {
		fixture.t.Fatal(err)
	}
	return fixture.readPID("obs")
}

func (fixture *processCorrelationFixture) close() {
	fixture.t.Helper()
	if fixture.command.ProcessState != nil {
		return
	}
	fixture.request("stop")
	_ = fixture.input.Close()
	if err := fixture.command.Wait(); err != nil {
		fixture.t.Errorf("process-correlation fixture shutdown: %v", err)
	}
}

func stopTestProcess(command *exec.Cmd) {
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
}

func TestProcessCorrelationFixtureParent(t *testing.T) {
	if os.Getenv("DOT65_PROCESS_CORRELATION_FIXTURE") != "1" {
		return
	}
	start := func(path string) *exec.Cmd {
		command := exec.Command(path, "30")
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		return command
	}
	product, obs := start("/usr/bin/sleep"), start("/usr/bin/sleep")
	var extra *exec.Cmd
	defer stopTestProcess(product)
	defer stopTestProcess(obs)
	defer stopTestProcess(extra)
	fmt.Printf("product %d\nobs %d\n", product.Process.Pid, obs.Process.Pid)

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			t.Fatal("empty fixture command")
		}
		switch fields[0] {
		case "start-renamed":
			if len(fields) != 2 || extra != nil {
				t.Fatal("invalid start-renamed fixture command")
			}
			extra = start(fields[1])
			fmt.Println("ok")
		case "start-unexpected":
			if len(fields) != 1 || extra != nil {
				t.Fatal("invalid start-unexpected fixture command")
			}
			extra = start("/usr/bin/sleep")
			fmt.Println("ok")
		case "stop-extra":
			stopTestProcess(extra)
			extra = nil
			fmt.Println("ok")
		case "restart-obs":
			stopTestProcess(obs)
			obs = start("/usr/bin/sleep")
			fmt.Printf("obs %d\n", obs.Process.Pid)
		case "stop":
			fmt.Println("ok")
			return
		default:
			t.Fatalf("unknown fixture command %q", fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestArmingDeadlineAndImmediatePostArmedFrame(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	deadline := time.Now().Add(30 * time.Millisecond)
	if _, err := readLineBefore(context.Background(), reader, deadline); err == nil {
		t.Fatal("expired confirmation did not abort")
	}
	armed := time.Now().UTC()
	received := armed.Add(time.Millisecond)
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	payload := []byte(`{"map":{"clock_time":-10,"game_state":"DOTA_GAMERULES_STATE_PRE_GAME","matchid":123}}`)
	frame := fmt.Sprintf(`{"sequence":1,"received_at":%q,"raw_base64":%q}`+"\n", received.Format(time.RFC3339Nano), base64.StdEncoding.EncodeToString(payload))
	if err := os.WriteFile(path, []byte(frame), 0o600); err != nil {
		t.Fatal(err)
	}
	boundary, err := inspectBoundaryRecords(path, LiveIdentity{MatchID: "123"}, liveBoundary{ArmedAt: armed, IdentityContinuous: true})
	if err != nil || !boundary.Pregame {
		t.Fatalf("post-ARMED pre-confirmation frame rejected: %+v %v", boundary, err)
	}
}

func TestVerifierRejectsRetainedV2Readiness(t *testing.T) {
	root, err := os.MkdirTemp("/var/tmp", "dot65-old-v2-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := Readiness{SchemaVersion: "m4_match_readiness.v2", Mode: "preflight", HumanInstruction: HumanInstruction}
	payload, err := canonical(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evidence/readiness.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), root, filepath.Join(root, "repo"), "preflight"); err == nil || !strings.Contains(err.Error(), "readiness contract mismatch") {
		t.Fatalf("old readiness accepted: %v", err)
	}
}
