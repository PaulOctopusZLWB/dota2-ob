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

Run from the exact candidate checkout with Dota and OBS stopped:

```sh
go run ./cmd/m4-match preflight --data-root /var/tmp/dot65-preflight-a
go run ./cmd/m4-match verify --data-root /var/tmp/dot65-preflight-a --expect preflight
```

Preflight checks exact Git ancestry and cleanliness; accepted fixture/golden
hashes; Go, Node, npm, Zig, kernel, Steam, Dota, and OBS identities; deterministic
OBS/GSI preparation; the complete production M4 package twice; browser and OBS
overlay suites; forbidden-source/privacy scans; a real product build; real GSI,
operator, and overlay endpoints; and orderly product shutdown. Test transcripts
are retained as noncanonical run logs. `evidence/canonical/evidence-index.json`
excludes root names, timestamps, PIDs, and durations, so two fresh roots on the
same candidate/environment must be byte-identical.

Run a second root and compare:

```sh
go run ./cmd/m4-match preflight --data-root /var/tmp/dot65-preflight-b
go run ./cmd/m4-match verify --data-root /var/tmp/dot65-preflight-b --expect preflight
cmp /var/tmp/dot65-preflight-a/evidence/canonical/evidence-index.json \
    /var/tmp/dot65-preflight-b/evidence/canonical/evidence-index.json
sha256sum /var/tmp/dot65-preflight-{a,b}/evidence/canonical/evidence-index.json
```

`evidence/readiness.json` is the machine-readable decision. `ready` cannot be
true if any exact-environment, fault-matrix, endpoint, privacy, identity, or
clean-tree check fails. It always contains `claims_p4:false` and the exact short
instruction payload for the single readiness issue.

## Live command

Create a private JSON file containing only public match identity:

```json
{"tournament":"The International","series":"Upper bracket","game":"Game 1","radiant":"Public Team A","dire":"Public Team B","match_id":"1234567890"}
```

Then run the foreground command:

```sh
go run ./cmd/m4-match live \
  --readiness-root /var/tmp/dot65-preflight-a \
  --data-root /var/tmp/dot65-live-1234567890 \
  --identity /var/tmp/dot65-match-identity.json
```

It verifies the exact preflight, creates a new isolated root, seals the accepted
live-only artifacts for the match session, installs the unique GSI config,
starts the exact candidate and isolated OBS profile/collection, and waits for a
manual preview confirmation. Only after OBS preview and recording are visibly
correct, type the exact confirmation printed by the command. Paul then manually
launches Dota, joins the identified official DotaTV match before `0:00`, and
performs this script:

1. Confirm the preview.
2. Approve, or observe a deterministic ineligible rejection; reject, or observe
   a deterministic ineligible rejection.
3. Pin and unpin when eligible.
4. Emergency-hide, confirm claim-free output within two seconds, then clear.

The foreground process samples at five-second cadence and runs until a normal
post-game GSI state. A missing negative pre-game clock, match identity change,
gap, disconnect/abandon state, process loss, signal, or missing terminal state
seals a failed non-resumable attempt. Never splice attempts. Natural DotaTV
pauses are context only and do not authorize a claim.

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
go run ./cmd/m4-match verify --data-root /var/tmp/dot65-preflight-a --expect preflight
go run ./cmd/m4-match verify --data-root /var/tmp/dot65-live-1234567890 --expect live
```

Cleanup requires the exact index hash printed in `evidence/readiness.json` and
refuses an unverifiable or protected root:

```sh
go run ./cmd/m4-match cleanup \
  --data-root /var/tmp/dot65-preflight-a \
  --confirm-index-sha256 '<exact evidence_index_sha256>'
```

Downloaded tools are not required. npm caches and Playwright browsers remain in
their existing user-managed locations; generated node modules are ignored and
never enter evidence or Git.
