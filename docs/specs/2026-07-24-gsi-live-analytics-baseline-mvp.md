# GSI Live Analytics Baseline MVP

Date: 2026-07-24

## Title

GSI Live Analytics Baseline MVP

## Objective

Build the first analytics layer on top of the accepted local GSI collector. When this work is complete, a Dota2-OB session can produce normalized telemetry ticks, derived event logs, a compact analytics summary, and local dashboard/API views from either live GSI POSTs or an existing captured `raw.jsonl` session.

This MVP should answer whether the project can turn verified spectator GSI fields into useful, low-risk realtime analysis without adding a new data source.

## Context

The first MVP, `DOT-1`, was accepted on 2026-07-07.

Accepted evidence:

- Accepted commit: `0f097fe`
- Evidence session: `20260707T144746.208213389Z`
- Public DotaTV match: `8885589324`
- Raw snapshots: 326 valid JSONL records
- Complete ten-player hero/player frames: 314
- Hero positions and economy fields: observed in 314 frames
- Field profile: `snapshot_count=326`, `field_count=5116`
- Exact ward coordinates: not available/observed

The Dota Labs check on 2026-07-24 found no public Dota Labs API, webhook, SDK, plugin hook, or data export surface. Dota Labs and Overwolf must not become MVP dependencies.

## Source Of Truth

- Repository: `https://github.com/PaulOctopusZLWB/dota2-ob.git`
- Existing MVP plan: `docs/plans/2026-07-05-mvp-gsi-validation.md`
- Accepted session evidence: `data/sessions/20260707T144746.208213389Z/raw.jsonl`
- Accepted session summary: `data/sessions/20260707T144746.208213389Z/session_summary.md`
- Data source research: `research/dota2_live_data_sources.md`
- Safety policy: `docs/safety_and_account_risk.md`
- Review checklist: `docs/review_checklist.md`
- Current server entrypoint: `cmd/dota2-ob/main.go`
- Current GSI receiver: `internal/gsi/server.go`
- Current latest-state projection: `internal/state/latest.go`
- Current profiler: `internal/profile/profiler.go`

## Requirements

### Functional Requirements

- Add a normalized telemetry model for GSI snapshots.
- Normalize at least these fields when present:
  - receive timestamp,
  - provider timestamp/version/app id,
  - match id when present,
  - map clock/game time and game state,
  - Roshan state,
  - tower/rax/building health where present,
  - ten player/hero slots by team and slot id,
  - hero name/id, level, alive/dead, health/mana, respawn status, buyback fields when present,
  - `xpos`/`ypos`,
  - player K/D/A, gold, reliable/unreliable gold, net worth, GPM, XPM, last hits, denies,
  - item slot names, cooldowns, charges, and ability names/levels/cooldowns when present,
  - ward-related counters and ward purchase cooldowns when present.
- Preserve raw snapshots as the primary evidence. Normalization must never replace raw JSONL persistence.
- Produce `normalized_ticks.jsonl` under each analyzed session directory.
- Produce `derived_events.jsonl` under each analyzed session directory.
- Produce `analytics_summary.json` and `analytics_summary.md` under each analyzed session directory.
- Support offline analysis of an existing session with a CLI path such as `--analyze-session ./data/sessions/<session-id>`.
- Support live analytics updates while the server receives `/gsi` snapshots.
- Expose local JSON APIs:
  - `GET /api/analytics` for current summary,
  - `GET /api/events` for recent derived events, with a bounded default limit.
- Extend the local dashboard with:
  - recent event feed,
  - team/player economy deltas,
  - objective/building/Roshan changes,
  - data freshness and snapshot count.

### Derived Event Requirements

Derive events only from observed GSI deltas. Every event must include source fields, `received_at`, and confidence.

Baseline event types:

- hero death and respawn from alive/dead or respawn field changes,
- kill/death/assist counter increments,
- meaningful gold/net worth changes at player level,
- item acquired, removed, or slot-changed from item slot name deltas,
- ability level changes and cooldown state changes when stable enough to infer,
- building health decrease and building destroyed,
- Roshan state changes,
- ward-related counter or purchase-cooldown changes.

Do not infer hidden state. Exact ward coordinates must remain `not available` unless a first-class observed field exists.

### Data Contract Requirements

- Each normalized field should be labeled by source, cadence basis, expected delay basis, nullability, and confidence where practical.
- Missing, null, unknown, or newly added GSI fields must not crash normalization or event derivation.
- JSON output must be deterministic enough for tests.
- API responses must be bounded so a long live session does not return unbounded history by default.

