# TI Broadcast Analytics Program

Date: 2026-08-12

Decision owner: Paul

Status: proposed program baseline

## Objective

Build a local, Chinese-language Dota 2 broadcast analytics system that uses
manually observed DotaTV matches to generate timely, evidence-backed technical
insights and present selected insights in an OBS sidebar without obscuring the
match or exposing hidden information.

Delivery is evidence-gated rather than deadline-gated. Milestones advance only
after their acceptance evidence exists and independent review has no blocking
finding.

## Accepted Baseline

The current `main` branch already provides:

- localhost-only GSI capture with raw-first persistence;
- deterministic normalization and derived event generation;
- live/latest/profile/analytics APIs and a local dashboard;
- waiting, receiving, stale, and degraded operator states;
- deterministic offline rebuild from a schema-versioned raw session;
- one accepted PaulPC4090 field session covering 922 ticks, 3,701 events,
  Roshan, Tormentor, buildings, wards, a real stale transition, clean shutdown,
  and a complete offline rebuild.

This program extends that baseline. It does not replace the accepted raw-first
or low-risk source boundaries.

## Product Outcome

A broadcast operator can manually join a DotaTV match, start the local system,
and add one transparent localhost page as an OBS Browser Source. During the
match, the system produces a small number of Chinese insights that are:

- factually grounded in current GSI observations and/or immutable prematch
  historical baselines;
- explicit about source, age, confidence, sample size, and evidence;
- ranked, deduplicated, rate-limited, and safe to suppress;
- reviewable and controllable by a human operator;
- hidden automatically when input state is stale, inconsistent, or unsafe.

## Architecture Principles

### High cohesion

Each component owns one reason to change:

- live capture owns GSI acceptance and source-faithful evidence;
- historical ingestion owns discovery, replay artifacts, identity, and batch
  state;
- historical aggregation owns prematch statistical baselines;
- the insight engine owns semantic claims and evidence;
- broadcast policy owns ranking, suppression, expiration, and operator state;
- localization owns terminology and message formatting;
- presentation owns display-ready state and rendering;
- the OBS adapter owns optional OBS control only;
- audit owns an append-only account of candidates and decisions.

### Low coupling

- Source adapters depend on narrow domain contracts; domain code never imports
  HTTP handlers, databases, filesystems, OBS, or secret handling.
- The live insight path performs no Steam/OpenDota call, replay parse, or
  analytical database query. It consumes a prematerialized historical snapshot.
- OBS rendering works through a localhost Browser Source without obs-websocket.
  WebSocket control is an optional adapter, not a rendering dependency.
- Localization receives semantic keys and typed parameters. Rules never emit
  final Chinese or English prose.
- Replay facts never mutate live observations or leak post-match hidden state
  into live output.

### Low entropy

- Use explicit source-specific contracts, not a generic plugin framework,
  universal event envelope, shared mutable map, or internal event bus.
- Every persisted contract has a version, provenance, deterministic identity,
  and compatibility test.
- Every batch step is idempotent and has explicit queued/running/succeeded/
  failed states. Retries cannot duplicate facts.
- Missing values remain missing. Unknown identity, stale history, and low sample
  size are modeled states, not guessed values.
- Add infrastructure only after measured load proves the current local design
  insufficient.

### Efficiency

- Raw replays and detailed parsed facts are processed offline and never compete
  with the live process during a broadcast.
- Runtime lookups use compact, immutable, prematch snapshots indexed by stable
  IDs.
- Expensive aggregation is incremental and reproducible from a content-addressed
  input manifest.
- Queues, API responses, candidate rings, audit records, and overlay state are
  bounded and observable.

## Stable Boundaries

The program freezes five distinct contracts. They are not interchangeable
source payloads.

### 1. Live observation

`LiveObservationV1` is produced only from an accepted GSI record. It carries
match identity, observed game clock, receive time, nullable observed fields,
source version, and confidence. It contains no historical or replay-derived
value.

### 2. Historical baseline

`HistoricalBaselineV1` is an immutable prematch artifact keyed by roster,
player, role, hero, patch, metric, window, and sample definition. Every value
includes sample size, period, generated time, source coverage, and nullability.

Primary windows are current-patch and trailing 90 days. A trailing 180-day
window is available for sparse samples, but the engine must not silently mix
the two. Team membership is time-bounded; current roster membership cannot be
retroactively applied to historical matches.

