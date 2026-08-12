// Package contracts defines the versioned, source-specific ports shared by the
// historical, insight, policy, and presentation tracks. It has no adapter,
// storage, HTTP, replay-parser, localization, or OBS dependencies.
package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	TournamentScopeSchemaV1            = "tournament_scope.v1"
	LiveObservationSchemaV1            = "live_observation.v1"
	HistoricalSnapshotManifestSchemaV1 = "historical_snapshot_manifest.v1"
	HistoricalBaselineSchemaV1         = "historical_baseline.v1"
	InsightCandidateSchemaV1           = "insight_candidate.v1"
	BroadcastDecisionSchemaV1          = "broadcast_decision.v1"
	OverlayStateSchemaV1               = "overlay_state.v1"
	OperatorCommandSchemaV1            = "operator_command.v1"
	OperatorCommandResultSchemaV1      = "operator_command_result.v1"
	AuditEventSchemaV1                 = "audit_event.v1"
	PolicyCommitSchemaV1               = "policy_commit.v1"
	PolicyCheckpointSchemaV1           = "policy_checkpoint.v1"
	MaxLiveObservationBytes            = 1 << 20
	MaxHistoricalBaselineBytes         = 1 << 20
	MaxInsightCandidateBytes           = 64 << 10
	MaxBroadcastDecisionBytes          = 32 << 10
	MaxOverlayBytes                    = 64 << 10
	MaxAuditEventBytes                 = 16 << 10
	MaxPolicyCommitBytes               = 256 << 10
	MaxCheckpointCommandResults        = 4096
	MaxPreviewCandidates               = 64
	IdentityVerified                   = "verified"
	IdentityQuarantined                = "quarantined"
	ExclusionActiveMatch               = "active_match"
	CommandAccepted                    = "accepted"
	CommandRejected                    = "rejected"
	ActionApprove                      = "approve"
	ActionShow                         = "show"
	ActionReject                       = "reject"
	ActionPin                          = "pin"
	ActionUnpin                        = "unpin"
	ActionEmergencyHide                = "emergency_hide"
	ActionClearEmergencyHide           = "clear_emergency_hide"
	ActionDisableRule                  = "disable_rule"
	ActionEnableRule                   = "enable_rule"
	DecisionQueued                     = "queued"
	DecisionShown                      = "shown"
	DecisionRejected                   = "rejected"
	DecisionSuperseded                 = "superseded"
	DecisionExpired                    = "expired"
	DecisionPinned                     = "pinned"
	DecisionEmergencyHidden            = "emergency_hidden"
	PublicationPublish                 = "publish"
	PublicationUnchanged               = "unchanged"
	PublicationHide                    = "hide"
)

type Contract interface{ Validate() error }

