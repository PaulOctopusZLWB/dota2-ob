package m4match

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestRehearsalFailedAttemptVerifiesTokenGatesAndCleans(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	root := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {})
	preflight, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
	if err != nil || preflight.ConsoleState != RehearsalReady {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) {
		return RehearsalDotaIdentityV1{}, errors.New("no physical dota")
	}})
	if err != nil || result.Outcome != "rehearsal_failed" || result.FailureCode != "dota_identity_unavailable" || result.CleanupToken == "" {
		t.Fatalf("attempt=%#v err=%v", result, err)
	}
	verified, err := VerifyRehearsal(context.Background(), root, repo)
	if err != nil || verified != result {
		t.Fatalf("verified=%#v err=%v", verified, err)
	}
	if err := CleanupRehearsal(context.Background(), root, repo, "wrong-token"); err == nil {
		t.Fatal("wrong cleanup token accepted")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("wrong token removed evidence")
	}
	if err := CleanupRehearsal(context.Background(), root, repo, result.CleanupToken); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root still exists: %v", err)
	}
}

func TestRehearsalRefusalTerminalizesDeterministicallyAndNeverShowsReadyInstruction(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	var terminals []string
	for index := 0; index < 2; index++ {
		root := freshVarTmp(t)
		withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {
			probe.remoteHead = ""
			if index == 1 {
				probe.draftPR, probe.prHead = false, ""
			}
		})
		preflight, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
		if err != nil || preflight.ConsoleState != RehearsalRefused || preflight.HumanInstruction != "" {
			t.Fatalf("refusal=%#v err=%v", preflight, err)
		}
		result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo})
		if err != nil || result.FailureCode != "preflight_refused" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if _, err := VerifyRehearsal(context.Background(), root, repo); err != nil {
			t.Fatal(err)
		}
		terminal, _ := os.ReadFile(filepath.Join(root, "rehearsal", "terminal.json"))
		terminals = append(terminals, string(terminal))
		if err := CleanupRehearsal(context.Background(), root, repo, result.CleanupToken); err != nil {
			t.Fatal(err)
		}
	}
	if terminals[0] == terminals[1] {
		t.Fatal("remote-only and remote+draft refusal were not distinguished")
	}
}

func TestRehearsalFreshRootsAreByteDeterministicWithinClass(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	var preflights, terminals [][]byte
	var tokens []string
	for range 2 {
		root := freshVarTmp(t)
		withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {})
		if _, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo}); err != nil {
			t.Fatal(err)
		}
		result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) { return RehearsalDotaIdentityV1{}, errors.New("absent") }})
		if err != nil {
			t.Fatal(err)
		}
		preflight, _ := os.ReadFile(filepath.Join(root, "rehearsal", "preflight.json"))
		terminal, _ := os.ReadFile(filepath.Join(root, "rehearsal", "terminal.json"))
		preflights, terminals, tokens = append(preflights, preflight), append(terminals, terminal), append(tokens, result.CleanupToken)
		if err := CleanupRehearsal(context.Background(), root, repo, result.CleanupToken); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(preflights[0], preflights[1]) || !reflect.DeepEqual(terminals[0], terminals[1]) || tokens[0] != tokens[1] {
		t.Fatal("fresh roots differ")
	}
}

func TestRehearsalVerifierRejectsTamperAndFreshListenerMutation(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	root := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {})
	_, _ = RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
	result, _ := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) { return RehearsalDotaIdentityV1{}, errors.New("absent") }})
	listener, err := net.Listen("tcp", CaptureAddress)
	if err != nil {
		t.Skipf("capture listener unavailable: %v", err)
	}
	if _, err := VerifyRehearsal(context.Background(), root, repo); err == nil {
		t.Fatal("occupied fresh listener accepted")
	}
	_ = listener.Close()
	terminalPath := filepath.Join(root, "rehearsal", "terminal.json")
	payload, _ := os.ReadFile(terminalPath)
	payload[0] = '['
	if err := os.WriteFile(terminalPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRehearsal(context.Background(), root, repo); err == nil {
		t.Fatal("tampered terminal accepted")
	}
	_ = os.RemoveAll(root)
	_ = result
}

