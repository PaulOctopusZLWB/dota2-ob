# MVP3 Block A: Manual Capture And Operator Hardening

Date: 2026-08-05

Parent architecture:
`docs/specs/2026-08-05-mvp3-manual-capture-and-source-boundaries.md`

## Objective

Turn the accepted GSI receiver into a predictable manual operator workflow on
PaulPC4090. A user must be able to check readiness, start a local session,
distinguish waiting/receiving/stale/degraded states, stop it manually, and
recover every accepted snapshot from raw JSONL even if downstream processing
fails.

## Context

The accepted MVP2 server persists raw GSI and then updates latest state,
profiling, analytics, summaries, APIs, and dashboard. Those post-persistence
steps currently execute directly in the HTTP handler. In particular, a summary
write error can return HTTP 500 after raw evidence has already been accepted,
which makes operator and retry semantics ambiguous.

This block is the first implementation stage of DOT-14. It does not implement
Steam metadata, replay parsing, evidence packaging, or Dota automation.

## Source Of Truth

- Architecture contract:
  `docs/specs/2026-08-05-mvp3-manual-capture-and-source-boundaries.md`
- Accepted baseline: `main` at
  `b47d71ac2442ff4a0af93f79c4484d758b066b59`
- Entrypoint: `cmd/dota2-ob/main.go`
- GSI HTTP adapter: `internal/gsi/server.go`
- Raw store: `internal/session/store.go`
- Existing APIs/tests: `internal/gsi/server_test.go`
- Dashboard: `web/index.html`
- Runbook: `docs/manual_test_gsi.md`
- Safety policy: `docs/safety_and_account_risk.md`

## Requirements

### Raw-First Processing Semantics

- Preserve body-size and single-JSON-value validation.
- A valid GSI POST is accepted only after exactly one complete,
  newline-terminated raw record has been written to the active session file.
- The Block A persistence guarantee is a successful full write accepted by the
  operating system. It is intentionally OS-buffered: there is no per-record
  `fsync`, and Block A does not claim survival across a kernel crash, storage
  failure, or sudden power loss.
- The appender serializes sequence allocation and file writes. The final newline
  is the commit marker. A short/partial write must be rolled back to the prior
  committed byte offset before the handler returns a retryable 5xx. If rollback
  cannot be verified, seal the session against later writes and return a stable
  non-accepted 5xx until startup recovery removes the unterminated tail.
- Use a session-scoped file handle or an equivalent injectable append boundary
  so per-record `Close` is not an ambiguous acceptance gate. A full write is
  accepted even if orderly shutdown later reports a close error; log that close
  error with stable code `raw_close_failed` without changing an earlier HTTP
  result.
- A terminated invalid JSONL record is corruption. Recovery must fail safely and
  leave the file untouched; it may remove only bytes after the last committed
  newline.
- A raw append failure does not update projections. A rolled-back failure returns
  HTTP 500; a sealed store returns HTTP 503 without attempting another write.
- Use stable raw-store codes `raw_append_failed`, `raw_store_sealed`, and
  `raw_close_failed`. A later accepted append may clear a transient active raw
  failure; a sealed store cannot recover in-process.
- Every accepted append returns exactly HTTP 200, content type
  `text/plain; charset=utf-8`, and body `ok\n`, whether downstream processing
  succeeds or fails. No post-append error may replace this with a non-2xx.
- Exactly-once means one append and one projection attempt per accepted request
  inside this process. Separate HTTP requests are not deduplicated because GSI
  supplies no idempotency key.
- Offline `--analyze-session` remains the deterministic recovery path for
  derived artifacts.

### Accepted Record And Raw Compatibility

Persist every accepted Block A record as raw-envelope schema version `2`:

```json
{
  "schema_version": 2,
  "session_id": "20260805T120000.000000000Z",
  "sequence": 1,
  "received_at": "2026-08-05T12:00:00Z",
  "source": "gsi",
  "payload": {},
  "raw": {}
}
```

- `session_id`, `sequence`, `received_at`, and `source` are persisted capture
  facts; none is transient-only.
- `sequence` starts at one and is unique and strictly increasing in committed
  raw-file order. Allocation and append happen under the same serialization
  boundary; failed appends do not consume a sequence.
- `payload` is the decoded value used by projections. `raw` is a semantically
  equivalent JSON value. Byte-for-byte whitespace, escape spelling, and object
  key layout are not part of Block A's contract.
