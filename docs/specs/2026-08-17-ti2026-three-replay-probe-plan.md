# Three-replay field probe and manual gold-set plan

Date: 2026-08-17

Issue: DOT-72

## Purpose

Use three already retained, real professional Source 2 demos to prove or reject the parser fields required by the phase and metric specifications before touching the 109-game TI archive. These games are parser/gold-set probes, not TI analytical samples and not evidence about a TI player.

The current facts provide screening signals only. A human must watch each replay and confirm the intended structural category before the gold set is sealed.

## Fixed initial candidates

All three are part of the accepted DOT-54 A/B deterministic 100-replay corpus.

| Intended category | Dota match ID | Duration | Current shallow signals | Demo SHA-256 | Current facts SHA-256 |
|---|---:|---:|---|---|---|
| ordinary-lane candidate | `8771908040` | 1,745 s | 43 resolved hero-kill aggregates, 0 buyback events, 8 smoke item-use aggregates | `68b242d2ee22d180196b766c30310856d88aa9bdb6eddb372dfcc0ab9d81d75c` | `79e77fedebd94586254d0ffcc4244204ae11d64ebf30ae94c5021d772d0b5bfc` |
| early-disruption/rotation candidate | `8835684858` | 934 s | 34 resolved hero-kill aggregates, 0 buybacks, 6 smoke item-use aggregates | `0c59dc344534a8b966ae892e9fb08da6a0b76f400014d6ad390933415e314900` | `f759740abb7d26943fe2c03c3d8e619b4bf499d485fe7b17d5dbf94974b8793c` |
| long multi-round candidate | `8768555805` | 3,950 s | 51 resolved hero-kill aggregates, 12 buyback events, 22 smoke item-use aggregates | `431dbdce717538997ea0448408d579a3b00db1bba709a0d6f71fef043df3231a` | `387fb3a814a52b39e346075baa944a5929ee41649a35c6a290c1d3f4893b3335` |

The smoke numbers are raw item-use aggregates, not smoke attempts or successful ganks. Kill aggregates are not identity-verified player stats. They only help choose contrasting files.

## Structural confirmation and replacement

Before annotation, one observer records a 10-minute screening note without seeing parser predictions:

- lane occupancy by hero from 0:00-10:00;
- actual lane swaps/trilanes;
- support departures and cross-lane arrivals;
- first sustained three-plus-hero objective move;
- whether the long game includes at least two separated buyback/high-ground or Roshan rounds.

Selection rules:

1. `8771908040` remains only if it has a recognizable stable lane structure for at least six minutes and no persistent level-one swap.
2. `8835684858` remains only if it contains an actual early lane swap, repeated roam, or sustained early multi-lane disruption. A short stomp with static lanes is not sufficient.
3. `8768555805` remains only if the 12 raw buyback events separate into at least two human-distinguishable decisive rounds with a reset between them.
4. If a candidate fails, choose another retained DOT-54 demo by the same evidence-first rule, record the old/new IDs and reason, and freeze the replacement before parser output is reviewed.

No selection is tuned to make an algorithm look accurate.

## Probe execution

Run the successor adapter twice in independent clean process roots over identical immutable demo bytes. Each run emits:

- parser/adapter/schema/build/map-geometry versions;
- game-clock anchors and alignment residuals;
- ten participant/slot/team/hero bindings;
- event counts and null/unknown actor rates by family;
- position sample count/cadence/gaps per hero;
- item/inventory, gold/XP/net-worth, creep/last-hit/deny, ability, damage/heal, modifier, ward, smoke, Roshan/Aegis, building, death-timer, and buyback coverage;
- phase intervals and metric opportunity records only after their field gates pass;
- canonical fact hash and artifact-tree hash;
- compressed/demo/fact bytes, elapsed/user/system CPU, and peak RSS.

A/B facts and all deterministic outputs must match byte-for-byte. Resource measurements are compared, not required to be byte-identical.

## Gold-set files

Use source-neutral JSONL outside git while annotating; commit only a sanitized fixture if privacy/source rules allow it.

### Interval record

```json
{
  "match_id": "8771908040",
  "annotator_id": "A",
  "kind": "phase_interval",
  "start_game_second": 0,
  "end_game_second": 600,
  "label": "laning",
  "team": null,
  "player_slot": null,
  "confidence": "high",
  "evidence_note": "stable lane structure",
  "source_view": "manual_replay"
}
```

### Event record

