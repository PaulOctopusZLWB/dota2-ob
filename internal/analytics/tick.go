package analytics

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// NormalizedTick is the stable, typed projection of a single accepted GSI
// snapshot. It exposes only fields that were actually observed in the raw
// payload: every optional value is a pointer so a missing field is distinct
// from a zero value, and consumers never infer unobserved state.
type NormalizedTick struct {
	ReceivedAt time.Time       `json:"received_at"`
	TickIndex  int64           `json:"tick_index"`
	Provider   ProviderMeta    `json:"provider"`
	MatchID    string          `json:"match_id,omitempty"`
	Map        MapState        `json:"map"`
	Roshan     RoshanState     `json:"roshan"`
	Tormentor  TormentorState  `json:"tormentor"`
	Buildings  []BuildingTick  `json:"buildings,omitempty"`
	Players    []PlayerTick    `json:"players,omitempty"`
}

// ProviderMeta is the GSI provider section.
type ProviderMeta struct {
	Name      string  `json:"name,omitempty"`
	AppID     int64   `json:"appid,omitempty"`
	Version   int64   `json:"version,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
}

// MapState is the normalized map/match-clock section.
type MapState struct {
	ClockTime                 *int64   `json:"clock_time,omitempty"`
	GameTime                  *int64   `json:"game_time,omitempty"`
	GameState                 string   `json:"game_state,omitempty"`
	Paused                    *bool    `json:"paused,omitempty"`
	Daytime                   *bool    `json:"daytime,omitempty"`
	NightstalkerNight         *bool    `json:"nightstalker_night,omitempty"`
	WinTeam                   string   `json:"win_team,omitempty"`
	RadiantScore              *int64   `json:"radiant_score,omitempty"`
	DireScore                 *int64   `json:"dire_score,omitempty"`
	RadiantGlyphCooldown      *float64 `json:"radiant_glyph_cooldown,omitempty"`
	DireGlyphCooldown         *float64 `json:"dire_glyph_cooldown,omitempty"`
	RadiantScanCharges        *int64   `json:"radiant_scan_charges,omitempty"`
	DireScanCharges           *int64   `json:"dire_scan_charges,omitempty"`
	RadiantLotusPoolCount     *int64   `json:"radiant_lotus_pool_count,omitempty"`
	DireLotusPoolCount        *int64   `json:"dire_lotus_pool_count,omitempty"`
	RadiantWardPurchaseCooldown *float64 `json:"radiant_ward_purchase_cooldown,omitempty"`
	DireWardPurchaseCooldown    *float64 `json:"dire_ward_purchase_cooldown,omitempty"`
}

// RoshanState is the Roshan observation from the map section.
type RoshanState struct {
	State      string   `json:"state,omitempty"`
	EndSeconds *float64 `json:"end_seconds,omitempty"`
}

// TormentorState is the Tormentor observation from the map section.
type TormentorState struct {
	State      string   `json:"state,omitempty"`
	Location   string   `json:"location,omitempty"`
	EndSeconds *float64 `json:"end_seconds,omitempty"`
}

// BuildingTick is one building's observed health.
type BuildingTick struct {
	Team      string   `json:"team"` // radiant | dire (the GSI top-level key)
	Name      string   `json:"name"`
	Health    *float64 `json:"health,omitempty"`
	MaxHealth *float64 `json:"max_health,omitempty"`
}

// ItemSlot is one observed inventory/stash/neutral item slot.
type ItemSlot struct {
	Slot        string   `json:"slot"`
	Name        string   `json:"name"`
	ItemLevel   *int64   `json:"item_level,omitempty"`
	Cooldown    *float64 `json:"cooldown,omitempty"`
	MaxCooldown *float64 `json:"max_cooldown,omitempty"`
	CanCast     *bool    `json:"can_cast,omitempty"`
	Charges     *int64   `json:"charges,omitempty"`
	Passive     *bool    `json:"passive,omitempty"`
}

// AbilitySlot is one observed ability slot.
type AbilitySlot struct {
	Slot        string   `json:"slot"`
	Name        string   `json:"name"`
	Level       *int64   `json:"level,omitempty"`
	Cooldown    *float64 `json:"cooldown,omitempty"`
	MaxCooldown *float64 `json:"max_cooldown,omitempty"`
	CanCast     *bool    `json:"can_cast,omitempty"`
	Passive     *bool    `json:"passive,omitempty"`
	Ultimate    *bool    `json:"ultimate,omitempty"`
}

// PlayerTick is the merged per-hero observation across the player/hero/items/
// abilities sections.
type PlayerTick struct {
	Slot      string `json:"slot"`  // e.g. "player0"
	TeamKey   string `json:"team_key"` // e.g. "team2"
	TeamName  string `json:"team_name,omitempty"` // radiant | dire
	PlayerSlot *int64 `json:"player_slot,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	SteamID   string `json:"steam_id,omitempty"`
	Name      string `json:"name,omitempty"`

	Gold         *float64 `json:"gold,omitempty"`
	NetWorth     *float64 `json:"net_worth,omitempty"`
	GPM          *int64   `json:"gpm,omitempty"`
	XPM          *int64   `json:"xpm,omitempty"`
	GoldReliable *float64 `json:"gold_reliable,omitempty"`

	Kills   *int64 `json:"kills,omitempty"`
	Deaths  *int64 `json:"deaths,omitempty"`
	Assists *int64 `json:"assists,omitempty"`

	LastHits *int64 `json:"last_hits,omitempty"`
	Denies   *int64 `json:"denies,omitempty"`

	XPos *float64 `json:"xpos,omitempty"`
	YPos *float64 `json:"ypos,omitempty"`

	Health        *float64 `json:"health,omitempty"`
	MaxHealth     *float64 `json:"max_health,omitempty"`
	HealthPercent *float64 `json:"health_percent,omitempty"`
	Mana          *float64 `json:"mana,omitempty"`
	MaxMana       *float64 `json:"max_mana,omitempty"`
	ManaPercent   *float64 `json:"mana_percent,omitempty"`

	Alive          *bool    `json:"alive,omitempty"`
	RespawnSeconds *float64 `json:"respawn_seconds,omitempty"`
	Level          *int64   `json:"level,omitempty"`
	XP             *float64 `json:"xp,omitempty"`

	Stunned      *bool `json:"stunned,omitempty"`
	Silenced     *bool `json:"silenced,omitempty"`
	Disarmed     *bool `json:"disarmed,omitempty"`
	Hexed        *bool `json:"hexed,omitempty"`
	Muted        *bool `json:"muted,omitempty"`
	Break        *bool `json:"break,omitempty"`
	HasDebuff    *bool `json:"has_debuff,omitempty"`
	MagicImmune  *bool `json:"magicimmune,omitempty"`
	Smoked       *bool `json:"smoked,omitempty"`

	WardsPlaced    *int64 `json:"wards_placed,omitempty"`
	WardsDestroyed *int64 `json:"wards_destroyed,omitempty"`
	WardsPurchased *int64 `json:"wards_purchased,omitempty"`

	Items     []ItemSlot    `json:"items,omitempty"`
	Abilities []AbilitySlot `json:"abilities,omitempty"`

	// ObservedFields lists the leaf field paths that were present for this
	// player in the raw payload. It is the raw-evidence footprint used by the
	// summary; downstream code never synthesizes values not listed here.
	ObservedFields []string `json:"observed_fields,omitempty"`
}

