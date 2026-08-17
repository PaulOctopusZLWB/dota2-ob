# TI 2026 replay analytics complete implementation specification

Date: 2026-08-17

Issue: DOT-72

Status: product decisions approved; this document is the implementation source of truth after independent spec review.

## Objective

Build one local, reproducible TI 2026 replay analytics product that processes the frozen 109-map corpus and produces:

- one source-backed nominal position 1-5 for every participant;
- one official, auditable global phase label at every eligible game second;
- replay-backed player and team facts, behavior episodes, V1/V2 metrics, and separately labelled V3 model outputs;
- eight role-aware radar axes plus an explicit total score;
- a five-match vertical slice that Paul can inspect before the full-corpus run;
- a match explorer, player/team tournament dashboard, and gold-label review workflow; and
- a fully resumable 109-match batch with provenance, quality, performance, and independent-review evidence.

The implementation is one coherent system and one final delivery. V1, V2, and V3 are capability and publication levels inside that system, not separate throwaway products.

## Context

The local directory `/home/paul-zhang/文档/ti15_rep` contains 109 `.dem.bz2` replay archives, 109 decompressed `.dem` files, `matches.json`, and `urls.txt`. The corpus is available now; acquisition failure is not the primary product risk. Bad or missing bytes may be recovered again through approved public paths.

The research package in PR #20 established useful archive, parser-field, metric-contract, and source-boundary material. It was written before the local TI corpus and later product decisions were known. This specification supersedes these conflicting parts of that package:

- fixed clock cuts are not a second report dimension or formal phase input;
- `reset` is an evidence episode and transition reason, not a fourth official phase;
- nominal roles come from frozen public sources plus manual override, not replay-based dynamic role classification;
- the probe corpus uses five TI 2026 matches, not three older historical matches;
- a transparent total score is required;
- V1, V2, V3, the user interfaces, the 109-match batch, and final acceptance are all in implementation scope.

Supporting PR #20 documents remain authoritative only where they do not conflict with this document. Implementation must update those documents and machine-readable registries before code review.

## Confirmed product decisions

Paul approved the following on DOT-72:

1. Keep a total score in addition to the radar and underlying metrics.
2. Show V3 as a visually distinct experimental/dashed layer with confidence and evidence.
3. Use a lightweight five-match human review: machine pre-annotation followed by Paul reviewing phase boundaries and approximately 10-15 disputed events per match.
4. Use a dedicated half-hourly DOT-72 coordination autopilot.
5. Use `Dota2 Fullstack Engineer（deepseek）` as the primary implementation agent and a separate Dota2 Code Reviewer for independent verification.
6. Freeze nominal role 1-5 from reliable public information and manual confirmation. Replay behavior never silently changes a player's nominal role.

## Scope

### Tournament scope

- Tournament: TI 2026.
- Corpus denominator: the 109 local replay match IDs listed by `matches.json` and cross-checked against OpenDota league `19719` plus the frozen official schedule.
- Reporting scope: the 109 completed maps currently in the local corpus.
- Patch, map, hero, item, ability, roster, and geometry data are frozen for this tournament. No cross-tournament or future-patch generalization is required.
- A replacement, remake, or superseded game remains an explicit manifest record. It is never silently removed from the denominator.

### Product scope

- Offline replay ingestion and deterministic normalization.
- Source-backed roster/role registry.
- Versioned behavior episodes and three-state phase engine.
- Versioned metric registry covering V1, V2, and V3.
- Role-specific radar and total-score computation.
- Local Chinese-first web interfaces for match, player, team, corpus quality, and review.
- Five-match vertical slice, 109-match batch, gold labels, evaluation, and audit.

### Safety scope

Allowed sources and operations:

- the local downloaded replay files;
- official/public tournament and Steam metadata;
- public OpenDota metadata as a secondary selector/cross-check, with source attribution;
- local replay parsing and local storage;
- local browser UI and user-authored review corrections.

Prohibited:

- account credentials, cookies, or private tokens in code, logs, fixtures, issues, or generated reports;
- Game Coordinator login or automation;
- Dota client/UI automation;
- process memory access, code injection, packet capture, decryption, anti-cheat bypass, or protocol bypass;
- using replay hidden state as a realtime claim or bypassing DotaTV delay/fog of war.

## Source of truth and precedence

Use this order when sources disagree:

1. Immutable local replay bytes for replay events and state.
2. Official tournament schedule/roster/team sources for series and nominal roster role.
3. Steam/OpenDota public match metadata for match identity and selection cross-checks.
4. Manual Paul override for ambiguous role or gold-label adjudication, retained with audit history.
5. Model output only as an explicitly modelled interpretation.

Every retained field declares source, retrieval or observation time, cadence, expected delay where applicable, nullability, confidence, schema version, and content identity.

## Five-match vertical-slice corpus

The following files are present under both the archive root and `dem/`. Public match data is selection evidence only; replay-derived facts control the product result.

| Category | Match ID | Local demo | Screening reason |
|---|---:|---|---|
| ordinary baseline candidate | `8944521919` | `8944521919_219967360.dem` | 45:04 public duration, moderate 25-14 score, three public teamfight records |
| early disruption/roam candidate | `8944525313` | `8944525313_1322006281.dem` | public pregame first blood, roaming flag, six teamfight records |
| fast-ending/stomp edge case | `8946228107` | `8946228107_734267335.dem` | 18:22, 21-5, no public buybacks |
| balanced fight candidate | `8944475884` | `8944475884_106564623.dem` | 42:06, 32-32, eight public teamfight records |
| long repeated-round candidate | `8943477775` | `8943477775_2113524684.dem` | 94:38, eleven public teamfight records, 25 public buybacks |

Before algorithm tuning, the match explorer must screen and either confirm each category or record a replacement from the 109-match corpus. Replacements require match ID, immutable replay hash, reason, and approval history. Selection may not be changed merely to improve evaluation scores.

The immutable initial selection and verified local file identities are recorded in [ti2026-five-replay-probe-v1.json](ti2026-five-replay-probe-v1.json).

## Core vocabulary

### Nominal role

`nominal_role` is a match participant's source-backed position `1`, `2`, `3`, `4`, or `5`. It is a report facet and scoring cohort.

It is not inferred from replay farm share, lane position, hero, inventory, or behavior. Those signals may reveal actions or temporary duties but may not rewrite the role.

### Behavior episode

A behavior episode is a bounded, evidence-linked interval such as a lane segment, lane departure, roam, smoke, fight, farm segment, objective attempt, siege, defense, disengage, reset, or buyback round. Behavior episodes may differ from conventional expectations for the player's nominal role.

### Official phase

Exactly one `global_phase` is assigned to every eligible game-time interval:

- `laning`
- `midgame`
- `decisive`

`reset` is not a fourth phase. It is an episode and an evidence-backed transition from `decisive` back to `midgame`. The phase sequence is therefore:

```text
pregame -> laning -> midgame <-> decisive -> ended
                           ^          |
                           +-- reset--+
```

No fixed `0-10`, `10-30`, or `30+` label is emitted as a parallel formal phase, report dimension, or scoring input. Clock time may be a bounded feature or fail-closed guard inside a documented rule, never an alternative phase conclusion.

### Metric capability level

- `V1`: replay facts and deterministic atomic transforms with no quality judgement.
- `V2`: opportunity-normalized, context-aware deterministic/heuristic conclusions with evidence and validated error bounds.
- `V3`: modelled or counterfactual evaluation with confidence, calibration, abstention, and alternatives.

Capability level is separate from `metric_version`. A V1 metric may evolve from version 1 to version 2 without becoming a V2 metric.

### Epistemic class

Every metric also uses exactly one class:

- `direct`
- `derived`
- `modelled`
- `counterfactual`
- `unavailable`

`unavailable` is null plus a reason code; it is never numeric zero.

## Architecture and ownership boundaries

The replay product is an offline plane. It must not be imported into the existing live GSI capture and realtime analytics core.

```text
immutable .dem
    -> replay verifier/parser
    -> normalized fact partitions
    -> behavior episodes
    -> three-state phase engine
    -> metric registry/calculators
    -> radar and score snapshots
    -> local read API
    -> match/player/team/review UI
```

Expected repository boundaries:

- `cmd/dota2-ob`: add bounded `replay` and `serve` commands without changing existing live defaults.
- `internal/replay/archive`: manifest, content identity, verification, terminal states.
- `internal/replay/parser`: parser adapter and raw parser event boundary.
- `internal/replay/facts`: typed normalization and per-family schemas.
- `internal/replay/roles`: source-backed role registry and manual overrides.
- `internal/replay/episodes`: deterministic behavior episode builders.
- `internal/replay/phases`: official three-state phase engine and correction overlay.
- `internal/replay/metrics`: registry, opportunity generation, calculators, eligibility, and evidence.
- `internal/replay/scoring`: normalization, eight axes, official/experimental totals, uncertainty.
- `internal/replay/store`: content-addressed artifacts, catalog, resume, and query snapshots.
- `internal/replay/api`: loopback read API and authenticated local review mutation API.
- `web/replay`: separate replay-analysis entry point and assets; no external CDN.

