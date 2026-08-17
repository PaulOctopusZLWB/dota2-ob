# TI 2026 group-stage replay archive and integrity plan

Date: 2026-08-17

Issue: DOT-72

## Outcome

Freeze archive completeness against 109 expected game slots, not against discovered URLs or downloaded file count. A game slot is stable before its Dota match ID is known: `ti2026:<official-series-id>:game-<n>`. The provider's series ID never substitutes for the Dota match ID.

Current local-corpus baseline:

- expected game slots: 109;
- completed official series: 44 (39 Swiss + 5 elimination);
- candidate Dota match ID/salt rows in local `matches.json`: 109;
- downloaded archive/demo pairs passing container integrity: 109;
- identity-verified replay files: 0 in DOT-72;
- byte-presence coverage: `109 / 109 = 100%`;
- identity-verified analytical coverage: `0 / 109 = 0%` until content gates run.

Paul supplied this fresh local TI corpus after the original research baseline was written. Candidate IDs and valid containers establish availability, not authenticity or schedule-slot identity. No file becomes analytically publishable until replay match/build/time, teams, all ten participants/heroes, parser completion, and deterministic facts pass.

The immutable official-schedule denominator is materialized in `ti2026-group-stage-schedule-freeze-v1.json`. It contains 44 sanitized series records and 109 unique `game_slot_id` values, with the original source-time `missing/dota_match_id_not_frozen` state. It intentionally contains no replay URL, credential, logo asset, or fabricated Dota match ID. The runtime replay manifest joins current candidate IDs to those slots with separate evidence and never rewrites the frozen source snapshot.

## Source hierarchy

1. Official Perfect World tournament schedule/result metadata and public Steam/Dota match metadata.
2. A public replay address supplied by approved metadata, subject to the accepted DOT-29 URL and transport boundary.
3. A replay manually downloaded by Paul in the Dota 2 client and imported into a watched local inbox.

Forbidden sources/actions:

- credentials, cookies, Steam session tokens, Game Coordinator login or automation;
- replay-salt guessing or automated derivation outside approved public metadata;
- Dota UI or account automation;
- packet capture, memory access, injection, protocol bypass, or fabricated replay URLs.

An approved public source may be absent. The correct state is `missing`, not an invented URL.

## Two identities

### Schedule-slot identity

The schedule slot is derived from immutable source evidence:

```text
game_slot_id = "ti2026:" + official_series_id + ":game-" + game_number
```

Required slot facts:

- official series ID, scheduled/result time, team source IDs/names;
- series score and game number (`1..home_score+away_score`);
- tournament phase (`swiss` or `elimination`);
- source URL, retrieval time, raw response SHA-256, and response-owned timestamp.

### Replay identity

A replay becomes `verified` only when all of these agree:

1. compressed and decompressed content SHA-256 plus byte sizes are recorded;
2. decompressed bytes begin with `PBDEMS2\x00`;
3. parsed Dota match ID equals the corroborated public Dota match ID;
4. parsed build and match/event time agree with independent public metadata under a documented tolerance;
5. radiant/dire teams agree;
6. all ten player slots and hero bindings agree one-to-one;
7. the pinned parser reaches end-of-demo without error and emits a deterministic fact hash on a second clean execution.

SHA-256 proves byte identity, not authenticity. HTTP is an offline-history-only exception for the exact accepted Valve replay-CDN shape; HTTPS remains preferred. Any failed conjunct quarantines the file.

## State machine

Non-terminal workflow states:

```text
expected -> metadata_discovered -> acquiring -> downloaded -> validating
```

Allowed terminal states:

| State | Meaning |
|---|---|
| `verified` | Every replay identity conjunct passed and A/B facts match. |
| `missing` | No approved replay bytes were available by the current archive cutoff, or the Dota match ID is unresolved. Reason code is mandatory. |
| `corrupt` | Bytes are truncated, decompression fails, magic is wrong, or content mutates. |
| `identity_mismatch` | Bytes parse, but match/build/time/team/player/hero correlation fails. |
| `parse_failed` | Identity prerequisites passed far enough to select the file, but the pinned parser fails to complete or to reproduce deterministic facts. |
| `superseded` | A correction/replay replacement owns the active slot. The successor record ID and reason are mandatory. |

`discovered`, `downloaded`, and `parsed` are never completeness states.

Terminal does not mean immutable truth. New approved evidence creates a new manifest version and history entry. It may supersede a prior `missing` or bad file; the prior record remains auditable.

## Required reason codes

At minimum:

- `dota_match_id_not_frozen`
- `approved_metadata_absent`
- `public_replay_unavailable`
- `manual_import_not_received`
- `download_http_status`
- `download_size_limit`
- `decompression_failed`
- `source2_magic_mismatch`
- `match_id_mismatch`
- `game_build_mismatch`
- `event_time_mismatch`
- `team_identity_mismatch`
- `participant_identity_mismatch`
- `hero_binding_mismatch`
- `parser_error`
- `facts_nondeterministic`
- `schedule_correction`
- `replay_replacement`

Free-form notes supplement a reason code; they never replace it.

## Recovery and tournament-time SLA