### 3. Insight candidate

`InsightCandidateV1` is a semantic claim, not display prose. It contains:

- stable candidate identity and rule version;
- localization key plus typed parameters;
- evidence references and observed values;
- confidence, sample size, priority, created time, and expiry;
- source requirements and an explicit unavailable/suppressed reason.

### 4. Broadcast decision

`BroadcastDecisionV1` records whether a candidate was queued, shown, rejected,
superseded, expired, pinned, or emergency-hidden. Automated policy and human
operator actions use the same auditable state machine.

### 5. Overlay state

`OverlayStateV1` is the only contract consumed by the public overlay page. It
contains localized, display-ready data and health/freshness state. It cannot
contain raw GSI payloads, Steam credentials, account IDs, replay-only hidden
facts, or arbitrary HTML.

## Dependency Direction

```text
Steam/OpenDota adapters ----> discovery/catalog ports
Downloaded .dem -----------> replay parser port ----> replay facts
                                              |             |
                                              v             v
                                     batch state       aggregates
                                                               |
                                                               v
Manual DotaTV -> GSI -> LiveObservationV1 + HistoricalBaselineV1
                                      |              |
                                      +------> insight engine
                                                    |
                                                    v
                                            InsightCandidateV1
                                                    |
                                                    v
                                             broadcast policy
                                                    |
                                      operator -----+-----> audit
                                                    |
                                                    v
                                             OverlayStateV1
                                                    |
                                             zh-CN renderer
                                                    |
                                             OBS Browser Source

Optional obs-websocket adapter controls source/scene visibility only.
```

The composition root may wire adapters. It must not contain parsing,
statistics, insight, ranking, localization, or presentation rules.

## Initial Insight Families

The first production rule set is intentionally bounded:

1. Draft context: recent player/hero use, role, sample size, and patch-aware
   performance.
2. Lane checkpoints: observed 10/15-minute economy or level versus a comparable
   historical distribution.
3. Item timing: observed key-item completion versus player/hero/role baseline.
4. Objective exchange: observable tower, Roshan, Tormentor, kill, and net-worth
   changes summarized as a trade rather than a raw event stream.
5. Teamfight readiness: observable deaths, respawns, key cooldowns, and current
   resources, without claiming fogged or replay-only state.

The first release does not show exact ward coordinates, hidden inventories,
unobserved smoke movements, speculative intent, or an uncalibrated win
probability.

## Milestones And Acceptance Gates

### M0: Architecture, contracts, and feasibility gate

Outcome: freeze the dependency rules and prove the two highest-risk external
edges before production implementation.

Acceptance:

- Independent architecture review reports no blocking finding.
- Versioned schemas and golden examples exist for the five contracts above.
- Contract compatibility and dependency-direction checks run in CI.
- A replay spike parses representative current-patch professional `.dem` files,
  reports available facts and parser failure modes, and benchmarks CPU, memory,
  output size, and deterministic repeatability on PaulPC4090.
- An OBS spike renders a transparent localhost overlay in OBS on Linux, proves
  reconnect/stale-hide behavior, captures 1080p and 1440p evidence, and records
  any CEF/font limitations.
- Replay acquisition options are classified by provenance, availability,
  credentials, rate limits, retention, and account risk. GC login or account
  automation is not introduced by this gate.
- Storage is selected only after measured replay and parsed-fact volumes. The
  decision explains why a simpler local store is insufficient before adding a
  database service.

### M1: Historical data foundation

Outcome: reproducibly discover, acquire, parse, normalize, and aggregate the
available professional history for current TI participants.

Acceptance:

- A versioned tournament roster manifest covers every current TI team and
  player with stable IDs, aliases, role, effective dates, and provenance.
- A 180-day discovery run creates a content-addressed, deduplicated match and
  replay manifest. Every missing replay has a classified reason; coverage is
  reported rather than fabricated.
- The batch state machine resumes after process termination without duplicate
  downloads, facts, or aggregates and supports bounded retries plus a dead-letter
  state.
- At least 100 accessible representative professional replays, spanning all
  available current TI teams and relevant patches, pass checksum, identity,
  parse, normalization, and repeatability checks before full backfill begins.
- Player, team, role, hero, patch, and player-hero aggregates are reproducible
  from the manifest and expose games, wins, K/D/A, GPM, XPM, farm checkpoints,
  key item timings, kill participation, and other metrics only when source
  coverage is sufficient.
