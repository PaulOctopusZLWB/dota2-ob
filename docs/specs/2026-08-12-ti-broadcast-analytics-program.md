# TI Broadcast Analytics Program

Date: 2026-08-12

Amended: 2026-08-13

Decision owner: Paul

Status: M0 integration base accepted; M1 and M3 active; M2 blocked on the
recovery-contract V2 gate

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
| raw GSI capture and committed session log | capture | localhost HTTP, session storage | replay, history, policy, i18n, OBS |
| bounded live projection | capture | committed accepted records, domain contracts | replay acquisition, UI bundles |
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

Raw acceptance retains the accepted Block A failure model. A record is
**committed** when one complete newline-terminated `session.Record` write has
returned successfully from the operating system with its session-local
sequence. A short/partial write is rolled back to the preceding newline or the
session is sealed. The GSI response does not wait for normalization, analytics,
insight, audit, localization, delivery, or rendering. This is an explicit
migration from the currently accepted synchronous projection path;
compatibility tests must prove that existing APIs and offline rebuild still
produce the same accepted values.

`committed` deliberately does not mean host/power-crash durable. Raw append does
not call `fsync`/`fdatasync` per record and claims only the existing
OS-buffered complete-write boundary in
`2026-08-05-mvp3-manual-capture-and-source-boundaries.md`: ordinary process
restart on a still-running host recovers complete newline records and removes
only an unterminated tail, but kernel crash, storage failure, or sudden power
loss may lose recently acknowledged records. A stronger raw sync profile would
require a separately reviewed contract and P5 latency evidence; DOT-31 must not
introduce it implicitly.

The live projector follows the committed session log by
`(session_id, sequence)`:

- a capacity-one in-memory notification carries only the newest committed
  high-water mark; it is published after raw append returns and is not persisted.
  Notifications may coalesce or disappear on restart, but accepted records
  never do;
- one projector reads every missing sequence in order and advances a cursor
  cache only after all projections for that sequence succeed;
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
  and writes the projector cursor cache after a bounded drain. An unfinished
  projection is replayed after restart; a downstream drain cannot corrupt or
  delay an already accepted raw record.

The cursor/checkpoint cache uses an exclusive complete same-directory temporary
write plus atomic rename so an ordinary process interruption yields an old, new,
missing, or rejected cache—not a trusted partial value. It is not synced and
makes no host/power-crash claim; the session log is always authoritative, and a
stale or lost cursor only causes deterministic duplicate work. Files whose
specification requires power-crash durability, such as committed
broadcast-policy records, use the stronger synchronized protocol defined
separately below.

Overlay freshness is computed independently from projection health. If lag,
disconnect, or invalid state crosses its threshold, the delivery plane emits a
hide state even while the projector later catches up. No internal event bus or
universal envelope is introduced; the committed source log and one coalesced
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
operator-command/result, policy-commit, and audit-event records below are
narrow versioned ports, not additional source models or a universal envelope.

### 1. Live observation

`LiveObservationV1` is owned by the contract/live-projection area and is
produced only from one accepted GSI `session.Record`. It carries:

- `EvidenceRefV1`: record schema version, session ID, monotonic sequence,
  receive time, provider/source version, and raw-payload SHA-256;
- match/session identity and the observed game-clock basis;
- every currently accepted non-private analytical `NormalizedTick` field, with
  source presence preserved separately from its value and participant identity
  represented by session-local slot plus verified tournament identity when
  available;
- source-quality/confidence flags and deterministic mapping version.

Provider-supplied numeric zero is a real value only when its presence bit is
true. Absent, redacted, unsupported, and invalid are distinct typed states; no
zero value substitutes for them. It contains no historical or replay-derived
value.

`LiveObservationV1` becomes the canonical cross-track observation. The existing
`NormalizedTick` remains a temporary compatibility view produced from it for
the accepted APIs and rebuild outputs; it is not a second persisted contract.
The canonical mapping is `session.Record + raw payload -> LiveObservationV1`.
A capture-owned legacy adapter alone may combine that observation with its
evidence-reference-resolved raw record to reproduce the existing
`NormalizedTick`, including source-private account/Steam fields, for accepted
diagnostic APIs and rebuild artifacts. No M1/M2/M3 package may import that
adapter or receive its output. Golden tests must prove non-private
field/presence equivalence, byte-compatible legacy output, evidence resolution,
and absence of private fields from `LiveObservationV1` and all new routes.
Existing raw and analytics API shapes do not change during M0.

