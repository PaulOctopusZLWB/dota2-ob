# Replay Acquisition, Parser, And Storage Spike

Date: 2026-08-12

Decision owner: Paul

Status: M0 feasibility evidence (proposed); not an accepted production contract.

Issue: DOT-29

Parent: DOT-21 (M0: freeze contracts and prove replay/OBS feasibility)

## Objective

Produce reproducible evidence that current-patch professional Dota 2 replays
can be safely acquired, parsed, normalized into deterministic facts, batched
resumably, and stored on PaulPC4090, and recommend the smallest storage design
that the measurements justify.

## Context

The M0 program gate requires a replay spike that parses representative
current-patch professional `.dem` files, reports available facts and parser
failure modes, and benchmarks CPU, memory, output size, and deterministic
repeatability, plus an acquisition path and storage decision that introduce no
GC credential or account automation.

Source of truth:

- Program spec: `docs/specs/2026-08-12-ti-broadcast-analytics-program.md` (M0)
- Acquired baseline boundary: `docs/specs/2026-08-05-mvp3-manual-capture-and-source-boundaries.md` (Replay Validation Boundary, `internal/replay`)
- Safety gate: `docs/safety_and_account_risk.md`
- Code: `internal/replay/`, `cmd/replay-spike/`
- Acceptance issue: DOT-29

## Source Of Truth (this spike)

- Two real current-patch professional replays downloaded from Valve's public
  replay CDN, discovered through the OpenDota public metadata API:

  | match_id | league | gameplay patch | replay format ver | duration | compressed bytes | decompressed bytes |
  |---|---|---|---|---|---|---|
  | 8941092540 | EPL Masters 2026 (professional) | 60 / Dota 7.41 | 22 | 3297s | 79,166,058 | 134,628,308 |
  | 8940891805 | EPL Masters 2026 (professional) | 60 / Dota 7.41 | 22 | 2807s | 65,070,664 | 112,605,718 |

  Provenance note: OpenDota metadata exposes two distinct version fields.
  `patch` (id 60) is the gameplay patch, resolved through the OpenDota patch
  catalog (`/api/constants/patch`) to Dota **7.41** (released 2026-03-24).
  `version` (22) is the per-replay demo protocol/format revision, **not** the
  gameplay patch. Raw `.dem`/`.dem.bz2` files stay under `data/replays/`
  (git-ignored) and are never committed or attached; only the measurements and
  their content hashes are reported here.

- OpenDota match metadata API: `https://api.opendota.com/api/matches/<matchID>`
  returns `replay_url`, `cluster`, `version`, league tier, team names, duration,
  and player count. No credentials and no GC interaction.
- Valve public replay CDN: `http://replay<cluster>.valve.net/570/<matchID>_<salt>.dem.bz2`.

## Acquisition Path Inventory

| # | Path | Provenance | Credentials | Rate limit | Retention | Account risk |
|---|---|---|---|---|---|---|
| 1 | Steam Web API (`GetMatchDetails`/`GetMatchHistory`) | official public metadata | optional Steam API key injected from environment, never committed | rate-limited | indefinite metadata | none (read-only) |
| 2 | OpenDota public API (`api.opendota.com`) | community mirror of pro metadata | none | best-effort, may purge | metadata may be purged; `replay_url` may expire | none (read-only) |
| 3 | Valve public replay CDN `replay<cluster>.valve.net/570/<id>_<salt>.dem.bz2` | official replay bytes | none | none observed | limited window (weeks to months), then 404/expired | none |
| 4 | Manual Dota 2 client replay download to `~/.local/share/Steam/steamapps/common/dota 2 beta/game/dota/replays/` | local client | none beyond the logged-in client (manual) | none | local until deleted | none (no automation) |
| 5 | Already-local `.dem` files placed under `data/replays/` | local | none | none | local until deleted | none |

The spike uses path 2 (discovery + `replay_url`) plus path 3 (download) for the
evidence; paths 4 and 5 are supported by the content-addressed layout. The
`acquire` subcommand performs exactly this and writes a provenance record.

### Critical compression finding

Files served as `<id>_<salt>.dem.bz2` are **not bzip2**. They begin with the
zstd magic `28 b5 2f fd` and decompress correctly with zstd only. Valve changed
the replay container compression while keeping the historical `.bz2` URL suffix.
The spike decompresses with a pure-Go zstd decoder
(`github.com/klauspost/compress/zstd`) and records `"compression": "zstd (file
suffix .bz2 is historical)"` in every acquisition record.

