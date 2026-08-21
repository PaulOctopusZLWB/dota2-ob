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
	FactsSchema = "replay.facts.v2"
	// IdentitySchema is the identity/verification record schema version.
	IdentitySchema = "replay.identity.v1"
	// ClockSchema is the calibrated clock record schema version.
	ClockSchema = "replay.clock.v1"
	// PhaseSchema is the official phase stream schema version.
	PhaseSchema = "replay.phase.v1"
	// EpisodeSchema is the behavior episode schema version.
	EpisodeSchema = "replay.episodes.v1"
	// MetricsSchema is the V1/V2/V3 metric publication schema version.
	MetricsSchema = "replay.metrics.v6"
	// ClosureSchema is the 52-row metric closure contract version.
	ClosureSchema = "ti2026.metric-closure.v1"
	// ScoreSchema v6 persists match-qualified team-match aggregation entities
	// so every tournament child is independently resolvable and validated.
	ScoreSchema = "replay.score.v6"
	// CorrectionSchemaV1 is the legacy review/correction overlay schema
	// version; v1 documents are still readable and are migrated
	// deterministically to the current version on load.
	CorrectionSchemaV1 = "replay.corrections.v1"
	// CorrectionSchemaV2 is the legacy typed phase-operation schema.
	CorrectionSchemaV2 = "replay.corrections.v2"
	// CorrectionSchema is the review/correction overlay schema version.
	CorrectionSchema = "replay.corrections.v3"
	// RoleSchema is the nominal role registry schema version.
	RoleSchema = "ti2026.roles.v1"
	// ReportSchema is the match report schema version. v4 includes the immutable
	// source_nominal_role and complete override provenance (author/version) so a
	// report can carry both the public-source role and the effective role.
	ReportSchema = "replay.report.v4"

	// PhaseRuleVersion identifies the phase-engine rule set.
	PhaseRuleVersion = "ti2026.phase.v1"
	// EpisodeRuleVersion identifies the episode-builder rule set.
	EpisodeRuleVersion = "ti2026.episodes.v1"
	// LaneRuleVersion identifies the lane-assignment rule set.
	LaneRuleVersion = "ti2026.lanes.v1"
	// MetricsRuleVersion identifies the V1/V2/V3 metric computation rule set.
	MetricsRuleVersion = "ti2026.metrics.v12"
	// ScoreRuleVersion identifies the version-gated radar/score computation and
	// complete structured aggregation-lineage rule set.
	ScoreRuleVersion = "ti2026.scoring.v5"
	// CorrectionRuleVersion identifies the review/correction overlay rule set.
	CorrectionRuleVersion = "ti2026.corrections.v4"
	// CorrectionRuleVersionV3 is accepted only for recovery of a durable
	// transaction journal prepared by the immediately preceding release.
	CorrectionRuleVersionV3 = "ti2026.corrections.v3"
)
