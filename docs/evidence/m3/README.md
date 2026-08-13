# M3 broadcast delivery evidence

This directory contains sanitized, reproducible acceptance evidence for DOT-24.
It contains no bearer token, user OBS profile, raw account identifier, raw GSI
payload, or retained recording.

## P2 fail-closed timing

Run from the repository root:

```sh
npm ci --prefix web/browser
node web/browser/measure-p2.mjs
```

`p2-overlay-timing.json` records 100 trials for each of eight unsafe states at
1920x1080 and 2560x1440. Timing starts at the deterministic unsafe condition
and ends only after the claim container is hidden and the captured card region
contains no analytical pixels. The gate is a maximum below 2,000 ms with zero
later revival.

## P3 OBS resource and render run

The runner creates a private `.dot24-p3-run` root, two audio-free OBS profiles
and collections, and a loopback-only network namespace. It refuses to start if
OBS is already running or the private root already exists. It removes the raw
OBS logs, profile, cache, and MKV after extracting sanitized metrics and frames.
It never reads or writes the user's existing OBS configuration.

Run on PaulPC4090 from the repository root:

```sh
unshare -Urn --map-root-user bash -lc \
  'ip link set lo up; python3 web/browser/run-p3.py >/dev/null'
```

The generated scene uses a 2560x1440 canvas and matching 30 FPS Browser Source,
the worst-case supported output size. The Playwright and P2 suites separately
exercise fixed 1920x1080 and 2560x1440 viewports. P3 records a ten-minute empty
scene baseline, starts a matching overlay scene, warms it for ten minutes, then
measures it for 60 minutes while cycling six localized template families,
maximum-length Chinese copy, stale state, malformed state, missing assets,
emergency hide, disconnect, and recovery at the production 750 ms polling
cadence.

Every five seconds the runner samples the OBS/CEF process-tree CPU and PSS, GPU
framebuffer use, socket peers, and current state. It derives render and encoding
counters from the isolated OBS log and decodes the recorded card region every
five seconds. P2 measures the exact two-second fail-closed SLA; P3 uses only
video samples at least two seconds from both state boundaries so a five-second
sampling point cannot be assigned to the adjacent state.

The P3 gate requires:

- no crash or remote socket;
- at least 60 measured minutes after warmup;
- incremental process-tree PSS no more than 256 MiB;
- post-warmup PSS slope no more than 1 MiB/minute and total range no more than
  64 MiB;
- median Browser Source CPU no more than 5% of one logical core;
- overlay-attributable render-lag delta no more than 1.0 percentage point;
- zero visible analytical frames in stable fail-closed windows and zero blank
  frames in stable recovery windows.

`p3-obs-measurement.json` records the immutable source commit, dirty-diff hash,
software stack, host and power context, raw five-second samples, state
transitions, derived metrics, checksums, and the overall result. The two PNGs
are sanitized full-canvas examples of a visible claim and a correctly hidden
claim.

## Current fixed-stack result

The 2026-08-13 full run completed all 600 seconds of empty-scene sampling, 600
seconds of overlay warmup, and 3,600 seconds of overlay resource sampling. It
then failed P3 because an isolated OBS stop-recording hotkey ended the MKV after
879.6 seconds and, before the recording stopped, the 2560x1440 CEF software path
used more than 256 MiB incremental PSS. The runner correctly reported
`passed: false`; that file is superseded by the post-fix evidence committed with
this change.

The hotkeys have been removed and the runner now aborts if an MKV stops growing
for 120 seconds (the normal muxer can buffer for more than 30 seconds). A
600-second warmup plus 120-second post-fix measurement then
recorded the full 720.2 seconds with zero hidden/recovery frame violations, but
the conservative OBS+CEF process-tree PSS delta remained 285,617 KiB. A 10 FPS
Browser Source reduced the three-minute sample only to 269,569 KiB, while the
official obs-browser `--enable-gpu` path increased it to 452,377 KiB. Both
experiments were reverted. This is a reproducible P3 blocker on the pinned CEF
127 software-composited 2560x1440 stack, not a passing claim.