func DecodeStrict(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

type Decimal string

func (d Decimal) Valid() bool { return decimalPattern.MatchString(string(d)) }

type ValueState string

const (
	ValuePresent     ValueState = "present"
	ValueAbsent      ValueState = "absent"
	ValueRedacted    ValueState = "redacted"
	ValueUnsupported ValueState = "unsupported"
	ValueInvalid     ValueState = "invalid"
)

type ObservedV1[T any] struct {
	State ValueState `json:"state"`
	Value *T         `json:"value,omitempty"`
}

func Present[T any](v T) ObservedV1[T] { return ObservedV1[T]{State: ValuePresent, Value: &v} }
func Absent[T any]() ObservedV1[T]     { return ObservedV1[T]{State: ValueAbsent} }

type EvidenceRefV1 struct {
	RecordSchemaVersion int               `json:"record_schema_version"`
	SessionID           string            `json:"session_id"`
	Sequence            uint64            `json:"sequence"`
	ReceiveTime         time.Time         `json:"receive_time"`
	Source              string            `json:"source"`
	ProviderVersion     ObservedV1[int64] `json:"provider_version"`
	RawPayloadSHA256    string            `json:"raw_payload_sha256"`
}

func (v EvidenceRefV1) validate(session string) error {
	if v.RecordSchemaVersion < 1 || v.SessionID == "" || v.Sequence == 0 || v.ReceiveTime.IsZero() || v.Source == "" || !isSHA(v.RawPayloadSHA256) || (session != "" && v.SessionID != session) {
		return errors.New("invalid evidence reference")
	}
	return validateObservedTree(v)
}

type SourceQualityV1 struct {
	Confidence string   `json:"confidence"`
	Flags      []string `json:"flags"`
}
type ProviderObservationV1 struct {
	Name      ObservedV1[string] `json:"name"`
	AppID     ObservedV1[int64]  `json:"appid"`
	Timestamp ObservedV1[int64]  `json:"timestamp"`
}
type VerifiedParticipantIdentityV1 struct {
	TournamentScopeID string `json:"tournament_scope_id"`
	RosterID          string `json:"roster_id"`
	PlayerID          string `json:"player_id"`
}
type MapObservationV1 struct {
	ClockTime                   ObservedV1[int64]   `json:"clock_time"`
	GameTime                    ObservedV1[int64]   `json:"game_time"`
	GameState                   ObservedV1[string]  `json:"game_state"`
	Paused                      ObservedV1[bool]    `json:"paused"`
	Daytime                     ObservedV1[bool]    `json:"daytime"`
	NightstalkerNight           ObservedV1[bool]    `json:"nightstalker_night"`
	WinTeam                     ObservedV1[string]  `json:"win_team"`
	RadiantScore                ObservedV1[int64]   `json:"radiant_score"`
	DireScore                   ObservedV1[int64]   `json:"dire_score"`
	RadiantGlyphCooldown        ObservedV1[Decimal] `json:"radiant_glyph_cooldown"`
	DireGlyphCooldown           ObservedV1[Decimal] `json:"dire_glyph_cooldown"`
	RadiantScanCharges          ObservedV1[int64]   `json:"radiant_scan_charges"`
	DireScanCharges             ObservedV1[int64]   `json:"dire_scan_charges"`
	RadiantLotusPoolCount       ObservedV1[int64]   `json:"radiant_lotus_pool_count"`
	DireLotusPoolCount          ObservedV1[int64]   `json:"dire_lotus_pool_count"`
	RadiantWardPurchaseCooldown ObservedV1[Decimal] `json:"radiant_ward_purchase_cooldown"`
	DireWardPurchaseCooldown    ObservedV1[Decimal] `json:"dire_ward_purchase_cooldown"`
}
type ObjectiveObservationV1 struct {
	State      ObservedV1[string]  `json:"state"`
	Location   ObservedV1[string]  `json:"location"`
	EndSeconds ObservedV1[Decimal] `json:"end_seconds"`
}
type BuildingObservationV1 struct {
	Team      string              `json:"team"`
	Name      string              `json:"name"`
	Health    ObservedV1[Decimal] `json:"health"`
	MaxHealth ObservedV1[Decimal] `json:"max_health"`
}
type ItemObservationV1 struct {
	Slot        string              `json:"slot"`
	Name        ObservedV1[string]  `json:"name"`
	ItemLevel   ObservedV1[int64]   `json:"item_level"`
	Cooldown    ObservedV1[Decimal] `json:"cooldown"`
	MaxCooldown ObservedV1[Decimal] `json:"max_cooldown"`
	CanCast     ObservedV1[bool]    `json:"can_cast"`
	Charges     ObservedV1[int64]   `json:"charges"`
	Passive     ObservedV1[bool]    `json:"passive"`
}
type AbilityObservationV1 struct {
	Slot        string              `json:"slot"`
	Name        ObservedV1[string]  `json:"name"`
	Level       ObservedV1[int64]   `json:"level"`
	Cooldown    ObservedV1[Decimal] `json:"cooldown"`
	MaxCooldown ObservedV1[Decimal] `json:"max_cooldown"`
	CanCast     ObservedV1[bool]    `json:"can_cast"`
	Passive     ObservedV1[bool]    `json:"passive"`
	Ultimate    ObservedV1[bool]    `json:"ultimate"`
}
type ParticipantObservationV1 struct {
	SessionSlot      string                         `json:"session_slot"`
	TeamKey          string                         `json:"team_key"`
	VerifiedIdentity *VerifiedParticipantIdentityV1 `json:"verified_identity,omitempty"`
	TeamName         ObservedV1[string]             `json:"team_name"`
	PlayerSlot       ObservedV1[int64]              `json:"player_slot"`
	HeroName         ObservedV1[string]             `json:"hero_name"`
	HeroID           ObservedV1[int64]              `json:"hero_id"`
	Gold             ObservedV1[Decimal]            `json:"gold"`
	NetWorth         ObservedV1[Decimal]            `json:"net_worth"`
	GPM              ObservedV1[int64]              `json:"gpm"`
	XPM              ObservedV1[int64]              `json:"xpm"`
	GoldReliable     ObservedV1[Decimal]            `json:"gold_reliable"`
	GoldUnreliable   ObservedV1[Decimal]            `json:"gold_unreliable"`
	Kills            ObservedV1[int64]              `json:"kills"`
	Deaths           ObservedV1[int64]              `json:"deaths"`
	Assists          ObservedV1[int64]              `json:"assists"`
	LastHits         ObservedV1[int64]              `json:"last_hits"`
	Denies           ObservedV1[int64]              `json:"denies"`
	XPos             ObservedV1[Decimal]            `json:"xpos"`
	YPos             ObservedV1[Decimal]            `json:"ypos"`
	Health           ObservedV1[Decimal]            `json:"health"`
	MaxHealth        ObservedV1[Decimal]            `json:"max_health"`
	HealthPercent    ObservedV1[Decimal]            `json:"health_percent"`
	Mana             ObservedV1[Decimal]            `json:"mana"`
	MaxMana          ObservedV1[Decimal]            `json:"max_mana"`
	ManaPercent      ObservedV1[Decimal]            `json:"mana_percent"`
	Alive            ObservedV1[bool]               `json:"alive"`
	RespawnSeconds   ObservedV1[Decimal]            `json:"respawn_seconds"`
	Level            ObservedV1[int64]              `json:"level"`
	XP               ObservedV1[Decimal]            `json:"xp"`
	BuybackCost      ObservedV1[Decimal]            `json:"buyback_cost"`
	BuybackCooldown  ObservedV1[Decimal]            `json:"buyback_cooldown"`
	Stunned          ObservedV1[bool]               `json:"stunned"`
	Silenced         ObservedV1[bool]               `json:"silenced"`
	Disarmed         ObservedV1[bool]               `json:"disarmed"`
	Hexed            ObservedV1[bool]               `json:"hexed"`
	Muted            ObservedV1[bool]               `json:"muted"`
	Break            ObservedV1[bool]               `json:"break"`
	HasDebuff        ObservedV1[bool]               `json:"has_debuff"`
	MagicImmune      ObservedV1[bool]               `json:"magicimmune"`
	Smoked           ObservedV1[bool]               `json:"smoked"`
	WardsPlaced      ObservedV1[int64]              `json:"wards_placed"`
	WardsDestroyed   ObservedV1[int64]              `json:"wards_destroyed"`
	WardsPurchased   ObservedV1[int64]              `json:"wards_purchased"`
	Items            []ItemObservationV1            `json:"items"`
	Abilities        []AbilityObservationV1         `json:"abilities"`
}
type LiveObservationV1 struct {
	SchemaVersion  string                     `json:"schema_version"`
	MappingVersion string                     `json:"mapping_version"`
	Evidence       EvidenceRefV1              `json:"evidence"`
	Provider       ProviderObservationV1      `json:"provider"`
	MatchID        ObservedV1[string]         `json:"match_id"`
	ClockBasis     string                     `json:"clock_basis"`
	Map            MapObservationV1           `json:"map"`
	Roshan         ObjectiveObservationV1     `json:"roshan"`
	Tormentor      ObjectiveObservationV1     `json:"tormentor"`
	Buildings      []BuildingObservationV1    `json:"buildings"`
	Participants   []ParticipantObservationV1 `json:"participants"`
	Quality        SourceQualityV1            `json:"quality"`
}

func (v LiveObservationV1) Validate() error {
	if v.SchemaVersion != LiveObservationSchemaV1 {
		return schemaError(LiveObservationSchemaV1, v.SchemaVersion)
	}
	if v.MappingVersion == "" || v.ClockBasis == "" || v.Quality.Confidence == "" || len(v.Participants) > 10 {
		return errors.New("invalid live observation")
	}
	if err := v.Evidence.validate(""); err != nil {
		return err
	}
	if err := validateObservedTree(v); err != nil {
		return err
	}
	if !sort.SliceIsSorted(v.Buildings, func(i, j int) bool {
		if v.Buildings[i].Team == v.Buildings[j].Team {
			return v.Buildings[i].Name < v.Buildings[j].Name
		}
		return v.Buildings[i].Team < v.Buildings[j].Team
	}) || !sort.SliceIsSorted(v.Participants, func(i, j int) bool {
		if v.Participants[i].TeamKey == v.Participants[j].TeamKey {
			return v.Participants[i].SessionSlot < v.Participants[j].SessionSlot
		}
		return v.Participants[i].TeamKey < v.Participants[j].TeamKey
	}) {
		return errors.New("live observation arrays not ordered")
	}
	return validateSize(v, MaxLiveObservationBytes)
}

type PublicSourceV1 struct {
	URL         string    `json:"url"`
	RetrievedAt time.Time `json:"retrieved_at"`
}
type TournamentTeamV1 struct {
	TeamID         string    `json:"team_id"`
	RosterID       string    `json:"roster_id"`
	EffectiveFrom  time.Time `json:"effective_from"`
	EffectiveUntil time.Time `json:"effective_until"`
}
type TournamentParticipantV1 struct {
	PersonID       string    `json:"person_id"`
	TeamID         string    `json:"team_id"`
	Handle         string    `json:"handle"`
	Role           string    `json:"role"`
	EffectiveFrom  time.Time `json:"effective_from"`
	EffectiveUntil time.Time `json:"effective_until"`
}
type DiscoveryPolicyV1 struct {
	ContractVersion         string   `json:"contract_version"`
	Providers               []string `json:"providers"`
	PageLimit               uint32   `json:"page_limit"`
	FullHistoryReplayTarget uint32   `json:"full_history_replay_target"`
	MinimumTeamMatches      uint32   `json:"minimum_team_matches"`
	AllowedOutcomes         []string `json:"allowed_outcomes"`
}
type TournamentScopeV1 struct {
	SchemaVersion            string                    `json:"schema_version"`
	ScopeID                  string                    `json:"scope_id"`
	ContentSHA256            string                    `json:"content_sha256"`
	Edition                  string                    `json:"edition"`
	SampledAt                time.Time                 `json:"sampled_at"`
	HistoryCutoff            time.Time                 `json:"history_cutoff"`
	DiscoveryContractVersion string                    `json:"discovery_contract_version"`
	PatchID                  string                    `json:"patch_id"`
	DotaPatch                string                    `json:"dota_patch"`
	Teams                    []TournamentTeamV1        `json:"teams"`
	Participants             []TournamentParticipantV1 `json:"participants"`
	Sources                  []PublicSourceV1          `json:"sources"`
	Discovery                DiscoveryPolicyV1         `json:"discovery"`
}

func (v TournamentScopeV1) Validate() error {
	if v.SchemaVersion != TournamentScopeSchemaV1 {
		return schemaError(TournamentScopeSchemaV1, v.SchemaVersion)
	}
	if v.ScopeID == "" || v.ScopeID != v.ContentSHA256 || v.Edition == "" || v.SampledAt.IsZero() || v.HistoryCutoff.IsZero() || v.DiscoveryContractVersion == "" || v.PatchID == "" || v.DotaPatch == "" || len(v.Teams) != 16 || len(v.Sources) == 0 {
		return errors.New("invalid tournament scope")
	}
	seen := map[string]bool{}
	for _, x := range v.Teams {
		if x.TeamID == "" || x.RosterID == "" || x.EffectiveFrom.IsZero() || !x.EffectiveUntil.After(x.EffectiveFrom) || seen[x.TeamID] {
			return errors.New("invalid tournament team")
		}
		seen[x.TeamID] = true
	}
	if !sort.SliceIsSorted(v.Teams, func(i, j int) bool { return v.Teams[i].TeamID < v.Teams[j].TeamID }) {
		return errors.New("tournament teams not ordered")
	}
	players := map[string]int{}
	for _, x := range v.Participants {
		if x.PersonID == "" || x.TeamID == "" || x.Handle == "" || (x.Role != "player" && x.Role != "coach") || x.EffectiveFrom.IsZero() || !x.EffectiveUntil.After(x.EffectiveFrom) || !seen[x.TeamID] {
			return errors.New("invalid tournament participant")
		}
		if x.Role == "player" {
			players[x.TeamID]++
		}
	}
	if !sort.SliceIsSorted(v.Participants, func(i, j int) bool { return v.Participants[i].PersonID < v.Participants[j].PersonID }) {
		return errors.New("tournament participants not ordered")
	}
	for team := range seen {
		if players[team] < 5 {
			return errors.New("tournament team has fewer than five players")
		}
	}
	for _, x := range v.Sources {
		if !strings.HasPrefix(x.URL, "https://") || x.RetrievedAt.IsZero() {
			return errors.New("invalid tournament source")
		}
	}
	if !sort.SliceIsSorted(v.Sources, func(i, j int) bool { return v.Sources[i].URL < v.Sources[j].URL }) {
		return errors.New("tournament sources not ordered")
	}
	if v.Discovery.ContractVersion != v.DiscoveryContractVersion || v.Discovery.PageLimit == 0 || v.Discovery.FullHistoryReplayTarget != 100 || v.Discovery.MinimumTeamMatches != 5 || !reflect.DeepEqual(v.Discovery.AllowedOutcomes, []string{"full_history_go", "historical_no_go", "restricted_history_go"}) || len(v.Discovery.Providers) == 0 || !sort.StringsAreSorted(v.Discovery.Providers) {
		return errors.New("invalid discovery policy")
	}
	return verifyContentHash(v, "scope")
}
func SealTournamentScopeV1(v *TournamentScopeV1) error {
	if v == nil {
		return errors.New("nil tournament scope")
	}
	v.ScopeID = ""
	v.ContentSHA256 = ""
	h, err := contentHash(*v, "scope")
	if err != nil {
		return err
	}
	v.ScopeID = h
	v.ContentSHA256 = h
	return v.Validate()
}

type LiveSessionBindingV1 struct {
	SessionID             string    `json:"session_id"`
	ActiveMatchID         string    `json:"active_match_id"`
	SessionStartTime      time.Time `json:"session_start_time"`
	TournamentScopeID     string    `json:"tournament_scope_id"`
	TournamentScopeSHA256 string    `json:"tournament_scope_sha256"`
}
type HistoricalMatchRefV1 struct {
	MatchID         string    `json:"match_id"`
	Completed       bool      `json:"completed"`
	SourceEventTime time.Time `json:"source_event_time"`
	ReplaySHA256    string    `json:"replay_sha256"`
	IdentityStatus  string    `json:"identity_status"`
}
type ExcludedMatchV1 struct {
	MatchID string `json:"match_id"`
	Reason  string `json:"reason"`
}
type HistoricalSnapshotManifestV1 struct {
	SchemaVersion           string                 `json:"schema_version"`
	SnapshotID              string                 `json:"snapshot_id"`
	ContentSHA256           string                 `json:"content_sha256"`
	TournamentScopeID       string                 `json:"tournament_scope_id"`
	TournamentScopeSHA256   string                 `json:"tournament_scope_sha256"`
	Binding                 LiveSessionBindingV1   `json:"binding"`
	CutoffTime              time.Time              `json:"cutoff_time"`
	MaximumSourceEventTime  time.Time              `json:"maximum_source_event_time"`
	SealedAt                time.Time              `json:"sealed_at"`
	DiscoveryManifestSHA256 string                 `json:"discovery_manifest_sha256"`
	InputManifestSHA256     string                 `json:"input_manifest_sha256"`
	ParserVersion           string                 `json:"parser_version"`
	AggregateVersion        string                 `json:"aggregate_version"`
	IncludedMatches         []HistoricalMatchRefV1 `json:"included_matches"`
	ExcludedMatches         []ExcludedMatchV1      `json:"excluded_matches"`
	BaselineContentSHA256   []string               `json:"baseline_content_sha256"`
}

func (v HistoricalSnapshotManifestV1) Validate() error {
	if v.SchemaVersion != HistoricalSnapshotManifestSchemaV1 {
		return schemaError(HistoricalSnapshotManifestSchemaV1, v.SchemaVersion)
	}
	if v.SnapshotID == "" || v.SnapshotID != v.ContentSHA256 || v.TournamentScopeID == "" || !isSHA(v.TournamentScopeSHA256) || v.Binding.SessionID == "" || v.Binding.ActiveMatchID == "" || v.Binding.SessionStartTime.IsZero() || v.Binding.TournamentScopeID != v.TournamentScopeID || v.Binding.TournamentScopeSHA256 != v.TournamentScopeSHA256 || v.CutoffTime.IsZero() || v.MaximumSourceEventTime.IsZero() || v.MaximumSourceEventTime.After(v.CutoffTime) || v.SealedAt.IsZero() || v.SealedAt.After(v.Binding.SessionStartTime) || !isSHA(v.DiscoveryManifestSHA256) || !isSHA(v.InputManifestSHA256) || v.ParserVersion == "" || v.AggregateVersion == "" || len(v.IncludedMatches) == 0 || len(v.BaselineContentSHA256) == 0 {
		return errors.New("invalid historical snapshot provenance")
	}
	included := map[string]bool{}
	var maximum time.Time
	for _, x := range v.IncludedMatches {
		if x.MatchID == "" || x.MatchID == v.Binding.ActiveMatchID || !x.Completed || x.SourceEventTime.IsZero() || x.SourceEventTime.After(v.CutoffTime) || x.IdentityStatus != IdentityVerified || !isSHA(x.ReplaySHA256) || included[x.MatchID] {
			return errors.New("ineligible historical match")
		}
		included[x.MatchID] = true
		if x.SourceEventTime.After(maximum) {
			maximum = x.SourceEventTime
		}
	}
	if !maximum.Equal(v.MaximumSourceEventTime) {
		return errors.New("maximum source event time mismatch")
	}
	if !sort.SliceIsSorted(v.IncludedMatches, func(i, j int) bool { return v.IncludedMatches[i].MatchID < v.IncludedMatches[j].MatchID }) {
		return errors.New("included matches not ordered")
	}
	activeExcluded := false
	for _, x := range v.ExcludedMatches {
		if x.MatchID == "" || x.Reason == "" || included[x.MatchID] {
			return errors.New("invalid excluded match")
		}
		if x.MatchID == v.Binding.ActiveMatchID && x.Reason == ExclusionActiveMatch {
			activeExcluded = true
		}
	}
	if !sort.SliceIsSorted(v.ExcludedMatches, func(i, j int) bool { return v.ExcludedMatches[i].MatchID < v.ExcludedMatches[j].MatchID }) || !sortedUnique(v.BaselineContentSHA256) {
		return errors.New("snapshot sets not ordered")
	}
	if !activeExcluded {
		return errors.New("active match not excluded")
	}
	for _, h := range v.BaselineContentSHA256 {
		if !isSHA(h) {
			return errors.New("invalid baseline content hash")
		}
	}
	if err := verifyContentHash(v, "snapshot"); err != nil {
		return err
	}
	return validateSize(v, MaxHistoricalBaselineBytes)
}
func (v HistoricalSnapshotManifestV1) ValidateAgainstScope(scope TournamentScopeV1) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := v.Validate(); err != nil {
		return err
	}
	if v.TournamentScopeID != scope.ScopeID || v.TournamentScopeSHA256 != scope.ContentSHA256 || !v.CutoffTime.Equal(scope.HistoryCutoff) {
		return errors.New("snapshot scope mismatch")
	}
	return nil
}
func SealHistoricalSnapshotManifestV1(v *HistoricalSnapshotManifestV1) error {
	if v == nil {
		return errors.New("nil snapshot")
	}
	v.SnapshotID = ""
	v.ContentSHA256 = ""
	h, err := contentHash(*v, "snapshot")
	if err != nil {
		return err
	}
	v.SnapshotID = h
	v.ContentSHA256 = h
	return v.Validate()
}