```json
{
  "match_id": "8835684858",
  "annotator_id": "A",
  "kind": "roam_arrival",
  "game_second": 318,
  "end_game_second": 342,
  "team": "radiant",
  "player_slot": 3,
  "location_label": "top_lane",
  "outcome_labels": ["hero_contact", "no_kill"],
  "confidence": "medium",
  "evidence_note": "arrived from original lane and joined non-lane fight",
  "source_view": "manual_replay"
}
```

Every record carries the replay SHA, annotation schema version, game-clock version, and annotation tool/view settings in a sidecar.

## Annotation handbook

Two annotators work independently, then an adjudicator resolves disagreements without deleting the original labels.

Annotate at minimum:

1. game time zero, pauses, end time;
2. player/hero/team/slot identity;
3. lane segments and stable lane structure;
4. lane departures, cross-lane arrivals, roam opportunities/results, and missed lane costs;
5. real tower/rax events and first sustained group objectives;
6. smoke activation, affected heroes, break/contact, location, and fixed 30/60/120-second outcomes when visible;
7. ward placement/destruction/location when the replay UI makes it observable, with `not_observable` otherwise;
8. Roshan death, drops, Aegis owner/transfer/expiry;
9. hero deaths, respawn, buyback availability/use when visible;
10. decisive-round entry, exit/reset, and re-entry;
11. fight windows, initiation/counter-initiation evidence, damage/heal/control/dispel/save casts;
12. explicit unknown/unobservable fields.

Annotators may mark observed actions. They do not label `saved life`, `beneficial sacrifice`, `correct buyback`, or strategic intent as fact.

## Matching tolerances

| Fact | Match rule |
|---|---|
| Direct discrete event | same actor/target/type; absolute time error <= 2 s |
| Position/lane segment | compare at 1-second grid; ignore only declared source gaps |
| Ward/smoke/Roshan location | same versioned region; coordinate error <= 300 units when coordinates are available |
| Phase transition | absolute boundary error; report median, p90, and within-30/60-second rates |
| Fight interval | intersection-over-union >= 0.5 plus participant-set Jaccard >= 0.7 |
| Opportunity | exact opportunity key and eligible/excluded reason agreement |

One-to-many event matching uses maximum bipartite matching within the tolerance window; greedy nearest matching is not allowed to double-count.

## Metrics and gates

Report per match and pooled micro/macro results:

- precision, recall, F1, false-positive/false-negative taxonomy;
- interval intersection-over-union;
- boundary median/p90 absolute error;
- opportunity denominator agreement;
- unknown/missing rate;
- actor/target resolution rate;
- A/B deterministic equality;
- inter-annotator Cohen's kappa for categorical labels and boundary disagreement distribution.

Probe decisions:

- `field_supported`: direct event precision/recall >= 0.98 and no identity/clock failure;
- `derived_candidate`: precision >= 0.90, recall >= 0.85, deterministic opportunities, and no systematic edge-case failure;
- `model_research_only`: below derived gate but precision >= 0.85, recall >= 0.70 with calibrated confidence evidence;
- `unavailable`: input missing, identity/clock unverified, or result below research floor.

Three games cannot promote a model to corpus release. They can only determine whether a larger annotated corpus is justified.

## Error taxonomy

At minimum:

- clock offset/drift/pause error;
- player/hero/team binding error;
- entity class or owner error;
- unresolved actor/target;
- missed/duplicate event;
- temporary structure treated as objective;
- illusion/summon damage assigned to wrong owner;
- ward placement/use conflation;
- smoke use/break/contact conflation;
- lane trip mistaken for roam;
- phase hysteresis/early/late boundary;
- missing source field;
- ambiguous human label.

## TI corpus follow-up

After at least three TI group-stage replays become `verified`, repeat the same probe on TI files selected by outcome-independent rules:

- closest-to-median duration with low early structural disruption;
- strongest manually confirmed early lane swap/rotation case;
- longest verified game with multiple Roshan/buyback rounds.

Do not transfer accuracy from the historical probe to TI without this patch/build-specific rerun.

## Verification commands for the later probe

Exact command names belong to the implementation spec, but evidence must include:

```sh
sha256sum <three immutable demo files>
<probe-command> --input-manifest <manifest> --output-root <clean-a>
<probe-command> --input-manifest <manifest> --output-root <clean-b>
<compare-command> <clean-a> <clean-b>
<evaluate-command> --gold <adjudicated-gold> --predictions <clean-a>
git status --short
```

The report lists exact commands, versions, output hashes, elapsed/CPU/RSS/bytes, and the final class of every priority metric family.
