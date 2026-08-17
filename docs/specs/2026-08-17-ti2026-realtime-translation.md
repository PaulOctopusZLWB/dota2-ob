# TI 2026 historical-to-realtime translation matrix

Date: 2026-08-17

Issue: DOT-72

## Boundary

Historical replay facts are for post-match validation, training, and editorial review. They never backfill hidden/off-camera facts into a live match. A realtime metric uses only:

- fields present in the current local GSI snapshot/history;
- deterministic deltas over those observed fields; or
- facts visibly rendered by the delayed spectator UI and captured by an explicitly labelled CV adapter.

All realtime output inherits the DotaTV/client delay. The system reports receive time, source game time, freshness, missingness, and confidence; it does not estimate or bypass the undelayed game state.

## Current GSI evidence

Accepted DOT-19 evidence:

- 922 snapshots in 1,098.182 seconds (`0.840 Hz`), not the configured maximum;
- 920 complete ten-player frames;
- 3,701 deterministic derived events;
- observed Roshan, Tormentor, building-destruction, and ward-related changes;
- exact ward coordinates absent;
- raw privacy fields include player name, Steam ID, account ID, and team name.

Accepted earlier evidence observed ten-player positions and economy in 314 of 326 snapshots. `internal/analytics/tick.go` supports nullable clock, player identity, position, economy, K/D/A, health/mana/alive/respawn, buyback, status, smoke, ward counters, items, abilities, buildings, Roshan, and Tormentor fields when present.

This is a nullable observation contract, not a promise that every field exists in every match/build.

## Translation classes

- `gsi_direct`: current snapshot value, labelled as observation.
- `gsi_derived`: deterministic delta/window over observed GSI values.
- `cv_visible`: fact visible in the delayed spectator UI, with frame evidence.
- `replay_only`: post-match metric requiring facts not available live.
- `unsafe_or_unavailable`: hidden state, delay bypass, unapproved source, or unsupported counterfactual.

## Matrix

| Historical family | GSI direct | GSI-derived approximation | CV-visible fallback | Realtime disposition |
|---|---|---|---|---|
| Match/player/team/hero identity | match/player/hero/team fields when present | stable slot binding with reconnect checks | scoreboard labels | `gsi_direct`; suppress on incomplete ten-player binding |
| Game clock and causal phase timing | clock/game time, pause, state | calibrated causal windows and debounce | visible clock | `gsi_direct` with receive/source time; no fixed phase label |
| Event-driven global phase | positions, alive/respawn, buyback fields, buildings, Roshan, items when present | causal/no-look-ahead state machine | visible siege/fight/Roshan cues | `gsi_derived`; separately versioned for source/cadence, with the same three official states |
| Lane assignment/presence | `xpos`,`ypos` | versioned lane polygons and debounced segments | minimap | `gsi_derived`; confidence follows cadence/gaps |
| Roam movement | positions | lane departure + cross-lane arrival | minimap | movement is `gsi_derived`; intent/value remains modelled |
| Pull/stack/creep block/cut | no confirmed creep/order table | none | visible lane/camp units | `cv_visible` only when observable; otherwise `replay_only` |
| Gold/net worth/GPM/XPM | nullable player values | deltas, leads, opportunity windows | scoreboard/player panel | `gsi_direct`/`gsi_derived` |
| Last hits/denies | nullable counters | counter deltas | scoreboard | `gsi_direct`/`gsi_derived`; no creep identity |
| Safe/dangerous farm | positions/economy | map-region occupancy and observed enemy positions | minimap | `modelled`; never assert hidden enemy threat/FoW |
| Item inventory/timing | item slots/cooldowns/charges | first observed possession, cooldown deltas | player item panel | `gsi_direct` observation; completion time is interval-censored by cadence |
| Ability levels/cooldowns | ability slots | level/cooldown transitions | ability panel/cast animation | `gsi_direct` state; cast/hit semantics only if observed delta is unambiguous |
| K/D/A | nullable counters | increments | scoreboard | `gsi_direct`/`gsi_derived`; not full combat attribution |
| Health/mana/status | nullable hero fields | damage/heal/status intervals from deltas | health bars/status icons | approximate `gsi_derived`; exact source/target is `replay_only` |
| Damage/heal by source | no accepted event log | net deltas cannot safely attribute source | combat log/UI numbers if visible | `replay_only` by default |
| Control duration | stunned/silenced/etc. booleans when present | observed-duration lower/interval estimate | status bar/icons | `gsi_derived` approximate, labelled cadence-bounded; exact modifier source replay-only |
| Dispel/save cast | ability/item states but no guaranteed target event | none without unambiguous target/effect | visible cast/target | `cv_visible` or `replay_only`; life saved is counterfactual |
| Death/respawn | alive/respawn | transitions | scoreboard/UI | `gsi_direct`/`gsi_derived` |
| Buyback availability/use | cost/cooldown plus alive/respawn when present | use transition and resource-window context | buyback announcement | use can be `gsi_derived`; decision quality remains modelled |
| Building health/death | building health when present | damage/death transitions | map/UI | `gsi_direct`/`gsi_derived` |
| Roshan/Tormentor state | nullable state/end/location | state transitions and cycles | objective UI/pit | `gsi_direct`/`gsi_derived`; killer/drop owner may remain replay/CV-only |
| Aegis owner/expiry | item slot when visible in GSI | first/last observed ownership | scoreboard/item panel | interval-censored `gsi_derived`; suppress if slot missing |
| Ward counters | placed/destroyed/purchased and purchase cooldown when present | counter transitions | scoreboard | `gsi_direct` counts; no coordinates/coverage |
| Ward coordinates/lifetime | not observed | impossible from counters | visible ward/minimap icon | `cv_visible` only for visible facts; otherwise replay-only after parser proof |
| Theoretical ward coverage | no coordinates | none from counters | CV coordinate + versioned geometry | `modelled`; always labelled theoretical |
| Actual enemy visibility/decision value | no FoW table | not safely derivable | only currently rendered enemy visibility | `unsafe_or_unavailable` as complete truth |
| Smoke state | nullable `smoked` when present | start/end membership intervals | smoke icon/visual | `gsi_derived` membership; exact break cause/person may need CV/replay |
| Smoke outcome | positions/KDA/buildings/economy | fixed post-end 30/60/120-second observed outcomes | visible contact | outcomes can be `gsi_derived`; beneficial sacrifice is counterfactual |
| Teamfight window | positions, status, KDA/health deltas | modelled multi-player contact window | visible fight | `modelled`; exact combat ledger replay-only |
| Initiation/counter-initiation | status/position/cooldown deltas | low-confidence causal order | visible casts/contact | `modelled`/`cv_visible`; no fact claim without source event |
| Nominal role and behavior episodes | source-backed role registry; observed position/economy/items/abilities | fixed nominal role plus separately named behavior episodes | analyst annotation | nominal role is registry-backed; behavior may be `derived`/`modelled` but never reclassifies it |
| Counterfactual save/sacrifice/buyback value | none | none | none | `unsafe_or_unavailable` for live factual output |

