# TI 2026 group-stage replay analysis research specification

Date: 2026-08-17

Issue: DOT-72

Status: research foundation revised by [the complete implementation specification](2026-08-17-ti2026-replay-analytics-implementation.md). Where the two conflict, the implementation specification controls.

## Objective

Define an auditable path from the 109 played TI 2026 group-stage maps to:

- one terminal integrity record per expected game;
- replay-derived, opportunity-normalized player and team metrics;
- event-driven phase labels that survive lane swaps, fast pushes, long games, and repeated late-game rounds; and
- a field-by-field gate for any later translation to GSI or visible-screen CV.

The product publishes atomic facts and normalized rates before scoring. It also publishes a transparent role-relative total score whose full axis/metric decomposition remains visible. Modelled/counterfactual claims remain a separate experimental layer and never appear as facts or contaminate the official score.

## Context

TI 2026 group play finished on August 16, one day before this specification. DOT-22/DOT-54 proved that the earlier broad historical-acquisition path could not publish identity-verified replay facts under the unchanged source gate. Paul has since supplied a fresh local corpus containing 109 compressed replay archives, 109 decompressed Source 2 demos, and a 109-row `matches.json`. File presence and container integrity are established; replay-to-match identity, ten-player binding, clock calibration, and parser capability still require the explicit gates below.

## Requirements

1. Freeze the official group-stage denominator and preserve the distinction between series identity, game-slot identity, Dota match identity, and replay content identity.
2. Give every expected game one auditable state and prove coverage from records rather than estimates.
3. Define exactly one event-driven global phase stream using only `laning`, `midgame`, and `decisive`; team/lane/behavior evidence stays separate and fixed time cuts are not a parallel report dimension.
4. Define 1-5 role functions and metrics with numerator, opportunity denominator, windows, exclusions, inputs, algorithm, confidence, bias, and test case.
5. Classify every fact/metric against the accepted parser, not against library theory.
6. Fix a five-TI-replay vertical slice, lightweight Paul gold-review process, error taxonomy, and quantitative promotion gates.
7. Map historical metrics to GSI, visible CV, replay-only, or unavailable realtime classes without hidden-state leakage.
8. Implement the complete reviewed V1/V2/V3, radar, score, UI, and 109-match workflow defined by the implementation specification.

## Source of truth

### Tournament schedule source

- Official Perfect World endpoint: `https://www.dota2.com.cn/international2026/getMatchScheduleGrouped?schedule_key=ti2026&swiss_format=ti_road`
- Retrieved: `2026-08-17T03:02:31Z`
- Response-owned timestamp: `2026-08-17T03:02:28Z` (`1786935748`)
- Raw response SHA-256: `d6314c9beb2fb36956f5f042ddd679661dc27590ca0df9cdeae7fff4a914cfa6`
- Sanitized schedule-freeze SHA-256: `7f8da6084386d97b7f30139d8dbb802de091ed8eea805f9337fcf16a898fd366`
- Frozen group-stage denominator: 39 five-round Swiss series plus 5 elimination series, 44 completed series and 109 played maps.
- The endpoint's small integer `matchId` is an event-provider series identifier. It is not a Dota match ID and must never be placed in `dota_match_id`.
- Official format/date corroboration: `https://www.dota2.com.cn/article/details/20260526/220476.html` (August 13-16; five Swiss rounds over three days, then five elimination series).

The response also contains 14 future main-event series. They are outside this specification's group-stage denominator.

### Accepted replay evidence

