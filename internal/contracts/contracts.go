// Package contracts defines the versioned, source-specific ports shared by the
// historical, insight, policy, and presentation tracks. It deliberately has no
// adapter, storage, HTTP, replay-parser, localization, or OBS dependencies.
package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const (
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

	MaxLiveObservationBytes    = 1 << 20
	MaxHistoricalBaselineBytes = 1 << 20
	MaxInsightCandidateBytes   = 64 << 10
	MaxBroadcastDecisionBytes  = 32 << 10
	MaxOverlayBytes            = 64 << 10
	MaxAuditEventBytes         = 16 << 10
	MaxPolicyCommitBytes       = 256 << 10
)

type Contract interface{ Validate() error }

// DecodeStrict rejects additional fields and trailing JSON so version changes
// cannot silently alter a consumer's interpretation.
func DecodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

// MarshalCanonical is the V1 canonical encoding: compact UTF-8 JSON in Go
// struct field order, with map keys sorted by encoding/json and no newline.
func MarshalCanonical(value any) ([]byte, error) { return json.Marshal(value) }

type ValueState string

const (
	ValuePresent     ValueState = "present"
	ValueAbsent      ValueState = "absent"
	ValueRedacted    ValueState = "redacted"
	ValueUnsupported ValueState = "unsupported"
	ValueInvalid     ValueState = "invalid"
)

// ObservedV1 separates source state from value. A provider-supplied zero or
// false is represented by state=present and a non-nil value.
type ObservedV1[T any] struct {
	State ValueState `json:"state"`
	Value *T         `json:"value,omitempty"`
}

func Present[T any](value T) ObservedV1[T] { return ObservedV1[T]{State: ValuePresent, Value: &value} }
func Absent[T any]() ObservedV1[T]         { return ObservedV1[T]{State: ValueAbsent} }

type EvidenceRefV1 struct {
	RecordSchemaVersion int               `json:"record_schema_version"`
	SessionID           string            `json:"session_id"`
	Sequence            uint64            `json:"sequence"`
	ReceiveTime         time.Time         `json:"receive_time"`
	Source              string            `json:"source"`
	ProviderVersion     ObservedV1[int64] `json:"provider_version"`
	RawPayloadSHA256    string            `json:"raw_payload_sha256"`
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
	RadiantGlyphCooldown        ObservedV1[float64] `json:"radiant_glyph_cooldown"`
	DireGlyphCooldown           ObservedV1[float64] `json:"dire_glyph_cooldown"`
	RadiantScanCharges          ObservedV1[int64]   `json:"radiant_scan_charges"`
	DireScanCharges             ObservedV1[int64]   `json:"dire_scan_charges"`
	RadiantLotusPoolCount       ObservedV1[int64]   `json:"radiant_lotus_pool_count"`
	DireLotusPoolCount          ObservedV1[int64]   `json:"dire_lotus_pool_count"`
	RadiantWardPurchaseCooldown ObservedV1[float64] `json:"radiant_ward_purchase_cooldown"`
	DireWardPurchaseCooldown    ObservedV1[float64] `json:"dire_ward_purchase_cooldown"`
}

type ObjectiveObservationV1 struct {
	State      ObservedV1[string]  `json:"state"`
	Location   ObservedV1[string]  `json:"location"`
	EndSeconds ObservedV1[float64] `json:"end_seconds"`
}

type BuildingObservationV1 struct {
	Team      string              `json:"team"`
	Name      string              `json:"name"`
	Health    ObservedV1[float64] `json:"health"`
	MaxHealth ObservedV1[float64] `json:"max_health"`
}

type ItemObservationV1 struct {
	Slot        string              `json:"slot"`
	Name        ObservedV1[string]  `json:"name"`
	ItemLevel   ObservedV1[int64]   `json:"item_level"`
	Cooldown    ObservedV1[float64] `json:"cooldown"`
	MaxCooldown ObservedV1[float64] `json:"max_cooldown"`
	CanCast     ObservedV1[bool]    `json:"can_cast"`
	Charges     ObservedV1[int64]   `json:"charges"`
	Passive     ObservedV1[bool]    `json:"passive"`
}