## Causal phase variants

The official offline replay phase and live/GSI phase are both causal: `global_phase(t)` may use only evidence whose game time is at or before `t`. Neither may use future smoothing or emit a fixed-time fallback. The live/GSI variant must:

- use only snapshots already received;
- retain the same state names but a separate rule version;
- expose provisional transitions and confidence;
- never rewrite a prior broadcast statement silently;
- emit `unavailable` with missing-evidence reasons when the event-driven inputs do not meet their gate.

Offline and live rule versions may differ only because their accepted fields, cadence, and missingness differ. Historical and live outputs are compared after the match, never mixed during it, and neither creates a second official phase stream.

## Cadence, delay, nullability, and confidence

Every live field/metric record includes:

```text
source = gsi | cv
source_game_time
received_at
tick/frame identity
observed_fields/evidence reference
cadence_window and largest gap
expected_delay_basis = dota_client_spectator
null/missing reason
confidence and rule/model version
```

Confidence ceilings:

- direct present GSI value: high for “client reported X,” not for undelayed world truth;
- delta with both endpoints present and gap <= 2 expected cadences: high/medium by field;
- interval-censored first/last possession or status: medium at best;
- CV fact: capped by detector calibration and visibility/occlusion;
- any metric depending on an absent field: unavailable, not low confidence.

## Vision and hidden-information rules

1. Exact ward coordinates remain unavailable from GSI unless a first-class field is empirically observed in a future accepted session.
2. A CV detector may report only a ward/icon visible in a captured delayed frame. It cannot extrapolate unseen wards.
3. Player positions present in spectator GSI are used only as received from the delayed spectator client. They do not authorize reconstruction or output of an undelayed/fog-bypassing state.
4. Replay-only enemy paths, wards, inventories, or intentions never enter a live match's state.
5. A theoretical vision polygon is geometry, not proof that an enemy was visible or that a decision used the vision.

## Release gate by class

- `gsi_direct`: field observed in a real session, profiled for null rate/cadence, and covered by normalization tests.
- `gsi_derived`: deterministic rule passes replay-aligned post-match validation and missing-gap adversarial tests.
- `cv_visible`: detector passes resolution/UI/occlusion tests and stores frame evidence; no hidden extrapolation.
- `replay_only`: absent from all live surfaces used by this project; available only after verified replay parsing.
- `unsafe_or_unavailable`: excluded until Paul explicitly authorizes a separate scope, if authorization could make it safe at all.

## Verification plan

For each candidate metric, capture a complete GSI session and its verified replay, align clocks, and produce a record-level comparison:

- GSI evidence reference and observed value;
- replay evidence reference and post-match value;
- alignment method/error;
- missingness/cadence/receive delay;
- match/mismatch reason;
- whether the realtime class changes.

Minimum live acceptance is three complete spectator sessions spanning normal, early-disruption, and long/multi-round games. This is separate from the five replay vertical-slice probes and may reuse a match only when both source identities are verified.
