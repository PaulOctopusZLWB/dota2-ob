package m4match

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type RehearsalDotaIdentityV1 struct {
	PID                  int    `json:"pid"`
	Comm                 string `json:"comm"`
	ExecutableSHA256     string `json:"executable_sha256"`
	ExecutablePathSHA256 string `json:"executable_path_sha256"`
	StartTicks           uint64 `json:"start_ticks"`
}
type RehearsalDotaObservationV1 struct {
	SourceSequence uint64                  `json:"source_sequence"`
	Transition     string                  `json:"transition"`
	Identity       RehearsalDotaIdentityV1 `json:"identity"`
}

func acquireRehearsalDota(procRoot string) (RehearsalDotaIdentityV1, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return RehearsalDotaIdentityV1{}, err
	}
	pids := []int{}
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || !entry.IsDir() {
			continue
		}
		comm, readErr := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if readErr == nil && strings.ToLower(strings.TrimSpace(string(comm))) == "dota2" {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	if len(pids) != 1 {
		return RehearsalDotaIdentityV1{}, errors.New("exactly one manually started Dota process is required")
	}
	return readRehearsalDotaIdentity(procRoot, pids[0])
}

func readRehearsalDotaIdentity(procRoot string, pid int) (RehearsalDotaIdentityV1, error) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	commBytes, err := os.ReadFile(filepath.Join(base, "comm"))
	if err != nil {
		return RehearsalDotaIdentityV1{}, err
	}
	comm := strings.TrimSpace(string(commBytes))
	if strings.ToLower(comm) != "dota2" {
		return RehearsalDotaIdentityV1{}, errors.New("process is not Dota")
	}
	executablePath, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil {
		return RehearsalDotaIdentityV1{}, err
	}
	executableSHA, _, err := fileSHA(executablePath)
	if err != nil {
		return RehearsalDotaIdentityV1{}, err
	}
	statBytes, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return RehearsalDotaIdentityV1{}, err
	}
	closeIndex := strings.LastIndex(string(statBytes), ")")
	if closeIndex < 0 {
		return RehearsalDotaIdentityV1{}, errors.New("malformed Dota stat")
	}
	fields := strings.Fields(string(statBytes)[closeIndex+1:])
	if len(fields) <= 19 {
		return RehearsalDotaIdentityV1{}, errors.New("short Dota stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return RehearsalDotaIdentityV1{}, errors.New("invalid Dota start ticks")
	}
	return RehearsalDotaIdentityV1{PID: pid, Comm: comm, ExecutableSHA256: executableSHA, ExecutablePathSHA256: payloadSHA([]byte(filepath.Clean(executablePath))), StartTicks: start}, nil
}

func observeBoundRehearsalDota(procRoot string, bound RehearsalDotaIdentityV1, sequence uint64, transition string) (RehearsalDotaObservationV1, error) {
	current, err := readRehearsalDotaIdentity(procRoot, bound.PID)
	if err != nil {
		return RehearsalDotaObservationV1{}, err
	}
	if current.PID != bound.PID || current.Comm != bound.Comm || current.ExecutableSHA256 != bound.ExecutableSHA256 || current.ExecutablePathSHA256 != bound.ExecutablePathSHA256 || current.StartTicks != bound.StartTicks {
		return RehearsalDotaObservationV1{}, errors.New("bound Dota identity changed")
	}
	if transition == "" || (sequence == 0 && transition == "raw") {
		return RehearsalDotaObservationV1{}, errors.New("invalid Dota observation transition")
	}
	return RehearsalDotaObservationV1{SourceSequence: sequence, Transition: transition, Identity: current}, nil
}