type AbilityObservationV1 struct {
	Slot        string              `json:"slot"`
	Name        ObservedV1[string]  `json:"name"`
	Level       ObservedV1[int64]   `json:"level"`
	Cooldown    ObservedV1[float64] `json:"cooldown"`
	MaxCooldown ObservedV1[float64] `json:"max_cooldown"`
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
	Gold             ObservedV1[float64]            `json:"gold"`
	NetWorth         ObservedV1[float64]            `json:"net_worth"`
	GPM              ObservedV1[int64]              `json:"gpm"`
	XPM              ObservedV1[int64]              `json:"xpm"`
	GoldReliable     ObservedV1[float64]            `json:"gold_reliable"`
	GoldUnreliable   ObservedV1[float64]            `json:"gold_unreliable"`
	Kills            ObservedV1[int64]              `json:"kills"`
	Deaths           ObservedV1[int64]              `json:"deaths"`
	Assists          ObservedV1[int64]              `json:"assists"`
	LastHits         ObservedV1[int64]              `json:"last_hits"`
	Denies           ObservedV1[int64]              `json:"denies"`
	XPos             ObservedV1[float64]            `json:"xpos"`
	YPos             ObservedV1[float64]            `json:"ypos"`
	Health           ObservedV1[float64]            `json:"health"`
	MaxHealth        ObservedV1[float64]            `json:"max_health"`
	HealthPercent    ObservedV1[float64]            `json:"health_percent"`
	Mana             ObservedV1[float64]            `json:"mana"`
	MaxMana          ObservedV1[float64]            `json:"max_mana"`
	ManaPercent      ObservedV1[float64]            `json:"mana_percent"`
	Alive            ObservedV1[bool]               `json:"alive"`
	RespawnSeconds   ObservedV1[float64]            `json:"respawn_seconds"`
	Level            ObservedV1[int64]              `json:"level"`
	XP               ObservedV1[float64]            `json:"xp"`
	BuybackCost      ObservedV1[float64]            `json:"buyback_cost"`
	BuybackCooldown  ObservedV1[float64]            `json:"buyback_cooldown"`
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
	if v.MappingVersion == "" || v.Evidence.SessionID == "" || v.Evidence.Sequence == 0 || v.Evidence.ReceiveTime.IsZero() || v.Evidence.Source != "gsi" || !sha256Pattern.MatchString(v.Evidence.RawPayloadSHA256) {
		return errors.New("invalid live observation identity or provenance")
	}
	if len(v.Participants) > 10 {
		return errors.New("participants exceeds 10")
	}
	if err := validateObservedTree(v); err != nil {
		return err
	}
	return validateSize(v, MaxLiveObservationBytes)
}

type HistoricalSnapshotManifestV1 struct {
	SchemaVersion             string    `json:"schema_version"`
	SnapshotID                string    `json:"snapshot_id"`
	TournamentScopeID         string    `json:"tournament_scope_id"`
	CutoffTime                time.Time `json:"cutoff_time"`
	MaximumSourceEventTime    time.Time `json:"maximum_source_event_time"`
	DiscoveryManifestSHA256   string    `json:"discovery_manifest_sha256"`
	InputManifestSHA256       string    `json:"input_manifest_sha256"`
	ParserVersion             string    `json:"parser_version"`
	AggregateVersion          string    `json:"aggregate_version"`
	IncludedCompletedMatchIDs []string  `json:"included_completed_match_ids"`
	ExcludedMatchIDs          []string  `json:"excluded_match_ids"`
	BaselineContentSHA256     []string  `json:"baseline_content_sha256"`
}

