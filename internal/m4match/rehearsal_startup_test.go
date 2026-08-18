//go:build linux

package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveRehearsalGoWithServicePATH(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	path, hash, err := resolveRehearsalGo()
	if err != nil || !filepath.IsAbs(path) || !validLowerSHA256(hash) {
		t.Fatalf("path=%q hash=%q err=%v", path, hash, err)
	}
}

func TestRehearsalCleanupQuarantinesOwnedEntryBeforeSubstitution(t *testing.T) {
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	priorTarget, priorHook := rehearsalGSITarget, rehearsalAfterGSIQuarantine
	rehearsalGSITarget = func() string { return target }
	t.Cleanup(func() { rehearsalGSITarget, rehearsalAfterGSIQuarantine = priorTarget, priorHook })
	preflight := RehearsalPreflightV1{SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RootOwnerSHA256: strings.Repeat("b", 64)}
	arm, err := armRehearsalGSI(root, preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.ArmSHA256, _ = rehearsalArmBindingID(arm)
	replacement := []byte("unrelated replacement survives\n")
	rehearsalAfterGSIQuarantine = func(path string) error {
		return os.WriteFile(path, replacement, 0o600)
	}
	if err := cleanupRehearsalArm(root, preflight, arm.ArmToken); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(target)
	if err != nil || string(payload) != string(replacement) {
		t.Fatalf("replacement=%q err=%v", payload, err)
	}
}

func TestRehearsalCleanupRetainsMismatchedQuarantineAndOccupiedTarget(t *testing.T) {
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	priorTarget, priorHook := rehearsalGSITarget, rehearsalAfterGSIQuarantine
	rehearsalGSITarget = func() string { return target }
	t.Cleanup(func() { rehearsalGSITarget, rehearsalAfterGSIQuarantine = priorTarget, priorHook })
	preflight := RehearsalPreflightV1{SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RootOwnerSHA256: strings.Repeat("b", 64)}
	arm, err := armRehearsalGSI(root, preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.ArmSHA256, _ = rehearsalArmBindingID(arm)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	a := []byte("unrelated entry A\n")
	b := []byte("unrelated entry B\n")
	if err := os.WriteFile(target, a, 0o600); err != nil {
		t.Fatal(err)
	}
	aBefore, err := inspectArmedGSI(target)
	if err != nil {
		t.Fatal(err)
	}
	rehearsalAfterGSIQuarantine = func(path string) error { return os.WriteFile(path, b, 0o600) }
	if err := cleanupRehearsalArm(root, preflight, arm.ArmToken); err == nil || !strings.Contains(err.Error(), "machine-checkable recovery retained") {
		t.Fatalf("cleanup error=%v", err)
	}
	var recovery RehearsalArmRecoveryV1
	payload, err := os.ReadFile(filepath.Join(root, "rehearsal", "arm-cleanup-recovery.json"))
	if err != nil || json.Unmarshal(payload, &recovery) != nil || recovery.State != rehearsalRecoveryRetained {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	aAfter, err := inspectArmedGSI(recovery.QuarantinePath)
	if err != nil || aAfter != aBefore || recovery.QuarantineDevice != aBefore.device || recovery.QuarantineInode != aBefore.inode {
		t.Fatalf("retained A=%+v before=%+v recovery=%+v err=%v", aAfter, aBefore, recovery, err)
	}
	bAfter, err := inspectArmedGSI(target)
	if err != nil || bAfter.hash != payloadSHA(b) || bAfter.bytes != int64(len(b)) || bAfter.device != recovery.OccupiedTargetDevice || bAfter.inode != recovery.OccupiedTargetInode {
		t.Fatalf("retained B=%+v recovery=%+v err=%v", bAfter, recovery, err)
	}
}

type rehearsalMissingStatT struct{ os.FileInfo }

func (rehearsalMissingStatT) Sys() any { return nil }

func TestRehearsalPostCreateIdentityFailuresNeverExposeConfiguredPath(t *testing.T) {
	for _, fault := range []string{"descriptor_stat", "missing_stat_t"} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replacement_%t", fault, replacement), func(t *testing.T) {
				root := t.TempDir()
				if _, err := Prepare(root, 1920, 1080); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
				priorTarget, priorStat := rehearsalGSITarget, rehearsalCreatedGSIStat
				rehearsalGSITarget = func() string { return target }
				replacementBytes := []byte("unrelated replacement remains exact\n")
				rehearsalCreatedGSIStat = func(file *os.File) (os.FileInfo, error) {
					if replacement {
						if err := os.WriteFile(target, replacementBytes, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if fault == "descriptor_stat" {
						return nil, errors.New("injected descriptor stat failure")
					}
					info, err := file.Stat()
					return rehearsalMissingStatT{FileInfo: info}, err
				}
				t.Cleanup(func() { rehearsalGSITarget, rehearsalCreatedGSIStat = priorTarget, priorStat })
				preflight := RehearsalPreflightV1{SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RootOwnerSHA256: strings.Repeat("b", 64)}
				if _, err := armRehearsalGSI(root, preflight); err == nil || !strings.Contains(err.Error(), "unpublished inode discarded") {
					t.Fatalf("arm error=%v", err)
				}
				if replacement {
					before, err := inspectArmedGSI(target)
					if err != nil || before.hash != payloadSHA(replacementBytes) || before.bytes != int64(len(replacementBytes)) {
						t.Fatalf("replacement changed: %+v err=%v", before, err)
					}
					if _, err := armRehearsalGSI(root, preflight); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
						t.Fatalf("later ARM did not preserve unrelated replacement: %v", err)
					}
					after, err := inspectArmedGSI(target)
					if err != nil || after != before {
						t.Fatalf("later ARM changed replacement: before=%+v after=%+v err=%v", before, after, err)
					}
					return
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("identity failure exposed configured path: %v", err)
				}
				rehearsalCreatedGSIStat = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
				arm, err := armRehearsalGSI(root, preflight)
				if err != nil {
					t.Fatal("later ARM remained wedged:", err)
				}
				preflight.ArmSHA256, _ = rehearsalArmBindingID(arm)
				if err := cleanupRehearsalArm(root, preflight, "wrong"); err == nil {
					t.Fatal("wrong cleanup token accepted")
				}
				if err := cleanupRehearsalArm(root, preflight, arm.ArmToken); err != nil {
					t.Fatal("token-gated cleanup failed:", err)
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("later ARM cleanup left target: %v", err)
				}
			})
		}
	}
}

