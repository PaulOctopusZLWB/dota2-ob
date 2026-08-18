//go:build linux

package m4match

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type flatpakOBSInstance struct {
	InstanceID  string
	WrapperPID  int
	SandboxPID  int
	OBSPID      int
	Application string
}

type ownedFlatpakOBS struct {
	flatpakOBSInstance
	Root         string
	Launcher     *exec.Cmd
	LauncherDone <-chan error
	Identity     RehearsalOwnedProcessIdentityV1
}

var flatpakOBSInstanceLister = listFlatpakOBSInstances

func listFlatpakOBSInstances(ctx context.Context) ([]flatpakOBSInstance, error) {
	command := exec.CommandContext(ctx, "flatpak", "ps", "--columns=instance,pid,child-pid,application")
	payload, err := command.Output()
	if err != nil {
		return nil, err
	}
	var result []flatpakOBSInstance
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 4 || fields[3] != "com.obsproject.Studio" {
			continue
		}
		wrapper, wrapperErr := strconv.Atoi(fields[1])
		child, childErr := strconv.Atoi(fields[2])
		if wrapperErr != nil || childErr != nil || fields[0] == "" || wrapper <= 1 || child <= 1 {
			return nil, errors.New("Flatpak OBS process metadata malformed")
		}
		result = append(result, flatpakOBSInstance{InstanceID: fields[0], WrapperPID: wrapper, SandboxPID: child, Application: fields[3]})
	}
	return result, scanner.Err()
}

func startOwnedFlatpakOBS(ctx context.Context, root string, output io.Writer) (*ownedFlatpakOBS, error) {
	existing, err := flatpakOBSInstanceLister(ctx)
	if err != nil {
		return nil, err
	}
	if len(existing) != 0 {
		return nil, errors.New("pre-existing OBS Flatpak instance present")
	}
	listener, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", obsWebSocketPort))
	if listenErr != nil {
		return nil, errors.New("exclusive OBS control listener unavailable")
	}
	_ = listener.Close()
	readID, writeID, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readID.Close()
	home := filepath.Join(root, "obs-home")
	config := filepath.Join(root, "config")
	data := filepath.Join(root, "data")
	cache := filepath.Join(root, "cache")
	for _, directory := range []string{home, config, data, cache} {
		if err := rootMkdirAll(directory, 0o700); err != nil {
			writeID.Close()
			return nil, err
		}
	}
	script := `export HOME="$1" XDG_CONFIG_HOME="$2" XDG_DATA_HOME="$3" XDG_CACHE_HOME="$4"; shift 4; exec obs "$@"`
	command := exec.Command("flatpak", "run", "--instance-id-fd=3", "--nofilesystem=home", "--filesystem="+root,
		"--command=sh", "com.obsproject.Studio", "-c", script, "sh", home, config, data, cache,
		"--multi", "--profile", "DOT65-P4", "--collection", "DOT65-P4", "--startrecording")
	command.Dir, command.Stdout, command.Stderr, command.ExtraFiles = root, output, output, []*os.File{writeID}
	if err := command.Start(); err != nil {
		writeID.Close()
		return nil, err
	}
	_ = writeID.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	idResult := make(chan struct {
		id  string
		err error
	}, 1)
	go func() {
		payload, readErr := io.ReadAll(io.LimitReader(readID, 257))
		idResult <- struct {
			id  string
			err error
		}{strings.TrimSpace(string(payload)), readErr}
	}()
	var instanceID string
	select {
	case result := <-idResult:
		if result.err != nil || result.id == "" || len(result.id) > 256 {
			_ = command.Process.Kill()
			return nil, errors.New("Flatpak instance ID unavailable")
		}
		instanceID = result.id
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		return nil, errors.New("Flatpak instance ID deadline exceeded")
	}
	owned := &ownedFlatpakOBS{flatpakOBSInstance: flatpakOBSInstance{InstanceID: instanceID}, Root: root, Launcher: command, LauncherDone: done}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		instances, listErr := flatpakOBSInstanceLister(ctx)
		if listErr == nil {
			var match *flatpakOBSInstance
			for index := range instances {
				if instances[index].InstanceID == instanceID {
					copy := instances[index]
					match = &copy
				} else {
					_ = stopOwnedFlatpakOBS(context.Background(), owned, 30*time.Second)
					return nil, errors.New("unrelated OBS instance appeared during launch")
				}
			}
			if match != nil {
				realPID, identity, identityErr := discoverRealOBSProcess(match.SandboxPID)
				if identityErr == nil {
					match.OBSPID = realPID
					owned.flatpakOBSInstance, owned.Identity = *match, identity
					if envErr := verifyOBSRootEnvironment(realPID, root); envErr != nil {
						_ = stopOwnedFlatpakOBS(context.Background(), owned, 30*time.Second)
						return nil, envErr
					}
					return owned, nil
				}
			}
		}
		select {
		case launchErr := <-done:
			return nil, fmt.Errorf("Flatpak launcher exited before instance binding: %w", launchErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = stopOwnedFlatpakOBS(context.Background(), owned, 30*time.Second)
	return nil, errors.New("real OBS process identity deadline exceeded")
}

func discoverRealOBSProcess(sandboxPID int) (int, RehearsalOwnedProcessIdentityV1, error) {
	descendants, err := descendantPIDs("/proc", sandboxPID)
	if err != nil {
		return 0, RehearsalOwnedProcessIdentityV1{}, err
	}
	var pid int
	for _, candidate := range descendants {
		comm, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(candidate), "comm"))
		if readErr != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(comm)))
		if name == "obs" || name == "obs64" {
			if pid != 0 {
				return 0, RehearsalOwnedProcessIdentityV1{}, errors.New("multiple real OBS process candidates")
			}
			pid = candidate
		}
	}
	if pid == 0 {
		return 0, RehearsalOwnedProcessIdentityV1{}, errors.New("real OBS process not present in Flatpak sandbox")
	}
	identity, err := readRehearsalOwnedProcessIdentity(pid)
	return pid, identity, err
}

