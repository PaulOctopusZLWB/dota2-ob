# MVP GSI Validation Plan

**Goal:** Build the smallest local system that proves whether a Linux Dota 2 spectator client can provide enough live telemetry for ten-player Dota 2 analysis.

**Architecture:** A local Go service receives Dota 2 Game State Integration POSTs, persists raw snapshots, maintains latest state in memory, exposes a minimal local dashboard/API, and generates a post-session field availability report. Steam/Dota 2 client setup remains manual for MVP.

**Tech Stack:** Go HTTP server, JSONL session storage, static HTML dashboard, standard Go tests.

---

## MVP Scope

The MVP answers these questions with evidence from a real spectator session:

- Can we receive GSI data reliably on Linux while spectating?
- Can we identify all ten players and their heroes?
- Can we get live hero coordinates for minimap/player-position analysis?
- Can we get economy state such as gold, net worth, GPM, and XPM?
- Can we get items, ability levels/cooldowns, health/mana, respawn, Roshan, buildings, and draft?
- What ward information is available: counts only, purchase cooldowns, placed/destroyed stats, or exact ward positions?
- What is the update cadence and missing/null rate for each field?

## Non-Goals

The MVP does not include:

- Game Coordinator integration,
- process memory access,
- injection,
- packet capture,
- DotaTV delay bypass,
- automated game joining,
- ML models,
- production database,
- polished product UI,
- exact ward-position extraction unless it appears in GSI data.

## File Structure

Planned implementation files:

- `cmd/dota2-ob/main.go` - CLI entrypoint and server startup.
- `internal/gsi/server.go` - HTTP receiver for GSI POSTs.
- `internal/gsi/schema.go` - loose typed envelope plus helpers for known GSI sections.
- `internal/session/store.go` - session directory creation and raw JSONL persistence.
- `internal/profile/profiler.go` - field path discovery, update counts, null/missing rates, sample values.
- `internal/state/latest.go` - latest-state projection for the local dashboard/API.
- `web/index.html` - minimal static dashboard.
- `configs/gamestate_integration_dota2_ob.cfg` - example Dota 2 GSI config.
- `docs/manual_test_gsi.md` - PaulPC4090 manual test procedure.
- `data/sessions/.gitkeep` - keeps data directory shape while excluding captured data.

Planned tests:

- `internal/gsi/server_test.go`
- `internal/session/store_test.go`
- `internal/profile/profiler_test.go`
- `internal/state/latest_test.go`

## Acceptance Requirements

### A. Safety

- The implementation only uses GSI POSTs, local files, and optional Steam Web API metadata.
- It does not read process memory, inject code, sniff packets, automate Dota 2, or store credentials.
- Steam API keys, if later used, must come from environment variables and must not be logged.

### B. Local GSI Collection

- The service listens on `127.0.0.1` by default.
- The port is configurable.
- It accepts Dota 2 GSI JSON POSTs at `/gsi`.
- It returns `200 OK` for valid JSON and a clear `4xx` for malformed JSON.
- It records receive timestamp and raw body for every accepted snapshot.

### C. Raw Evidence

- Every session creates a directory under `data/sessions/<session-id>/`.
- Raw GSI snapshots are appended to `raw.jsonl`.
- `raw.jsonl` contains one valid JSON object per line.
- Captured session data is ignored by git.

### D. Live State

- `/api/latest` returns the latest known map, player, hero, item, ability, building, draft, and provider sections when present.
- Missing sections are represented as absent/null without crashing.
- The local dashboard shows whether each of the ten player slots has data.

### E. Field Profiling

- `/api/profile` returns:
  - field path,
  - seen count,
  - null count,
  - first seen timestamp,
  - last seen timestamp,
  - at least one sample value for scalar fields.
- The profiler handles nested objects and arrays/maps without hard-coded field lists.

### F. Session Summary

After a test session, the system can generate `session_summary.md` containing:

- session start/end time,
- number of snapshots,
- observed update cadence,
- whether ten-player hero/player data appeared,
- whether `hero.*.xpos/ypos` appeared,
- whether economy fields appeared,
- whether item and ability cooldown fields appeared,
- whether ward-related fields appeared,
- explicit conclusion for exact ward coordinates: available, not available, or not observed.

### G. Manual Test

The manual test procedure must include:

- where to put `gamestate_integration_dota2_ob.cfg` on Linux,
- how to start the local server,
- how to manually join a DotaTV/spectator game,
- how long to observe,
- which output files to inspect,
- how to stop the service cleanly.

## Suggested Issue Breakdown

Issue 1: Write final MVP spec and safety gate

- Create the implementation spec from this plan.
- Include safety constraints as acceptance criteria.
- Confirm exact CLI commands and output paths.

Issue 2: Implement Go project skeleton and GSI receiver

- Create Go module.
- Add `/gsi`, `/healthz`, and basic config flags.
- Add malformed JSON tests.

Issue 3: Implement raw session storage

- Create session directories.
- Append accepted snapshots to JSONL.
- Add tests for valid JSONL and timestamp metadata.

Issue 4: Implement latest-state API and minimal dashboard

- Maintain latest snapshot state.
- Serve `/api/latest` and `web/index.html`.
- Dashboard displays map time and ten player slots.

Issue 5: Implement field profiler and profile API

- Discover nested field paths.
- Track counts/nulls/samples.
- Serve `/api/profile`.

Issue 6: Implement session summary generator

- Generate `session_summary.md`.
- Include the core field availability conclusions.

Issue 7: PaulPC4090 manual spectator test

- Run Dota 2 spectator session.
- Save artifacts.
- Record what was available and what was missing.

Issue 8: Independent code review and MVP acceptance

- Reviewer checks safety boundaries, parser robustness, persistence, tests, and manual-test evidence.

## Open Decisions Before Creating Issues

1. UI level: CLI-only plus JSON APIs, or minimal browser dashboard in MVP?
2. Session duration: 5 minutes enough for first test, or full match segment?
3. Steam Web API: exclude from MVP entirely, or include only optional metadata enrichment?
4. Test account: use main account under low-risk GSI-only boundary, or use a small account for the first manual test?
5. Data format: raw JSONL only for MVP, or also produce Parquet/SQLite in MVP?

## Recommended Defaults

- Include minimal browser dashboard.
- First manual test: 5-10 minutes of spectator time.
- Keep Steam Web API out of first implementation unless needed for metadata.
- Main account is acceptable under GSI-only boundary; small account is fine for comfort.
- Use raw JSONL plus generated Markdown/JSON reports only.
