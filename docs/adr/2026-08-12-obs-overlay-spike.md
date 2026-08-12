# ADR: isolate the DOT-30 overlay behind a fail-closed fixture boundary

Date: 2026-08-12

Status: accepted for the feasibility spike; not a production contract decision

## Context

DOT-30 must prove a transparent Chinese localhost overlay without coupling
presentation to capture, insight, replay, storage, or optional OBS control. The
M0 contract track owns the accepted shared `OverlayStateV1`; this parallel spike
must not silently freeze or migrate that contract.

PaulPC4090 inventory during this run:

- Ubuntu 24.04.4 LTS, X11, 5120×1440 desktop on an RTX 4090;
- native display modes include 1920×1080 and 2560×1440;
- Google Chrome 150.0.7871.181 is installed;
- Noto Sans CJK SC and Noto Serif CJK SC resolve locally;
- `obs` is absent, Debian packages `obs-studio` and `obs-plugins` are not
  installed, no Flatpak or Snap OBS application is registered, and no
  `obs-browser.so` or `libcef.so` was found under `/usr` or `/app`.

Those facts were the exact DOT-30 environment blocker. DOT-34 subsequently
installed and measured the user-scoped Flatpak package described below without
using `sudo` or the deployment checkout. No pre-existing OBS profile or scene
was read or modified.

## Decision

The spike is a standalone, standard-library Go server bound to loopback. Its
public page accepts only a strict spike-local fixture envelope described by
`spikes/obs-overlay/fixture.schema.json`. The envelope contains localized,
display-ready plain text, an allow-listed local asset path, and explicit health
and freshness flags. Unknown fields—including a raw GSI object—fail closed.

This schema is a conformance aid for the presentation spike, not authority to
change the shared M0 contract. Integrating the accepted contract requires an
explicit compatibility check or migration after the contract track lands.

The renderer:

- assigns all audience text with `textContent`; arbitrary HTML is impossible;
- permits no remote script, style, font, image, or connection through CSP;
- uses installed CJK font fallbacks and a fixed-size text fallback for missing
  local images;
- polls every 250 ms and hides the complete analytical card after 1,250 ms
  without a valid safe state, below the two-second acceptance limit;
- treats stale, disconnected, malformed, render-unsafe, display-hidden, and
  emergency-hidden inputs as hidden output;
- does not contain or depend on obs-websocket.

The visual direction is a restrained Chinese broadcast editorial card anchored
inside five-percent horizontal and seven-to-nine-percent vertical safe areas.
The transparent canvas remains otherwise empty. Card layout is content-sized,
with fixed asset geometry and no truncating line clamps.

## Evidence

Playwright system-Chrome tests cover both canvas sizes, longest Chinese strings,
all five initial insight families, local-only requests, alpha transparency,
missing assets, malformed state, stale/disconnected/emergency state, connection
loss, and recovery. The committed RGBA baselines are:

- `spikes/obs-overlay/tests/overlay.spec.mjs-snapshots/overlay-1080p-linux.png`
- `spikes/obs-overlay/tests/overlay.spec.mjs-snapshots/overlay-1440p-linux.png`

The fresh measurement in
`spikes/obs-overlay/evidence/browser-measurement.json` used a temporary headless
Chrome profile after a five-second warmup. Across 61 fixture responses in 15
seconds:

- process-tree proportional set size changed from 502,999 KiB to 488,043 KiB;
- retained JS heap changed by 1,152 bytes after forced collection;
- live document and DOM node counts stayed at 1 and 76;
- renderer task duration advanced by 0.077921 seconds;
- analytical claims hid 1,075 ms after simulated connection loss;
- a valid response restored the card in 216 ms;
- HTML and body computed backgrounds were fully transparent;
- the installed Noto Sans CJK SC font reported ready.

Summed RSS is also retained in the JSON for reproducibility, but it double-counts
shared pages across Chrome processes; PSS is the more defensible memory measure.
This short spike sample is not the M3 60-minute OBS recording gate.

Fresh verification passed `go test ./...`, `go vet ./...`, `go build ./...`,
JavaScript syntax checks, and all 24 Playwright tests. The host exposes
`CGO_ENABLED=0` and no `gcc`, so `go test -race ./...` cannot start; race testing
remains an environment limitation rather than a passing claim.

### Real OBS follow-up (DOT-34)

The follow-up installed `com.obsproject.Studio/x86_64/stable` as a user Flatpak
at commit
`a3a5cbd575a1ab74c239cd3fd6903377377a43ef65e936271f655d458dc520b4`.
The measured stack is OBS 32.2.1, Browser Source 2.26.9, CEF
127.0.6533.120, Qt 6.11.1, Freedesktop 25.08, and OpenGL 3.3 on the RTX 4090
with NVIDIA 595.84. Browser Source hardware acceleration was disabled by its
driver blacklist, so this result exercises its software-composited path.

OBS ran with `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, and `XDG_CACHE_HOME` exported
inside the Flatpak sandbox to an issue-local directory. Two audio-free profiles
and scenes used native 1920x1080 and 2560x1440 canvases and matching Browser
Source dimensions. The real CEF pass rendered all five families, the longest
Chinese fixture, and the missing-asset fallback without clipping, overlap, or
layout shift. Known-color sources behind the browser page remained visible
outside the card, confirming transparent compositing. Stale, disconnected, and
emergency fixtures hid the complete card; Playwright retains malformed-schema
coverage.

After a healthy warmup, killing the loopback server hid the OBS card in an
observed 1,428 ms and restarting it restored the card in 477 ms. The capture
probe sampled about every 235 ms, so these are conservative observed bounds,
not exact renderer callback durations.

The final 1440p steady sample used a 10-second warmup and a 30-second window at
five-second intervals. OBS averaged 2.43% CPU and active CEF processes 1.27%; the
combined OBS/CEF process tree used 721,321 KiB PSS and 59 MiB GPU framebuffer.
Preview held 60/60 FPS. The complete non-recording run reported a 0.506 ms
graphics-thread p99 and 99.9523% of calls below the 16.667 ms frame budget.

A separate 17.941-second 2560x1440 NVENC recording made output counters
observable: 1,076 frames were output, 1,058 of 1,099 attempted frames were
drawn, 41 frames (3.7%) lagged in rendering, and 41 of 1,093 frames (3.8%) were
skipped for encoding lag. The recorded frame is correct, but this short
startup-inclusive result is a residual performance risk for the later M3
60-minute recording gate.

The final OBS pass ran in an unprivileged network namespace whose routing table
contained loopback only. CEF connections were exclusively to
`127.0.0.1:18838`; OBS update attempts failed immediately because no remote
route existed. The overlay server still exposes only GET/HEAD fixture and
static routes, so the page cannot call a state-changing operator endpoint.
Sanitized measurements are committed in
`spikes/obs-overlay/evidence/obs-measurement.json`.

## Consequences

Browser rendering and failure behavior remain reproducible without Dota,
capture, analytics, a database, or credentials. DOT-34 removes the Linux OBS,
CEF, font, alpha, and reconnect feasibility blocker on PaulPC4090. Browser
hardware acceleration is unavailable with this OBS/driver combination, and the
short recording's render/encode lag remains a measured risk for the production
duration gate.
