# Manual GSI Capture Runbook

## Safety Boundary

This procedure uses only Dota 2 Game State Integration POSTs to localhost, local files, and manual spectator actions. Do not use process memory reads, packet capture, injection, UI automation, matchmaking automation, credential storage, or hidden-state work.

## 1. Run Doctor

From the repository root, use a writable data root and the repository config:

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$PATH
DOTA2_OB_DOCTOR_DATA="$(mktemp -d)"
go run ./cmd/dota2-ob \
  --doctor \
  --addr 127.0.0.1:43210 \
  --data-dir "$DOTA2_OB_DOCTOR_DATA" \
  --gsi-config ./configs/gamestate_integration_dota2_ob.cfg
```

Doctor prints one JSON object. Its checks appear in this order: `listen_address`, `listen_available`, `data_root`, `dashboard_asset`, and `gsi_config`. `ok: true` means no required check failed. A missing automatically discovered Dota config is a warning; a missing or mismatched explicit `--gsi-config` is a failure. Doctor does not start the receiver or control Steam/Dota.

## 2. Install The GSI Config Manually

Copy `configs/gamestate_integration_dota2_ob.cfg` into one of these common Linux locations:

- `~/.local/share/Steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/`
- `~/.steam/steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/`

Create the `gamestate_integration` directory if needed. The config URI must exactly match the receiver, normally `http://127.0.0.1:43210/gsi`.

## 3. Start The Receiver

```bash
go run ./cmd/dota2-ob \
  --addr 127.0.0.1:43210 \
  --data-dir ./data/sessions \
  --stale-threshold 15s
```

Startup logs report the normalized loopback address, session ID, and session-relative `raw.jsonl` target. Wildcard, non-loopback, empty-host, and port-zero addresses are rejected before session data is created.

## 4. Check Local Operator Surfaces

```bash
curl -i http://127.0.0.1:43210/healthz
curl -s http://127.0.0.1:43210/api/status
```

Open `http://127.0.0.1:43210/`. Before the first accepted GSI snapshot, status is `waiting`.

## 5. Run A Manual Spectator Session

1. Launch Steam and Dota 2 manually.
2. Manually join a DotaTV or spectator match.
3. Observe the dashboard and `/api/status`.
4. Continue long enough to produce a useful raw session. Do not automate joining or account actions.

Expected states:

- `waiting`: no valid snapshot has been accepted.
- `receiving`: a raw snapshot was accepted within the stale threshold and no subsystem has an active failure.
- `stale`: the most recent accepted snapshot is at least the stale threshold old.
- `degraded`: raw, latest, profile, or analytics has an active failure. Accepted raw evidence still receives HTTP `200 ok` if only downstream processing failed.

## 6. Stop Manually

Press `Ctrl+C` (`SIGINT`) or send `SIGTERM`. The receiver stops accepting requests, drains in-flight requests and projections, closes the HTTP server, then closes the raw appender once. A clean stop logs `server_stopped`; stable failure codes include `server_shutdown_failed` and `raw_close_failed`.

## 7. Inspect Artifacts

The session is under `data/sessions/<session-id>/`. Inspect:

```bash
wc -l data/sessions/<session-id>/raw.jsonl
tail -n 1 data/sessions/<session-id>/raw.jsonl
```

Each committed line is a schema-version `2` envelope containing `session_id`, increasing `sequence`, `received_at`, `source: "gsi"`, `payload`, and semantically equivalent `raw`. A final newline is the commit marker. Derived files include `session_summary.md`, normalized ticks, events, and analytics summaries when their projections succeed.

## 8. Rebuild Offline

Raw JSONL is the recovery source of truth. Rebuild deterministic analytics after any downstream failure:

```bash
go run ./cmd/dota2-ob --analyze-session data/sessions/<session-id>
```

The reader supports MVP2 version-1 records and MVP3 version-2 records. It ignores only an unterminated final fragment and fails safely on terminated corruption or unknown versions.

## 9. Record Evidence-Gate Measurements

Evidence packaging remains deferred. For at least three useful sessions, including one long continuous run and one restart/disconnect/stale scenario, record:

- duration and raw bytes per minute;
- snapshot cadence and accepted/rejected/failure counters;
- schema, field, and nullability drift from `/api/profile`;
- reconnect, restart, stale, and degraded behavior;
- time and tools needed to rebuild derived artifacts;
- privacy-sensitive fields observed in raw payloads;
- match type, manual observation context, and observed DotaTV delay.

Do not upload or package raw sessions until the later evidence-archive decision defines sanitization and retention.