Because the group stage ended before this specification was written, validate the supplied corpus first and use recovery only for proven gaps:

1. Load the 109 slot identities from `ti2026-group-stage-schedule-freeze-v1.json` and create one manifest record per slot.
2. Treat the 109 local `matches.json` rows as candidate Dota match IDs; map each to exactly one official slot using approved public metadata.
3. Verify each existing archive/demo pair and run the full replay identity/parser gate before assigning a terminal state.
4. Emit a human-readable quarantine/gap list after the first pass.
5. Reacquire only corrupt, mismatched, or missing bytes through an approved public/client path, retaining old/new hashes and source history.
6. Seal manifest version 1 only when all 109 slots have an allowed terminal state. Later corrections create successor versions.

For future tournament days:

| Deadline from map completion | Required result |
|---|---|
| 15 min | slot exists; teams/game number/result source frozen |
| 45 min | Dota match ID corroborated or explicit unresolved reason |
| 2 h | approved public acquisition terminal; manual-import alert emitted for gaps |
| 6 h | imported files validated; missing/corrupt/mismatch/parser failures visible |
| daily 02:00 local | schedule denominator reconciled; coverage report and gap list sealed |
| 72 h after phase end | every slot terminal; manifest version sealed |

The alert may open a local dashboard or write a report. It must not drive the Dota client.

## Manual import contract

Paul performs the client download. The importer receives a copied file plus a small sidecar:

```json
{
  "game_slot_id": "ti2026:3367881:game-1",
  "claimed_dota_match_id": "",
  "downloaded_by": "manual_dota_client",
  "copied_at": "2026-08-17T00:00:00Z",
  "notes": ""
}
```

The claim remains untrusted. Import computes hashes/sizes, preserves the original filename as metadata, moves bytes to content-addressed storage, and runs the same identity gate as a public download. Duplicate bytes reuse the content object but retain both provenance receipts.

## Storage layout

Raw/generated data remains outside git under a mode-`0700` root:

```text
ti2026-replays/
  manifests/<manifest-sha256>.json
  sources/<source-response-sha256>.json
  compressed/sha256/<first2>/<sha256>
  demos/sha256/<first2>/<sha256>.dem
  facts/<schema-version>/<replay-sha256>.json
  receipts/<game-slot-id>/<receipt-sha256>.json
  quarantine/<reason-code>/<replay-sha256>.json
  reports/<manifest-sha256>.md
```

The decompressed demo is a rebuildable cache; compressed source bytes and provenance receipts are retained. Never put secrets or raw player data in issue metadata.

DOT-54 measured 100 objects at 4,734,122,001 compressed bytes and 7,730,318,570 decompressed bytes. Use those as planning evidence only:

- mean compressed: about 47.3 MB/game;
- mean decompressed: about 77.3 MB/game;
- 109 games at that mean: about 5.16 GB compressed and 8.43 GB decompressed, before facts/receipts and safety margin.

Real TI size is reported from actual objects; the estimate is not a quota.

## Completeness arithmetic

For manifest `M`:

```text
expected(M) = 109
terminal(M) = verified + missing + corrupt + identity_mismatch + parse_failed + superseded
resolved_rate(M) = terminal(M) / expected(M)
verified_coverage(M) = verified(M) / expected(M)
```

Archive completeness requires:

- exactly 109 unique active slot identities;
- no duplicate `(series_id, game_number)`;
- `terminal(M) == 109`;
- every `superseded` record names a valid active successor;
- every file identity is unique or explicitly deduplicated;
- coverage counts are derived from records, never manually entered.

`resolved_rate == 100%` may coexist with poor verified coverage. Reports must show both.

## Schedule corrections, remakes, and replays

- A changed opponent, score, or game count creates a new schedule snapshot and manifest version.
- A remake/abandoned map remains its own game slot if the official result source counts it; otherwise preserve it as superseded evidence outside the active denominator.
- A replayed map receives a new slot or successor relationship according to the official competition ruling. Never overwrite the first bytes.
- A replacement replay for the same Dota match ID supersedes the prior record only after both identities and provenance are retained.
- Team aliases never define identity; stable source/team IDs and effective-time rosters do.

## Verification requirements for implementation

- Schema validation against `ti2026-replay-manifest-v1.schema.json`.
- Recompute 44 series / 109 slots from the frozen official response.
- Reject provider series IDs in `dota_match_id` (including seven-digit examples).
- Test every state transition and reason code.
- Test duplicate slot IDs, duplicate Dota match IDs, duplicate bytes, corrected schedules, remakes, supersession cycles, and content replacement.
- Test PBDEMS2, truncation, decompression failure, build/time/team/player/hero mismatch, parser failure, and nondeterministic facts.
- Run two clean roots and one resume; compare manifest/fact/tree hashes and prove zero duplicate effects.
- Prove no tracked replay/generated data with `git status --short` and repository ignore tests.

## Acceptance handoff

The first implementation report must state the exact manifest SHA-256 and this tuple:

```text
expected=109 verified=? missing=? corrupt=? identity_mismatch=? parse_failed=? superseded=? terminal=?
```

No other summary is an archive-completeness claim.
