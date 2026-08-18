//go:build linux

package m4match

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRehearsalGoWithServicePATH(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	path, hash, err := resolveRehearsalGo()
	if err != nil || !filepath.IsAbs(path) || !validLowerSHA256(hash) {
		t.Fatalf("path=%q hash=%q err=%v", path, hash, err)
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
	preflight.ArmSHA256 = arm.ArmToken
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
	preflight.ArmSHA256 = second.ArmToken
	var current RehearsalArmV1
	current, err = readRehearsalArm(root, preflight, true)
	if err != nil {
		t.Fatal(err)
	}
	preflight.ArmSHA256 = current.ArmToken
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
