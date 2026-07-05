# Manual GSI Test

## Safety Boundary

This procedure uses only Dota 2 Game State Integration POSTs to localhost, local files, and manual spectator actions. Do not use process memory reads, packet capture, injection, UI automation, matchmaking automation, or credential storage.

## Install The GSI Config On Linux

Copy `configs/gamestate_integration_dota2_ob.cfg` into the Dota 2 GSI config directory.

Common Steam Linux paths:

- `~/.local/share/Steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/`
- `~/.steam/steam/steamapps/common/dota 2 beta/game/dota/cfg/gamestate_integration/`

Create the `gamestate_integration` directory if it does not exist.

## Start The Receiver

From the repository root:

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$PATH
go run ./cmd/dota2-ob --addr 127.0.0.1:43210 --data-dir ./data/sessions
```

Check health from another shell:

```bash
curl -i http://127.0.0.1:43210/healthz
```

Open the local dashboard:

```text
http://127.0.0.1:43210/
```

## Run A Spectator Session

1. Launch Steam and Dota 2 manually.
2. Manually join a DotaTV or spectator match.
3. Observe for 5-10 minutes for the first MVP validation run.
4. Stop the receiver with `Ctrl+C`.

## Inspect Artifacts

The receiver logs the session ID on startup. Raw snapshots are written to:

```text
data/sessions/<session-id>/raw.jsonl
```

Each line should parse as one JSON object with:

- `received_at`
- `payload`
- `raw`

Inspect the latest state and field profile:

```bash
curl -s http://127.0.0.1:43210/api/latest
curl -s http://127.0.0.1:43210/api/profile
```

The session summary is written to:

```text
data/sessions/<session-id>/session_summary.md
```

For the MVP validation run, record:

- account used,
- match type,
- observation duration,
- whether `raw.jsonl` was created,
- whether `/api/latest` shows hero/player sections,
- whether `/api/profile` shows expected field paths,
- whether `session_summary.md` answers the required availability questions,
- whether the file grows while spectating,
- any observed DotaTV delay.