Large raw/generated files stay outside git. The implementation accepts `--replay-root` and `--data-root`; tests use small sanitized fixtures inside the repository. Production defaults must not write generated data into the clean repository root.

## Storage and reproducibility contract

Each match has a content-addressed artifact directory containing:

- archive and demo hashes plus verification record;
- parser invocation and dependency versions;
- normalized fact partitions by family;
- quality report and missing-field reasons;
- behavior episodes;
- machine phase output and human correction overlay;
- metric opportunity/value records;
- radar/score snapshots;
- canonical artifact-tree hash.

A small local catalog indexes match, player, team, quality, phase, metric, score, and annotation records for the UI. The artifact partitions remain the recomputable source; the catalog is rebuildable.

All batch stages are idempotent, resumable, atomic per match, and keyed by input replay hash plus parser/schema/rule version. An interrupted run resumes completed matches and never duplicates facts.

## Role registry contract

Each effective role record contains:

```json
{
  "tournament_id": "ti2026",
  "match_id": "8944521919",
  "team_id": "...",
  "account_id": "...",
  "player_name": "...",
  "nominal_role": "4",
  "source_kind": "official_roster|team_announcement|reliable_public_database|manual_override",
  "source_url": "https://...",
  "retrieved_at": "...",
  "effective_from": "...",
  "effective_to": null,
  "confidence": "high",
  "override_reason": null,
  "record_version": "ti2026.roles.v1"
}
```

Rules:

1. Prefer official tournament or team sources; use reliable public databases as corroboration/fallback.
2. Freeze source pages or sanitized source facts with retrieval time and hash when legally and practically possible.
3. Start from tournament-default roles and permit an explicit match override.
4. Never infer a missing role from replay behavior for official reporting. Missing role suppresses role-relative radar/score and produces a review item.
5. Preserve all prior values and override reasons.

## Replay identity, clock, and fact gates

A match may publish player metrics only after all of these pass:

1. Source 2 demo magic and full-file integrity.
2. Dota match ID equals the intended manifest entry.
3. Build, start/end time, teams, and ten participant/hero bindings are internally consistent and cross-checked.
4. Game clock is calibrated across pregame, pauses, gameplay, and end state.
5. Parser run A and clean run B produce byte-identical canonical facts.
6. Required fact-family coverage and actor/target resolution meet the metric's gate.

Minimum normalized fact families:

- `match_state`
- `participant_binding`
- `hero_position_sample`
- `hero_state_sample`
- `economy_sample`
- `item_event`
- `ability_event`
- `combat_event`
- `modifier_event`
- `entity_lifecycle_event`
- `objective_event`
- `vision_event`
- `death_respawn_buyback_event`

Every fact includes match ID, replay hash, tick or ordered source sequence, calibrated game second where available, source event ID, actor/team/entity identity as applicable, parser/schema version, null/missing fields, and confidence.

Position storage retains ordered source changes and materializes a deterministic 2 Hz analysis view. UI may request a coarser view, but higher-resolution source identity remains available for evidence and fight reconstruction.

## Behavior episode contract

V1 implementation must support these deterministic or explicitly unavailable episodes:

- lane assignment and lane segment;
- lane departure and cross-lane arrival;
- roam attempt and outcome window;
- farm/resource interval;
- rune contest;
- smoke activation, participants, contact/break, and outcome window;
- ward placement/destruction/lifetime;
- fight interval and participant set;
- tower/Roshan/Tormentor/high-ground objective attempt;
- siege, defense, disengage, and reset;
- death/respawn/buyback round;
- key item and ability window.

Each episode includes start/end, participants, region, evidence IDs, rule version, missing inputs, exclusions, and confidence. Strategic correctness is never implied by an episode label.

## Official phase engine

The detailed threshold candidates live in `2026-08-17-ti2026-phase-state-machine.md`, amended to match this contract.

Hard requirements:

