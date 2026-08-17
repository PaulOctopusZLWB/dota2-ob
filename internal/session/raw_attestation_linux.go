//go:build linux

package session

import (
	"fmt"
	"os"
	"syscall"
)

// RawV3DescriptorIdentity binds an already-open raw descriptor. Device/inode
// identity prevents a pathname replacement from being admitted after scan.
type RawV3DescriptorIdentity struct {
	Device uint64
	Inode  uint64
	Size   int64
	Mtime  int64
}

// VerifyRawV3ContentGuard exposes the existing Linux raw inode guard as a
// read-only attestation for evidence consumers. It does not mutate capture.
func VerifyRawV3ContentGuard(path string) bool {
	valid, available := rawContentGuardState(path)
	return available && valid
}

// BindRawV3Descriptor verifies the content guard through the descriptor and
// returns its immutable identity at the instant before streaming begins.
func BindRawV3Descriptor(file *os.File) (RawV3DescriptorIdentity, bool) {
	if file == nil {
		return RawV3DescriptorIdentity{}, false
	}
	info, err := file.Stat()
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.Mode().IsRegular() {
		return RawV3DescriptorIdentity{}, false
	}
	guard, available := readRawFileGuard(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if !available || guard.rawSize != info.Size() || guard.rawMtime != fileMtime(info) {
		return RawV3DescriptorIdentity{}, false
	}
	return RawV3DescriptorIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: info.Size(), Mtime: fileMtime(info)}, true
}

// VerifyRawV3Descriptor proves the same descriptor and path still identify the
// guarded bytes after streaming. It rejects append, rewrite, and replacement.
func VerifyRawV3Descriptor(file *os.File, path string, before RawV3DescriptorIdentity, bytesRead int64) bool {
	current, ok := BindRawV3Descriptor(file)
	if !ok || current != before || current.Size != bytesRead {
		return false
	}
	pathInfo, err := os.Stat(path)
	pathStat, pathOK := infoSys(pathInfo)
	return err == nil && pathOK && uint64(pathStat.Dev) == before.Device && pathStat.Ino == before.Inode
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
