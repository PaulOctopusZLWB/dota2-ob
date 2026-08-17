# TI 2026 replay phase state machine

Date: 2026-08-17

Issue: DOT-72

## Decision

Use exactly one event-driven `global_phase`, with independent descriptive `team_shape` labels per team and mutable `lane_segment`/behavior episodes per player. The only official phase labels are `laning`, `midgame`, and `decisive`. Fixed time cuts are not emitted as a parallel report dimension or scoring input. `reset` is an evidence episode and transition reason, not a fourth phase.

The accepted replay adapter cannot run this state machine today because it lacks a calibrated game clock, participant bindings, positions, entity state, Roshan/Aegis state, death timers, and buyback availability. Until those field gates pass, the event-driven phase is `unavailable`, not inferred from current aggregate facts.

## Required input contract

All inputs are nullable and carry source-event identity:

| Family | Minimum fields | Cadence/order |
|---|---|---|
| Clock | game second, pause intervals, game state | monotonic game-time basis; pauses do not advance windows |
| Participant | match ID, slot, team, person, hero | complete one-to-one ten-player binding |
| Position | hero `(x,y)`, alive, teleport/displacement marker | target >= 2 Hz after deterministic resampling |
| Map geometry | patch/build, lane polygons, river, tower/high-ground/Roshan polygons | content-addressed and build-versioned |
| Economy | gold/net worth/last-hit/deny deltas, item inventory | ordered snapshots/deltas |
| Combat | damage, heal, death, ability/item, modifier, attacker/target | exact event time and resolved actor where required |
| Objective | tower/rax/ancient/Roshan state, Aegis ownership/drop/expiry | ordered transitions |
| Round resources | alive, respawn seconds, buyback available/cooldown/cost/use | per participant and event time |

Missing required inputs reduce confidence or suppress the corresponding label. No last-known value is carried across an unbounded gap.

## Clock calibration gate

1. Locate replay game-state transitions and the first unambiguous `game_time == 0` anchor.
2. Build a piecewise mapping from replay tick/combat timestamp to game seconds, excluding pauses.
3. Cross-check match duration against independent public metadata within `max(2 seconds, 0.1%)`.
4. Check at least ten visible events across early/mid/late replay with absolute alignment error <= 2 seconds.
5. Fail closed on discontinuity, negative duration outside pregame, or ambiguous anchor.

`MaxCombatLogTimestampSec` is never used as match duration or directly subtracted by a hard-coded constant.

## Lane assignment and lane segments

### Lane geometry

Version one uses patch-specific polygons for radiant/dire safe lane, mid lane, off lane, river, each base, and neutral regions. Geometry is content-addressed. A point within 1,200 world units of a lane polygon is a lane candidate; overlapping candidates resolve by nearest path distance, then explicit priority `mid -> safe/off by team orientation -> unknown`.

### Initial assignment

For game seconds `[0, 180)`, compute each player's alive, non-teleport sample share in each lane. The initial lane is the highest share when:

- share >= 0.55;
- the margin over the second lane >= 0.15; and
- at least 90 seconds of valid samples exist.

Otherwise the assignment is `unknown`. A 2-1-2/1-1-3 shape is an observed occupancy description, not a forced template.

### Segment change

A new lane segment begins when the candidate lane differs from the current lane for 30 continuous game seconds and contains at least 20 valid samples. Teleports/displacements start a 5-second grace window. A return shorter than 20 seconds is merged into the surrounding segment.

Role labels do not drive lane assignment. The resulting lane history is later joined with the source-backed nominal roster 1-5 facet and behavior episodes. Replay behavior never changes the nominal role.

## Team shape labels

At each second, construct connected components among alive allied heroes with an undirected edge at distance <= 1,800 units. Debounce for 15 seconds.

Priority-ordered labels:

1. `siege`: at least three allied heroes within 3,000 units of a live enemy tier-3/rax and recent building damage in 20 seconds.
2. `defense`: at least three allied heroes within 3,000 units of their threatened tier-3/rax and enemy siege evidence exists.
3. `grouped_objective`: at least three allied heroes within 2,500 units of Roshan, Tormentor, or a live enemy tower with objective damage/activity.
4. `lane_structure`: at least four heroes have stable lane assignments, with no component of three or more away from lane polygons.
5. `split_map`: no component has four or more heroes and at least two cores occupy distinct lane/neutral regions.
6. `disengage`: a prior siege/fight component separates, average distance from the conflict centroid increases for 10 seconds, and no new combat event occurs.
7. `unknown`: insufficient positions or conflicting evidence.

The label is descriptive. It does not assert that the shape was strategically correct.

## Global state machine

States:

```text
pregame -> laning -> midgame <-> decisive
                         ^          |
                         +--reset---+
all non-terminal states -> ended
```

`decisive` may repeat any number of times. A `reset` episode is emitted when the engine confirms the transition back to `midgame`; it is not a `global_phase` value.

### `pregame -> laning`

Enter `laning` at game second 0 only when all ten participant/hero bindings are verified. Missing bindings make the whole phase stream unavailable; they do not shrink the match to observed players.

### Lane-structure score

Once per second for each team:

```text
stable_lane_share = alive valid samples in current lane over trailing 120 s
support_departure = position 4/5 outside current lane by >2,500 units for >=45 s
cross_lane_core = position 1/2/3 begins a different stable lane segment
grouped_transition = component of >=3 away from base for >=20 s
objective_transition = >=3 heroes pressure a tower/Roshan/Tormentor for >=20 s

lane_structure_intact =
  stable_lane_share >= 0.60 for >=4 players
  AND fewer than 2 of the four transition signals are active
```

