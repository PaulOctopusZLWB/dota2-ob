# Dota2-OB Data Source Boundaries

Use this skill when designing, implementing, or reviewing Dota 2 data ingestion and analytics.

Preferred source order:

1. Steam Web API for discovery and static metadata.
2. Local Dota 2 Game State Integration for live spectator telemetry.
3. Replay/demo parsing for retrospective truth, validation, and training data.
4. Computer vision for UI-visible facts not exposed by structured sources.
5. Unofficial Game Coordinator, packet capture, memory access, or other brittle methods only with explicit approval.

Known first-pass facts:

- Steam Web API alone is insufficient for deep realtime telemetry.
- GSI spectator mode is the primary candidate for ten-player live positions, economy, items, abilities, buildings, draft, Roshan state, and map state.
- Exact ward entity positions are not confirmed as a stable GSI field.
- Replay parsing is best for post-match truth and validation.

Every data field should be labeled by source, cadence, expected delay, nullability, and confidence.
