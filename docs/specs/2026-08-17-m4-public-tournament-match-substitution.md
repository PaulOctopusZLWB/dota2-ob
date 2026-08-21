# M4 Public Tournament DotaTV Match Substitution

Date: 2026-08-17

Decision owner: Paul

Status: candidate successor pending complete independent re-review

## Objective

Allow one complete non-TI public professional/tournament DotaTV match to satisfy
the M4 P4 live systems gate without weakening any accepted telemetry, timing,
evidence, privacy, resource, recovery, operator, or review requirement. Preserve
an official TI DotaTV rehearsal as a separate M6 release gate.

## Context

The TI 2026 group stage ended on August 16 and the main event begins on August
20. M4 functional and harness work is ready now, but accepted harness candidate
`abb4210257f26364c2a539de90e386f411f3229a` deliberately rejects a non-TI
identity: it pins program spec `271cc47d503828528b7c69212deb4d22683cb715`,
requires an International URL, and emits TI-specific instructions. It must not
be bypassed through input, configuration, or manual evidence editing.

Paul authorizes a non-TI professional/tournament match as an M4 substitute and
an ordinary publicly spectatable match only as a non-acceptance systems
rehearsal. A ranked public match, private lobby, bot game, replay playback,
demo, synthetic POST stream, or prerecorded broadcast cannot pass M4. Only a
live public match available through DotaTV may enter the rehearsal mode defined
below; that mode cannot produce or be relabeled as P4 evidence.

## Source Of Truth

- Program spec predecessor: `docs/specs/2026-08-12-ti-broadcast-analytics-program.md`
  at `271cc47d503828528b7c69212deb4d22683cb715`.
- Accepted functional candidate: `fa5e7c308ee272722469499ae227b99d1644e2ac`.
- Accepted harness candidate: `abb4210257f26364c2a539de90e386f411f3229a`.
- Harness review: `DOT-63` comment `92a4f970-c8cd-4681-a526-d6a7fa83dae9`.
- Existing single manual handoff: `DOT-70`; it remains closed and must be reused.
- Valve event announcement: `https://steamcommunity.com/app/570/announcements/?l=english`.

## Requirements

### Closed purpose and match classification

- `RunPurposeV1` is a closed enum with exactly `p4_acceptance` and
  `public_match_rehearsal`. `MatchClassV1` is a separate closed enum with exactly
  `ti`, `public_tournament`, and `public_match`.
- The only valid pairs are `(p4_acceptance, ti)`,
  `(p4_acceptance, public_tournament)`, and
  `(public_match_rehearsal, public_match)`. Every unknown, missing, duplicate,
  conflicting, or other cross-product fails before readiness or capture.
- Purpose and class are identity-bearing command inputs included in the
  canonical run manifest, binary/candidate identity, readiness identity, and
  evidence-root hash. They cannot be inferred from mutable evidence,
  configuration, an environment override, or an issue status.
- P4 acceptance and rehearsal use distinct versioned readiness, evidence-index,
  terminal-outcome, and verifier schemas. A verifier for either purpose rejects
  the other purpose's schema and root even if a discriminator, filename, or
  copied JSON field is edited.

### Qualifying match and machine-verifiable authority

- The game is live in the public DotaTV client and belongs to a named
  professional league or tournament.
- Before arming, bind a stable nonzero Valve match ID, competition/league,
  series/game, both public teams, and the expected start window.
- A `public_tournament` run accepts exactly one `MatchAuthorityRootV1`. Its
  canonical JSON bytes are committed inside the exact harness successor and
  embedded in the built verifier. The successor records
  `authority_root_sha256 = SHA-256(canonical root bytes)` in its candidate and
  binary identity; startup recalculates the digest and requires exact equality
  with the compiled value. No CLI argument, path, environment variable,
  configuration, issue field, or live-run input can replace the bytes or
  digest. Exact-SHA harness review covers the root bytes and derived digest.
  Any root change requires a new harness candidate, complete independent
  exact-SHA review, and fresh dual-root readiness before match selection.
