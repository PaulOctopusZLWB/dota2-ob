package replay

// Pinned provenance versions embedded in ReplayFactsV1 and sidecars so a
// parser/adapter upgrade changes the deterministic output hash and so every
// facts artifact self-describes the code that produced it.
const (
	ParserName      = "dotabuff/manta"
	ParserVersion   = "v1.5.0"
	AdapterName     = "internal/replay"
	AdapterVersion  = "spike.v2"
	FactsSchema     = "replay.facts.spike.v2"
)

// FactsSchemaVersion is retained under its historical name for existing
// references; it is now an alias for FactsSchema.
const FactsSchemaVersion = FactsSchema