### Replay "salt" is a URL component, not a decryption salt

The numeric token in the replay URL is a public file-name component returned by
OpenDota/Steam metadata. Current Source 2 demos are not encrypted; the obsolete
replay-decryption salts do not apply. The spike uses no Game Coordinator login,
no Steam password, no cookies, no replay-salt automation, no Dota UI automation,
and no packet or memory access. This stays inside the accepted safety boundary.

### Untrusted-input boundary (acquisition hardening)

The acquisition surface treats OpenDota metadata and the Valve CDN as
untrusted input and defends it explicitly (reviewed hardening):

- `matchID` is validated as numeric and length-bounded so it cannot carry path
  separators or escape the `data/replays/<matchID>.dem` filename slot.
- HTTPS is preferred when an approved endpoint works. The two measured replay
  objects are available from plain HTTP while their equivalent HTTPS endpoint
  fails TLS negotiation, so this spike permits a narrow offline-history-only
  HTTP exception. The initial URL, every redirect, and final response must use
  one unchanged scheme and match exactly `replay<digits>.valve.net` plus
  `/570/<requested-match-id>_<numeric-salt>.dem.bz2`. Embedded credentials,
  explicit/nonstandard ports, queries, fragments, malformed paths, mismatched
  match IDs, other hosts, and HTTP/HTTPS cross-policy redirects are rejected.
  Redirects remain capped at five.
- HTTP provides no authenticated transport. An on-path party can substitute
  bytes, and SHA-256 establishes content identity only—not source authenticity
  and not a replacement for TLS. Acquisition records therefore retain
  `transport_scheme`, `unauthenticated_transport`, `source_quality`, and an
  explicit pending `identity_correlation` status.
- All HTTP calls go through one bounded client (90s timeout) with bounded bodies:
  metadata/patch-catalog ≤ 4 MiB, download ≤ 1 GiB compressed,
  decompression ≤ 4 GiB decompressed. A zstd bomb or oversized response is
  rejected by the limit, not by disk exhaustion.
- Download and decompression stream to randomized exclusive adjacent temporary files (never the final
  path), validate magic (zstd `28 b5 2f fd`, then `PBDEMS2`) and content
  SHA-256, fsync/close, and only then atomically rename over the canonical path
  and fsync its parent directory. Acquisition records use the same protocol.
  Any failure removes the temp file so a short/oversized/rewind body never looks
  canonical before rename. File-sync, close, and rename errors preserve the
  prior canonical path. A directory-sync error occurs after rename and is
  instead returned as typed `commit outcome uncertain`: the canonical name may
  contain the complete old or complete intended payload across a crash. Each
  caller reconciles the current canonical file against exact expected bytes or
  magic+size+full SHA-256, treats uncertainty as failure, and permits an
  idempotent retry to converge. No rollback/preservation claim is made for this
  post-rename path, and truncated/invalid content is never accepted.
- HTTP-acquired bytes remain untrusted after download. They require bounded
  zstd decompression, zstd and `PBDEMS2` magic checks, full-content SHA-256,
  successful parsing, and requested-match/build/time correlation with public
  source metadata before downstream use. This spike records the correlation as
  pending; M1 must suppress/quarantine any object that cannot be reconciled.
  The exception adds no credential, GC/account automation, hidden live state,
  or broadcast-path network dependency.
- This is testable without network via `cmd/replay-spike/main_test.go` and
  `internal/atomicfile/atomicfile_test.go` (numeric match ID, redirect
  allowlist/count, validate-before-replace, atomic acquisition record, and
  injected file-sync/rename/directory-sync failures).

## Parser Evaluation

| Parser | Language | License | Maintained | Source 2 / Dota 7.41 | Choice |
|---|---|---|---|---|---|
| `dotabuff/manta` v1.5.0 | Go | MIT | pushed 2026-07-01, not archived | yes (verified) | **selected** |
| `skadistats/clarity` | Java | BSD-2 | yes | yes | unavailable (no JVM on PaulPC4090) |
| `odota/parser` | Java | MIT | yes | yes | unavailable (no JVM) |
| `Rupas1k/source2-demo` | Rust | MIT | active | yes | unavailable (no Rust toolchain) |
| `odota/rapier` | JS | MIT | stale (2017) | partial | not selected |

