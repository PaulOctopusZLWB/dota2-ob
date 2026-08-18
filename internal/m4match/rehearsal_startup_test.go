//go:build linux

package m4match

import (
	"context"
	"errors"
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
