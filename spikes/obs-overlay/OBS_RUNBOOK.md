# DOT-34 isolated real-OBS evidence runbook

This runbook reproduces the bounded PaulPC4090 measurement without reading or
overwriting an existing OBS profile. It uses generated, audio-free profiles and
scene collections under a disposable repository-local directory. It never
enables obs-websocket and runs OBS, CEF, and the fixture server in one
unprivileged network namespace whose only usable interface is loopback.

The committed result of the 2026-08-12 rerun is in
`evidence/obs-run-transcript.log`; its artifact hashes are pinned by
`evidence/obs-run-manifest.json`. The earlier 1,428/477 ms, 721,321 KiB PSS,
and recording-counter claims came from a deleted runtime and are explicitly
superseded. Repeatable current values come from this procedure.

## Preconditions and identity pin

Run from the repository root in an X11 desktop session. Set paths explicitly;
do not point either variable at a home directory, the acceptance root, or an
existing OBS configuration.

```bash
export REPO="$PWD"
export RUN_ROOT="$REPO/.dot34-obs-rerun"
export ACCEPTANCE_ROOT="<canonical-deploy-root>"
test ! -e "$RUN_ROOT"
test -d "$ACCEPTANCE_ROOT"

flatpak info --user --show-ref com.obsproject.Studio
flatpak info --user --show-commit com.obsproject.Studio
flatpak run --user --command=obs com.obsproject.Studio --version
```

Expected identity for this evidence is user installation
`app/com.obsproject.Studio/x86_64/stable`, Flatpak commit
`a3a5cbd575a1ab74c239cd3fd6903377377a43ef65e936271f655d458dc520b4`,
and OBS 32.2.1. Stop if any value differs; a different package is a new
measurement, not a reproduction of this one.

Record the deploy-root identity without reading any file below it, then create
the disposable profiles and server binary:

```bash
stat -c '%i %y' "$ACCEPTANCE_ROOT" > "$REPO/.dot34-acceptance.before"
python3 spikes/obs-overlay/prepare_obs_profile.py \
  --root "$RUN_ROOT" \
  --recording-dir "$RUN_ROOT/recordings"
go build -o "$RUN_ROOT/obs-overlay" ./spikes/obs-overlay
```

`prepare_obs_profile.py` is the source of truth for the isolated settings. It
creates `DOT34-1080p` and `DOT34-1440p`, with these properties:

- 1920x1080 and 2560x1440 base/output canvases at 60 FPS;
- one full-canvas known-color source under one transparent `browser_source`;
- Browser Source URL
  `http://127.0.0.1:18838/?fixture=item-timing-long` and dimensions matching
  the selected canvas exactly;
- no desktop/microphone source, remote URL, custom plugin, or credential;
- advanced MKV output using `obs_nvenc_h264_tex`, written only below
  `$RUN_ROOT/recordings`.

Inspect those generated values before launch:

```bash
grep -E '^(Name|BaseC|OutputC|FPS|RecFilePath|RecFormat2|RecEncoder)=' \
  "$RUN_ROOT"/config/obs-studio/basic/profiles/*/basic.ini
jq -r '.sources[] | select(.id=="browser_source") |
  [.name,.settings.url,.settings.width,.settings.height] | @tsv' \
  "$RUN_ROOT"/config/obs-studio/basic/scenes/*.json
```

## Namespace and inner Flatpak/OBS launch

Enter the namespace in one terminal. `unshare -Urn` creates user and network
namespaces; `--map-root-user` grants capability only inside them. Bring up `lo`
and confirm there is no default route before starting either process.

```bash
cd "$REPO"
unshare -Urn --map-root-user bash
export RUN_ROOT="$PWD/.dot34-obs-rerun"
ip link set lo up
ip -brief address show lo
ip route show table all
test -z "$(ip route show default)"

"$RUN_ROOT/obs-overlay" -addr 127.0.0.1:18838 \
  >"$RUN_ROOT/server.log" 2>&1 &
export SERVER_PID=$!
```

Flatpak rewrites XDG variables passed as outer `--env` values. Export them in
the inner Flatpak shell, then `exec obs` so the launcher lifetime is bounded by
OBS. Launch 1080p first:

```bash
flatpak run --user --command=sh com.obsproject.Studio -c '
  export XDG_CONFIG_HOME="$1/config"
  export XDG_DATA_HOME="$1/data"
  export XDG_CACHE_HOME="$1/cache"
  exec obs --multi --profile "$2" --collection "$2" \
    --scene "DOT34 Scene" --disable-missing-files-check --verbose
' sh "$RUN_ROOT" DOT34-1080p >"$RUN_ROOT/launcher-1080.log" 2>&1 &
export OBS_WRAPPER_PID=$!
```

Visually inspect the 1920x1080 canvas: the known color must remain visible
outside the card, Chinese must be legible, the longest copy must not clip, and
the Browser Source entry must report 1920x1080. Capture only the OBS window;
the 2026-08-12 run used `xwd` and the Flatpak-bundled `ffmpeg`:

```bash
export OBS_WINDOW_ID="$(xdotool search --name 'Profile: DOT34-1080p' | head -1)"
xwd -silent -id "$OBS_WINDOW_ID" -out "$RUN_ROOT/obs-1080p.xwd"
flatpak run --user --command=sh com.obsproject.Studio -c \
  'ffmpeg -hide_banner -loglevel error -y -i "$1" "$2"' \
  sh "$RUN_ROOT/obs-1080p.xwd" "$RUN_ROOT/obs-1080p.png"

kill -INT "$(pgrep -x obs)"
wait "$OBS_WRAPPER_PID"
```

Repeat with `DOT34-1440p`, changing the profile, collection, launcher log, OBS
window search, and screenshot name. Confirm the log says base/output
2560x1440, 60/1 FPS, Browser Source 2.26.9, and CEF 127.0.6533.120.

## Timing probe

With the 1440p OBS process warm and visible, the timing-probe samples the fixed
card region of OBS-window captures. Record the kill/restart epoch before the
first capture. The run used 150 ms requested sleep; capture overhead produced
an observed median interval of about 181 ms.

```bash
mkdir -p "$RUN_ROOT/timing"
export OBS_WINDOW_ID="$(xdotool search --name 'Profile: DOT34-1440p' | head -1)"
xwd -silent -id "$OBS_WINDOW_ID" -out "$RUN_ROOT/timing/healthy.xwd"

export LOSS_NS="$(date +%s%N)"
kill "$SERVER_PID"
wait "$SERVER_PID" || true
for i in $(seq -w 0 14); do
  now="$(date +%s%N)"
  xwd -silent -id "$OBS_WINDOW_ID" -out "$RUN_ROOT/timing/loss-$i.xwd"
  printf '%s %s\n' "$i" "$now" >>"$RUN_ROOT/timing/loss-index.txt"
  sleep 0.15
done

export RECOVERY_NS="$(date +%s%N)"
"$RUN_ROOT/obs-overlay" -addr 127.0.0.1:18838 \
  >"$RUN_ROOT/server-recovery.log" 2>&1 &
export SERVER_PID=$!
for i in $(seq -w 0 09); do
  now="$(date +%s%N)"
  xwd -silent -id "$OBS_WINDOW_ID" -out "$RUN_ROOT/timing/recovery-$i.xwd"
  printf '%s %s\n' "$i" "$now" >>"$RUN_ROOT/timing/recovery-index.txt"
  sleep 0.15
done
```

Convert the XWD captures with the same Flatpak `ffmpeg`. Compare crop
`(900,130,1220,340)` using Pillow `ImageChops.difference`: count pixels whose
maximum RGB delta from `healthy.png` exceeds 20. The first plateau capture is
fully hidden; the first zero-delta capture is fully recovered. Subtract its
indexed epoch from `LOSS_NS` or `RECOVERY_NS`. Preserve the indexes, thresholds,
derived durations, and selected capture checksums in the sanitized transcript.

## CPU, PSS, GPU, frame counters, and sockets

After a 10-second 1440p warmup, sample seven times at five-second intervals.
CPU uses `/proc/PID/stat` tick deltas across the complete 30-second window. PSS
is the sum of `Pss:` in `smaps_rollup` for OBS and every
`/app/lib/obs-plugins/obs-browser-page` process; it does not double-count shared
pages like RSS does.

