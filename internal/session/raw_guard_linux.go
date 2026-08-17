//go:build linux

package session

import (
	"encoding/binary"
	"os"
	"syscall"
)

const (
	rawGuardXattr = "user.dota2_ob_raw_v3_guard"
	rawGuardBytes = 8 + 8 + 8 + 8 + 8
)

var rawGuardMagic = [8]byte{'D', 'O', 'T', 'A', 'V', '3', 'G', '1'}

type rawFileGuard struct {
	rawSize, rawMtime             int64
	authoritySize, authorityCtime int64
}

func fileMtime(info os.FileInfo) int64 { return info.ModTime().UnixNano() }

func fileCtime(info os.FileInfo) (int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Ctim.Sec*1_000_000_000 + stat.Ctim.Nsec, true
}

func readRawFileGuard(rawPath string) (rawFileGuard, bool) {
	data := make([]byte, rawGuardBytes)
	n, err := syscall.Getxattr(rawPath, rawGuardXattr, data)
	if err != nil || n != len(data) || string(data[:8]) != string(rawGuardMagic[:]) {
		return rawFileGuard{}, false
	}
	return rawFileGuard{
		rawSize:        int64(binary.BigEndian.Uint64(data[8:16])),
		rawMtime:       int64(binary.BigEndian.Uint64(data[16:24])),
		authoritySize:  int64(binary.BigEndian.Uint64(data[24:32])),
		authorityCtime: int64(binary.BigEndian.Uint64(data[32:40])),
	}, true
}

func writeRawFileGuard(rawPath string, guard rawFileGuard) bool {
	data := make([]byte, rawGuardBytes)
	copy(data[:8], rawGuardMagic[:])
	binary.BigEndian.PutUint64(data[8:16], uint64(guard.rawSize))
	binary.BigEndian.PutUint64(data[16:24], uint64(guard.rawMtime))
	binary.BigEndian.PutUint64(data[24:32], uint64(guard.authoritySize))
	binary.BigEndian.PutUint64(data[32:40], uint64(guard.authorityCtime))
	if err := syscall.Setxattr(rawPath, rawGuardXattr, data, 0); err != nil {
		_ = syscall.Removexattr(rawPath, rawGuardXattr)
		return false
	}
	return true
}

func refreshRawContentGuard(rawPath string) bool {
	info, err := os.Stat(rawPath)
	if err != nil {
		return false
	}
	guard, _ := readRawFileGuard(rawPath)
	guard.rawSize, guard.rawMtime = info.Size(), fileMtime(info)
	return writeRawFileGuard(rawPath, guard)
}

func refreshAuthorityGuard(rawPath string) bool {
	info, err := os.Stat(rawAuthorityPath(rawPath))
	if err != nil {
		return false
	}
	ctime, ok := fileCtime(info)
	if !ok {
		return false
	}
	guard, _ := readRawFileGuard(rawPath)
	guard.authoritySize, guard.authorityCtime = info.Size(), ctime
	return writeRawFileGuard(rawPath, guard)
}

func refreshCompleteRawGuard(rawPath string) bool {
	return refreshRawContentGuard(rawPath) && refreshAuthorityGuard(rawPath)
}

func rawContentGuardState(rawPath string) (valid, available bool) {
	guard, ok := readRawFileGuard(rawPath)
	if !ok {
		return false, false
	}
	info, err := os.Stat(rawPath)
	if err != nil {
		return false, true
	}
	return guard.rawSize == info.Size() && guard.rawMtime == fileMtime(info), true
}

// RawContentGuardState exposes the accepted sidecar guard to bounded evidence
// admission without allowing callers to create or refresh it.
func RawContentGuardState(rawPath string) (valid, available bool) {
	return rawContentGuardState(rawPath)
}

func rawAuthorityGuardValid(rawPath string) bool {
	// The xattr is attached to the authoritative raw inode, not stored in a
	// second sidecar. Store append/recovery refresh the raw half only after a
	// bounded committed write; authority writes refresh the cache half. Any
	// direct raw rewrite or sidecar mutation therefore invalidates the fast path.
	// Filesystems without this invariant simply take the one-scan fallback.
	guard, ok := readRawFileGuard(rawPath)
	if !ok {
		return false
	}
	rawInfo, err := os.Stat(rawPath)
	if err != nil || guard.rawSize != rawInfo.Size() || guard.rawMtime != fileMtime(rawInfo) {
		return false
	}
	authorityInfo, err := os.Stat(rawAuthorityPath(rawPath))
	if err != nil || guard.authoritySize != authorityInfo.Size() {
		return false
	}
	ctime, ok := fileCtime(authorityInfo)
	return ok && guard.authorityCtime == ctime
}
