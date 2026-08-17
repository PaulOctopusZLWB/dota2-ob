# Five-replay vertical-slice and gold-label plan

Date: 2026-08-17

Issue: DOT-72

## Purpose

Use five local TI 2026 replays to produce the first browser-visible result and prove or reject the fields, episodes, phases, metrics, radar components, and scoring inputs required by the complete implementation specification.

These five matches are a vertical slice and initial gold set. They are not sufficient by themselves to release V3 modelled/counterfactual metrics across the tournament.

## Frozen candidates

All candidate `.dem` and `.dem.bz2` files were confirmed present under `/home/paul-zhang/文档/ti15_rep` on 2026-08-17.

| Intended category | Match ID | Duration | Public screening evidence | Demo file |
|---|---:|---:|---|---|
| ordinary baseline | `8944521919` | 2,704 s | Vici Gaming 25-14 HULIGANI; three public teamfight records | `8944521919_219967360.dem` |
| early disruption/roam | `8944525313` | 2,377 s | Liquid 42-21 Yandex; pregame first blood; public roaming flag | `8944525313_1322006281.dem` |
| fast-ending/stomp | `8946228107` | 1,102 s | Yandex 21-5 Resilience; two public teamfight records; no public buybacks | `8946228107_734267335.dem` |
| balanced fight | `8944475884` | 2,526 s | Spirit 32-32 Aurora; eight public teamfight records | `8944475884_106564623.dem` |
| long repeated-round | `8943477775` | 5,678 s | Vision 56-39 Falcons; eleven public teamfight records; 25 public buybacks | `8943477775_2113524684.dem` |

OpenDota league/match data is used only to screen structurally different candidates. Replay bytes, source-backed roles, and manual review control the analytical output.

## Outcome-independent confirmation

Before viewing machine phase/episode predictions, generate a screening view containing raw tracks and direct replay events only. Confirm:

1. `8944521919` has a recognizable stable opening lane structure and no persistent early disruption that makes it a poor baseline.
2. `8944525313` contains a real pregame/early movement or roaming disturbance, not merely a metadata artefact.
3. `8946228107` ends quickly enough to test whether the phase engine avoids inventing conventional later phases.
4. `8944475884` contains repeated contested fights rather than one-sided cleanup recorded as equal final kills.
5. `8943477775` contains at least two human-distinguishable decisive intervals separated by a genuine reset episode.

If a candidate fails, choose the replacement from the 109-match corpus using the same predeclared category, record candidate and replacement IDs, hashes and reason, and freeze it before examining algorithm quality.

## Immutable input manifest

The implementation adds a five-match probe manifest containing:

- match ID and replay salt;
- archive/demo relative paths;
- compressed and demo SHA-256;
- byte sizes and Source 2 magic;
- intended structural category;
- public selection source URL and retrieval time;
- expected nominal teams and ten participants;
- role-registry version;
- explicit initial archive/identity/parse/publication state plus the required runtime terminal-state field.

Raw replays and generated datasets stay outside git. A sanitized manifest, schemas, small fixtures, and reproducible commands may be committed.

The initial frozen manifest is [ti2026-five-replay-probe-v1.json](ti2026-five-replay-probe-v1.json). Its initial states are deliberately non-terminal: byte/container checks passed, while replay-content identity and the successor parser have not run. The 50 source-backed match-participant roles are frozen in [ti2026-five-replay-role-registry-v1.json](ti2026-five-replay-role-registry-v1.json) and referenced from every expected participant.

## Probe execution

Run the complete pipeline twice from independent clean data roots over identical replay bytes.

Each run emits per match:

- archive/demo identity and parser invocation versions;
- game-clock anchors, pauses and residuals;
- ten match/team/player/hero bindings;
- source-backed nominal 1-5 roles;
- fact counts, null/missing rates and actor/target resolution by family;
- position source updates, deterministic 2 Hz samples, coverage and gaps per hero;
- economy, item, ability, combat, modifier, entity, objective, vision and buyback coverage;
- behavior episodes and exactly one official phase stream using only `laning`, `midgame`, `decisive`;
- V1 values and opportunity denominators;
- V2 candidates only when their gates pass;
- V3 predictions, evidence and abstentions only as experimental output;
- official and experimental radar/score inputs with missing-component reasons;
- canonical fact, episode, metric, score and artifact-tree hashes;
- elapsed/user/system CPU, peak RSS and output bytes.

Canonical deterministic artifacts from clean A and B must match byte-for-byte. Timing and resource measurements are compared but need not be identical.

## First visible result

The probe is incomplete until the local UI shows all five matches. At minimum:

- corpus quality cards;
- one official three-phase timeline per match;
- ten hero tracks;
- fight, objective, smoke, ward and buyback evidence when supported;
- player and team V1/V2 cards with numerator, denominator and evidence;
- solid official radar and total where coverage permits;
- dashed V3 radar/experimental total or explicit abstention;
- drilldown from every displayed value to match interval/event evidence;
- no fabricated value for an unsupported field.

