// Package raw defines the raw observation stream produced by the parser
// adapter. Raw observations are persisted verbatim (JSONL) before any
// normalization so derived analytics always remain recomputable from the
// immutable source stream. Events are emitted in parse order; downstream
// packages re-derive deterministic views.
package raw

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// EventKind enumerates the raw observation kinds.
type EventKind string

const (
	KindFileInfo    EventKind = "file_info"
	KindHeader      EventKind = "header"
	KindEngine      EventKind = "engine"
	KindGameState   EventKind = "game_state"
	KindCombat      EventKind = "combat"
	KindHeroState   EventKind = "hero_state"
	KindTeamState   EventKind = "team_state"
	KindPlayerRes   EventKind = "player_resource"
	KindParseDone   EventKind = "parse_done"
	KindParseFailed EventKind = "parse_failed"
)

// PlayerInfo is one participant binding taken from the demo file info.
type PlayerInfo struct {
	HeroName     string `json:"hero_name"`
	PlayerName   string `json:"player_name"`
	IsFakeClient bool   `json:"is_fake_client"`
	SteamID      uint64 `json:"steam_id"`
	GameTeam     int32  `json:"game_team"`
}

// FileInfo is the immutable demo file-info message.
type FileInfo struct {
	MatchID       uint64       `json:"match_id"`
	GameMode      int32        `json:"game_mode"`
	GameWinner    int32        `json:"game_winner"`
	LeagueID      uint32       `json:"league_id"`
	RadiantTeamID uint32       `json:"radiant_team_id"`
	DireTeamID    uint32       `json:"dire_team_id"`
	RadiantTag    string       `json:"radiant_tag"`
	DireTag       string       `json:"dire_tag"`
	EndTime       uint32       `json:"end_time_unix"`
	PlaybackTicks int32        `json:"playback_ticks"`
	PlaybackTime  float32      `json:"playback_time"`
	Players       []PlayerInfo `json:"players"`
	PicksBans     int32        `json:"picks_bans"`
}

// Header is the demo file header.
type Header struct {
	ServerName string `json:"server_name"`
}

// Engine is the server-info engine identity.
type Engine struct {
	GameBuild uint32 `json:"game_build"`
	GameDir   string `json:"game_dir"`
	MapName   string `json:"map_name"`
}

// GameState is a DOTA_GAMERULES_STATE transition carried by the combat log.
type GameState struct {
	CombatTS float64 `json:"combat_ts"`
	Tick     uint32  `json:"tick"`
	State    int64   `json:"state"`
}

// Combat is one combat-log entry with string-table names already resolved.
// Nullable fields are pointers so a missing field stays explicitly null and
// never collapses to a fabricated zero.
type Combat struct {
	Seq               int64    `json:"seq"`
	Type              string   `json:"type"`
	TypeID            int32    `json:"type_id"`
	TS                float64  `json:"ts"`
	TSRaw             float64  `json:"ts_raw"`
	Tick              uint32   `json:"tick"`
	AttackerIdx       *uint32  `json:"attacker_idx"`
	TargetIdx         *uint32  `json:"target_idx"`
	InflictorIdx      *uint32  `json:"inflictor_idx"`
	AttackerName      string   `json:"attacker_name"`
	TargetName        string   `json:"target_name"`
	InflictorName     string   `json:"inflictor_name"`
	IsAttackerHero    *bool    `json:"is_attacker_hero"`
	IsTargetHero      *bool    `json:"is_target_hero"`
	IsTargetBuilding  *bool    `json:"is_target_building"`
	Value             *int64   `json:"value"`
	Health            *int64   `json:"health"`
	GoldReason        *uint32  `json:"gold_reason"`
	XpReason          *uint32  `json:"xp_reason"`
	LastHits          *uint32  `json:"last_hits"`
	AttackerTeam      *uint32  `json:"attacker_team"`
	TargetTeam        *uint32  `json:"target_team"`
	AssistPlayers     []int32  `json:"assist_players"`
	Networth          *uint32  `json:"networth"`
	BuildingType      *uint32  `json:"building_type"`
	ObsWardsPlaced    *uint32  `json:"obs_wards_placed"`
	RuneType          *uint32  `json:"rune_type"`
	DamageType        *uint32  `json:"damage_type"`
	IsHealSave        *bool    `json:"is_heal_save"`
	IsUltimate        *bool    `json:"is_ultimate"`
	LocationX         *float64 `json:"location_x"`
	LocationY         *float64 `json:"location_y"`
	ModifierDuration  *float64 `json:"modifier_duration"`
	AbilityLevel      *uint32  `json:"ability_level"`
	IsAbilityToggleOn *bool    `json:"is_ability_toggle_on"`
}

