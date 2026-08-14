# M1 Historical No-Go Live-Only Scope Successor

Date: 2026-08-14

Status: proposed for independent exact-SHA review

Issues: `DOT-20`, `DOT-22`, `DOT-54`, `DOT-55`, `DOT-59`

## Objective

Convert the independently verified M1 `historical_no_go_candidate` into the
smallest explicit program scope that can close M1 without weakening identity,
source-safety, or numerical-readiness requirements.

## Context And Source Of Truth

- Governing program spec:
  `docs/specs/2026-08-12-ti-broadcast-analytics-program.md` at
  `2a0dabb60f5bc57adbc93d79c055ee80b1ccab3a`, especially the frozen program
  scope, M1, and protocols P0/P4. It already defines historical no-go as a
  release that becomes live-only unless a separately reviewed source/scope
  revision is accepted.
- Accepted M0 base:
  `35191fe95609df52d2b387e8a79ea8c982308cac`.
- Accepted M1 code foundation:
  `246a49825e2a7776c0673a2be448b23900a9d49e`.
- Exact code/evidence candidate:
  `77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b` on
  `agent/dota2-data-pipeline-engineer/dot-22-m1-history`, PR #12, with parent
  `9f3187c752575f371e3e04eb44342b2b66cb999f`.
- Producer handoff: `DOT-54` comment
  `9694526f-d565-41b7-a826-01ee531bde8a`.
- Independent no-blocker review: `DOT-55` comment
  `826516a1-0e41-491f-8095-360a196dc146`, recommendation
  `accept_historical_no_go_evidence_for_spec_successor`.
- `DOT-59` policy-lineage finding:
  `ba2f1a79-d9c4-400d-95b0-b0eb18633e76`. This successor closes that finding
  through the documentation-level versioned migration below; it does not change
  `PolicyLineageManifestV2` or claim that downstream implementation exists.
- Sealed evidence index SHA-256:
  `9d5f6b6e95941c2704f96f85e6e49f61679055648df60b49ccb5ff9c396b0dc4`.
- Sealed artifact-tree SHA-256:
  `ba6e7e168bba9117dbcb6804f99e9cb4f25482b7e5c6edad9adc740adbccc37a`.
- Sealed replay-gate audit SHA-256:
  `c6a40c002548ba80773c00fdfb126196e079e46cfdf34a99ffe8f7a67705bcb4`.
- Sealed source-provenance SHA-256:
  `e959e9ff1f7b723bd66641a069a7e610a900e326bc6694a5240c929635bad812`.

The coordinator independently reproduced a no-write resume and a new clean
materialization from the exact candidate. Both returned the index and artifact
tree identities above. Resume reported `created=0 reused=9`; the clean root
reported `created=9 reused=0`; both contained 9 files / 446,902 bytes and were
byte-identical.

## Verified Terminal Evidence

- The cutoff-effective scope contains exactly 16 teams and 80 players. The
  rejected legacy Nigma team ID `7554697` is absent.
- The exhaustive terminal population contains 1,254 matches:
  367 `excluded_patch_mismatch`, 780 `gate_target_not_selected`, 100
  `replay_identity_quarantined`, and 7 `replay_metadata_missing`.
- The deterministic sample contains 100 hash-verified replay objects. Every
  team has at least five selected matches. Two independent pinned manta v1.5.0
  runs each parsed 100/100 and produced byte-identical facts.
- All 100 normalization receipts are quarantined. The frozen public inputs do
  not provide an independently correlated public game build, while the
  accepted identity gate is conjunctive across build and all ten participant
  bindings. Consequently, 0/100 facts are identity-verified and the eligible
  draft/distribution/team-comparative counts are 0 against minima 5/8/10.
- OpenDota match/player metadata can expose `hero_id`; therefore the retained
  lack of hero-to-participant bindings is a frozen-query limitation, not proof
  that every public source lacks that field. This does not change the terminal
  result: adding hero IDs alone cannot satisfy the missing independent public
  game-build correlation.

## Versioned History-Availability Binding

`HistoryAvailabilityBindingV1` is the canonical, content-addressed union used by
the successor lineage and release contracts. Its JSON object is closed: UTF-8,
lexicographically sorted object keys, no duplicate or unknown fields, RFC 3339
UTC timestamps, and arrays in the orders required below. Its ID and SHA-256 are
the SHA-256 of those canonical bytes. Validation accepts exactly one `mode` tag
and exactly the fields for that tag:

1. `snapshot_baseline` binds one immutable `HistoricalSnapshotManifestV1` ID and
   SHA-256 plus the non-empty ordered eligible `HistoricalBaselineV1` content
   hashes. It preserves the existing snapshot/baseline semantics; none of the
   historical-no-go fields may be present.
