package m4match

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func waitStableProcessCorrelation(ctx context.Context, harnessPID, productPID, obsPID int, timeout time.Duration) (processCorrelation, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var previous processCorrelation
	for {
		current, err := captureProcessCorrelation(harnessPID, productPID, obsPID)
		if err == nil && current.Processes > 3 && current.SHA256 != "" && current.SHA256 == previous.SHA256 {
			return current, nil
		}
		if err == nil {
			previous = current
		} else {
			previous = processCorrelation{}
		}
		select {
		case <-ctx.Done():
			return processCorrelation{}, ctx.Err()
		case <-deadline.C:
			return processCorrelation{}, errors.New("process correlation did not stabilize")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type processCorrelation struct {
	SHA256    string `json:"sha256"`
	Processes int    `json:"processes"`
}

type processCorrelationIdentity struct {
	PID              int    `json:"pid"`
	ParentPID        int    `json:"parent_pid"`
	StartTicks       uint64 `json:"start_ticks"`
	Comm             string `json:"comm"`
	ExecutableSHA256 string `json:"executable_sha256"`
	CommandSHA256    string `json:"command_sha256"`
}

func proveProductListener(address string, productPID int) error {
	listener, err := net.Listen("tcp", address)
	if err == nil {
		_ = listener.Close()
		return errors.New("capture listener is not occupied")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("capture listener bind failed for a reason other than EADDRINUSE: %w", err)
	}
	inodes, err := listeningSocketInodes("/proc", address)
	if err != nil || len(inodes) != 1 {
		return errors.New("capture listener socket identity is missing or ambiguous")
	}
	owned, err := processSocketInodes("/proc", productPID)
	if err != nil {
		return fmt.Errorf("capture listener owner cannot be inspected: %w", err)
	}
	if !owned[inodes[0]] {
		return fmt.Errorf("capture listener inode %s is not owned by launched product; owned=%v", inodes[0], owned)
	}
	return nil
}

func listeningSocketInodes(procRoot, address string) ([]string, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || net.ParseIP(host).To4() == nil {
		return nil, errors.New("capture listener address is not canonical IPv4")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	wantAddress := fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], port)
	file, err := os.Open(filepath.Join(procRoot, "net/tcp"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []string
	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
	} // header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return nil, errors.New("malformed /proc/net/tcp row")
		}
		if fields[1] == wantAddress && fields[3] == "0A" {
			result = append(result, fields[9])
		}
	}
	sort.Strings(result)
	return result, scanner.Err()
}

func processSocketInodes(procRoot string, pid int) (map[string]bool, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		result, err := processSocketInodesOnce(procRoot, pid)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, lastErr
}

func processSocketInodesOnce(procRoot string, pid int) (map[string]bool, error) {
	directory, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "fd"))
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "fd", entry))
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			result[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	return result, nil
}

func captureProcessCorrelation(harnessPID, productPID, obsPID int) (processCorrelation, error) {
	return captureProcessCorrelationAt("/proc", harnessPID, productPID, obsPID)
}

func captureProcessCorrelationAt(procRoot string, harnessPID, productPID, obsPID int) (processCorrelation, error) {
	children, err := processChildren(procRoot, harnessPID)
	if err != nil || len(children) != 2 || !containsPID(children, productPID) || !containsPID(children, obsPID) {
		return processCorrelation{}, fmt.Errorf("harness process tree has an unreadable or unexpected direct child: children=%v err=%v", children, err)
	}
	productChildren, err := processChildren(procRoot, productPID)
	if err != nil || len(productChildren) != 0 {
		return processCorrelation{}, errors.New("product process tree changed or has an unexpected producer")
	}
	all := []int{harnessPID, productPID, obsPID}
	obsTree, err := descendantPIDs(procRoot, obsPID)
	if err != nil {
		return processCorrelation{}, err
	}
	all = append(all, obsTree...)
	sort.Ints(all)
	identities := make([]processCorrelationIdentity, 0, len(all))
	for _, pid := range all {
		identity, err := readProcessCorrelationIdentity(procRoot, pid)
		if err != nil {
			return processCorrelation{}, errors.New("process correlation entry is unreadable")
		}
		lower := strings.ToLower(identity.Comm)
		if pid != harnessPID && pid != productPID && pid != obsPID && !allowedOBSHelper(lower) {
			return processCorrelation{}, errors.New("renamed or unknown producer exists in the invocation tree")
		}
		identities = append(identities, identity)
	}
	payload, err := canonical(identities)
	if err != nil {
		return processCorrelation{}, err
	}
	return processCorrelation{SHA256: payloadSHA(payload), Processes: len(identities)}, nil
}

func allowedOBSHelper(comm string) bool {
	for _, allowed := range []string{"obs", "obs64", "obs-browser", "obs-browser-page", "cef", "bwrap", "flatpak", "dbus", "ffmpeg", "gpu-process", "utility", "zypak-helper", "chrome-sandbox"} {
		if strings.Contains(comm, allowed) {
			return true
		}
	}
	return false
}

func processChildren(procRoot string, pid int) ([]int, error) {
	tasks, err := os.ReadDir(filepath.Join(procRoot, strconv.Itoa(pid), "task"))
	if err != nil {
		return nil, err
	}
	unique := map[int]bool{}
	for _, task := range tasks {
		payload, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "task", task.Name(), "children"))
		if err != nil {
			return nil, err
		}
		for _, field := range strings.Fields(string(payload)) {
			child, err := strconv.Atoi(field)
			if err != nil {
				return nil, err
			}
			unique[child] = true
		}
	}
	children := make([]int, 0, len(unique))
	for child := range unique {
		children = append(children, child)
	}
	sort.Ints(children)
	return children, nil
}

func descendantPIDs(procRoot string, root int) ([]int, error) {
	queue := []int{root}
	seen := map[int]bool{root: true}
	var result []int
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		children, err := processChildren(procRoot, pid)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if seen[child] {
				return nil, errors.New("process tree cycle")
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	return result, nil
}

func readProcessCorrelationIdentity(procRoot string, pid int) (processCorrelationIdentity, error) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return processCorrelationIdentity{}, err
	}
	closeIndex := strings.LastIndex(string(stat), ")")
	if closeIndex < 0 {
		return processCorrelationIdentity{}, errors.New("malformed stat")
	}
	fields := strings.Fields(string(stat)[closeIndex+1:])
	if len(fields) <= 19 {
		return processCorrelationIdentity{}, errors.New("short stat")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return processCorrelationIdentity{}, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processCorrelationIdentity{}, err
	}
	comm, err := os.ReadFile(filepath.Join(base, "comm"))
	if err != nil {
		return processCorrelationIdentity{}, err
	}
	command, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil {
		return processCorrelationIdentity{}, err
	}
	if _, err := os.Readlink(filepath.Join(base, "exe")); err != nil {
		return processCorrelationIdentity{}, err
	}
	// Hash through the process descriptor. A Flatpak executable may be named
	// /app/bin/obs inside its mount namespace and have no corresponding host
	// path, while /proc/<pid>/exe remains an exact opened executable identity.
	executableHash, _, err := fileSHA(filepath.Join(base, "exe"))
	if err != nil {
		return processCorrelationIdentity{}, err
	}
	return processCorrelationIdentity{PID: pid, ParentPID: ppid, StartTicks: start, Comm: strings.TrimSpace(string(comm)), ExecutableSHA256: executableHash, CommandSHA256: payloadSHA(command)}, nil
}

func containsPID(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
