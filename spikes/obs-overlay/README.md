# OBS zh-CN overlay spike

This spike proves the presentation and failure-hide boundary for DOT-30. It is
not the final M3 overlay or an operator console. The page reads only a fixed,
localized `OverlayStateV1` fixture envelope; it does not import or request raw
GSI, replay, analytics, database, Steam, or OBS state.

## Run locally

From the repository root:

```bash
go run ./spikes/obs-overlay -addr 127.0.0.1:18838
```

Open `http://127.0.0.1:18838/?fixture=healthy`. Other fixture names are:

- `draft-context`
- `lane-checkpoint`
- `item-timing-long`
- `objective-exchange`
- `teamfight-readiness`
- `missing-asset`
- `stale`
- `disconnected`
- `emergency-hide`

The server rejects non-loopback listen addresses. The renderer polls its one
fixture endpoint every 250 ms and hides the card after 1,250 ms without a valid,
safe response. Invalid schema, extra fields, stale health, disconnected health,
and emergency hide all fail closed.

## Verify and reproduce evidence

From the repository root:

```bash
go test ./...
go vet ./...
go build ./...
```

Then run the browser checks:

```bash
cd spikes/obs-overlay
npm ci
npm test
npm run measure
```

`npm test` uses installed Google Chrome and checks both 1920×1080 and
2560×1440. The committed PNG baselines are under
`tests/overlay.spec.mjs-snapshots/`. `npm run measure` refreshes
`evidence/browser-measurement.json` using a fresh temporary Chrome profile and
loopback server; both are stopped and deleted when the command exits.

The measured host has `CGO_ENABLED=0` and no C compiler, so the Go race detector
cannot run there. This does not affect the standard tests above, but remains a
verification limitation.

## Bounded OBS verification

DOT-34 completed this procedure with user Flatpak OBS 32.2.1 and Browser Source
2.26.9 on PaulPC4090. The exact measured package commit, CEF/GPU versions,
failure timing, resource sample, output-frame counters, and residual risks are
recorded in `docs/adr/2026-08-12-obs-overlay-spike.md` and
`evidence/obs-measurement.json`. Do not modify an existing broadcast
collection when repeating it.

1. Start the loopback server with the command above.
2. In OBS, create a profile named `DOT-30 Overlay Spike` and a scene collection
   with the same name.
3. Add one Browser Source using the healthy URL. Set its width and height to
   1920×1080, then repeat at 2560×1440. Do not add obs-websocket.
4. Record the transparent output, font rendering, source CPU, and source memory.
   Switch the URL through `item-timing-long`, `missing-asset`, `stale`,
   `disconnected`, and `emergency-hide`.
5. Stop the Go process with `Ctrl-C`. Delete only the profile and scene
   collection created for the spike through the OBS menus.

This manual step is intentionally bounded by unique names. It does not require
or expose an obs-websocket password and does not overwrite Paul's profiles or
scenes.

The reproducible user install and package identity commands are:

```bash
flatpak install --user -y flathub com.obsproject.Studio
flatpak info --user --show-commit com.obsproject.Studio
flatpak run --user --command=obs com.obsproject.Studio --version
```

For a fully isolated validation, export issue-local XDG directories from an
inner Flatpak shell before executing `obs`; Flatpak itself rewrites XDG
variables passed through `flatpak run --env`. Run OBS and the overlay server in
the same unprivileged network namespace with only loopback enabled when the
test must prove that CEF cannot reach remote resources. The package can later
be removed with `flatpak uninstall --user com.obsproject.Studio`; delete only
the issue-local XDG directory used by the spike.