// Normalize converts a raw GSI payload (decoded as map[string]any, with either
// json.Number or float64 leaf numbers) into a NormalizedTick. It only copies
// fields that were actually present; no value is fabricated.
func Normalize(receivedAt time.Time, payload any) NormalizedTick {
	tick := NormalizedTick{ReceivedAt: receivedAt.UTC()}
	root, ok := payload.(map[string]any)
	if !ok {
		return tick
	}

	tick.Provider = normalizeProvider(root["provider"])
	tick.MatchID = matchID(root)
	tick.Map = normalizeMap(root["map"])
	tick.Roshan = normalizeRoshan(root["map"])
	tick.Tormentor = normalizeTormentor(root["map"])
	tick.Buildings = normalizeBuildings(root["buildings"])
	tick.Players = normalizePlayers(root)
	return tick
}

func normalizeProvider(v any) ProviderMeta {
	m := asMap(v)
	return ProviderMeta{
		Name:      str(m["name"]),
		AppID:     intOrZero(m["appid"]),
		Version:   intOrZero(m["version"]),
		Timestamp: intOrZero(m["timestamp"]),
	}
}

// matchID prefers league.match_id, then map.matchid. A present-but-zero id is
// still reported so callers can see the observed value, but is cleared when no
// meaningful id was seen.
func matchID(root map[string]any) string {
	if league := asMap(root["league"]); league != nil {
		if id := str(league["match_id"]); id != "" {
			return id
		}
	}
	if m := asMap(root["map"]); m != nil {
		if id := str(m["matchid"]); id != "" {
			return id
		}
	}
	return ""
}

