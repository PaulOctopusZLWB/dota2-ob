# M4 Public-Match Rehearsal Extraction

Date: 2026-08-17

Amended: 2026-08-18

Decision owner: Paul

Status: evidence-invalidated successor correction pending independent review

## Objective

Deliver one independently reviewable ordinary-public-match rehearsal before the
TI main event without waiting for, implementing, or weakening the separate
public-tournament P4 authority boundary. The rehearsal exercises production
GSI, raw capture, projection, policy, operator, overlay, OBS, evidence, resource,
recovery, and cleanup paths and produces a value-free field-coverage delta. It
never claims or advances P4/M4 acceptance.

## Context And Source Of Truth

- Accepted functional candidate: `fa5e7c308ee272722469499ae227b99d1644e2ac`.
- Accepted TI-only harness and required sole implementation parent:
  `abb4210257f26364c2a539de90e386f411f3229a`.
- Accepted design: `ee91e97b9dcbe5e94845256d538ff01857e89e46`,
  reviewed in `DOT-71` comment `97264bc6-ef1b-443a-8045-b6522f46ae85`.
- Rejected combined implementations: `d84936731452c3f99ed1e548fa3748efc424db7e`
  and `3fd8c3261556bfb56758eb93c16264cb4fda8fed`.
- Rejected rehearsal implementations culminate in
  `bf0de1d616b4a8d75d1afc9e8d2498d33fc100a2`; `DOT-85` review comment
  `86574c61-0caa-4521-a39e-d80cdf7d2e6b` records five blockers.
- `DOT-84` contradiction evidence is comment
  `ccac3009-77bf-4f5c-b009-e1b2a4cd9151`: the accepted ordinary live-only
  evaluator has no audit result port and no eight family-owned eligibility
  sites, while its source digest is part of the production engine identity.
- Latest public-tournament review: `DOT-82` comment
  `94e539fe-4d90-4fc1-bbe7-b95216662217`.
- Existing P4 handoff `DOT-70` stays closed and is not used for rehearsal.
- Captured-schedule baseline SHA-256:
  `2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e`.

The rejected candidates are read-only design evidence. A rehearsal successor
starts again from exact `abb4210257...` and contains none of their tournament-
authority implementation.

## Product Boundary

- Add exactly `public_match_rehearsal + public_match`.
- Preserve predecessor TI-only P4 purpose, authority, command, schemas,
  terminal behavior, human instruction, and
  `AcceptedP4Spec = 271cc47d503828528b7c69212deb4d22683cb715`.
  Existing P4 golden bytes remain immutable reference evidence. The migration
  below creates successor identities/goldens; it never relabels old bytes.
- Add a distinct identity-bearing `AcceptedRehearsalSpec` for the accepted
  commit containing this contract. It has no effect on P4 identity.
- Do not implement `public_tournament`, `MatchAuthorityRootV1`, authority HTTPS
  retrieval/approval, or any P4 source widening.
- Use separate rehearsal command entry points or an equivalently closed parser,
  schemas, root domain, readiness, terminal outcomes, verifier, and cleanup
  admission. Cross-purpose bytes and edited discriminators fail closed.
- Every rehearsal artifact binds `claims_p4:false`,
  `qualifying_match:false`, `acceptance_eligible:false`, and
  `acceptance_gate:"none"`. The only positive state is `REHEARSAL_READY`;
  `ARMED` and every acceptance issue transition are impossible.

## Evidence-Invalidated Contract Correction

The original contract required eight genuine production suppression sites while
also forbidding changes to the production insight source and every identity
derived from it. Exact accepted harness `abb4210257...` proves those requirements
cannot both hold: `EvaluateLiveOnly` returns only candidates, emits only the
objective family, and its source SHA-256 is bound into `EngineBuild`. Adapter or
rehearsal-only enumeration would manufacture the evidence; changing the genuine
production path truthfully rotates lineage. This successor chooses the latter
without weakening suppression or P4 qualification gates.

- Add one versioned pure live-only evaluation API used by the ordinary V3
  production and recovery paths, not only by rehearsal. Its input extends the
  accepted live observation/history/lineage/config values with one closed typed
  availability value for each of exactly `hero`, `item`, `lane`, `patch`,
  `player`, `player_hero`, `role`, and `team`. These values are constructed by
  validated product wiring from the accepted history binding; a rehearsal or
  CLI caller cannot invent them.
