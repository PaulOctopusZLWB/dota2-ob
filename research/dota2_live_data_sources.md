# Dota 2 Live Spectator Analytics: Data Source Survey

Date: 2026-07-05

## Executive Summary

The most promising first architecture is a local Linux Dota 2 spectator client plus Game State Integration (GSI). In spectator/observer mode, community-documented GSI output can expose ten-player data for hero position, health/mana, net worth, gold, GPM/XPM, items, ability cooldowns/levels, draft, map state, Roshan state, ward purchase cooldowns, and buildings.

Steam Web API alone is not enough for deep realtime analysis. `GetLiveLeagueGames` is useful for discovering live league matches and coarse match state, but it does not provide per-tick positions, inventory cooldowns, or full economic telemetry. `GetRealtimeStats` exists in public interface listings, but it requires a `server_steam_id`; its practical availability and field set need key-backed experiments.

Replay/demo parsing is excellent for post-match truth and model training, but not naturally live unless we can obtain a delayed/streaming demo source. It can still be used to validate GSI and build richer derived metrics.

## Source Classes

### 1. Steam Web API

Confirmed interfaces of interest:

- `IDOTA2Match_570/GetLiveLeagueGames/v1`
- `IDOTA2Match_570/GetMatchDetails/v1`
- `IDOTA2Match_570/GetMatchHistory/v1`
- `IDOTA2MatchStats_570/GetRealtimeStats/v1`
- `IEconDOTA2_570/GetHeroes/v1`

Expected from `GetLiveLeagueGames`:

- live league games list,
- players in game,
- account id,
- display name,
- hero id,
- team,
- radiant/dire team metadata,
- lobby id,
- spectator count,
- tower state,
- league id.

Limitations:

- Requires Steam Web API key for Dota 2 endpoints in practice.
- Live league scope is narrow; normal public matchmaking is not guaranteed.
- Coarse state only; not enough for minimap positions, item cooldowns, current gold, net worth, or live ward positions.

Open question:

- `GetRealtimeStats(server_steam_id)` may expose richer stats if `server_steam_id` can be discovered from live games. Needs controlled testing with a valid key and a known live DotaTV game.

### 2. Local Dota 2 Client + Game State Integration

Configuration path on Linux should be under the local Steam library, equivalent to:

`steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/gamestate_integration_*.cfg`

The client posts JSON to a configured local HTTP endpoint. Community-documented fields in spectator/observer mode include:

- provider: app id, version, timestamp,
- map: clock/game time, day/night, game state, match id, Roshan state, ward purchase cooldowns,
- player per slot: K/D/A, last hits, denies, gold, reliable/unreliable gold, GPM, XPM, net worth, hero damage, camps stacked, wards placed/destroyed/purchased, support gold spent,
- hero per slot: id/name, level, alive, health/mana, respawn, buyback cost/cooldown, debuff states, talent flags, `xpos`, `ypos`,
- items per slot/stash: name, cooldown, charges, rune, passive/can cast,
- abilities: name, level, cooldown, can cast, ultimate/passive flags,
- buildings: tower/rax/ancient health and max health,
- draft: active team, pick/ban ids, bonus time,
- wearables.

Limitations:

- GSI is client-provided JSON and Valve can change fields silently.
- Child objects may be absent/null until first observed.
- It does not document exact observer delay; actual delay depends on DotaTV/lobby settings and must be measured.
- It does not appear to expose exact observer ward entity positions as a first-class field. It exposes ward counts and team ward purchase cooldowns, not a full ward entity table.

### 3. Replay/Demo Parsing

Useful tools:

- Clarity: Java parser for Dota 2 replay files. It can extract combat log, entities, modifiers, temporary entities, user messages, game events, overview, and raw protobuf messages.
- OpenDota parser: replay parse server that generates JSON logs from Dota 2 replay files.

Strengths:

- Best source for retrospective truth, detailed event reconstruction, model training, and validation.
- Can produce richer derived metrics than Steam Web API or GSI.

Limitations:

- Usually post-match or delayed.
- Live replay ingestion would require access to a streamable demo source, which is not confirmed as a stable public API.