func (v HistoricalSnapshotManifestV1) Validate() error {
	if v.SchemaVersion != HistoricalSnapshotManifestSchemaV1 {
		return schemaError(HistoricalSnapshotManifestSchemaV1, v.SchemaVersion)
	}
	if v.SnapshotID == "" || v.TournamentScopeID == "" || v.CutoffTime.IsZero() || v.MaximumSourceEventTime.After(v.CutoffTime) {
		return errors.New("invalid historical snapshot provenance")
	}
	return validateSize(v, MaxHistoricalBaselineBytes)
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
	State          ValueState `json:"state"`
	Value          *float64   `json:"value,omitempty"`
	SampleSize     uint64     `json:"sample_size"`
	PeriodStart    time.Time  `json:"period_start"`
	PeriodEnd      time.Time  `json:"period_end"`
	SourceCoverage float64    `json:"source_coverage"`
}
type HistoricalBaselineV1 struct {
	SchemaVersion string                  `json:"schema_version"`
	BaselineID    string                  `json:"baseline_id"`
	SnapshotID    string                  `json:"snapshot_id"`
	GeneratedTime time.Time               `json:"generated_time"`
	Key           HistoricalBaselineKeyV1 `json:"key"`
	Value         HistoricalValueV1       `json:"value"`
}

func (v HistoricalBaselineV1) Validate() error {
	if v.SchemaVersion != HistoricalBaselineSchemaV1 {
		return schemaError(HistoricalBaselineSchemaV1, v.SchemaVersion)
	}
	if v.BaselineID == "" || v.SnapshotID == "" || v.Key.RosterID == "" || v.Key.Patch == "" || v.Key.Metric == "" || v.Key.Window == "" || v.Key.SampleDefinition == "" || v.GeneratedTime.IsZero() {
		return errors.New("invalid historical baseline identity")
	}
	return validateSize(v, MaxHistoricalBaselineBytes)
}

