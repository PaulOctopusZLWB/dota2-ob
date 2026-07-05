# Dota2-OB Review Checklist

Use code-review stance. Findings lead.

## Correctness

- Does the change satisfy the spec exactly?
- Are data-source assumptions explicit and defensible?
- Are latency, null/missing fields, and reconnect cases handled?
- Are Dota 2 field names and coordinate systems treated as versioned inputs?

## Reliability

- Are raw observations persisted before lossy transformation?
- Are retries/backoff bounded?
- Are partial data and API failures represented clearly?
- Can the system resume after process restart?

## Security And Risk

- No Steam keys, tokens, cookies, or local account secrets in files or logs.
- No memory scraping, packet sniffing, or anti-cheat-sensitive approach unless the spec explicitly approves it.
- No side-effecting Multica mentions or assignments unless required.

## Tests

- Unit tests for parsing, normalization, and derived metrics.
- Integration tests or fixtures for GSI snapshots.
- Replay/backfill comparisons when available.
- Verification commands are reported with actual results.

## Maintainability

- Small modules with clear ownership.
- Structured parsers over ad hoc string parsing.
- Versioned schemas for collected data.
- Local docs updated when contracts change.
