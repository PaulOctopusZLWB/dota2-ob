# MVP3 Manual Capture And Source-Boundary Architecture

Date: 2026-08-05

Decision owner: Paul

Status: approved direction; implementation pending

## Objective

Make the accepted GSI analytics baseline reliable enough for repeated manual
spectator runs on PaulPC4090, while defining small and stable boundaries for
future Steam Web API metadata and replay/demo validation.

MVP3 must produce useful raw sessions before the project designs durable
evidence packaging. It must not automate Steam, Dota 2, matchmaking, DotaTV
joining, or account actions.

## Decision Summary

Paul approved the following direction on 2026-08-05:

- Defer evidence-session archive and distribution design until multiple raw
  sessions have been collected and their actual size, cadence, field coverage,
  interruption behavior, and privacy characteristics are known.
- Operate the live capture flow manually first. Automation is a later decision.
- Define high-cohesion, low-coupling boundaries for live capture, optional Steam
  metadata, and offline replay validation before implementing those sources in
  parallel.
- Test each block against its own contract before cross-block integration.

## Context

MVP2 was accepted at commit
`57ce20205dc371ae1ddd63137599704b834e6b74` and merged to `main` as
`b47d71ac2442ff4a0af93f79c4484d758b066b59`.

The current flow is:

```text
POST /gsi
  -> session.Store.Append
  -> state.Latest.Update
  -> profile.Profiler.Observe + summary write
  -> analytics.Normalize + Engine.Observe + summary write
  -> HTTP response
```

This proves the data path, but transport, persistence, derived processing,
operator state, and response semantics are still coordinated directly inside
`internal/gsi`. Future metadata or replay work must not add more source-specific
logic to that request handler.

## Source Of Truth

- Accepted code: `main` at
  `b47d71ac2442ff4a0af93f79c4484d758b066b59`
- Current live entrypoint: `cmd/dota2-ob/main.go`
- Current GSI adapter: `internal/gsi/server.go`
- Raw session store: `internal/session/store.go`
- Current normalized contract: `internal/analytics/tick.go`
- Current derivation and summaries: `internal/analytics/engine.go` and
  `internal/analytics/summary.go`
- Manual runbook: `docs/manual_test_gsi.md`
- Data-source survey: `research/dota2_live_data_sources.md`
- Safety policy: `docs/safety_and_account_risk.md`

## Architecture Decision

### Component Flow

```text
Manual Dota 2 spectator
          |
          v
    GSI HTTP adapter
          |
          v
  Capture application service -----> Operator status/readiness
          |
          +---- raw-first ----> Raw session store
          |
          `---- accepted record ----> GSI normalizer ----> Analytics engine
                                                            |
                                                            v
                                                     Local APIs/dashboard

Steam Web API adapter ----> Metadata cache/read model -------^
       (optional; query-time presentation join only)

Downloaded replay/demo ----> Replay adapter ----> Validation facts
                                                   |
Persisted GSI artifacts ----------------------------+----> Validation report
       (offline comparison only)
