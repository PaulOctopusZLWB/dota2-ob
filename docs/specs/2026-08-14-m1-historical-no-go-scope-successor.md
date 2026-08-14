# M1 Historical No-Go Live-Only Scope Successor

Date: 2026-08-14

Status: proposed for independent exact-SHA review

Issues: `DOT-20`, `DOT-22`, `DOT-54`, `DOT-55`

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
4. M4 must exercise the complete missing-baseline/unavailable-history path and
   prove deterministic suppression while the live-only capture, insight,
   policy, localization, overlay, and audit path continues safely.
5. M5 historical corpus completion, replay-to-GSI validation, and
   history-dependent editorial calibration remain parked. Live-only editorial
   calibration may proceed only where it has no dependency on a historical
   snapshot.
6. M6 rehearsals and the release manifest pin this accepted no-go evidence
   index and artifact-tree identity in place of a historical snapshot identity.
   They must prove zero history-dependent claims and pass the existing missing-
   history, stale, rollback, privacy, latency, and operator-control gates.
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
- No new provider, source adapter, replay download, parser run, contract schema,
  aggregate, database, live-capture change, policy rule, UI/OBS change, merge,
  deployment, or evidence cleanup.
- No revision of the frozen 16-team cutoff scope, patch/window bounds, 100-
  replay target, per-team minimum, or 5/8/10 family minima.
- No permission to publish the quarantined facts or use replay-derived hidden
  information during live output.

## Acceptance Criteria

- An independent reviewer verifies the exact successor SHA, its direct ancestry
  from `77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b`, PR-head equality, and that the
  successor changes only this scope decision.
- The reviewer reproduces or validates the sealed identities and terminal
  arithmetic cited above and confirms that the missing public build correlation
  is sufficient under the unchanged conjunctive identity contract.
- The reviewer confirms that live-only operation, downstream unavailable-state
  handling, and the re-enable gate are explicit and introduce no fabricated
  baseline, unsafe source, hidden-state use, or silent contract weakening.
- The reviewer ends with either `accept_historical_no_go_live_only_scope` or
  `reject_with_blockers` and distinguishes blockers from residual limitations.
- G胖 records the accepted successor SHA, reviewer comment, coordinator
  reproduction, evidence identities, disabled families, residual limitations,
  and blocker count before closing `DOT-22`.

## Verification

```sh
git diff --check 77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b..HEAD
git diff --name-only 77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b..HEAD
git merge-base --is-ancestor 77741f940f6b1b2c2cdb68e2c08cfb6491d0b38b HEAD
go test -count=1 ./...
go vet ./...
go build ./...
go mod verify
```

Review focus: exact evidence binding, typed live-only/unavailable behavior,
downstream M4-M6 consistency, unchanged identity/minimum/source gates, and the
absence of any path that treats quarantined data as publishable.