func normalizeMap(v any) MapState {
	m := asMap(v)
	return MapState{
		ClockTime:                   intP(m["clock_time"]),
		GameTime:                    intP(m["game_time"]),
		GameState:                   str(m["game_state"]),
		Paused:                      boolP(m["paused"]),
		Daytime:                     boolP(m["daytime"]),
		NightstalkerNight:           boolP(m["nightstalker_night"]),
		WinTeam:                     str(m["win_team"]),
		RadiantScore:                intP(m["radiant_score"]),
		DireScore:                   intP(m["dire_score"]),
		RadiantGlyphCooldown:        numP(m["radiant_glyph_cooldown"]),
		DireGlyphCooldown:           numP(m["dire_glyph_cooldown"]),
		RadiantScanCharges:          intP(m["radiant_scan_charges"]),
		DireScanCharges:             intP(m["dire_scan_charges"]),
		RadiantLotusPoolCount:       intP(m["radiant_lotus_pool_count"]),
		DireLotusPoolCount:          intP(m["dire_lotus_pool_count"]),
		RadiantWardPurchaseCooldown: numP(m["radiant_ward_purchase_cooldown"]),
		DireWardPurchaseCooldown:     numP(m["dire_ward_purchase_cooldown"]),
	}
}

func normalizeRoshan(v any) RoshanState {
	m := asMap(v)
	return RoshanState{
		State:      str(m["roshan_state"]),
		EndSeconds: numP(m["roshan_state_end_seconds"]),
	}
}

func normalizeTormentor(v any) TormentorState {
	m := asMap(v)
	return TormentorState{
		State:      str(m["tormentor_state"]),
		Location:   str(m["tormentor_state_location"]),
		EndSeconds: numP(m["tormentor_state_end_seconds"]),
	}
}

func normalizeBuildings(v any) []BuildingTick {
	teams := asMap(v)
	if teams == nil {
		return nil
	}
	out := make([]BuildingTick, 0)
	teamKeys := sortedKeys(teams)
	for _, team := range teamKeys {
		buildings := asMap(teams[team])
		if buildings == nil {
			continue
		}
		for _, name := range sortedKeys(buildings) {
			stats := asMap(buildings[name])
			b := BuildingTick{Team: team, Name: name}
			if stats != nil {
				b.Health = numP(stats["health"])
				b.MaxHealth = numP(stats["max_health"])
			}
			out = append(out, b)
		}
	}
	return out
}

func normalizePlayers(root map[string]any) []PlayerTick {
	playerSec := asMap(root["player"])
	heroSec := asMap(root["hero"])
	itemsSec := asMap(root["items"])
	abilitiesSec := asMap(root["abilities"])

	// Build a stable list of (team, player) keys from whichever section has them.
	keys := playerTeamKeys(playerSec, heroSec, itemsSec, abilitiesSec)
	if len(keys) == 0 {
		return nil
	}

	out := make([]PlayerTick, 0, len(keys))
	for _, k := range keys {
		pt := PlayerTick{Slot: k.player, TeamKey: k.team}

		// Observed-field footprint: union of leaf paths actually present.
		obs := newFieldFootprint()

		if p := asMap(playerSec[k.team]); p != nil {
			pp := asMap(p[k.player])
			fillPlayerSection(&pt, pp, obs)
		}
		if h := asMap(heroSec[k.team]); h != nil {
			hh := asMap(h[k.player])
			fillHeroSection(&pt, hh, obs)
		}
		if it := asMap(itemsSec[k.team]); it != nil {
			ip := asMap(it[k.player])
			pt.Items = normalizeItems(ip, obs)
		}
		if ab := asMap(abilitiesSec[k.team]); ab != nil {
			ap := asMap(ab[k.player])
			pt.Abilities = normalizeAbilities(ap, obs)
		}

		pt.ObservedFields = obs.paths()
		out = append(out, pt)
	}
	return out
}

func fillPlayerSection(pt *PlayerTick, m map[string]any, obs *fieldFootprint) {
	if m == nil {
		return
	}
	pt.AccountID = str(m["accountid"])
	pt.SteamID = str(m["steamid"])
	pt.Name = str(m["name"])
	pt.TeamName = str(m["team_name"])
	pt.PlayerSlot = intP(m["player_slot"])

	pt.Gold = numP(m["gold"])
	pt.NetWorth = numP(m["net_worth"])
	pt.GPM = intP(m["gpm"])
	pt.XPM = intP(m["xpm"])
	pt.GoldReliable = numP(m["gold_reliable"])

	pt.Kills = intP(m["kills"])
	pt.Deaths = intP(m["deaths"])
	pt.Assists = intP(m["assists"])

	pt.LastHits = intP(m["last_hits"])
	pt.Denies = intP(m["denies"])

	pt.WardsPlaced = intP(m["wards_placed"])
	pt.WardsDestroyed = intP(m["wards_destroyed"])
	pt.WardsPurchased = intP(m["wards_purchased"])

	for _, key := range []string{
		"accountid", "steamid", "name", "team_name", "player_slot",
		"gold", "net_worth", "gpm", "xpm", "gold_reliable",
		"kills", "deaths", "assists", "last_hits", "denies",
		"wards_placed", "wards_destroyed", "wards_purchased",
	} {
		if _, ok := m[key]; ok {
			obs.add("player." + pt.TeamKey + "." + pt.Slot + "." + key)
		}
	}
}