func verifyOBSRootEnvironment(pid int, root string) error {
	payload, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, entry := range strings.Split(string(payload), "\x00") {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	want := map[string]string{"HOME": filepath.Join(root, "obs-home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache")}
	for key, value := range want {
		if values[key] != value {
			return fmt.Errorf("OBS sandbox %s is not root-isolated", key)
		}
	}
	return nil
}

func captureOwnedFlatpakCorrelation(ctx context.Context, productPID int, obs *ownedFlatpakOBS) (processCorrelation, error) {
	instances, err := flatpakOBSInstanceLister(ctx)
	if err != nil || len(instances) != 1 || instances[0].InstanceID != obs.InstanceID || instances[0].WrapperPID != obs.WrapperPID || instances[0].SandboxPID != obs.SandboxPID {
		return processCorrelation{}, errors.New("owned Flatpak OBS instance identity changed")
	}
	current, err := readRehearsalOwnedProcessIdentity(obs.OBSPID)
	if err != nil || !sameRehearsalOwnedProcessIdentity(current, obs.Identity) {
		return processCorrelation{}, errors.New("real OBS process identity changed")
	}
	all := []int{productPID, obs.WrapperPID, obs.SandboxPID}
	descendants, err := descendantPIDs("/proc", obs.SandboxPID)
	if err != nil {
		return processCorrelation{}, err
	}
	all = append(all, descendants...)
	sort.Ints(all)
	identities := make([]processCorrelationIdentity, 0, len(all))
	for _, pid := range all {
		identity, readErr := readProcessCorrelationIdentity("/proc", pid)
		if readErr != nil {
			return processCorrelation{}, readErr
		}
		if pid != productPID && pid != obs.OBSPID && pid != obs.WrapperPID && pid != obs.SandboxPID && !allowedOBSHelper(strings.ToLower(identity.Comm)) {
			return processCorrelation{}, errors.New("unknown process in owned OBS instance topology")
		}
		identities = append(identities, identity)
	}
	payload, err := canonical(struct {
		Instance  string                       `json:"instance"`
		Processes []processCorrelationIdentity `json:"processes"`
	}{obs.InstanceID, identities})
	if err != nil {
		return processCorrelation{}, err
	}
	return processCorrelation{SHA256: payloadSHA(payload), Processes: len(identities)}, nil
}

func waitOwnedFlatpakCorrelation(ctx context.Context, productPID int, obs *ownedFlatpakOBS, timeout time.Duration) (processCorrelation, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var previous processCorrelation
	for {
		current, err := captureOwnedFlatpakCorrelation(ctx, productPID, obs)
		if err == nil && current.SHA256 != "" && current.SHA256 == previous.SHA256 {
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
			return processCorrelation{}, errors.New("owned Flatpak OBS topology did not stabilize")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitOBSRecordingStarted(ctx context.Context, root string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		logPayload, _ := os.ReadFile(filepath.Join(root, "evidence/logs/obs-live.log"))
		recordings, _ := filepath.Glob(filepath.Join(root, "recordings", "*.mkv"))
		if strings.Contains(string(logPayload), "==== Recording Start") && len(recordings) == 1 {
			if info, err := os.Stat(recordings[0]); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("OBS recording readiness deadline exceeded")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func stopOwnedFlatpakOBS(ctx context.Context, obs *ownedFlatpakOBS, timeout time.Duration) error {
	if obs == nil || obs.InstanceID == "" {
		return nil
	}
	current, identityErr := readRehearsalOwnedProcessIdentity(obs.OBSPID)
	if identityErr != nil || !sameRehearsalOwnedProcessIdentity(current, obs.Identity) {
		return errors.New("refusing to signal changed OBS process identity")
	}
	recordingErr := stopOBSRecording(ctx, obs.Root, timeout/2)
	process, findErr := os.FindProcess(obs.OBSPID)
	if findErr != nil {
		return findErr
	}
	signalErr := process.Signal(syscall.SIGINT)
	deadline := time.Now().Add(timeout)
	graceDeadline := time.Now().Add(timeout * 2 / 3)
	waitGone := func(until time.Time) bool {
		for time.Now().Before(until) {
			instances, listErr := flatpakOBSInstanceLister(context.Background())
			if listErr != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			present := false
			for _, instance := range instances {
				present = present || instance.InstanceID == obs.InstanceID
			}
			identity, err := readRehearsalOwnedProcessIdentity(obs.OBSPID)
			if !present && (err != nil || !sameRehearsalOwnedProcessIdentity(identity, obs.Identity)) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	if signalErr == nil && waitGone(graceDeadline) {
		return recordingErr
	}
	command := exec.CommandContext(ctx, "flatpak", "kill", obs.InstanceID)
	killErr := command.Run()
	if waitGone(deadline) {
		return errors.Join(recordingErr, signalErr, killErr)
	}
	return errors.Join(recordingErr, signalErr, killErr, errors.New("owned OBS instance did not terminate before deadline"))
}