func TestRemoveCreatedGSIRejectsZeroIdentity(t *testing.T) {
	target := filepath.Join(t.TempDir(), "unrelated.cfg")
	if err := os.WriteFile(target, []byte("unrelated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeCreatedGSI(target, 0, 0); err == nil {
		t.Fatal("zero identity cleanup was accepted")
	}
	after, err := os.Lstat(target)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("zero identity cleanup changed entry: %v", err)
	}
}

func TestRehearsalAnonymousPublishNeverOverwritesRacingEntry(t *testing.T) {
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	priorTarget, priorStat := rehearsalGSITarget, rehearsalCreatedGSIStat
	rehearsalGSITarget = func() string { return target }
	replacement := []byte("racing unrelated final entry\n")
	rehearsalCreatedGSIStat = func(file *os.File) (os.FileInfo, error) {
		if err := os.WriteFile(target, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
		return file.Stat()
	}
	t.Cleanup(func() { rehearsalGSITarget, rehearsalCreatedGSIStat = priorTarget, priorStat })
	preflight := RehearsalPreflightV1{SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RootOwnerSHA256: strings.Repeat("b", 64)}
	if _, err := armRehearsalGSI(root, preflight); err == nil || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("publication race error=%v", err)
	}
	identity, err := inspectArmedGSI(target)
	if err != nil || identity.hash != payloadSHA(replacement) || identity.bytes != int64(len(replacement)) {
		t.Fatalf("racing entry changed: %+v err=%v", identity, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "rehearsal", "arm-recovery.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed publication left installed recovery intent: %v", err)
	}
}