- Current-patch, trailing-90-day, and trailing-180-day windows remain separate;
  every aggregate exposes sample size and missingness.
- Secrets, raw account credentials, and unneeded personal profile data never
  enter artifacts, logs, APIs, fixtures, or issue metadata.

### M2: Deterministic insight and broadcast-policy engine

Outcome: turn live observations plus prematch baselines into sparse, auditable
candidate insights and display decisions.

Acceptance:

- The five initial insight families run from immutable fixtures without network,
  database, filesystem, clock, or OBS dependencies.
- Replaying identical inputs produces byte-equivalent ordered candidates and
  decisions for the same rule/config versions.
- Every shown candidate has evidence, source, confidence, sample size when
  historical, rule version, creation time, and expiry.
- Missing/stale baseline suppresses only history-dependent rules; stale or
  inconsistent live input suppresses all live claims and requests overlay hide.
- Ranking enforces one primary insight at a time, duplicate suppression,
  configurable cooldowns, expiry, and a bounded queue.
- The engine cannot import replay parsers, network clients, storage adapters,
  localization/rendering code, or OBS code; an automated dependency test proves
  this constraint.
- On PaulPC4090, evaluation p99 is below 20 ms for a complete ten-player update,
  excluding GSI/DotaTV delay, with bounded memory under a multi-hour fixture.
- Golden tests cover positive, negative, low-sample, missing-data, stale,
  out-of-order, pause, restart, and patch-boundary cases.

### M3: Chinese broadcast UX and OBS integration

Outcome: provide a production-shaped operator console and transparent OBS
sidebar while keeping presentation independent of capture and analytics.

Acceptance:

- `zh-CN` covers 100% of audience-facing keys; `en-US` is the development
  fallback, and CI fails on missing/unused keys or incompatible parameters.
- Hero, item, ability, team, role, metric, and broadcast terminology is
  versioned and reviewed for Chinese broadcast use. Player handles remain their
  official forms.
- The overlay consumes only `OverlayStateV1`, uses no external CDN, has a
  transparent background, and remains legible at 1080p and 1440p safe areas.
- Screenshot tests cover longest Chinese strings, all insight templates,
  missing assets, reconnect, stale, and emergency-hide states with no clipping
  or overlap.
- The operator can preview, approve, reject, pin, unpin, suppress a rule, and
  emergency-hide all output. Every action writes a bounded audit event.
- Loss of live freshness, API connection, or valid render state hides analytical
  claims within two seconds and never leaves stale claims frozen on air.
- Browser Source works without obs-websocket. If scene/source control is enabled,
  the separate obs-websocket adapter is loopback-only, authenticated, optional,
  and cannot mutate analytical state.
- A 60-minute OBS recording has no overlay crash, unbounded growth, visible
  refresh flash, or attributable render stall; measured frame/update behavior is
  attached to acceptance evidence.

### M4: Replay-driven end-to-end integration

Outcome: deterministically exercise the complete live-to-broadcast path without
requiring Dota or OBS for every test run.

Acceptance:

- A captured GSI session can be replayed through live observation, insight,
  policy, localization, overlay state, and audit with deterministic virtual time.
- Golden end-to-end fixtures validate the exact sequence of candidates,
  suppressions, displayed states, and reasons.
- Separate clocks for source observation, local receipt, game time, policy time,
  display time, and DotaTV delay are explicit and alignment is tested.
- Fault scenarios cover malformed input, stale GSI, missing baseline, corrupt
  cache, unavailable history store, overlay disconnect, OBS restart, service
  restart, partial write, and out-of-order delivery.
- Failures outside raw capture do not corrupt evidence; presentation failures do
  not block capture; all unsafe states fail closed.
- A 12-hour accelerated soak keeps queues, files, API payloads, goroutines, and
  memory within documented bounds and exits cleanly.
- Full test, race, vet, contract, frontend, screenshot, and end-to-end suites
  pass from one documented command set.

### M5: Corpus completion, validation, and editorial calibration

Outcome: populate the available 90/180-day corpus and tune rules for usefulness
without weakening factual standards.

Acceptance:

- Every discoverable professional match for the accepted roster/time window has
  a terminal manifest state; all accessible replays are processed or have a
  reproducible parser defect recorded.
- A published local coverage matrix reports teams, players, patches, matches,
  replay availability, parse success, metric availability, and missingness.
