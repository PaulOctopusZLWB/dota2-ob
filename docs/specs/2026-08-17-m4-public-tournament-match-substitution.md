# M4 Public Tournament DotaTV Match Substitution

Date: 2026-08-17

Decision owner: Paul

Status: candidate amendment pending independent review

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

Paul authorizes an ordinary match only as an M4 substitute. Engineering narrows
that phrase to a live public professional or tournament match available through
DotaTV. A ranked public match, private lobby, bot game, replay playback, demo,
synthetic POST stream, or prerecorded broadcast is useful as a dry run but
cannot pass M4.

## Source Of Truth

- Program spec predecessor: `docs/specs/2026-08-12-ti-broadcast-analytics-program.md`
  at `271cc47d503828528b7c69212deb4d22683cb715`.
- Accepted functional candidate: `fa5e7c308ee272722469499ae227b99d1644e2ac`.
- Accepted harness candidate: `abb4210257f26364c2a539de90e386f411f3229a`.
- Harness review: `DOT-63` comment `92a4f970-c8cd-4681-a526-d6a7fa83dae9`.
- Existing single manual handoff: `DOT-70`; it remains closed and must be reused.
- Valve event announcement: `https://steamcommunity.com/app/570/announcements/?l=english`.

## Requirements

### Qualifying match

- The game is live in the public DotaTV client and belongs to a named
  professional league or tournament.
- Before arming, bind a stable nonzero Valve match ID, competition/league,
  series/game, both public teams, and the expected start window.
- Corroborate the in-client listing and GSI match ID with Valve-provided league
  metadata or an official tournament-organizer schedule. Retain source class,
  sanitized endpoint/URL, retrieval time, response/page SHA-256, pagination when
  relevant, and any conflict. Never retain a Steam Web API key.
- Community indexes may discover a candidate but cannot be the sole authority or
  independently corroborate a fact they derived from Valve.
- Arm before game clock `0:00`, run continuously through normal post-game and
  recording finalization, and reject every late join, remake, abandon, identity
  contradiction, missing boundary, evidence gap, or unsafe output exactly as in
  accepted P4.

### Narrow harness successor

- Use exact `abb4210257f26364c2a539de90e386f411f3229a` as the sole parent.
- Bind the accepted commit of this amendment in the harness identity. Do not use
  a floating branch, environment override, or runtime waiver.
- Replace the TI-only identity/source validator with one closed typed
  classification for `ti` and `public_tournament`. A public-tournament identity
  must carry the qualifying evidence above. Reject missing, unknown, duplicate,
  conflicting, private/local, credential-bearing, or non-HTTPS sources.
- Replace only TI-specific runbook and human-instruction wording. Keep the same
  four manual actions and the same at-most-30-minute pregame window.
- Preserve every accepted candidate/parent/remote/PR/binary/environment/process
  identity check, exclusive capture binding, raw/projection/policy/audit/operator/
  overlay reconciliation, five-second measurement, 100 ms visibility trace,
  resource bound, fault matrix, recovery, byte comparison, privacy scan,
  confinement, cleanup, and non-resumable failure rule.
- Rerun two fresh deterministic preflight roots against the successor and exact
  current environment. Prior readiness hashes do not authorize the successor.
- Update the existing `DOT-70` only after independent exact-SHA review and a new
  exact readiness result pass. Never create another manual checkpoint issue.

### TI release boundary

- A non-TI P4 pass closes only M4 live systems validation.
- At least one of the three M6 dress rehearsals must use a complete official TI
  DotaTV match on the exact release candidate. The non-TI M4 evidence cannot be
  relabeled or reused to satisfy that requirement.

## Acceptance Criteria

- Independent spec review reports no blocking ambiguity or gate weakening on one
  immutable remote commit.
- The implementation successor changes only the typed identity/source boundary,
  bound spec identity, directly dependent goldens/tests, and operator/runbook
  wording required by this amendment.
- Adversarial tests reject arbitrary matchmaking, replay/demo input, synthetic
  feeds, unsafe URLs, credentials, unknown source classes, missing hashes/times,
  metadata conflict, match-ID drift, and TI/non-TI substitution.
- The complete accepted DOT-65 verification matrix, dual-root canonical byte
  comparison, uninterrupted full race run, browser/OBS suites, privacy/source
  scans, remote/PR equality, and clean tree pass at the successor SHA.
- Independent code review and coordinator reproduction pass before `DOT-64` or
  `DOT-70` is reopened. No live match is attempted during spec or code review.

## Verification

```sh
git diff --check 271cc47d503828528b7c69212deb4d22683cb715..HEAD
git diff --name-status 271cc47d503828528b7c69212deb4d22683cb715..HEAD
rg -n "public_tournament|official TI|DOT-70|abb4210257" docs/specs
git status --short
```

The implementation issue must additionally run every verification command and
fresh-root comparison required by accepted `DOT-65`.

## Non-Goals

- No arbitrary public matchmaking or private lobby as M4 acceptance.
- No replay/demo playback, synthetic feed, hidden-state source, account/Dota UI
  automation, memory read, packet capture, or unofficial coordinator work.
- No threshold, queue, duration, identity, privacy, evidence, recovery, or
  cleanup relaxation.
- No merge, deployment, canonical-root mutation, M4 acceptance, M5 promotion,
  or TI-specific M6 waiver.

## Review Focus

- Whether `public_tournament` is closed enough to prevent a random match or
  fabricated organizer page from entering the gate.
- Whether source provenance remains useful without retaining API credentials or
  account identifiers.
- Whether the successor scope preserves accepted harness behavior and forces all
  readiness evidence to be regenerated.
- Whether M4 substitution and TI-specific M6 release acceptance remain
  unambiguously separate.
