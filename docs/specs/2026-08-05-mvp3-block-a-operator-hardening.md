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
- A valid GSI POST is accepted only after exactly one raw record is appended.
- A raw append failure returns a 5xx response and does not update projections.
- Latest state, field profiling, analytics, and derived summary materialization
  run only after raw append succeeds.
- If any post-processing step fails after raw append:
  - retain the raw record,
  - do not process the same record twice inside the request,
  - record a bounded operator error with a stable code,
  - mark operator state degraded,
  - log the failure without raw payload or credentials,
  - return a success-class response that reflects raw acceptance.
- A later successful request may clear the active degraded condition only when
  the failed subsystem has demonstrably recovered. Historical failure counters
  remain monotonic.
- Offline `--analyze-session` remains the deterministic recovery path for
  derived artifacts.

### Operator State Model

Add a concurrency-safe operator tracker with an injectable clock and configurable
stale threshold.

Required top-level states:

- `waiting`: process is ready but no valid snapshot has been accepted.
- `receiving`: a valid snapshot was accepted within the stale threshold and no
  active post-processing failure exists.
- `stale`: at least one valid snapshot was accepted, but none within the stale
  threshold, and no active post-processing failure exists.
- `degraded`: raw capture may still be working, but a tracked required
  post-processing subsystem has an active failure.

The snapshot must include:

- process start time,
- session id,
- stale threshold seconds,
- last request time,
- last accepted time,
- last analytics success time when analytics is configured,
- request, accepted, rejected, raw-write-failure, and post-processing-failure
  counters,
- latest bounded error records with code, subsystem, safe message, and time.

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

Add a `--gsi-config` flag for an explicit config path. The implementation may
add a focused config reader for the controlled Valve KeyValues shape used here,
but must not attempt to build a general VDF framework.

The doctor result must distinguish required failures, warnings, and successful
checks. Exit non-zero when any required check fails. It must not:

- launch or stop Steam/Dota 2,
- copy or rewrite a Dota config,
- join a match,
- make network calls,
- read credentials.

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
- The application processor may coordinate latest state, profiler, analytics,
  and summary writers. It must consume an already accepted session record.
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
  root without Steam, Dota 2, network, or credentials.
- `--doctor` fails clearly for an occupied/invalid listen address, unwritable
  data target, missing dashboard, and an explicitly invalid GSI config.
- `/api/status` returns valid bounded JSON for all four operator states.
- A valid POST creates exactly one raw JSONL record and increments accepted
  count once.
- Invalid or oversized input creates no raw record and increments rejected
  count.
- A forced raw-write failure returns 5xx and does not update projections.
- A forced profiler/analytics/summary failure after append retains the raw
  record, returns a success-class raw-acceptance response, increments the
  post-processing failure count, and exposes degraded state.
- A subsequent demonstrated subsystem recovery clears active degradation while
  preserving historical counters.
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
- no network or Steam/Dota action occurs.

Implementer must also report exact focused test names covering raw-write failure,
post-processing failure, recovery, state transitions, and bounded errors.

## Review Focus

- HTTP success/failure semantics match the raw-first invariant.
- A post-processing error cannot erase evidence or invite accidental duplicate
  raw appends.
- Degraded-state recovery is based on subsystem success, not merely time or a
  new request.
- Status data is race-safe, bounded, and free of raw/secrets/path leakage.
- Doctor checks are deterministic, local-only, and side-effect limited.
- No sleep-based flaky tests.
- No regression in existing analytics or offline rebuild behavior.
- No automation or source-policy violation.
