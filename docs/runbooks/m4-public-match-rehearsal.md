## M4 public-match rehearsal harness

This harness is evidence collection only. It does not authorize, select, or run a match; launch or control Dota 2, Steam, or OBS; claim P4/M4 acceptance; or change a manual window. A human starts and controls those applications. `REHEARSAL_READY` is the sole positive preflight state, and every refused preflight omits the activation instruction.

The compiled rehearsal contract is `1d793bc3d1d38b3ce3fd9e97005a5d4e45e928b2`. The P4 contract remains `271cc47d503828528b7c69212deb4d22683cb715`. Rehearsal and P4 roots reject each other in both directions.

### Commands

Run preflight from the exact pushed candidate and retain its printed root and cleanup token:

```sh
m4-match rehearsal-preflight --branch <DOT-84-branch> --commit <exact-head> --draft-pr <number>
```

Only after a `REHEARSAL_READY` receipt may an operator manually start the external applications and capture. The attempt consumes only the harness-owned root and its fixed raw path; it accepts no caller paths, hashes, counters, booleans, or success assertions:

```sh
m4-match rehearsal-attempt --root <harness-root>
m4-match rehearsal-verify --root <harness-root>
m4-match rehearsal-cleanup --root <harness-root> --token <cleanup-token>
```

`rehearsal-attempt` exits zero only when its `rehearsal_completed` receipt passes the independent verifier. Every refusal, failed or partial attempt, unavailable or drifting Dota identity, evidence mismatch, and verifier failure prints its canonical receipt and exits nonzero. Cleanup is verifier- and token-gated; a wrong token preserves the root.

### Closed resource envelope

- total raw stream: at most 67,108,864 bytes; the reader observes one additional byte to reject cap+1;
- one canonical JSONL record: at most 13,985,113 bytes including its required LF;
- CRLF and a final unterminated record are rejected;
- records and coverage frames: at most 4,096 each;
- family executions and audits: exactly eight per admitted record, at most 32,768 each;
- all reads are streaming and descriptor-bound; no whole-file raw read is used.

The line cap is the accepted maximum V3 envelope (payload plus framing), while the 64 MiB total cap contains memory, disk, and verification work for a bounded rehearsal. Failed admission receipts bind the opened descriptor, path identity, prefix bytes/hash, counters, primary failure, and independently reconciled concurrent-change state; they never claim an unread suffix as a full-content hash.

### Process and source boundary

The harness uses only low-risk Linux `/proc` identity facts for the manually started `dota2` process: PID, exact `comm` and resolved executable path, descriptor-hashed executable content, path hash, start ticks, device, and inode. It compares the acquired identity before every admitted raw sequence and at both terminal boundaries. PID reuse, disappearance, replacement, or any path/content/start-tick drift fails closed. It does not read process memory, packets, hidden game state, accounts, or UI state.