#### Legacy localhost API migration

The accepted capture listener has an explicit compatibility/privacy inventory:

- `POST /gsi` is the localhost capture input and returns no captured identity;
- `GET /healthz` and `GET /api/status` expose only health/counters;
- `GET /api/latest` is a source-faithful diagnostic and can expose raw player
  names, account IDs, Steam IDs, and provider fields;
- `GET /api/profile` can expose sampled scalar values, including source-private
  identifiers;
- `GET /api/analytics` and `GET /api/events` can expose observed player handles
  and evidence-derived values;
- `GET /` is the legacy diagnostic dashboard that consumes those `/api/*`
  endpoints.

The accepted offline `normalized_ticks.jsonl` rebuild artifact can likewise
contain account/Steam IDs, and analytics summaries can contain observed player
handles. They remain capture-owned legacy diagnostics under the local session
root; they are not historical baselines or broadcast inputs.

M0 preserves the response shapes and accepted tests for these endpoints. They
are classified as deprecated, protected local diagnostics—not operator,
overlay, or broadcast contracts. The new delivery gateway runs on a separate
loopback listener, never proxies or imports a capture diagnostic handler, and
serves only authenticated `/v1/operator/*` routes plus read-only
`/v1/overlay/state` as `OverlayStateV1`. Neither `web/operator` nor
`web/overlay` may request the legacy listener; dependency, route, CSP, and
hostile-page tests enforce that separation.

By M3, the production profile disables `GET /`, `/api/latest`, `/api/profile`,
`/api/analytics`, and `/api/events` by default while leaving `/gsi`, `/healthz`,
and `/api/status` available on the capture listener. Explicit diagnostic mode
may re-enable the legacy GET shapes unchanged, but requires the ephemeral
operator bearer token, same-origin checks, no CORS, and the separately chosen
capture port. This is the compatibility consequence: authorized diagnostic
clients keep their JSON shape, while unauthenticated production access receives
`404`/`401`. Redacting, versioning, or deleting those shapes after M6 requires
its own migration; DOT-31 does not choose one.

New production session roots use user-only directory/file permissions
(`0700`/`0600`). Existing acceptance artifacts are not silently rewritten;
their permission audit and any in-place migration are explicit runbook steps.

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
by revision. The first 4,096 distinct commands that pass frozen
`OperatorCommandV1` validation plus the V2 identifier-admission bounds enter the
durable idempotency index whether policy accepts or rejects them. After basic
authenticated request/session/command-ID validation, indexed-ID lookup precedes
capacity and action/target evaluation: repeating an indexed ID returns its exact
committed result and creates no new commit. A stale expected revision on a new
admitted ID leaves policy revision, domain state, and policy-time high-water
unchanged, but its terminal rejection commit inserts the idempotency entry and
therefore changes `policy_state.v2` and its hash. Emergency hide has precedence
over pin/show/approve and remains active until an explicit later command clears
it.

Once the durable index contains 4,096 IDs, the authenticated command adapter
rejects every unknown ID before policy evaluation with the fixed admission
error `session_command_limit`. Such an attempt is not an admitted
`OperatorCommandV1`, creates no `OperatorCommandResultV1`, policy commit, or
policy audit, and is not added to the index. Repeating an unindexed over-limit
ID receives the same stateless admission error, not an exact-result replay.
Only a bounded gateway counter records these attempts. V2 production admission
also rejects policy-owned identifiers over 128 UTF-8 bytes before policy
evaluation, without changing the frozen V1 schema.

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
transition, and every policy-accepted or policy-rejected admitted command. An
event is individually bounded to 16 KiB and is never persisted independently
of its causal policy result.

### Recovery contract V2 correction

