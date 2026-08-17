// Package version pins the replay pipeline's parser, adapter, and schema
// versions. Every persisted artifact embeds these values so that a parser,
// adapter, or schema upgrade deterministically changes the canonical output
// hash and cannot silently alter historical results.
package version

const (
	// ParserName identifies the replay extraction library.
	ParserName = "dotabuff/manta"
	// ParserVersion is the exact pinned extraction library version.
	ParserVersion = "v1.5.0"
	// AdapterName identifies this repository's replay adapter.
	AdapterName = "internal/replay"
	// AdapterVersion is the immutable adapter contract version.
	AdapterVersion = "stage3.v1"

	// RawSchema is the raw observation stream schema version.
	RawSchema = "replay.raw.v1"
	// FactsSchema is the normalized fact partition schema version.
	FactsSchema = "replay.facts.v1"
	// IdentitySchema is the identity/verification record schema version.
	IdentitySchema = "replay.identity.v1"
	// ClockSchema is the calibrated clock record schema version.
	ClockSchema = "replay.clock.v1"
	// PhaseSchema is the official phase stream schema version.
	PhaseSchema = "replay.phase.v1"
	// EpisodeSchema is the behavior episode schema version.
	EpisodeSchema = "replay.episodes.v1"
	// MetricsSchema is the V1/V2/V3 metric publication schema version.
	MetricsSchema = "replay.metrics.v2"
	// ScoreSchema is the radar/score snapshot schema version.
	ScoreSchema = "replay.score.v1"
	// CorrectionSchema is the review/correction overlay schema version.
	CorrectionSchema = "replay.corrections.v1"
	// RoleSchema is the nominal role registry schema version.
	RoleSchema = "ti2026.roles.v1"
	// ReportSchema is the match report schema version.
	ReportSchema = "replay.report.v2"

	// PhaseRuleVersion identifies the phase-engine rule set.
	PhaseRuleVersion = "ti2026.phase.v1"
	// EpisodeRuleVersion identifies the episode-builder rule set.
	EpisodeRuleVersion = "ti2026.episodes.v1"
	// LaneRuleVersion identifies the lane-assignment rule set.
	LaneRuleVersion = "ti2026.lanes.v1"
	// MetricsRuleVersion identifies the V1/V2/V3 metric computation rule set.
	MetricsRuleVersion = "ti2026.metrics.v4"
	// ScoreRuleVersion identifies the radar/score computation rule set.
	ScoreRuleVersion = "ti2026.scoring.v1"
	// CorrectionRuleVersion identifies the review/correction overlay rule set.
	CorrectionRuleVersion = "ti2026.corrections.v1"
)
