# M1 Historical Data Foundation — Implementation And Evidence

Date: 2026-08-13

Status: M1 code-foundation successor (proposed for independent re-review; real-data gate pending)

Issue: DOT-22

Program spec: `docs/specs/2026-08-12-ti-broadcast-analytics-program.md` (M1, protocols P0/P4)

Replay spike: `docs/specs/2026-08-12-replay-acquisition-parser-storage-spike.md`

## Objective

Own the reproducible historical data foundation for the frozen TI 2026 scope:
materialize the roster/identity scope, run bounded discovery, acquire/verify/
parse/normalize replays, aggregate across current-patch/90/180-day windows,
seal immutable `HistoricalBaselineV1` snapshots, and evaluate the
full/restricted/no-go readiness gate.

## Architecture Boundary

`internal/history` is the pure historical domain. The M0 architecture test
allowed it zero standard-library imports (it was a doc-only placeholder). M1
evolves that rule visibly: `internal/history` now imports only
`crypto/sha256`, `encoding/hex`, `errors`, `sort`, `strings`, `time`, plus
`internal/contracts`. It still imports NO `net/http`, `os`, `io/fs`,
`database`, `internal/replay`, presentation, or policy. Concrete network,
filesystem, replay-parser, and storage adapters live in `internal/replay` and
the `cmd/history` composition root. The architecture ownership rule update
is recorded in `internal/architecture/dependencies_test.go`.

## Modules

- `internal/history/version.go` — pinned M1 provenance versions, discovery
  contract version, readiness outcomes, stage names, providers.
- `internal/history/window.go` — current-patch / trailing-90 / trailing-180
  half-open window bounds from the frozen cutoff.
- `internal/history/roster.go` — `RosterManifestV1`: versioned, content-
  addressed, binds to `TournamentScopeV1`, covers 16 teams + players with
  stable IDs, aliases, roles, effective dates, provenance.
- `internal/history/discovery.go` — `DiscoveryManifestV1`: content-addressed,
  deduplicated 180-day match/replay union; every item has a terminal,
  explainable state; coverage is derived, not hand-edited.
- `internal/history/facts.go` — `NormalizedMatchFacts`: per-participant
  metrics with explicit presence/missingness and verified/quarantined
  identity; unknown actor/entity fields stay nil, never fabricated.
- `internal/history/aggregate.go` — aggregation across player/team/role/hero/
  player-hero/patch × windows with sample-size minima (draft ≥5, lane/item ≥8,
  team ≥10), source coverage PPM, and missingness; cells below minimum are
  emitted `absent`, not omitted.
- `internal/history/snapshot.go` — `BuildSnapshot`: seals
  `HistoricalSnapshotManifestV1` + `HistoricalBaselineV1` values; enforces
  active-match exclusion, late-fact rejection, cutoff eligibility, and
  content-addressed input-manifest digest.
- `internal/history/batch.go` — `StagePipeline`: idempotent
  discovery→acquisition→verification→parse→normalize→aggregate state machine
  with resume, bounded retries, dead-letter terminal state, and checkpoint
  failure propagation.
- `internal/history/readiness.go` — `ReadinessGate`: evaluates the exhaustive
  discovery manifest against the frozen scope and returns exactly one of
  `full_history_go`, `restricted_history_go`, `historical_no_go`.
- `internal/replay/normalize.go` — adapter mapping `ReplayFactsV1` + public
  metadata + optional participant mapping to `NormalizedMatchFacts`; identity
  is quarantined unless a full 10-hero mapping binds every parsed hero.
- `cmd/history/main.go` — explicitly synthetic fixture harness: deterministic
  `corpus` and `report` subcommands wire atomic local state to the pure seams;
  no network, credentials, or replay download. This command is reproducibility
  evidence for the code paths only, never tournament-readiness evidence.

## Determinism, Resume, And Identity Quarantine

The representative corpus is built from fixed deterministic inputs (no
network, no replay bytes). Running `go build -o ./history ./cmd/history &&
./history corpus` twice from fresh data dirs produces byte-identical content
identities:

```
discovery_manifest_id = fbba0a07b8f27cceb213e6a91094aceba7f1b63c0c60c027c7df613bab8e5198
snapshot_manifest_id  = ada88309bd6ccfb818d44c6409c18a9a932409a73a7ce38d76bf91b884310e06
batch_state_sha256    = 144102e29e66d1b67ff8e01bd0cf6c06e124f7cb2415a49239e9a10bd0a9a288
```

The second batch pass (resume) re-runs zero stages — every succeeded entry is
skipped, every terminal dead-letter entry (quarantined identity, expired
replay) is skipped before any work, and no fact is duplicated. Quarantined
matches are excluded from the sealed snapshot; expired matches reach a
`replay_not_accessible` terminal state. These guarantees are covered by
`cmd/history/main_test.go` and the focused `internal/history/*_test.go` suite.

## Readiness Outcome (representative corpus)

The representative corpus is deliberately below the 100-replay / 16-team ×
5-match full-history gate. The six-match report returns `historical_no_go`
because no team reaches the minimum; this is fixture behavior, not a request
to accept a restricted tournament-history scope:

```
outcome                = historical_no_go
replay_accessible_total= 6
full_history_target    = 100
minimum_team_matches   = 5
replay_quarantined     = 2
replay_expired         = 1
```

Reaching full-history would require a real bounded discovery + acquisition
run against the corroborated TI 2026 roster (an upstream M1 input) and at
least 100 replay-accessible professional replays. That run is a field test
that must not be performed with fabricated identity, mixed patches, or
unavailable replays counted as samples.

## Safety And Provenance

- No credentials, GC login, account/UI automation, packet/memory access, or
  replay-salt automation. `dotabuff/manta` v1.5.0 stays pinned behind
  `internal/replay`.
- HTTP replay CDN bytes are unauthenticated; SHA-256 is content identity, not
  authenticity. The normalize adapter quarantines any metadata/parser
  identity mismatch before publication.
- Raw replays and generated datasets stay under the canonical git-ignored
  data root (`data/`). No raw or generated data is committed to git; only the
  safe deterministic command + report is.
- manta completeness limits are preserved: only combat-log-derivable scalars
  (kills, deaths) are populated per participant; assists, GPM, XPM, last
  hits, denies, net-worth, level, farm checkpoints, and key item timings need
  entity state and remain nil, never fabricated.

## Verification

All commands run from the repo root on the issue branch
`agent/dota2-data-pipeline-engineer/dot-22-m1-history` based on
`35191fe95609df52d2b387e8a79ea8c982308cac`:

```
go test -count=1 ./internal/history ./internal/replay ./cmd/replay-spike
go test -count=1 ./...
CGO_ENABLED=1 CC="zig cc" go test -race -count=1 ./...
go vet ./...
go build ./...
go mod verify
git diff --check 35191fe95609df52d2b387e8a79ea8c982308cac..HEAD
go build -o ./history ./cmd/history && ./history corpus --data-dir ./data-m1-run-a
                                                ./history corpus --data-dir ./data-m1-run-b
```

Measured results: all pass. Two clean-root default 12-match fixture runs took
17.614s / 17.855s elapsed, 18.739s / 18.812s user CPU, 1.398s / 1.365s system
CPU, and 6 / 7 MiB peak heap. Each root contained one 3,955-byte checkpoint;
all three deterministic identities above matched across runs.

## Residual Risks And Non-Goals

- The real TI 2026 roster identity and 100-replay full-history gate are field
  work that requires an independently reviewed scope successor; this delivery
  provides the complete, tested pipeline plus the honest restricted/no-go
  evidence path.
- manta string-table update bounds remain; first-blood/building-kill gold-XP
  actors and named item purchases/entity-state metrics stay deferred until
  correctly reconstructed.
- No live-network call from the insight engine, no replay-derived live
  enrichment, no speculative database service, no UI/OBS work (non-goals).