```

### Dependency Rule

Dependencies point from source adapters and process wiring toward narrow domain
contracts. The live analytics core must not import HTTP, filesystem, Steam Web
API, replay parser, dashboard, or credential concerns.

`cmd/dota2-ob` is the composition root. It may wire concrete adapters together,
but it must contain no source parsing or analytics rules.

The project will not introduce a generic plugin registry, universal event bus,
or one model that pretends GSI, Steam metadata, and replay facts have identical
semantics. Those abstractions would increase entropy before there is evidence
that they remove real duplication.

## Boundary Contracts

### 1. Capture Boundary

Responsibility:

- Accept one local GSI request.
- Enforce method and body-size constraints.
- Validate that the body is one JSON value.
- Append the untouched payload to the active raw session.
- Produce an accepted-record envelope only after the append succeeds.

The accepted-record envelope owns only capture facts:

- session id,
- monotonically increasing sequence within the process session,
- receive timestamp,
- source kind (`gsi`),
- decoded payload,
- original raw JSON.

It does not own normalized Dota fields, analytics events, Steam labels, or replay
truth.

Raw persistence is the acceptance boundary. A failure before raw append returns
a request error. A failure in profiling, normalization, analytics, or summary
materialization after raw append marks the process degraded and is reported by
operator status, but must not claim that the already-persisted GSI request was
rejected.

### 2. Operator Boundary

Responsibility:

- Report whether the local process is ready to receive GSI.
- Expose whether it is waiting, receiving, stale, or degraded.
- Report the current session id and safe session location relative to the data
  root.
- Report accepted/rejected/post-processing-failure counters.
- Report first accepted time, last request time, last accepted time, and last
  successful analytics update time.
- Expose stable error codes and concise messages without secrets or raw payloads.

The initial operator surface is manual:

- a preflight/doctor command,
- structured startup and failure logs,
- `GET /api/status`,
- clear waiting/receiving/stale/degraded states in the local dashboard,
- an updated manual runbook.

Preflight may inspect local configuration, port availability, data-directory
writability, and required web assets. It must not launch or control Steam or
Dota 2.

### 3. Normalized Telemetry Boundary

`NormalizedTick` remains the live analytics input. It represents only values
observed in one accepted GSI payload.

Every field family must have documented provenance:

- source,
- source version when known,
- receive/observation time basis,
- cadence basis,
- expected delay basis,
- nullability,
- confidence.

Missing values remain missing. Steam metadata and replay-derived values must not
be written into `NormalizedTick`, because doing so would make source, timing,
and confidence ambiguous.

Normalization must be deterministic and side-effect free. The analytics engine
consumes normalized ticks and owns delta/event derivation; it does not call any
source adapter.

### 4. Steam Metadata Boundary

Steam Web API integration is an optional enrichment adapter, not part of the
live capture critical path.

Its domain contract returns metadata snapshots keyed by stable identifiers such
as match id, hero id, league id, team id, or account id. Each snapshot includes:

- source endpoint,
- fetched time,
- data timestamp when supplied by Steam,
- freshness/expiry state,
- nullable fields,
- availability or error status.

Metadata is joined at query/presentation time through a read model. It must not
mutate raw records, normalized ticks, or derived events.

Required failure behavior:

- No API key: enrichment is disabled and capture remains healthy.
- Rate limit, timeout, or unavailable endpoint: return stale cached data when
  allowed, otherwise an explicit unavailable state.
- Unknown match/player: preserve identifiers and leave labels null.
- API key: injected from the environment or an external secret facility; never
  stored in code, config committed to git, logs, issue metadata, raw sessions,
  or API responses.

No Steam network call is required for deterministic unit or integration tests.

### 5. Replay Validation Boundary

Replay/demo parsing is post-match validation only. Its input is a downloaded
local replay file plus an explicit match identity. It must never feed hidden
replay state into the live dashboard or live analytics engine.

The replay adapter emits source-labelled validation facts. The validator
compares those facts with immutable GSI-derived artifacts and produces a report
containing:

- metric or event identity,
- GSI-observed value and evidence reference,
- replay-derived value and evidence reference,
- clock/timeline alignment method,
- tolerance,
- delta,
- pass, mismatch, unavailable, or incomparable status,
- explanation for null or incomparable values.

The shared cross-source concepts are intentionally small:

- match identity,
- participant/hero identity,
- game-clock anchor,
- metric/event name,
- value plus provenance,
- comparison result.

Replay entities and protobuf structures remain inside the replay adapter. They
must not leak into analytics or presentation packages.

## Package Ownership Target

The exact package split may be introduced incrementally. The target ownership
is:

| Package area | Owns | Must not own |
|---|---|---|
| `internal/gsi` | HTTP/GSI transport adapter | storage layout, analytics rules, Steam/replay logic |
| `internal/session` | append-only raw sessions and accepted records | Dota normalization, metadata, validation |
| `internal/operator` | readiness, lifecycle counters, stable error state | raw payloads, domain derivation |
| `internal/telemetry` | normalized live contracts and GSI normalization | HTTP, filesystem, network clients |
| `internal/analytics` | deterministic tick-to-event/summary logic | source transport, credentials, replay parsing |
| `internal/metadata` | metadata contracts, cache/read model | capture acceptance, tick mutation |
| `internal/metadata/steamweb` | Steam Web API client adapter | analytics rules, credential persistence |
| `internal/validation` | cross-source comparison contracts and reports | replay parsing internals, live mutation |
| `internal/replay` | local demo parser adapter | live ingestion, dashboard enrichment |
| `internal/app` | use-case orchestration and failure isolation | source-specific parsing or metric rules |
| `cmd/dota2-ob` | flags, dependency wiring, process lifecycle | business logic |

Moving the current normalizer from `internal/analytics` to
`internal/telemetry` is allowed only as a focused, behavior-preserving change.
It is not a prerequisite for the first operator-hardening block.

## Data And Error Semantics

### Raw-First Invariant

For each valid accepted GSI POST:

1. exactly one raw record is durably appended,
2. its sequence and receive timestamp identify downstream work,
3. downstream outputs can be rebuilt from raw evidence,
4. downstream failure never rewrites or deletes the raw record.

### Idempotency And Rebuild

Live observation may update in-memory projections once per accepted record.
Offline analysis is the recovery path and must deterministically rebuild
normalized ticks, events, and summaries from `raw.jsonl`.

Metadata cache entries and replay reports are separate derived products. They
must be safe to delete and regenerate without modifying raw evidence.

### Backpressure

MVP3 may process accepted records synchronously, but the boundary must make
post-processing failure distinct from capture failure. Queueing or asynchronous
workers are not required until measurements show that processing latency causes
GSI loss.

### Compatibility

Public local APIs should remain backward compatible during package separation.
New status fields may be added. Renaming or removing existing analytics fields
requires a separate contract change and migration note.

## MVP3 Functional Requirements

- Add a manual preflight command that checks, without changing Dota/Steam state:
  - listen address validity and availability,
  - data root creation/writability,
  - web asset availability,
  - GSI config presence and parseability when an explicit or discovered path is
    available,
  - configured local URI compatibility.
- Add a bounded `GET /api/status` response with operator lifecycle and error
  state.
- Add dashboard states for waiting, receiving, stale, and degraded.
- Preserve the raw-first invariant and make post-processing failures observable.
- Update `docs/manual_test_gsi.md` with exact preflight, start, observe, stop,
  inspect, and offline-analysis steps.
- Keep the process bound to localhost by default.
- Keep Steam metadata and replay validation behind the contracts above; they are
  not required to make manual GSI capture succeed.

## Non-Goals

- No evidence archive format, upload workflow, compression policy, retention
  policy, or sanitization implementation before empirical sessions exist.
- No automatic Steam or Dota 2 launch.
- No automatic DotaTV discovery or joining.
- No gameplay, UI, matchmaking, or account automation.
- No replay download automation or live replay ingestion.
- No Steam Web API key requirement for capture.
- No generic plugin framework, message broker, database, or distributed system.
- No process memory reads, injection, packet capture/decryption, unofficial Game
  Coordinator work, anti-cheat bypass, delay/fog-of-war bypass, or hidden-state
  inference.

## Block Plan And Test Gates

### Block A: Capture And Operator Hardening

Scope:

- capture application boundary,
- raw-first response/error semantics,
- preflight command,
- operator status model/API,
- dashboard operator states,
- manual runbook.

Contract tests must prove:

- invalid payload never creates a raw record,
- accepted payload creates exactly one raw record,
- a forced downstream failure leaves the raw record intact and changes status
  to degraded,
- no-post state is waiting,
- recent accepted traffic is receiving,
- elapsed traffic becomes stale using an injected clock,
- counters and status arrays remain bounded,
- no operator response contains raw payloads or secrets.

### Block B: Metadata Port And No-Network Adapter Tests

Scope:

- metadata domain types and consumer-owned query interface,
- disabled/no-key adapter,
- cache and freshness semantics,
- fixture-backed Steam response decoder tests,
- presentation join that does not mutate analytics contracts.

This block remains independently deployable and may stay disabled in MVP3.

Contract tests must prove disabled, fresh, stale, rate-limited, timeout, unknown
identifier, and malformed-response behavior without internet access or a real
API key.

### Block C: Replay Validation Port And Fixture Tests

Scope:

- validation fact/report types,
- match/timeline alignment contract,
- comparator using synthetic or committed minimal fixtures,
- replay adapter selection spike kept behind the port.

The selected third-party parser must be proven against a local downloaded demo
before implementation proceeds beyond the adapter. Parser choice, version, and
license must be recorded.

Contract tests must prove exact match, tolerated delta, mismatch, missing fact,
wrong match id, ambiguous clock, and parser failure behavior.

### Block D: Cross-Block Integration

Integration starts only after the relevant block contract tests pass.

The integration suite must run with no Dota process, no Steam credentials, and
no network. It uses a compact GSI fixture stream, fixture metadata, and fixture
validation facts to prove:

- raw capture and analytics work with enrichment disabled,
- metadata enriches presentation only,
- metadata failure does not affect capture or analytics,
- replay validation reads completed artifacts only,
- replay mismatch does not mutate live or persisted GSI evidence,
- provenance remains visible at every cross-source result.

### Block E: Manual Field Run

Paul manually launches Steam/Dota 2 and joins a spectator match. The software
only performs preflight, local capture, local status display, and offline
analysis.

Record for each run:

- session id and match id when observed,
- start/end time and wall-clock duration,
- raw record count and bytes,
- average and peak observed cadence,
- complete ten-player frame count,
- field/null coverage summary,
- reconnect, pause, and process-stop behavior when encountered,
- operator-state transitions and post-processing errors,
- any potentially identifying fields requiring later sanitization.

## Evidence-Archive Design Gate

Evidence packaging remains backlog until empirical data exists. Open its design
only after at least three useful raw sessions have been measured, including one
long continuous spectator run and one run that exercises a restart, disconnect,
or stale interval. At least one session should cover enough of a match to expose
session growth and late-game field behavior.

The later archive decision must be based on measured:

- bytes per minute and total session size,
- field stability and schema drift,
- duplicate/reconnect behavior,
- derived-artifact rebuild cost,
- privacy/sanitization needs,
- checksum and manifest requirements,
- retention and sharing target.

These measurements, rather than the current accepted sample alone, decide
whether the archive contains full raw JSONL, a sanitized copy, compressed data,
derived artifacts, or a combination.

## Acceptance Criteria

- This architecture is recorded in the repository and linked from DOT-14.
- Block A has a concrete implementation issue and verification commands.
- Blocks B and C remain parked until their contracts and dependencies are
  reviewed; neither can block manual GSI capture.
- All automated tests pass without Steam, Dota 2, network access, replay files,
  or credentials.
- A manual run can distinguish waiting, receiving, stale, and degraded states.
- Every accepted valid POST remains recoverable from raw JSONL even when a
  downstream processor fails.
- Existing `/api/latest`, `/api/profile`, `/api/analytics`, `/api/events`,
  dashboard, and offline analysis behavior remains available.
- Safety gate passes.

## Verification

Architecture/document verification:

```bash
git diff --check
go test -count=1 ./...
go vet ./...
```

Block A implementer must additionally provide focused tests for the capture and
operator contracts and a deterministic preflight smoke command. Exact flags may
be finalized in the Block A implementation spec, but verification must not
modify Steam/Dota state.

Blocks B-D must provide their own focused package tests plus a no-network
integration command before they are promoted from backlog.

## Review Focus

- Raw append remains the single capture-acceptance boundary.
- Downstream failures are isolated and observable, not silently discarded.
- Interfaces are owned by their consumers and remain source-specific where
  semantics differ.
- Metadata never mutates raw or normalized telemetry.
- Replay data remains offline validation truth and never leaks hidden state into
  live output.
- Tests do not require credentials, internet, Steam, or Dota 2.
- The design does not introduce a speculative plugin framework or a large
  behavior-changing package migration.
- No secrets, raw payloads, or account identifiers appear in operator errors or
  logs beyond explicitly reviewed local evidence outputs.