M2 pre-implementation analysis proved that the M0 `PolicyCommitV1` and
`PolicyCheckpointV1` supporting contracts cannot meet their stated recovery
requirements. A V1 command commit retains only the command ID and result, so a
restart cannot recover the action or rule/candidate target. A V1 checkpoint
retains only preview candidate IDs and omits disabled rules and complete active
state, so `checkpoint + later frames` cannot deterministically continue ranking,
expiry, command targeting, or display state.

This correction does not change `LiveObservationV1`, `HistoricalBaselineV1`,
`InsightCandidateV1`, `BroadcastDecisionV1`, or `OverlayStateV1`, nor the
accepted M0 replay, OBS, capture, projector, delivery, or security evidence. It
explicitly withdraws only `PolicyCommitV1` and `PolicyCheckpointV1` as
production restart formats; they remain immutable legacy compatibility/test
contracts. M2 must first implement and obtain independent review of the V2
supporting contracts below before insight or policy feature work resumes.

`PolicyCommitV2` is the narrow recoverable unit for the policy plane. It is not
a source envelope or event bus. One canonical record, bounded to 256 KiB,
retains the V1 identities, sequence, revision/state hashes, resulting
observation-sequence and policy-time high-water marks, ordered
`BroadcastDecisionV1` values, optional `OperatorCommandResultV1`, causal
`AuditEventV1` values, and publication outcome. It also contains the complete
canonical `OperatorCommandV1` for every command commit. Every observation
commit instead contains the exact input's `EvidenceRefV1` and SHA-256 of its RFC
8785 canonical `LiveObservationV1`. Exactly one causal input exists: an
observation sequence plus matching evidence/hash, or both a command ID and
matching embedded command. Observation evidence must match the commit session
and sequence. Its raw-payload SHA-256 must match the immutable accepted session
record, and deterministic projection through the bound mapping must reproduce
the committed live-observation hash before policy evaluation.

The embedded command's ID/session must match the commit, result, decisions, and
audits. Its action, candidate/rule target, expected revision, and policy time
are source data; recovery must never infer them from reason strings, candidate
IDs, cooldowns, or another frozen field. Every policy-rejected admitted command
and no-display/suppressed observation also receives one terminal commit. Equal
prior/resulting hashes are required only when the canonical semantic state is
unchanged; deterministic time advancement, expiry, or bounded-index maintenance
is represented in the resulting hash even when publication remains unchanged.
In particular, a policy-rejected admitted command keeps revision/domain/time
state unchanged while its one new durable idempotency entry changes the semantic
state hash.

Before its first commit, each V2 policy-log lineage synchronously seals one
content-addressed, 2 MiB-bounded `PolicyLineageManifestV2`. It binds the
session ID; append-only raw-record schema/framing; raw/live schema and mapping
identities; `TournamentScopeV1` and `HistoricalSnapshotManifestV1` identities;
the ordered eligible `HistoricalBaselineV1` content hashes; rule, config,
catalog, and
localization-parameter-mapping versions plus content hashes; and pure-engine
build identity. Every `PolicyCommitV2` and `PolicyCheckpointV2` carries the
manifest ID and SHA-256. The referenced manifest and artifacts are immutable
and retained with the policy log. Its ID is SHA-256 over canonical manifest
content, and its write uses file sync, atomic rename, and parent-directory sync
before the first commit. A manifest write/validation failure hides policy output
and rejects commands without affecting capture. A different artifact set starts
a new lineage; recovery never searches alternative configs or snapshots until a
state hash happens to match. The manifest intentionally does not hash the
growing session-log contents; each later observation commit is content-bound by
its own evidence and live-observation hashes above.

The pure core deterministically returns the unpersisted commit payload from its
explicit input and prior state. Its semantic idempotency entry contains only the
command ID and SHA-256 of the canonical `OperatorCommandResultV1`, both known
before framing. The application layer later supplies the commit sequence and
cache-only frame locator and performs the storage protocol; none of those
application values enters `policy_state.v2`, and no filesystem operation or
retry policy enters the core.

