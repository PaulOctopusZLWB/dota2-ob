# DOT-54 M1 terminal evidence candidate

Outcome: `historical_no_go_candidate` (requires independent review; this is not self-acceptance).

The frozen discovery set contains 1,306 distinct matches in the inclusive 180-day window ending `2026-08-12T00:00:00Z`. All candidates have terminal classifications: 369 `excluded_patch_mismatch`, 7 `replay_metadata_missing`, and 930 `scope_identity_unverifiable`.

The official tournament asset identifies 16 teams and 80 player handles. Supplemental OpenDota current-member records produce unique stable account candidates for all 80 handles, but neither source proves roster effective intervals at the cutoff, and the official asset was retrieved after the cutoff. The tournament identity gate therefore remains unresolved. No replay body was downloaded or parsed. HTTP HEAD returned 200 for all 930 post-patch candidates with metadata, but object presence is not replay accessibility or parser evidence. All 16 teams and all historical cell families remain disabled.

## Immutable evidence

- Local data root: `$XDG_DATA_HOME/dota2-ob/dot-54/recovery-a49e161b` (mode `0700`; defaults to `$HOME/.local/share/...`).
- Independent final roots: `runs/run-g` and `runs/run-h` (`run-c` is an empty, classified failed attempt from the stricter asset-binding check).
- Evidence index SHA-256, both roots: `c7adf303d83dc0c8ee58a964b792a9183ad147be0e46bc729df1b8e3b299f643`.
- Artifact tree SHA-256, both roots and both resume runs: `55e50e5cb76f00e567922ff318d11f771a791ad071ecf5f7c6dc34aa21c39d16`.
- Discovery manifest SHA-256: `98f9c254260c57b117be9a4915f8320871a44b5a37bc46179e865e9457a32e96`.
- Tournament scope SHA-256: `5875e63492cb22462308f05cb8de714e4491271642686763318cda2db714e2d1`.
- Readiness SHA-256: `e6a5e5528b197ce96e9dcc7e94ea59c015ca650819d61be6914d5c0212d8f228`.
- Source provenance SHA-256: `3b310c3c47f33a9004726a55c99a9c96308ebdb27f6a792126296726da91c837` (44 files, including all 34 team player/match pages).

The official asset response SHA-256 is `ca2f0d9c464ff6abaaaca5a250bbea4c1aedbfc1200a93adc96e0bdc9592656d`. The reconciled OpenDota Explorer response SHA-256 is `045f8f59047ecfb789eea5ddb240a66196ec3eb59c1ba1ea2803016715d7fa27`; its exact query text, ordered 17-team ID set, and query SHA-256 `5d9f09c66d3ade44eb32bf54856754684c4db25da2a62820a5356cb7c29d7e09` are preserved in the discovery manifest. The Valve HEAD manifest SHA-256 is `6c0a672e2f704ac0aee3fcd5d9d973aedc2fee8b0b2e33b612e4a4f2d847dc45`; advertised sizes total 86,604,577,566 bytes. DatDota was not used.

Parser provenance is `github.com/dotabuff/manta v1.5.0`, with zero executions because the upstream scope gate failed.

## Reproduce and clean up

Build and run from the repository commit that contains this report:

```sh
DOT54_DATA_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/recovery-a49e161b"
go build -o "$DOT54_DATA_ROOT/bin/history-dot54" ./cmd/history
"$DOT54_DATA_ROOT/bin/history-dot54" dot54-evidence \
  --source-root "$DOT54_DATA_ROOT" \
  --data-dir "$DOT54_DATA_ROOT/runs/run-g"
```

An exact rerun against either final root reports `created=0 reused=6` and preserves both hashes. Each output root has 6 files / 323,285 bytes. Clean runs used 26,952–28,076 KiB peak RSS and 0.13–0.16 seconds wall time; resume runs used 25,984–26,756 KiB and 0.08 seconds. The checkpointed provider-page set has 1,913 files / 10,659,986 bytes.

After review, remove the external evidence with:

```sh
DOT54_DATA_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/dota2-ob/dot-54/recovery-a49e161b"
rm -r -- "$DOT54_DATA_ROOT"
```

The smallest safe successor is to obtain a timestamped primary registration record carrying stable team/account IDs and effective intervals at or before the cutoff. Only then should the 930 replay bodies proceed through acquisition, verification, and repeatable parsing.
