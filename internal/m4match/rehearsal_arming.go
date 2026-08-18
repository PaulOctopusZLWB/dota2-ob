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
	"unsafe"
)

const rehearsalArmSchemaV1 = "rehearsal_arm.v1"
const rehearsalArmRecoverySchemaV1 = "rehearsal_arm_recovery.v1"

const (
	rehearsalOTmpfile    = 020000000 | syscall.O_DIRECTORY
	rehearsalATEmptyPath = 0x1000
)

const (
	rehearsalRecoveryInstalled       = "installed_owned"
	rehearsalRecoveryRetained        = "substituted_quarantine_retained"
	rehearsalRecoveryOwnedQuarantine = "owned_quarantine_retained"
)

var rehearsalGSITarget = func() string {
	return filepath.Join(os.Getenv("HOME"), ".local/share/Steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/gamestate_integration_dota2_ob_dot87_rehearsal.cfg")
}

var rehearsalArmFault = func(string) error { return nil }
var rehearsalAfterGSIQuarantine = func(string) error { return nil }
var rehearsalCreatedGSIStat = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
var rehearsalBeforeGSIRemove = func(string) error { return nil }

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
	fd, err := syscall.Open(filepath.Dir(target), syscall.O_RDWR|syscall.O_CLOEXEC|rehearsalOTmpfile, 0o600)
	if err != nil {
		return RehearsalArmV1{}, fmt.Errorf("create anonymous rehearsal GSI: %w", err)
	}
	out := os.NewFile(uintptr(fd), "anonymous-rehearsal-gsi")
	createdInfo, statErr := rehearsalCreatedGSIStat(out)
	if statErr != nil {
		_ = out.Close()
		return RehearsalArmV1{}, errors.New("created anonymous rehearsal GSI identity unavailable; unpublished inode discarded")
	}
	createdStat, statOK := createdInfo.Sys().(*syscall.Stat_t)
	if !statOK {
		_ = out.Close()
		return RehearsalArmV1{}, errors.New("created anonymous rehearsal GSI Stat_t unavailable; unpublished inode discarded")
	}
	createdDevice, createdInode := uint64(createdStat.Dev), createdStat.Ino
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	if err := errors.Join(copyErr, syncErr); err != nil {
		_ = out.Close()
		return RehearsalArmV1{}, err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		_ = out.Close()
		return RehearsalArmV1{}, err
	}
	hash := sha256.New()
	writtenBytes, hashErr := io.Copy(hash, out)
	if hashErr != nil || hex.EncodeToString(hash.Sum(nil)) != sourceHash || writtenBytes != sourceBytes {
		_ = out.Close()
		return RehearsalArmV1{}, errors.Join(errors.New("anonymous rehearsal GSI content mismatch"), hashErr)
	}
	arm := RehearsalArmV1{SchemaVersion: rehearsalArmSchemaV1, Purpose: RehearsalPurpose, SessionID: preflight.SessionID,
		CandidateCommit: preflight.CandidateCommit, RootOwnerSHA256: preflight.RootOwnerSHA256, ConfigSourcePath: sourceRelative,
		ConfigTargetPath: target, ConfigSHA256: sourceHash, ConfigBytes: sourceBytes, ConfigDevice: createdDevice, ConfigInode: createdInode}
	arm.ArmToken, err = rehearsalArmContentID(arm)
	if err != nil {
		_ = out.Close()
		return RehearsalArmV1{}, err
	}
	if err := persistInstalledArmRecovery(root, arm); err != nil {
		_ = out.Close()
		return RehearsalArmV1{}, fmt.Errorf("rehearsal recovery intent persistence failed before publication: %w", err)
	}
	if err := publishAnonymousGSI(out, target); err != nil {
		closeErr := out.Close()
		recoveryErr := rootRemove(filepath.Join(root, "rehearsal", "arm-recovery.json"))
		return RehearsalArmV1{}, errors.Join(fmt.Errorf("publish anonymous rehearsal GSI: %w", err), closeErr, recoveryErr)
	}
	if err := syncRehearsalGSIDirectory(target); err != nil {
		closeErr := out.Close()
		return RehearsalArmV1{}, errors.Join(err, closeErr, rollbackArmedGSI(root, arm))
	}
	identity, inspectErr := inspectArmedGSI(target)
	if inspectErr != nil || identity.hash != sourceHash || identity.bytes != sourceBytes || identity.device != createdDevice || identity.inode != createdInode {
		closeErr := out.Close()
		return RehearsalArmV1{}, errors.Join(errors.New("installed rehearsal GSI identity mismatch"), inspectErr, closeErr, rollbackArmedGSI(root, arm))
	}
	if err := out.Close(); err != nil {
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

func publishAnonymousGSI(file *os.File, target string) error {
	empty, err := syscall.BytePtrFromString("")
	if err != nil {
		return err
	}
	targetBytes, err := syscall.BytePtrFromString(target)
	if err != nil {
		return err
	}
	atFDCWD := ^uintptr(99) // Linux AT_FDCWD (-100) represented as uintptr.
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, file.Fd(), uintptr(unsafe.Pointer(empty)), atFDCWD, uintptr(unsafe.Pointer(targetBytes)), rehearsalATEmptyPath, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func syncRehearsalGSIDirectory(target string) error {
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func removeCreatedGSI(target string, device, inode uint64) error {
	if device == 0 || inode == 0 {
		return errors.New("created rehearsal GSI cleanup identity invalid; entry retained")
	}
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
	if !ok || uint64(stat.Dev) != device || stat.Ino != inode {
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
	if err := removeArmedGSI(root, arm); err != nil {
		return err
	}
	if err := rootRemove(filepath.Join(root, "rehearsal", "arm-recovery.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeArmedGSI(root string, arm RehearsalArmV1) error {
	if !filepath.IsAbs(arm.ConfigTargetPath) || !validLowerSHA256(arm.ConfigSHA256) || arm.ConfigBytes <= 0 || arm.ConfigDevice == 0 || arm.ConfigInode == 0 {
		return errors.New("armed GSI cleanup identity invalid")
	}
	quarantine := armedGSIQuarantinePath(arm)
	if _, err := os.Lstat(quarantine); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("armed GSI cleanup quarantine occupied")
	}
	if err := os.Rename(arm.ConfigTargetPath, quarantine); err != nil {
		return err
	}
	if err := rehearsalAfterGSIQuarantine(arm.ConfigTargetPath); err != nil {
		restoreErr := restoreQuarantinedEntry(quarantine, arm.ConfigTargetPath)
		if restoreErr != nil {
			recoveryErr := persistRetainedCleanupRecovery(root, arm, quarantine)
			return errors.Join(err, restoreErr, recoveryErr)
		}
		return err
	}
	identity, inspectErr := inspectArmedGSI(quarantine)
	if inspectErr != nil || identity.hash != arm.ConfigSHA256 || identity.bytes != arm.ConfigBytes || identity.device != arm.ConfigDevice || identity.inode != arm.ConfigInode {
		restoreErr := restoreQuarantinedEntry(quarantine, arm.ConfigTargetPath)
		if restoreErr != nil {
			recoveryErr := persistRetainedCleanupRecovery(root, arm, quarantine)
			return errors.Join(errors.New("armed GSI cleanup encountered substituted entry; machine-checkable recovery retained"), inspectErr, restoreErr, recoveryErr)
		}
		return errors.Join(errors.New("armed GSI cleanup encountered substituted entry"), inspectErr)
	}
	if err := rehearsalBeforeGSIRemove(quarantine); err != nil {
		return errors.Join(err, persistOwnedCleanupRecovery(root, arm, quarantine))
	}
	return os.Remove(quarantine)
}

func restoreQuarantinedEntry(quarantine, target string) error {
	if err := os.Link(quarantine, target); err != nil {
		return err
	}
	return os.Remove(quarantine)
}

func armedGSIQuarantinePath(arm RehearsalArmV1) string {
	suffix := arm.ArmToken
	if suffix == "" {
		suffix = arm.ConfigSHA256
	}
	return filepath.Join(filepath.Dir(arm.ConfigTargetPath), ".dota2-ob-cleanup-"+suffix[:16])
}

func rollbackArmedGSI(root string, arm RehearsalArmV1) error {
	recoveryErr := verifyInstalledArmRecovery(root, arm)
	removeErr := removeArmedGSI(root, arm)
	if removeErr != nil {
		if recoveryErr != nil {
			return errors.Join(fmt.Errorf("armed GSI rollback failed and durable recovery verification failed: %w", removeErr), recoveryErr)
		}
		return fmt.Errorf("armed GSI rollback failed; durable recovery token %s: %w", arm.ArmToken, removeErr)
	}
	removeRecoveryErr := rootRemove(filepath.Join(root, "rehearsal", "arm-recovery.json"))
	if errors.Is(removeRecoveryErr, os.ErrNotExist) {
		removeRecoveryErr = nil
	}
	return errors.Join(recoveryErr, removeRecoveryErr)
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
	if recovery, recoveryErr := readArmRecovery(lease.abs, "arm-cleanup-recovery.json"); recoveryErr == nil {
		if token == "" || token != recovery.Arm.ArmToken {
			return errors.New("rehearsal arm cleanup token mismatch")
		}
		return restoreRetainedCleanupRecovery(lease.abs, recovery)
	}
	if recovery, recoveryErr := readArmRecovery(lease.abs, "arm-recovery.json"); recoveryErr == nil {
		if token == "" || token != recovery.Arm.ArmToken {
			return errors.New("rehearsal arm cleanup token mismatch")
		}
		if err := disarmInstalledRecovery(lease.abs, recovery); err != nil {
			return err
		}
		return rootRemove(filepath.Join(lease.abs, "rehearsal", "arm-recovery.json"))
	}
	for _, name := range []string{"arm-recovery.json", "arm.json"} {
		arm, err := readStandaloneRehearsalArm(lease.abs, name)
		if err != nil {
			continue
		}
		if token == "" || token != arm.ArmToken {
			return errors.New("rehearsal arm cleanup token mismatch")
		}
		return removeArmedGSI(lease.abs, arm)
	}
	return errors.New("recoverable rehearsal arm unavailable")
}

func armRecoveryContentID(value RehearsalArmRecoveryV1) (string, error) {
	value.RecoverySHA256 = ""
	return payloadSHAFromCanonical(value)
}

func newInstalledArmRecovery(arm RehearsalArmV1) (RehearsalArmRecoveryV1, error) {
	value := RehearsalArmRecoveryV1{SchemaVersion: rehearsalArmRecoverySchemaV1, Purpose: RehearsalPurpose, State: rehearsalRecoveryInstalled, Arm: arm, QuarantinePath: armedGSIQuarantinePath(arm)}
	var err error
	value.RecoverySHA256, err = armRecoveryContentID(value)
	return value, err
}

func persistInstalledArmRecovery(root string, arm RehearsalArmV1) error {
	if err := rehearsalArmFault("recovery_persist"); err != nil {
		return err
	}
	value, err := newInstalledArmRecovery(arm)
	if err != nil {
		return err
	}
	if err := writeArmRecovery(root, "arm-recovery.json", value); err != nil {
		return err
	}
	return verifyInstalledArmRecovery(root, arm)
}

func verifyInstalledArmRecovery(root string, arm RehearsalArmV1) error {
	value, err := readArmRecovery(root, "arm-recovery.json")
	if err != nil {
		return err
	}
	if value.State != rehearsalRecoveryInstalled || value.Arm.ArmToken != arm.ArmToken || value.Arm.ConfigDevice != arm.ConfigDevice || value.Arm.ConfigInode != arm.ConfigInode || value.QuarantinePath != armedGSIQuarantinePath(arm) {
		return errors.New("installed rehearsal arm recovery mismatch")
	}
	return nil
}

func disarmInstalledRecovery(root string, recovery RehearsalArmRecoveryV1) error {
	if recovery.State != rehearsalRecoveryInstalled || recovery.QuarantinePath != armedGSIQuarantinePath(recovery.Arm) {
		return errors.New("installed rehearsal recovery path mismatch")
	}
	if _, err := os.Lstat(recovery.Arm.ConfigTargetPath); err == nil {
		return removeArmedGSI(root, recovery.Arm)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	identity, err := inspectArmedGSI(recovery.QuarantinePath)
	if err != nil || identity.hash != recovery.Arm.ConfigSHA256 || identity.bytes != recovery.Arm.ConfigBytes || identity.device != recovery.Arm.ConfigDevice || identity.inode != recovery.Arm.ConfigInode {
		return errors.New("installed rehearsal recovery cannot prove target or quarantine identity")
	}
	return os.Remove(recovery.QuarantinePath)
}

func persistRetainedCleanupRecovery(root string, arm RehearsalArmV1, quarantine string) error {
	quarantineIdentity, err := inspectArmedGSI(quarantine)
	if err != nil {
		return err
	}
	targetIdentity, err := inspectArmedGSI(arm.ConfigTargetPath)
	if err != nil {
		return err
	}
	value := RehearsalArmRecoveryV1{
		SchemaVersion: rehearsalArmRecoverySchemaV1, Purpose: RehearsalPurpose, State: rehearsalRecoveryRetained, Arm: arm,
		QuarantinePath: quarantine, QuarantineSHA256: quarantineIdentity.hash, QuarantineBytes: quarantineIdentity.bytes,
		QuarantineDevice: quarantineIdentity.device, QuarantineInode: quarantineIdentity.inode,
		OccupiedTargetSHA256: targetIdentity.hash, OccupiedTargetBytes: targetIdentity.bytes,
		OccupiedTargetDevice: targetIdentity.device, OccupiedTargetInode: targetIdentity.inode,
	}
	value.RecoverySHA256, err = armRecoveryContentID(value)
	if err != nil {
		return err
	}
	if err := writeArmRecovery(root, "arm-cleanup-recovery.json", value); err != nil {
		return err
	}
	_, err = readArmRecovery(root, "arm-cleanup-recovery.json")
	return err
}

func writeArmRecovery(root, name string, value RehearsalArmRecoveryV1) error {
	path := filepath.Join(root, "rehearsal", name)
	if err := rootMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	payload, err := canonical(value)
	if err != nil {
		return err
	}
	file, err := rootOpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	dirSyncErr := dir.Sync()
	dirCloseErr := dir.Close()
	return errors.Join(dirSyncErr, dirCloseErr)
}

func readArmRecovery(root, name string) (RehearsalArmRecoveryV1, error) {
	var value RehearsalArmRecoveryV1
	payload, err := rootReadFile(filepath.Join(root, "rehearsal", name))
	if err != nil || json.Unmarshal(payload, &value) != nil {
		return value, errors.New("rehearsal arm recovery unavailable")
	}
	want, contentErr := armRecoveryContentID(value)
	armWant, armErr := rehearsalArmContentID(value.Arm)
	if contentErr != nil || armErr != nil || value.RecoverySHA256 != want || value.Arm.ArmToken != armWant || value.SchemaVersion != rehearsalArmRecoverySchemaV1 || value.Purpose != RehearsalPurpose || (value.State != rehearsalRecoveryInstalled && value.State != rehearsalRecoveryRetained && value.State != rehearsalRecoveryOwnedQuarantine) {
		return value, errors.New("rehearsal arm recovery mismatch")
	}
	return value, nil
}

func restoreRetainedCleanupRecovery(root string, recovery RehearsalArmRecoveryV1) error {
	if (recovery.State != rehearsalRecoveryRetained && recovery.State != rehearsalRecoveryOwnedQuarantine) || !filepath.IsAbs(recovery.QuarantinePath) {
		return errors.New("retained rehearsal cleanup recovery invalid")
	}
	quarantine, quarantineErr := inspectArmedGSI(recovery.QuarantinePath)
	if quarantineErr != nil || quarantine.hash != recovery.QuarantineSHA256 || quarantine.bytes != recovery.QuarantineBytes || quarantine.device != recovery.QuarantineDevice || quarantine.inode != recovery.QuarantineInode {
		return errors.New("retained rehearsal cleanup quarantine changed")
	}
	if _, err := os.Lstat(recovery.Arm.ConfigTargetPath); err == nil {
		return errors.New("retained rehearsal cleanup target remains occupied; both entries preserved")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if recovery.State == rehearsalRecoveryOwnedQuarantine {
		if quarantine.hash != recovery.Arm.ConfigSHA256 || quarantine.bytes != recovery.Arm.ConfigBytes || quarantine.device != recovery.Arm.ConfigDevice || quarantine.inode != recovery.Arm.ConfigInode {
			return errors.New("retained owned rehearsal cleanup identity mismatch")
		}
		if err := os.Remove(recovery.QuarantinePath); err != nil {
			return err
		}
	} else if err := restoreQuarantinedEntry(recovery.QuarantinePath, recovery.Arm.ConfigTargetPath); err != nil {
		return err
	}
	return rootRemove(filepath.Join(root, "rehearsal", "arm-cleanup-recovery.json"))
}

func persistOwnedCleanupRecovery(root string, arm RehearsalArmV1, quarantine string) error {
	identity, err := inspectArmedGSI(quarantine)
	if err != nil {
		return err
	}
	value := RehearsalArmRecoveryV1{
		SchemaVersion: rehearsalArmRecoverySchemaV1, Purpose: RehearsalPurpose, State: rehearsalRecoveryOwnedQuarantine, Arm: arm,
		QuarantinePath: quarantine, QuarantineSHA256: identity.hash, QuarantineBytes: identity.bytes,
		QuarantineDevice: identity.device, QuarantineInode: identity.inode,
	}
	value.RecoverySHA256, err = armRecoveryContentID(value)
	if err != nil {
		return err
	}
	return writeArmRecovery(root, "arm-cleanup-recovery.json", value)
}

func commitArmedGSI(root string, arm RehearsalArmV1) error {
	if err := verifyInstalledArmRecovery(root, arm); err != nil {
		return err
	}
	return rootRemove(filepath.Join(root, "rehearsal", "arm-recovery.json"))
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
