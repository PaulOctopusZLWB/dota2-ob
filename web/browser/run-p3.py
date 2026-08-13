#!/usr/bin/env python3
"""Run protocol P3 with generated OBS state and sanitized evidence only."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import signal
import statistics
import subprocess
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

import numpy as np


REPO = Path(__file__).resolve().parents[2]
RUN = REPO / ".dot24-p3-run"
EVIDENCE = REPO / "docs/evidence/m3"
BASELINE_SECONDS = int(os.environ.get("DOTA2_OB_P3_BASELINE_SECONDS", "600"))
WARMUP_SECONDS = int(os.environ.get("DOTA2_OB_P3_WARMUP_SECONDS", "600"))
RECORD_SECONDS = int(os.environ.get("DOTA2_OB_P3_RECORD_SECONDS", "3600"))
SAMPLE_SECONDS = int(os.environ.get("DOTA2_OB_P3_SAMPLE_SECONDS", "5"))
PASSWORD_RE = re.compile(r"(?i)(password|token|authorization)[^\s,;]*")


def run(command: list[str], *, check: bool = True, text: bool = True, **kwargs):
    return subprocess.run(command, check=check, text=text, **kwargs)


def output(command: list[str]) -> str:
    return subprocess.check_output(command, text=True).strip()


def wait_until(predicate, timeout: float, message: str, interval: float = 0.1):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(interval)
    raise RuntimeError(message)


def obs_pids() -> list[int]:
    result = []
    for item in Path("/proc").iterdir():
        if not item.name.isdigit():
            continue
        try:
            comm = (item / "comm").read_text().strip()
            command = (item / "cmdline").read_bytes().replace(b"\0", b" ").decode("utf-8", "replace")
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
        if comm == "obs" or "obs-browser-page" in command:
            result.append(int(item.name))
    return sorted(result)


def process_kind(pid: int) -> str:
    try:
        command = Path(f"/proc/{pid}/cmdline").read_bytes()
        return "browser" if b"obs-browser-page" in command else "obs"
    except (FileNotFoundError, PermissionError):
        return "gone"


def ticks(pid: int) -> int:
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().split()
        return int(fields[13]) + int(fields[14])
    except (FileNotFoundError, PermissionError, IndexError, ValueError):
        return 0


def pss_kib(pid: int) -> int:
    try:
        match = re.search(r"^Pss:\s+(\d+) kB$", Path(f"/proc/{pid}/smaps_rollup").read_text(), re.MULTILINE)
        return int(match.group(1)) if match else 0
    except (FileNotFoundError, PermissionError):
        return 0


def gpu_mib(pids: set[int]) -> int:
    result = run(["nvidia-smi", "pmon", "-c", "1", "-s", "m"], capture_output=True, check=False)
    total = 0
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) >= 5 and fields[1].isdigit() and int(fields[1]) in pids and fields[3].isdigit():
            total += int(fields[3])
    return total


def socket_peers() -> list[str]:
    result = run(["ss", "-Hntp"], capture_output=True, check=False)
    peers = set()
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) >= 5:
            peers.add(fields[4])
    return sorted(peers)


def remote_peer(peer: str) -> bool:
    return not (peer.startswith("127.0.0.1:") or peer.startswith("[::1]:"))


def probe_mode() -> str:
    try:
        with urllib.request.urlopen("http://127.0.0.1:18838/v1/overlay/state", timeout=1) as response:
            body = response.read(70 * 1024)
        try:
            state = json.loads(body)
        except json.JSONDecodeError:
            return "malformed"
        if state.get("visibility") == "hidden":
            return state.get("health_code", "hidden")
        key = state.get("claim", {}).get("asset_key", "")
        if key == "missing-local-asset":
            return "missing-asset"
        if state.get("stale_deadline_ms", 0) < int(time.time() * 1000):
            return "stale"
        return key or "visible"
    except Exception:
        return "disconnect"


def window_for(profile: str) -> str:
    result = run(["xdotool", "search", "--onlyvisible", "--class", "obs"], capture_output=True, check=False)
    for window in result.stdout.split():
        name = run(["xdotool", "getwindowname", window], capture_output=True, check=False).stdout
        if f"Profile: {profile}" in name:
            return window
    return ""


def launch_obs(profile: str, launcher_log: Path, *, record: bool = False):
    script = (
        'export XDG_CONFIG_HOME="$1/config"; export XDG_DATA_HOME="$1/data"; '
        'export XDG_CACHE_HOME="$1/cache"; '
        'exec obs --multi --profile "$2" --collection "$2" --scene "DOT24 P3 Scene" '
        '--disable-missing-files-check --verbose'
    )
    stream = launcher_log.open("w", encoding="utf-8")
    command = ["flatpak", "run", "--user", "--command=sh", "com.obsproject.Studio", "-c", script, "sh", str(RUN), profile]
    if record:
        script += ' --startrecording'
        command = ["flatpak", "run", "--user", "--command=sh", "com.obsproject.Studio", "-c", script, "sh", str(RUN), profile]
    process = subprocess.Popen(
        command,
        stdout=stream, stderr=subprocess.STDOUT, text=True,
    )
    window = wait_until(lambda: window_for(profile), 45, f"OBS window unavailable for {profile}", 0.2)
    wait_until(lambda: any(process_kind(pid) == "obs" for pid in obs_pids()), 10, "OBS process unavailable")
    return process, stream, window


def recording_files() -> set[Path]:
    return set((RUN / "recordings").glob("*.mkv"))


def started_recording(before: set[Path]) -> tuple[Path, float]:
    found = wait_until(lambda: list(recording_files() - before), 20, "recording did not start")
    return found[0], found[0].stat().st_mtime


def stop_obs(process: subprocess.Popen, stream) -> None:
    current = obs_pids()
    main = [pid for pid in current if process_kind(pid) == "obs"]
    if main:
        os.kill(main[0], signal.SIGINT)
    try:
        process.wait(timeout=20)
    except subprocess.TimeoutExpired:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
    stream.close()


def latest_obs_log() -> Path:
    logs = list((RUN / "config/obs-studio/logs").glob("*.txt"))
    if not logs:
        raise RuntimeError("OBS log missing")
    return max(logs, key=lambda path: path.stat().st_mtime_ns)


def collect_phase(name: str, duration: int, recording: Path | None = None) -> list[dict]:
    samples = []
    hz = os.sysconf("SC_CLK_TCK")
    previous_ticks = None
    previous_ticks_by_kind = None
    previous_at = None
    start = time.monotonic()
    next_sample = start
    while True:
        now = time.monotonic()
        if now < next_sample:
            time.sleep(min(next_sample - now, 0.25))
            continue
        elapsed = now - start
        if elapsed > duration and samples:
            break
        if recording is not None:
            try:
                recording_age = time.time() - recording.stat().st_mtime
            except FileNotFoundError as error:
                raise RuntimeError(f"{name} recording disappeared") from error
            # Matroska writes may remain buffered for more than 30 seconds on
            # this stack. Two minutes still detects a stopped recording early
            # without treating normal muxer flush cadence as failure.
            if recording_age > max(120, SAMPLE_SECONDS * 4):
                raise RuntimeError(f"{name} recording stopped updating {recording_age:.1f}s ago")
        pids = obs_pids()
        ticks_by_kind = {
            "obs": sum(ticks(pid) for pid in pids if process_kind(pid) == "obs"),
            "browser": sum(ticks(pid) for pid in pids if process_kind(pid) == "browser"),
        }
        tick_total = sum(ticks_by_kind.values())
        cpu = None
        cpu_by_kind = {"obs": None, "browser": None}
        if previous_ticks is not None and previous_at is not None and now > previous_at:
            cpu = (tick_total - previous_ticks) * 100 / (hz * (now - previous_at))
            for kind in cpu_by_kind:
                cpu_by_kind[kind] = round((ticks_by_kind[kind] - previous_ticks_by_kind[kind]) * 100 / (hz * (now - previous_at)), 3)
        peers = socket_peers()
        pss_by_kind = {
            "obs": sum(pss_kib(pid) for pid in pids if process_kind(pid) == "obs"),
            "browser": sum(pss_kib(pid) for pid in pids if process_kind(pid) == "browser"),
        }
        samples.append({
            "phase": name,
            "at": datetime.now(timezone.utc).isoformat(),
            "elapsedSeconds": round(elapsed, 3),
            "pids": {"obs": sum(process_kind(pid) == "obs" for pid in pids), "browser": sum(process_kind(pid) == "browser" for pid in pids)},
            "pssKiB": sum(pss_by_kind.values()),
            "pssKiBByKind": pss_by_kind,
            "cpuPercentOfOneCore": None if cpu is None else round(cpu, 3),
            "cpuPercentOfOneCoreByKind": cpu_by_kind,
            "gpuFramebufferMiB": gpu_mib(set(pids)),
            "mode": probe_mode(),
            "socketPeers": peers,
            "remoteSocketPeers": [peer for peer in peers if remote_peer(peer)],
        })
        previous_ticks, previous_ticks_by_kind, previous_at = tick_total, ticks_by_kind, now
        next_sample += SAMPLE_SECONDS
    return samples


def recording_metrics(log: Path) -> dict:
    text = log.read_text(encoding="utf-8", errors="replace")
    def last(pattern: str, cast=float, default=0):
        matches = re.findall(pattern, text)
        return cast(matches[-1]) if matches else default
    return {
        "framesOutput": last(r"Total frames output: (\d+)", int),
        "drawnFrames": last(r"Total drawn frames: (\d+)", int),
        "attemptedDrawnFrames": last(r"Total drawn frames: \d+ \((\d+) attempted\)", int),
        "renderLaggedFrames": last(r"Number of lagged frames due to rendering lag/stalls: (\d+)", int),
        "renderLagPercent": last(r"Number of lagged frames due to rendering lag/stalls: \d+ \(([\d.]+)%\)"),
        "encodingSkippedFrames": last(r"number of skipped frames due to encoding lag: (\d+)", int),
        "encodingSkipPercent": last(r"number of skipped frames due to encoding lag: \d+/\d+ \(([\d.]+)%\)"),
        "graphicsThreadP99Ms": last(r"obs_graphics_thread\([^\n]+99th percentile=([\d.]+)\s*ms"),
        "crash": "crashed" in text.lower() or "fatal" in text.lower(),
    }


def probe_duration(video: Path) -> float:
    result = output(["flatpak", "run", "--user", "--command=ffprobe", "com.obsproject.Studio", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", str(video)])
    return float(result)


def crop_scores(video: Path) -> list[float]:
    width, height = 750, 450
    command = [
        "flatpak", "run", "--user", "--command=ffmpeg", "com.obsproject.Studio", "-hide_banner", "-loglevel", "error",
        "-i", str(video), "-vf", f"fps=1/{SAMPLE_SECONDS},crop={width}:{height}:1770:80",
        "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1",
    ]
    process = subprocess.Popen(command, stdout=subprocess.PIPE)
    frame_bytes = width * height * 3
    scores = []
    while True:
        data = process.stdout.read(frame_bytes) if process.stdout else b""
        if not data:
            break
        if len(data) != frame_bytes:
            raise RuntimeError("short decoded frame")
        pixels = np.frombuffer(data, dtype=np.uint8).reshape((-1, 3)).astype(np.float32)
        scores.append(float(np.std(pixels, axis=0).mean()))
    if process.wait() != 0:
        raise RuntimeError("ffmpeg visibility decode failed")
    return scores


def parse_events(log: Path) -> list[dict]:
    events = []
    for line in log.read_text(encoding="utf-8").splitlines():
        value = json.loads(line)
        if "mode" in value:
            value["epoch"] = datetime.fromisoformat(value["at"].replace("Z", "+00:00")).timestamp()
            events.append(value)
    return events


def mode_at(events: list[dict], epoch: float) -> tuple[str, float, float | None]:
    selected = {"mode": "unknown", "epoch": epoch}
    selected_index = -1
    for index, event in enumerate(events):
        if event["epoch"] <= epoch:
            selected = event
            selected_index = index
        else:
            break
    remaining = None
    if selected_index >= 0 and selected_index + 1 < len(events):
        remaining = events[selected_index + 1]["epoch"] - epoch
    return selected["mode"], epoch - selected["epoch"], remaining


def extract_frame(video: Path, seconds: float, target: Path) -> None:
    run(["flatpak", "run", "--user", "--command=ffmpeg", "com.obsproject.Studio", "-hide_banner", "-loglevel", "error", "-y", "-ss", f"{seconds:.3f}", "-i", str(video), "-frames:v", "1", str(target)])


def stack_info(log: Path) -> dict:
    text = log.read_text(encoding="utf-8", errors="replace")
    def find(pattern: str, default="unknown"):
        match = re.search(pattern, text)
        return match.group(1).strip() if match else default
    flatpak = output(["flatpak", "info", "--user", "--show-commit", "com.obsproject.Studio"])
    return {
        "obs": output(["flatpak", "run", "--user", "--command=obs", "com.obsproject.Studio", "--version"]).replace("OBS Studio - ", ""),
        "browserSource": find(r"obs-browser[^\n]*?Version[: ]+([^\s]+)"),
        "cef": find(r"CEF Version ([^ ]+)"),
        "flatpakCommit": flatpak,
        "gpu": output(["nvidia-smi", "--query-gpu=name", "--format=csv,noheader"]),
        "driver": output(["nvidia-smi", "--query-gpu=driver_version", "--format=csv,noheader"]),
        "ffmpeg": output(["flatpak", "run", "--user", "--command=ffmpeg", "com.obsproject.Studio", "-version"]).splitlines()[0],
    }


def source_identity() -> dict:
    diff = subprocess.check_output(["git", "diff", "--binary", "HEAD"], cwd=REPO)
    return {"baseCommit": output(["git", "-C", str(REPO), "rev-parse", "HEAD"]), "sourceDiffSHA256": hashlib.sha256(diff).hexdigest()}


def read_optional(path: str, default: str = "unknown") -> str:
    try:
        return Path(path).read_text(encoding="utf-8").strip() or default
    except (FileNotFoundError, PermissionError):
        return default


def process_count() -> int:
    return sum(path.name.isdigit() for path in Path("/proc").iterdir())


def main() -> int:
    if RUN.exists():
        raise RuntimeError(f"refusing existing run root: {RUN}")
    if obs_pids():
        raise RuntimeError("OBS is already running")
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    RUN.mkdir(mode=0o700)
    server = None
    baseline_obs = overlay_obs = None
    baseline_stream = overlay_stream = None
    try:
        run([sys.executable, str(REPO / "web/browser/prepare-p3-obs.py"), "--root", str(RUN), "--recording-dir", str(RUN / "recordings")])
        env = os.environ.copy()
        env["DOTA2_OB_P3_LOG"] = str(RUN / "server.jsonl")
        server_stream = (RUN / "server.out").open("w", encoding="utf-8")
        server = subprocess.Popen(["node", str(REPO / "web/browser/p3-server.mjs")], cwd=REPO, env=env, stdout=server_stream, stderr=subprocess.STDOUT)
        wait_until(lambda: probe_mode() != "disconnect", 10, "P3 server unavailable")

        before = recording_files()
        baseline_obs, baseline_stream, _ = launch_obs("DOT24-P3-Empty", RUN / "baseline-launcher.log", record=True)
        baseline_video, baseline_started = started_recording(before)
        baseline_samples = collect_phase("empty-baseline", BASELINE_SECONDS, baseline_video)
        stop_obs(baseline_obs, baseline_stream)
        baseline_obs = baseline_stream = None
        baseline_log = RUN / "baseline-obs.log"
        shutil.copy2(latest_obs_log(), baseline_log)

        before = recording_files()
        overlay_obs, overlay_stream, _ = launch_obs("DOT24-P3-Overlay", RUN / "overlay-launcher.log", record=True)
        overlay_video, overlay_started = started_recording(before)
        warmup_samples = collect_phase("overlay-warmup", WARMUP_SECONDS, overlay_video)
        recording_samples = collect_phase("overlay-recording", RECORD_SECONDS, overlay_video)
        stop_obs(overlay_obs, overlay_stream)
        overlay_obs = overlay_stream = None
        overlay_log = RUN / "overlay-obs.log"
        shutil.copy2(latest_obs_log(), overlay_log)

        if server:
            server.send_signal(signal.SIGTERM)
            server.wait(timeout=5)
            server = None
        server_stream.close()

        baseline_counters = recording_metrics(baseline_log)
        overlay_counters = recording_metrics(overlay_log)
        baseline_duration = probe_duration(baseline_video)
        overlay_duration = probe_duration(overlay_video)
        baseline_scores = crop_scores(baseline_video)
        overlay_scores = crop_scores(overlay_video)
        visibility_threshold = max(baseline_scores) + 3.0
        events = parse_events(RUN / "server.jsonl")
        unsafe = {"stale", "malformed", "missing-asset", "emergency-hide", "disconnect"}
        hidden_checks = []
        visible_checks = []
        visible_second = hidden_second = None
        for index, score in enumerate(overlay_scores):
            seconds = index * SAMPLE_SECONDS
            if seconds < WARMUP_SECONDS:
                continue
            mode, age, remaining = mode_at(events, overlay_started + seconds)
            visible = score > visibility_threshold
            # The five-second video sampler can select a frame near either side
            # of a state boundary. Guarding both edges keeps P3 frame evidence
            # unambiguous; protocol P2 separately measures the two-second SLA.
            stable_window = age >= 2.0 and remaining is not None and remaining >= 2.0
            sample = {"seconds": seconds, "mode": mode, "modeAgeSeconds": round(age, 3), "modeRemainingSeconds": None if remaining is None else round(remaining, 3), "score": round(score, 3), "visible": visible}
            if mode in unsafe and stable_window:
                hidden_checks.append(sample)
                if hidden_second is None:
                    hidden_second = seconds
            elif mode not in unsafe and mode not in {"unknown", "server-started"} and stable_window:
                visible_checks.append(sample)
                if visible_second is None:
                    visible_second = seconds
        hidden_violations = sum(item["visible"] for item in hidden_checks)
        recovery_violations = sum(not item["visible"] for item in visible_checks)

        baseline_pss = [sample["pssKiB"] for sample in baseline_samples]
        recording_pss = [sample["pssKiB"] for sample in recording_samples]
        cpu = [sample["cpuPercentOfOneCore"] for sample in recording_samples if sample["cpuPercentOfOneCore"] is not None]
        browser_cpu = [sample["cpuPercentOfOneCoreByKind"]["browser"] for sample in recording_samples if sample["cpuPercentOfOneCoreByKind"]["browser"] is not None]
        x = np.array([sample["elapsedSeconds"] / 60 for sample in recording_samples], dtype=float)
        y = np.array(recording_pss, dtype=float)
        growth_slope = float(np.polyfit(x, y, 1)[0]) if len(x) > 1 else 0.0
        remote = sorted({peer for sample in baseline_samples + warmup_samples + recording_samples for peer in sample["remoteSocketPeers"]})
        lag_delta = overlay_counters["renderLagPercent"] - baseline_counters["renderLagPercent"]

        visible_path = EVIDENCE / "p3-overlay-visible.png"
        hidden_path = EVIDENCE / "p3-overlay-hidden.png"
        if visible_second is not None:
            extract_frame(overlay_video, visible_second, visible_path)
        if hidden_second is not None:
            extract_frame(overlay_video, hidden_second, hidden_path)

        pss_increment = statistics.median(recording_pss) - statistics.median(baseline_pss)
        total_growth = max(recording_pss) - min(recording_pss)
        passed = all([
            overlay_duration >= WARMUP_SECONDS + RECORD_SECONDS - 2,
            pss_increment <= 256 * 1024,
            growth_slope <= 1024,
            total_growth <= 64 * 1024,
            statistics.median(browser_cpu) <= 5,
            lag_delta <= 1.0,
            hidden_violations == 0,
            recovery_violations == 0,
            not remote,
            not overlay_counters["crash"],
        ])
        summary = {
            "protocol": "P3", "measuredAt": datetime.now(timezone.utc).isoformat(), **source_identity(),
            "configured": {"emptyBaselineSeconds": BASELINE_SECONDS, "overlayWarmupSeconds": WARMUP_SECONDS, "recordingSeconds": RECORD_SECONDS, "sampleSeconds": SAMPLE_SECONDS},
            "host": {
                "os": output(["bash", "-lc", ". /etc/os-release; echo \"$PRETTY_NAME\""]),
                "kernel": output(["uname", "-srmo"]),
                "logicalCPUs": os.cpu_count(),
                "cpuGovernor": read_optional("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"),
                "loadAverage": [round(value, 3) for value in os.getloadavg()],
                "concurrentProcessCount": process_count(),
                "sessionType": os.environ.get("XDG_SESSION_TYPE", "unknown"),
                "networkNamespace": "loopback-only",
            },
            "stack": {
                **stack_info(overlay_log),
                "canvas": "2560x1440@60",
                "browserSourceViewport": "2560x1440@30",
                "browserHardwareAcceleration": False,
                "composition": "OBS OpenGL; CEF software-composited",
                "encoder": "NVENC H.264",
            },
            "baseline": {"durationSeconds": round(baseline_duration, 3), "samples": len(baseline_samples), "medianPssKiB": round(statistics.median(baseline_pss), 1), "counters": baseline_counters},
            "overlay": {
                "videoDurationSeconds": round(overlay_duration, 3), "measuredDurationSeconds": RECORD_SECONDS,
                "warmupSamples": len(warmup_samples), "recordingSamples": len(recording_samples),
                "medianPssKiB": round(statistics.median(recording_pss), 1), "incrementalPssKiB": round(pss_increment, 1),
                "pssGrowthKiBPerMinute": round(growth_slope, 3), "pssTotalGrowthKiB": total_growth,
                "medianOverlayCpuPercentOfOneCore": round(statistics.median(browser_cpu), 3),
                "medianCombinedObsAndBrowserCpuPercentOfOneCore": round(statistics.median(cpu), 3),
                "maxGpuFramebufferMiB": max(sample["gpuFramebufferMiB"] for sample in recording_samples),
                "counters": overlay_counters, "renderLagDeltaPercentagePoints": round(lag_delta, 3),
            },
            "visibility": {
                "crop": {"x": 1770, "y": 80, "width": 750, "height": 450}, "sampleSeconds": SAMPLE_SECONDS,
                "threshold": round(visibility_threshold, 3), "hiddenChecks": len(hidden_checks), "hiddenViolations": hidden_violations,
                "recoveryChecks": len(visible_checks), "recoveryViolations": recovery_violations,
                "hiddenSamples": hidden_checks, "recoverySamples": visible_checks,
            },
            "network": {"remotePeers": remote, "loopbackPeersObserved": sorted({peer for sample in recording_samples for peer in sample["socketPeers"]})},
            "stateTransitions": [{"at": event["at"], "mode": event["mode"]} for event in events if event["epoch"] >= overlay_started - 5],
            "samples": {"emptyBaseline": baseline_samples, "overlayWarmup": warmup_samples, "overlayRecording": recording_samples},
            "artifacts": [
                {"path": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                for path in (visible_path, hidden_path) if path.exists()
            ],
            "passed": passed,
        }
        target = EVIDENCE / "p3-obs-measurement.json"
        target.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        os.chmod(target, 0o600)
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 0 if passed else 1
    finally:
        if baseline_obs and baseline_stream:
            stop_obs(baseline_obs, baseline_stream)
        if overlay_obs and overlay_stream:
            stop_obs(overlay_obs, overlay_stream)
        if server and server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=3)
            except subprocess.TimeoutExpired:
                server.kill(); server.wait()
        if RUN.exists():
            shutil.rmtree(RUN)


if __name__ == "__main__":
    raise SystemExit(main())