- The embedded root declares the league/event identity, its Valve identity when
  available, exact trusted HTTPS origins, bounded endpoint templates,
  extraction-profile version, and the Valve announcement/league record that
  establishes each organizer origin. A URL, display name, operator assertion,
  DNS result, or page supplied only by the live-run caller cannot establish
  authority.
- The harness, not the operator, retrieves every authority artifact after the
  match is selected. One authority retrieval has fixed limits: at most 12
  logical pages total, at most eight pages for one endpoint/paginator, at most
  48 HTTP transactions total including redirects, at most three redirects for
  one logical page, a five-second deadline for each transaction, and a
  45-second deadline for the complete retrieval. Retry count is exactly zero
  and there is no backoff: a connection error, timeout, HTTP 408, 429, 5xx, or
  other non-success response is terminal. Each transaction deadline starts
  before DNS/connect and ends only after the bounded decoded body reaches EOF.
  Redirects remain
  restricted to origins and endpoint shapes in the embedded root. A repeated
  canonical request/cursor, cursor cycle, duplicate page SHA-256 under different
  cursors, page/request/transaction exhaustion, deadline, or non-success terminal
  response fails closed.
- Retrieval permits only identity and gzip content encoding and streams both
  wire and decoded bytes through independent limits: at most 2 MiB of each per
  response and 8 MiB of each for the artifact set. A declared or observed limit
  breach, unsupported encoding, truncated decode, or trailing compressed member
  fails closed. It records retrieval start/end time, final sanitized URL,
  status, media type, wire/decoded byte counts, encoding, redirect chain,
  logical-page key, cursor, transaction count, `retry_count:0`, pagination
  terminal state, and retrieval error. A provider secret may enter only through
  channel; it is stripped from persisted URLs, headers, logs, artifacts, hashes
  exposed outside the private root, and issue evidence.
- `MatchAuthorityEvidenceV1` retains the exact fetched response/page bytes in
  the isolated private evidence root, their SHA-256 identities, and a
  deterministic sanitized export plus its SHA-256 for independent review. Its
  fact bindings cover the exact match ID, league/event, series/game, both teams,
  and start window across the retained artifact set. Each binding contains the
  normalized fact, artifact SHA-256, an extraction-profile version, and either
  a JSON Pointer or exact byte range plus bound-span SHA-256. The independent
  verifier re-extracts every fact from retained bytes and rejects an unmatched,
  missing, ambiguous, out-of-range, or hash-mismatched binding.
- Successful authority preflight emits `MatchAuthorityPreflightV1`, binding the
  exact candidate commit, binary digest, accepted amendment commit, embedded
  authority-root digest, match ID, retained artifact-set digest,
  authority-evidence digest,
  evidence-index digest, retrieval-limit version, and terminal retrieval
  counters. The subsequent `DOT-70` update binds these exact identities. Live,
  finalization, and recovery reject any root, evidence, index, artifact, match,
  candidate, binary, or spec mismatch; substitution requires a new reviewed
  candidate/readiness/preflight cycle.
- Valve-provided league/match metadata may establish the complete authority
  chain directly. An organizer schedule is accepted only under a pinned
  organizer origin and only when the retained artifact set also binds the exact
  Valve match ID; an official-looking or unrelated organizer page is
  insufficient. Redirects outside the pinned origins, incomplete pagination,
  fetch/hash mismatch, a source that does not contain the bound match, any
  conflict among Valve, organizer, DotaTV, GSI, or Paul's confirmation, or any
  fact without an exact retained locator fails closed.
- Community indexes may discover a candidate but cannot be the sole authority or
  independently corroborate a fact they derived from Valve. If retained for
  discovery, their artifacts and facts are explicitly labeled
  `community_contributed` and cannot satisfy an authority binding.
- Arm before game clock `0:00`, run continuously through normal post-game and
  recording finalization, and reject every late join, remake, abandon, identity
  contradiction, missing boundary, evidence gap, or unsafe output exactly as in
  accepted P4.

### Narrow harness successor

