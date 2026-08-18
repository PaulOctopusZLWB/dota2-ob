package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	startIdentity, err := run.identity()
	if err != nil {
		return report, err
	}
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "producer_start", ObservedIdentity: startIdentity})
	var admitted uint64
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
		if role == "product" {
			admitted, err = admitProducerRawIdentities(filepath.Join(run.root, "data/sessions", run.preflight.SessionID, "raw.jsonl"), run.preflight.SessionID, run.boundDota, run.identity, admitted, &report.dotaContinuity)
			if err != nil {
				return report, err
			}
		}
	}
	captureIdentity, err := run.identity()
	if err != nil {
		return report, err
	}
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "capture_terminal", RawSequence: admitted, ObservedIdentity: captureIdentity})
	terminalIdentity, err := run.identity()
	if err != nil {
		return report, err
	}
	report.dotaContinuity = append(report.dotaContinuity, RehearsalProcessObservationV1{Boundary: "producer_terminal", RawSequence: admitted, ObservedIdentity: terminalIdentity})
	return report, errors.New("hermetic lifecycle is evidence only, not a physical match")
}

func TestProducerDotaContinuityIsCausalAndRawHashBound(t *testing.T) {
	bound := RehearsalDotaIdentityV1{SchemaVersion: "rehearsal_dota_identity.v1", PID: 7, Comm: "dota2", ExecutablePath: "/game/dota2", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 9, ExecutableDevice: 1, ExecutableInode: 2}
	records := []session.RawRecordAttestationV1{{Sequence: 1, RawRecordSHA256: stringsOf('c')}, {Sequence: 2, RawRecordSHA256: stringsOf('d')}}
	valid := []RehearsalProcessObservationV1{
		{Boundary: "producer_start", ObservedIdentity: bound},
		{Boundary: "raw_admission", RawSequence: 1, RawRecordSHA256: records[0].RawRecordSHA256, ObservedIdentity: bound},
		{Boundary: "raw_admission", RawSequence: 2, RawRecordSHA256: records[1].RawRecordSHA256, ObservedIdentity: bound},
		{Boundary: "capture_terminal", RawSequence: 2, ObservedIdentity: bound},
		{Boundary: "producer_terminal", RawSequence: 2, ObservedIdentity: bound},
	}
	if err := validateProducerDotaContinuity(valid, bound, records, true); err != nil {
		t.Fatal(err)
	}
	clone := func() []RehearsalProcessObservationV1 { return append([]RehearsalProcessObservationV1(nil), valid...) }
	attacks := map[string]func([]RehearsalProcessObservationV1) []RehearsalProcessObservationV1{
		"missing": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			return append(v[:2], v[3:]...)
		},
		"duplicate": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			return append(v[:2], append([]RehearsalProcessObservationV1{v[1]}, v[2:]...)...)
		},
		"reordered": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[1], v[2] = v[2], v[1]
			return v
		},
		"spliced_hash": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[2].RawRecordSHA256 = stringsOf('e')
			return v
		},
		"drift_then_restore": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[2].ObservedIdentity.ProcessStartTicks++
			return v
		},
		"path_drift": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[1].ObservedIdentity.ExecutablePath = "/other/dota2"
			return v
		},
		"content_drift": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[1].ObservedIdentity.ExecutableSHA256 = stringsOf('f')
			return v
		},
		"device_drift": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[1].ObservedIdentity.ExecutableDevice++
			return v
		},
		"inode_drift": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[1].ObservedIdentity.ExecutableInode++
			return v
		},
		"capture_terminal_drift": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[3].ObservedIdentity.PID++
			return v
		},
		"producer_terminal_drift": func(v []RehearsalProcessObservationV1) []RehearsalProcessObservationV1 {
			v[4].ObservedIdentity.PID++
			return v
		},
	}
	for name, attack := range attacks {
		t.Run(name, func(t *testing.T) {
			if err := validateProducerDotaContinuity(attack(clone()), bound, records, true); err == nil {
				t.Fatal("causally invalid continuity accepted")
			}
		})
	}
}

func TestRecoveryProcessLifecycleRejectsResealedSubstitution(t *testing.T) {
	identity := RehearsalOwnedProcessIdentityV1{SchemaVersion: "rehearsal_owned_process_identity.v1", PID: 101, Comm: "dota2-ob", ExecutablePath: "/owned/dota2-ob", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 22, ExecutableDevice: 3, ExecutableInode: 4}
	valid := []RehearsalOwnedProcessObservationV1{{Boundary: "recovery_start", Identity: identity}, {Boundary: "recovery_terminal", Identity: identity}}
	if err := validateRecoveryProcessPopulation(identity.PID, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]RehearsalOwnedProcessObservationV1){
		"omitted":     func(v []RehearsalOwnedProcessObservationV1) { v[1].Boundary = "" },
		"pid":         func(v []RehearsalOwnedProcessObservationV1) { v[1].Identity.PID++ },
		"path":        func(v []RehearsalOwnedProcessObservationV1) { v[1].Identity.ExecutablePath += ".substitute" },
		"content":     func(v []RehearsalOwnedProcessObservationV1) { v[1].Identity.ExecutableSHA256 = stringsOf('c') },
		"start_ticks": func(v []RehearsalOwnedProcessObservationV1) { v[1].Identity.ProcessStartTicks++ },
		"device":      func(v []RehearsalOwnedProcessObservationV1) { v[1].Identity.ExecutableDevice++ },
		"inode":       func(v []RehearsalOwnedProcessObservationV1) { v[1].Identity.ExecutableInode++ },
	} {
		t.Run(name, func(t *testing.T) {
			copy := append([]RehearsalOwnedProcessObservationV1(nil), valid...)
			mutate(copy)
			if err := validateRecoveryProcessPopulation(identity.PID, copy); err == nil {
				t.Fatal("substituted recovery process accepted")
			}
		})
	}
}

func TestRetainedRunIdentityAndJSONLPopulationAreIndependentlyDerived(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rehearsal"), 0o700); err != nil {
		t.Fatal(err)
	}
	owner := RehearsalRootOwnerV1{SchemaVersion: "rehearsal_root_owner.v1", Purpose: RehearsalPurpose, SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RawRelativePath: "data/sessions/session/raw.jsonl"}
	ownerSHA, _ := payloadSHAFromCanonical(owner)
	preflight := RehearsalPreflightV1{SessionID: owner.SessionID, CandidateCommit: owner.CandidateCommit, PreflightSHA256: stringsOf('b'), RootOwnerSHA256: ownerSHA}
	if err := writeJSON(filepath.Join(root, "rehearsal/owner.json"), owner, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, runID, err := deriveRetainedProducerRunIdentity(root, preflight); err != nil || !validLowerSHA256(runID) {
		t.Fatalf("run=%q err=%v", runID, err)
	}
	owner.SessionID = "other"
	if err := writeJSON(filepath.Join(root, "rehearsal/owner.json"), owner, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deriveRetainedProducerRunIdentity(root, preflight); err == nil {
		t.Fatal("cross-run owner accepted")
	}
	jsonl := filepath.Join(root, "samples.jsonl")
	if err := os.WriteFile(jsonl, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := countCanonicalJSONL(jsonl, 2); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := countCanonicalJSONL(jsonl, 1); err == nil {
		t.Fatal("population cap bypassed")
	}
	if err := os.WriteFile(jsonl, []byte("{}\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := countCanonicalJSONL(jsonl, 2); err == nil {
		t.Fatal("changed population accepted after hash-independent recount")
	}
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