1. `laning -> midgame` is irreversible.
2. `midgame <-> decisive` may repeat.
3. A reset episode provides evidence for `decisive -> midgame`; it is not emitted as `global_phase`.
4. Exactly one machine phase covers each eligible second. There are no overlaps or uncovered gaps except explicit unavailable clock spans.
5. Transitions use evidence available at or before the boundary in the live-translatable variant.
6. Debounce/hysteresis prevents one kill, one rune trip, one teleport, or one high-ground poke from changing phase.
7. Fast endings may remain `laning` if the lane-break rule never passes; the engine does not fabricate midgame to fill a conventional timeline.
8. Manual review can accept, move, relabel, add, delete, split, or merge phase intervals.
9. Human correction is an overlay. Machine output is immutable and remains inspectable.
10. Every correction retains operator, timestamp, reason, source replay hash, machine rule version, previous value, and new value.

## Metric registry contract

Each metric definition contains:

- stable ID, display name, description, domain, capability level, epistemic class, and metric version;
- report level (`player`, `team`, `episode`, or `match`);
- applicable nominal roles and official phases;
- numerator, denominator/opportunity key, value unit, direction, and aggregation rule;
- time, space, and causal-attribution windows;
- exclusions and explicit zero/null behavior;
- required fact families and field-quality gates;
- deterministic algorithm or model/features;
- evidence record shape;
- confidence and abstention rules;
- known biases and failure modes;
- unit, fixture, gold, and invariant tests;
- radar axis membership, weight eligibility, and score publication gate.

The existing 34-metric registry is the minimum seed. It must be expanded with atomic baseline facts required to interpret rates, including deaths, kills/assists, last hits/denies, XP/net-worth deltas, resource share, item timings, objective damage, participation, and phase/opportunity duration.

The machine-readable initial axis/component and total-weight contract is [ti2026-radar-scoring-v1.json](ti2026-radar-scoring-v1.json). Any weight change requires a new scoring version and recomputation; it may not silently rewrite historical scores.

### V1 publication

V1 publishes direct facts and deterministic atomic transforms. Examples:

- lane and region presence;
- last-hit/deny/XP/net-worth and inventory timelines;
- damage, healing, control, dispel, ability, item, death, respawn, and buyback events;
- ward/smoke/objective/entity events when field gates pass;
- raw participation and resource shares;
- direct phase/opportunity denominators.

No V1 name may contain unproven judgement such as `good`, `effective`, `safe`, `saved`, `space`, or `correct`.

### V2 publication

V2 adds deterministic/contextual opportunity metrics, including:

- lane pressure per contact;
- core farm opportunities and observed protection windows;
- roam attempt/conversion and observed resource cost;
- resource-to-objective conversion;
- smoke contact/outcome windows;
- fight contribution by opportunity;
- initiation/counter-initiation evidence;
- high-ground and buyback-round conversion;
- context-adjusted vision and map-pressure evidence when validated.

V2 must expose its opportunity set. Missing or unreliable opportunity detection suppresses the metric.

### V3 publication

V3 contains modelled or counterfactual questions such as:

- dangerous-farm quality;
- space creation;
- lineup-task execution;
- save value or death prevention;
- beneficial sacrifice or smoke break;
- buyback decision quality;
- alternative-action expected value.

V3 always exposes confidence, supporting/opposing evidence, model/rule version, calibration result, and abstention reason. It never overwrites V1/V2 or appears as an observed fact.

## Eight radar axes

Every player report has the same eight semantic axes, while component metrics and weights are role-specific:

1. `laning`: lane execution, pressure, survival, partner/core outcomes.
2. `resources`: farm opportunity, resource share, efficiency, and item timing.
3. `tempo`: rotations, rune/smoke timing, ability/item windows, response speed.
4. `map`: lane pressure, territory occupation, grouping/split behavior, space evidence.
5. `vision`: placement, removal, lifetime, coverage/contact evidence, smoke information.
6. `fight`: damage, control, healing, saves-as-events, initiation evidence, survival.
7. `objectives`: tower, Roshan, Tormentor, barracks, and post-fight conversion.
8. `endgame`: buyback rounds, high-ground execution, reset/re-entry, terminal decisions.

Rules:

- The official solid radar uses only V1/V2 metrics that passed publication gates.
- The experimental dashed radar may add V3 metric components.
- Axis weights are explicit, versioned, role-specific, non-negative, and sum to 1 within each role/axis.
- Positive/negative direction is declared per metric before normalization.
- Each axis displays metric coverage, opportunity count, matches, confidence, and unavailable components.
- Radar values are comparable only within the same nominal role and scoring version.

