// Package commitlog persists the complete causal PolicyCommitV1 as the only
// authoritative policy-plane record. Append returns a Committed value only
// after the containing segment is synced. Callers must treat ErrSealed and
// ErrCorrupt as deterministic hidden-output, fail-closed states.
//
// Frames are length-prefixed canonical JSON followed by SHA-256 and a fixed
// commit marker. Segments are protected with user-only permissions and rotate
// before exceeding MaxSegmentBytes. A session fails closed before exceeding
// MaxSessionBytes or MaxSessionSegments, including when recovery finds an
// already-over-limit log. Checkpoints are replaceable caches: their loss or
// mismatch causes replay and never resets policy state.
package commitlog
