## Public-match rehearsal harness

This harness is a closed `public_match_rehearsal + public_match` evidence path. It never claims P4, never qualifies a match, and never creates acceptance or tournament authority. Dota, Steam, and OBS remain manually controlled. The producer reads only low-risk `/proc` identity facts for one already-running `dota2` process.

### Lifecycle

Use a fresh absolute root below `/var/tmp`. Preflight owns that root and its raw V3 session. Only `REHEARSAL_READY` permits Paul to manually select and spectate a public match; `REFUSED` is terminal and exposes no activation instruction.

```sh
go run ./cmd/m4-match rehearsal-preflight \
  --data-root /var/tmp/dot84-rehearsal-a \
  --run-purpose public_match_rehearsal \
  --match-class public_match

go run ./cmd/m4-match rehearsal-attempt \
  --data-root /var/tmp/dot84-rehearsal-a \
  --expected-match-id '<optional identity selector>'

go run ./cmd/m4-match rehearsal-terminal-verify \
  --data-root /var/tmp/dot84-rehearsal-a

go run ./cmd/m4-match rehearsal-cleanup \
  --data-root /var/tmp/dot84-rehearsal-a \
  --confirm-terminal-sha256 '<verified terminal_sha256>'
```

The expected match ID is selection-only: it cannot provide a path, hash, counter, process fact, or success assertion. A missing physical source, interruption, partial capture, identity drift, OBS/product failure, or incomplete post-game produces a deterministic typed `rehearsal_failed` receipt. Cleanup requires independent retained-input regeneration plus the exact terminal hash token.

### Resource envelope

- Raw input: 64 MiB total, read through a hard `limit + 1` descriptor boundary.
- Raw line: `13,985,113` bytes, covering the accepted 10 MiB raw GSI body after base64 and canonical V3 framing.
- Raw records and field-coverage frames: 4,096 each, enough for a 30-minute match at the accepted cadence while closing allocation before population growth.
- Historical executions and audits: 32,768 each, exactly `4,096 frames × 8 families`.
- Canonical artifact: 32 MiB, read through a bounded descriptor and rechecked against descriptor/path identity.
- Attempt duration: 30 minutes.

These are compile-time limits. Exact-boundary inputs are accepted; total/line/population cap+1, unterminated records, descriptor growth, and path replacement fail closed while retaining terminal evidence when a harness root exists.
