# M4 public-match rehearsal

This package is a non-acceptance rehearsal. It cannot arm or accept P4/M4,
select or join a match, control Dota/Steam/OBS, or provide public-tournament
authority. Its immutable identity is
`AcceptedRehearsalSpec = 958c3f0d3fd464df4960905bc4bb7918521884e6`.

## Closed workflow

Use one fresh isolated root under `/var/tmp`, outside the repository and
`/home/paul-zhang/文档/dota2_ob`.

```sh
go run ./cmd/m4-match rehearsal-preflight \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a

go run ./cmd/m4-match rehearsal-verify \
  --expect preflight \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a
```

A successful preflight emits only `REHEARSAL_READY`. A refused preflight emits
`REFUSED`, an exact failure set, and no manual activation instruction. Never
stop or reconfigure a user-owned process to make a refusal pass.

Preflight creates the only admissible `data/sessions/<session>` capture under
the owned root, builds the exact live-only product, and seals the fixed raw,
harness, and capture-seal paths into `capture/owner.json`. External raw or
evidence paths are never accepted.

With an expected match selector, `rehearsal-attempt` itself launches the
preflight-built product, localhost delivery proxy, isolated OBS profile,
operator journal, resource and fail-closed samplers, reconciliation, and two
raw-only recovery runs. It derives and content-addresses process continuity,
the finalized recording, six accepted operator transitions, five-second
resource samples, confinement, recovery byte equality, and clean exits before
making raw read-only and writing the harness, capture seal, and producer
receipt. Missing component output always produces a typed failed terminal.

```sh
go run ./cmd/m4-match rehearsal-attempt \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a \
  --expected-match-id <expected-identity-selector>

go run ./cmd/m4-match rehearsal-verify \
  --expect terminal \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a
```

The expected match identity is only a selector: it can cause a mismatch
failure and can never prove or upgrade success. Match identity, pre-zero entry,
clock/order, post-game/winner, and raw hashes are derived only from retained V3.
Zero, one, partial, malformed, late, replaced-process, lost-process, recording,
reconciliation, operator, deadline, confinement, or unclean attempts seal
`rehearsal_failed`. Attempts are non-resumable.

Terminal verification reloads RawRecordV3, verifies its order and hashes,
re-executes all eight direct production history branches with typed unavailable
input, regenerates value-free coverage, revalidates every content-addressed
harness artifact, derives the outcome/failure receipt, and compares every
canonical byte.

The compile-time resource envelope is 64 MiB total raw, base64 expansion of the
accepted 10 MiB GSI payload plus a 4 KiB V3 envelope per framed line, 4096 raw
records/coverage frames, 32768 branch executions, 32768 audits, and 32 MiB per
canonical artifact. Streaming rejects total/line/record limit+1 and unterminated
input before unbounded allocation; these caps bound a long local rehearsal while
remaining well above the accepted live cadence.

Cleanup is impossible after preflight alone. It requires the exact terminal
hash returned by terminal verification:

```sh
go run ./cmd/m4-match rehearsal-cleanup \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a \
  --confirm-terminal-sha256 <verified-terminal-sha256>
```

Never splice, resume, relabel, or import rehearsal evidence into P4. A new
attempt always uses a new isolated root.
