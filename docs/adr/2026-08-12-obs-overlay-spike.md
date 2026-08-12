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

Therefore Browser Source availability, OBS CEF rendering, OBS compositing, and
OBS source CPU/memory are exact environment blockers. No software was installed
and no existing OBS profile or scene was read or modified.

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
JavaScript syntax checks, and all 19 Playwright tests. The host exposes
`CGO_ENABLED=0` and no `gcc`, so `go test -race ./...` cannot start; race testing
remains an environment limitation rather than a passing claim.

## Consequences

Browser rendering and failure behavior are reproducible without Dota, capture,
analytics, a database, OBS, or credentials. The branch cannot claim Linux OBS
Browser Source acceptance until the bounded manual procedure in the spike
README is completed on a host with OBS and its Browser Source plugin. CEF-specific
font, CSS, alpha, refresh-flash, and resource behavior remain residual risks.