type HistoricalBaselineKeyV1 struct {
	RosterID         string `json:"roster_id"`
	PlayerID         string `json:"player_id,omitempty"`
	Role             string `json:"role,omitempty"`
	HeroID           string `json:"hero_id,omitempty"`
	Patch            string `json:"patch"`
	Metric           string `json:"metric"`
	Window           string `json:"window"`
	SampleDefinition string `json:"sample_definition"`
}
type HistoricalValueV1 struct {
	State             ValueState `json:"state"`
	Value             *Decimal   `json:"value,omitempty"`
	SampleSize        uint64     `json:"sample_size"`
	PeriodStart       time.Time  `json:"period_start"`
	PeriodEnd         time.Time  `json:"period_end"`
	SourceCoveragePPM uint32     `json:"source_coverage_ppm"`
}
type HistoricalBaselineV1 struct {
	SchemaVersion         string                  `json:"schema_version"`
	BaselineID            string                  `json:"baseline_id"`
	SnapshotID            string                  `json:"snapshot_id"`
	SnapshotContentSHA256 string                  `json:"snapshot_content_sha256"`
	TournamentScopeID     string                  `json:"tournament_scope_id"`
	TournamentScopeSHA256 string                  `json:"tournament_scope_sha256"`
	SessionID             string                  `json:"session_id"`
	ActiveMatchID         string                  `json:"active_match_id"`
	GeneratedTime         time.Time               `json:"generated_time"`
	Key                   HistoricalBaselineKeyV1 `json:"key"`
	Value                 HistoricalValueV1       `json:"value"`
}