- A missing `schema_version` denotes MVP2 schema version `1`, containing
  `received_at`, `payload`, and `raw`. Offline readers derive its session id from
  the directory, derive sequence from valid newline-terminated file order
  starting at one, and set source to `gsi`.
- Readers accept versions 1 and 2 and reject unknown explicit versions with a
  bounded error. They ignore/recover only an unterminated final fragment; an
  invalid newline-terminated record fails analysis without discarding earlier
  evidence.

### Projection Ordering And Recovery

After append acceptance, invoke every configured projection group once in this
fixed order:

1. `latest`,
2. `profile` (observe, then write the profile summary),
3. `analytics` (normalize, observe, then write analytics summaries).

- Continue to later groups when an earlier group fails. Within a group, do not
  run steps that depend on a failed earlier step.
- Record active failure independently for each group. A complete later success
  by that same group clears only its own active failure; success by another group
  cannot clear it.
- Increment `post_processing_failure_count` once per failed group attempt. It
  does not count failed requests or individual operations inside one group.
- Keep active failures until all affected groups have demonstrated recovery.
  Historical counters remain monotonic.
- Log each failed group with a stable code and no raw payload, credential, or
  arbitrary path content. Use `latest_failed`, `profile_failed`, and
  `analytics_failed` for the three group-level failure codes.

### Operator State Model

Add a concurrency-safe operator tracker with an injectable clock and configurable
stale threshold.

Required top-level states:

- `waiting`: process is ready but no valid snapshot has been accepted.
- `receiving`: a valid snapshot was accepted within the stale threshold and no
  active post-processing failure exists.
- `stale`: at least one valid snapshot was accepted, but none within the stale
  threshold, and no active post-processing failure exists.
- `degraded`: raw capture is unavailable/sealed, or a tracked required
  projection group has an active failure.

The snapshot must include:

- process start time,
- session id,
- stale threshold seconds,
- last request time,
- last accepted time,
- last analytics success time when analytics is configured,
- `request_count`, `accepted_count`, `rejected_count`,
  `raw_write_failure_count`, and `post_processing_failure_count` counters,
- active failures keyed by the bounded subsystem ids `raw`, `latest`, `profile`,
  and `analytics`,
- latest bounded error records with code, subsystem, safe message, and time.

`request_count` counts POST attempts reaching `/gsi`; `accepted_count` counts
successful raw commits; `rejected_count` counts validation/body-limit failures;
and `raw_write_failure_count` counts valid requests that fail before acceptance.
`post_processing_failure_count` has the per-group-attempt meaning defined above.

Error history must have a fixed maximum length. Messages must not contain the
raw request body, API keys, Steam credentials, cookies, or arbitrary filesystem
contents.

State calculation must happen at snapshot time so an idle `receiving` process
transitions to `stale` without requiring a new POST.

### Local Status API

Add `GET /api/status`.

- Return JSON and a bounded response.
- Return HTTP 200 for waiting, receiving, stale, and degraded process states;
  state is represented in the body.
- Reject unsupported methods consistently with the other APIs or explicitly
  document their read-only behavior.
- Do not expose raw payloads, credentials, absolute home-directory paths, or
  unbounded error/log history.
- Existing endpoints and response contracts remain available.

### Doctor/Preflight Command

Add a one-shot `--doctor` mode. It exits after printing a deterministic summary
and must not start the long-running HTTP server.

Required checks:

- listen address parses and can be bound at check time,
- data root can be created and written,
- dashboard `index.html` can be read,
- an explicitly supplied GSI config exists and its URI targets the selected
  localhost address and `/gsi` path,
- when no explicit GSI config is supplied, known Linux Steam locations may be
  inspected; absence is a clearly labelled warning rather than a failure on a
  development machine without Dota 2.

Listen and URI rules are explicit:

- Accept only host `127.0.0.1`, literal `localhost`, or IPv6 loopback `::1`, with
  an explicit port in `1..65535`. Normalize `localhost` to `127.0.0.1` before
  binding and comparison.
- Reject an empty host, wildcard addresses (`0.0.0.0` and `::`), non-loopback
  addresses, hostnames other than literal `localhost`, and port zero.
