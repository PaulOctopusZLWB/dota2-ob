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