2. `historical_no_go` binds all of the following, and no snapshot or baseline
   field may be present:
   - terminal outcome `historical_no_go_accepted_live_only`;
   - accepted code-foundation commit
     `246a49825e2a7776c0673a2be448b23900a9d49e` and accepted evidence commit
     `77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b`;
   - evidence-index SHA-256
     `9d5f6b6e95941c2704f96f85e6e49f61679055648df60b49ccb5ff9c396b0dc4`,
     artifact-tree SHA-256
     `ba6e7e168bba9117dbcb6804f99e9cb4f25482b7e5c6edad9adc740adbccc37a`,
     replay-gate-audit SHA-256
     `c6a40c002548ba80773c00fdfb126196e079e46cfdf34a99ffe8f7a67705bcb4`,
     and source-provenance SHA-256
     `e959e9ff1f7b723bd66641a069a7e610a900e326bc6694a5240c929635bad812`;
   - the complete byte-sorted disabled-family array `hero`, `item`, `lane`,
     `patch`, `player`, `player_hero`, `role`, `team`;
   - frozen `TournamentScopeV1` scope ID
     `da84de9204dc8493c07a0d4ba9fafcdec7e4481c01060f6bad5d7b954ef5f8f2`
     and file SHA-256
     `890c8517b6ae7859b6dd794aab486c27c2ce3b82f7c87e84884c7006825340bf`;
     cutoff `2026-08-12T00:00:00Z`, trailing-90 start
     `2026-05-14T00:00:00Z`, trailing-180 start
     `2026-02-13T00:00:00Z`, patch ID `60`, and Dota patch `7.41`.

Both or neither mode, an unknown tag or field, a missing or additional disabled
family, non-canonical bytes, any ID/hash/scope/window/patch mismatch, or any
history-dependent candidate/output under `historical_no_go` is a validation
failure. The system fails closed: it hides policy output and rejects operator
commands without interrupting raw capture. It never converts such a failure
into an empty, synthetic, nullable, or optional snapshot.

## Policy-Lineage Successor

`PolicyLineageManifestV2` remains byte- and behavior-unchanged. Snapshot releases
may retain it with its mandatory `HistoricalSnapshotManifestV1` and ordered
eligible-baseline bindings. Live-only sessions require a separately versioned
`PolicyLineageManifestV3`; making the V2 snapshot fields nullable or optional is
forbidden.

`PolicyLineageManifestV3` retains every non-history identity, size bound,
canonicalization rule, immutability rule, and durability rule required by V2,
but replaces V2's direct snapshot/baseline fields with one required
`HistoryAvailabilityBindingV1` ID and SHA-256. A live-only V3 lineage must use
the `historical_no_go` mode above. Before the first policy commit, the
application synchronously validates, seals, file-syncs, atomically renames,
parent-directory-syncs, and retains the canonical history binding and V3
manifest with the policy log.

The corresponding versioned `PolicyCommitV3` and `PolicyCheckpointV3` carry the
V3 manifest ID and SHA-256 on every frame. Validation, write, recovery, binding,
or manifest-identity mismatch hides output and rejects commands without
affecting capture. A different binding starts a new lineage. Recovery may load
only the exact retained V3 manifest and binding named by the log; it must never
search for or substitute a snapshot, baseline, evidence root, evidence hash,
scope/window/patch identity, or alternate configuration until state happens to
match.

This section authorizes the versioned migration needed for
`historical_no_go_accepted_live_only`; it does not implement it. A separately
assigned downstream contract/policy task must implement canonical encoding,
validation, durable sealing, commits/checkpoints, recovery, and adversarial
tests, then pass independent exact-SHA contract/code review before M4 or M6 may
run in live-only mode.

## P1 Fixture Reconciliation

P1's warmup, sample count, timing boundary, resource measurements, host record,
and M2 bounds remain unchanged. A snapshot release continues to use the frozen
complete ten-player update fixture with its bound baseline/policy state. A
live-only release instead uses a separately identified, immutable
`HistoricalUnavailableFixtureV1` bound to the exact historical-no-go
`HistoryAvailabilityBindingV1` and V3 lineage identities. It contains the same
complete ten-player live update and explicit typed-unavailable history state,
the complete disabled-family set, and no baseline values.

The live-only P1 run executes 10,000 untimed warmups and 100,000 measured
evaluations under the unchanged protocol and must emit zero history-dependent
candidates. The typed-unavailable fixture is not a `HistoricalBaselineV1`, and
an empty, zero-filled, synthetic, or quarantined-data baseline never satisfies
P1.

## Live-Only M4/M6 And Release Binding

M4 and M6 live-only execution requires a canonical, content-addressed
`LiveOnlyReleaseBindingV1`. It pins the accepted release source commit; exact
`PolicyLineageManifestV3` schema/build and sealed manifest identity; exact
historical-no-go `HistoryAvailabilityBindingV1` identity; both accepted commits,
all four sealed evidence hashes, frozen scope/window/patch identity, complete
disabled-family set, and the rule/config/catalog/translation identities used by
the release. The M6 release manifest pins this release-binding ID and SHA-256 in
place of asserting a historical snapshot identity.

M4 must prove deterministic typed-unavailable handling and suppression through
capture, insight, policy, localization, overlay, and audit. M6 must reproduce
the sealed V3 lineage and release binding, prove zero history-dependent
candidates, decisions, overlays, or audit claims, and pass the unchanged stale,
rollback, privacy, latency, resource, and operator-control gates. Any asserted
historical snapshot/baseline, identity mismatch, incomplete disabled set, or
history-dependent claim in live-only mode fails closed and blocks rehearsal or
release.

