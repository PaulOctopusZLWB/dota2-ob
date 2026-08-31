# DOT-54 M1 terminal evidence candidate

Outcome: `historical_no_go_candidate` (independent exact-SHA review required; this is not self-acceptance).

The corrected, conflict-resolved population contains 1,254 matches in the inclusive 180-day window ending `2026-08-12T00:00:00Z`. Its exhaustive terminal counts are 367 `excluded_patch_mismatch`, 7 `replay_metadata_missing`, 100 `replay_identity_quarantined`, and 780 `gate_target_not_selected`. The rejected legacy Nigma ID `7554697` is absent from the sealed 16-team allow-set.

The cutoff-effective `TournamentScopeV1` contains 16 teams and 80 players. An immutable pre-cutoff Liquipedia revision supplies dated tournament registration, positions, and membership intervals; it includes Topson and marks TaiLung former, so TaiLung is excluded. The Perfect World tournament asset supplies the primary field and handles. Hash-verified OpenDota team pages bind those handles to Valve-derived stable team/account IDs. Liquipedia is community-contributed corroboration for membership and effective dates only, not independent Valve stable-ID authority. DatDota was not used.

The governing numerical gate was then evaluated on a deterministic 100-replay sample: every selected team appears at least five times, all 100 replay objects are byte-verified, and two independent manta v1.5.0 runs parsed all 100 with byte-identical fact identities. Every selection entry is bound to its corrected Explorer row and eligible Valve HEAD record. The accepted normalizer produced 100 sealed quarantine receipts and zero verified facts because the public metadata has neither game-build correlation nor hero-to-participant bindings. Eligible counts are therefore zero against the accepted draft/distribution/team-comparative minima of 5/8/10, so zero aggregate cells are publishable. This evidence-derived numerical result is the reason for the no-go candidate; it is not an unresolved-scope shortcut.

## Immutable evidence

- Canonical external root: `${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166`, mode `0700`. Raw/generated data remains outside git.
- Independent sealed roots: `final-v4/run-a` and `final-v4/run-b`; both contain 9 files / 446,902 bytes.
- Evidence-index SHA-256: `9d5f6b6e95941c2704f96f85e6e49f61679055648df60b49ccb5ff9c396b0dc4`.
- Artifact-tree SHA-256: `ba6e7e168bba9117dbcb6804f99e9cb4f25482b7e5c6edad9adc740adbccc37a`.
- `TournamentScopeV1` scope ID: `da84de9204dc8493c07a0d4ba9fafcdec7e4481c01060f6bad5d7b954ef5f8f2`; file SHA-256 `890c8517b6ae7859b6dd794aab486c27c2ce3b82f7c87e84884c7006825340bf`.
- Roster-manifest file SHA-256: `c8a63c77da929aeb6d41df8fac6c5751b525ce1d916cbcb78af9b2b9f5737bb1`.
- Discovery-manifest file SHA-256: `7cfdf2e0304e799e831e64dae11f7a51142a90236537d551a7200029f87506c8`.
- Readiness file SHA-256: `87635076ea3676ad79824cf1e687dbdf5717c793717f676133ff4e33fe53adea`.
- Source-provenance file SHA-256: `e959e9ff1f7b723bd66641a069a7e610a900e326bc6694a5240c929635bad812`; sealed content identity `3d9cd1515d715b0b74208f5d3d8d94f501f4b7a637d82a3665a5460e8f757fca`. It inventories 43 selected source files / 7,379,181 bytes and independently derives the complete provider-page count below.
- Replay-gate-audit file SHA-256: `c6a40c002548ba80773c00fdfb126196e079e46cfdf34a99ffe8f7a67705bcb4`; sealed content identity `496f30f362ff67839f8d2c8cfd8535251eec410faf0e30317069d26ca7ef5542`. It contains 100 one-to-one normalization receipts, all `quarantined`.

The corrected provider-page root contains 43 files / 7,383,314 bytes. For comparison, the rejected root actually contains 1,913 files / **10,661,000 bytes**; the previously tracked 10,659,986-byte statement was incorrect.

## Provider and parser provenance

