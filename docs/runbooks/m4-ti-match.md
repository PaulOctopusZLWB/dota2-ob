# M4 P4 authority-verified public-tournament harness

This runbook prepares and measures one complete authority-verified public tournament DotaTV game. The
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
go run -buildvcs=true ./cmd/m4-match preflight --purpose p4_acceptance --match-class public_tournament --data-root /var/tmp/dot77-p4-preflight-a
go run -buildvcs=true ./cmd/m4-match verify --purpose p4_acceptance --match-class public_tournament --data-root /var/tmp/dot77-p4-preflight-a --expect preflight
```

Preflight fails closed unless local HEAD has exactly one parent—the rejected
`9d9e7d93...` candidate—and local HEAD, the origin branch, PR #19's head, the
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
go run -buildvcs=true ./cmd/m4-match preflight --purpose p4_acceptance --match-class public_tournament --data-root /var/tmp/dot77-p4-preflight-b
go run -buildvcs=true ./cmd/m4-match verify --purpose p4_acceptance --match-class public_tournament --data-root /var/tmp/dot77-p4-preflight-b --expect preflight
cmp /var/tmp/dot77-p4-preflight-a/evidence/canonical/evidence-index.json \
    /var/tmp/dot77-p4-preflight-b/evidence/canonical/evidence-index.json
diff -qr /var/tmp/dot77-p4-preflight-a/evidence/canonical \
    /var/tmp/dot77-p4-preflight-b/evidence/canonical
cmp /var/tmp/dot77-p4-preflight-a/evidence/readiness.json \
    /var/tmp/dot77-p4-preflight-b/evidence/readiness.json
sha256sum /var/tmp/dot77-p4-preflight-{a,b}/evidence/canonical/evidence-index.json
```

`evidence/readiness.json` is the machine-readable decision. `ready` cannot be
true if any exact-environment, fault-matrix, endpoint, privacy, identity, or
clean-tree check fails. It always contains `claims_p4:false` and the exact short
instruction payload for the single readiness issue.

## Live command

After independent exact-SHA review, select a match and write the canonical
`MatchAuthoritySelectionV1` JSON. Pass the Steam Web API key only on an inherited
file descriptor; it never enters arguments, environment variables, URLs, logs,
artifacts, or retained headers. Seal the authority retrieval before any live run:

```sh
go run -buildvcs=true ./cmd/m4-match authority-preflight \
  --readiness-root /var/tmp/dot77-p4-preflight-a \
  --data-root /var/tmp/dot77-authority-1234567890 \
  --selection /var/tmp/dot77-authority-selection.json \
  --webapi-key-fd 3 3</path/to/private-key-file
```

The command retrieves only the compiled Valve Web API and Valve Dota 2 esports
endpoint shapes, with no retry, and seals exact retained bytes, sanitized exports, fact locators, bounded
counters, the selected match, candidate, binary, amendment, authority root, and
readiness evidence index. Any missing, ambiguous, conflicting, redirected,
credential-bearing, over-bound, compressed-trailing, or hash-mismatched fact
fails closed.

The embedded independently reviewable trust anchor is the historical Valve
Stockholm Major record (league `14173`). It proves the code path without
self-asserting a 2026 organizer origin, but it cannot authorize a current live
match. A usable future tournament requires a new immutable harness successor
with a defensible Valve event/league record and fresh exact-SHA review.

Create a private JSON file containing only public match identity:

```json
{"tournament":"The Stockholm Major","series":"Upper bracket","game":"Game 1","radiant":"Public Team A","dire":"Public Team B","match_id":"1234567890","official_source_url":"https://www.dota2.com/esports/springmajor22/watch/14173/130/game3details","confirmed_at":"2026-08-17T10:00:00Z","dota_pid":12345,"dota_executable_sha256":"<sha256 of /proc/12345/exe>","dota_process_start_ticks":123456789}
```

Then run the foreground command:

```sh
go run -buildvcs=true ./cmd/m4-match live --purpose p4_acceptance --match-class public_tournament \
  --readiness-root /var/tmp/dot77-p4-preflight-a \
  --data-root /var/tmp/dot77-p4-live-1234567890 \
  --authority-root /var/tmp/dot77-authority-1234567890 \
  --identity /var/tmp/dot65-match-identity.json
```

`confirmed_at` must be the current canonical UTC time (within the assigned
start window, which is at most 30 minutes).
`dota_pid`, executable hash, and `/proc/<pid>/stat` start ticks bind the manually
launched Dota process instance; the harness rechecks all three throughout the
attempt. It descriptor-confines and verifies the complete retained authority
graph, imports it for finalization and recovery verification, creates a new
isolated root, seals the accepted live-only artifacts for the match session,
installs the unique GSI config,
starts the exact candidate and isolated OBS profile/collection, and waits for
the single bounded four-action human payload. Paul performs exactly these
actions; dependency preparation and troubleshooting are not part of the
notification:

1. Manually launch Dota 2 within the assigned start window (maximum 30 minutes).
2. After the agent reports `ARMED`, join the authority-verified public tournament DotaTV game
   before `0:00` and confirm the public tournament, series, game, teams, and
   match ID printed by the command.
3. Execute the prescribed operator script: confirm preview; approve or record
   the prescribed deterministic ineligible terminal result; reject or record
   it; pin then unpin when eligible; emergency-hide, prove claim-free output
   within two seconds through another accepted heartbeat, then clear.
4. Remain through normal post-game and recording finalization.

Stop and abort immediately for a late join, identity contradiction, or any
agent-reported failure. Dota must already be running before PID binding and
agent-owned arming; capture and OBS must be armed before the match is joined.
The harness records the real arming instant before printing `ARMED`, applies a
hard 30-minute deadline to confirmation and the remaining pregame start window,
and accepts a retained pregame frame that arrives immediately after `ARMED`
while Paul is still confirming the preview.

The bound V3 policy application, not the GSI server, publishes its instantiated
candidate limit and health. Live samples require its observed `64` plus the
capture channel's independently observed capacity `1`. The harness also proves
that the capture listener's unique Linux socket inode belongs to the launched
product and binds stable start/end invocation trees. Unreadable or changed
correlation evidence fails closed. This does not attest each localhost request
sender; the residual remains `localhost_gsi_sender_unattested`.

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
go run -buildvcs=true ./cmd/m4-match verify --purpose p4_acceptance --match-class public_tournament --data-root /var/tmp/dot77-p4-preflight-a --expect preflight
go run -buildvcs=true ./cmd/m4-match verify --purpose p4_acceptance --match-class public_tournament --data-root /var/tmp/dot77-p4-live-1234567890 --expect live
```

Cleanup requires the exact index hash printed in `evidence/readiness.json` and
refuses an unverifiable or protected root:

```sh
go run -buildvcs=true ./cmd/m4-match cleanup \
  --purpose p4_acceptance --match-class public_tournament \
  --data-root /var/tmp/dot65-preflight-a \
  --confirm-index-sha256 '<exact evidence_index_sha256>'
```

All harness mutations and cleanup are confined through descriptor-backed
`os.Root` handles under `/var/tmp`. A component swap cannot redirect a write or
recursive cleanup to the repository, home, canonical root, or another protected
target; unsupported confinement fails closed.

Downloaded tools are not required. npm caches and Playwright browsers remain in
their existing user-managed locations; generated node modules are ignored and
never enter evidence or Git.