Position numbers above are source-backed nominal roster facets. Observed task differences are retained as behavior evidence but never used to relabel the role.

### `laning -> midgame`

Transition at the first second where either rule holds:

- both teams have `lane_structure_intact == false` for 90 of the trailing 120 seconds; or
- one team has it false for 90/120 seconds and an irreversible structural event occurs: a real tier-1 tower dies, Roshan is contested by >=3 heroes, or two lane segments change across teams.

Do not trigger on temporary summoned structures, a single support rune trip, one teleport, a single kill, or clock time alone.

If the match ends before the rule, it remains a fast-ending `laning` match. A fast push normally triggers through sustained grouping/objective pressure, not through a clock threshold.

### Decisive-round evidence

Compute only from verified inputs:

- `unavailable_player_seconds_60`: sum over dead heroes of `min(respawn_remaining, 60)`, excluding a hero once it buys back;
- `buyback_commit_120`: count of buybacks actually used in trailing 120 seconds;
- `buyback_locked`: dead heroes whose verified buyback is unavailable;
- `aegis_pressure`: Aegis carrier plus >=2 allies within 4,000 units of enemy tier-3/rax, or an active Roshan contest with >=3 heroes from each side;
- `highground_threat`: siege/defense team-shape pair plus real tier-3/rax damage;
- `major_fight`: >=6 distinct heroes deal/receive hero combat effects within 20 seconds and at least two deaths occur within 30 seconds;
- `barracks_change`: a real melee/ranged barracks health transition or death.

### `midgame -> decisive`

Enter when any one high-specificity rule holds:

1. `highground_threat` and (`buyback_commit_120 >= 1` or `unavailable_player_seconds_60 >= 60`);
2. `major_fight` and (`buyback_commit_120 >= 2` or `buyback_locked >= 2` or `aegis_pressure`);
3. `barracks_change`;
4. Aegis pressure plus enemy high-ground entry lasting >=15 seconds.

These are candidate thresholds. They remain `modelled` until the gold-set release gate passes.

### `decisive -> midgame` with reset evidence

Confirm a reset after 30 seconds without major fight or high-ground damage when all are true:

- each team has at least four alive heroes, or the disadvantaged team has begun stable respawns/buybacks;
- neither team has a siege shape;
- opposing main components are separating or one team is in its own base;
- no barracks/Roshan transition is active.

The official phase changes to `midgame` at the last decisive event plus the 30-second debounce once these conditions are confirmed. Emit a separate `reset` behavior episode beginning at that boundary.

The reset episode ends when either a new decisive entry occurs or 90 seconds pass with both teams outside siege/defense and ordinary map-resource exchange resumed.

### `midgame -> decisive` after a reset

Re-enter immediately when any decisive entry rule holds. End the active reset episode and increment `round_index`.

### `* -> ended`

Enter on verified game-end/ancient event. If game-end is missing, the phase stream is incomplete and carries an explicit right-censor reason.

## Conflict resolution

1. Direct game-end/objective/entity facts outrank modelled spatial labels.
2. A phase transition can use only evidence at or before that game second. No look-ahead is allowed in live-translatable variants.
3. Concurrent decisive evidence from both teams produces one global `decisive` interval with team-specific `attack`, `defend`, or `contested` posture.
4. A player event at the exact transition second belongs to the new phase; intervals are left-closed/right-open.
5. Missing position coverage >10 consecutive seconds makes spatial features unknown for that gap. It does not preserve the prior value.

## Output contract

Each phase interval includes:

```json
{
  "start_game_second": 0,
  "end_game_second": 600,
  "global_phase": "laning",
  "round_index": 0,
  "team_shapes": { "radiant": "lane_structure", "dire": "lane_structure" },
  "confidence": 0.0,
  "evidence_event_ids": [],
  "rule_version": "ti2026.phase.v1",
  "missing_inputs": []
}
```

Intervals split only at official phase boundaries or end-of-match. Team-shape and behavior-episode boundaries remain separate evidence streams.

## Required test scenarios

1. Normal 2-1-2 lanes, stable until a tower/rotation transition.
2. Level-one lane swap that stabilizes: still `laning`; lane segments reflect the swap.
3. Repeated support rune trips: no false lane end.
4. Early 1-1-3/trilane: valid lane structure, not automatically midgame.
5. Fast five-player push: sustained structure/objective evidence may cause an early midgame/decisive transition without a clock cut.
6. Short stomp ending before the lane-end rule: no fabricated midgame.
7. Long game with multiple Roshan/Aegis and buyback rounds: repeated decisive intervals separated by reset episodes and midgame intervals.
8. High-ground poke without death/buyback: no decisive round unless sustained threat rule passes.
9. Buybacks in a river fight without high-ground pressure: only the major-fight rule may trigger.
10. Underlord portals/summoned structures: never count as tower/rax transitions.
11. Pause/reconnect and missing position gaps: clock/windows remain correct and confidence degrades.
12. Clock offset and discontinuity: fail closed rather than shifting every phase.

## Validation metrics

- Lane segment: per-second macro F1 and boundary absolute error.
- Global phase: per-second macro F1, transition precision/recall, median and 90th-percentile boundary error.
- Decisive rounds: round intersection-over-union, false round count, missed re-entry count.
- Team shape: per-second macro F1 by label.
- Inter-annotator agreement: Cohen's kappa >= 0.70 before treating the gold label as stable; disagreements are adjudicated and retained.

No threshold is tuned on the same five games used for the final probe report without labelling that result exploratory.
