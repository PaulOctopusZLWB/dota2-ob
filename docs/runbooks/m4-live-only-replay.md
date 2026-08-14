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
- canonical production-composition golden SHA-256: `7999ad212871750a97c707265757d180006b58879a8d8d455c7bee36b60cd56e`
- no account ID, Steam ID, player handle, token, or other private identifier is present

The test starts the real product composition through `runWithDependencies`,
strictly loads all three release artifacts, posts every captured body to its
real `/gsi` handler, waits on the independent production policy follower, and
uses the production command/operator/overlay gateway. It does not open a V3
commit log or construct a policy application directly. The raw V3 append
acknowledges before asynchronous projection. The live-only evaluator emits a
truthfully generic visible-state-change claim: it maps captured `team2` and
`team3` values to Radiant and Dire economy deltas, but never attributes a tower,
Roshan, Tormentor, or taking team without a stable captured identifier. A second
startup on the same root proves replay verification, restore-barrier behavior,
and visible-state rebuild. Two independent clean roots must produce identical
canonical bytes.

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

The schedule covers live candidate suppression, a truthfully attributed
nonzero `team2` economy delta, approval/publish, paused-input suppression,
durable audit ordering, restart, and visible-state rebuild. Focused V3 tests
also pin equality/older/retry causal-baseline handling, the next-newer delivery,
restart baseline recovery, and capacity-64 queue saturation. Saturation latches
`candidate_queue_saturated`, hides immediately (inside the accepted two-second
bound), rejects commands, preserves the already-accepted raw stream, and
recovers hidden without transient republication. The broader component and
product-startup matrices retain malformed/oversize input, raw tail/cursor,
projection bounds, policy frame/checkpoint, synchronized append failure,
gateway ordering/body limits, and browser/OBS disconnect coverage.

## Verification

```sh
git merge-base --is-ancestor c0b328a95b225e68771adeb4d927b54a90c79a1e HEAD
git merge-base --is-ancestor cf20368430d46ad395127609eb7eb9609422aaa8 HEAD
git diff --check c0b328a95b225e68771adeb4d927b54a90c79a1e..HEAD
go test -count=2 ./cmd/dota2-ob ./internal/integration/m4
go test -count=2 ./cmd/dota2-ob -run 'TestSnapshotV2AcceptedCanonicalBytesAndLineageRemainPinned|TestBroadcastRuntimeV3DoesNotRewindCausalBaselineAndRestartsAtNewest|TestBroadcastRuntimeV3QueueSaturationLatchesHealthHideAndRecovers|TestM4CapturedGSIUsesProductionCompositionTwiceAndRestarts'
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