func (v HistoricalBaselineV1) Validate() error {
	if v.SchemaVersion != HistoricalBaselineSchemaV1 {
		return schemaError(HistoricalBaselineSchemaV1, v.SchemaVersion)
	}
	if !isSHA(v.BaselineID) || !isSHA(v.SnapshotID) || v.SnapshotID != v.SnapshotContentSHA256 || v.TournamentScopeID == "" || !isSHA(v.TournamentScopeSHA256) || v.SessionID == "" || v.ActiveMatchID == "" || v.GeneratedTime.IsZero() || v.Key.RosterID == "" || v.Key.Patch == "" || v.Key.Metric == "" || v.Key.Window == "" || v.Key.SampleDefinition == "" || v.Value.PeriodStart.IsZero() || v.Value.PeriodEnd.Before(v.Value.PeriodStart) || v.Value.SourceCoveragePPM > 1_000_000 {
		return errors.New("invalid historical baseline")
	}
	if (v.Value.State == ValuePresent) != (v.Value.Value != nil) {
		return errors.New("invalid historical nullability")
	}
	if v.Value.Value != nil && !v.Value.Value.Valid() {
		return errors.New("invalid historical decimal")
	}
	want, err := baselineContentHash(v)
	if err != nil {
		return err
	}
	if v.BaselineID != want {
		return errors.New("baseline content identity mismatch")
	}
	return validateSize(v, MaxHistoricalBaselineBytes)
}
func SealHistoricalBaselineV1(v *HistoricalBaselineV1) error {
	if v == nil {
		return errors.New("nil baseline")
	}
	v.BaselineID = ""
	h, err := baselineContentHash(*v)
	if err != nil {
		return err
	}
	v.BaselineID = h
	return nil
}
func baselineContentHash(v HistoricalBaselineV1) (string, error) {
	payload := struct {
		GeneratedTime time.Time               `json:"generated_time"`
		Key           HistoricalBaselineKeyV1 `json:"key"`
		Value         HistoricalValueV1       `json:"value"`
	}{v.GeneratedTime, v.Key, v.Value}
	return contentHash(payload, "baseline")
}
func (v HistoricalBaselineV1) ValidateAgainst(m HistoricalSnapshotManifestV1) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if v.SnapshotID != m.SnapshotID || v.SnapshotContentSHA256 != m.ContentSHA256 || v.TournamentScopeID != m.TournamentScopeID || v.TournamentScopeSHA256 != m.TournamentScopeSHA256 || v.SessionID != m.Binding.SessionID || v.ActiveMatchID != m.Binding.ActiveMatchID || v.GeneratedTime.After(m.Binding.SessionStartTime) {
		return errors.New("baseline binding mismatch")
	}
	if !containsString(m.BaselineContentSHA256, v.BaselineID) {
		return errors.New("baseline absent from snapshot manifest")
	}
	return nil
}