`dotabuff/manta` is selected because it is Go-native (matches this codebase),
MIT-licensed, actively maintained by Dotabuff, and integrates behind the
`internal/replay` port named by the accepted boundary spec. Its regenerated
protobuf bindings are committed in the module, so `go get` is self-contained.

### Recorded parser limitations (feed into M1)

1. **String-table update integration is incomplete.** manta explicitly marks
   `onCDemoStringTables` and `onCSVCMsg_UpdateStringTable` as TODO/incomplete.
   As a consequence some combat-log name indices that are populated through
   string-table *updates* (rather than the initial `CreateStringTable`) resolve
   to the `"dota_unknown"` sentinel. Observed affected slots: first-blood
   killer, building-kill attacker, and the gold/XP actor. Resolved correctly:
   hero names in death/kill events, item names in item-use events, building
   names in building-kill events, and purchase-ledger purchasers.
2. **Per-hero gold/XP attribution and named item purchases require entity
   state.** The combat log records gold/XP totals and a per-hero purchase gold
   ledger, but the gold/XP actor is not a hero name, and purchase entries carry
   a gold value rather than an item name. Named item purchases and per-hero
   gold/XP attribution need entity-inventory and player-to-hero mapping.
3. **Draft picks/bans, positions, wards, and net-worth timeline need entity
   reconstruction**, which is deliberately deferred to M1.

These are fact-completeness bounds, not parse failures: manta parses both
current-patch demos to completion with no error.

## Normalized Facts (`ReplayFactsV1`, provisional)

`internal/replay/facts.go` defines a deterministic fact set derived purely from
the demo bytes (no acquisition metadata), so identical bytes produce identical
JSON and an identical SHA-256. This is provisional M0 evidence and is **not** the
accepted `HistoricalBaselineV1` contract, which is owned by a separate contract
track.

Fact families available this spike:

- match header (`PBDEMS2`, game build, server name, last tick, combat-log clock)
- combat-log type histogram (19-20 types observed)
- hero deaths and hero-vs-hero kills (10 heroes per match)
- item usage by name (64 and 56 distinct items)
- per-hero purchase gold ledger
- building kills by building name (tower tier/lane)
- first-blood timestamp, killstreak, multikill
- message-type counts (CDemoPacket, CDemoFullPacket, CNETMsg_Tick, PacketEntities, Source1LegacyGameEvent)

Deferred to M1:

- named item purchases, per-hero gold/XP attribution
- first-blood/building-kill actor resolution (parser string-table path)
- per-tick positions, ward entity coordinates, exact net-worth timeline
- draft picks/bans, gold/xpm checkpoint series, full entity-state reconstruction

Unavailable inside the safety boundary:

- replay salts / GC credentials, hidden fog-of-war state

## Determinism And Performance Evidence

Both replays were parsed twice; the second pass produced a byte-identical
content hash each time.

| match_id | raw bytes | facts JSON bytes | facts sha256 (pass 1 = pass 2) | elapsed | user CPU | system CPU | peak heap | GC | combat entries | heroes | timeline |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 8941092540 | 134,628,308 | 18,130 | `d53feb20765a20f8731342770f9644a021d87d8fe6e9e8cd5ba550c2510b59d5` | ~1.64s | ~2.08s | ~0.07s | 62-64 MiB | 36 | 115,165 | 10 | 67 |
| 8940891805 | 112,605,718 | 15,588 | `3dd6d472a2d20472d75bdecf0bd7e12fc8ffc29978f3bc47917c0dd8fa73c143` | ~1.37s | ~1.73s | ~0.06s | 56-60 MiB | 32 | 96,196 | 10 | 53 |

`deterministic: true` on both. CPU is process-level user/system seconds from
`getrusage(RUSAGE_SELF)`, measured across the parse call only (the 25ms heap
sampler goroutine is joined before return so resource cleanup is deterministic).
On a 32-core/61 GiB host this is far below any live-process contention risk,
which was a program principle (replay work must not compete with the live
broadcast).

## Batch Behavior (resume, dedupe, terminal failure, checkpoint safety)

`internal/replay/batch.go` implements an idempotent, manifest-driven state
machine with explicit `queued / running / succeeded / failed_terminal` states,
content-addressed dedupe, bounded retries, and a dead-letter (terminal) state.
State is persisted via atomic write (random exclusive temp file, file fsync,
close, rename, parent-directory fsync) after every
state transition, and any checkpoint failure is propagated (not ignored)
because resume safety is lost if the on-disk state diverges from in-memory
state. A post-rename directory-sync failure is a typed uncertain commit; the
complete canonical JSON is reconciled but the run still fails. A subsequent
idempotent run reloads/reconciles state and retries the checkpoint to converge.