## Non-Goals

- No Dota Labs or Overwolf dependency.
- No Game Coordinator integration.
- No process memory reads, code injection, packet capture, protocol bypass, anti-cheat bypass, gameplay automation, account automation, DotaTV delay bypass, or hidden-state extraction.
- No Steam Web API metadata enrichment in this MVP unless needed only for optional labels.
- No replay/demo parser integration.
- No ML model, prediction, win probability, or hero recommendation.
- No production database.
- No polished product UI beyond a usable local dashboard.

## Implementation Notes

- Keep implementation inside the existing Go project.
- Prefer small packages such as:
  - `internal/telemetry` for normalized tick models and extraction helpers,
  - `internal/analytics` for stateful delta/event derivation and summary aggregation.
- Avoid over-typed assumptions for GSI. Use typed structs for Dota2-OB output contracts, but read source GSI through safe map/path helpers where fields are optional.
- Reuse the current `session.Store`, `state.Latest`, and `profile.Profiler` flow.
- Offline analysis should read `raw.jsonl` records written by the current store format.
- Live mode should update analytics only after a snapshot is accepted and persisted.
- All persisted derived files must stay under `data/sessions/<session-id>/` and must remain ignored by git.
- Use the existing accepted session as a real-world fixture for smoke verification, but unit tests should use compact fixtures committed in test files or under a dedicated testdata directory.

## Acceptance Criteria

- `go test -count=1 ./...` passes.
- Existing GSI receiver, latest-state API, profiler, and dashboard tests still pass.
- Offline analysis of `data/sessions/20260707T144746.208213389Z/raw.jsonl` completes without panic.
- Offline analysis writes:
  - `normalized_ticks.jsonl`,
  - `derived_events.jsonl`,
  - `analytics_summary.json`,
  - `analytics_summary.md`.
- `normalized_ticks.jsonl` contains valid JSONL and at least 300 normalized ticks for the accepted evidence session.
- `analytics_summary.md` reports:
  - session id,
  - snapshot/tick counts,
  - observed time range,
  - complete ten-player frame count,
  - per-team or per-player economy summary,
  - event counts by type,
  - objective/Roshan/building observations,
  - ward-related observations,
  - explicit exact ward coordinate conclusion.
- `derived_events.jsonl` contains valid JSONL. If a specific event type is not observed, the summary must say `not observed` rather than fabricating events.
- Live server mode exposes `GET /api/analytics` and `GET /api/events`.
- Dashboard remains local/offline and does not use external CDN resources.
- Safety gate passes: no memory reading, injection, packet capture, Dota automation, unofficial GC behavior, credential storage, or external dependency on Dota Labs/Overwolf.

## Verification

Implementer must run from inside `dota2-ob/`:

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$PATH
export GOCACHE=/tmp/dota2-ob-gocache
pwd
go test -count=1 ./...
go run ./cmd/dota2-ob --analyze-session ./data/sessions/20260707T144746.208213389Z
test -s ./data/sessions/20260707T144746.208213389Z/normalized_ticks.jsonl
test -s ./data/sessions/20260707T144746.208213389Z/derived_events.jsonl
test -s ./data/sessions/20260707T144746.208213389Z/analytics_summary.json
test -s ./data/sessions/20260707T144746.208213389Z/analytics_summary.md
git status --short
```

Live API smoke check:

```bash
go run ./cmd/dota2-ob --addr 127.0.0.1:43210 --data-dir ./data/sessions
curl -s http://127.0.0.1:43210/api/analytics
curl -s http://127.0.0.1:43210/api/events
curl -i http://127.0.0.1:43210/
```

Expected result shape:

- Tests pass.
- Offline analysis exits successfully.
- Generated files are valid JSONL/JSON/Markdown.
- `git status --short` does not show generated session artifacts.
- API responses are valid JSON and bounded.
- Dashboard HTML loads locally.

## Review Focus

- Correctness of optional/null GSI field handling.
- Delta/event derivation does not hallucinate unobserved state.
- Raw evidence remains the source of truth before normalized or derived outputs.
- Session output files remain ignored by git.
- Long-session API responses are bounded.
- No secrets, external calls, Dota automation, memory access, packet capture, Game Coordinator behavior, or Dota Labs/Overwolf dependency.
- Verification uses both focused fixtures and the accepted real session.
