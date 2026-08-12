# TI Broadcast Analytics Program

Date: 2026-08-12

Decision owner: Paul

Status: M0 architecture-correction candidate

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

## Frozen Program Scope

The release target is **The International 2026 (TI 15) in Shanghai**. The
official tournament field is sampled at `2026-08-12T00:00:00Z`, immediately
before the scheduled group stage. The authoritative tournament source is the
Valve/Perfect World TI 2026 site at
`https://www.dota2.com.cn/international/2026`; aliases and roster membership
must be corroborated with public team-registration or tournament records.

M1 must materialize that scope as a content-addressed `TournamentScopeV1`
manifest before discovery starts. The manifest contains the edition, 16-team
field, player/coach records that are actually needed, public source URLs,
source retrieval times, roster effective intervals, and a SHA-256 content
identity. Later roster changes create a new manifest; they never mutate the
accepted one or retroactively relabel old matches.

The initial historical cutoff is `2026-08-12T00:00:00Z`:

- trailing 90 days starts at `2026-05-14T00:00:00Z`;
- trailing 180 days starts at `2026-02-13T00:00:00Z`;
- the initial accepted gameplay patch is OpenDota patch ID `60`, Dota `7.41`,
  with exact demo game-build identifiers retained per replay;
- a later patch ID or build is a separate window and cannot be mixed silently.

Discovery is a reproducible union, not an open-ended web search. Its manifest
pins provider, endpoint, query parameters, pagination/cursor bounds, retrieval
time, response-page hashes, and terminal result for every candidate match.
Source order is official Steam/public tournament metadata first, OpenDota
public metadata second, and public Valve replay CDN URLs only after metadata
validation. The denominator called `discovered` is the deduplicated union at
the cutoff; `replay-accessible` means a replay passed bounded public download,
checksum, magic, and identity validation within the recorded retry policy.

The 100-replay value is a full-history readiness target, not an unbounded
deadlock:

- **full-history go** requires at least 100 repeatably parsed replays, all 16
  teams represented by at least five matches, and every enabled baseline cell
  meeting its rule's published minimum sample;
- **restricted-history go** is an explicit reviewed scope revision when the
  exhaustive terminal manifest yields fewer than 100 replays or a team has
  fewer than five. Historical rules are disabled per uncovered team/cell and
  the release may show only live-observation rules there;
- **historical no-go** applies when no historical rule family can meet its
  published minimum. M1 records the evidence and the release becomes live-only
  unless a separately reviewed source/scope revision is accepted.

No count may be reached by relaxing source safety, inventing identity, mixing
patches/windows, or treating unavailable replays as successful samples.

## Product Outcome

A broadcast operator can manually join a DotaTV match, start the local system,
and add one transparent localhost page as an OBS Browser Source. During the
match, the system produces a small number of Chinese insights that are:

- factually grounded in current GSI observations and/or immutable prematch
  historical baselines;
- explicit about provenance, source quality, freshness, confidence, sample
  size, and evidence;
- ranked, deduplicated, rate-limited, and safe to suppress;
- assigned a deterministic reason whenever a candidate is omitted or hidden;
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

## Production Topology And Ownership

The production system is one modular local application with separately owned
planes and frontends. It is not a microservice topology:

| Area | Owner | Allowed dependencies | Forbidden dependencies |
| --- | --- | --- | --- |
| raw GSI capture and durable session log | capture | localhost HTTP, session storage | replay, history, policy, i18n, OBS |
| bounded live projection | capture | durable accepted records, domain contracts | replay acquisition, UI bundles |
| offline replay/history | M1 data pipeline | public metadata, downloaded demos, local storage | live policy, presentation |
| pure insight and policy core | M2 backend | five accepted contracts, explicit config/time/state | HTTP, filesystem, database, replay parser, i18n, OBS |
| typed loopback delivery gateway | M3 backend edge | policy command/result ports, committed overlay state | raw GSI/replay/history queries from overlay routes |
| authenticated operator frontend | M3 operator UI | operator command/result API | overlay bundle/state, raw adapters |
| read-only OBS overlay frontend | M3 overlay UI | `OverlayStateV1` only | commands, raw GSI, replay, storage, policy internals |
| optional obs-websocket adapter | M3 OBS edge | approved display/source visibility commands | analytical or operator state mutation |