func fillHeroSection(pt *PlayerTick, m map[string]any, obs *fieldFootprint) {
	if m == nil {
		return
	}
	pt.XPos = numP(m["xpos"])
	pt.YPos = numP(m["ypos"])

	pt.Health = numP(m["health"])
	pt.MaxHealth = numP(m["max_health"])
	pt.HealthPercent = numP(m["health_percent"])
	pt.Mana = numP(m["mana"])
	pt.MaxMana = numP(m["max_mana"])
	pt.ManaPercent = numP(m["mana_percent"])

	pt.Alive = boolP(m["alive"])
	pt.RespawnSeconds = numP(m["respawn_seconds"])
	pt.Level = intP(m["level"])
	pt.XP = numP(m["xp"])

	pt.Stunned = boolP(m["stunned"])
	pt.Silenced = boolP(m["silenced"])
	pt.Disarmed = boolP(m["disarmed"])
	pt.Hexed = boolP(m["hexed"])
	pt.Muted = boolP(m["muted"])
	pt.Break = boolP(m["break"])
	pt.HasDebuff = boolP(m["has_debuff"])
	pt.MagicImmune = boolP(m["magicimmune"])
	pt.Smoked = boolP(m["smoked"])

	for _, key := range []string{
		"xpos", "ypos", "health", "max_health", "health_percent",
		"mana", "max_mana", "mana_percent", "alive", "respawn_seconds",
		"level", "xp", "stunned", "silenced", "disarmed", "hexed",
		"muted", "break", "has_debuff", "magicimmune", "smoked",
	} {
		if _, ok := m[key]; ok {
			obs.add("hero." + pt.TeamKey + "." + pt.Slot + "." + key)
		}
	}
}

func normalizeItems(m map[string]any, obs *fieldFootprint) []ItemSlot {
	if m == nil {
		return nil
	}
	out := make([]ItemSlot, 0, len(m))
	for _, slot := range sortedKeys(m) {
		stats := asMap(m[slot])
		item := ItemSlot{Slot: slot, Name: str(stats["name"])}
		if stats != nil {
			item.ItemLevel = intP(stats["item_level"])
			item.Cooldown = numP(stats["cooldown"])
			item.MaxCooldown = numP(stats["max_cooldown"])
			item.CanCast = boolP(stats["can_cast"])
			item.Charges = intP(stats["charges"])
			item.Charges = firstNonNilInt(item.Charges, intP(stats["item_charges"]))
			item.Passive = boolP(stats["passive"])
		}
		out = append(out, item)
		obs.add("items." + slot + ".name")
	}
	return out
}

func normalizeAbilities(m map[string]any, obs *fieldFootprint) []AbilitySlot {
	if m == nil {
		return nil
	}
	out := make([]AbilitySlot, 0, len(m))
	for _, slot := range sortedKeys(m) {
		stats := asMap(m[slot])
		ab := AbilitySlot{Slot: slot, Name: str(stats["name"])}
		if stats != nil {
			ab.Level = intP(stats["level"])
			ab.Cooldown = numP(stats["cooldown"])
			ab.MaxCooldown = numP(stats["max_cooldown"])
			ab.CanCast = boolP(stats["can_cast"])
			ab.Passive = boolP(stats["passive"])
			ab.Ultimate = boolP(stats["ultimate"])
		}
		out = append(out, ab)
		obs.add("abilities." + slot + ".name")
	}
	return out
}

// ---- generic value extractors ------------------------------------------------

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strP(v any) *string {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func boolP(v any) *bool {
	if v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

// num coerces a leaf value (json.Number, float64, int*, numeric string) to float64.
func num(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func numP(v any) *float64 {
	f, ok := num(v)
	if !ok {
		return nil
	}
	return &f
}

func intOrZero(v any) int64 {
	f, ok := num(v)
	if !ok {
		return 0
	}
	return int64(f)
}

func intP(v any) *int64 {
	f, ok := num(v)
	if !ok {
		return nil
	}
	i := int64(f)
	return &i
}

func firstNonNilInt(a, b *int64) *int64 {
	if a != nil {
		return a
	}
	return b
}