### 4. Game Coordinator / Unofficial Steam Client Interfaces

Steamworks exposes `ISteamGameCoordinator` as a client API for sending/receiving game coordinator messages, but Valve describes it as largely deprecated and still present for a few games.

Potential:

- Could discover live games, DotaTV/session data, or richer match metadata depending on Dota 2 GC messages.

Limitations:

- Not a stable public Web API contract.
- Requires Steam login/session handling and Dota 2 protobuf maintenance.
- Higher breakage and account-risk profile than GSI or Web API.

### 5. Computer Vision / Screen Observation

Potential:

- Can read exactly what the spectator UI shows: minimap, scoreboard, item panel, overhead indicators, ward icons if visible, fights, camera target.

Limitations:

- Higher engineering complexity.
- UI-skin/resolution sensitive.
- Should be a fallback or augmentation layer, not the primary source.

## Field Feasibility Matrix

| Field | Steam Web API | Local GSI Spectator | Replay Parser | Notes |
|---|---:|---:|---:|---|
| Live match discovery | Good for leagues | No | No | API first |
| Team/player/hero ids | Good | Good | Good | API/GSI enough |
| Hero positions | No | Good (`xpos`,`ypos`) | Excellent | GSI likely primary |
| Current health/mana/status | No | Good | Excellent | GSI likely primary |
| Current gold/net worth/GPM/XPM | No or poor | Good | Good/excellent | GSI primary |
| Items/cooldowns/charges | Post-match only via details | Good | Excellent | GSI primary |
| Ability levels/cooldowns | No | Good | Excellent | GSI primary |
| Buildings health | Coarse tower bitmasks | Good | Excellent | GSI primary |
| Draft/picks/bans | Good | Good | Good | Both |
| Roshan state | No | Good | Good | GSI primary |
| Wards placed/destroyed counts | No | Good | Good | GSI has counts |
| Exact ward positions | No | Not confirmed | Likely possible post-match | Needs experiment/CV |
| Creeps, neutrals, summons | No | Not confirmed | Excellent | Replay or CV |
| Combat log | No | Not explicit | Excellent | Replay; maybe derive partially |
| Camera/spectator events | No | No | Possible in replay | Optional |

## Recommended First Prototype

1. Build a local GSI collector service on PaulPC4090.
2. Configure Dota 2 Linux client to POST all GSI categories to localhost at 10 Hz or lower.
3. Join an in-client DotaTV live match as spectator/observer.
4. Persist raw GSI snapshots as append-only JSONL/Parquet.
5. Build a field profiler that records actual keys, null rates, update cadence, and per-field delay.
6. In parallel, use Steam Web API key to call `GetLiveLeagueGames` and map match/lobby/team metadata.
7. After a test match ends, fetch/parse the replay and compare GSI-derived metrics against replay truth.

## Early Design Direction

Use a layered ingestion model:

- Layer A: Steam Web API for discovery and static metadata.
- Layer B: local GSI for live telemetry.
- Layer C: replay parser for truth/backfill/training.
- Layer D: optional CV for UI-only facts such as visible ward icons or minimap state if GSI lacks them.
- Layer E: unofficial Game Coordinator only after A-C are proven insufficient.

## Sources

- Steamworks Web API overview and key requirements: https://partner.steamgames.com/doc/webapi and https://partner.steamgames.com/doc/webapi_overview/auth
- Better Steam Web API Documentation endpoint list: https://steamwebapi.azurewebsites.net/
- Dota 2 GSI Node server documentation: https://github.com/xzion/dota2-gsi
- Steamworks Game Coordinator API: https://partner.steamgames.com/doc/api/ISteamGameCoordinator
- TF2 Wiki Dota 2 WebAPI pages: https://wiki.teamfortress.com/wiki/WebAPI/GetLiveLeagueGames and https://wiki.teamfortress.com/wiki/WebAPI/GetMatchDetails
- Clarity replay parser: https://github.com/skadistats/clarity
- OpenDota parser: https://github.com/odota/parser