// HeroState is one ordered hero entity snapshot. Position coordinates are the
// raw Source 2 cell-space values emitted by the adapter.
type HeroState struct {
	Tick             uint32   `json:"tick"`
	HeroIndex        int32    `json:"hero_index"`
	Class            string   `json:"class"`
	PlayerID         *int32   `json:"player_id"`
	TeamNum          *int32   `json:"team_num"`
	PosX             *float64 `json:"pos_x"`
	PosY             *float64 `json:"pos_y"`
	PosZ             *float64 `json:"pos_z"`
	Health           *int64   `json:"health"`
	MaxHealth        *int64   `json:"max_health"`
	Mana             *float64 `json:"mana"`
	MaxMana          *float64 `json:"max_mana"`
	Level            *int32   `json:"level"`
	XP               *int64   `json:"xp"`
	RespawnTime      *float64 `json:"respawn_time"`
	BuybackAvailable *bool    `json:"buyback_available"`
	Alive            *bool    `json:"alive"`
	Networth         *uint32  `json:"networth"`
}

// TeamState is one ordered team entity snapshot.
type TeamState struct {
	Tick          uint32  `json:"tick"`
	TeamIndex     int32   `json:"team_index"`
	TeamNum       *int32  `json:"team_num"`
	Name          string  `json:"name"`
	Tag           string  `json:"tag"`
	TournamentID  *uint32 `json:"tournament_id"`
	Score         *int64  `json:"score"`
	TowerKills    *int64  `json:"tower_kills"`
	BarracksKills *int64  `json:"barracks_kills"`
}

// PlayerResource is one player-resource slot binding.
type PlayerResource struct {
	Slot       int32  `json:"slot"`
	SteamID64  uint64 `json:"steam_id64"`
	PlayerName string `json:"player_name"`
	Team       *int32 `json:"team"`
	PlayerSlot *int32 `json:"player_slot"`
}

// ParseDone is the terminal raw event emitted after a successful parse.
type ParseDone struct {
	LastTick      uint32            `json:"last_tick"`
	LastNetTick   uint32            `json:"last_net_tick"`
	GameBuild     uint32            `json:"game_build"`
	MessageCounts map[string]uint64 `json:"message_counts"`
	CombatTotal   uint64            `json:"combat_total"`
	BytesRead     int64             `json:"bytes_read"`
	ElapsedSec    float64           `json:"elapsed_sec"`
	Outcome       string            `json:"outcome"`
}

// ParseFailed is the terminal raw event emitted when extraction fails.
type ParseFailed struct {
	Error string `json:"error"`
}

// Event is one raw observation line.
type Event struct {
	Kind        EventKind       `json:"kind"`
	Seq         int64           `json:"seq"`
	FileInfo    *FileInfo       `json:"file_info,omitempty"`
	Header      *Header         `json:"header,omitempty"`
	Engine      *Engine         `json:"engine,omitempty"`
	GameState   *GameState      `json:"game_state,omitempty"`
	Combat      *Combat         `json:"combat,omitempty"`
	HeroState   *HeroState      `json:"hero_state,omitempty"`
	TeamState   *TeamState      `json:"team_state,omitempty"`
	PlayerRes   *PlayerResource `json:"player_resource,omitempty"`
	ParseDone   *ParseDone      `json:"parse_done,omitempty"`
	ParseFailed *ParseFailed    `json:"parse_failed,omitempty"`
}

// SchemaVersion is the raw schema constant (exposed for tests and reports).
func SchemaVersion() string { return version.RawSchema }

// Writer streams events as JSONL to an io.Writer.
type Writer struct {
	w   *bufio.Writer
	enc *json.Encoder
	seq int64
}

// NewWriter wraps w with a buffered JSONL writer.
func NewWriter(w io.Writer) *Writer {
	bw := bufio.NewWriterSize(w, 1<<20)
	return &Writer{w: bw, enc: json.NewEncoder(bw)}
}

// Write emits one event with an incrementing sequence number.
func (w *Writer) Write(e *Event) error {
	w.seq++
	e.Seq = w.seq
	if err := w.enc.Encode(e); err != nil {
		return fmt.Errorf("raw: encode event: %w", err)
	}
	return nil
}

// EventsWritten returns the number of events written so far.
func (w *Writer) EventsWritten() int64 { return w.seq }

// Flush flushes the underlying buffered writer.
func (w *Writer) Flush() error { return w.w.Flush() }

// Close flushes and releases the writer.
func (w *Writer) Close() error { return w.w.Flush() }

// Reader streams events from a JSONL source.
type Reader struct {
	sc *bufio.Scanner
}

// NewReader reads events from an io.Reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{sc: bufio.NewScanner(r)}
}

// Next decodes the next event. Returns io.EOF at end of stream.
func (r *Reader) Next() (*Event, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, fmt.Errorf("raw: scan: %w", err)
		}
		return nil, io.EOF
	}
	var e Event
	if err := json.Unmarshal(r.sc.Bytes(), &e); err != nil {
		return nil, fmt.Errorf("raw: decode: %w", err)
	}
	return &e, nil
}

// OpenWriter opens a JSONL file for writing.
func OpenWriter(path string) (*Writer, *os.File, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return NewWriter(f), f, nil
}

// OpenReader opens a JSONL file for reading.
func OpenReader(path string) (*Reader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return NewReader(f), f, nil
}