- A configured GSI URI must use `http`, contain no user info, query, or fragment,
  and have path exactly `/gsi`. Its normalized host and exact port must equal the
  selected listener. IPv4/`localhost` and IPv6 are separate endpoint families;
  `::1` is equivalent only to `[::1]`.
- URI validation is syntactic and performs no DNS or outbound network request.

Add a `--gsi-config` flag for an explicit config path. The implementation may
add a focused config reader for the controlled Valve KeyValues shape used here,
but must not attempt to build a general VDF framework.

The doctor result must distinguish required failures, warnings, and successful
checks. Print one bounded JSON object with this stable check order:

1. `listen_address`,
2. `listen_available`,
3. `data_root`,
4. `dashboard_asset`,
5. `gsi_config`.

Each check has only `id`, `status` (`pass`, `warning`, or `fail`), and a bounded
safe `message`. The top-level object has `ok` and `checks`. Exit `0` when no
required check fails, `1` when one or more required checks fail, and retain the
standard flag parser's exit `2` for invalid CLI syntax. Always close the
point-in-time listener before exit, including after later failures. It must not:

- launch or stop Steam/Dota 2,
- copy or rewrite a Dota config,
- join a match,
- make outbound network calls,
- read credentials.

Doctor code must expose injected filesystem, listener, and config-location
boundaries for tests. Negative tests use controlled fakes or temporary fixtures,
not host permissions, real occupied ports, Steam installation state, DNS, or
timing. A data-root probe may create the requested root and one uniquely named
file inside it; it must close and remove the probe file. No test or doctor path
may leave an HTTP server running or invoke a Steam/Dota process-control surface.

### Dashboard And Logs

- Display waiting, receiving, stale, and degraded status prominently but
  compactly in the existing operational dashboard.
- Show session id, accepted count, last accepted age, and a concise active error
  when present.
- Do not add tutorial or architecture prose to the dashboard.
- Preserve the existing latest, analytics, event, economy, objective, and
  freshness panels.
- Keep all assets local; no CDN or external calls.
- Startup logs state listen address, session id, and relative capture target.
- Failure logs identify subsystem and error code without raw payloads or
  secrets.

### Manual Runbook

Update `docs/manual_test_gsi.md` to cover:

1. doctor/preflight,
2. manual GSI config placement,
3. receiver start,
4. health/status/dashboard checks,
5. manual Steam/Dota launch and spectator join,
6. expected waiting/receiving/stale/degraded transitions,
7. manual stop,
8. artifact inspection,
9. offline analysis,
10. the measurements required by the architecture evidence-design gate.

## Boundary And Implementation Notes

- Keep `internal/gsi` responsible for HTTP transport only. Introduce the
  smallest application-level processing abstraction needed to make raw
  acceptance and downstream failure isolation explicit.
- Put sequence allocation, append commit/rollback, store sealing, and trailing
  fragment recovery behind an injectable `internal/session` boundary. Do not
  expose filesystem operations to `internal/gsi`.
- The application processor may coordinate latest state, profiler, analytics,
  and summary writers. It must consume an already accepted versioned session
  record and preserve the fixed group ordering and continue-on-error rules.
- Prefer consumer-owned narrow interfaces over a generic plugin/event system.
- `internal/operator` should own status/counter/error state and time-based state
  calculation.
- Preserve existing constructor options or provide a focused migration that
  keeps current tests readable. Do not combine this block with moving all
  analytics types to new packages.
- Use injected clocks and failing test doubles for state transitions and error
  paths. Do not use sleep-based tests.
- A preflight port check is point-in-time only; the startup path must still
  handle a later bind failure normally.

## Non-Goals

- No automated Dota/Steam actions.
- No systemd service, daemon manager, container deployment, or background
  supervisor.
- No evidence archive/package/upload design.
- No Steam Web API integration or API-key handling.
- No replay parser or replay download.
- No asynchronous queue unless a measured test proves the synchronous boundary
  cannot satisfy capture reliability.
- No database or generic plugin framework.
- No changes to analytics event meaning.
- No high-risk source or hidden-state work.

## Acceptance Criteria

- `--doctor` passes against repository assets and a writable temporary data
  root without Steam, Dota 2, outbound network, or credentials.
- `--doctor` fails clearly for an occupied/invalid listen address, unwritable
  data target, missing dashboard, and an explicitly invalid GSI config.