M1 acceptance specifies and permits this migration but does not self-attest its
implementation. Before any live-only rehearsal or release, the downstream
contract/policy implementation gate described above and an independent exact-SHA
review must be accepted; M4/M6 evidence must then bind that accepted
implementation and the exact `LiveOnlyReleaseBindingV1`.

## Scope Decision

If an independent reviewer accepts this successor, M1 closes with terminal
state `historical_no_go_accepted_live_only` at the exact evidence identity
above.

1. No `HistoricalBaselineV1` snapshot is published for this cutoff. An empty,
   synthetic, fixture-derived, partially verified, or zero-filled snapshot is
   forbidden.
2. Historical families `hero`, `item`, `lane`, `patch`, `player`,
   `player_hero`, `role`, and `team` are disabled for all 16 teams. No
   history-dependent insight candidate may be emitted.
3. The live program continues only with source-faithful live-observation rules.
   Missing history is an explicit typed unavailable/suppressed condition, not
   a zero value, low-confidence baseline, or operator-overridable warning.
4. M4 must use the accepted V3 lineage and `LiveOnlyReleaseBindingV1`, exercise
   the complete unavailable-history path, and prove deterministic suppression
   while the live-only capture, insight, policy, localization, overlay, and
   audit path continues safely.
5. M5 historical corpus completion, replay-to-GSI validation, and
   history-dependent editorial calibration remain parked. Live-only editorial
   calibration may proceed only where it has no dependency on a historical
   snapshot.
6. M6 rehearsals and the release manifest pin the accepted V3 lineage and
   `LiveOnlyReleaseBindingV1`, including all accepted no-go identities. They must
   prove zero history-dependent claims, fail closed if a historical snapshot is
   asserted, and pass the existing missing-history, stale, rollback, privacy,
   latency, resource, and operator-control gates.
7. Re-enabling any historical family requires a separate source/scope
   successor that safely binds all ten public participant identities and an
   independently correlated replay game build, reruns normalization and the
   complete readiness arithmetic, meets the unchanged 5/8/10 family minima,
   and receives independent exact-SHA plus data review.

## Source And Safety Boundary

The existing source order and safety gate remain unchanged: official/public
Steam and tournament metadata first, OpenDota public metadata second, and Valve
public replay CDN bytes only after identity validation. This successor does not
authorize credentials, cookies, tokens, GC login, replay-salt automation, Dota
UI/account automation, packet or memory access, hidden-state live use,
fabricated identity, or silent threshold/source relaxation.

## Non-Goals

- No claim that OpenDota lacks `hero_id` or that the current query exhausts
  every possible safe metadata field.
- No new provider, source adapter, replay download, parser run, aggregate,
  database, live-capture change, policy rule, UI/OBS change, merge, deployment,
  or evidence cleanup. This document specifies only the minimum versioned
  history-binding, lineage, commit/checkpoint, fixture, and release-binding
  migration; it does not edit or implement contract or policy code.
- No revision of the frozen 16-team cutoff scope, patch/window bounds, 100-
  replay target, per-team minimum, or 5/8/10 family minima.
- No permission to publish the quarantined facts or use replay-derived hidden
  information during live output.

## Acceptance Criteria

- An independent reviewer verifies the exact successor SHA, its direct parent
  `489583326e98e80c7beddddeffe17337c05b8ab9`, PR-head equality, and that the
  successor changes only this scope decision.
- The reviewer reproduces or validates the sealed identities and terminal
  arithmetic cited above and confirms that the missing public build correlation
  is sufficient under the unchanged conjunctive identity contract.
- The reviewer confirms that live-only operation, downstream unavailable-state
  handling, and the re-enable gate are explicit and introduce no fabricated
  baseline, unsafe source, hidden-state use, or silent contract weakening.
- The reviewer confirms the tagged binding, V3 lineage/commit/checkpoint
  migration, P1 fixture split, and live-only release binding are canonical,
  versioned, fail closed, and explicitly gated on later implementation and
  independent review.
- The reviewer ends with either `accept_historical_no_go_live_only_scope` or
  `reject_with_blockers` and distinguishes blockers from residual limitations.
- G胖 records the accepted successor SHA, reviewer comment, coordinator
  reproduction, evidence identities, disabled families, residual limitations,
  and blocker count before closing `DOT-22`.

## Verification

```sh
git rev-parse HEAD^
git diff --check 489583326e98e80c7beddddeffe17337c05b8ab9..HEAD
git diff --name-only 489583326e98e80c7beddddeffe17337c05b8ab9..HEAD
git merge-base --is-ancestor 489583326e98e80c7beddddeffe17337c05b8ab9 HEAD
go test -count=1 ./...
go vet ./...
go build ./...
go mod verify
```

Review focus: exact evidence binding, typed live-only/unavailable behavior,
downstream M4-M6 consistency, unchanged identity/minimum/source gates, and the
absence of any path that treats quarantined data as publishable.
