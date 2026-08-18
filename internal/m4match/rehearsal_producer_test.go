package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type helperProcessRehearsalDriver struct{}

func (helperProcessRehearsalDriver) Run(ctx context.Context, run rehearsalLifecycleContext) (rehearsalLifecycleReport, error) {
	steps := make([]RehearsalProducerStepV1, 0, len(requiredProducerSteps))
	for _, name := range requiredProducerSteps {
		steps = append(steps, RehearsalProducerStepV1{Name: name, Started: true, Completed: true, ExitCode: 0})
	}
	report := rehearsalLifecycleReport{sourceMode: "hermetic_helper", steps: steps, operatorActions: []string{contracts.ActionApprove, contracts.ActionReject, contracts.ActionPin, contracts.ActionUnpin, contracts.ActionEmergencyHide, contracts.ActionClearEmergencyHide}, resourceSamples: 2, visibilitySamples: 2, rawRecords: 2, recordingFinalized: true, reconciled: true, recoveryByteEqual: true, cleanShutdown: true}
	for _, role := range []string{"product", "obs", "recovery"} {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRehearsalLifecycleHelperProcess", "--", role, run.root, run.preflight.SessionID)
		command.Env = append(os.Environ(), "DOT84_HELPER_PROCESS=1")
		if err := command.Start(); err != nil {
			return report, err
		}
		switch role {
		case "product":
			report.productPID = command.Process.Pid
		case "obs":
			report.obsPID = command.Process.Pid
		case "recovery":
			report.recoveryPID = command.Process.Pid
		}
		if err := command.Wait(); err != nil {
			return report, err
		}
	}
	return report, errors.New("hermetic lifecycle is evidence only, not a physical match")
}

func TestRehearsalLifecycleHelperProcess(t *testing.T) {
	if os.Getenv("DOT84_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	separator := 0
	for index, value := range args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator == 0 || len(args) != separator+4 {
		os.Exit(2)
	}
	role, root, sessionID := args[separator+1], args[separator+2], args[separator+3]
	if role == "product" {
		store, err := session.NewStore(filepath.Join(root, "data/sessions"), session.WithSessionID(sessionID), session.WithClock(func() time.Time { return time.Unix(10, 0).UTC() }))
		if err != nil {
			os.Exit(3)
		}
		for _, payload := range [][]byte{[]byte(`{"provider":{"name":"Dota 2"},"map":{"clock_time":-1}}`), []byte(`{"provider":{"name":"Dota 2"},"map":{"clock_time":0}}`)} {
			if _, err := store.Append(payload); err != nil {
				os.Exit(4)
			}
		}
		if store.Close() != nil {
			os.Exit(5)
		}
		_ = os.MkdirAll(filepath.Join(root, "evidence/logs"), 0o700)
		_ = os.WriteFile(filepath.Join(root, "evidence/logs/product-live.log"), []byte("helper product ready\n"), 0o600)
		_ = os.WriteFile(filepath.Join(root, "evidence/samples.jsonl"), []byte("{}\n{}\n"), 0o600)
		_ = os.WriteFile(filepath.Join(root, "evidence/visibility.jsonl"), []byte("{}\n{}\n"), 0o600)
		os.Exit(0)
	}
	if role == "obs" {
		_ = os.MkdirAll(filepath.Join(root, "recordings"), 0o700)
		_ = os.WriteFile(filepath.Join(root, "evidence/logs/obs-live.log"), []byte("helper obs finalized\n"), 0o600)
		_ = os.WriteFile(filepath.Join(root, "recordings/helper.mkv"), []byte{0x1a, 0x45, 0xdf, 0xa3}, 0o600)
		os.Exit(0)
	}
	if role == "recovery" {
		os.Exit(0)
	}
	os.Exit(2)
}

func TestHermeticHelperExercisesProducerOwnedLifecycleWithoutPhysicalClaim(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	root := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(*rehearsalPreflightProbe) {})
	if _, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo}); err != nil {
		t.Fatal(err)
	}
	identity := RehearsalDotaIdentityV1{SchemaVersion: "rehearsal_dota_identity.v1", PID: 7, Comm: "dota2", ExecutablePath: "/game/dota2", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 9, ExecutableDevice: 1, ExecutableInode: 2}
	result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) { return identity, nil }, producer: helperProcessRehearsalDriver{}})
	if err != nil || result.Outcome != "rehearsal_failed" || result.FailureCode != "producer_lifecycle_failed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	verified, err := VerifyRehearsal(context.Background(), root, repo)
	if err != nil || verified != result {
		t.Fatalf("verified=%#v err=%v", verified, err)
	}
	var terminal RehearsalTerminalV1
	payload, err := os.ReadFile(filepath.Join(root, "rehearsal/terminal.json"))
	if err != nil || json.Unmarshal(payload, &terminal) != nil {
		t.Fatal("terminal unavailable")
	}
	if terminal.Producer == nil || terminal.Producer.SourceMode != "hermetic_helper" || terminal.Producer.PhysicalMatch || len(terminal.Producer.Steps) != len(requiredProducerSteps) {
		t.Fatalf("producer=%#v", terminal.Producer)
	}
	if err := CleanupRehearsal(context.Background(), root, repo, result.CleanupToken); err != nil {
		t.Fatal(err)
	}
}

func TestSixCallerAuthoredSessionStubsCannotCompleteAndRemainCleanable(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	root := freshVarTmp(t)
	withRehearsalProbe(t, repo, func(*rehearsalPreflightProbe) {})
	preflight, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	producerDir := filepath.Join(root, "rehearsal", "producer")
	if err := os.MkdirAll(producerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"capture-seal.json", "operator-evidence.json", "obs-finalization.json", "resource-samples.json", "recovery-proof.json", "reconciliation.json"} {
		if err := os.WriteFile(filepath.Join(producerDir, name), []byte(`{"session_id":"`+preflight.SessionID+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	identity := RehearsalDotaIdentityV1{SchemaVersion: "rehearsal_dota_identity.v1", PID: 7, Comm: "dota2", ExecutablePath: "/game/dota2", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 9, ExecutableDevice: 1, ExecutableInode: 2}
	result, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, identity: func() (RehearsalDotaIdentityV1, error) { return identity, nil }})
	if err != nil || result.Outcome != "rehearsal_failed" || result.FailureCode != "zero_frame_attempt" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var terminal RehearsalTerminalV1
	payload, err := os.ReadFile(filepath.Join(root, "rehearsal/terminal.json"))
	if err != nil || json.Unmarshal(payload, &terminal) != nil {
		t.Fatal("terminal unavailable")
	}
	if terminal.Producer == nil || terminal.Producer.SourceMode != "physical_public_match" || terminal.Producer.PhysicalMatch {
		t.Fatalf("producer=%#v", terminal.Producer)
	}
	if _, err := VerifyRehearsal(context.Background(), root, repo); err != nil {
		t.Fatal(err)
	}
	if err := CleanupRehearsal(context.Background(), root, repo, result.CleanupToken); err != nil {
		t.Fatal(err)
	}
}