The application adapter serializes `PolicyCommitV2` frames into a policy-only
append log. Each frame is `length || canonical payload || SHA-256 || commit
marker`. Before any command response or presentation publication, the writer
must complete the frame and `fdatasync`/`fsync` its segment. Segment creation or
rotation uses an exclusive same-directory temporary file, file sync, atomic
rename, and parent-directory sync. A failed append is truncated and synced back
to its prior offset; if rollback cannot be verified, the policy log is sealed,
the overlay hides, and commands fail closed. Recovery accepts only contiguous
commit sequences with valid length, hash, and marker; it truncates an incomplete
tail and fails closed on a terminated invalid frame. Segments roll at 10 MiB
and are retained through release acceptance plus 180 days.

The committed frame—not a separate decision, result, audit, or checkpoint
write—is the source of truth. Presentation consumes only a synced committed
frame. Repeating an indexed admitted command ID resolves its cache-only locator,
validates the complete frame, and returns the exact stored result only when its
canonical result hash matches the semantic index entry. This direct lookup is
not pre-checkpoint state replay. Replay reconstructs semantic entries and
locators from committed frames. Checkpoints are optional caches written with
file sync, atomic rename, and parent-directory sync; losing one only forces log
replay. This stronger policy protocol deliberately differs from the OS-buffered
raw-capture boundary and its cost is included in P5.

The M2 policy area owns these supporting schemas and transition semantics.
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
priority descending, confidence descending, evidence time ascending,
`InsightCandidateV1.rule_version` ascending, then candidate ID ascending. For
this order, `rule_version` is the stable, language-neutral versioned rule
identity; V1 has no separate rule-ID field. Every other set has a documented
total order.

`PolicyCheckpointV2` has an explicit cache anchor: lineage manifest ID/hash,
session ID, checkpointed commit sequence, referenced commit SHA-256, last
processed observation sequence, policy revision, and last accepted policy-time
high-water mark. The anchor must equal the referenced commit's lineage,
sequence, resulting revision/state hash, observation-sequence high-water mark,
and policy-time high-water mark.

The checkpointed semantic state contains complete preview
`InsightCandidateV1` records in deterministic rank order; sorted unique disabled
rules; cooldowns; pins; emergency-hide state; and the optional active primary as
its complete candidate plus latest `BroadcastDecisionV1`. It also contains:

- at most 4,096 semantic idempotency entries in canonical command-ID order,
  each containing only command ID and canonical `OperatorCommandResultV1`
  SHA-256; these entries are part of `policy_state.v2` and are computed by the
  pure core;
- a one-to-one cache-only locator table containing command ID, segment ID, frame
  offset, commit sequence, and frame SHA-256; these application values are
  excluded from `policy_state.v2`;
- at most 4,096 candidate tombstones, each with candidate ID, rule ID, terminal
  decision ID/state, and `suppress_until_policy_time_ms`, ordered by suppression
  deadline then candidate ID; the deadline is derived from the bound rule/config
  at the terminal transition; and
- the nondecreasing last accepted policy-time high-water mark used by expiry and
  out-of-order checks.

The preview queue holds at most 64 candidates, pins hold at most 64 entries, and
disabled-rule/cooldown sets hold at most 256 rule IDs each. Candidate IDs are
unique across preview and active state; every pin references a retained
candidate. Expired tombstones are removed deterministically when the policy-time
high-water mark advances. If all 4,096 tombstones remain live, a new unique
candidate is suppressed without insertion using `candidate_index_capacity`;
the resulting hash includes any accepted time/index maintenance. An input
policy time below the high-water mark is rejected using
`out_of_order_policy_time`. An out-of-order observation changes no semantic
state. A new admitted out-of-order command leaves revision/domain/time state
unchanged but inserts its idempotency entry, so only the index and resulting
state hash change. Only successfully time-ordered observations and
policy-accepted commands advance the high-water mark; stale-revision, duplicate,
over-limit, malformed, and out-of-order commands do not.

The V2 checkpoint is bounded to 16 MiB canonical JSON; policy-owned identifiers
stored in its bounded indexes are at most 128 UTF-8 bytes. Its state hash is
SHA-256 over a documented canonical `policy_state.v2` projection containing all
semantic state above while excluding cache creation time, the checkpoint
commit/hash anchor, and the cache-only locator table. Validators recompute that
projection rather than trusting an opaque hash. Each locator must resolve to one
complete valid frame whose sequence/hash, command ID, and canonical command
result hash match both the locator and semantic entry; a missing, duplicate, or
tampered locator fails closed.