- Its result contains the existing ordered `InsightCandidateV1` output plus a
  separate bounded ordered list of versioned suppression audits. The evaluator
  remains deterministic and pure: no ambient clock, mutation, callback, HTTP,
  filesystem, database, localization, rendering, or audit side effect.
- The ordinary evaluator directly executes exactly one family-owned eligibility
  site for each typed family on every otherwise valid live-only sequence. Each
  site consumes its own typed value and the observation evidence, decides
  eligibility before any dependent candidate can exist, and returns exactly one
  audit binding family, reason, session, raw sequence, raw-record SHA-256, and
  `candidate_emitted:false`. A fixed helper list, registry walk, no-op field read,
  hard-coded identity return, wrapper/post-evaluation append, arbitrary test site,
  or rehearsal-owned construction is not execution evidence.
- Global invalid/stale/unsafe input may return its accepted global suppression
  candidate only if the result also carries a typed `family_sites_not_evaluated`
  terminal reason. It must not fabricate eight executed audits. For a valid
  live-only sequence, missing, extra, duplicate, reordered, unexecuted, spliced,
  fallback-backed, or claim-producing family evidence fails the production
  result before policy, overlay, or display publication.
- Existing visible candidate semantics, rule thresholds, ordering, localization
  keys, parameters, and observed values do not change. The old and new evaluators
  must produce the same semantic candidate projection for the fixed schedule;
  only fields transitively derived from the truthful source/build/lineage/release
  identities may differ.
- Because the real engine source and ordinary wiring change, the successor must
  recompute their source fingerprints and create new content-addressed
  `EngineBuild`, V3 lineage, live-only release, evidence, and dependent golden
  identities. A canonical migration manifest records old/new identities, the
  exact causal source set, and every changed field. Old artifacts are never
  overwritten, relabeled, or accepted under the successor identity.
- The canonical diff allowlist is closed to source commit, source fingerprints,
  engine build, lineage/release/evidence content IDs and SHA-256 values, and
  fields whose canonical construction directly includes one of those values.
  Match facts, clocks, candidates' semantic projection, policy outcomes,
  suppression reasons, operator/overlay state, thresholds, P4 purpose/authority,
  and terminal results must remain identical. Any other diff rejects the
  migration.
- This spec correction requires independent review before implementation resumes.
  It does not accept any rejected candidate, authorize a live match, or make a
  rehearsal eligible for P4/M4.

## Retained Blocking Corrections

The identity migration resolves only the contract contradiction. The same
successor must also close the other four exact `DOT-85` blockers; none is waived
or deferred.

- Dota executable identity is acquired from an opened `/proc/<pid>/exe`
  descriptor, with pre/post descriptor identity checks and content hashing from
  that descriptor rather than the resolved pathname. Bind PID, comm/path facts,
  executable-content SHA-256, executable-path SHA-256, and start ticks to every
  raw sequence before it is admitted. Recheck both terminal boundaries. Missing
  or ambiguous Dota, PID reuse, exit/replacement, same-content/different-path,
  adjacent-frame drift, and drift then restoration fail closed on the affected
  sequence. This is limited to low-risk `/proc` process identity; it never reads
  game memory.
- `MaxRehearsalRawLineBytes = 13,985,113` means the maximum complete encoded
  line including its required final LF byte. Exactly 13,985,113 bytes passes;
  13,985,114 fails before unbounded allocation. The payload therefore has a
  maximum of `MaxRehearsalRawLineBytes - 1` bytes. LF is the sole canonical line
  ending; CRLF and an unterminated final line fail. Retain descriptor-safe
  streaming and the accepted `64 MiB + 1` total-session admission boundary.
- Every failed raw admission after any byte is consumed emits a typed bounded
  receipt. Line, total, unterminated, parse, concurrent-growth, replacement, and
  reconciliation failures bind the opened descriptor identity, bytes actually
  read through the relevant `cap + 1` boundary, reconciled counters and prefix
  hash, pre/post descriptor/path identity, size/content guard, and primary plus
  concurrent-change failure state. Post-read reconciliation always runs. An
  unread suffix is never represented as a full-content hash; suffix mutation is
  detected or the retained failed prefix/descriptor state is immutably bound.
