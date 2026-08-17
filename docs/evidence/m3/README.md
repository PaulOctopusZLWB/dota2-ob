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

For each target resolution, the runner creates a private
`.dot24-p3-run-{resolution}` root, two audio-free OBS profiles and collections,
and a loopback-only network namespace. It refuses to start if OBS is already
running or the private root already exists. It removes the raw OBS logs,
profile, cache, and MKV after extracting sanitized metrics and frames. It never
reads or writes the user's existing OBS configuration.

Run on PaulPC4090 from the repository root:

```sh
for resolution in 1080p 1440p; do
  unshare -Urn --map-root-user bash -lc \
    "ip link set lo up; DOTA2_OB_P3_RESOLUTION=$resolution \
      python3 web/browser/run-p3.py >/dev/null"
done
```

The accepted generated scenes use native 1920x1080 and 2560x1440 canvases with
one transparent 750x640, 30 FPS Browser Source. Its identity transform is
positioned at `(1130,60)` for 1080p and `(1770,80)` for 1440p, with no crop,
rotation, bounds transform, or scaling. P3 records a ten-minute empty-scene
baseline at each resolution, starts a matching overlay scene, warms it for ten
minutes, then measures it for 60 minutes while cycling six localized template families,
maximum-length Chinese copy, stale, malformed, schema-mismatch, oversize,
missing-asset, emergency-hide, disconnect, out-of-order, and recovered states
at the production 750 ms polling cadence.

Every five seconds the runner samples the OBS/CEF process-tree CPU and PSS, GPU
framebuffer use, socket peers, and current state. It derives render and encoding
counters from the isolated OBS log and decodes both the complete 750x640 source
rectangle and composed full-output frame every five seconds. P2 measures the
exact two-second fail-closed SLA; P3 uses settled samples at least two seconds
after a state boundary and a three-second trailing guard for recording PTS
alignment. The historical 750x450 rectangle is retained in the evidence schema
only as `rejected_visibility_analysis_only_not_sampled`; it is not evaluated.

The P3 gate requires:

- no crash or remote socket;
- at least 60 measured minutes after warmup;
- incremental process-tree PSS no more than 256 MiB;
- absolute overlay process-tree PSS no more than 1 GiB;
- both whole-tree and Browser Source post-warmup PSS slope no more than
  1 MiB/minute, and whole-tree total range no more than 64 MiB;
- median Browser Source CPU no more than 5% of one logical core;
- overlay-attributable render-lag delta no more than 1.0 percentage point;
- zero visible analytical frames in stable fail-closed windows and zero blank
  frames in stable recovery windows.

`p3-viewport-1080p.json` and `p3-viewport-1440p.json` record the immutable
source commit, source-diff hash, software stack, host and power context, raw
five-second samples, total/OBS/browser component PSS, state transitions,
derived metrics, checksums, and the overall result. Resolution-specific PNGs
are sanitized complete-source and composed full-output examples of a visible
claim and a correctly hidden claim.

## Current fixed-stack result

The 2026-08-13 full run completed all 600 seconds of empty-scene sampling, 600
seconds of overlay warmup, and 3,600 seconds of overlay resource sampling. It
then failed P3 because an isolated OBS stop-recording hotkey ended the MKV after
879.6 seconds and, before the recording stopped, the 2560x1440 CEF software path
used more than 256 MiB incremental PSS. The runner correctly reported
`passed: false`; that file is superseded by the post-fix evidence committed with
this change.

The hotkeys have been removed and the runner now aborts if an MKV stops growing
for 120 seconds (the normal muxer can buffer for more than 30 seconds). The
committed blocker rerun used a 60-second baseline followed by a 600-second
warmup and 120-second measurement. It recorded the full 720.3 seconds with zero
hidden/recovery frame violations, but the conservative OBS+CEF process-tree PSS
delta remained 276,994 KiB and its short-window slope was 1,635 KiB/minute. A 10 FPS
Browser Source reduced the three-minute sample only to 269,569 KiB, while the
official obs-browser `--enable-gpu` path increased it to 452,377 KiB. Both
experiments were reverted. This is a reproducible P3 blocker on the pinned OBS
32.2.1 / Browser Source 2.26.9 / CEF 127.0.6533.120 software-composited
2560x1440 stack, not a passing claim.

The DOT-49 predecessor added component-level PSS accounting and native bounded
preflights at both protocol resolutions. They retained the ten-minute warmup
and measured three post-warmup minutes. At 1920x1080 the total incremental PSS
was 254,420 KiB and growth was 1,950.517 KiB/minute. At 2560x1440 the total
incremental PSS was 276,381 KiB and growth was 1,130.584 KiB/minute. Those
bounded windows reproduce resource-gate failures on the pinned stack, but they
do not establish whether either complete 60-minute measurement can pass.

The successor therefore ran the unchanged protocol to completion at both
resolutions: 600 seconds empty baseline, 600 seconds overlay warmup, and 3,600
seconds measurement with 120/120/720 five-second samples. Both complete runs
failed only the unchanged 256 MiB whole-process-tree incremental-PSS gate:

- 1920x1080: 266,647 KiB incremental PSS, 320.997 KiB/minute growth, and
  24,309 KiB range;
- 2560x1440: 289,851 KiB incremental PSS, 359.992 KiB/minute growth, and
  26,748 KiB range.

Both complete runs passed the remaining unchanged gates: browser median CPU
was 0.6% of one logical core, render-lag delta was -0.1 percentage point, no
crash or remote peer occurred, and all 480 hidden checks plus 359 recovery
checks had zero violations. Their overlay videos were 4,200.277 and 4,200.256
seconds. `p3-measurement-1080p.json` and `p3-measurement-1440p.json` therefore
correctly record `passed: false`; no threshold, baseline, or duration was
changed or represented as accepted.

These historical full-canvas failures remain diagnostic evidence only. The
accepted 750x640 correction did not change their baseline, duration, or any
resource threshold.

## Accepted 750x640 full-protocol result

The 2026-08-14 successor used accepted spec commit
`2a0dabb60f5bc57adbc93d79c055ee80b1ccab3a` and source-diff SHA-256
`cac4d0d64dfd3da02939b1bfe32b789527e5d681b744004f9048eecf541f7825`.
Both resolution runs completed the unchanged 600-second baseline, 600-second
warmup, and 3,600-second measurement with 120/120/720 samples at five-second
cadence and recording enabled. Both record `passed: true`:

- 1920x1080: baseline/overlay median PSS 556,126/785,991 KiB;
  whole-tree incremental PSS 229,865 KiB, growth 526.617 KiB/minute, and range
  33,692 KiB; OBS growth 69.676 KiB/minute; browser median PSS 296,024 KiB and
  growth 456.940 KiB/minute.
- 2560x1440: baseline/overlay median PSS 634,159/864,949 KiB;
  whole-tree incremental PSS 230,790 KiB, growth 543.162 KiB/minute, and range
  38,139 KiB; OBS growth 69.931 KiB/minute; browser median PSS 296,143 KiB and
  growth 473.231 KiB/minute.

At both resolutions browser median CPU was 0.4% of one logical core, lag delta
was -0.1 percentage point, no crash or remote peer occurred, and each of the
complete-source and full-output regions recorded 204 hidden plus 155 recovered
settled checks with zero violations. The evidence retains the negative OBS
incremental attribution separately (-66,139 and -65,501 KiB); it does not mask
the passing whole-tree or browser growth gates. The pinned stack remains OBS
32.2.1 / Browser Source 2.26.9 / CEF 127.0.6533.120, software-composited and
isolated from the user's OBS profiles.
