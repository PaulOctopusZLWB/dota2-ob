package m4match

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const rehearsalArmSchemaV1 = "rehearsal_arm.v1"

var rehearsalGSITarget = func() string {
	return filepath.Join(os.Getenv("HOME"), ".local/share/Steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
}

var rehearsalArmFault = func(string) error { return nil }
var rehearsalAfterGSIQuarantine = func(string) error { return nil }

func resolveRehearsalGo() (string, string, error) {
	candidates := []string{os.Getenv("DOTA2_OB_GO"), "/home/linuxbrew/.linuxbrew/bin/go", "/usr/local/go/bin/go", "/usr/bin/go"}
	if path, err := exec.LookPath("go"); err == nil {
		candidates = append(candidates, path)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		hash, _, err := fileSHA(resolved)
		if err == nil {
			return resolved, hash, nil
		}
	}
	return "", "", errors.New("deterministic Go executable unavailable")
}

func armRehearsalGSI(root string, preflight RehearsalPreflightV1) (RehearsalArmV1, error) {
	sourceRelative := "config/dota/gamestate_integration_dota2_ob_m4.cfg"
	source := filepath.Join(root, filepath.FromSlash(sourceRelative))
	sourceHash, sourceBytes, err := fileSHA(source)
	if err != nil {
		return RehearsalArmV1{}, err
	}
	target := rehearsalGSITarget()
	if !filepath.IsAbs(target) {
		return RehearsalArmV1{}, errors.New("rehearsal GSI target is not absolute")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return RehearsalArmV1{}, err
	}
	resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return RehearsalArmV1{}, err
	}
	target = filepath.Join(resolvedDir, filepath.Base(target))
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return RehearsalArmV1{}, errors.New("refusing to overwrite pre-existing non-owned rehearsal GSI config")
	}
	in, err := os.Open(source)
	if err != nil {
		return RehearsalArmV1{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return RehearsalArmV1{}, err
	}
	createdInfo, statErr := out.Stat()
	if statErr != nil {
		_ = out.Close()
		return RehearsalArmV1{}, errors.Join(errors.New("created rehearsal GSI identity unavailable"), removeCreatedGSI(target, 0, 0))
	}
	createdStat, statOK := createdInfo.Sys().(*syscall.Stat_t)
	if !statOK {
		_ = out.Close()
		return RehearsalArmV1{}, errors.Join(errors.New("created rehearsal GSI identity unavailable"), removeCreatedGSI(target, 0, 0))
	}
	createdDevice, createdInode := uint64(createdStat.Dev), createdStat.Ino
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return RehearsalArmV1{}, errors.Join(err, removeCreatedGSI(target, createdDevice, createdInode))
	}
	identity, err := inspectArmedGSI(target)
	if err != nil || identity.hash != sourceHash || identity.bytes != sourceBytes {
		return RehearsalArmV1{}, errors.Join(errors.New("installed rehearsal GSI identity mismatch"), removeCreatedGSI(target, createdDevice, createdInode))
	}
	arm := RehearsalArmV1{SchemaVersion: rehearsalArmSchemaV1, Purpose: RehearsalPurpose, SessionID: preflight.SessionID,
		CandidateCommit: preflight.CandidateCommit, RootOwnerSHA256: preflight.RootOwnerSHA256, ConfigSourcePath: sourceRelative,
		ConfigTargetPath: target, ConfigSHA256: sourceHash, ConfigBytes: sourceBytes, ConfigDevice: identity.device, ConfigInode: identity.inode}
	arm.ArmToken, err = rehearsalArmContentID(arm)
	if err != nil {
		return RehearsalArmV1{}, errors.Join(err, rollbackArmedGSI(root, arm))
	}
	if err := rehearsalArmFault("arm_content_id"); err != nil {
		return RehearsalArmV1{}, errors.Join(err, rollbackArmedGSI(root, arm))
	}
	if err := rehearsalArmFault("arm_record_write"); err != nil {
		return RehearsalArmV1{}, errors.Join(err, rollbackArmedGSI(root, arm))
	}
	if err := writeJSON(filepath.Join(root, "rehearsal", "arm.json"), arm, 0o600); err != nil {
		return RehearsalArmV1{}, errors.Join(err, rollbackArmedGSI(root, arm))
	}
	return arm, nil
}