The corresponding ownership roots are explicit: accepted capture remains in
`internal/gsi`, `internal/capture`, and `internal/session`; M0 shared schemas
live only in `internal/contracts`; the asynchronous follower is
`internal/liveprojection`; M1 owns `internal/history` and its commands; M2 owns
`internal/insight` and `internal/policy`; M3 owns `internal/delivery`,
`internal/presentation`, `internal/obscontrol`, `web/operator`, and
`web/overlay`. A track may import `internal/contracts` but may not import another
track's adapter/storage root. Any necessary root change is an explicit M0 spec
revision, not an implementation convenience.

The operator frontend and OBS overlay have different entry points, bundles,
API privileges, content-security policies, and runtime state. They do not share
a mutable browser store. A composition root wires concrete adapters to typed
ports but contains no parsing, statistics, policy, localization, or rendering
rules.

### Raw acceptance and asynchronous handoff

Raw acceptance ends after one `session.Record` has been validated and durably
appended with its session-local sequence. The GSI response does not wait for
normalization, analytics, insight, audit, localization, delivery, or rendering.
This is an explicit migration from the currently accepted synchronous
projection path; compatibility tests must prove that existing APIs and offline
rebuild still produce the same accepted values.

The live projector follows the durable session log by `(session_id, sequence)`:

- a capacity-one in-memory notification carries only the newest durable
  high-water mark; notifications may coalesce, but accepted records never do;
- one projector reads every missing sequence in order and commits a durable
  cursor only after all projections for that sequence succeed;
- saturation is represented as sequence lag and age. It never drops or rewrites
  raw records and never extends the raw HTTP acknowledgement path;
- on restart, a valid cursor resumes at `cursor + 1`; a missing/corrupt cursor
  rebuilds from the session log. Derived writes are idempotent by evidence
  reference;
- out-of-order or duplicate sequence delivery is rejected or deduplicated with
  a deterministic reason; it cannot reorder committed observations;
- health exposes high-water sequence, projected sequence, lag count, oldest lag
  age, last error, and degraded state;
- shutdown stops intake, finishes any in-flight raw append, closes the listener,
  and persists the projector cursor after a bounded drain. An unfinished
  projection is replayed after restart; a downstream drain cannot corrupt or
  delay an already accepted raw record.

Overlay freshness is computed independently from projection health. If lag,
disconnect, or invalid state crosses its threshold, the delivery plane emits a
hide state even while the projector later catches up. No internal event bus or
universal envelope is introduced; the durable source log and one coalesced
wakeup are the complete handoff.

For this program, inconsistent live input means one of: match/session identity
changes without a session boundary; duplicate sequence with different content;
source/game time regresses outside an explicit pause/reset transition; team or
participant identity conflicts with the bound live manifest; or mutually
exclusive source-presence/value invariants fail. Each condition has a stable
health/suppression code and fails live claims closed.

## Stable Boundaries

The program freezes five cross-track data contracts. They are not
interchangeable source payloads. Supporting tournament/snapshot manifests,
operator-command, command-result, and audit-event records below are narrow
versioned ports, not additional source models or a universal envelope.

### 1. Live observation

`LiveObservationV1` is owned by the contract/live-projection area and is
produced only from one accepted GSI `session.Record`. It carries:

- `EvidenceRefV1`: record schema version, session ID, monotonic sequence,
  receive time, provider/source version, and raw-payload SHA-256;
- match/session identity and the observed game-clock basis;
- every currently accepted `NormalizedTick` field, with source presence
  preserved separately from its value;
- source-quality/confidence flags and deterministic mapping version.

Provider-supplied numeric zero is a real value only when its presence bit is
true. Absent, redacted, unsupported, and invalid are distinct typed states; no
zero value substitutes for them. It contains no historical or replay-derived
value.

`LiveObservationV1` becomes the canonical cross-track observation. The existing
`NormalizedTick` remains a temporary compatibility view produced from it for
the accepted APIs and rebuild outputs; it is not a second persisted contract.
The mapping path is `session.Record + raw payload -> LiveObservationV1 ->
NormalizedTick compatibility view`. Golden tests must prove field/presence
equivalence with current live processing and offline rebuild, and the evidence
reference must resolve back to the immutable raw record. Existing raw and
analytics API shapes do not change during M0.

### 2. Historical baseline

`HistoricalBaselineV1` is an immutable prematch artifact keyed by roster,
player, role, hero, patch/build, metric, window, and sample definition. Every
value includes sample size, period, generated time, source coverage,
nullability, and the identity of its sealed `HistoricalSnapshotManifestV1`.