- Doctor negative tests use injected boundaries or controlled fixtures and prove
  exit code, stable ordered result shape, closed listener, no outbound network,
  and no Steam/Dota mutation without sleeps or host-specific permissions.
- Doctor accepts the defined IPv4/`localhost` and IPv6 forms, rejects wildcard
  and non-loopback addresses, and enforces normalized URI host, exact port, and
  exact `/gsi` path equivalence.
- `/api/status` returns valid bounded JSON for all four operator states.
- A valid POST creates exactly one version 2 raw JSONL record, returns HTTP 200
  `ok\n`, and increments accepted count once.
- Invalid or oversized input creates no raw record and increments rejected
  count.
- A forced short write rolls back to the previous committed offset, preserves
  all earlier records, returns HTTP 500, and does not update projections.
- A forced rollback failure seals the store; later POSTs return HTTP 503 without
  writing, and startup recovery removes only the unterminated tail.
- Orderly close failure cannot reverse an accepted response and is reported with
  stable code `raw_close_failed`; no per-record sync/close is required.
- Version 1 and version 2 fixtures both rebuild, while an unknown explicit
  version and an invalid terminated record fail with bounded errors.
- Concurrent valid POSTs persist unique, strictly increasing sequences in file
  order with no duplicate or consumed failed sequence.
- A forced `latest`, `profile`, or `analytics` group failure after append retains
  the raw record, returns HTTP 200 `ok\n`, increments the failure counter once
  per failed group attempt, continues to later independent groups, and exposes
  degraded state.
- An overlapping recovery test proves: A fails; then B fails while A succeeds;
  state remains degraded for B; only B's later success clears degradation. It
  also proves each configured group ran at most once for every accepted
  sequence and historical counters never decreased.
- Waiting-to-receiving-to-stale transitions are deterministic under an injected
  clock.
- Existing APIs, offline analysis, analytics output, and dashboard tests still
  pass.
- Runbook reflects the actual implemented flags and output.
- Safety gate passes.

## Verification

Run from the repository root:

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$PATH
export GOCACHE=/tmp/dota2-ob-gocache
go test -count=1 ./...
go vet ./...
git diff --check
```

Doctor smoke test:

```bash
DOTA2_OB_DOCTOR_DATA="$(mktemp -d)"
go run ./cmd/dota2-ob \
  --doctor \
  --addr 127.0.0.1:43210 \
  --data-dir "$DOTA2_OB_DOCTOR_DATA" \
  --gsi-config ./configs/gamestate_integration_dota2_ob.cfg
```

Expected result shape:

- command exits 0,
- required checks report pass,
- no long-running server remains,
- no outbound network or Steam/Dota action occurs.

Implementer must also report exact focused test names covering:

- full and short writes, verified rollback, rollback failure/store sealing,
  trailing-fragment recovery, and close failure at shutdown,
- version 1/version 2 reads, unknown versions, semantic raw preservation, and
  concurrent sequence order,
- every projection-group failure plus the overlapping A/B recovery scenario,
  exact invocation counts/order, HTTP 200 after acceptance, and monotonic
  per-group-attempt failure counts,
- waiting/receiving/stale/degraded transitions and bounded errors,
- invalid, occupied, wildcard, non-loopback, IPv4/`localhost`, and IPv6 listener
  cases,
- unwritable data root, missing dashboard, explicit invalid config, discovered
  config absence, URI mismatch, ordered doctor JSON, exit codes, listener close,
  and absence of outbound network or Steam/Dota mutation.

All focused tests use injected clocks and side-effect boundaries or controlled
temporary fixtures. They must not use sleeps, host permission assumptions, real
Steam paths, credentials, or internet access.

## Review Focus

- HTTP success/failure semantics match the raw-first invariant.
- Partial append recovery preserves prior records, and the OS-buffered
  acceptance guarantee is not misrepresented as power-loss durability.
- A post-processing error cannot erase evidence or invite accidental duplicate
  raw appends.
- Degraded-state recovery is based on subsystem success, not merely time or a
  new request.
- Persisted version 2 identity, concurrent sequence order, and MVP2 replay
  compatibility match the raw-envelope contract.
- Status data is race-safe, bounded, and free of raw/secrets/path leakage.
- Doctor checks are deterministic, loopback-only, and side-effect limited.
- No sleep-based flaky tests.
- No regression in existing analytics or offline rebuild behavior.
- No automation or source-policy violation.