func TestExactRehearsalCheckRegistryRejectsDeletionExtraAndReorder(t *testing.T) {
	checks := make([]RehearsalCheckV1, 0, len(rehearsalCheckRegistry))
	for _, id := range rehearsalCheckRegistry {
		checks = append(checks, RehearsalCheckV1{ID: id, Passed: true, Code: "ok"})
	}
	base := RehearsalPreflightV1{SchemaVersion: RehearsalPreflightSchemaV1, Purpose: RehearsalPurpose, MatchClass: RehearsalMatchClass, AcceptedSpec: AcceptedRehearsalSpec, AcceptedP4Spec: AcceptedP4Spec, ConsoleState: RehearsalReady, HumanInstruction: RehearsalInstruction, Checks: checks, AcceptanceGate: "none", RootOwnerSHA256: stringsOf('a'), ArmSHA256: stringsOf('b'), GoExecutable: "/bound/go", GoExecutableSHA256: stringsOf('c')}
	if err := validateRehearsalPreflight(base); err != nil {
		t.Fatal(err)
	}
	attacks := []func(*RehearsalPreflightV1){
		func(v *RehearsalPreflightV1) { v.Checks = v.Checks[1:] },
		func(v *RehearsalPreflightV1) {
			v.Checks = append(v.Checks, RehearsalCheckV1{ID: "invented", Passed: true, Code: "ok"})
		},
		func(v *RehearsalPreflightV1) { v.Checks[0], v.Checks[1] = v.Checks[1], v.Checks[0] },
		func(v *RehearsalPreflightV1) { v.Checks[0] = v.Checks[1] },
	}
	for index, attack := range attacks {
		candidate := base
		candidate.Checks = append([]RehearsalCheckV1(nil), base.Checks...)
		attack(&candidate)
		if validateRehearsalPreflight(candidate) == nil {
			t.Fatalf("registry attack %d accepted", index)
		}
	}
}

func TestRehearsalSuccessorArtifactsRotateWithoutChangingP4Artifacts(t *testing.T) {
	_, p4Lineage, p4Release, err := liveArtifacts("m4-live-only")
	if err != nil {
		t.Fatal(err)
	}
	_, successor, release, err := rehearsalSuccessorArtifacts("m4-live-only")
	if err != nil {
		t.Fatal(err)
	}
	successorRelease, _ := release.ContentID()
	p4ReleaseID, _ := p4Release.ContentID()
	if p4Lineage.MustContentID() != "a256e20aa0c9f7ef457ad4e9b3a2cfc8ec5d83af18fb919490955c0412ed2585" || p4ReleaseID != "3ee8b789a4046b9adc2aff2d13958663831b7e134329b4d83cea6700673edccc" {
		t.Fatal("accepted P4 identities moved")
	}
	if successor.EngineBuild.ContentSHA256 != "3bd1ee86a2b67062f28db39559d31e92ef4f0e2ae73caaf954e01ffe9a2c99a3" || successor.MustContentID() != "d21a69b05e8e9ae5e83a7b40af6d4223eec76f2bde5a3a857d8a45b1ff5ac342" || successorRelease != "161049527d0c48fd5adf50ea935b605aa4670785000a720f619fbe24ba36cd7c" {
		t.Fatalf("rehearsal successor identities engine=%s lineage=%s release=%s", successor.EngineBuild.ContentSHA256, successor.MustContentID(), successorRelease)
	}
}