## Total score

Two different totals may exist and must never be conflated:

- `official_total_score`: weighted combination of the eight official V1/V2 axes.
- `experimental_total_score`: separately labelled combination including eligible V3 components.

Official scoring procedure:

1. Aggregate each metric using its declared numerator/denominator rule; never average already-averaged rates unless declared.
2. Normalize the eligible metric within the same nominal role and TI 2026 corpus to a 0-100 empirical percentile. Preserve raw value and comparison population.
3. Compute each axis from its role-specific metric weights.
4. Compute the total from versioned role-specific axis weights, each non-negative and summing to 1.
5. Display the total only when at least six axes are publishable, all mandatory role axes are present, at least three matches exist, and the role-specific opportunity coverage gate passes.
6. Never impute an unavailable metric as 0 or 50. If an axis cannot pass coverage, suppress it; do not silently renormalize a total around missing mandatory axes.
7. Always show match count, opportunities, coverage, confidence, score version, and axis breakdown beside the total.

Ties use mid-rank percentiles. Small-sample uncertainty is shown through per-match distribution and bootstrap intervals; it does not disappear behind the total.

The official total is not a causal statement and is not comparable across roles. The UI must not render a combined 1-5 leaderboard as if a position-5 score and position-1 score had identical opportunity meaning.

## Tournament aggregation

Required grains:

- event/episode;
- player-match-phase;
- player-match;
- team-match-phase;
- team-match;
- player-tournament within nominal role;
- team-tournament.

Every aggregate exposes raw numerator, denominator, excluded opportunities, eligible matches, unavailable matches, pooled micro rate, per-match median/IQR, and confidence interval where meaningful.

Team outcomes stay team metrics unless a player attribution rule is directly supportable. A player's participation may be reported without allocating the entire team outcome to that player.

## User interfaces

The replay product is Chinese-first, desktop-first, loopback-only, and usable without reading JSON or logs.

### Corpus quality page

- 109 expected matches and terminal states;
- replay/parser/schema versions and hashes;
- fact-family coverage and missing reasons;
- batch progress, runtime, memory, and resumability evidence;
- filters for verified, corrupt, mismatch, parse failure, and superseded.

### Match explorer

- game timeline with exactly one official phase lane;
- ten hero tracks on a versioned map;
- behavior episodes and objective/fight/vision/buyback markers;
- machine boundary evidence and missing inputs;
- player/team metric cards with numerator, denominator, confidence, and evidence links;
- playback/scrub controls and selectable evidence intervals.

### Player profile

- nominal role and source provenance;
- solid official radar, dashed V3 overlay, official total, and experimental total;
- phase and match filters;
- raw values, role percentile, opportunity/sample counts, confidence, and trends;
- drilldown from every axis to metrics, matches, episodes, and source evidence.

### Team profile

- team radar and team total under a separate team scoring registry;
- lane, tempo, map, vision, fight, objective, and endgame summaries;
- player contribution facts without unsupported causal allocation;
- match and opponent filters.

### Gold-label review

- preloaded five-match queue;
- accept, move, relabel, add, delete, split, and merge phase intervals;
- accept/reject/edit disputed behavior events;
- reason required for mutation;
- machine output, previous corrections, and current correction visible together;
- immutable audit and exportable adjudicated gold set.

No UI uses external CDN assets. Review mutations bind to loopback and require a local anti-CSRF/session token; read-only pages cannot mutate analytical state.

## API contract

Version all replay endpoints under `/api/replay/v1`.

Minimum read endpoints:

- `GET /corpus`
- `GET /matches`
- `GET /matches/{match_id}`
- `GET /matches/{match_id}/timeline`
- `GET /matches/{match_id}/tracks`
- `GET /players/{account_id}`
- `GET /teams/{team_id}`
- `GET /metrics/registry`
- `GET /roles`
- `GET /reviews/queue`

Minimum mutation endpoints:

- `POST /reviews/phase-corrections`
- `POST /reviews/event-corrections`
- `POST /roles/overrides`

Responses include schema version, data/rule/model/scoring versions, generated-at time, input artifact identity, quality/confidence, and explicit unavailable reasons. Large tracks support deterministic time/viewport downsampling.

## CLI contract

Implement stable commands or equivalent subcommands:

```text
dota2-ob replay verify --manifest <path> --replay-root <path> --data-root <path>
dota2-ob replay parse --manifest <path> --match-id <id> --data-root <path>
dota2-ob replay probe --manifest <five-match-manifest> --data-root <path>
dota2-ob replay batch --manifest <109-match-manifest> --data-root <path> --workers <n> --resume
dota2-ob replay evaluate --gold <path> --data-root <path>
dota2-ob replay rebuild-catalog --data-root <path>
dota2-ob serve --data-root <path> --listen 127.0.0.1:<port>
```

Commands return non-zero on invalid identity, required field-gate failure, corrupted output, or failed evaluation gate. Per-match failure never corrupts completed matches and is recorded as a terminal status.

## Evaluation and publication gates

### Direct/V1

- clean A/B canonical facts byte-identical;
- identity and clock gates pass;
- discrete visible-event precision and recall at least 0.98;
- zero silent schema fallback;
- actor/target and position coverage reported per family.

### Derived/V2

- deterministic opportunities and values;
- event precision at least 0.90 and recall at least 0.85;
- phase boundary median absolute error at most 30 seconds and 90th percentile at most 60 seconds;
- no known systematic failure across the five structural probes;
- unavailable/suppression used when required inputs fail.

### Modelled/V3

- at least 20 structurally diverse, human-reviewed matches before tournament-wide default publication;
- precision at least 0.85 and recall at least 0.70 for applicable labelled decisions/events;
- expected calibration error at most 0.10;
- documented feature evidence, opposing evidence, model/rule version, and abstention;
- counterfactual outputs remain experimental even after passing the numeric gate.

The five probes establish feasibility and the first gold labels. They cannot alone promote V3 to default publication. V3 code and UI remain part of the complete implementation even when the data gate keeps a specific output suppressed.

## Performance and operational requirements

- Five-match cold vertical-slice run target: at most 45 minutes on PaulPC4090.
- 109-match cold batch target: at most 8 hours with bounded parallelism; report actual throughput rather than hiding a miss.
- A single worker peak RSS target: at most 8 GiB; aggregate worker count must respect measured host memory.
- Warm restart must skip content-identical completed stages and resume within 60 seconds.
- Precomputed player/team/profile API p95 target: at most 250 ms locally.
- Match timeline/track initial response p95 target: at most 1 second locally for the default viewport.
- Every operation expected to exceed 60 seconds emits visible progress at least once per minute.
- Partial writes use temporary names and atomic promotion; stale partials are detectable and recoverable.

## Implementation stages

These are acceptance barriers inside one final delivery. They do not authorize stopping after V1.

### Stage 1 — specification and contracts

- revise PR #20 and all supporting registries for confirmed decisions;
- freeze five-match probe manifest and role-source contract;
- independently review the complete spec;
- accept exact spec commit before production implementation.

### Stage 2 — five-match vertical slice

- implement verifier, identity, clock, parser facts, role registry, and artifact store;
- implement minimum episodes, three-state phase engine, core V1 metrics, local API, and visible match/player pages;
- run the five probes twice from clean roots;
- deliver direct browser-visible results and field feasibility evidence.

### Stage 3 — complete metric and scoring engine

- implement the full metric registry, V2 opportunities, all eight role radars, official total, V3 interface, and experimental score path;
- implement traceable drilldowns and explicit suppressions;
- add deterministic, fixture, invariant, and property tests.

### Stage 4 — review and gold workflow

- implement all review mutations and audit records;
- machine-prelabel the five matches;
- Paul reviews boundaries and disputed events;
- evaluate and revise thresholds without deleting prior predictions or labels.

### Stage 5 — 109-match batch and tournament UI

- process all terminally valid replays;
- publish corpus, player, team, match, radar, score, and data-quality pages;
- measure runtime, memory, storage, missingness, role coverage, and scoring coverage;
- keep failed/unavailable matches explicit.

### Stage 6 — independent review and acceptance

- review exact candidate commit and artifact identities;
- reproduce critical five-match and corpus commands;
- fix blockers and re-review successor commits;
- record accepted commit, data/scoring versions, evidence root, limitations, and cleanup/rollback commands;
- mark DOT-72 done and pause its dedicated autopilot only after all mandatory gates pass.

## Acceptance criteria