func TestRehearsalArmPostInstallFaultsRollbackOwnedGSI(t *testing.T) {
	repo, _ := os.Getwd()
	repo = filepath.Clean(filepath.Join(repo, "../.."))
	for _, point := range []string{"arm_content_id", "arm_record_write", "preflight_after_arm", "preflight_content_id", "preflight_json", "preflight_seal"} {
		t.Run(point, func(t *testing.T) {
			root := freshVarTmp(t)
			target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
			priorTarget, priorFault := rehearsalGSITarget, rehearsalArmFault
			rehearsalGSITarget = func() string { return target }
			rehearsalArmFault = func(observed string) error {
				if observed == point {
					return errors.New("injected " + point)
				}
				return nil
			}
			t.Cleanup(func() { rehearsalGSITarget, rehearsalArmFault = priorTarget, priorFault })
			withRehearsalProbe(t, repo, func(*rehearsalPreflightProbe) {})
			_, _ = RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo})
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("post-install fault left GSI target: %v", err)
			}
			matches, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".dota2-ob-cleanup-*"))
			if len(matches) != 0 {
				t.Fatalf("post-install fault left cleanup entries: %v", matches)
			}
		})
	}
}

func TestRehearsalArmRollbackFailureHasDurableConsumableRecovery(t *testing.T) {
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Clean(filepath.Join(working, "../.."))
	root := freshVarTmp(t)
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	priorTarget, priorFault, priorRemove := rehearsalGSITarget, rehearsalArmFault, rehearsalBeforeGSIRemove
	rehearsalGSITarget = func() string { return target }
	rehearsalArmFault = func(point string) error {
		if point == "preflight_after_arm" {
			return errors.New("injected post-install failure")
		}
		return nil
	}
	rehearsalBeforeGSIRemove = func(string) error { return errors.New("injected rollback failure") }
	t.Cleanup(func() {
		rehearsalGSITarget, rehearsalArmFault, rehearsalBeforeGSIRemove = priorTarget, priorFault, priorRemove
	})
	withRehearsalProbe(t, repo, func(*rehearsalPreflightProbe) {})
	if _, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo}); err == nil || !strings.Contains(err.Error(), "durable recovery token") {
		t.Fatalf("preflight error=%v", err)
	}
	var recovery RehearsalArmRecoveryV1
	payload, err := os.ReadFile(filepath.Join(root, "rehearsal", "arm-cleanup-recovery.json"))
	if err != nil || json.Unmarshal(payload, &recovery) != nil || recovery.State != rehearsalRecoveryOwnedQuarantine || recovery.Arm.ArmToken == "" {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	rehearsalBeforeGSIRemove = func(string) error { return nil }
	if err := DisarmRehearsal(root, repo, recovery.Arm.ArmToken); err != nil {
		t.Fatal("durable recovery was not consumable:", err)
	}
	if _, err := os.Lstat(recovery.QuarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned quarantine remains: %v", err)
	}
}

