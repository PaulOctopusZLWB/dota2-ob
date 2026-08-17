# M4 public-match rehearsal

This package is an operational rehearsal only. It cannot arm or accept P4/M4,
does not implement public-tournament authority, and does not select, join, or
control Dota 2, Steam, or OBS. Its immutable identity is
`AcceptedRehearsalSpec = 958c3f0d3fd464df4960905bc4bb7918521884e6`.

## Build and review preflight

Use a fresh isolated root under `/var/tmp`, outside the repository and
`/home/paul-zhang/文档/dota2_ob`:

```sh
go run ./cmd/m4-match rehearsal-preflight \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-preflight-a
go run ./cmd/m4-match rehearsal-verify \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-preflight-a
```

The sole positive state is `REHEARSAL_READY`. A refusal reports the exact
failed preflight check; do not stop or reconfigure a user-owned process to make
the check pass. The listener probe binds localhost only long enough to prove
exclusive admission and releases it before returning.

Create a second fresh root and byte-compare these canonical files:

```sh
cmp /var/tmp/dota2-ob-rehearsal-preflight-a/evidence/readiness.json \
    /var/tmp/dota2-ob-rehearsal-preflight-b/evidence/readiness.json
cmp /var/tmp/dota2-ob-rehearsal-preflight-a/evidence/canonical/evidence-index.json \
    /var/tmp/dota2-ob-rehearsal-preflight-b/evidence/canonical/evidence-index.json
```

Cleanup is admitted only after independent verification and an exact index-hash
confirmation:

```sh
go run ./cmd/m4-match rehearsal-cleanup \
  --repo-root "$PWD" \
  --data-root /var/tmp/dota2-ob-rehearsal-preflight-a \
  --confirm-index-sha256 <exact-evidence-index-sha256>
```

## Manual rehearsal boundary

Only after a reviewed exact candidate reports `REHEARSAL_READY` may Paul
manually choose and join an ordinary publicly spectatable DotaTV match before
game clock `0:00`. Keep the accepted localhost GSI, raw-first storage,
projection, policy, operator, overlay, OBS, recovery, resource, and cleanup
path unchanged. No code in this package performs that manual action.

Every terminal attempt is non-resumable. Preview refusal, zero or partial
frames, late join, malformed input, process loss/replacement, identity failure,
coverage failure, verification restart, recording failure, or unclean exit must
seal `rehearsal_failed`. A later attempt starts in a new isolated root. Never
splice, relabel, or import rehearsal evidence into P4.
