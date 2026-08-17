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

After separate authorization, Paul manually chooses and joins a public DotaTV
match before `0:00`. The existing capture process retains RawRecordV3 first.
After the attempt stops, seal that retained file and the observed operational
facts:

```sh
go run ./cmd/m4-match rehearsal-attempt \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a \
  --raw-session /absolute/retained/session/raw.jsonl \
  --session-id <exact-session-id> \
  --match-id <stable-nonzero-match-id> \
  --process-executable-sha256 <exact-dota-executable-sha256> \
  --process-start-ticks <exact-start-tick>

go run ./cmd/m4-match rehearsal-verify \
  --expect terminal \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-a
```

Boolean attempt flags default to the successful observation. Set an exact flag
to false when that fact failed; never edit the sealed artifacts afterward.
Zero, one, partial, malformed, late, replaced-process, lost-process, recording,
reconciliation, operator, deadline, confinement, or unclean attempts seal
`rehearsal_failed`. Attempts are non-resumable.

Terminal verification reloads RawRecordV3, verifies its order and hashes,
re-executes the accepted live-only evaluator and all eight typed unavailable
families, regenerates value-free coverage, reproduces the no-cache suppression
bytes, derives the outcome/failure receipt, and compares every canonical byte.

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