type TypedParameterV1 struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	StringValue  *string  `json:"string_value,omitempty"`
	DecimalValue *Decimal `json:"decimal_value,omitempty"`
	BooleanValue *bool    `json:"boolean_value,omitempty"`
}
type ObservedMetricV1 struct {
	Name  string  `json:"name"`
	Value Decimal `json:"value"`
	Unit  string  `json:"unit"`
}
type SourceRequirementV1 struct {
	Source            string `json:"source"`
	MinimumConfidence string `json:"minimum_confidence"`
}
type InsightCandidateV1 struct {
	SchemaVersion      string                `json:"schema_version"`
	CandidateID        string                `json:"candidate_id"`
	SessionID          string                `json:"session_id"`
	RuleVersion        string                `json:"rule_version"`
	ConfigVersion      string                `json:"config_version"`
	SnapshotID         string                `json:"snapshot_id,omitempty"`
	LocalizationKey    string                `json:"localization_key"`
	Parameters         []TypedParameterV1    `json:"parameters"`
	Evidence           []EvidenceRefV1       `json:"evidence"`
	ObservedValues     []ObservedMetricV1    `json:"observed_values"`
	Confidence         string                `json:"confidence"`
	SampleSize         uint64                `json:"sample_size"`
	Priority           int                   `json:"priority"`
	CreatedTimeMS      int64                 `json:"created_time_ms"`
	ExpiryTimeMS       int64                 `json:"expiry_time_ms"`
	SourceRequirements []SourceRequirementV1 `json:"source_requirements"`
	Availability       string                `json:"availability"`
	Reason             string                `json:"reason,omitempty"`
}