```bash
export OBS_PID="$(pgrep -x obs)"
export BROWSER_PIDS="$(pgrep -f '^/app/lib/obs-plugins/obs-browser-page')"
export HZ="$(getconf CLK_TCK)"

ticks() {
  local total=0 pid value
  for pid in $1; do
    test -r "/proc/$pid/stat" || continue
    value="$(awk '{print $14+$15}' "/proc/$pid/stat")"
    total=$((total + value))
  done
  echo "$total"
}

obs_start="$(ticks "$OBS_PID")"
browser_start="$(ticks "$BROWSER_PIDS")"
start_ns="$(date +%s%N)"
for i in 0 1 2 3 4 5 6; do
  pss=0
  for pid in $OBS_PID $BROWSER_PIDS; do
    one="$(awk '/^Pss:/{print $2}' "/proc/$pid/smaps_rollup")"
    pss=$((pss + one))
  done
  printf 'sample=%s epoch_ns=%s tree_pss_kib=%s\n' \
    "$i" "$(date +%s%N)" "$pss"
  test "$i" -eq 6 || sleep 5
done
end_ns="$(date +%s%N)"
obs_end="$(ticks "$OBS_PID")"
browser_end="$(ticks "$BROWSER_PIDS")"
# CPU percent = (end_ticks-start_ticks)*100/(HZ*elapsed_seconds)
```

GPU framebuffer comes from one `nvidia-smi pmon -c 1 -s m` snapshot. Sum the
OBS graphics row and the CEF GPU-process row only. Record the complete command
but sanitize unrelated desktop process rows from the committed transcript.

Socket inspection must happen inside the namespace while Browser Source is
active:

```bash
ip route show table all
ss -Hntp
nvidia-smi pmon -c 1 -s m
```

There must be no default route and every CEF peer must be `127.0.0.1:18838`.

For recording counters, stop OBS and relaunch the same inner command with
`DOT34-1440p --startrecording`. Allow startup plus at least 20 seconds of actual
recording, send SIGINT, and extract these source-log lines:

```bash
grep -E 'Recording Start|Output of file|Total frames output|Total drawn frames|lagged frames|skipped frames|obs_graphics_thread' \
  "$RUN_ROOT"/config/obs-studio/logs/*.txt
```

Do not commit the MKV, raw XWD/PNG series, generated profiles, cache, or raw OBS
logs. Commit only sanitized excerpts and attach the selected screenshots to the
issue. Run `sha256sum` before upload and record the filenames/hashes in the
transcript and manifest.

## Verification and cleanup

Exit OBS cleanly, stop the fixture server, then leave the namespace:

```bash
kill -INT "$(pgrep -x obs)" 2>/dev/null || true
wait "$OBS_WRAPPER_PID" 2>/dev/null || true
kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
exit
```

From the host shell, verify and remove only the explicit disposable directory:

```bash
pgrep -x obs && exit 1 || true
ss -ltn | grep -q ':18838' && exit 1 || true
stat -c '%i %y' "$ACCEPTANCE_ROOT" > "$REPO/.dot34-acceptance.after"
cmp "$REPO/.dot34-acceptance.before" "$REPO/.dot34-acceptance.after"
rm -rf -- "$RUN_ROOT"
rm -- "$REPO/.dot34-acceptance.before" "$REPO/.dot34-acceptance.after"
```

The final verification suite is:

```bash
go test -count=1 ./...
go vet ./...
go build ./...
node --check spikes/obs-overlay/static/overlay.js
node --check spikes/obs-overlay/measure.mjs
node --check spikes/obs-overlay/tests/overlay.spec.mjs
python3 spikes/obs-overlay/tests/obs_evidence_test.py
npm ci --prefix spikes/obs-overlay
npm test --prefix spikes/obs-overlay
jq empty spikes/obs-overlay/fixture.schema.json \
  spikes/obs-overlay/fixtures/*.json spikes/obs-overlay/evidence/*.json
git diff --check 289e7cafb591097195fc45fa62e2a363e9a288fe..HEAD
```
