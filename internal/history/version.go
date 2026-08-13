package history

// Pinned M1 provenance versions embedded in history-owned artifacts so an
// adapter/schema upgrade changes the deterministic content hash and every
// published artifact self-describes the code that produced it.
const (
	AdapterName     = "internal/history"
	AdapterVersion  = "m1.v1"
	RosterSchema    = "history.roster.v1"
	DiscoverySchema = "history.discovery.v1"
	FactsSchema     = "history.facts.v1"
	AggregateSchema = "history.aggregate.v1"
	StageSchema     = "history.stage.v1"
	ReadinessSchema = "history.readiness.v1"
)

// DiscoveryContractVersion names the frozen provider/query/page contract. It
// is mirrored in TournamentScopeV1.DiscoveryContractVersion so a scope binds
// to exactly one discovery shape.
const DiscoveryContractVersion = "discovery.v1"

// Readiness outcomes match TournamentScopeV1.Discovery.AllowedOutcomes so the
// gate cannot silently invent a fourth state.
const (
	ReadinessFullHistoryGo   = "full_history_go"
	ReadinessRestrictedGo    = "restricted_history_go"
	ReadinessHistoricalNoGo  = "historical_no_go"
)

// Stage names for the M1 batch state machine. Order is meaningful: each stage
// advances one artifact closer to a sealed snapshot.
const (
	StageDiscovery    = "discovery"
	StageAcquisition = "acquisition"
	StageVerification = "verification"
	StageParse        = "parse"
	StageNormalize   = "normalize"
	StageAggregate    = "aggregate"
)

// Source providers, pinned in the discovery contract. Steam/public tournament
// metadata is preferred; OpenDota public metadata is second; the Valve replay
// CDN is only a replay-byte source used after metadata validation.
const (
	ProviderSteam     = "steam"
	ProviderOpenDota  = "opendota"
	ProviderValveCDN  = "valve_cdn"
	ProviderManual    = "manual_local"
)