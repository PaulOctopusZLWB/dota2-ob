## M4 public-match rehearsal harness

This harness is evidence collection only. It does not authorize or select a match, launch or control Dota 2 or Steam, claim P4/M4 acceptance, or change a manual window. A human starts and controls Dota 2 and Steam. The attempt owns its built analytics product and an isolated OBS profile/process rooted under the rehearsal evidence root; it never attaches to or stops user-owned OBS. `REHEARSAL_READY` is the sole positive preflight state, and every refused preflight omits the activation instruction.

The compiled rehearsal contract is `1d793bc3d1d38b3ce3fd9e97005a5d4e45e928b2`. The P4 contract remains `271cc47d503828528b7c69212deb4d22683cb715`. Rehearsal and P4 roots reject each other in both directions.

### ARM -> human launch -> ATTEMPT/JOIN

Before Dota is launched, arm from the exact pushed candidate. `rehearsal-arm` creates the owned root, binds the exact Go executable, installs the exact root-owned GSI bytes with no-overwrite semantics, and prints an `arm_sha256` token in the receipt:

```sh
m4-match rehearsal-arm --data-root /var/tmp/<fresh-DOT-87-root> --repo-root <exact-candidate-checkout>
```

Only after a `REHEARSAL_READY` receipt may the human launch Dota 2, then manually join the selected public match. Do not launch Dota before ARM. The attempt consumes and revalidates the retained arm record; it does not restage GSI. It builds with the bound Go executable, starts the exact product, and starts a new Flatpak OBS instance whose real instance ID/PID and root-local HOME/config/data/cache/recording paths are verified:

```sh
m4-match rehearsal-attempt --data-root <harness-root> --repo-root <exact-candidate-checkout>
m4-match rehearsal-verify --data-root <harness-root> --repo-root <exact-candidate-checkout>
m4-match rehearsal-cleanup --data-root <harness-root> --repo-root <exact-candidate-checkout> --confirm-terminal-sha256 <cleanup-token>
```

For cancellation before ATTEMPT, remove only the exact still-owned GSI file with its arm token. A wrong token, changed inode, changed bytes, symlink, or substituted path is refused:

```sh
m4-match rehearsal-disarm --data-root <harness-root> --repo-root <exact-candidate-checkout> --confirm-arm-sha256 <arm_sha256>
```

Recovery after interruption is fail-closed: first inspect `flatpak ps --columns=instance,pid,child-pid,application`; never use global `pkill`, `killall`, or an application-wide stop. If producer evidence names an owned instance still present, stop only that exact ID with `flatpak kill <instance-id>`, wait for its real PID to disappear, then run the token-gated cleanup command. Never stop a pre-existing or unrecorded OBS instance.

`rehearsal-attempt` exits zero only when its `rehearsal_completed` receipt passes the independent verifier. Every refusal, zero-frame abort, failed or partial attempt, unavailable or drifting Dota identity, evidence mismatch, and verifier failure prints a canonical failed receipt and exits nonzero. All terminal paths stop the exact owned OBS instance and close its recording/logs before hashing and sealing. Cleanup is verifier- and token-gated; a wrong token preserves the root.

The producer seal cross-links process step receipts and content-addressed product, raw, operator, resource, visibility, recording, final-plane, recovery, and reconciliation artifacts. Its capture loop observes the acquired Dota identity before admitting each new raw sequence, binds the exact RawRecordV3 SHA-256 to that observation, and checks separate capture-terminal and producer-terminal boundaries. A later replay of the file cannot replace these producer-time receipts.

The verifier rehashes every artifact, derives the run identity from the retained owner and preflight records, recounts raw, resource, visibility, and policy populations, validates cadence/bounds and operator actions, and checks recording finalization. For a completion claim it archives the exact candidate commit into a verifier-owned source root and performs a deterministic `-trimpath -buildvcs=false` build. That independently derived executable SHA-256 must equal the retained application artifact and both producer-time recovery process identities. The verifier then copies only fixed retained data inputs plus its independently built executable into a fresh isolated root, launches a new owned recovery process, binds that process to the same executable SHA-256 at start and terminal, reruns raw-only recovery, and byte/hash-compares regenerated cursor, policy, audit, operator, and overlay results. It does not accept nonempty producer-declared recovery hashes or two internally consistent substituted process identities as proof. Six caller-authored JSON placeholders—or any pre-created, cross-run, internally resealed, or substituted completion artifact—cannot satisfy this contract. Hermetic helper-process tests exercise the same producer orchestration and sealing path but are explicitly typed `hermetic_helper`, `physical_match:false`, and can never produce `rehearsal_completed`.

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