Content identity is enforced, not assumed: on resume a succeeded entry is
re-hashed and the fresh SHA-256 is compared with the stored digest; a changed
file is reprocessed rather than silently skipped. A terminal entry is skipped
before hashing on resume (no re-attempt, no re-increment). The manifest is
validated up front: empty entries, empty identities/paths, and duplicate
`match_id` values (which would collide in the state map) are rejected.

Evidence run over a four-entry manifest (two matches, one duplicate of the first
demo, one missing path):

```
batch error: replay: terminal failures for 1 entry(ies): missing_demo
8940891805           succeeded        attempts=1 facts=3dd6d472a2d2 content=fd166d89374d err=""
8941092540           succeeded        attempts=1 facts=d53feb20765a content=3b100ca7c930 err=""
8941092540_dup       succeeded        attempts=0 facts=d53feb20765a content=3b100ca7c930 err=""
missing_demo         failed_terminal  attempts=1 facts= content= err="verify: open data/replays/does_not_exist.dem: no such file or directory"
summary: succeeded=3 queued=0 running=0 failed=1
```

- The CLI exits **nonzero (1)** whenever the final state holds any terminal
  entry, on both the first run and a resume re-run, so `failed=1` does not read
  as success to an operator or CI. The whole bounded manifest is processed
  before the aggregate terminal error is returned (a terminal entry does not
  skip later entries).
- The duplicate input (`8941092540_dup`) reused the first parse (`attempts=0`,
  identical facts hash and content hash) and produced no second facts artifact.
- Exactly two content-addressed facts files plus two provenance sidecars were
  written (one per distinct content), total `data/replay-facts/` size ~37 KiB.
- The missing demo reached `failed_terminal` after the bounded attempt with a
  terminal reason. A resume re-run left both succeeded and terminal entries
  unchanged (`attempts` stable) and rewrote no facts file, proving no duplicate
  work on resume. The no-duplicate-work, content-change-detection,
  checkpoint-propagation, aggregate-terminal, and manifest-validation
  guarantees are covered by focused unit tests (`internal/replay/batch_test.go`).

## Provenance And Self-Describing Facts

`ReplayFactsV1` (schema `replay.facts.spike.v2`) embeds a `Provenance` envelope
pinning `dotabuff/manta v1.5.0`, `internal/replay spike.v2`, and the schema
version inside the deterministic output, so a parser/adapter/schema upgrade
changes the content hash. The facts hold no wall clock and no input digest, so
the content hash is still a deterministic function of (demo bytes, pinned
versions).

The batch runner also writes a `ProvenanceSidecar` (`<full-sha256>.provenance.json`)
alongside each facts artifact, keyed by the **full** decompressed-demo SHA-256
with the matching facts hash, match id, and pinned parser/adapter/schema
versions, plus a wall-clock `generated_at` for human inspection. Facts and
sidecar filenames use the full digest (not a truncated prefix) to avoid
collisions and to keep the input identity recoverable from the filesystem.

## Storage Recommendation

Measured per professional replay:

- compressed `.dem.bz2` (zstd): ~65-79 MiB
- decompressed `.dem`: ~113-135 MiB
- normalized facts JSON: ~15-18 KiB

Smallest sufficient local design (recommended):

- Keep the compressed `.dem.bz2` archive (zstd) as the durable raw artifact under
  `data/replays/`, content-addressed by its SHA-256 (so duplicate downloads are
  detected and deduped). Decompress on demand; the decompressed `.dem` need not
  be retained long-term.
- Persist normalized facts as small JSON files under `data/replay-facts/`,
  content-addressed by the demo SHA-256, with a single `batch-state.json`
  manifest state file.
- No database service. A directory tree plus content-addressed files satisfies
  all measured access patterns for M0/M1.

Capacity check on PaulPC4090 (`/dev/sda2`, ~2.9 TiB free): even a generous full
90/180-day professional corpus of ~2000 matches is ~150 GiB compressed / ~256 GiB
decompressed / ~35 MiB facts, far below free space. The M1 ~100-replay
representative sample is ~7.5 GiB compressed / ~12.8 GiB decompressed / ~1.7 MiB
facts. A database service is therefore not justified by load; it should be
reconsidered only if M1-era aggregated snapshot builds require indexed lookup or
cross-match joins that file-system enumeration cannot satisfy, and only after
that need is measured.

