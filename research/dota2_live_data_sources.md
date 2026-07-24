# Dota 2 Live Spectator Analytics: Data Source Survey

Date: 2026-07-05

## Executive Summary

The most promising first architecture is a local Linux Dota 2 spectator client plus Game State Integration (GSI). In spectator/observer mode, community-documented GSI output can expose ten-player data for hero position, health/mana, net worth, gold, GPM/XPM, items, ability cooldowns/levels, draft, map state, Roshan state, ward purchase cooldowns, and buildings.

Steam Web API alone is not enough for deep realtime analysis. `GetLiveLeagueGames` is useful for discovering live league matches and coarse match state, but it does not provide per-tick positions, inventory cooldowns, or full economic telemetry. `GetRealtimeStats` exists in public interface listings, but it requires a `server_steam_id`; its practical availability and field set need key-backed experiments.

Replay/demo parsing is excellent for post-match truth and model training, but not naturally live unless we can obtain a delayed/streaming demo source. It can still be used to validate GSI and build richer derived metrics.

## Source Classes

### 0. Dota Labs / Plugin API Check

Checked on 2026-07-24.

Valve Dota Labs is an in-client experimental feature bucket, not a documented external API or plugin platform. Official announcements describe it as disabled-by-default settings under the Dota Labs tab, with examples such as Overlay Map, Modifier Key Filter Bindings, High-Visibility Local Hero Healthbar, Dota Plus pre-match analytics, and later Labs UI/overlay-map updates. Recent 2026 patch notes still mention Dota Labs Overlay Map bug fixes, so the feature bucket still exists, but there is no evidence of a public Dota Labs API, webhook, SDK, plugin hook, or data export surface.

Usability for this project:

- Not a primary data source for live spectator analytics.
- Not a stable integration surface; features can graduate, change, or be retired.
- Possible indirect UI aid only: Overlay Map, Dynamic Health Bar Focus, and Persistent Range Indicators may improve human observation or CV target visibility, but they do not expose structured telemetry.
- Pre-Match Analytics is Dota Plus-gated and intentionally coarse; it is not a reliable source for match/player telemetry.

Related but separate plugin/API surfaces:

- Dota 2 Workshop Tools provide official addon/custom-game tooling and Lua/Panorama scripting APIs, but they apply to custom games/addons, not normal DotaTV/live public match observation. The tools are not a path to instrument normal matches.
- Overwolf has a Dota 2 Game Events Provider API and public Dota 2 apps such as DotaPlus. Its documented Dota 2 event surface includes game state, match state, clock time, ward purchase cooldown, kills/deaths/assists, gold/GPM/XPM, hero health/mana/status, abilities, items, roster, and damage. It requires Dota 2 `-gamestateintegration`, and therefore appears to wrap or depend on the same low-risk GSI channel we can consume directly. Overwolf Native is Windows-only, which does not fit the PaulPC4090 Linux MVP.

Recommendation:

- Do not build around Dota Labs.
- Keep Dota Labs as a manual/CV observation aid candidate only.
- Treat Overwolf's Dota 2 event list as useful field-discovery evidence, but implement our own Linux local GSI collector rather than depending on Overwolf.
- Use Workshop Tools only for custom-game experiments, not the live spectator product.

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
- Valve Dota Labs initial announcement: https://store.steampowered.com/news/posts/?appids=620%2C550%2C80822%2C240%2C80788%2C80762%2C80752%2C80747%2C80739%2C220%2C80633%2C80923%2C70%2C400%2C440%2C420%2C10%2C500%2C380%2C5739%2C300%2C4000%2C30%2C219%2C80%2C5952%2C410%2C360%2C340%2C320%2C280%2C130%2C40%2C60%2C50%2C20%2C5489%2C5268%2C630%2C570%2C5724%2C922%2C5260%2C5149%2C5150%2C997%2C987%2C5734%2C985%2C5073%2C5051%2C937%2C936%2C934%2C933%2C932%2C931%2C930%2C916%2C923%2C915%2C914%2C913%2C912%2C905%2C904%2C5032%2C960%2C5141%2C5139%2C5138%2C918%2C917%2C901%2C5825%2C5722%2C995%2C906%2C1003&enddate=1711476873&feed=steam_community_announcements
- Valve Dota Labs update: https://store.steampowered.com/news/posts/?appids=570&enddate=1717798601
- Valve Dota 2 7.36 patch notes: https://www.dota2.com/patches/7.36
- Dota 2 official announcements, 7.41d Dota Labs Overlay Map fix: https://steamcommunity.com/app/570/announcements/
- Valve Developer Community Dota 2 Workshop Tools: https://developer.valvesoftware.com/wiki/Dota_2_Workshop_Tools
- Overwolf Dota 2 Game Events Provider: https://dev.overwolf.com/ow-native/live-game-data-gep/supported-games/dota-2/
- Overwolf platform OS limitations: https://dev.overwolf.com/ow-native/guides/dev-tools/non-windows-dev/
