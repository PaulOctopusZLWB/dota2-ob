# TI 2026 replay field and metric feasibility matrix

Date: 2026-08-17

Issue: DOT-72

## Scope and conclusion

This matrix describes the accepted adapter at commit `77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b`, not manta's theoretical protocol surface.

Conclusion: current replay parsing is sufficient for byte/magic checks, parse-cost evidence, a match-header subset, combat-log histograms, and several hero-name aggregates. It is not sufficient for identity-verified player/team analysis, phase segmentation, position/economy/vision/smoke/fight attribution, or opportunity-normalized role metrics.

All 100 DOT-54 real demos parsed twice, but all 100 were quarantined. Therefore no current per-player metric may be published from that corpus.

## Current raw and normalized fields

| Field/event | Accepted evidence | Current class | Important limitation |
|---|---|---|---|
| Compressed/decompressed SHA-256 and bytes | Acquisition/batch sidecars | `direct` | Identity, not authenticity. |
| Source 2 magic | `PBDEMS2\x00` pre-parse check | `direct` | Does not identify the match. |
| Parser/provenance versions | Pinned in facts | `direct` | Accepted replay code is off `main`. |
| Game build | `p.GameBuild` | `direct` | Public metadata did not independently supply a matching build in DOT-54. |
| Server name | demo header | `direct` | Not a stable match identity. |
| Last tick/net tick | parser counters | `direct` | No verified tick-to-game-clock mapping. |
| Max combat timestamp | maximum combat event timestamp | `direct` | Explicitly not authoritative duration. |
| Message counts | selected parser callbacks | `direct` | Count only, not retained message payloads. |
| Combat-log type counts | all received combat entries grouped by type | `direct` | Detail is discarded for most types. |
| Combat event timestamp/type | collected internally | `direct` | Only selected types survive normalized timeline. Clock is uncalibrated. |
| Attacker/target/inflictor names | CombatLogNames lookup | `direct` when resolved | Partial; accepted real data contains `dota_unknown`. |
| Hero kills/deaths | combat-log aggregate by hero name | `direct` at hero-name level | No verified player binding; unknown actors undercount/misattribute. |
| Item uses | aggregate by hero/item name | `direct` at hero-name level | No timestamps in output; activations such as boots can be extremely frequent; not ownership or purchase proof. |
| Purchase events/gold | aggregate by hero | `direct` at hero-name level | Named purchases and timestamps are absent. |
| Buyback events | selected timeline event | `direct` event existence/time | Actor may be `dota_unknown`; no availability/cost/decision context. |
| Building-kill target/time | selected timeline event | `direct` event existence/time | Attacker may be unknown; temporary units may share the event type. |
| First blood | selected timeline event | `direct` event existence/time | Attacker/target may be unknown; clock uncalibrated. |
| Multikill/killstreak | selected timeline events | `direct` where actor resolves | Not a complete fight record. |
| Normalized participant facts | kills/deaths only after full identity gate | `unavailable` in DOT-54 | Zero of 100 passed build + ten-binding identity. |
| Match metadata | external match ID/time/patch/team/duration | external `direct` | Not derived from current replay bytes. |

## Current missing fields

`unavailable` below means unavailable from the accepted adapter, not proven absent from every Source 2 replay.

| Required family | Current class | Gate required before use |
|---|---|---|
| Parsed Dota match ID | `unavailable` | Extract from a verified replay message/header and cross-check fixtures. |
| Ten player IDs/slots/teams/heroes | `unavailable` | Reconstruct complete binding and pass adversarial duplicate/mismatch tests. |
| Calibrated game clock and pauses | `unavailable` | Piecewise tick/event-to-game-time mapping with <=2 s validation error. |
| Hero/entity positions over time | `unavailable` | Versioned entity reconstruction, ownership, resampling, gap semantics. |
| Entity orders/targets | `unavailable` | Preserve unit order events with actor and clock identity. |
| Inventory snapshots and item timings | `unavailable` | Reconstruct inventory ownership and purchase/combine/drop/transfer lifecycle. |
| Gold/XP/net-worth timeline | `unavailable` | Per-player entity state or correctly resolved ledger; no carry-forward over gaps. |
| Last hits/denies/creep identity | `unavailable` | Resolve player, unit class, lane/neutral context, denies. |
| Ability casts/targets/hits | `unavailable` | Retain ability events, targets, disjoint cast/hit semantics. |
| Damage/heal values and actors | `unavailable` | Retain detail with illusions/summons/owner attribution rules. |
| Modifiers/control duration/dispels | `unavailable` | Reconstruct add/remove/stack lifecycle and overlapping sources. |
| Death timers and buyback availability | `unavailable` | Entity/player state plus verified use/cooldown/cost. |
| Ward entities/coordinates/team/lifetime | `unavailable` | Entity class/owner/team/position/spawn/death proof. |
| Smoke activation/break membership | `unavailable` | Item/modifier lifecycle, affected heroes, break cause/time/location. |
| Roshan state, death, drops, Aegis owner | `unavailable` | Resolve Roshan entity/objective and item transfer/expiry. |
| Tower/rax health state | `unavailable` | Reconstruct real building entities and exclude temporary structures. |
| True fog-of-war/visibility state | `unavailable` | No approved claim; theoretical geometry is not actual visibility. |
| Camera intent/team communications | `unavailable` | Not inferred. |