- Use exact `abb4210257f26364c2a539de90e386f411f3229a` as the sole parent.
- Bind the accepted commit of this amendment in the harness identity. Do not use
  a floating branch, environment override, or runtime waiver.
- Implement the closed purpose/class model, authority-root/evidence types, and
  purpose-specific schemas above. A public-tournament identity must carry the
  complete qualifying authority evidence. Reject missing, unknown, duplicate,
  conflicting, private/local, credential-bearing, or non-HTTPS sources.
- The allowed production implementation surface is closed to: purpose/class and
  authority types, validation, embedded-root binding, and bounded retrieval;
  purpose-specific readiness/evidence/root/terminal schemas and verifiers;
  value-free coverage normalization and delta generation; directly required
  typed-unavailability suppression and audit adapters; candidate/spec/root/
  preflight identity binding; directly dependent goldens/tests; and command,
  operator, or runbook wording. Do not change insight calculations, policy
  thresholds, queues, persistence formats outside those schemas, capture,
  projection, delivery, rendering, OBS behavior, or unrelated packages.
- Replace only TI-specific runbook and human-instruction wording. Keep the same
  four manual actions and the same at-most-30-minute pregame window.
- Preserve every accepted candidate/parent/remote/PR/binary/environment/process
  identity check, exclusive capture binding, raw/projection/policy/audit/operator/
  overlay reconciliation, five-second measurement, 100 ms visibility trace,
  resource bound, fault matrix, recovery, byte comparison, privacy scan,
  confinement, cleanup, and non-resumable failure rule.
- Rerun two fresh deterministic harness evidence roots against the successor and
  exact current environment. Prior readiness hashes do not authorize the
  successor.
- Update the existing `DOT-70` only after independent exact-SHA review, a new
  exact readiness result pass, and preflight validation of one selected
  qualifying match and its authority evidence. The update identifies the exact
  root/preflight/artifact/evidence/index identities, match, and one absolute
  China Standard Time start interval of at most 30 minutes. It authorizes one
  attempt; it is not match acceptance. Never create another P4 manual checkpoint
  issue.

### Public-match rehearsal is structurally non-acceptance

- `public_match_rehearsal` may exercise the production live process, localhost
  GSI, raw capture, projection, policy, operator, overlay, OBS, resource,
  recovery, and cleanup path on one complete publicly spectatable DotaTV match.
  It requires a stable nonzero public match ID, public DotaTV visibility, one
  continuously bound Dota process and GSI identity, entry before `0:00`, and
  normal post-game/recording finalization. It does not require or fabricate
  tournament qualification.
- Its distinct `PublicMatchRehearsalReadinessV1` and
  `PublicMatchRehearsalEvidenceV1` always canonically bind
  `claims_p4:false`, `qualifying_match:false`,
  `acceptance_eligible:false`, `acceptance_gate:"none"`, and exactly one terminal
  result `rehearsal_complete` or `rehearsal_failed`. They use a separate root
  domain/version and cannot be consumed by the P4 preflight, live, recovery, or
  evidence verifier. Copying or editing a root, index, purpose, class, source,
  readiness, terminal result, or eligibility field invalidates canonical hashes
  and verification.
- Rehearsal uses the console state `REHEARSAL_READY`; it never emits `ARMED` or
  a P4 readiness instruction. Generated output contains no Multica mention or
  status-transition payload and cannot name or update `DOT-64`, `DOT-62`, or
  `DOT-70`. Rehearsal completion has no acceptance-gate side effect.
- Tournament, series/game, professional-team/roster, and historical identities
  unavailable in a public match are represented by the stable typed state
  `unavailable_for_public_match`. Radiant and Dire remain side identifiers, not
  fabricated teams. Account IDs, persona/display names, chat, or unrelated
  screen content cannot fill those fields or enter sanitized canonical
  evidence. Every dependent insight/policy branch suppresses with a stable
  audited reason and never falls back to display names.
