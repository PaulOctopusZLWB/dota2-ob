# M4 ordinary-public-match systems rehearsal

This optional path exercises the accepted live systems on one complete publicly
spectatable DotaTV match. It is structurally non-acceptance: every artifact binds
`claims_p4:false`, `qualifying_match:false`, `acceptance_eligible:false`, and
`acceptance_gate:"none"`. It cannot arm, update, mention, reopen, or satisfy an
acceptance issue. A later P4 attempt always uses a fresh isolated P4 root.

Use two fresh roots under `/var/tmp`; Dota and OBS must be stopped for preflight:

```sh
go run -buildvcs=true ./cmd/m4-match preflight --purpose public_match_rehearsal --match-class public_match --data-root /var/tmp/dot77-rehearsal-preflight-a
go run -buildvcs=true ./cmd/m4-match verify --purpose public_match_rehearsal --match-class public_match --data-root /var/tmp/dot77-rehearsal-preflight-a --expect preflight
go run -buildvcs=true ./cmd/m4-match preflight --purpose public_match_rehearsal --match-class public_match --data-root /var/tmp/dot77-rehearsal-preflight-b
go run -buildvcs=true ./cmd/m4-match verify --purpose public_match_rehearsal --match-class public_match --data-root /var/tmp/dot77-rehearsal-preflight-b --expect preflight
cmp /var/tmp/dot77-rehearsal-preflight-{a,b}/evidence/readiness.json
cmp /var/tmp/dot77-rehearsal-preflight-{a,b}/evidence/canonical/evidence-index.json
```

The verifier must reject either rehearsal root when invoked with
`--purpose p4_acceptance --match-class public_tournament`.

The live identity contains the nonzero DotaTV match ID, a current confirmation
time, the manually launched Dota process identity, and a public `dota2.com`
watch URL. Tournament, series, and game are ignored and sealed as
`unavailable_for_public_match`; sides are sealed as `radiant` and `dire`.

```sh
go run -buildvcs=true ./cmd/m4-match live --purpose public_match_rehearsal --match-class public_match \
  --readiness-root /var/tmp/dot77-rehearsal-preflight-a \
  --data-root /var/tmp/dot77-rehearsal-live-1234567890 \
  --identity /var/tmp/dot77-public-match-identity.json
```

The same four manual actions apply:

1. Manually launch Dota 2 within the assigned start window (maximum 30 minutes).
2. After `REHEARSAL_READY`, join the identified public DotaTV match before `0:00`
   and confirm its match ID and isolated OBS preview.
3. Confirm preview; approve or record the deterministic ineligible result;
   reject or record it; pin then unpin when eligible; emergency-hide then clear.
4. Remain through normal post-game and recording finalization.

Late join, identity drift, evidence gaps, unsafe output, failed recovery, or
incomplete OBS finalization seals `rehearsal_failed`. A technically complete run
seals `rehearsal_complete`, never an acceptance result. Keep the private raw
root for independent review; share only canonical value-free evidence and hashes.

Verify a completed root only with the rehearsal terminal verifier:

```sh
go run -buildvcs=true ./cmd/m4-match verify --purpose public_match_rehearsal --match-class public_match \
  --data-root /var/tmp/dot77-rehearsal-live-1234567890 --expect rehearsal_complete
```

The verifier regenerates `FieldCoverageDeltaV1` from the exact captured-schedule
baseline and exact rehearsal raw V3 frames, checks the typed acceptance N/A set
and suppression audits, and rejects the root under the P4 verifier.
