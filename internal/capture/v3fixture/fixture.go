package v3fixture

import (
	"encoding/json"
	"fmt"
	"strings"
)

const RawLimit = 10 << 20

var maxNumber = json.RawMessage(strings.Repeat("9", 128))

// MaximumRelevantBody returns the exact 10 MiB migration-gate fixture. Every
// retained identifier/string/number reaches its lexical bound; the relevant
// projection contains 10 participants, 32 items and abilities per participant,
// and 64 buildings. An unknown filler member supplies the remaining bytes.
func MaximumRelevantBody() ([]byte, error) {
	root := map[string]any{
		"provider": fields([]string{"appid", "version", "timestamp"}, []string{"name"}),
		"league":   map[string]any{"match_id": maxString()},
		"map":      fields([]string{"clock_time", "game_time", "radiant_score", "dire_score", "radiant_glyph_cooldown", "dire_glyph_cooldown", "radiant_scan_charges", "dire_scan_charges", "radiant_lotus_pool_count", "dire_lotus_pool_count", "radiant_ward_purchase_cooldown", "dire_ward_purchase_cooldown", "roshan_state_end_seconds", "tormentor_state_end_seconds"}, []string{"game_state", "win_team", "roshan_state", "tormentor_state", "tormentor_state_location"}),
		"player":   map[string]any{}, "hero": map[string]any{}, "items": map[string]any{}, "abilities": map[string]any{}, "buildings": map[string]any{},
		"unknown_filler": "",
	}
	withBools(root["map"].(map[string]any), "paused", "daytime", "nightstalker_night")
	for _, section := range []string{"player", "hero", "items", "abilities"} {
		root[section] = map[string]any{id128("team", 0): map[string]any{}, id128("team", 1): map[string]any{}}
	}
	for i := 0; i < 10; i++ {
		team := id128("team", i/5)
		slot := id128("participant", i)
		root["player"].(map[string]any)[team].(map[string]any)[slot] = fields([]string{"player_slot", "gold", "net_worth", "gpm", "xpm", "gold_reliable", "gold_unreliable", "kills", "deaths", "assists", "last_hits", "denies", "wards_placed", "wards_destroyed", "wards_purchased"}, []string{"accountid", "steamid", "name", "team_name"})
		root["hero"].(map[string]any)[team].(map[string]any)[slot] = fields([]string{"id", "xpos", "ypos", "health", "max_health", "health_percent", "mana", "max_mana", "mana_percent", "respawn_seconds", "level", "xp", "buyback_cost", "buyback_cooldown"}, []string{"name"})
		withBools(root["hero"].(map[string]any)[team].(map[string]any)[slot].(map[string]any), "alive", "stunned", "silenced", "disarmed", "hexed", "muted", "break", "has_debuff", "magicimmune", "smoked")
		items := map[string]any{}
		abilities := map[string]any{}
		for j := 0; j < 32; j++ {
			items[id128("item", j)] = fields([]string{"item_level", "cooldown", "max_cooldown", "charges", "item_charges"}, []string{"name"})
			abilities[id128("ability", j)] = fields([]string{"level", "cooldown", "max_cooldown"}, []string{"name"})
			withBools(items[id128("item", j)].(map[string]any), "can_cast", "passive")
			withBools(abilities[id128("ability", j)].(map[string]any), "can_cast", "passive", "ultimate")
		}
		root["items"].(map[string]any)[team].(map[string]any)[slot] = items
		root["abilities"].(map[string]any)[team].(map[string]any)[slot] = abilities
	}
	buildings := map[string]any{}
	for i := 0; i < 64; i++ {
		buildings[id128("building", i)] = fields([]string{"health", "max_health"}, nil)
	}
	root["buildings"].(map[string]any)[id128("building-team", 0)] = buildings
	empty, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	filler := RawLimit - len(empty)
	if filler < 0 {
		return nil, fmt.Errorf("maximum projection exceeds raw limit by %d bytes", -filler)
	}
	root["unknown_filler"] = strings.Repeat("x", filler)
	body, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	if len(body) != RawLimit {
		return nil, fmt.Errorf("fixture size %d, want %d", len(body), RawLimit)
	}
	return body, nil
}

func fields(numbers, stringsFields []string) map[string]any {
	out := map[string]any{}
	for _, name := range numbers {
		out[name] = maxNumber
	}
	for _, name := range stringsFields {
		out[name] = maxString()
	}
	return out
}
func withBools(out map[string]any, names ...string) {
	for _, name := range names {
		out[name] = true
	}
}
func maxString() string { return strings.Repeat("界", 85) + "x" }
func id128(prefix string, index int) string {
	head := fmt.Sprintf("%s-%03d-", prefix, index)
	return head + strings.Repeat("x", 128-len(head))
}
