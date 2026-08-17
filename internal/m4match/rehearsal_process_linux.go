//go:build linux

package m4match

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const maxDotaExecutableBytes = int64(1 << 30)

type RehearsalDotaIdentityV1 struct {
	SchemaVersion        string `json:"schema_version"`
	PID                  int    `json:"pid"`
	Comm                 string `json:"comm"`
	ExecutablePath       string `json:"executable_path"`
	ExecutablePathSHA256 string `json:"executable_path_sha256"`
	ExecutableSHA256     string `json:"executable_sha256"`
	ProcessStartTicks    uint64 `json:"process_start_ticks"`
	ExecutableDevice     uint64 `json:"executable_device"`
	ExecutableInode      uint64 `json:"executable_inode"`
}

func discoverRehearsalDotaIdentity() (RehearsalDotaIdentityV1, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return RehearsalDotaIdentityV1{}, err
	}
	var pids []int
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		comm, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if readErr == nil && strings.TrimSpace(string(comm)) == "dota2" {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	if len(pids) != 1 {
		return RehearsalDotaIdentityV1{}, fmt.Errorf("dota_identity_unavailable: found %d exact dota2 processes", len(pids))
	}
	return readRehearsalDotaIdentityAt("/proc", pids[0])
}

func readRehearsalDotaIdentityAt(procRoot string, pid int) (RehearsalDotaIdentityV1, error) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	commBytes, err := os.ReadFile(filepath.Join(base, "comm"))
	if err != nil || strings.TrimSpace(string(commBytes)) != "dota2" {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_unavailable")
	}
	startBefore, err := processStartTicks(filepath.Join(base, "stat"))
	if err != nil {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_unavailable")
	}
	exeLink := filepath.Join(base, "exe")
	pathBefore, err := os.Readlink(exeLink)
	if err != nil || pathBefore == "" {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_unavailable")
	}
	descriptor, err := os.Open(exeLink)
	if err != nil {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_unavailable")
	}
	defer descriptor.Close()
	pre, err := descriptor.Stat()
	if err != nil || !pre.Mode().IsRegular() || pre.Size() <= 0 || pre.Size() > maxDotaExecutableBytes {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_unavailable")
	}
	preStat, ok := pre.Sys().(*syscall.Stat_t)
	if !ok {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_unavailable")
	}
	hash := sha256.New()
	read, hashErr := io.Copy(hash, io.LimitReader(descriptor, maxDotaExecutableBytes+1))
	post, postErr := descriptor.Stat()
	pathAfter, linkErr := os.Readlink(exeLink)
	startAfter, startErr := processStartTicks(filepath.Join(base, "stat"))
	if hashErr != nil || postErr != nil || linkErr != nil || startErr != nil || read != pre.Size() || !os.SameFile(pre, post) || pre.Size() != post.Size() || pathBefore != pathAfter || startBefore != startAfter {
		return RehearsalDotaIdentityV1{}, errors.New("dota_identity_changed_during_acquisition")
	}
	pathHash := sha256.Sum256([]byte(pathBefore))
	return RehearsalDotaIdentityV1{
		SchemaVersion: "rehearsal_dota_identity.v1", PID: pid, Comm: "dota2", ExecutablePath: pathBefore,
		ExecutablePathSHA256: hex.EncodeToString(pathHash[:]), ExecutableSHA256: hex.EncodeToString(hash.Sum(nil)), ProcessStartTicks: startBefore,
		ExecutableDevice: uint64(preStat.Dev), ExecutableInode: preStat.Ino,
	}, nil
}

func processStartTicks(path string) (uint64, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	close := strings.LastIndexByte(string(payload), ')')
	if close < 0 || close+2 >= len(payload) {
		return 0, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(payload[close+2:]))
	if len(fields) <= 19 {
		return 0, errors.New("short proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func sameRehearsalDotaIdentity(left, right RehearsalDotaIdentityV1) bool {
	return left.SchemaVersion == right.SchemaVersion && left.PID == right.PID && left.Comm == right.Comm &&
		left.ExecutablePath == right.ExecutablePath && left.ExecutablePathSHA256 == right.ExecutablePathSHA256 &&
		left.ExecutableSHA256 == right.ExecutableSHA256 && left.ProcessStartTicks == right.ProcessStartTicks &&
		left.ExecutableDevice == right.ExecutableDevice && left.ExecutableInode == right.ExecutableInode
}