The manifest is content-addressed and contains tournament-scope identity,
cutoff time, maximum included source-event time, discovery/input manifest
hashes, parser/aggregate versions, included completed match IDs, excluded match
IDs, and baseline content hashes. It is sealed before the observed match/session
starts and bound to that live session. The active match ID is always excluded;
every contributing fact must come from an eligible completed match whose event
time is at or before the cutoff. Unknown live match identity suppresses all
history-dependent claims until the operator binds a verified prematch snapshot.
Late/current-match facts and a snapshot sealed after live-session start are
rejected by contract tests.

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

Creation and expiry are derived from explicit integer-millisecond policy time,
never an ambient wall clock. Candidate identity includes rule/config version,
ordered evidence identities, and the bound historical snapshot identity.

### 4. Broadcast decision

`BroadcastDecisionV1` records whether a candidate was queued, shown, rejected,
superseded, expired, pinned, or emergency-hidden. Automated policy and human
operator actions use the same auditable state machine. It includes policy
revision, causal candidate/command identity, prior state, resulting state,
explicit policy time, and a stable transition/suppression reason.

### 5. Overlay state

`OverlayStateV1` is the only contract consumed by the public overlay page. It
contains localized, display-ready data and health/freshness state. It cannot
contain raw GSI payloads, Steam credentials, account IDs, replay-only hidden
facts, operator privileges, command URLs, or arbitrary HTML. Its canonical JSON
payload is at most 64 KiB, its text/array fields have schema bounds, and any
unknown field, schema mismatch, oversize value, invalid asset key, stale state,
or disconnect causes the renderer to hide analytical claims.

Every visible state carries decision identity, ordered evidence references,
source-quality/confidence, source receive time, presentation publication time,
stale deadline, and bound snapshot identity when historical. Hidden states
carry no claim text and include one stable suppression/health code.

### Supporting control, result, and audit ports

`OperatorCommandV1` is the only mutation input to policy. It contains a unique
command ID, bound session, action enum, target candidate/rule when applicable,
expected policy revision, and explicit policy time. Policy serializes commands
by revision. Repeating a command ID returns the identical prior result; a stale
expected revision is rejected without mutation. Emergency hide has precedence
over pin/show/approve and remains active until an explicit later command clears
it. A session accepts at most 4,096 unique command IDs; later new IDs return
`session_command_limit` without mutation, while duplicates remain replayable.

The startup mode is `approval_required`: safe candidates enter the bounded
preview queue but cannot be shown until approved. A versioned `auto_show`
allowlist may be enabled only after its rules pass M5 factual/editorial
calibration; it uses the same state machine and audit path. Startup, restart,
invalid config, and missing operator authentication all default to
`approval_required` plus hidden output.

`OperatorCommandResultV1` contains command ID, accepted/rejected status,
previous/resulting revision, resulting decision identities, and a deterministic
reason. The M3 console is only a client of this port. It never edits policy
state directly.

`AuditEventV1` is emitted for candidate creation/suppression, every autonomous
transition, and every accepted or rejected command. The pure policy core
returns the decision and audit event as values; an application adapter durably
commits them before a show/pin result is published. Audit failure hides output
and fails the command closed but never blocks raw capture. Events are canonical,
individually bounded to 16 KiB, segmented at 10 MiB, sequence-ordered, and
retained through release acceptance plus 180 days. Rotation bounds storage
without rewriting prior events.

The M2 policy area owns all three supporting schemas and transition semantics.
M3 owns the authenticated HTTP adapter and operator client. The M3 presentation
assembler exclusively owns localization plus creation of `OverlayStateV1` from
committed decisions and health; M2 never emits final prose. The read-only
overlay route cannot address any command port.

### Deterministic encoding and restart

Core evaluation receives observations, one bound baseline snapshot, explicit
rule/config/catalog versions, prior policy state, and policy time as arguments.
There is no ambient clock, random source, network, filesystem, database, or
locale dependency.

Canonical persisted/golden output uses UTF-8 RFC 8785 JSON. Domain quantities
are integers in documented units or fixed-scale decimal strings; floats,
`NaN`, infinity, unordered maps, locale-dependent formatting, and unspecified
time zones are forbidden. Arrays are explicitly ordered. Candidate ties sort by
priority descending, confidence descending, evidence time ascending, rule ID
ascending, then candidate ID ascending. Every other set has a documented total
order.

