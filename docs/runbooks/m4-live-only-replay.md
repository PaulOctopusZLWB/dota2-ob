# M4 live-only replay candidate

This candidate adds an explicit product policy selection. `v2-snapshot` is the
default and retains the existing `--policy-lineage-file` path. `v3-live-only`
requires an explicit session ID and all three strict canonical contracts:

```sh
go run ./cmd/dota2-ob \
  --policy-mode v3-live-only \
  --session-id <session-id> \
  --history-binding-file <history_availability_binding_v1.json> \
  --live-only-lineage-file <policy_lineage_manifest_v3.json> \
  --live-only-release-file <live_only_release_binding_v1.json>
```

The live-only loader rejects noncanonical bytes, oversize or non-regular files,
session/source/content-ID mismatch, cross-object substitution, snapshot-backed
history, and product artifact mismatch before policy commands or overlay output
become available. Raw GSI capture remains independently available when policy
configuration or delivery fails. A session never appends both `.pcl2` and
`.pcl3` frames.

## Deterministic replay

The sanitized fixture is
`internal/integration/m4/testdata/captured_gsi_schedule.json`:

- fixture SHA-256: `2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e`
- canonical replay golden SHA-256: `80d0252271f9470a2c936af63b552df2d8ba93df0e95ce68a636a7c457c04525`
- no account ID, Steam ID, player handle, token, or other private identifier is present

The test posts every captured body to the real `/gsi` HTTP handler. The raw V3
append acknowledges before asynchronous projection. The capacity-one
high-water follower maps ordered `LiveObservationV1` values, evaluates only
live/objective insight under typed unavailable history, synchronizes one V3
terminal commit per causal input, renders zh-CN presentation, and exercises the
typed loopback command/operator/overlay gateway. It freezes ordered input,
observation, candidate, V3 commit, command result, audit, operator, and overlay
identities. Two clean temporary roots must produce identical canonical bytes.

Clocks are separate and fixture-owned:

- `received_at` drives local raw receipt/evidence time;
- map `clock_time` and `game_time` remain source game clocks;
- `policy_time_ms` drives policy causality;
- command times drive approval/rejection/publication;
- overlay publication time is at least the corresponding decision time;
- DotaTV delay is not synthesized and authorizes no claim.

Run the deterministic candidate with:

```sh
go test -count=2 ./cmd/dota2-ob ./internal/integration/m4
```

The schedule covers live candidate suppression, unchanged non-event,
approval/publish, exact duplicate idempotency, revision-conflict rejection,
paused-input suppression, emergency hide, audit ordering, and visible-to-hidden
overlay delivery. Existing focused package matrices continue to own malformed
input/body limits, raw tail/cursor recovery, projection bounds, policy frame and
checkpoint corruption, synchronized append failure, gateway ordering/body
limits, and browser/OBS disconnect behavior.

## Verification

```sh
git merge-base --is-ancestor c0b328a95b225e68771adeb4d927b54a90c79a1e HEAD
git merge-base --is-ancestor cf20368430d46ad395127609eb7eb9609422aaa8 HEAD
git diff --check c0b328a95b225e68771adeb4d927b54a90c79a1e..HEAD
go test -count=2 ./cmd/dota2-ob ./internal/integration/m4
go test -count=1 ./...
CGO_ENABLED=1 CC="zig cc" go test -race -timeout 30m -count=1 ./...
go vet ./...
go build ./...
go mod verify
(cd web/browser && npm ci && npm test)
(cd spikes/obs-overlay && npm ci && npm test)
```

This is a functional M4 candidate only. It does not claim the P4 12-hour wall
clock soak, M5 calibration, M6 rehearsal, deployment, or merge acceptance. Raw
acknowledgement retains the accepted complete OS-buffered append boundary; it
does not add per-record `fsync`.
