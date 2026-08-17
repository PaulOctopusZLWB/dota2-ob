# Dota2-OB

Local Linux Dota 2 live-spectator analytics system for PaulPC4090.

The project goal is to observe Dota 2 matches through Steam/Dota 2 on Linux, collect stable live telemetry, and build deep analytics around player performance, positions, economy, items, skills, objectives, and derived events.

## Current Direction

Use a layered source model:

1. Steam Web API for match discovery and metadata.
2. Local Dota 2 Game State Integration for live observer telemetry.
3. Replay/demo parsing for validation and backfill.
4. Computer vision only for visible UI facts unavailable from stable data sources.
5. Unofficial Game Coordinator, packet, or memory methods only after explicit approval.

## Current MVP

MVP3 starts with manual live-capture and operator hardening. Evidence packaging
is deferred until multiple raw sessions have been measured. Steam Web API
metadata and replay/demo validation remain optional, isolated adapters behind
source-specific contracts.

See:

- `docs/specs/2026-08-05-mvp3-manual-capture-and-source-boundaries.md`
- `docs/specs/2026-08-05-mvp3-block-a-operator-hardening.md`

## Workspace Flow

Development follows:

1. Spec.
2. Fullstack implementation.
3. Local verification.
4. Independent code review.
5. G胖 final acceptance.

See:

- `docs/workspace_operating_model.md`
- `docs/safety_and_account_risk.md`
- `docs/spec_template.md`
- `docs/review_checklist.md`
- `docs/plans/2026-07-05-mvp-gsi-validation.md`
- `research/dota2_live_data_sources.md`
- `research/multica_agent_harness_best_practices.md`

## Local delivery boundary

The capture listener defaults to `127.0.0.1:43210`; the separate operator and
OBS overlay listener defaults to `127.0.0.1:43211`. Production capture exposes
only `/gsi`, `/healthz`, and `/api/status`. Use `--diagnostic-mode` to re-enable
the deprecated dashboard and diagnostic JSON routes behind bearer and
same-origin checks.

For an explicit operator-process token handoff, create a user-only directory
and pass an absolute path inside it with `--operator-token-file`. The process
creates that file as `0600`, never logs the token or path, and removes it on
shutdown. If the flag is omitted, the documented handoff is
`$XDG_RUNTIME_DIR/dota2-ob/runtime/operator.token`, falling back to the same
path under the user's cache directory when no runtime directory is configured.