Restart validates the complete V2 checkpoint anchor and state, then replays
later commits strictly by commit sequence. For an observation commit, recovery
loads the named session record, validates the embedded `EvidenceRefV1` including
raw-payload hash, reprojects it with the lineage-bound mapping, and verifies the
canonical `LiveObservationV1` hash before evaluation. A mismatch fails even when
the altered field would not change a decision. For a command commit, recovery
re-evaluates the embedded canonical command. The reproduced result, decisions,
audits, publication outcome, revision, and state hash must be byte-equivalent to
the committed record or recovery fails closed. A missing/incompatible checkpoint
loads the same sealed lineage manifest and performs the same replay from sequence
one. A missing or hash-mismatched manifest, record, evidence, observation, or
artifact fails closed; recovery never silently resets state or guesses an
evaluation context.

V1 and V2 frames cannot be mixed in one policy-log lineage. Because no V1
production session has been accepted, new production sessions start with V2.
Encountering V1 during production recovery fails closed; an explicit offline
rebuild may read authoritative session inputs and emit a fresh V2 lineage. A V1
lineage containing a command commit is non-convertible unless a separately
authoritative, complete command journal supplies the exact action, target,
expected revision, and policy time for every command and is content-bound to
that lineage; otherwise it remains archival and recovery fails closed. The
migration must never synthesize missing command data.

The migration gate requires V2 documentation, validators, canonical
goldens/hashes, dependency checks, and recovery tests for admitted versus
over-limit IDs, rejected-command index-only hash mutation, non-circular first
command construction, exact duplicate frame lookup, locator tampering,
accepted/rejected `disable_rule`/`enable_rule`, lineage-manifest loss/mismatch,
later-appended observation evidence/raw/live hash mismatch, size/identifier
admission limits, preview ordering/expiry, tombstone eviction/capacity, time
high-water ordering, active-primary continuation, pins, valid/missing/corrupt
checkpoint anchors, later-frame replay, mixed-version rejection, and state/hash
mismatch before M2 resumes.

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
Manual DotaTV -> GSI -> committed session log                   |
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
             uncommitted PolicyCommitV2 payload      |
                              |                       |
                              v                       |
                   synced policy-commit log          |
                              |                       |
             committed decision/result/audit --------+
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
  raw acknowledgement is independent of every downstream adapter and retains
  the accepted OS-buffered—not host/power-durable—failure model.
- Legacy endpoint inventory, production disable/auth behavior, separate listener
  ownership, and forbidden operator/overlay reachability have compatibility,
  route, dependency, CSP, and hostile-origin tests.
- A `TournamentScopeV1` golden manifest pins TI 2026, the dated roster-snapshot
  rule, history cutoffs, discovery-run shape, initial patch/build rules, and the
  full/restricted/no-go outcomes above.
- Legacy `PolicyCommitV1`/`PolicyCheckpointV1` schema, golden, framing, and
  fail-closed decoder tests remain immutable compatibility evidence; they do not
  satisfy production restart acceptance.
- Before M2 resumes, command idempotency/revision ordering, emergency-hide
  precedence, atomic `PolicyCommitV2`, `PolicyLineageManifestV2`,
  audit-before-display, partial-tail/rollback/recovery, canonical encoding,
  total ordering, complete bounded checkpoint/replay, identifier
  classification, and localhost control security have adversarial contract
  tests with injectable sync/rename/directory-sync failures.
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

- The independently reviewed recovery-contract migration above is accepted at
  an exact immutable commit before rule implementation continues; the five
  primary V1 contracts remain byte-compatible.
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
- Production OBS scenes use one native 750x640 transparent Browser Source with
  no crop, rotation, bounds transform, or scaling. Its top-left position is
  `(1130,60)` in the 1920x1080 canvas and `(1770,80)` in the 2560x1440 canvas.
  Viewport-native tests must cycle every template, the accepted longest `zh-CN`
  fixture, and every fail-closed state after render transitions settle. Every
  visible card must remain inside the source with at least 24 px clearance from
  each source edge, no client/scroll overflow, and ordered header/claim/footer
  regions; every hidden frame must be empty across the complete source. The
  source does not render an otherwise transparent full-canvas CEF surface.
  Full-output screenshots and recording checks still cover both target canvases.