- `cmd/m4-match rehearsal-attempt` exits zero only after independent verification
  of canonical `rehearsal_completed`. Every `rehearsal_failed`, `REFUSED`,
  `preflight_refused`, `dota_identity_unavailable`, zero/partial outcome,
  verification failure, or cleanup-ineligible result first emits its canonical
  machine-readable receipt and then exits nonzero. Command-level tests assert
  both receipt bytes and shell status.

## Public Match And Manual Boundary

- After a separate rehearsal handoff reports `REHEARSAL_READY`, Paul manually
  chooses and joins one publicly spectatable DotaTV match.
- Require a stable nonzero match ID, continuously bound Dota process/executable/
  start tick, exclusive localhost GSI listener, entry before `0:00`, plausible
  continuous frames, normal winner/post-game, and complete OBS finalization.
- Competition, league, series, game number, team, roster/history, organizer, and
  TI identities are typed `unavailable_for_public_match`. Display names,
  Steam/account identifiers, or inferred labels cannot fill those fields or
  enter canonical evidence.
- Retain the four operator actions, fail-closed visibility, resource sampling,
  raw/projected/policy/audit/operator/overlay reconciliation, restart, no-cache
  recovery, byte comparison, confinement, cleanup, and non-resumable failures.
  Acceptance-only checks are `not_applicable_rehearsal`, never passes.
- Late join, remake/abandon, gap, process replacement, unsafe/stale output,
  failed meaningful check, incomplete recording, or unclean exit produces a
  verifiable `rehearsal_failed`; attempts are never resumed or spliced.

## Verifiable Failure Contract

- Every terminal rehearsal, including preview refusal, zero/partial frames,
  process loss, and coverage failure, is independently verifiable and cleanable.
- `FieldCoverageStatusV1` is a closed union. `complete` carries
  `FieldCoverageDeltaV1`. `unavailable` carries exactly one reason:
  `no_accepted_frames`, `insufficient_frames`, `baseline_identity_failure`,
  `rehearsal_identity_failure`, or `coverage_generation_failure`, reconciled
  frame/population counters, and relevant retained hashes; it carries no partial
  delta or scalar sample.
- `rehearsal_completed` requires `complete`, nonzero exact populations, and a
  valid delta. `rehearsal_failed` accepts `complete` or `unavailable` only when
  reason, terminal failure, counters, artifacts, and cleanup state reconcile.
- Tests cover preview abort, zero/one/partial frame, late join, malformed frames,
  baseline mismatch, verifier restart, and cleanup for every reason.

## Runtime Suppression Evidence

- Static manufactured suppression assertions are forbidden. Every tournament/
  history-dependent family site executed by the ordinary production evaluator
  during rehearsal receives its typed unavailable input and returns one linked
  canonical suppression audit through the pure versioned result above.
- Each audit binds source sequence, branch/family ID, unavailable reason,
  suppression result, and absence of candidate/decision/overlay claim; it
  retains no display/account identity, scalar, or raw fragment.
- The verifier derives the expected branch set from accepted registry/config
  identity and rejects missing, duplicate, static-only, unexecuted, fallback-
  backed, or claim-producing audits.
- Tests make a real branch without its audit, an audit without execution,
  display/account fallback, and a leaked dependent claim fail.

## Value-Free Field Coverage

- Implement `FieldCoverageDeltaV1` from accepted `ee91e97...`: strict frame
  identity/order/hash, escaped typed paths, unique-frame/occurrence/null/type/
  presence/collision profiles, complete union, deterministic classification,
  and exact baseline/rehearsal/algorithm/root binding.
- Baseline is the captured schedule hash above; rehearsal inputs are retained
  RawRecordV3 identities from the same sealed attempt.
- Never retain scalar samples, concrete dynamic keys, account/display names, or
  sensitive values. Canonical verification regenerates the delta.
- Cover null versus absent, intermittent presence, type changes, duplicate/
  missing/out-of-order/hash-mismatched frames, same-frame dynamic collisions,
  arrays, malformed segments, union errors, sensitive values, and baseline hash
  mismatch.

## Implementation Surface