func (v InsightCandidateV1) Validate() error {
	if v.SchemaVersion != InsightCandidateSchemaV1 {
		return schemaError(InsightCandidateSchemaV1, v.SchemaVersion)
	}
	if v.CandidateID == "" || v.SessionID == "" || v.RuleVersion == "" || v.ConfigVersion == "" || v.LocalizationKey == "" || v.ExpiryTimeMS < v.CreatedTimeMS {
		return errors.New("invalid insight candidate")
	}
	if v.SnapshotID != "" && !isSHA(v.SnapshotID) {
		return errors.New("invalid candidate snapshot identity")
	}
	if err := validateEvidenceOrder(v.Evidence, v.SessionID); err != nil {
		return err
	}
	for _, m := range v.ObservedValues {
		if m.Name == "" || m.Unit == "" || !m.Value.Valid() {
			return errors.New("invalid observed metric")
		}
	}
	return validateSize(v, MaxInsightCandidateBytes)
}

type BroadcastDecisionV1 struct {
	SchemaVersion  string `json:"schema_version"`
	DecisionID     string `json:"decision_id"`
	SessionID      string `json:"session_id"`
	PolicyRevision uint64 `json:"policy_revision"`
	CandidateID    string `json:"candidate_id,omitempty"`
	CommandID      string `json:"command_id,omitempty"`
	PriorState     string `json:"prior_state"`
	ResultingState string `json:"resulting_state"`
	PolicyTimeMS   int64  `json:"policy_time_ms"`
	Reason         string `json:"reason"`
}

func (v BroadcastDecisionV1) Validate() error {
	if v.SchemaVersion != BroadcastDecisionSchemaV1 {
		return schemaError(BroadcastDecisionSchemaV1, v.SchemaVersion)
	}
	if v.DecisionID == "" || v.SessionID == "" || v.PolicyRevision == 0 || !decisionStates[v.PriorState] || !decisionStates[v.ResultingState] || v.Reason == "" || (v.CandidateID == "" && v.CommandID == "") {
		return errors.New("invalid broadcast decision")
	}
	return validateSize(v, MaxBroadcastDecisionBytes)
}

type OverlayClaimV1 struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	AssetKey string `json:"asset_key,omitempty"`
}
type OverlayStateV1 struct {
	SchemaVersion     string          `json:"schema_version"`
	SessionID         string          `json:"session_id"`
	PublicationTimeMS int64           `json:"publication_time_ms"`
	StaleDeadlineMS   int64           `json:"stale_deadline_ms"`
	Visibility        string          `json:"visibility"`
	HealthCode        string          `json:"health_code,omitempty"`
	DecisionID        string          `json:"decision_id,omitempty"`
	Evidence          []EvidenceRefV1 `json:"evidence"`
	Confidence        string          `json:"confidence,omitempty"`
	SourceReceiveTime *time.Time      `json:"source_receive_time,omitempty"`
	SnapshotID        string          `json:"snapshot_id,omitempty"`
	Claim             *OverlayClaimV1 `json:"claim,omitempty"`
}

func (v OverlayStateV1) Validate() error {
	if v.SchemaVersion != OverlayStateSchemaV1 {
		return schemaError(OverlayStateSchemaV1, v.SchemaVersion)
	}
	if v.SessionID == "" || v.PublicationTimeMS <= 0 || v.StaleDeadlineMS < v.PublicationTimeMS || (v.Visibility != "visible" && v.Visibility != "hidden") {
		return errors.New("invalid overlay identity, clock, or visibility")
	}
	if v.Visibility == "hidden" && (v.Claim != nil || v.HealthCode == "") {
		return errors.New("invalid hidden overlay")
	}
	if v.Visibility == "visible" && (v.Claim == nil || v.DecisionID == "" || len(v.Evidence) == 0 || v.Confidence == "" || v.SourceReceiveTime == nil || v.SourceReceiveTime.IsZero()) {
		return errors.New("visible overlay missing evidence")
	}
	if v.SnapshotID != "" && !isSHA(v.SnapshotID) {
		return errors.New("invalid overlay snapshot identity")
	}
	if err := validateEvidenceOrder(v.Evidence, v.SessionID); err != nil {
		return err
	}
	if v.Claim != nil {
		if invalidOverlayText(v.Claim.Title) || invalidOverlayText(v.Claim.Body) || !assetPattern.MatchString(v.Claim.AssetKey) || utf8.RuneCountInString(v.Claim.Title) > 160 || utf8.RuneCountInString(v.Claim.Body) > 1024 {
			return errors.New("invalid overlay text or asset")
		}
	}
	return validateSize(v, MaxOverlayBytes)
}

type OperatorCommandV1 struct {
	SchemaVersion          string `json:"schema_version"`
	CommandID              string `json:"command_id"`
	SessionID              string `json:"session_id"`
	Action                 string `json:"action"`
	TargetCandidateID      string `json:"target_candidate_id,omitempty"`
	TargetRuleID           string `json:"target_rule_id,omitempty"`
	ExpectedPolicyRevision uint64 `json:"expected_policy_revision"`
	PolicyTimeMS           int64  `json:"policy_time_ms"`
}

func (v OperatorCommandV1) Validate() error {
	if v.SchemaVersion != OperatorCommandSchemaV1 || v.CommandID == "" || v.SessionID == "" || !commandActions[v.Action] || v.PolicyTimeMS < 0 {
		return errors.New("invalid operator command")
	}
	candidate := v.Action == ActionApprove || v.Action == ActionShow || v.Action == ActionReject || v.Action == ActionPin || v.Action == ActionUnpin
	rule := v.Action == ActionDisableRule || v.Action == ActionEnableRule
	if candidate != (v.TargetCandidateID != "") || rule != (v.TargetRuleID != "") || (!candidate && v.TargetCandidateID != "") || (!rule && v.TargetRuleID != "") {
		return errors.New("invalid command target")
	}
	return validateSize(v, 16<<10)
}

type OperatorCommandResultV1 struct {
	SchemaVersion     string   `json:"schema_version"`
	CommandID         string   `json:"command_id"`
	SessionID         string   `json:"session_id"`
	Status            string   `json:"status"`
	PreviousRevision  uint64   `json:"previous_revision"`
	ResultingRevision uint64   `json:"resulting_revision"`
	DecisionIDs       []string `json:"decision_ids"`
	Reason            string   `json:"reason"`
}

