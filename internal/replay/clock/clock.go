// Package clock derives the calibrated game clock from the raw observation
// stream. It anchors game_second 0 at the DOTA_GAMERULES_STATE_GAME_IN_PROGRESS
// transition and cross-checks the derived duration against independent public
// metadata. It fails closed on a missing anchor or a negative/implausible
// duration; it never treats the max combat-log timestamp as the duration.
package clock

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// DOTA_GAMERULES_STATE constants used by the calibration gate.
const (
	StateInit                 int64 = 0
	StateHeroSelection        int64 = 2
	StateStrategyTime         int64 = 3
	StatePregame              int64 = 4
	StateGameInProgress       int64 = 5
	StatePostgame             int64 = 6
	StatePlayerDraft          int64 = 12
	StateCustomGameSetup      int64 = 9
	StateWaitForMapToLoad     int64 = 10
	StateTeamShowcase         int64 = 8
	StateDisconnect           int64 = 7
	StateWaitForPlayersToLoad int64 = 1
)

// DotaTickRate is the fixed demo tick rate (30 ticks per second).
const DotaTickRate = 30.0

// State values for a Clock record.
const (
	StateCalibrated  = "calibrated"
	StateUnavailable = "unavailable"
	StateFailClosed  = "fail_closed"
)

// Transition is one observed game-state transition.
type Transition struct {
	State    int64   `json:"state"`
	CombatTS float64 `json:"combat_ts"`
	Tick     uint32  `json:"tick"`
}

// Clock is the calibrated clock record.
type Clock struct {
	SchemaVersion      string       `json:"schema_version"`
	RuleVersion        string       `json:"rule_version"`
	State              string       `json:"state"`
	Reason             string       `json:"reason"`
	Transitions        []Transition `json:"transitions"`
	AnchorCombatTS     float64      `json:"anchor_combat_ts"`
	AnchorTick         uint32       `json:"anchor_tick"`
	EndCombatTS        *float64     `json:"end_combat_ts"`
	EndTick            *uint32      `json:"end_tick"`
	PregameSeconds     float64      `json:"pregame_seconds"`
	GameDurationSeconds *float64    `json:"game_duration_seconds"`
	PlaybackSeconds    float64      `json:"playback_seconds"`
	PublicDurationSeconds *int      `json:"public_duration_seconds"`
	DurationDeltaSeconds *float64   `json:"duration_delta_seconds"`
	DurationGateOK     bool         `json:"duration_gate_ok"`
	GameStartUnix      *int64       `json:"game_start_unix"`
	GameEndUnix        *int64       `json:"game_end_unix"`
	Missing            []string     `json:"missing_inputs"`
	EventAlignmentErrors []float64  `json:"event_alignment_errors"`
}

// GameSecondOfCombatTS converts a combat-log timestamp to a game second.
func (c *Clock) GameSecondOfCombatTS(ts float64) float64 {
	return ts - c.AnchorCombatTS
}

// GameSecondOfTick converts a demo tick to a game second.
func (c *Clock) GameSecondOfTick(tick uint32) float64 {
	return (float64(tick) - float64(c.AnchorTick)) / DotaTickRate
}

// StateName returns the friendly name of a game state value.
func StateName(s int64) string {
	switch s {
	case StateInit:
		return "init"
	case StateWaitForPlayersToLoad:
		return "wait_for_players_to_load"
	case StateHeroSelection:
		return "hero_selection"
	case StateStrategyTime:
		return "strategy_time"
	case StatePregame:
		return "pre_game"
	case StateGameInProgress:
		return "game_in_progress"
	case StatePostgame:
		return "post_game"
	case StateDisconnect:
		return "disconnect"
	case StateTeamShowcase:
		return "team_showcase"
	case StateCustomGameSetup:
		return "custom_game_setup"
	case StateWaitForMapToLoad:
		return "wait_for_map_to_load"
	case StatePlayerDraft:
		return "player_draft"
	}
	return fmt.Sprintf("state_%d", s)
}

// BuildOptions carries the independent public metadata used for the duration
// cross-check.
type BuildOptions struct {
	PublicDurationSeconds int
	EndTimeUnix           uint32
	PlaybackSeconds       float64
}

// Build calibrates the clock from raw game-state transitions. transitions
// must be in observed order (ascending combat timestamp).
func Build(transitions []Transition, opts BuildOptions) *Clock {
	c := &Clock{
		SchemaVersion:      version.ClockSchema,
		RuleVersion:        version.PhaseRuleVersion,
		State:              StateFailClosed,
		Reason:             "no_game_in_progress_anchor",
		Transitions:        transitions,
		PlaybackSeconds:    opts.PlaybackSeconds,
		PublicDurationSeconds: &opts.PublicDurationSeconds,
		Missing:            []string{},
	}

	if opts.PublicDurationSeconds <= 0 {
		c.Reason = "public_duration_missing"
		return c
	}

	for i := range transitions {
		if transitions[i].State == StateGameInProgress {
			c.AnchorCombatTS = transitions[i].CombatTS
			c.AnchorTick = transitions[i].Tick
			break
		}
	}
	if c.AnchorTick == 0 && c.AnchorCombatTS == 0 {
		c.Reason = "game_in_progress_anchor_missing"
		return c
	}

	for i := range transitions {
		if transitions[i].State == StatePostgame {
			ts := transitions[i].CombatTS
			c.EndCombatTS = &ts
			t := transitions[i].Tick
			c.EndTick = &t
			break
		}
	}

	if c.EndCombatTS == nil {
		c.Missing = append(c.Missing, "post_game_transition")
		c.State = StateUnavailable
		c.Reason = "game_end_missing_right_censored"
		return c
	}

	dur := *c.EndCombatTS - c.AnchorCombatTS
	if dur <= 0 {
		c.Reason = "non_positive_duration"
		return c
	}
	c.GameDurationSeconds = &dur
	c.PregameSeconds = c.AnchorCombatTS
	delta := float64(opts.PublicDurationSeconds) - dur
	c.DurationDeltaSeconds = &delta

	tolerance := math.Max(2.0, float64(opts.PublicDurationSeconds)*0.001)
	c.DurationGateOK = math.Abs(delta) <= tolerance

	replayStart := float64(opts.EndTimeUnix) - opts.PlaybackSeconds
	gameStart := replayStart + c.AnchorCombatTS
	gameEnd := gameStart + dur
	gs, ge := int64(gameStart), int64(gameEnd)
	c.GameStartUnix = &gs
	c.GameEndUnix = &ge

	if !c.DurationGateOK {
		c.State = StateFailClosed
		c.Reason = "duration_mismatch_exceeds_tolerance"
		return c
	}

	c.State = StateCalibrated
	c.Reason = "anchored_at_game_in_progress"
	return c
}

// CanonicalJSON returns the deterministic encoding.
func (c *Clock) CanonicalJSON() ([]byte, error) { return json.Marshal(c) }