- Required sole parent is exact `abb4210257f26364c2a539de90e386f411f3229a`.
- Allowed files are `cmd/m4-match`, `internal/m4match`, one rehearsal runbook;
  the directly required typed supporting contracts and pure insight eligibility
  implementation; ordinary V3 production/recovery wiring; source fingerprints,
  generated/reference copies, migration manifest, and successor content-
  addressed goldens; and dependent tests. Every outside path needs exact
  requirement mapping.
- Allowed behavior is rehearsal parsing, distinct schemas/verifier/root/cleanup,
  pure production suppression audit, the closed identity migration above,
  value-free coverage, wording, and tests.
- Excluded behavior is tournament authority/root/retrieval, P4 qualification or
  terminal semantics, visible insight calculations, thresholds, queues, raw
  capture, projection, delivery, rendering, OBS behavior, unrelated persistence,
  and normal user profiles.
- `3fd8c326...` may be read but cannot be an ancestor or contribute authority
  files. Delivery proves the authority implementation is absent path by path.

## Acceptance Criteria

- One remote candidate has sole parent `abb4210257...`, contains none of the
  rejected implementations as an ancestor, preserves P4 semantics, and contains
  no tournament authority implementation.
- The ordinary production and recovery entry points use the pure versioned
  evaluator result; all eight family sites are genuine, typed, sequence-bound,
  and fail closed before publication.
- Source fingerprint checks pass; the migration manifest and two clean-root
  generations prove the closed old/new identity and canonical-diff allowlist.
- Every failed rehearsal verifies and cleans under the coverage-status union.
- Real runtime suppression audits fully reconcile; static/fallback evidence
  fails adversarial tests.
- Two fresh rehearsal preflight roots are byte-identical; verification
  regenerates coverage/suppression evidence; P4 and rehearsal verifiers reject
  each other's roots.
- Full focused, Go, uninterrupted race, vet, build, module, browser, OBS,
  privacy/source/dependency, secret/generated-data, remote/PR, and clean-tree
  checks pass.
- Independent exact-SHA review and coordinator reproduction pass before a
  separate rehearsal issue is assigned to Paul.
- Manual rehearsal success records operational readiness and field delta only;
  it cannot complete `DOT-64`, open `DOT-62`, update `DOT-70`, or accept M4/P4.

## Verification

```sh
test "$(git rev-parse HEAD^)" = abb4210257f26364c2a539de90e386f411f3229a
git merge-base --is-ancestor fa5e7c308ee272722469499ae227b99d1644e2ac HEAD
git merge-base --is-ancestor 3fd8c3261556bfb56758eb93c16264cb4fda8fed HEAD && exit 1 || true
git diff --check abb4210257f26364c2a539de90e386f411f3229a..HEAD
go test -count=2 -timeout=12m ./cmd/m4-match ./cmd/dota2-ob ./internal/contracts ./internal/insight ./internal/m4match
go test -count=1 ./...
CGO_ENABLED=1 CC="zig cc" go test -race -timeout 30m -count=1 ./...
go vet ./...
go build ./...
go mod verify
(cd web/browser && npm ci --no-audit --no-fund && npm test)
(cd spikes/obs-overlay && npm ci --no-audit --no-fund && npm test)
git status --porcelain=v1 --untracked-files=all
```

The implementation issue adds exact rehearsal preflight/verify commands, runs
two fresh roots, compares canonical bytes, and proves cross-purpose rejection.
No live public match runs during implementation or review.

## Non-Goals

No tournament/TI authority implementation, P4 source widening, visible insight
or policy semantic change, live match during build/review, `DOT-70` update,
`DOT-64`/`DOT-62` transition, M4/P4 acceptance, merge/deploy, canonical-root
mutation, account or Dota UI automation, replay/demo/synthetic substitution,
hidden-state inference, memory read, packet capture, unofficial Game Coordinator
work, or M6 waiver.

## Review Focus

- Exact preservation of TI/P4 behavior and identity.
- Absence of authority code and acceptance side effects.
- Verifiability/cleanup of zero/partial/failed rehearsal outcomes.
- Runtime, not static, suppression evidence and no identity fallback.
- Truthful production-source fingerprints, bounded pure audit output, and a
  closed identity/golden migration with no semantic candidate drift.
- Value-free deterministic coverage and cross-purpose rejection.
- Whether the rehearsal exposes pre-TI operational defects without any path to
  acceptance.