- Rehearsal produces a value-free `FieldCoverageDeltaV1` bound to accepted
  captured-schedule SHA-256
  `2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e`,
  exact rehearsal raw-session SHA-256, normalization-algorithm version, and
  evidence-root SHA-256. It retains no scalar value or raw object key classified
  as dynamic/sensitive. Each compared root has a nonzero `source_frame_count`.
  A frame identity is the tuple `(sequence, raw_record_sha256)` for one accepted
  RawRecord. Sequence is strictly increasing; a duplicate sequence or tuple,
  missing identity component, record-hash mismatch, or out-of-order identity
  fails generation rather than being deduplicated.
- For each normalized path and each side, the canonical profile records:
  `frame_count`, the number of unique frames containing at least one occurrence;
  `seen_count`, all occurrences after normalization and aggregation;
  `null_count`, occurrences whose JSON value is null; sorted `json_types` drawn
  only from `null|boolean|number|string|array|object`, including `null` when
  observed;
  `presence`, which is `never` when `frame_count == 0`, `always` when
  `frame_count == source_frame_count`, and `sometimes` otherwise; `nullable`,
  which equals `(null_count > 0)`; `dynamic_collision_count`; and
  `has_dynamic_collision = (dynamic_collision_count > 0)`. Counts must satisfy
  `0 <= frame_count <= source_frame_count`, `seen_count >= frame_count`, and
  `0 <= null_count <= seen_count`.
- Path normalization uses escaped, type-prefixed segments: fixed schema keys use
  `k:<escaped-key>`, every array element uses `a:[]`, and non-schema object keys
  use only a reviewed kind token such as `d:decimal`, `d:uuid`, or `d:opaque`.
  Dynamic observations that map to one token aggregate counts/type sets and
  never overwrite one another. For each parent/path/frame,
  `dynamic_collision_count` increases by one for every distinct concrete
  dynamic key after the first that maps to the same reviewed token; raw keys are
  discarded before canonical output. Array occurrences increase `seen_count`
  but are not dynamic collisions. Prefixes prevent fixed/dynamic/array
  collisions. An unclassifiable key, invalid escape, normalization collision
  across segment kinds, omitted or duplicate union path, or more than one
  classification for a path fails generation. The existing profiler's
  first-scalar sample is prohibited from this artifact.
- The canonical union contains every path observed on either side exactly once.
  Classification uses this precedence: observed in baseline but `never` in
  rehearsal is `missing_in_rehearsal`; `never` in baseline but observed in
  rehearsal is `additional_in_rehearsal`; when both are observed, any difference
  in `presence`, `nullable`, `json_types`, or `has_dynamic_collision` is
  `different_type_or_nullability`; otherwise it is `same`. A union path that is
  `never` on both sides is invalid. Absolute counts are evidence and do not need
  equality once the defined structural profile is equal.
- Every accepted check meaningful for rehearsal remains unchanged, including
  process correlation, sender limitation, privacy, five-second resource
  samples, 100 ms visibility, raw-first capture, gaps/saturation, operator
  actions, recovery byte comparison, confinement, cleanup, and non-resumable
  failure. Acceptance-only tournament/history checks are recorded as typed
  `not_applicable_rehearsal`, never as passes. A late join, incomplete match,
  gap, unsafe output, failed meaningful check, or incomplete OBS finalization
  yields `rehearsal_failed`.
- A later P4 attempt uses a fresh isolated root, `p4_acceptance`, a qualifying
  authority root/evidence set, fresh readiness, and the complete unchanged P4
  procedure. No rehearsal artifact, field delta, hash, operator confirmation,
  or completion can be imported, resumed, spliced, relabeled, or used to skip a
  P4 check.

### Gate ordering

1. Accept one immutable spec successor after complete independent re-review.
2. Implement a harness successor whose sole parent is exact `abb4210257...`.
3. Independently review the exact harness SHA and reproduce fresh readiness.
4. Select one qualifying match and validate its authority evidence against the
   exact embedded root; seal `MatchAuthorityPreflightV1`.
5. Update/reopen existing `DOT-70` with the exact preflight/root/evidence/index/
   artifact identities, match, and bounded window, then execute one complete
   attempt.