func TestRehearsalAttemptRejectsAdjacentSequenceIdentityDriftThenRestore(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	root := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {})
	preflight, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(filepath.Join(root, "data", "sessions"), session.WithSessionID(preflight.SessionID), session.WithClock(func() time.Time { return time.Unix(1, 0).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := store.Append([]byte(`{"provider":{"name":"Dota 2"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	bound := RehearsalDotaIdentityV1{SchemaVersion: "rehearsal_dota_identity.v1", PID: 7, Comm: "dota2", ExecutablePath: "/game/dota2", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 9, ExecutableDevice: 1, ExecutableInode: 2}
	drift := bound
	drift.ExecutablePath = "/replacement/dota2"
	drift.ExecutablePathSHA256 = stringsOf('c')
	observed := []RehearsalDotaIdentityV1{bound, bound, bound, drift, bound, bound}
	index := 0
	result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) {
		value := observed[index]
		index++
		return value, nil
	}, producer: interleavedIdentityTestDriver{}})
	if err != nil || result.Outcome != "rehearsal_failed" || result.FailureCode != "raw_admission_failed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var terminal RehearsalTerminalV1
	payload, readErr := os.ReadFile(filepath.Join(root, "rehearsal", "terminal.json"))
	if readErr != nil || json.Unmarshal(payload, &terminal) != nil {
		t.Fatalf("read terminal: %v", readErr)
	}
	if terminal.RawAdmission.AcceptedRecords != 2 || terminal.RawAdmission.Failure.ConcurrentChange != "producer_dota_continuity_invalid" || len(terminal.Process) < 3 || terminal.Process[2].RawSequence != 2 || terminal.Process[2].ObservedIdentity.ExecutablePath != drift.ExecutablePath {
		t.Fatalf("terminal=%#v", terminal)
	}
}

type interleavedIdentityTestDriver struct{}

func (interleavedIdentityTestDriver) Run(_ context.Context, run rehearsalLifecycleContext) (rehearsalLifecycleReport, error) {
	report := rehearsalLifecycleReport{sourceMode: "physical_public_match", physicalMatch: true}
	start, err := run.identity()
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "producer_start", ObservedIdentity: start})
	if err != nil || !sameRehearsalDotaIdentity(run.boundDota, start) {
		return report, errors.New("producer start drift")
	}
	rawPath := filepath.Join(run.root, "data/sessions", run.preflight.SessionID, "raw.jsonl")
	admitted, admissionErr := admitProducerRawIdentities(rawPath, run.preflight.SessionID, run.boundDota, run.identity, 0, &report.dotaContinuity)
	capture, _ := run.identity()
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "capture_terminal", RawSequence: admitted, ObservedIdentity: capture})
	terminal, _ := run.identity()
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "producer_terminal", RawSequence: admitted, ObservedIdentity: terminal})
	return report, admissionErr
}

func TestRehearsalMalformedRawFailureReceiptVerifiesAndCleans(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	root := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {})
	preflight, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(root, "data", "sessions", preflight.SessionID, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte("unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := RehearsalDotaIdentityV1{SchemaVersion: "rehearsal_dota_identity.v1", PID: 7, Comm: "dota2", ExecutablePath: "/game/dota2", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 9, ExecutableDevice: 1, ExecutableInode: 2}
	result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) { return identity, nil }})
	if err != nil || result.FailureCode != "raw_admission_failed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := VerifyRehearsal(context.Background(), root, repo); err != nil {
		t.Fatal(err)
	}
	if err := CleanupRehearsal(context.Background(), root, repo, result.CleanupToken); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("failed root remains: %v", err)
	}
}

func TestP4AndRehearsalRootsRejectEachOther(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	rehearsalRoot := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(probe *rehearsalPreflightProbe) {})
	if _, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: rehearsalRoot, RepoRoot: repo}); err != nil {
		t.Fatal(err)
	}
	result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: rehearsalRoot, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) { return RehearsalDotaIdentityV1{}, errors.New("absent") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), rehearsalRoot, repo, "preflight"); err == nil {
		t.Fatal("P4 verifier accepted rehearsal root")
	}
	if err := CleanupRehearsal(context.Background(), rehearsalRoot, repo, result.CleanupToken); err != nil {
		t.Fatal(err)
	}
	p4ShapedRoot := freshVarTmp(t)
	if err := os.MkdirAll(filepath.Join(p4ShapedRoot, "evidence", "canonical"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p4ShapedRoot, "evidence", "canonical", "evidence-index.json"), []byte(`{"purpose":"ti_public_tournament"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRehearsal(context.Background(), p4ShapedRoot, repo); err == nil {
		t.Fatal("rehearsal verifier accepted P4-shaped root")
	}
}

func stringsOf(value byte) string { return string(bytes.Repeat([]byte{value}, 64)) }

func withRehearsalProbe(t *testing.T, repo string, mutate func(*rehearsalPreflightProbe)) {
	t.Helper()
	head, _ := runText(context.Background(), repo, "git", "rev-parse", "HEAD")
	goExecutable, goSHA, _ := resolveRehearsalGo()
	probe := rehearsalPreflightProbe{commit: head, parent: requiredRehearsalParent, branch: "agent/dota2-fullstack-engineer/DOT-84-test", remoteHead: head, prHead: head, goExecutable: goExecutable, goExecutableSHA256: goSHA, clean: true, ancestry: true, fixture: true, listener: true, toolchain: true, draftPR: true}
	mutate(&probe)
	prior := rehearsalPreflightInspector
	rehearsalPreflightInspector = func(context.Context, string) rehearsalPreflightProbe { return probe }
	t.Cleanup(func() { rehearsalPreflightInspector = prior })
}

func freshVarTmp(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/var/tmp", "dot84-rehearsal-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
