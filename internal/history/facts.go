package history

import (
	"errors"
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// NormalizedMatchFacts is the history-domain normalized fact set for one
// completed match. It is the pure input to aggregation: identical bytes plus
// identical provenance yield identical facts and an identical content hash.
// It carries explicit presence/missingness rather than fabricated values, and
// an identity status so a mismatched match is quarantined before publication.
//
// Field availability follows the accepted manta completeness limits recorded
// in the replay spike. Unknown actor/entity fields remain unavailable until
// correctly reconstructed; this model never fabricates them.
type NormalizedMatchFacts struct {
	SchemaVersion   string             `json:"schema_version"`
	ContentSHA256   string             `json:"content_sha256"`
	MatchID         string             `json:"match_id"`
	ReplaySHA256    string             `json:"replay_sha256"`
	SourceEventTime time.Time          `json:"source_event_time"`
	PatchID         string             `json:"patch_id"`
	GameBuild       uint32             `json:"game_build,omitempty"`
	DurationSeconds *int64             `json:"duration_seconds,omitempty"`
	RadiantTeamID   string             `json:"radiant_team_id,omitempty"`
	DireTeamID      string             `json:"dire_team_id,omitempty"`
	RadiantWin      *bool              `json:"radiant_win,omitempty"`
	Participants    []ParticipantFacts `json:"participants"`
	IdentityStatus  string             `json:"identity_status"`
	Availability    FactsAvailability  `json:"availability"`
}

// ParticipantFacts is one participant's per-match metrics. Each scalar field
// is a pointer: nil means the source did not expose it (missingness), not
// zero. Zero is a real value only when the pointer is non-nil.
type ParticipantFacts struct {
	PersonID  string `json:"person_id"`
	TeamID    string `json:"team_id"`
	HeroName  string `json:"hero_name,omitempty"`
	Role      string `json:"role,omitempty"`
	Slot      int    `json:"slot"`
	Kills     *int64 `json:"kills,omitempty"`
	Deaths    *int64 `json:"deaths,omitempty"`
	Assists   *int64 `json:"assists,omitempty"`
	GPM       *int64 `json:"gpm,omitempty"`
	XPM       *int64 `json:"xpm,omitempty"`
	LastHits  *int64 `json:"last_hits,omitempty"`
	Denies    *int64 `json:"denies,omitempty"`
	NetWorth  *string `json:"net_worth,omitempty"`
	Level     *int64 `json:"level,omitempty"`
	// FarmCheckpoints maps minute boundary -> net worth at that minute. Source
	// the entity-state reconstruction (M1 deferred). Empty when unavailable.
	FarmCheckpoints map[int64]string `json:"farm_checkpoints,omitempty"`
	// KeyItemTimings maps item name -> completion game-second. Named item
	// purchases require entity inventory (M1 deferred). Empty when unavailable.
	KeyItemTimings map[string]int64 `json:"key_item_timings,omitempty"`
}

// FactsAvailability records which metric families the normalized facts expose,
// which are feasible-but-deferred, and which are unavailable inside the safety
// boundary. Integration adapters populate this so a missing value is never
// confused with an absent family.
type FactsAvailability struct {
	Available   []string `json:"available"`
	Deferred    []string `json:"deferred"`
	Unavailable []string `json:"unavailable"`
}

// Metrics enumerates the aggregate metrics M1 supports. Values below a cell's
// minimum sample remain unavailable, not low-confidence prose.
const (
	MetricGames             = "games"
	MetricWins              = "wins"
	MetricKills              = "kills"
	MetricDeaths             = "deaths"
	MetricAssists            = "assists"
	MetricGPM                = "gpm"
	MetricXPM                = "xpm"
	MetricLastHits           = "last_hits"
	MetricDenies             = "denies"
	MetricNetWorth           = "net_worth"
	MetricLevel              = "level"
	MetricKillParticipation  = "kill_participation"
	MetricFarmCheckpoint    = "farm_checkpoint"
	MetricKeyItemTiming      = "key_item_timing"
)

// RequiredMetric returns whether a metric requires source coverage to be
// published. All metrics require at least one non-missing observation; the
// per-cell minimum is enforced by aggregate.go.
func RequiredMetric(metric string) bool {
	switch metric {
	case MetricGames, MetricWins, MetricKills, MetricDeaths, MetricAssists,
		MetricGPM, MetricXPM, MetricLastHits, MetricDenies, MetricNetWorth,
		MetricLevel, MetricKillParticipation, MetricFarmCheckpoint, MetricKeyItemTiming:
		return true
	}
	return false
}

// Cell minimum samples per the program spec. Draft cells need >=5 eligible
// matches; lane/item distribution cells need >=8 eligible observations;
// team-level comparative cells need >=10 eligible matches.
const (
	MinDraftCell      = 5
	MinLaneItemCell   = 8
	MinTeamCompCell   = 10
)

// Validate checks the normalized facts are self-consistent. It does NOT
// re-validate the replay bytes; content identity is asserted by sealing.
func (f NormalizedMatchFacts) Validate() error {
	if f.SchemaVersion != FactsSchema {
		return schemaErr(FactsSchema, f.SchemaVersion)
	}
	if f.ContentSHA256 == "" || !isSHA(f.ReplaySHA256) || f.MatchID == "" || f.SourceEventTime.IsZero() || f.PatchID == "" {
		return errors.New("invalid normalized facts identity")
	}
	if f.IdentityStatus != "" && f.IdentityStatus != contracts.IdentityVerified && f.IdentityStatus != contracts.IdentityQuarantined {
		return errors.New("invalid normalized facts identity status")
	}
	personSeen := map[string]bool{}
	for _, p := range f.Participants {
		if p.PersonID == "" || personSeen[p.PersonID] {
			return errors.New("invalid normalized participant")
		}
		personSeen[p.PersonID] = true
	}
	if !sort.SliceIsSorted(f.Participants, func(i, j int) bool { return f.Participants[i].PersonID < f.Participants[j].PersonID }) {
		return errors.New("normalized participants not ordered")
	}
	return verifySealedFacts(f)
}

// SealNormalizedMatchFacts computes and sets the content identity. Identity
// status is preserved (verified or quarantined) so quarantine survives sealing.
func SealNormalizedMatchFacts(f *NormalizedMatchFacts) error {
	if f == nil {
		return errors.New("nil normalized facts")
	}
	f.ContentSHA256 = ""
	hash, err := contentSHA256("facts", *f)
	if err != nil {
		return err
	}
	f.ContentSHA256 = hash
	return f.Validate()
}

func verifySealedFacts(f NormalizedMatchFacts) error {
	got := f.ContentSHA256
	if !isSHA(got) {
		return errors.New("facts content identity missing")
	}
	f.ContentSHA256 = ""
	want, err := contentSHA256("facts", f)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("facts content identity mismatch")
	}
	return nil
}

// int64Ptr, boolPtr, strPtr are small helpers for building facts in tests and
// adapters without exposing pointer literals everywhere.
func int64Ptr(v int64) *int64 { return &v }
func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

func decimalFromInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// SumExpectedMetrics lists the metric families a participant is expected to
// expose for full aggregation. Adapters check this against Availability so
// missingness is reported by family.
func SumExpectedMetrics() []string {
	return []string{
		MetricKills, MetricDeaths, MetricAssists, MetricGPM, MetricXPM,
		MetricLastHits, MetricDenies, MetricNetWorth, MetricLevel,
		MetricKillParticipation, MetricFarmCheckpoint, MetricKeyItemTiming,
	}
}