type TypedParameterV1 struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	StringValue  *string  `json:"string_value,omitempty"`
	NumberValue  *float64 `json:"number_value,omitempty"`
	BooleanValue *bool    `json:"boolean_value,omitempty"`
}
type ObservedMetricV1 struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
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
	if err := validateObservedTree(v); err != nil {
		return err
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
	if v.DecisionID == "" || v.SessionID == "" || v.ResultingState == "" || v.Reason == "" {
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
	if v.SessionID == "" || v.PublicationTimeMS <= 0 || v.StaleDeadlineMS < v.PublicationTimeMS {
		return errors.New("invalid overlay identity or clock")
	}
	if v.Visibility != "visible" && v.Visibility != "hidden" {
		return errors.New("invalid overlay visibility")
	}
	if v.Visibility == "hidden" && v.Claim != nil {
		return errors.New("hidden overlay contains claim")
	}
	if v.Visibility == "visible" && (v.Claim == nil || v.DecisionID == "") {
		return errors.New("visible overlay missing claim identity")
	}
	if v.Claim != nil {
		if containsMarkup(v.Claim.Title) || containsMarkup(v.Claim.Body) || !assetPattern.MatchString(v.Claim.AssetKey) {
			return errors.New("overlay claim contains forbidden markup or asset key")
		}
		if len(v.Claim.Title) > 160 || len(v.Claim.Body) > 1024 {
			return errors.New("overlay text bound exceeded")
		}
	}
	if err := validateObservedTree(v); err != nil {
		return err
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
	if v.SchemaVersion != OperatorCommandSchemaV1 {
		return schemaError(OperatorCommandSchemaV1, v.SchemaVersion)
	}
	if v.CommandID == "" || v.SessionID == "" || v.Action == "" {
		return errors.New("invalid operator command")
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
	if v.SchemaVersion != OperatorCommandResultSchemaV1 {
		return schemaError(OperatorCommandResultSchemaV1, v.SchemaVersion)
	}
	if v.CommandID == "" || v.SessionID == "" || (v.Status != "accepted" && v.Status != "rejected") || v.Reason == "" {
		return errors.New("invalid operator command result")
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
	if v.SchemaVersion != AuditEventSchemaV1 {
		return schemaError(AuditEventSchemaV1, v.SchemaVersion)
	}
	if v.EventID == "" || v.SessionID == "" || v.EventType == "" || v.Reason == "" {
		return errors.New("invalid audit event")
	}
	return validateSize(v, MaxAuditEventBytes)
}

type PolicyCommitV1 struct {
	SchemaVersion           string                   `json:"schema_version"`
	SessionID               string                   `json:"session_id"`
	CommitSequence          uint64                   `json:"commit_sequence"`
	ObservationIdentity     string                   `json:"observation_identity,omitempty"`
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
	if v.SchemaVersion != PolicyCommitSchemaV1 {
		return schemaError(PolicyCommitSchemaV1, v.SchemaVersion)
	}
	if v.SessionID == "" || v.CommitSequence == 0 || !sha256Pattern.MatchString(v.ResultingStateHash) || (v.Publication != "publish" && v.Publication != "unchanged" && v.Publication != "hide") {
		return errors.New("invalid policy commit")
	}
	for i := range v.AuditEvents {
		if err := v.AuditEvents[i].Validate(); err != nil {
			return fmt.Errorf("audit_events[%d]: %w", i, err)
		}
	}
	return validateSize(v, MaxPolicyCommitBytes)
}

type PolicyCheckpointV1 struct {
	SchemaVersion          string `json:"schema_version"`
	SessionID              string `json:"session_id"`
	CommitSequence         uint64 `json:"commit_sequence"`
	PolicyRevision         uint64 `json:"policy_revision"`
	RuleVersion            string `json:"rule_version"`
	ConfigVersion          string `json:"config_version"`
	StateHash              string `json:"state_hash"`
	ReferencedCommitSHA256 string `json:"referenced_commit_sha256"`
	CreatedTimeMS          int64  `json:"created_time_ms"`
}

func (v PolicyCheckpointV1) Validate() error {
	if v.SchemaVersion != PolicyCheckpointSchemaV1 {
		return schemaError(PolicyCheckpointSchemaV1, v.SchemaVersion)
	}
	if v.SessionID == "" || v.CommitSequence == 0 || v.RuleVersion == "" || v.ConfigVersion == "" || !sha256Pattern.MatchString(v.StateHash) || !sha256Pattern.MatchString(v.ReferencedCommitSHA256) {
		return errors.New("invalid policy checkpoint")
	}
	return validateSize(v, 32<<10)
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var assetPattern = regexp.MustCompile(`^$|^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

func schemaError(want, got string) error {
	return fmt.Errorf("schema version: want %q, got %q", want, got)
}
func validateSize(v any, max int) error {
	data, err := MarshalCanonical(v)
	if err != nil {
		return err
	}
	if len(data) > max {
		return fmt.Errorf("canonical size %d exceeds %d", len(data), max)
	}
	return nil
}
func containsMarkup(s string) bool { return strings.ContainsAny(s, "<>") }

func validateObservedTree(value any) error { return walkObserved(reflect.ValueOf(value), "contract") }

func walkObserved(value reflect.Value, path string) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return walkObserved(value.Elem(), path)
	}
	if value.Kind() == reflect.Struct && value.Type().PkgPath() == "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts" && strings.HasPrefix(value.Type().Name(), "ObservedV1[") {
		state := ValueState(value.FieldByName("State").String())
		hasValue := !value.FieldByName("Value").IsNil()
		validState := state == ValuePresent || state == ValueAbsent || state == ValueRedacted || state == ValueUnsupported || state == ValueInvalid
		if !validState || (state == ValuePresent) != hasValue {
			return fmt.Errorf("observed field %s has inconsistent state/value", path)
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if err := walkObserved(value.Field(i), path+"."+value.Type().Field(i).Name); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := walkObserved(value.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