- A 60-minute OBS recording passes measurement protocol P3: no crash or refresh
  flash; median incremental PSS for the complete OBS plus Browser Source process
  tree is at most 256 MiB over the same-resolution empty-scene OBS baseline;
  median overlay process-tree PSS is at most 1 GiB; post-warmup growth is at
  most 1 MiB/minute and 64 MiB total; median Browser Source CPU is at most 5% of
  one logical core; overlay-attributable rendering-lag delta is at most 1.0
  percentage point; and zero analytical frames are visible after any fail-closed
  deadline. Frame/update evidence is attached.

P3 geometry was corrected after exact M3 successor
`5ad9a26d34712196e9b155d760aba61566b7d493` completed both unchanged full runs
on OBS 32.2.1 / Browser Source 2.26.9 / CEF 127.0.6533.120. Independent review
reproduced that all code, evidence-integrity, duration, growth, CPU, lag,
visibility, crash, and network gates passed, while full-canvas Browser Sources
used 266,647 KiB incremental PSS at 1080p and 289,851 KiB at 1440p. The audited
750x450 crop in those recordings was only a visibility-analysis ROI, not proof
that the production layout fit a viewport of that size. Exact-candidate browser
reproduction showed the longest fixture at native 750x450 extended to
`bottom=608.1` and clipped. A bounded-geometry preflight against the same code
selected 750x640: the longest card bounds were `(228,46)-(708,608.1)`, all six
templates stayed ordered without overflow, and the minimum edge clearance was
31.9 px; 750x600 still clipped. This revision removes the unused full-canvas CEF
surface instead of raising the pre-measurement 256 MiB ceiling. The two prior
full runs remain failed evidence and do not accept M3. P3 must be rerun at both
resolutions after the exact 750x640 geometry is implemented and again on the
exact composed M3 candidate.

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
  notification capacity one; candidate queue capacity 64; accepted GSI request
  bodies at most the existing 10 MiB limit, other API request/response bodies at
  most 1 MiB, and `OverlayStateV1` at most 64 KiB; combined live
  projection/policy/gateway RSS at most 384 MiB; post-warmup RSS growth at most
  64 MiB; goroutine delta at most ten; open FDs at most 128 and at most 16 above
  post-warmup baseline; raw file count exactly matches the schedule manifest and
  raw bytes stay within expected fixture bytes plus the larger of 1% or 16 MiB;
  policy commits use at most 1 GiB and 104 segments; total non-raw live files
  under the isolated run data root number at most 192 and use at most 1.6 GiB;
  rotated operational logs use at most ten files/100 MiB; no temporary file
  remains at exit; all backlogs expose deterministic health and the process
  exits cleanly.
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
- No P0/P1 factual, privacy, hidden-state, crash, evidence-loss within the stated
  OS-buffered capture failure model, stale-on-air, or operator-control defect
  remains open.
- GSI-receive to terminal broadcast-outcome latency over all accepted updates,
  and GSI-receive to overlay publication for the publication-required subset,
  are each p95 below 500 ms under protocol P5. DotaTV delay is excluded and all
  local stages are measured separately.
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

- Use the isolated OBS profile/scene and fixed 1920x1080 and 2560x1440 output
  canvases. In each overlay scene, position one native 750x640 transparent
  Browser Source at `(1130,60)` for 1080p and `(1770,80)` for 1440p, with no
  crop, rotation, bounds transform, or scaling. Before either full run, reproduce
  the viewport-native geometry acceptance above and attach full-output composed
  frames; a failure rejects the configuration. The empty baseline uses the same
  canvas, renderer, encoder, preview, recording, and color-source settings but
  no Browser Source. Record source geometry and transform, output FPS, Browser
  Source FPS, software/GPU mode, OBS/Browser Source/CEF versions, and GPU/driver
  in the run manifest.
