# DOT-54 M1 terminal evidence candidate

Outcome: `historical_no_go_candidate` (independent exact-SHA review required; this is not self-acceptance).

The corrected, conflict-resolved population contains 1,254 matches in the inclusive 180-day window ending `2026-08-12T00:00:00Z`. Its exhaustive terminal counts are 367 `excluded_patch_mismatch`, 7 `replay_metadata_missing`, 100 `replay_identity_quarantined`, and 780 `gate_target_not_selected`. The rejected legacy Nigma ID `7554697` is absent from the sealed 16-team allow-set.

The cutoff-effective `TournamentScopeV1` contains 16 teams and 80 players. An immutable pre-cutoff Liquipedia revision supplies dated tournament registration, positions, and membership intervals; it includes Topson and marks TaiLung former, so TaiLung is excluded. The Perfect World tournament asset supplies the primary field and handles. Hash-verified OpenDota team pages bind those handles to Valve-derived stable team/account IDs. Liquipedia is community-contributed corroboration for membership and effective dates only, not independent Valve stable-ID authority. DatDota was not used.

The governing numerical gate was then evaluated on a deterministic 100-replay sample: every selected team appears at least five times, all 100 replay objects are byte-verified, and two independent manta v1.5.0 runs parsed all 100 with byte-identical fact identities. None of the 100 facts can satisfy the accepted full metadata-to-parser participant/build identity correlation, so zero normalized replays and zero aggregate cells are publishable. This is the numerical reason for the no-go candidate; it is not an unresolved-scope shortcut.

## Immutable evidence

- Canonical external root: `${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166`, mode `0700`. Raw/generated data remains outside git.
- Independent sealed roots: `final-v2/run-a` and `final-v2/run-b`; both contain 9 files / 381,877 bytes.
- Evidence-index SHA-256: `3acf70a7a1b2f4e8b260abcdaa0f0b9b4d7b0441250919ac1b89704e5561218c`.
- Artifact-tree SHA-256: `a79eed2ff7c4894d9df28658611d91a948633723e470a19a68639fcef50d3ff5`.
- `TournamentScopeV1` scope ID: `da84de9204dc8493c07a0d4ba9fafcdec7e4481c01060f6bad5d7b954ef5f8f2`; file SHA-256 `890c8517b6ae7859b6dd794aab486c27c2ce3b82f7c87e84884c7006825340bf`.
- Roster-manifest file SHA-256: `c8a63c77da929aeb6d41df8fac6c5751b525ce1d916cbcb78af9b2b9f5737bb1`.
- Discovery-manifest file SHA-256: `7cfdf2e0304e799e831e64dae11f7a51142a90236537d551a7200029f87506c8`.
- Readiness file SHA-256: `87635076ea3676ad79824cf1e687dbdf5717c793717f676133ff4e33fe53adea`.
- Source-provenance file SHA-256: `9a18be08efc368f874ed2182edc4ce95d45fe4015fbd5f3a66c0375b807ab187`; it inventories 43 source files / 7,379,181 bytes.
- Replay-gate-audit file SHA-256: `852346ab4b7d93be154d564478b5bf7dd0e52a2541f7633150528113972f07bc`; sealed content identity `9727c74804403b551ed82f426abbc451b05eef8eacb510f11f309c1e6255143c`.

The corrected provider-page root contains 43 files / 7,383,314 bytes. For comparison, the rejected root actually contains 1,913 files / **10,661,000 bytes**; the previously tracked 10,659,986-byte statement was incorrect.

## Provider and parser provenance

- OpenDota Explorer: exact 16-ID query, one bounded response ordered by match ID, retrieved `2026-08-14T04:16:34Z`, 1,254 rows, response SHA-256 `946dbf9247f037e20c6bfa174174222fcf180c4ee5ef441b2714d2ea0e952931`. Exact query, bounds, IDs, URL, and query hash are sealed in `provider-source.json` and the discovery manifest.
- OpenDota team pages: 32 complete endpoint responses (players + matches for each selected team), per-page URL/time/hash checkpoints, checkpoint SHA-256 `cf7af69f950f13e3522c4bc57d0cf7dc7e7ac49eecf356743f5063f0fcf74903`, latest retrieval `2026-08-14T04:24:11Z`.
- Liquipedia MediaWiki API: exact revision `2413904` at `2026-08-11T23:23:30Z`, retrieved `2026-08-14T04:08:24Z`, one immutable page, response SHA-256 `ecb0f2788618434851d0463f7360516dc0fe43a9aa1901593739d46c4ee8fdca`. The exact endpoint is sealed in source provenance.
- Valve public replay CDN: 880-object presence manifest SHA-256 `03201eb8751c958302fea0495ca6e19cd569cf7c8936cb86b54811271b79187e`; public object presence is not authenticity or parser evidence.
- Replay sample: selection SHA-256 `e4b9336940da2bd7a39a9a3d16e0fd4eeac25927dd68cf6b08da36c995271457`; 100 terminal checkpoints; 72 bzip2 and 28 zstd payloads; 4,734,122,001 compressed bytes and 7,730,318,570 decompressed bytes. Incomplete transport parts were restarted from byte zero; only hash-verified atomic completions were reused.
- Parser: `github.com/dotabuff/manta v1.5.0`; independent comparison SHA-256 `2904a5f889c960e7ef2003e4d6c6622c32d5ca5ee3b8796742d4534c1aec2ea0`. Runs A/B each report 100 succeeded, 0 failed; resume kept every attempt count at one and reparsed zero files.

Parser run A used 117.40 s user / 6.29 s system / 1:49.17 elapsed and 108,472 KiB peak RSS. Run B used 117.65 s / 6.18 s / 1:39.47 and 116,440 KiB. Parser resume used 2.85 s / 0.70 s / 3.53 s and 23,616 KiB. Final evidence runs A/B each used 5.93 s elapsed and 27,752/28,520 KiB peak RSS; the no-write resume used 5.86 s and 28,712 KiB.

## Verification

All of the following passed on the direct successor worktree. The focused command includes the rejected-ID, unresolved-scope, former-player, and sealed-report regressions. The full race run completed in 8:01, including `cmd/history` in 479.307 s.

```sh
go test -count=1 -run 'Test(ReconcileDOT54DiscoveryRejectsCandidateOutsideResolvedAllowSet|UnresolvedScopeDoesNotEmitHistoricalNoGoCandidate|ParseDOT54TournamentRosterUsesCutoffRolesAndRejectsFormer|DOT54ReportIsDerivedFromSealedReadiness)' ./cmd/history
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
  --data-dir "$DOT54_ROOT/final-v2/run-a"
```

The resume must report `created=0 reused=9` with the index and tree hashes above. For an independent clean run, point `--data-dir` at a new empty directory; immutable writes fail closed on any content conflict. The replay selection, every download terminal transition, parser manifests, both parser outputs, and comparison receipt remain under the same mode-`0700` root for audit.

After independent review, remove only the named correction root:

```sh
DOT54_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166"
test "$DOT54_ROOT" = "${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/correction-d454166"
rm -r -- "$DOT54_ROOT"
```

The smallest safe successor would add a metadata source that can bind all ten public participant identities and the replay build to each parsed demo. Until that evidence passes normalization and the per-family minima, all historical families remain disabled.