## Machine pre-annotation

For each match, prepare a bounded review queue:

1. every machine phase boundary;
2. every decisive entry and exit plus reset evidence;
3. the highest-confidence and lowest-confidence roam episode;
4. one smoke/contact episode when available;
5. one fight boundary/participant set;
6. one objective-conversion episode;
7. one vision event/interval when observable;
8. one buyback round for matches containing buybacks;
9. every V3 claim that would affect a radar axis or total;
10. explicit missing/unobservable cases.

Deduplicate overlapping items and cap the disputed-event queue at approximately 10-15 items per match, excluding phase boundaries.

## Paul review workflow

Paul reviews the machine-prepared queue in the local interface rather than watching each replay linearly.

Allowed actions:

- accept;
- move boundary;
- relabel interval/event;
- add or delete interval/event;
- split or merge;
- mark observable, unobservable, ambiguous or evidence-insufficient;
- add a reason note.

Every mutation retains match/replay identity, machine version/output, old and new values, operator, time and reason. No correction overwrites or deletes the original machine result.

## Gold records

### Phase interval

```json
{
  "match_id": "8944521919",
  "kind": "phase_interval",
  "start_game_second": 0,
  "end_game_second": 615,
  "label": "laning",
  "reviewer": "paul",
  "confidence": "high",
  "reason": "stable three-lane structure until sustained rotation",
  "replay_sha256": "...",
  "annotation_version": "ti2026.gold.v1"
}
```

### Behavior event/interval

```json
{
  "match_id": "8944525313",
  "kind": "roam_arrival",
  "start_game_second": 210,
  "end_game_second": 236,
  "team": "radiant",
  "player_slot": 3,
  "region": "top_lane",
  "outcome_labels": ["hero_contact", "no_kill"],
  "reviewer": "paul",
  "confidence": "medium",
  "reason": "cross-lane arrival after leaving stable lane",
  "replay_sha256": "...",
  "annotation_version": "ti2026.gold.v1"
}
```

## Matching tolerances

| Fact | Match rule |
|---|---|
| Direct discrete event | same actor/target/type; absolute time error <= 2 s |
| Position/lane segment | compare at 1-second grid; ignore only declared source gaps |
| Ward/smoke/Roshan location | same versioned region; coordinate error <= 300 units when coordinates exist |
| Phase boundary | report absolute error, median, p90 and within-30/60-second rates |
| Fight interval | interval IoU >= 0.5 and participant-set Jaccard >= 0.7 |
| Opportunity | exact opportunity key and eligible/excluded reason agreement |

One-to-many matching uses maximum bipartite matching within tolerance. Greedy nearest matching may not double-count.

## Evaluation

Report per match, category and pooled micro/macro results:

- precision, recall, F1 and error taxonomy;
- interval IoU;
- phase boundary median/p90 absolute error;
- opportunity denominator agreement;
- unknown/missing and abstention rates;
- actor/target resolution and position coverage;
- clean A/B deterministic equality;
- score/axis reproduction from drilldown inputs;
- resource and storage measurements.

Promotion decisions:

- `V1_supported`: identity/clock pass, direct visible event precision/recall >= 0.98, deterministic A/B equality.
- `V2_candidate`: precision >= 0.90, recall >= 0.85, deterministic opportunity sets and no systematic category failure.
- `V3_research_only`: confidence/evidence available but the 20-match gold/calibration gate has not passed.
- `unavailable`: required input, identity, clock, quality or accuracy gate fails.

## Error taxonomy

- clock offset, pause, drift or discontinuity;
- player/team/hero binding or nominal-role source error;
- entity class, owner, illusion or summon error;
- unresolved actor/target;
- missed, duplicate or reordered event;
- temporary structure treated as a real objective;
- ward placement/use/destruction conflation;
- smoke activation/contact/break conflation;
- lane trip mistaken for roam;
- fight clustering or participant-set error;
- phase hysteresis, early/late boundary or false re-entry;
- reset emitted as a phase rather than an episode;
- metric opportunity mismatch;
- unavailable value emitted as zero;
- official score contaminated by V3;
- ambiguous or unobservable human evidence.

## Verification

```sh
sha256sum <five immutable demo files>

go run ./cmd/dota2-ob replay verify \
  --manifest <five-match-manifest> \
  --replay-root /home/paul-zhang/文档/ti15_rep \
  --data-root <clean-a>

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

go run ./cmd/dota2-ob serve \
  --data-root <clean-a> \
  --listen 127.0.0.1:<port>
```

The completion report includes exact commits, commands, hashes, browser-visible URLs/screenshots, actual test results, limitations, replacements, and the final class of every priority field and metric family.