- Record a ten-minute empty-scene baseline, ten-minute overlay warmup, then 60
  minutes cycling all six template families, longest zh-CN strings, hide/reconnect, and
  missing assets at the production update cadence.
- Sample whole OBS-plus-browser process-tree CPU/PSS and separate OBS/browser
  components, GPU memory, rendered/missed/skipped frames, claim visibility, and
  socket/remote-request activity every five seconds. Claim visibility sampling
  covers the complete 750x640 source rectangle and is corroborated by sanitized
  full-output frames; the former 750x450 ROI is not reused. Incremental PSS is
  the overlay-recording median whole-tree PSS minus the empty-baseline median
  whole-tree PSS; a negative OBS component cannot replace reporting the browser
  component or the absolute overlay-process-tree median. Compare lag to the same
  scene without the Browser Source. Apply every numeric M3 bound to raw samples
  and attach sanitized frames/logs. A short preflight can reject a configuration
  but cannot pass P3 or justify a threshold change.

### P4 — 12-hour bounded soak

- Replay a fixed content-addressed multi-match GSI corpus for 12 wall-clock
  hours at 10 accepted records/second. Its checked-in deterministic schedule
  freezes session count, record count, exact input/raw byte expectation, pause,
  burst, stale, out-of-order, audit failure, and overlay-disconnect intervals.
- Execute 12 orderly restarts, 12 `SIGKILL` process restarts on the same running
  host, and 12 injected incomplete raw tails/cursor/checkpoint/policy frames at
  deterministic sequence numbers. Recovery must preserve every record whose
  OS-buffered append returned before process termination, discard only invalid
  tails, replay stale/missing caches, and preserve every synced policy commit.
  Kernel crash, storage failure, and sudden power loss are explicitly outside
  this gate because raw capture does not sync per record; report that residual
  instead of claiming those failures passed.
- Sample sequence lag, queue occupancy, file/descriptor/goroutine counts, RSS,
  per-class file counts/bytes, policy segment count, API/state sizes,
  decision/audit identities, and raw/derived counts every 30 seconds. Rebuild
  afterward and byte-compare canonical derived outputs.
- Apply every numeric M4 bound. Any accepted-record loss, silent queue drop,
  unreported saturation, nondeterministic rebuild, or unclean exit fails P4.

### P5 — production rehearsal latency

- Collect every accepted update across the three full-match rehearsals, with at
  least 5,000 total samples. `t0` is immediately after the complete GSI body is
  accepted for validation. Every accepted update must produce exactly one
  committed `PolicyCommitV2` terminal outcome: `publish`, `unchanged`,
  `suppressed`, or `hide`.
- For the all-update denominator, `t1` is the synchronized policy-commit time.
  For `publish`/`hide` outcomes that require a new overlay revision, record a
  second `t1_overlay` at atomic gateway publication. Compute nearest-rank p95
  over all accepted updates for `t1 - t0` and separately over the complete
  publication-required subset for `t1_overlay - t0`; report both numerator and
  denominator counts. Unmatched, unchanged, and suppressed observations remain
  in the all-update denominator.
- An update without a terminal commit within 2,000 ms is `unresolved`, is
  assigned 2,000 ms in the all-update distribution if no later timestamp exists,
  and fails P5 regardless of percentile. A required overlay revision missing or
  published after 2,000 ms is likewise a hard failure and remains in the
  publication denominator with its actual latency or 2,000 ms lower bound. Zero
  unresolved/missing publications are allowed.
- Record raw append, projector dequeue, engine return, synchronized policy
  commit, presentation assembly, and gateway publication timestamps separately.
- DotaTV observer delay is reported as separate external context and is the only
  excluded stage. P5 passes when both p95 values are below 500 ms, no hard
  failure above occurs, and all capture, fail-closed, and resource gates pass
  concurrently.

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
  source requires them and, during the explicit compatibility window, in the
  legacy capture diagnostic/rebuild artifacts and loopback responses inventoried
  above; the M3 production profile protects or disables those surfaces as
  specified. They never enter candidates, decisions, policy commits/audit, new
  versioned operator/overlay APIs, operational logs, screenshots, sanitized
  fixtures, or overlay state;
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