6. Independently evaluate the sealed result before accepting P4 or opening
   `DOT-62`; match acceptance is never a prerequisite to step 5.

The optional public-match rehearsal follows its separately reviewed
non-acceptance implementation/execution path and changes none of these states.

### TI release boundary

- A non-TI P4 pass closes only M4 live systems validation.
- At least one of the three M6 dress rehearsals must use a complete official TI
  DotaTV match on the exact release candidate. The non-TI M4 evidence cannot be
  relabeled or reused to satisfy that requirement.

## Acceptance Criteria

- Independent spec review reports no blocking ambiguity or gate weakening on one
  immutable remote commit.
- The implementation successor changes only the closed surface enumerated under
  **Narrow harness successor**: purpose/class and authority validation/retrieval;
  purpose-specific schemas/verifiers; value-free coverage normalization/delta;
  required suppression/audit adapters; bound identities; directly dependent
  goldens/tests; and command/operator/runbook wording.
- Adversarial tests reject arbitrary matchmaking, replay/demo input, synthetic
  feeds, unsafe URLs, credentials, unknown source classes, missing hashes/times,
  metadata conflict, match-ID drift, fabricated/unrelated organizer pages,
  caller-supplied or unembedded authority, root/preflight substitution,
  out-of-root redirect, source/fact locator mismatch, request/page/transaction/
  byte exhaustion, cursor cycle, duplicate page, unexpected retry, compressed
  expansion, incomplete pagination, and TI/non-TI substitution.
- Exhaustive purpose/class tests reject every illegal cross-product. Rehearsal
  tests prove its roots cannot pass P4, it cannot emit `ARMED` or a P4 issue
  transition, sensitive scalar values never enter `FieldCoverageDeltaV1`, and
  two clean roots produce byte-identical full-union coverage classifications.
  Coverage tests include null versus absent, intermittent presence, JSON type
  change, repeated frame identity, multiple dynamic keys in one frame, arrays,
  malformed/unclassifiable keys, omitted/duplicate union paths, sensitive-value
  rejection, and exact baseline-hash mismatch.
- The complete accepted DOT-65 verification matrix, dual-root canonical byte
  comparison, uninterrupted full race run, browser/OBS suites, privacy/source
  scans, remote/PR equality, and clean tree pass at the successor SHA.
- Independent code review and coordinator reproduction pass before a selected
  qualifying match can update/reopen `DOT-70`. No live match is attempted
  during spec or code review.

## Verification

```sh
git diff --check 271cc47d503828528b7c69212deb4d22683cb715..HEAD
git diff --name-status 271cc47d503828528b7c69212deb4d22683cb715..HEAD
rg -n "public_tournament|public_match_rehearsal|MatchAuthority|DOT-70|abb4210257" docs/specs
git status --short
```

The implementation issue must additionally run every verification command and
fresh-root comparison required by accepted `DOT-65`.

## Non-Goals

- No arbitrary public matchmaking or private lobby as M4 acceptance.
- No rehearsal result, field-coverage delta, or manual action as P4 evidence.
- No replay/demo playback, synthetic feed, hidden-state source, account/Dota UI
  automation, memory read, packet capture, or unofficial coordinator work.
- No threshold, queue, duration, identity, privacy, evidence, recovery, or
  cleanup relaxation.
- No merge, deployment, canonical-root mutation, M4 acceptance, M5 promotion,
  or TI-specific M6 waiver.

## Review Focus

- Whether `public_tournament` is closed enough to prevent a random match or
  fabricated organizer page from entering the gate.
- Whether P4 and rehearsal schemas, verifiers, console states, roots, and issue
  side effects are structurally disjoint and fail closed under relabeling.
- Whether source provenance remains useful without retaining API credentials or
  account identifiers.
- Whether the successor scope preserves accepted harness behavior and forces all
  readiness evidence to be regenerated.
- Whether M4 substitution and TI-specific M6 release acceptance remain
  unambiguously separate.