func (v OperatorCommandResultV1) Validate() error {
	if v.SchemaVersion != OperatorCommandResultSchemaV1 || v.CommandID == "" || v.SessionID == "" || (v.Status != CommandAccepted && v.Status != CommandRejected) || v.ResultingRevision < v.PreviousRevision || v.Reason == "" {
		return errors.New("invalid operator command result")
	}
	if v.Status == CommandRejected && v.ResultingRevision != v.PreviousRevision {
		return errors.New("rejected command mutated revision")
	}
	if v.Status == CommandAccepted && v.ResultingRevision != v.PreviousRevision+1 {
		return errors.New("accepted command revision is not monotonic")
	}
	return validateSize(v, 32<<10)
}

type AuditEventV1 struct {
	SchemaVersion string `json:"schema_version"`
	EventID       string `json:"event_id"`
	SessionID     string `json:"session_id"`
	EventType     string `json:"event_type"`
	PolicyTimeMS  int64  `json:"policy_time_ms"`
	CandidateID   string `json:"candidate_id,omitempty"`
	DecisionID    string `json:"decision_id,omitempty"`
	CommandID     string `json:"command_id,omitempty"`
	Reason        string `json:"reason"`
}

func (v AuditEventV1) Validate() error {
	if v.SchemaVersion != AuditEventSchemaV1 || v.EventID == "" || v.SessionID == "" || v.EventType == "" || v.PolicyTimeMS < 0 || v.Reason == "" || (v.CandidateID == "" && v.DecisionID == "" && v.CommandID == "") {
		return errors.New("invalid audit event")
	}
	return validateSize(v, MaxAuditEventBytes)
}

type PolicyCommitV1 struct {
	SchemaVersion           string                   `json:"schema_version"`
	SessionID               string                   `json:"session_id"`
	CommitSequence          uint64                   `json:"commit_sequence"`
	ObservationSequence     uint64                   `json:"observation_sequence,omitempty"`
	CommandID               string                   `json:"command_id,omitempty"`
	PriorPolicyRevision     uint64                   `json:"prior_policy_revision"`
	ResultingPolicyRevision uint64                   `json:"resulting_policy_revision"`
	PriorStateHash          string                   `json:"prior_state_hash,omitempty"`
	ResultingStateHash      string                   `json:"resulting_state_hash"`
	Decisions               []BroadcastDecisionV1    `json:"decisions"`
	CommandResult           *OperatorCommandResultV1 `json:"command_result,omitempty"`
	AuditEvents             []AuditEventV1           `json:"audit_events"`
	Publication             string                   `json:"publication"`
}

func (v PolicyCommitV1) Validate() error {
	if v.SchemaVersion != PolicyCommitSchemaV1 || v.SessionID == "" || v.CommitSequence == 0 || !isSHA(v.ResultingStateHash) || !publications[v.Publication] || v.ResultingPolicyRevision < v.PriorPolicyRevision || (v.ObservationSequence == 0) == (v.CommandID == "") || len(v.AuditEvents) == 0 {
		return errors.New("invalid policy commit")
	}
	if v.PriorPolicyRevision > 0 && !isSHA(v.PriorStateHash) {
		return errors.New("invalid prior state hash")
	}
	for i, x := range v.Decisions {
		if err := x.Validate(); err != nil {
			return fmt.Errorf("decisions[%d]: %w", i, err)
		}
		if x.SessionID != v.SessionID || x.PolicyRevision != v.ResultingPolicyRevision || (x.CommandID != "" && x.CommandID != v.CommandID) {
			return fmt.Errorf("decisions[%d] incoherent", i)
		}
	}
	if v.CommandResult != nil {
		if err := v.CommandResult.Validate(); err != nil || v.CommandResult.SessionID != v.SessionID || v.CommandResult.CommandID != v.CommandID || v.CommandResult.PreviousRevision != v.PriorPolicyRevision || v.CommandResult.ResultingRevision != v.ResultingPolicyRevision {
			return errors.New("command result incoherent")
		}
	} else if v.CommandID != "" {
		return errors.New("command commit missing result")
	}
	decisionIDs := map[string]bool{}
	for _, d := range v.Decisions {
		decisionIDs[d.DecisionID] = true
	}
	if v.CommandResult != nil {
		for _, id := range v.CommandResult.DecisionIDs {
			if !decisionIDs[id] {
				return errors.New("command result references unknown decision")
			}
		}
	}
	for i, x := range v.AuditEvents {
		if err := x.Validate(); err != nil {
			return fmt.Errorf("audit_events[%d]: %w", i, err)
		}
		if x.SessionID != v.SessionID || (x.CommandID != "" && x.CommandID != v.CommandID) {
			return fmt.Errorf("audit_events[%d] incoherent", i)
		}
	}
	if v.Publication == PublicationPublish && len(v.Decisions) == 0 {
		return errors.New("publish commit missing decision")
	}
	return validateSize(v, MaxPolicyCommitBytes)
}

type RuleCooldownV1 struct {
	RuleID            string `json:"rule_id"`
	UntilPolicyTimeMS int64  `json:"until_policy_time_ms"`
}
type PolicyPinV1 struct {
	CandidateID string `json:"candidate_id"`
	DecisionID  string `json:"decision_id"`
}
type PolicyCheckpointV1 struct {
	SchemaVersion           string                    `json:"schema_version"`
	SessionID               string                    `json:"session_id"`
	SnapshotID              string                    `json:"snapshot_id,omitempty"`
	SnapshotContentSHA256   string                    `json:"snapshot_content_sha256,omitempty"`
	CommitSequence          uint64                    `json:"commit_sequence"`
	LastObservationSequence uint64                    `json:"last_observation_sequence"`
	PolicyRevision          uint64                    `json:"policy_revision"`
	RuleVersion             string                    `json:"rule_version"`
	ConfigVersion           string                    `json:"config_version"`
	StateHash               string                    `json:"state_hash"`
	ReferencedCommitSHA256  string                    `json:"referenced_commit_sha256"`
	CreatedTimeMS           int64                     `json:"created_time_ms"`
	CommandResults          []OperatorCommandResultV1 `json:"command_results"`
	PreviewCandidateIDs     []string                  `json:"preview_candidate_ids"`
	Cooldowns               []RuleCooldownV1          `json:"cooldowns"`
	Pins                    []PolicyPinV1             `json:"pins"`
	EmergencyHide           bool                      `json:"emergency_hide"`
}