Policy checkpoints contain schema/rule/config versions, bound session and
snapshot identities, last processed observation sequence, current revision,
command-idempotency window, queue contents, cooldowns, pins, and emergency-hide
state. The preview queue holds at most 64 candidates; the checkpoint retains the
complete bounded set of at most 4,096 command results in revision order. Restart
validates a checkpoint and replays canonical inputs after its sequence. A
missing/incompatible checkpoint rebuilds from the session/decision logs; it
never silently resets policy state. Reprocessing produces identical decision
and audit identities, so persistence adapters deduplicate safely.

## Dependency Direction

```text
Steam/OpenDota adapters ----> discovery/catalog ports
Downloaded .dem -----------> replay parser port ----> replay facts
                                              |             |
                                              v             v
                                     batch state       aggregates
                                                               |
                                                               v
                              sealed prematch snapshot manifest
                                                               |
                                                               v
                                                HistoricalBaselineV1
                                                               |
Manual DotaTV -> GSI -> durable session log                     |
                              |                                 |
                     coalesced high-water wakeup                |
                              |                                 |
                              v                                 |
                     ordered live projector                     |
                              |                                 |
                              v                                 |
                    LiveObservationV1 --------------------------+
                              |
                              v
                       pure insight engine
                              |
                              v
                     InsightCandidateV1
                              |
                              v
                       pure policy core <----- OperatorCommandV1
                              |                       ^
                              |                       |
             BroadcastDecisionV1 + AuditEventV1      |
                              |                       |
                              +--> durable audit      |
                              |                       |
                              v                       |
                 zh-CN presentation assembler        |
                              |                       |
                              v                       |
                       OverlayStateV1                 |
                              |                       |
                    read-only gateway      authenticated operator gateway
                              |                       |
                    OBS overlay bundle      operator console bundle

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
- Versioned golden records exist for the historical snapshot manifest,
  operator command/result, audit event, and policy checkpoint without creating
  a generic shared envelope.
- Contract compatibility and dependency-direction checks run in CI.
- Golden mapping proves each accepted raw record has a stable evidence reference,
  preserves current normalization presence/value semantics, and keeps current
  live/rebuild API results compatible.
- The raw-log/high-water/projector handoff has tested ordering, coalescing,
  saturation, restart, invalid cursor, shutdown, and downstream-failure behavior;
  raw acknowledgement is independent of every downstream adapter.
- A `TournamentScopeV1` golden manifest pins TI 2026, the dated roster-snapshot
  rule, history cutoffs, discovery-run shape, initial patch/build rules, and the
  full/restricted/no-go outcomes above.
- Command idempotency/revision ordering, emergency-hide precedence,
  audit-before-display, canonical encoding, total ordering, checkpoint/replay,
  identifier classification, and localhost control security have adversarial
  contract tests.
- A replay spike parses at least two public professional `.dem` files on the
  frozen patch/build set, reports available facts and parser failure modes, and
  benchmarks CPU, memory, output size, and deterministic repeatability on
  PaulPC4090.
- An OBS spike renders a transparent localhost overlay in OBS on Linux, proves
  reconnect/stale-hide behavior, captures 1080p and 1440p evidence, and records
  any CEF/font limitations.
- Replay acquisition options are classified by provenance, availability,
  credentials, rate limits, retention, and account risk. GC login or account
  automation is not introduced by this gate.
- Storage is selected only after measured replay and parsed-fact volumes. The
  decision explains why a simpler local store is insufficient before adding a
  database service.
- Every M0 measurement uses the protocol and numeric bound in the Measurement
  Protocols section; unsupported claims remain explicit feasibility risks.

### M1: Historical data foundation

Outcome: reproducibly discover, acquire, parse, normalize, and aggregate the
available professional history for participants in the frozen TI 2026 scope.

Acceptance:

- A versioned tournament roster manifest covers every team and player in the
  frozen TI 2026 field with stable IDs, aliases, role, effective dates, and
  provenance.
- The manifest is bound to `TournamentScopeV1` at the `2026-08-12T00:00:00Z`
  effective cutoff; later changes create a reviewed successor.
- A bounded 180-day discovery run using the frozen provider/query/page contract
  creates a content-addressed, deduplicated match and replay manifest. Every
  missing replay has a classified reason; coverage is reported rather than
  fabricated.
- The batch state machine resumes after process termination without duplicate
  downloads, facts, or aggregates and supports bounded retries plus a dead-letter
  state.
- Full-history mode requires the numeric readiness gate above. If the exhaustive
  manifest cannot reach it, M1 records restricted-history or historical-no-go,
  disables unsupported cells/families, and obtains an independently reviewed
  scope successor rather than weakening the gate or stalling indefinitely.
- Player, team, role, hero, patch, and player-hero aggregates are reproducible
  from the manifest and expose games, wins, K/D/A, GPM, XPM, farm checkpoints,
  key item timings, kill participation, and other metrics only when source
  coverage is sufficient.
- Current-patch, trailing-90-day, and trailing-180-day windows remain separate;
  every aggregate exposes sample size and missingness.
- Draft player/hero cells require at least five eligible matches; lane/item
  distribution cells require at least eight eligible observations; team-level
  comparative cells require at least ten eligible matches. Values below these
  initial minima remain unavailable, not low-confidence prose. M2 may raise a
  minimum through versioned rule config but cannot lower one without reviewed
  calibration evidence.
- Secrets, raw account credentials, and unneeded personal profile data never
  enter artifacts, logs, APIs, fixtures, or issue metadata.

### M2: Deterministic insight and broadcast-policy engine

Outcome: turn live observations plus prematch baselines into sparse, auditable
candidate insights and display decisions.

Acceptance:

- The five initial insight families run from immutable fixtures without network,
  database, filesystem, ambient clock, localization, rendering, or OBS
  dependencies. Policy time and every other clock are explicit inputs.
- Replaying identical inputs produces byte-equivalent ordered candidates and
  decisions for the same rule/config versions under the canonical encoding and
  total-order rules above.
- Every shown candidate has evidence, source, confidence, sample size when
  historical, rule version, creation time, and expiry.
- Missing/stale baseline suppresses only history-dependent rules; stale or
  inconsistent live input suppresses all live claims and requests overlay hide.
- Ranking enforces one primary insight at a time, duplicate suppression,
  configurable cooldowns, expiry, and a bounded queue.
- Policy exclusively owns decision mutation. The console can act only through
  idempotent revision-checked commands; every autonomous and human transition
  produces a bounded audit event, and audit/persistence failure fails display
  closed without affecting capture.
- The engine cannot import replay parsers, network clients, storage adapters,
  localization/rendering code, or OBS code; an automated dependency test proves
  this constraint.
- On PaulPC4090, evaluation p99 is below 20 ms under measurement protocol P1 for
  a complete ten-player update, excluding GSI/DotaTV delay. The pure-core
  benchmark process stays below 128 MiB RSS and does not grow more than 16 MiB
  after warmup.
- Golden tests cover positive, negative, low-sample, missing-data, stale,
  out-of-order, pause, restart from valid/corrupt/missing checkpoint, command
  replay, and patch-boundary cases.

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
- Operator and overlay frontends have separate bundles, entry points, state,
  CSPs, and API route sets. The overlay has read-only state access and no raw,
  history, storage, policy-command, operator-token, or OBS-control capability.
- Screenshot tests cover longest Chinese strings, all insight templates,
  missing assets, reconnect, stale, and emergency-hide states with no clipping
  or overlap.
- The operator can preview, approve, reject, pin, unpin, suppress a rule, and
  emergency-hide all output only through `OperatorCommandV1`. Revision conflicts,
  duplicate commands, invalid targets, and audit failure return deterministic
  results and never partially mutate policy.
- State-changing routes bind loopback only, accept POST with
  `application/json`, reject cross-origin/null-origin requests, use no ambient
  cookie authentication, require an ephemeral per-launch bearer token plus
  anti-CSRF request header, cap command bodies at 16 KiB, responses at 64 KiB,
  and each operator session at 20 commands per ten seconds. The token is
  delivered only to the operator process, kept out of URLs, code, logs,
  fixtures, issue metadata, and the overlay bundle, and stored externally with
  user-only permissions if persistence is unavoidable.
- Loss of live freshness, API connection, or valid render state hides analytical
  claims within two seconds under measurement protocol P2 and never leaves stale
  claims frozen on air. Unknown fields, oversized text/state, invalid assets,
  delayed out-of-order responses, and malformed typed parameters follow the
  same fail-closed path.
- Browser Source works without obs-websocket. If scene/source control is enabled,
  the separate obs-websocket adapter is loopback-only, authenticated, optional,
  and cannot mutate analytical state.
- A 60-minute OBS recording passes measurement protocol P3: no crash or refresh
  flash; Browser Source incremental PSS at most 256 MiB over an empty-scene OBS
  baseline; post-warmup growth at most 1 MiB/minute and 64 MiB total; median
  overlay CPU at most 5% of one logical core; overlay-attributable rendering-lag
  delta at most 1.0 percentage point; and zero analytical frames visible after
  any fail-closed deadline. Frame/update evidence is attached.

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
  restart, partial write, audit failure, projector saturation/cursor loss,
  command replay/revision conflict, and out-of-order delivery.
- Failures outside raw capture do not corrupt evidence; presentation failures do
  not block capture; all unsafe states fail closed.
- A 12-hour accelerated soak passes protocol P4: zero lost accepted raw records;
  notification capacity one; candidate queue capacity 64; API request/response
  bodies at most 1 MiB except `OverlayStateV1` at 64 KiB; combined live
  projection/policy/gateway RSS at most 384 MiB; post-warmup RSS growth at most
  64 MiB; goroutine delta at most ten; no unbounded file/descriptor growth; all
  backlogs expose deterministic health and the process exits cleanly.
- Full test, race, vet, contract, frontend, screenshot, and end-to-end suites
  pass from one documented command set.

### M5: Corpus completion, validation, and editorial calibration

Outcome: populate the available 90/180-day corpus and tune rules for usefulness
without weakening factual standards.

Acceptance:

- Every match in the frozen discovery union for the accepted roster/time window
  has a terminal manifest state; all replay-accessible entries are processed or
  have a reproducible parser defect recorded.
- A published local coverage matrix reports teams, players, patches, matches,
  replay availability, parse success, metric availability, and missingness.
- Sampled post-match replay facts validate corresponding GSI-observable metrics
  and event timing within per-metric documented tolerances; mismatches are
  classified, not averaged away.
- A deterministic sample of at least 20 complete matches (seed, population hash,
  and selection algorithm recorded) is evaluated by two reviewers using a fixed
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
- GSI-receive to overlay-state latency is p95 below 500 ms under protocol P5,
  explicitly excluding DotaTV delay; all latency stages are measured separately.
- Restart/reconnect, stale input, overlay refresh, OBS restart, missing history,
  emergency hide, and rollback are exercised from the production runbook.
- Capture remains raw-first and complete during presentation failures; batch
  replay work is suspended or resource-isolated during broadcasts.
- The release manifest pins source commit, contract/rule/catalog/translation
  versions, historical snapshot identity, config checksum, and rollback target.
- Independent code review, security/privacy review, data-quality review, and
  Paul acceptance are recorded with exact evidence.

## Measurement Protocols

Every performance claim records exact source commit, release build command,
contract/rule/config versions, fixture or session identity, host/kernel/runtime,
CPU/GPU/OBS/CEF versions, power mode, concurrent processes, warmup, sample
count, raw samples, and summarization command. Durations use a same-host
monotonic clock. Percentiles use nearest-rank over all valid samples; failures
and excluded samples are reported, never silently removed.

### P1 — pure engine latency and memory

- Run the release-built pure engine on PaulPC4090 with the frozen complete
  ten-player update fixture and its bound baseline/policy state.
- Disable replay parsing, downloads, OBS, browsers, and test parallelism; record
  remaining host load rather than claiming an isolated lab.
- Execute 10,000 untimed warmup evaluations followed by 100,000 measured
  evaluations in one process. Measure function-entry to returned
  candidates/decisions before persistence or JSON encoding.
- Report p50/p95/p99/max, allocations, peak RSS, and post-warmup RSS delta. P1
  passes only if all M2 bounds pass in the same run.

### P2 — fail-closed overlay timing

- At both 1920x1080 and 2560x1440, run 100 trials each for stale deadline,
  disconnect, malformed payload, schema mismatch, oversize payload, missing
  asset, emergency hide, and delayed older response after a newer unsafe state.
- Start timing at the earliest deterministic unsafe condition: stale deadline,
  transport close/error, completed invalid response, or committed emergency
  command. Stop when the DOM claim container is hidden and a captured frame has
  no analytical pixels.
- Report p50/p95/max and any transient reappearance. Every trial must hide
  within 2,000 ms; any later or transient claim is a failure.

### P3 — 60-minute OBS resource/render run

- Use the isolated OBS profile/scene, fixed 1080p and 1440p Browser Sources,
  software/GPU mode and CEF version recorded by the run manifest.
- Record a ten-minute empty-scene baseline, ten-minute overlay warmup, then 60
  minutes cycling all five families, longest zh-CN strings, hide/reconnect, and
  missing assets at the production update cadence.
- Sample process-tree CPU/PSS, GPU memory, rendered/missed/skipped frames, claim
  visibility, and socket/remote-request activity every five seconds. Compare
  lag to the same scene without the Browser Source. Apply the numeric M3 bounds
  to raw samples and attach sanitized frames/logs.

### P4 — 12-hour bounded soak

- Replay a fixed content-addressed multi-match GSI corpus for 12 wall-clock
  hours at 10 accepted records/second, including pause, burst, stale,
  out-of-order, restart, cursor loss, audit failure, and overlay disconnect
  intervals from a checked-in deterministic schedule.
- Sample sequence lag, queue occupancy, file/descriptor/goroutine counts, RSS,
  API/state sizes, decision/audit identities, and raw/derived counts every 30
  seconds. Rebuild afterward and byte-compare canonical derived outputs.
- Apply every numeric M4 bound. Any accepted-record loss, silent queue drop,
  unreported saturation, nondeterministic rebuild, or unclean exit fails P4.

### P5 — production rehearsal latency

- Collect every accepted update across the three full-match rehearsals, with at
  least 5,000 total samples. `t0` is immediately after the complete GSI body is
  accepted for validation; `t1` is atomic publication of the corresponding
  committed `OverlayStateV1` revision.
- Record raw append, projector dequeue, engine return, decision/audit commit,
  presentation assembly, and gateway publication timestamps separately. Report
  unmatched/suppressed observations as outcomes, not discarded latency samples.
- DotaTV observer delay is reported as separate external context and is the only
  excluded stage. P5 passes when end-to-end p95 is below 500 ms and all capture,
  fail-closed, and resource gates pass concurrently.

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
replays remain local.

Identifier classes are explicit:

- source-private identifiers (Steam/account IDs, raw provider handles, local
  paths, tokens) may exist only in protected raw/acquisition records when the
  source requires them; they never enter candidates, decisions, audit, APIs,
  logs, screenshots, fixtures, or overlay state;
- domain identifiers are stable language-neutral tournament/team/player/hero/
  match IDs with source provenance. `LiveObservationV1` uses session-local
  participant slots and evidence references, not Steam/account IDs;
- display identifiers are reviewed official player/team handles and bounded
  catalog asset keys. They enter the overlay only through typed localization
  parameters and can never select a URL, CSS, HTML, or command.

All externally sourced strings render through context-safe text APIs such as
`textContent`; HTML interpretation and dynamic script/style/URL construction
are forbidden. Contract/browser tests include markup, bidi controls, oversized
UTF-8, invalid Unicode, path traversal, unknown keys, and mixed-script handles.
Logs use event codes and opaque evidence hashes rather than raw payloads or
display strings.

Default local retention is 30 days for unpinned raw GSI sessions, 180 days for
downloaded public replays/facts and audit segments, and seven days for bounded
operational logs. Acceptance fixtures retain only sanitized canonical records
and source hashes. A release/rehearsal manifest may pin exact evidence through
release acceptance plus 180 days; expiration removes payloads through an
idempotent cleanup command while preserving non-identifying hashes and deletion
evidence. No file is uploaded or attached without an explicit sanitization
check.

The control plane follows the M3 loopback token/origin/method rules. The overlay
plane is separately read-only, has a restrictive CSP (`default-src 'none'`
with only the exact local script/style/connect/image allowances), performs no
remote fetch, and cannot receive or derive the operator token. Security tests
must prove a hostile remote-origin page cannot mutate policy and that the
overlay cannot call a state-changing route.

## Non-Goals

- A generic analytics plugin marketplace or universal cross-source event model.
- Automatic camera control, matchmaking, spectator joining, or gameplay input.
- Replay-derived hidden facts in live output.
- An unreviewed machine-learning win-probability model.
- Cloud, Kubernetes, Kafka, or distributed operation without measured need.
- Supporting every language before the `zh-CN` broadcast is complete.
