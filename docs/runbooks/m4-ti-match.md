# M4 P4 complete-TI-match harness

This runbook prepares and measures one complete official TI DotaTV game. The
synthetic preflight never claims P4, and the live command always leaves final
acceptance to the independent reviewer and G胖.

## Safety and roots

Use fresh absolute roots outside the repository and outside
`/home/paul-zhang/文档/dota2_ob`. The harness rejects the repository, the
canonical root, the home directory, and their descendants. It creates private
application, configuration, cache, session, evidence, recording, runtime, and
tool directories. The only file installed outside the selected root is the
uniquely named Dota GSI file
`gamestate_integration_dota2_ob_dot65.cfg`; the live command refuses to
overwrite it and removes it on every normal or aborted exit. It never starts or
controls Dota, Steam, an account, or gameplay.

The generated GSI configuration disables provider and wearable payloads. The
verifier treats credentials, account/Steam identifiers, chat, cookies, and
authorization material as fatal privacy violations. Raw observations remain
the authority and are never pasted into issue comments.

## Deterministic preflight

Run only after the exact clean candidate has been pushed to
`origin/agent/dota2-fullstack-engineer/DOT-65-ti-match-harness` and is the head
of draft PR #19. Dota and OBS must be stopped:

```sh
go run -buildvcs=true ./cmd/m4-match preflight --data-root /var/tmp/dot65-preflight-a
go run -buildvcs=true ./cmd/m4-match verify --data-root /var/tmp/dot65-preflight-a --expect preflight
```

Preflight fails closed unless local HEAD has exactly one parent—the rejected
`bc38bf3d...` candidate—and local HEAD, the origin branch, PR #19's head, the
executing harness VCS revision, and the built product VCS revision are identical.
It captures the branch and PR refs together in one bounded remote snapshot both
before and after the full matrix. Named canonical sub-checks retain non-secret
observed identities and stable reason codes; transport text is confined to an
optional noncanonical `diagnostics/candidate-identity.log`.
It binds the binary hash, Git tree, remote URL, and freshly captured Go, Node,
npm, Zig, kernel, Steam, Dota, OBS, and GPU identities. It runs the complete
focused-twice, full Go, uninterrupted race, vet, build, module, browser,
OBS-overlay, dependency, privacy, source, diff, secret/generated-data, and clean
tree matrix. It also starts the real product for endpoint/orderly-shutdown proof
and deliberately `SIGKILL`s and restarts a second product from retained raw
input. Selected normalized transcripts and the explicit fault-proof manifest
are content-addressed artifacts. Root names, timestamps, PIDs, and durations are
normalized, so two fresh roots on the same candidate/environment must be
byte-identical.

Run a second root and compare:

```sh
go run -buildvcs=true ./cmd/m4-match preflight --data-root /var/tmp/dot65-preflight-b
go run -buildvcs=true ./cmd/m4-match verify --data-root /var/tmp/dot65-preflight-b --expect preflight
cmp /var/tmp/dot65-preflight-a/evidence/canonical/evidence-index.json \
    /var/tmp/dot65-preflight-b/evidence/canonical/evidence-index.json
diff -qr /var/tmp/dot65-preflight-a/evidence/canonical \
    /var/tmp/dot65-preflight-b/evidence/canonical
cmp /var/tmp/dot65-preflight-a/evidence/readiness.json \
    /var/tmp/dot65-preflight-b/evidence/readiness.json
sha256sum /var/tmp/dot65-preflight-{a,b}/evidence/canonical/evidence-index.json
```

`evidence/readiness.json` is the machine-readable decision. `ready` cannot be
true if any exact-environment, fault-matrix, endpoint, privacy, identity, or
clean-tree check fails. It always contains `claims_p4:false` and the exact short
instruction payload for the single readiness issue.

## Live command

Create a private JSON file containing only public match identity:

```json
{"tournament":"The International 2026","series":"Upper bracket","game":"Game 1","radiant":"Public Team A","dire":"Public Team B","match_id":"1234567890","official_source_url":"https://www.dota2.com.cn/international/2026","confirmed_at":"2026-08-15T10:00:00Z","dota_pid":12345,"dota_executable_sha256":"<sha256 of /proc/12345/exe>","dota_process_start_ticks":123456789}
```

Then run the foreground command:

```sh
go run -buildvcs=true ./cmd/m4-match live \
  --readiness-root /var/tmp/dot65-preflight-a \
  --data-root /var/tmp/dot65-live-1234567890 \
  --identity /var/tmp/dot65-match-identity.json
```

`confirmed_at` must be the current canonical UTC time (within 15 minutes).
`dota_pid`, executable hash, and `/proc/<pid>/stat` start ticks bind the manually
launched Dota process instance; the harness rechecks all three throughout the
attempt. It verifies the exact preflight, creates a new isolated root, seals the accepted
live-only artifacts for the match session, installs the unique GSI config,
starts the exact candidate and isolated OBS profile/collection, and waits for a
manual preview confirmation. Only after OBS preview and recording are visibly
correct, type the exact confirmation printed by the command. Paul then manually
launches Dota, joins the identified official DotaTV match before `0:00`, and
performs this script:

1. Confirm the preview.
2. Approve, or observe a deterministic ineligible rejection; reject, or observe
   a deterministic ineligible rejection.
3. Pin and unpin when eligible. If ineligible, make both attempts so their
   deterministic durable rejection results prove ineligibility; omission is not
   evidence.
4. Emergency-hide, confirm claim-free output within two seconds, keep it hidden
   through at least one further accepted GSI heartbeat, then clear.

The foreground process samples the complete product/OBS process trees and all
accepted P4 body, queue, state, resource, cursor, policy, frame, visibility, and
cross-plane identities at five-second cadence. A separate 100 ms visibility
trace proves emergency-hide deadline, claim-free output, no stale revival, and
continued raw capture. Missing telemetry is failure, never zero. Every accepted
frame must carry the same nonzero public match ID and plausible retained receive/
game-clock cadence. Missing pre-game arming, identity loss/change, a receive gap,
clock regression/jump, remake/abandon, Dota process replacement, process loss,
signal, missing winner/post-game, or incomplete recording seals a failed,
non-resumable attempt. Never splice attempts.

After normal post-game, product and OBS must stop cleanly, OBS must produce a
finalized EBML MKV with real frame counters and no partial file, and a fresh
isolated product is started with no cursor, checkpoint, policy, audit, operator,
or overlay output. Only `raw.jsonl` plus the canonical durable operator-input
journal is admitted. The restarted product rebuilds and byte-compares cursor,
V3 policy, audit, operator, and overlay outputs; copied derived output, assigned
cursor values, termination errors, or over-bound recovery RSS fail the attempt.

Press `Ctrl-C` once to abort. The command stops product/OBS, removes only its
unique GSI config, preserves the attempt evidence, and records failure. Killing
the harness, Dota, OBS, or the host invalidates the attempt; do not resume it.

## Evidence, retention, and cleanup

Keep the complete root until independent review. Share only the canonical index,
readiness result, sanitized summaries, and hashes—not raw GSI, recordings,
tokens, logs, process command lines, or private paths. The live result always
has `ready:false` and `claims_p4:false`; technical completeness does not
self-accept P4.

Verify before retention or deletion:

```sh
go run -buildvcs=true ./cmd/m4-match verify --data-root /var/tmp/dot65-preflight-a --expect preflight
go run -buildvcs=true ./cmd/m4-match verify --data-root /var/tmp/dot65-live-1234567890 --expect live
```

Cleanup requires the exact index hash printed in `evidence/readiness.json` and
refuses an unverifiable or protected root:

```sh
go run -buildvcs=true ./cmd/m4-match cleanup \
  --data-root /var/tmp/dot65-preflight-a \
  --confirm-index-sha256 '<exact evidence_index_sha256>'
```

Downloaded tools are not required. npm caches and Playwright browsers remain in
their existing user-managed locations; generated node modules are ignored and
never enter evidence or Git.