- Replay acquisition/parser decision: commit `b5ee5abb41978317c19cf6c58ae6ce97c31d2cba` (DOT-29, PR #6).
- M1 terminal evidence: commit `77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b` (DOT-54, PR #12).
- Accepted scope outcome: commit `264fed3cdef4ef845b815581b4011a6d293ecbaa` (DOT-22/DOT-59).
- Parser: `github.com/dotabuff/manta` v1.5.0 behind the accepted off-main replay adapter.
- Retained DOT-54 evidence: 100 real demo files, 200 successful deterministic parser executions, evidence index `9d5f6b6e95941c2704f96f85e6e49f61679055648df60b49ccb5ff9c396b0dc4`.

### Accepted live evidence

- DOT-19 session `20260812T084305.387831321Z`, match `8941626802`.
- 922 accepted GSI snapshots, 920 complete ten-player frames, 0.840 snapshots/second.
- Observed objective evidence included Roshan, Tormentor, building, and ward-counter changes; exact ward coordinates were not exposed.
- The accepted live contract is `internal/analytics/tick.go` on `main`.

## Evidence ledger

### Verified facts

1. Repository `main` at the start of DOT-72 contains the GSI product but no `internal/replay` or `internal/history` package. Replay evidence remains on accepted off-main commits.
2. DOT-54 parsed 100 real demos twice, but all 100 normalized records remained quarantined. Public metadata did not independently correlate game build and all ten hero-to-participant bindings, so there were zero identity-verified historical facts.
3. The accepted adapter emits Source 2 magic/byte identity, game build, server name, last ticks, combat-log counts, partial name-resolved combat records, hero-level kill/death aggregates, item-use aggregates, purchase-gold aggregates, and a limited timeline.
4. It does not emit a replay-derived Dota match ID, a calibrated game clock, ten verified participant bindings, positions, inventories over time, entity orders, wards, Roshan/Aegis state, modifiers, ability targets, damage/heal detail, last hits/denies, net worth, death timers, or buyback availability.
5. Combat-log actor name resolution is partial. Accepted evidence includes `dota_unknown` attackers for first blood, building kills, and buybacks, and `TEAM_BUILDING_KILL` entries for temporary units such as Underlord portals.
6. `MaxCombatLogTimestampSec` is explicitly not authoritative match duration. Phase logic may not use it until a replay-clock calibration is independently proven.
7. The official TI schedule endpoint proves 109 played group-stage maps but does not expose their Dota match IDs. The local `matches.json` supplies 109 candidate Dota match ID/salt pairs; each pair must still be mapped to one official game slot and verified against replay content before publication.

### Assumptions that require an experiment

1. A successor parser adapter can reconstruct stable per-tick hero/entity positions at an acceptable cost.
2. Replay messages contain enough unit ownership/order data to identify pulls, stacks, creep cuts, ward entities, Roshan/Aegis transitions, and modifier windows.
3. A corrupt or mismatched local replay can be reacquired through an approved public/client path with its URL, retrieval time, and hashes recorded. Future availability is unknown and cannot be assumed.
4. Map geometry and vision rules can be versioned for the tournament build well enough to support lane and theoretical-vision models.

### Rejected assumptions

- A discovered series or replay URL proves archive completeness.
- A deterministic parse proves match identity.
- Item-use counters prove ownership duration, purchase timing, or meaningful activation.
- A ward placement plus a radius proves actual vision or decision value.
- A support death after smoke break proves a beneficial sacrifice.
- Fixed 8/10/30-minute cuts are the primary phase definition.

## Deliverables

- [Frozen group-stage schedule and 109 game-slot identities](ti2026-group-stage-schedule-freeze-v1.json)
- [Archive and integrity plan](2026-08-17-ti2026-replay-archive-integrity.md)
- [Replay manifest JSON Schema](ti2026-replay-manifest-v1.schema.json)
- [Event-driven phase state machine](2026-08-17-ti2026-phase-state-machine.md)
- [Role/phase metric dictionary](ti2026-role-phase-metrics-v1.json)
- [Eight-axis radar and total-score contract](ti2026-radar-scoring-v1.json)
- [Current parser field feasibility matrix](2026-08-17-ti2026-replay-field-feasibility.md)
- [Five-replay vertical-slice and gold-label plan](2026-08-17-ti2026-five-replay-probe-plan.md)
- [Complete implementation specification](2026-08-17-ti2026-replay-analytics-implementation.md)
- [Historical-to-realtime translation matrix](2026-08-17-ti2026-realtime-translation.md)

## Recommended reporting grain

Use one official phase plus separate descriptive evidence:

1. `global_phase` is the only formal report phase: `laning`, `midgame`, or `decisive`.
2. `team_shape` describes each team independently: lane structure, split map, grouped objective, siege, defense, disengage, or unknown.
3. `lane_segment` describes a player/lane assignment and may change repeatedly.

`reset` is a behavior episode and transition reason from `decisive` back to `midgame`, not a fourth phase. Fixed clock cuts are not emitted as formal labels or scoring inputs.

## 1-5 role x phase x function framework

The source-backed nominal roster position is the fixed role report facet. Each metric also records behavior episodes and lineup/hero context, but replay behavior never reclassifies the nominal role.

| Position | Laning | Midgame | Decisive / reset evidence |
|---|---|---|---|
| 1 | safe farm opportunities, lane survival, resource handoff | safe/dangerous farm mix, item timing, selective participation | survival, buyback, damage window, building conversion |
| 2 | lane pressure/recovery, rune access | first rotations, side-lane pressure, tempo item/skill windows | target access, spell sequence, reset and re-entry |
| 3 | deny/pressure, dangerous lane absorption | tower/area space, initiation posture, aura timing | formation, initiation/counter-initiation, high-ground task |
| 4 | partner reinforcement, pull/stack/rune/roam choices | smoke/vision/rotation efficiency, initiation or response | control, saves, smoke break evidence, buyback resource use |
| 5 | core protection, lane equilibrium, supply/vision | vision plan, smoke organization, resource sacrifice | positioning, saves, vision renewal, buyback and reset management |

Temporary farm, initiation, support, or lane duties are represented as behavior episodes. A nominal position 4 remains position 4 for role-relative reporting unless a source-backed match override is recorded.

## Metric publication rules

Each metric in the dictionary has two labels:

- `current_class`: what can be defended with the accepted parser today; and
- `target_class`: its intended epistemic class after the named field gate passes.

Allowed classes are `direct`, `derived`, `modelled`, `counterfactual`, and `unavailable`.

Publication rules:

1. `direct` must retain source event identity, parser/schema version, clock basis, nullability, and participant binding.
2. `derived` must be deterministic from versioned inputs and rules. Re-running identical inputs must produce identical output.
3. `modelled` must emit a confidence score, feature evidence, model/rule version, and calibration result. A missing opportunity denominator suppresses the metric.
4. `counterfactual` is excluded from default player/team reports. It may appear only in a research appendix with explicit alternatives and uncertainty.
5. `unavailable` stays null with a reason code. It is never zero.
6. Every rate reports numerator, denominator/opportunity count, excluded opportunities, sample size, and confidence class.
7. A role-relative total score is allowed only through the versioned eight-axis scoring contract. It must expose every axis, metric, weight, opportunity, sample, confidence, and unavailable component. Official scores contain no V3 inputs.

## Confidence and release gates

| Class | Probe gate | Corpus release gate |
|---|---|---|
| Direct event | deterministic A/B bytes; identity and clock verified | precision and recall >= 0.98 on visible gold-set events; zero silent schema fallback |
| Derived event/rate | deterministic; all inputs present; edge-case tests | precision >= 0.90 and recall >= 0.85, or boundary median absolute error <= 30 s with 90% <= 60 s |
| Modelled | evidence trace and calibrated confidence | precision >= 0.85, recall >= 0.70, expected calibration error <= 0.10; minimum 20 structurally diverse games |
| Counterfactual | research-only qualitative review | never default-published under DOT-72 |

Five games are a feasibility probe and initial gold set, not a model release corpus.

## Decisions confirmed by Paul

1. One official event-driven phase stream; no fixed-time parallel phase.
2. Source-backed nominal role 1-5 with explicit match override; no behavioral role classifier.
3. Five local TI replay probes and lightweight Paul review.
4. Eight-axis radar plus a transparent role-relative total score.
5. V3 appears as a separate dashed experimental layer and never enters the official score.
6. One complete implementation covering V1/V2/V3, UI, gold workflow, and all 109 matches.

## Non-goals

- This research document does not itself implement code; implementation is authorized and governed by the linked complete implementation specification and child issues.
- No credentials, GC login/automation, Dota UI automation, replay-salt automation, packet capture, memory access, code injection, protocol bypass, anti-cheat bypass, hidden-state live output, or DotaTV delay/fog bypass.
- No causal or counterfactual truth claim.
- No claim that local file presence alone makes all 109 replays identity-verified or analytically publishable.

## Acceptance criteria for a later implementation

1. The official 109-map denominator is materialized as 109 unique game slots, and every slot reaches one allowed terminal state.
2. Every `verified` replay passes content, Source 2, match/build/time, team, and ten-participant checks; failures are quarantined.
3. The phase state machine passes lane-swap, fast-push, long-game, and decisive exit/re-entry fixtures while emitting only the three official phase labels.
4. Every published metric satisfies its dictionary definition and release gate with no fabricated denominator.
5. Vision, smoke-break, save, and sacrifice claims remain downgraded when FoW or counterfactual evidence is missing.
6. Realtime mappings expose only facts observed through GSI or visible CV and retain source delay/confidence.

## Verification for this specification package

Run from the repository root:

```sh
jq empty docs/specs/ti2026-replay-manifest-v1.schema.json
jq empty docs/specs/ti2026-role-phase-metrics-v1.json
jq empty docs/specs/ti2026-group-stage-schedule-freeze-v1.json
jq -e '.metrics | length >= 25' docs/specs/ti2026-role-phase-metrics-v1.json
jq -e 'all(.metrics[]; has("numerator") and has("denominator") and has("raw_inputs") and has("known_biases") and has("test_case"))' docs/specs/ti2026-role-phase-metrics-v1.json
jq -e '.series_count == 44 and .expected_game_count == 109 and .frozen_dota_match_id_count == 0 and ([.series[].game_slot_ids[]] | length) == 109 and ([.series[].game_slot_ids[]] | unique | length) == 109' docs/specs/ti2026-group-stage-schedule-freeze-v1.json
git diff --check
```

## Review focus

- Check that accepted DOT-29/DOT-54 limitations are not upgraded from evidence to capability.
- Check that the 109-map denominator is not confused with 44 series or with provider series IDs.
- Check every metric's opportunity denominator, exclusions, field gate, and epistemic class.
- Check that state transitions can exit and re-enter decisive rounds.
- Check the live mapping for hidden-state or delay-bypass leakage.