## Non-Goals

- No production replay pipeline, full corpus backfill, or GC integration.
- No live dashboard enrichment from replay facts; replay facts stay offline.
- No definition of the accepted `HistoricalBaselineV1` contract here.
- No committing or attaching raw replays, credentials, or unneeded personal
  data. Account IDs are needed for roster identity upstream, are present in
  OpenDota metadata locally, and are never written to committed artifacts; the
  spike's committed fact set contains only hero/item/building names and counts.

## Verification

Automated (no network, no replay, no credentials):

```bash
go test -count=1 ./...
go vet ./...
git diff --check
```

Expected: all packages pass; `internal/replay` covers deterministic aggregation,
hero/name filtering, timeline ordering, batch resume, content-change detection,
checkpoint-failure propagation, aggregate terminal signaling, manifest
validation, and idempotent restore. `cmd/replay-spike` covers numeric match ID,
Valve-host redirects, malformed-manifest CLI behavior, retry-all terminal exit,
and validate-before-replace acquisition (no network); `internal/atomicfile`
covers injected durability failures.

Manual reproducible spike (requires network + ~300 MiB local; raw files stay
git-ignored under `data/`):

```bash
go build -o ./spike ./cmd/replay-spike
./spike acquire 8941092540 --data-dir ./data
./spike acquire 8940891805 --data-dir ./data
./spike parse ./data/replays/8941092540.dem --twice --out ./data/replay-facts/8941092540.facts.json
cat > ./data/replay-facts/manifest.json <<JSON
{"entries":[
  {"match_id":"8941092540","dem_path":"data/replays/8941092540.dem"},
  {"match_id":"8940891805","dem_path":"data/replays/8940891805.dem"}
]}
JSON
./spike batch ./data/replay-facts/manifest.json --max-retries 1
```

Expected: `deterministic: true` for each `parse --twice`; `acquire` prints
`replay_format_version=22 patch_id=60 patch_name="7.41"`; batch `succeeded=`
count meeting the manifest and exits nonzero when the final state holds a
terminal entry; resume re-runs leave succeeded entries and fact artifacts
unchanged.

## Acceptance Mapping

- At least two safely available current-patch professional replays parse twice
  with deterministic normalized output: met (8941092540 and 8940891805, gameplay
  patch 60 / Dota 7.41, replay format version 22, `deterministic: true`).
- Acquisition/parser/storage decisions evidence-backed; no GC credential or
  account automation: met (OpenDota metadata + Valve public CDN + manta; no
  credentials, no GC, no salts/automation).
- Parser choice, version, and license recorded and proven against a local
  downloaded demo: met (`dotabuff/manta` v1.5.0, MIT, verified on build 6896).
- Report available facts, missing facts, parser/version failure modes, elapsed
  time, CPU, peak memory, raw size, parsed size, facts per match: met (see
  tables above; no parser/version failure; limitations recorded as fact-
  completeness bounds).
- Exercise interruption/resume and duplicate input behavior at the spike
  boundary: met (batch resume + content-addressed dedupe, with unit tests).
- Recommend the smallest storage design supported by measurements; no
  speculative database service: met (directory + content-addressed files).
- No credential, raw replay, or unneeded personal data committed or attached:
  met (raw replays git-ignored; facts JSON contains only names/counts).
- `go test -count=1 ./...`, `go vet ./...`, `git diff --check` pass: met.

## Residual Risks / Next Experiment

- Valve/OpenDota replay availability is best-effort and may expire; M1 must
  classify every missing replay with a bounded reason (expired, purged,
  unlisted) rather than fabricate coverage.
- manta's incomplete string-table update handling bounds actor resolution; the
  safe next experiment is to add `UpdateStringTable`/`CDemoStringTables`
  integration inside the `internal/replay` adapter (or upstream) and re-measure
  first-blood/building-kill/gold-XP actor resolution, before entity-state
  reconstruction.
- Named item purchases and per-hero gold/XP attribution require entity
  inventory + player-to-hero mapping, deferred to M1.
- The measured Valve replay URLs are plain `http://`. Their bytes have an
  unavoidable on-path substitution/chain-of-custody limitation; full SHA-256
  is only an identity check. M1 must quarantine metadata/parser identity
  mismatches, and any future authenticity requirement needs a working approved
  authenticated source rather than treating these hashes as proof of origin.
