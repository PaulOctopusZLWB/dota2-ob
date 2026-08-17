# M4 Public-Match Rehearsal Extraction

Date: 2026-08-17

Decision owner: Paul

Status: candidate spec pending independent review

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
- Latest review: `DOT-82` comment
  `94e539fe-4d90-4fc1-bbe7-b95216662217`.
- Existing P4 handoff `DOT-70` stays closed and is not used for rehearsal.
- Captured-schedule baseline SHA-256:
  `2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e`.

The rejected candidates are read-only design evidence. A rehearsal successor
starts again from exact `abb4210257...` and contains none of their tournament-
authority implementation.

## Product Boundary

- Add exactly `public_match_rehearsal + public_match`.
- Preserve predecessor TI-only P4 command, schemas, verifier, behavior,
  instruction, `AcceptedP4Spec = 271cc47d503828528b7c69212deb4d22683cb715`,
  and every accepted P4 golden unchanged.
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
  history-dependent production branch evaluated during rehearsal receives typed
  unavailable input and emits one linked canonical suppression audit.
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
- Allowed files are `cmd/m4-match`, `internal/m4match`, one rehearsal runbook,
  directly required suppression/audit adapter wiring, and dependent tests/
  goldens. Every outside path needs exact requirement mapping.
- Allowed behavior is rehearsal parsing, distinct schemas/verifier/root/cleanup,
  runtime suppression audit, value-free coverage, wording, and tests.
- Excluded behavior is tournament authority/root/retrieval, P4 identity/source,
  insight calculations, thresholds, queues, raw capture, projection, delivery,
  rendering, OBS behavior, unrelated persistence, and normal user profiles.
- `3fd8c326...` may be read but cannot be an ancestor or contribute authority
  files. Delivery proves the authority implementation is absent path by path.

## Acceptance Criteria

- One remote candidate has sole parent `abb4210257...`, preserves P4 identity/
  behavior/goldens, and contains no tournament authority implementation.
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
go test -count=2 -timeout=12m ./cmd/m4-match ./internal/m4match
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

No tournament/TI authority implementation, P4 source widening, live match
during build/review, `DOT-70` update, `DOT-64`/`DOT-62` transition, M4/P4
acceptance, merge/deploy, canonical-root mutation, account or Dota UI
automation, replay/demo/synthetic substitution, hidden-state inference, memory
read, packet capture, unofficial Game Coordinator work, or M6 waiver.

## Review Focus

- Exact preservation of TI/P4 behavior and identity.
- Absence of authority code and acceptance side effects.
- Verifiability/cleanup of zero/partial/failed rehearsal outcomes.
- Runtime, not static, suppression evidence and no identity fallback.
- Value-free deterministic coverage and cross-purpose rejection.
- Whether the rehearsal exposes pre-TI operational defects without any path to
  acceptance.