- OpenDota Explorer: exact 16-ID query, one bounded response ordered by match ID, retrieved `2026-08-14T04:16:34Z`, 1,254 rows, response SHA-256 `946dbf9247f037e20c6bfa174174222fcf180c4ee5ef441b2714d2ea0e952931`. Exact query, bounds, IDs, URL, and query hash are sealed in `provider-source.json` and the discovery manifest.
- OpenDota team pages: 32 complete endpoint responses (players + matches for each selected team), per-page URL/time/hash checkpoints, checkpoint SHA-256 `cf7af69f950f13e3522c4bc57d0cf7dc7e7ac49eecf356743f5063f0fcf74903`, latest retrieval `2026-08-14T04:24:11Z`.
- Liquipedia MediaWiki API: exact revision `2413904` at `2026-08-11T23:23:30Z`, retrieved `2026-08-14T04:08:24Z`, one immutable page, response SHA-256 `ecb0f2788618434851d0463f7360516dc0fe43a9aa1901593739d46c4ee8fdca`. The exact endpoint is sealed in source provenance.
- Valve public replay CDN: 880-object presence manifest SHA-256 `03201eb8751c958302fea0495ca6e19cd569cf7c8936cb86b54811271b79187e`; public object presence is not authenticity or parser evidence.
- Replay sample: selection SHA-256 `e4b9336940da2bd7a39a9a3d16e0fd4eeac25927dd68cf6b08da36c995271457`; 100 terminal checkpoints; 72 bzip2 and 28 zstd payloads; 4,734,122,001 compressed bytes and 7,730,318,570 decompressed bytes. Incomplete transport parts were restarted from byte zero; only hash-verified atomic completions were reused.
- Parser: `github.com/dotabuff/manta v1.5.0`; independent comparison SHA-256 `2904a5f889c960e7ef2003e4d6c6622c32d5ca5ee3b8796742d4534c1aec2ea0`. Runs A/B each report 100 succeeded, 0 failed; resume kept every attempt count at one and reparsed zero files.

Parser run A used 117.40 s user / 6.29 s system / 1:49.17 elapsed and 108,472 KiB peak RSS. Run B used 117.65 s / 6.18 s / 1:39.47 and 116,440 KiB. Parser resume used 2.85 s / 0.70 s / 3.53 s and 23,616 KiB. Final evidence runs A/B used 6.03/6.11 s elapsed and 28,792/28,536 KiB peak RSS; the no-write resume used 5.93 s and 28,780 KiB.

## Verification

All of the following passed on the direct successor worktree. The focused command includes the rejected-ID, unresolved-scope, former-player, sealed-report, and generated provider-byte-count regressions. The final full race run completed in 8:08, including `cmd/history` in 487.021 s.

```sh
go test -count=1 -run 'Test(ReconcileDOT54DiscoveryRejectsCandidateOutsideResolvedAllowSet|UnresolvedScopeDoesNotEmitHistoricalNoGoCandidate|ParseDOT54TournamentRosterUsesCutoffRolesAndRejectsFormer|DOT54ReportIsDerivedFromSealedReadiness|SourceProvenanceDerivesProviderPageByteCount)' ./cmd/history
go test -count=1 ./...
CGO_ENABLED=1 CC="zig cc" go test -race -count=1 ./...
go vet ./...
go build ./...
go mod verify
git diff --check
```

## Reproduce, audit, and clean up

Run from this report's exact repository commit while the immutable external evidence root is retained:

```sh
DOT54_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166"
go test -count=1 ./cmd/history
go build -o "$DOT54_ROOT/bin/history-dot54" ./cmd/history
"$DOT54_ROOT/bin/history-dot54" dot54-evidence \
  --source-root "$DOT54_ROOT/source" \
  --replay-evidence-root "$DOT54_ROOT" \
  --data-dir "$DOT54_ROOT/final-v4/run-a"
```

The resume must report `created=0 reused=9` with the index and tree hashes above. For an independent clean run, point `--data-dir` at a new empty directory; immutable writes fail closed on any content conflict. The replay selection, every download terminal transition, parser manifests, both parser outputs, and comparison receipt remain under the same mode-`0700` root for audit.

After independent review, remove only the named correction root:

```sh
DOT54_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166"
test "$DOT54_ROOT" = "${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166"
rm -r -- "$DOT54_ROOT"
```

The smallest safe successor would add a metadata source that can bind all ten public participant identities and the replay build to each parsed demo. Until that evidence passes normalization and the per-family minima, all historical families remain disabled.