1. All 109 manifest entries reach an explicit terminal state; every published replay is identity-verified.
2. The five selected replays produce inspectable match pages and machine results; any replacement is recorded before tuning.
3. Nominal 1-5 roles are source-backed, auditable, and never silently inferred from behavior.
4. Every eligible game second has exactly one official phase among `laning`, `midgame`, and `decisive`; reset is an episode, not a phase.
5. Machine and corrected phase/event outputs coexist with complete audit history.
6. V1/V2/V3, epistemic class, confidence, evidence, and suppression reasons are visible throughout API and UI.
7. Every published metric contains raw numerator, denominator/opportunity set, exclusions, aggregation rule, sample size, and version.
8. Every player has eight role-specific axes when gates pass; missing axes are explicit.
9. Official and experimental totals are distinct, role-relative, versioned, decomposable, and suppressed when coverage fails.
10. No unavailable value becomes zero and no V3/counterfactual output masquerades as fact.
11. The full batch is deterministic, resumable, bounded, and leaves raw/generated data out of git.
12. The local Chinese-first UI exposes corpus quality, match evidence, player/team reports, radar/score drilldown, and review tools.
13. Safety boundaries are unchanged; no account-risky or hidden-state-live mechanism is introduced.
14. Local verification and independent review pass on the exact accepted commit and artifact identities.

## Required invariants and tests

- one replay hash maps to one immutable parse artifact per parser/schema version;
- ten unique participant bindings or metric publication is suppressed;
- phase intervals are ordered, non-overlapping, gap-free over eligible time, and use only three labels;
- `laning` never re-enters after exit;
- decisive may exit and re-enter with monotonic round indices;
- metric numerator never exceeds a bounded denominator when the metric contract declares that invariant;
- team/player shares reconcile within documented unknown/unattributed residuals;
- unavailable/null never serializes as numeric zero;
- axis weights and total weights are non-negative and sum to 1 per role/version;
- score drilldown exactly reproduces the displayed total;
- official totals contain no V3 inputs;
- repeated clean runs yield identical canonical output hashes;
- interruption and resume do not duplicate facts, opportunities, corrections, or scores;
- review edits never mutate machine predictions;
- loopback mutation endpoints reject missing/invalid local session protection;
- existing GSI/live tests remain unchanged and pass.

## Verification

The implementation issue must preserve exact commands. Minimum final verification:

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
git diff --check

go run ./cmd/dota2-ob replay verify \
  --manifest <five-match-manifest> \
  --replay-root /home/paul-zhang/文档/ti15_rep \
  --data-root <clean-data-root>

go run ./cmd/dota2-ob replay probe \
  --manifest <five-match-manifest> \
  --data-root <clean-a>
go run ./cmd/dota2-ob replay probe \
  --manifest <five-match-manifest> \
  --data-root <clean-b>
<compare-command> <clean-a> <clean-b>

go run ./cmd/dota2-ob replay evaluate \
  --gold <adjudicated-gold> \
  --data-root <clean-a>

go run ./cmd/dota2-ob replay batch \
  --manifest <109-match-manifest> \
  --replay-root /home/paul-zhang/文档/ti15_rep \
  --data-root <corpus-data-root> \
  --workers <measured-safe-count> \
  --resume

go run ./cmd/dota2-ob serve \
  --data-root <corpus-data-root> \
  --listen 127.0.0.1:<port>
```

Browser verification covers all five probe pages, one player at each nominal role, one team, unavailable/suppressed metrics, official versus experimental radar/total, phase corrections, audit history, restart, and a full-corpus quality page.

## Review focus

- parser and identity claims against real replay bytes, not library theory;
- game clock, pauses, entity ownership, illusion/summon attribution, and ten-player binding;
- exact three-phase invariant and reset treatment;
- role provenance and absence of behavioral role inference;
- metric opportunity denominators, aggregation, nulls, and evidence traceability;
- total-score math, weights, role-relative population, small samples, and V3 exclusion from official totals;
- V3 calibration, abstention, and counterfactual labelling;
- idempotency, content addressing, atomic writes, resume, and resource bounds;
- UI drilldown reproducing every displayed axis and score;
- no replay/live-plane coupling or safety-boundary regression.

## Non-goals

- Cross-tournament or future-patch generalization.
- Cloud deployment, multi-user hosting, mobile UI, or public internet exposure.
- Realtime hidden-state output from replay knowledge.
- A cross-role combined leaderboard.
- Presenting strategic intent, causality, saves, sacrifices, or alternative futures as observed truth.
- Silently publishing metrics or scores that fail identity, field, opportunity, coverage, gold, or calibration gates.