func TestRehearsalArmRollbackAndCleanupRecoveryWriteFailureUsesPreprovisionedIntent(t *testing.T) {
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Clean(filepath.Join(working, "../.."))
	root := freshVarTmp(t)
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	priorTarget, priorFault, priorRemove := rehearsalGSITarget, rehearsalArmFault, rehearsalBeforeGSIRemove
	rehearsalGSITarget = func() string { return target }
	rehearsalArmFault = func(point string) error {
		if point == "preflight_after_arm" {
			if err := os.WriteFile(filepath.Join(root, "rehearsal", "arm-cleanup-recovery.json"), []byte("occupied\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return errors.New("injected post-install failure")
		}
		return nil
	}
	rehearsalBeforeGSIRemove = func(string) error { return errors.New("injected rollback failure") }
	t.Cleanup(func() {
		rehearsalGSITarget, rehearsalArmFault, rehearsalBeforeGSIRemove = priorTarget, priorFault, priorRemove
	})
	withRehearsalProbe(t, repo, func(*rehearsalPreflightProbe) {})
	if _, err := RehearsalPreflight(context.Background(), RehearsalPreflightConfig{DataRoot: root, RepoRoot: repo}); err == nil || !strings.Contains(err.Error(), "durable recovery token") || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("nested failure error=%v", err)
	}
	var recovery RehearsalArmRecoveryV1
	payload, err := os.ReadFile(filepath.Join(root, "rehearsal", "arm-recovery.json"))
	if err != nil || json.Unmarshal(payload, &recovery) != nil || recovery.State != rehearsalRecoveryInstalled || recovery.QuarantinePath == "" {
		t.Fatalf("preprovisioned recovery=%+v err=%v", recovery, err)
	}
	rehearsalBeforeGSIRemove = func(string) error { return nil }
	if err := DisarmRehearsal(root, repo, recovery.Arm.ArmToken); err != nil {
		t.Fatal("preprovisioned recovery was not consumable:", err)
	}
	if _, err := os.Lstat(recovery.QuarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned quarantine remains: %v", err)
	}
}

func TestRehearsalArmRecoveryPersistenceFailureRollsBackAndPropagates(t *testing.T) {
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	priorTarget, priorFault := rehearsalGSITarget, rehearsalArmFault
	rehearsalGSITarget = func() string { return target }
	rehearsalArmFault = func(point string) error {
		if point == "recovery_persist" {
			return errors.New("injected recovery persistence failure")
		}
		return nil
	}
	t.Cleanup(func() { rehearsalGSITarget, rehearsalArmFault = priorTarget, priorFault })
	preflight := RehearsalPreflightV1{SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RootOwnerSHA256: strings.Repeat("b", 64)}
	if _, err := armRehearsalGSI(root, preflight); err == nil || !strings.Contains(err.Error(), "recovery intent persistence failed") {
		t.Fatalf("arm error=%v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("persistence failure left installed GSI: %v", err)
	}
}

func TestRehearsalArmOwnsRefusesSubstitutionAndTokenGatesCleanup(t *testing.T) {
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
	prior := rehearsalGSITarget
	rehearsalGSITarget = func() string { return target }
	t.Cleanup(func() { rehearsalGSITarget = prior })
	preflight := RehearsalPreflightV1{SessionID: "session", CandidateCommit: strings.Repeat("a", 40), RootOwnerSHA256: strings.Repeat("b", 64)}
	arm, err := armRehearsalGSI(root, preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.ArmSHA256, _ = rehearsalArmBindingID(arm)
	if _, err := readRehearsalArm(root, preflight, true); err != nil {
		t.Fatal(err)
	}
	if _, err := armRehearsalGSI(root, preflight); err == nil {
		t.Fatal("pre-existing config was overwritten")
	}
	if err := cleanupRehearsalArm(root, preflight, "wrong"); err == nil {
		t.Fatal("wrong arm token accepted")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("wrong token removed config")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "config/dota/gamestate_integration_dota2_ob_m4.cfg"), target); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRehearsalArm(root, preflight, arm.ArmToken); err == nil {
		t.Fatal("symlink substitution accepted")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "rehearsal", "arm-recovery.json")); err != nil {
		t.Fatal(err)
	}
	second, err := armRehearsalGSI(root, preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.ArmSHA256, _ = rehearsalArmBindingID(second)
	var current RehearsalArmV1
	current, err = readRehearsalArm(root, preflight, true)
	if err != nil {
		t.Fatal(err)
	}
	preflight.ArmSHA256, _ = rehearsalArmBindingID(current)
	if err := cleanupRehearsalArm(root, preflight, current.ArmToken); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedOBSRefusesPreexistingInstanceBeforeLaunch(t *testing.T) {
	prior := flatpakOBSInstanceLister
	flatpakOBSInstanceLister = func(context.Context) ([]flatpakOBSInstance, error) {
		return []flatpakOBSInstance{{InstanceID: "unrelated", WrapperPID: 10, SandboxPID: 11, Application: "com.obsproject.Studio"}}, nil
	}
	t.Cleanup(func() { flatpakOBSInstanceLister = prior })
	if _, err := startOwnedFlatpakOBS(context.Background(), t.TempDir(), os.Stderr); err == nil || !strings.Contains(err.Error(), "pre-existing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFlatpakPrebindFailuresAbortOnlyFDBoundInstance(t *testing.T) {
	priorLister, priorDiscoverer, priorAbort := flatpakOBSInstanceLister, flatpakOBSDiscoverer, flatpakAbortBoundInstance
	t.Cleanup(func() {
		flatpakOBSInstanceLister, flatpakOBSDiscoverer, flatpakAbortBoundInstance = priorLister, priorDiscoverer, priorAbort
	})
	flatpakOBSDiscoverer = func(int) (int, RehearsalOwnedProcessIdentityV1, error) {
		return 0, RehearsalOwnedProcessIdentityV1{}, errors.New("not bound")
	}
	for _, scenario := range []string{"unrelated", "timeout", "launcher_exit"} {
		t.Run(scenario, func(t *testing.T) {
			aborted := ""
			flatpakAbortBoundInstance = func(_ context.Context, obs *ownedFlatpakOBS, _ time.Duration) error {
				aborted = obs.InstanceID
				return nil
			}
			owned := &ownedFlatpakOBS{flatpakOBSInstance: flatpakOBSInstance{InstanceID: "fd-owned"}, Root: t.TempDir(), LauncherDone: make(chan error, 1), KnownProcesses: map[int]RehearsalOwnedProcessIdentityV1{}}
			flatpakOBSInstanceLister = func(context.Context) ([]flatpakOBSInstance, error) {
				values := []flatpakOBSInstance{{InstanceID: "fd-owned", WrapperPID: 10, SandboxPID: 11, Application: "com.obsproject.Studio"}}
				if scenario == "unrelated" {
					values = append(values, flatpakOBSInstance{InstanceID: "unrelated", WrapperPID: 20, SandboxPID: 21, Application: "com.obsproject.Studio"})
				}
				return values, nil
			}
			if scenario == "launcher_exit" {
				owned.LauncherDone <- errors.New("launcher exited")
			}
			timeout := 3 * time.Millisecond
			if scenario == "unrelated" || scenario == "launcher_exit" {
				timeout = time.Second
			}
			if _, err := bindLaunchedFlatpakOBS(context.Background(), owned, timeout); err == nil {
				t.Fatal("pre-bind failure accepted")
			}
			if aborted != "fd-owned" {
				t.Fatalf("aborted=%q", aborted)
			}
		})
	}
}

func TestAbortBoundFlatpakWaitsOwnedProcessesAndLeavesUnrelated(t *testing.T) {
	priorLister, priorReader, priorKill := flatpakOBSInstanceLister, flatpakOwnedIdentityReader, flatpakKillBoundInstance
	t.Cleanup(func() {
		flatpakOBSInstanceLister, flatpakOwnedIdentityReader, flatpakKillBoundInstance = priorLister, priorReader, priorKill
	})
	identity := RehearsalOwnedProcessIdentityV1{SchemaVersion: "rehearsal_owned_process_identity.v1", PID: 30, Comm: "bwrap", ExecutablePath: "/owned/bwrap", ExecutablePathSHA256: stringsOf('a'), ExecutableSHA256: stringsOf('b'), ProcessStartTicks: 9, ExecutableDevice: 1, ExecutableInode: 2}
	killed := []string{}
	flatpakKillBoundInstance = func(_ context.Context, instance string) error {
		killed = append(killed, instance)
		return nil
	}
	flatpakOBSInstanceLister = func(context.Context) ([]flatpakOBSInstance, error) {
		return []flatpakOBSInstance{{InstanceID: "unrelated", WrapperPID: 40, SandboxPID: 41, Application: "com.obsproject.Studio"}}, nil
	}
	reads := 0
	flatpakOwnedIdentityReader = func(pid int) (RehearsalOwnedProcessIdentityV1, error) {
		reads++
		if pid == identity.PID && reads == 1 {
			return identity, nil
		}
		return RehearsalOwnedProcessIdentityV1{}, os.ErrNotExist
	}
	done := make(chan error, 1)
	done <- nil
	owned := &ownedFlatpakOBS{flatpakOBSInstance: flatpakOBSInstance{InstanceID: "fd-owned"}, LauncherDone: done, KnownProcesses: map[int]RehearsalOwnedProcessIdentityV1{identity.PID: identity}}
	if err := abortBoundFlatpakOBS(context.Background(), owned, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(killed) != 1 || killed[0] != "fd-owned" || reads < 2 || !owned.LauncherWaited {
		t.Fatalf("killed=%v reads=%d waited=%v", killed, reads, owned.LauncherWaited)
	}
}

func TestRehearsalOBSProfileUsesAbsoluteOwnedRecordingPath(t *testing.T) {
	root := t.TempDir()
	if _, err := Prepare(root, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	if err := prepareRehearsalOBSProfile(root); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(root, "config/obs-studio/basic/profiles/DOT65-P4/basic.ini"))
	if err != nil || !strings.Contains(string(payload), "RecFilePath="+filepath.Join(root, "recordings")) {
		t.Fatalf("profile=%s err=%v", payload, err)
	}
}
