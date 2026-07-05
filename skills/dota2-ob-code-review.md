# Dota2-OB Code Review

Use this skill for reviewing Dota2-OB code changes.

Review priorities:

- Spec conformance.
- Correctness of Dota 2 telemetry interpretation.
- Null/missing-field handling.
- Schema versioning and raw data retention.
- Test coverage for parsers, normalizers, and analytics.
- Security and secret handling.
- Avoidance of anti-cheat-sensitive or account-risky methods unless explicitly approved.

Output findings first, ordered by severity. Use file/line references when possible.

If no blocking issues are found, state residual risk and test gaps clearly.
