// Package history owns the reproducible historical data foundation: roster
// identity, bounded discovery, replay acquisition/verification/parse/normalize
// orchestration, batch state, normalized replay facts, aggregation, sealed
// prematch snapshot/baseline publication, and the full/restricted/no-go
// readiness gate.
//
// The package is pure domain logic. It imports only the standard library and
// internal/contracts; it performs no filesystem, network, replay-parser,
// database, live-policy, or presentation work. Adapters in internal/replay
// and the cmd composition root wire concrete HTTP/download/parse/storage
// behaviors to the pure seams declared here.
//
// Every persisted artifact has a schema version, provenance, deterministic
// content identity (SHA-256), nullability, sample definition, patch, and
// observation period. Missing values remain missing; unknown identity, stale
// history, and low sample size are modeled states, not guessed values.
package history