- Sampled post-match replay facts validate corresponding GSI-observable metrics
  and event timing within per-metric documented tolerances; mismatches are
  classified, not averaged away.
- At least 20 complete matches are evaluated by two reviewers using a fixed
  rubric. No accepted insight may be factually unsupported; at least 80% of
  displayed insights must be rated useful and non-disruptive by both or be
  revised/disabled.
- Rule thresholds, cooldowns, wording, and disabled-rule decisions are versioned
  and reproducible from the evaluation set.
- Chinese editorial review approves every enabled template and its fallback,
  uncertainty, low-sample, and unavailable wording.

### M6: Production rehearsal and release acceptance

Outcome: prove that a human operator can run the complete system for real
DotaTV/OBS broadcasts and recover safely from routine failures.

Acceptance:

- Three consecutive full-match dress rehearsals complete with DotaTV, local GSI,
  historical snapshot, insight engine, operator console, OBS Browser Source,
  recording, and audit enabled.
- No P0/P1 factual, privacy, hidden-state, crash, evidence-loss, stale-on-air, or
  operator-control defect remains open.
- GSI-receive to overlay-state latency is p95 below 500 ms, explicitly excluding
  DotaTV delay; all latency stages are measured separately.
- Restart/reconnect, stale input, overlay refresh, OBS restart, missing history,
  emergency hide, and rollback are exercised from the production runbook.
- Capture remains raw-first and complete during presentation failures; batch
  replay work is suspended or resource-isolated during broadcasts.
- The release manifest pins source commit, contract/rule/catalog/translation
  versions, historical snapshot identity, config checksum, and rollback target.
- Independent code review, security/privacy review, data-quality review, and
  Paul acceptance are recorded with exact evidence.

## Parallel Delivery Model

Parallel work is allowed only after its input contract is accepted.

```text
M0 barrier
   |
   +--> historical track (M1) --------+
   +--> insight track on fixtures (M2)+--> M4 integration
   +--> zh-CN/OBS track (M3) ----------+
                                           |
                                           v
                                      M5 calibration
                                           |
                                           v
                                      M6 release
```

Rules:

- Each implementation track uses an isolated branch and owns disjoint packages.
- Shared contract changes require a new contract version or an explicit
  coordinator-approved migration; one track cannot silently edit another
  track's input.
- Cross-track imports are allowed only through accepted contracts.
- Each implementation receives focused tests and independent review before its
  branch enters integration.
- Integration happens on a dedicated branch after exact reviewed commits are
  recorded. The complete suite is rerun; passing component tests are not
  integration acceptance.
- A blocking contract or data-integrity finding stops only dependent work.
  Independent tracks may continue.
- The half-hour coordinator advances gates, resolves blockers, and avoids
  duplicate comments or concurrent edits to the same ownership area.

## Verification Strategy

Four test layers are required:

1. Contract tests: schema compatibility, provenance, identity, nullability, and
   dependency direction.
2. Component tests: deterministic rules, adapters, batch state, i18n, policy,
   renderer, and failure behavior with fakes and injected time.
3. Integration tests: real local storage/parser/browser boundaries using fixed
   fixtures and no external credentials.
4. Field tests: manually joined DotaTV sessions, OBS recordings, operator drills,
   and post-match replay comparison.

Acceptance claims require fresh commands, exact commits, measured outputs, and
an independent review. A milestone is not accepted because its assignee reports
completion.

## Safety, Privacy, And Broadcast Boundaries

Allowed sources remain Steam Web API/public metadata, localhost GSI, downloaded
replays, local static game data, and OBS localhost interfaces.

Without a separate explicit approval, the program does not use process memory,
injection, packet capture/decryption, anti-cheat bypass, DotaTV delay/fog bypass,
gameplay/account/UI automation, unofficial GC integration, or credentials for
replay salts.

Community broadcasts must use the operator's own DotaTV view and original
overlay/commentary, not another broadcaster's video feed. Raw sessions and
replays remain local until retention, sanitization, and distribution rules are
accepted from measured evidence.

## Non-Goals

- A generic analytics plugin marketplace or universal cross-source event model.
- Automatic camera control, matchmaking, spectator joining, or gameplay input.
- Replay-derived hidden facts in live output.
- An unreviewed machine-learning win-probability model.
- Cloud, Kubernetes, Kafka, or distributed operation without measured need.
- Supporting every language before the `zh-CN` broadcast is complete.