func (v PolicyCheckpointV1) Validate() error {
	if v.SchemaVersion != PolicyCheckpointSchemaV1 || v.SessionID == "" || v.CommitSequence == 0 || v.RuleVersion == "" || v.ConfigVersion == "" || !isSHA(v.StateHash) || !isSHA(v.ReferencedCommitSHA256) || v.CreatedTimeMS < 0 || len(v.CommandResults) > MaxCheckpointCommandResults || len(v.PreviewCandidateIDs) > MaxPreviewCandidates || (v.SnapshotID == "") != (v.SnapshotContentSHA256 == "") || (v.SnapshotContentSHA256 != "" && (!isSHA(v.SnapshotContentSHA256) || v.SnapshotID != v.SnapshotContentSHA256)) {
		return errors.New("invalid policy checkpoint")
	}
	seen := map[string]bool{}
	last := uint64(0)
	for i, x := range v.CommandResults {
		if err := x.Validate(); err != nil || x.SessionID != v.SessionID || seen[x.CommandID] || x.ResultingRevision < last {
			return fmt.Errorf("command_results[%d] invalid", i)
		}
		seen[x.CommandID] = true
		last = x.ResultingRevision
	}
	for _, x := range v.Cooldowns {
		if x.RuleID == "" || x.UntilPolicyTimeMS < 0 {
			return errors.New("invalid cooldown")
		}
	}
	for _, x := range v.Pins {
		if x.CandidateID == "" || x.DecisionID == "" {
			return errors.New("invalid pin")
		}
	}
	return validateSize(v, 256<<10)
}
func (v PolicyCheckpointV1) ValidateAgainstCommit(commit PolicyCommitV1) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := commit.Validate(); err != nil {
		return err
	}
	h, err := CanonicalSHA256(commit)
	if err != nil {
		return err
	}
	if v.SessionID != commit.SessionID || v.CommitSequence != commit.CommitSequence || v.PolicyRevision != commit.ResultingPolicyRevision || v.StateHash != commit.ResultingStateHash || v.ReferencedCommitSHA256 != h || v.LastObservationSequence < commit.ObservationSequence {
		return errors.New("checkpoint commit mismatch")
	}
	return nil
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)\.[0-9]{3}$`)
var assetPattern = regexp.MustCompile(`^$|^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
var commandActions = map[string]bool{ActionApprove: true, ActionShow: true, ActionReject: true, ActionPin: true, ActionUnpin: true, ActionEmergencyHide: true, ActionClearEmergencyHide: true, ActionDisableRule: true, ActionEnableRule: true}
var decisionStates = map[string]bool{DecisionQueued: true, DecisionShown: true, DecisionRejected: true, DecisionSuperseded: true, DecisionExpired: true, DecisionPinned: true, DecisionEmergencyHidden: true}
var publications = map[string]bool{PublicationPublish: true, PublicationUnchanged: true, PublicationHide: true}

func isSHA(v string) bool { return sha256Pattern.MatchString(v) }
func schemaError(want, got string) error {
	return fmt.Errorf("schema version: want %q, got %q", want, got)
}
func validateSize(v any, max int) error {
	b, err := MarshalCanonical(v)
	if err != nil {
		return err
	}
	if len(b) > max {
		return fmt.Errorf("canonical size %d exceeds %d", len(b), max)
	}
	return nil
}
func invalidOverlayText(s string) bool {
	if strings.ContainsAny(s, "<>") || !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x061C || r == 0x200E || r == 0x200F {
			return true
		}
	}
	return false
}
func validateObservedTree(v any) error { return walkObserved(reflect.ValueOf(v), "contract") }
func walkObserved(v reflect.Value, path string) error {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return walkObserved(v.Elem(), path)
	}
	if v.Kind() == reflect.Struct && v.Type().PkgPath() == "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts" && strings.HasPrefix(v.Type().Name(), "ObservedV1[") {
		s := ValueState(v.FieldByName("State").String())
		has := !v.FieldByName("Value").IsNil()
		valid := s == ValuePresent || s == ValueAbsent || s == ValueRedacted || s == ValueUnsupported || s == ValueInvalid
		if !valid || (s == ValuePresent) != has {
			return fmt.Errorf("observed field %s has inconsistent state/value", path)
		}
		if has {
			e := v.FieldByName("Value").Elem()
			if e.Type() == reflect.TypeOf(Decimal("")) && !Decimal(e.String()).Valid() {
				return fmt.Errorf("observed field %s has invalid decimal", path)
			}
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := walkObserved(v.Field(i), path+"."+v.Type().Field(i).Name); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := walkObserved(v.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func contentHash(v any, kind string) (string, error) {
	b, err := MarshalCanonical(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(kind+":"), b...))
	return hex.EncodeToString(h[:]), nil
}
func verifyContentHash(v any, kind string) error {
	rv := reflect.ValueOf(v)
	copy := reflect.New(rv.Type()).Elem()
	copy.Set(rv)
	for _, name := range []string{"ScopeID", "SnapshotID", "ContentSHA256"} {
		f := copy.FieldByName(name)
		if f.IsValid() && f.CanSet() {
			f.SetString("")
		}
	}
	want, err := contentHash(copy.Interface(), kind)
	if err != nil {
		return err
	}
	var got string
	if f := rv.FieldByName("ContentSHA256"); f.IsValid() {
		got = f.String()
	}
	if got != want {
		return errors.New(kind + " content identity mismatch")
	}
	return nil
}
func sortedUnique(values []string) bool {
	if !sort.StringsAreSorted(values) {
		return false
	}
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return false
		}
	}
	return true
}
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func validateEvidenceOrder(values []EvidenceRefV1, session string) error {
	last := uint64(0)
	seen := map[uint64]bool{}
	for _, e := range values {
		if err := e.validate(session); err != nil {
			return err
		}
		if seen[e.Sequence] || e.Sequence <= last {
			return errors.New("evidence is not strictly ordered")
		}
		seen[e.Sequence] = true
		last = e.Sequence
	}
	return nil
}