func removeCreatedGSI(target string, device, inode uint64) error {
	quarantine := fmt.Sprintf("%s.creating-%d", target, os.Getpid())
	if _, err := os.Lstat(quarantine); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("created rehearsal GSI quarantine occupied")
	}
	if err := os.Rename(target, quarantine); err != nil {
		return err
	}
	file, err := os.OpenFile(quarantine, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = restoreQuarantinedEntry(quarantine, target)
		return err
	}
	info, statErr := file.Stat()
	_ = file.Close()
	if statErr != nil {
		_ = restoreQuarantinedEntry(quarantine, target)
		return statErr
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (device != 0 && (uint64(stat.Dev) != device || stat.Ino != inode)) {
		_ = restoreQuarantinedEntry(quarantine, target)
		return errors.New("created rehearsal GSI entry was substituted")
	}
	return os.Remove(quarantine)
}

type armedGSIIdentity struct {
	hash          string
	bytes         int64
	device, inode uint64
}

func inspectArmedGSI(path string) (armedGSIIdentity, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return armedGSIIdentity{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return armedGSIIdentity{}, errors.New("armed GSI is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return armedGSIIdentity{}, errors.New("armed GSI stat unavailable")
	}
	h := sha256.New()
	bytes, err := io.Copy(h, file)
	if err != nil {
		return armedGSIIdentity{}, err
	}
	return armedGSIIdentity{hash: hex.EncodeToString(h.Sum(nil)), bytes: bytes, device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func rehearsalArmContentID(value RehearsalArmV1) (string, error) {
	value.ArmToken = ""
	return payloadSHAFromCanonical(value)
}

func rehearsalArmBindingID(value RehearsalArmV1) (string, error) {
	value.ArmToken = ""
	value.ConfigDevice = 0
	value.ConfigInode = 0
	return payloadSHAFromCanonical(value)
}

func readRehearsalArm(root string, preflight RehearsalPreflightV1, requireInstalled bool) (RehearsalArmV1, error) {
	var arm RehearsalArmV1
	payload, err := rootReadFile(filepath.Join(root, "rehearsal", "arm.json"))
	if err != nil || json.Unmarshal(payload, &arm) != nil {
		return arm, errors.New("rehearsal arm record unavailable")
	}
	want, err := rehearsalArmContentID(arm)
	binding, bindingErr := rehearsalArmBindingID(arm)
	if err != nil || bindingErr != nil || arm.ArmToken != want || preflight.ArmSHA256 != binding || arm.SchemaVersion != rehearsalArmSchemaV1 || arm.Purpose != RehearsalPurpose || arm.SessionID != preflight.SessionID || arm.CandidateCommit != preflight.CandidateCommit || arm.RootOwnerSHA256 != preflight.RootOwnerSHA256 || !validLowerSHA256(arm.ConfigSHA256) || arm.ConfigBytes <= 0 || arm.ConfigDevice == 0 || arm.ConfigInode == 0 || !filepath.IsAbs(arm.ConfigTargetPath) {
		return arm, errors.New("rehearsal arm record mismatch")
	}
	sourceHash, sourceBytes, sourceErr := fileSHA(filepath.Join(root, filepath.FromSlash(arm.ConfigSourcePath)))
	if sourceErr != nil || sourceHash != arm.ConfigSHA256 || sourceBytes != arm.ConfigBytes {
		return arm, errors.New("rehearsal arm source changed")
	}
	if requireInstalled {
		identity, inspectErr := inspectArmedGSI(arm.ConfigTargetPath)
		if inspectErr != nil || identity.hash != arm.ConfigSHA256 || identity.bytes != arm.ConfigBytes || identity.device != arm.ConfigDevice || identity.inode != arm.ConfigInode {
			return arm, errors.New("rehearsal armed GSI changed")
		}
	}
	return arm, nil
}

func cleanupRehearsalArm(root string, preflight RehearsalPreflightV1, token string) error {
	arm, err := readRehearsalArm(root, preflight, false)
	if err != nil {
		return err
	}
	if token == "" || token != arm.ArmToken {
		return errors.New("rehearsal arm cleanup token mismatch")
	}
	return removeArmedGSI(arm)
}

func removeArmedGSI(arm RehearsalArmV1) error {
	if !filepath.IsAbs(arm.ConfigTargetPath) || !validLowerSHA256(arm.ConfigSHA256) || arm.ConfigBytes <= 0 || arm.ConfigDevice == 0 || arm.ConfigInode == 0 {
		return errors.New("armed GSI cleanup identity invalid")
	}
	suffix := arm.ArmToken
	if suffix == "" {
		suffix = arm.ConfigSHA256
	}
	quarantine := filepath.Join(filepath.Dir(arm.ConfigTargetPath), ".dota2-ob-cleanup-"+suffix[:16])
	if _, err := os.Lstat(quarantine); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("armed GSI cleanup quarantine occupied")
	}
	if err := os.Rename(arm.ConfigTargetPath, quarantine); err != nil {
		return err
	}
	if err := rehearsalAfterGSIQuarantine(arm.ConfigTargetPath); err != nil {
		_ = restoreQuarantinedEntry(quarantine, arm.ConfigTargetPath)
		return err
	}
	identity, inspectErr := inspectArmedGSI(quarantine)
	if inspectErr != nil || identity.hash != arm.ConfigSHA256 || identity.bytes != arm.ConfigBytes || identity.device != arm.ConfigDevice || identity.inode != arm.ConfigInode {
		restoreErr := restoreQuarantinedEntry(quarantine, arm.ConfigTargetPath)
		return errors.Join(errors.New("armed GSI cleanup encountered substituted entry"), restoreErr)
	}
	return os.Remove(quarantine)
}

func restoreQuarantinedEntry(quarantine, target string) error {
	if err := os.Link(quarantine, target); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return os.Remove(quarantine)
}

func rollbackArmedGSI(root string, arm RehearsalArmV1) error {
	if err := removeArmedGSI(arm); err != nil {
		_ = writeJSON(filepath.Join(root, "rehearsal", "arm-recovery.json"), arm, 0o600)
		return fmt.Errorf("armed GSI rollback failed; recovery token %s: %w", arm.ArmToken, err)
	}
	return nil
}

func DisarmRehearsal(dataRoot, repoRoot, token string) error {
	lease, err := acquireExistingRoot(dataRoot, repoRoot)
	if err != nil {
		return err
	}
	defer lease.Close()
	if preflight, preflightErr := readRehearsalPreflight(lease.abs); preflightErr == nil && preflight.ConsoleState == RehearsalReady {
		return cleanupRehearsalArm(lease.abs, preflight, token)
	}
	for _, name := range []string{"arm-recovery.json", "arm.json"} {
		arm, err := readStandaloneRehearsalArm(lease.abs, name)
		if err != nil {
			continue
		}
		if token == "" || token != arm.ArmToken {
			return errors.New("rehearsal arm cleanup token mismatch")
		}
		return removeArmedGSI(arm)
	}
	return errors.New("recoverable rehearsal arm unavailable")
}

func readStandaloneRehearsalArm(root, name string) (RehearsalArmV1, error) {
	var arm RehearsalArmV1
	payload, err := rootReadFile(filepath.Join(root, "rehearsal", name))
	if err != nil || json.Unmarshal(payload, &arm) != nil {
		return arm, errors.New("standalone rehearsal arm unavailable")
	}
	want, err := rehearsalArmContentID(arm)
	if err != nil || arm.ArmToken != want || arm.SchemaVersion != rehearsalArmSchemaV1 || arm.Purpose != RehearsalPurpose || !validLowerSHA256(arm.ConfigSHA256) {
		return arm, errors.New("standalone rehearsal arm mismatch")
	}
	return arm, nil
}