## Priority metric-family verdicts

| Family | Current conclusion | Target class if gate passes | Evidence needed |
|---|---|---|---|
| Phase state machine | `unavailable` | `modelled` | Clock, bindings, positions, buildings, Roshan/Aegis, death/buyback state; three-game gold timeline. |
| Lane reinforcement/pressure | `unavailable` | `modelled` | Lane segments, damage/heal, creep economy, retreat/regen evidence, opportunity denominator. |
| Roaming | `unavailable` | `derived` for movement; `modelled` for intent/value | Positions, initial lane, cross-lane arrival, fight/objective windows, missed lane resources. |
| Pull/stack/creep block/cut | `unavailable` | `derived` | Creep ownership/class/position/order and camp/lane geometry. |
| Safe core farm windows | `unavailable` | `modelled` | Creeps, positions, enemy threat/visibility assumptions, last-hit opportunity. |
| Item timing | `unavailable` | `derived` | Inventory/purchase lifecycle and player identity. Current purchase-gold aggregate is insufficient. |
| Ward placement count | `unavailable` per player | `direct` | Ward entity spawn and owner binding. Item-use aggregate is insufficient. |
| Ward lifetime | `unavailable` | `derived` | Ward spawn/death/expiry and team identity. |
| Theoretical ward coverage | `unavailable` | `modelled` | Ward coordinates plus versioned height/tree/vision geometry. Must be labelled theoretical. |
| Actual visible enemy track | `unavailable` | `modelled` | Visibility/FoW reconstruction; positions alone do not prove visibility. |
| Deward opportunity | `unavailable` | `modelled` | Enemy ward truth, allied detection/reveal/position, attack opportunity. |
| Smoke initiation | `unavailable` | `derived` | Smoke use timestamp, modifier membership, movement, encounter/objective result. |
| Smoke break person/location | `unavailable` | `derived` if modifier break is explicit; otherwise `modelled` | Modifier removal/break reason, positions, enemy proximity. |
| Beneficial smoke sacrifice | `unavailable` | `counterfactual` | Alternative-outcome model; excluded from default reports. |
| Fight damage/heal | `unavailable` | `direct`/`derived` | Detailed events and owner attribution; current adapter keeps only type counts. |
| Control duration | `unavailable` | `derived` | Modifier lifecycle, status resistance/overlap, source ownership. |
| Dispel/save cast | `unavailable` | `direct` | Ability/item target and effect event. |
| Death prevented by save | `unavailable` | `counterfactual` | Never reduced to a direct fact. |
| Initiation/counter-initiation | `unavailable` | `modelled` | Cast/control/damage/death sequence plus positions and calibrated confidence. |
| Buyback use | `direct` event existence only | `derived` per player after binding | Actor binding, availability/cost, phase, subsequent actions. |
| Buyback decision quality | `unavailable` | `modelled` | Opportunity set and round outcome; no causal claim. |
| Tower/rax conversion | `direct` target/time at low attribution confidence | `derived` after entity gate | Exclude temporary structures and resolve team/attacker/preceding fight. |
| Roshan/Aegis cycle | `unavailable` | `direct`/`derived` | Objective and item ownership lifecycle. |
| Dynamic 1-5 responsibility | `unavailable` | `modelled` | Positions, farm allocation, item/ability task, lineup context, confidence. |

## Observed anomaly examples

These are reasons to keep raw evidence and exclusions explicit:

- In retained match `8741845584`, the normalized timeline includes multiple `building_kill` events targeting `npc_dota_unit_underlord_portal`. A naive building count would inflate structural conversion.
- The same facts contain buyback events whose attacker is `dota_unknown`.
- Item-use aggregates include hundreds of `item_power_treads` activations for a hero. Raw activation count is not a meaningful item-effectiveness metric.
- Hero aggregate kills and deaths do not always balance because actor/target resolution is incomplete and the combat-log death family includes non-hero deaths.

## Required parser spike before metric implementation

The next replay implementation must be a field spike, not a full metric engine. For each of the three fixed probe demos it must emit:

1. clock calibration evidence;
2. complete ten-player binding;
3. raw event/entity coverage counts and null rates;
4. positions at a documented cadence with gap statistics;
5. detailed samples for items, abilities, damage/heal, modifiers, wards, smoke, Roshan/Aegis, buildings, creeps, death timers, and buybacks;
6. A/B deterministic fact hashes;
7. wall/user/system CPU, peak RSS, input bytes, output bytes; and
8. one row in this matrix changed only when the emitted evidence and a fixture test justify it.

An adapter callback existing in library code is not evidence that a field is complete or correct.

## Storage/cost baseline

DOT-54 measured:

- 100 selected replays;
- 4,734,122,001 compressed bytes;
- 7,730,318,570 decompressed bytes;
- manta A/B elapsed 109.17 / 99.47 seconds;
- A/B peak RSS 108,472 / 116,440 KiB;
- 100 succeeded in each run and zero reparses on resume.

This demonstrates affordable parse throughput for a 109-game TI corpus at the current shallow fact schema. Rich entity/tick extraction must be measured again; its output size and memory cannot be inferred from the 15-18 KiB shallow facts.
