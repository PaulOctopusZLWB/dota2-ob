package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type projectionLimits struct {
	participants, items, abilities, buildings bool
	identifier, sourceString, number          bool
}

var projectionReasonOrder = []struct {
	reason string
	hit    func(projectionLimits) bool
}{
	{"participant_count", func(x projectionLimits) bool { return x.participants }},
	{"item_count", func(x projectionLimits) bool { return x.items }},
	{"ability_count", func(x projectionLimits) bool { return x.abilities }},
	{"building_count", func(x projectionLimits) bool { return x.buildings }},
	{"identifier_bytes", func(x projectionLimits) bool { return x.identifier }},
	{"string_bytes", func(x projectionLimits) bool { return x.sourceString }},
	{"number_bytes", func(x projectionLimits) bool { return x.number }},
}

var simpleFields = map[string]map[string]bool{
	"provider": set("name", "appid", "version", "timestamp"),
	"league":   set("match_id"),
	"map":      set("matchid", "clock_time", "game_time", "game_state", "paused", "daytime", "nightstalker_night", "win_team", "radiant_score", "dire_score", "radiant_glyph_cooldown", "dire_glyph_cooldown", "radiant_scan_charges", "dire_scan_charges", "radiant_lotus_pool_count", "dire_lotus_pool_count", "radiant_ward_purchase_cooldown", "dire_ward_purchase_cooldown", "roshan_state", "roshan_state_end_seconds", "tormentor_state", "tormentor_state_location", "tormentor_state_end_seconds"),
}

var participantFields = map[string]map[string]bool{
	"player": set("accountid", "steamid", "name", "team_name", "player_slot", "gold", "net_worth", "gpm", "xpm", "gold_reliable", "gold_unreliable", "kills", "deaths", "assists", "last_hits", "denies", "wards_placed", "wards_destroyed", "wards_purchased"),
	"hero":   set("name", "id", "xpos", "ypos", "health", "max_health", "health_percent", "mana", "max_mana", "mana_percent", "alive", "respawn_seconds", "level", "xp", "buyback_cost", "buyback_cooldown", "stunned", "silenced", "disarmed", "hexed", "muted", "break", "has_debuff", "magicimmune", "smoked"),
}

var itemFields = set("name", "item_level", "cooldown", "max_cooldown", "can_cast", "charges", "item_charges", "passive")
var abilityFields = set("name", "level", "cooldown", "max_cooldown", "can_cast", "passive", "ultimate")
var buildingFields = set("health", "max_health")

func set(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

func decodeBoundedGSI(raw []byte) (any, ProjectionResult, string, string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	first, err := dec.Token()
	if err != nil {
		return nil, "", "", "", fmt.Errorf("%w: malformed value", ErrInvalidJSON)
	}
	if first != json.Delim('{') {
		if delim, ok := first.(json.Delim); ok {
			if err := skipDelimited(dec, delim); err != nil {
				return nil, "", "", "", fmt.Errorf("%w: malformed value", ErrInvalidJSON)
			}
		}
		if token, err := dec.Token(); err != io.EOF || token != nil {
			return nil, "", "", "", fmt.Errorf("%w: trailing data", ErrInvalidJSON)
		}
		return first, ProjectionConsumedNoOutput, "gsi_projection_non_object", "top_level_non_object", nil
	}
	known := map[string]bool{"provider": true, "league": true, "map": true, "player": true, "hero": true, "items": true, "abilities": true, "buildings": true}
	sections := map[string]json.RawMessage{}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, "", "", "", fmt.Errorf("%w: malformed object", ErrInvalidJSON)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, "", "", "", fmt.Errorf("%w: invalid object key", ErrInvalidJSON)
		}
		if known[key] {
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return nil, "", "", "", fmt.Errorf("%w: malformed member", ErrInvalidJSON)
			}
			sections[key] = value
		} else if err := skipValue(dec); err != nil {
			return nil, "", "", "", fmt.Errorf("%w: malformed member", ErrInvalidJSON)
		}
	}
	if closeToken, err := dec.Token(); err != nil || closeToken != json.Delim('}') {
		return nil, "", "", "", fmt.Errorf("%w: malformed object", ErrInvalidJSON)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "", "", "", fmt.Errorf("%w: trailing data", ErrInvalidJSON)
	}
	root := map[string]any{}
	for key, value := range sections {
		decoded, err := decodeJSON(value)
		if err != nil {
			return nil, "", "", "", err
		}
		root[key] = decoded
	}
	payload, result, code, reason := boundedGSIProjection(root)
	return payload, result, code, reason, nil
}

func skipValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		return skipDelimited(dec, delim)
	}
	return nil
}
func skipDelimited(dec *json.Decoder, open json.Delim) error {
	var close json.Delim
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return errors.New("unexpected closing delimiter")
	}
	for dec.More() {
		if open == '{' {
			if _, err := dec.Token(); err != nil {
				return err
			}
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	token, err := dec.Token()
	if err != nil || token != close {
		return errors.New("mismatched delimiter")
	}
	return nil
}

func boundedGSIProjection(payload any) (any, ProjectionResult, string, string) {
	root, ok := payload.(map[string]any)
	if !ok {
		return payload, ProjectionConsumedNoOutput, "gsi_projection_non_object", "top_level_non_object"
	}
	out := map[string]any{}
	limits := projectionLimits{}
	participants := map[string]bool{}
	for name, fields := range simpleFields {
		if value, exists := root[name]; exists {
			out[name] = boundedFields(value, fields, &limits)
		}
	}
	for _, section := range []string{"player", "hero"} {
		if value, exists := root[section]; exists {
			out[section] = boundedParticipants(value, participantFields[section], participants, &limits)
		}
	}
	if value, exists := root["items"]; exists {
		out["items"] = boundedEntries(value, itemFields, 32, participants, &limits, true)
	}
	if value, exists := root["abilities"]; exists {
		out["abilities"] = boundedEntries(value, abilityFields, 32, participants, &limits, false)
	}
	if value, exists := root["buildings"]; exists {
		out["buildings"] = boundedBuildings(value, &limits)
	}
	if len(participants) > 10 {
		limits.participants = true
	}
	for _, entry := range projectionReasonOrder {
		if entry.hit(limits) {
			return out, ProjectionConsumedNoOutput, "gsi_projection_bounds_exceeded", entry.reason
		}
	}
	return out, ProjectionProduced, "", ""
}

// PrepareGSIProjection applies the bounded V3 projection domain to immutable
// V1/V2 compatibility inputs without rewriting their persisted envelope.
func PrepareGSIProjection(payload any) (any, ProjectionResult, string, string) {
	return boundedGSIProjection(payload)
}

func boundedFields(value any, fields map[string]bool, limits *projectionLimits) any {
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for key, v := range m {
		if fields[key] {
			out[key] = boundedScalar(v, limits)
		}
	}
	return out
}
func boundedParticipants(value any, fields map[string]bool, participants map[string]bool, limits *projectionLimits) any {
	teams, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for team, tv := range teams {
		slots, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		checkID(team, limits)
		bounded := map[string]any{}
		for slot, v := range slots {
			checkID(slot, limits)
			participants[team+"\x00"+slot] = true
			bounded[slot] = boundedFields(v, fields, limits)
		}
		out[team] = bounded
	}
	return out
}
func boundedEntries(value any, fields map[string]bool, max int, participants map[string]bool, limits *projectionLimits, item bool) any {
	teams, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for team, tv := range teams {
		slots, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		checkID(team, limits)
		boundedSlots := map[string]any{}
		for slot, sv := range slots {
			entries, ok := sv.(map[string]any)
			if !ok {
				continue
			}
			checkID(slot, limits)
			participants[team+"\x00"+slot] = true
			if len(entries) > max {
				if item {
					limits.items = true
				} else {
					limits.abilities = true
				}
			}
			boundedEntries := map[string]any{}
			for id, v := range entries {
				checkID(id, limits)
				if len(boundedEntries) < max+1 {
					boundedEntries[id] = boundedFields(v, fields, limits)
				}
			}
			boundedSlots[slot] = boundedEntries
		}
		out[team] = boundedSlots
	}
	return out
}
func boundedBuildings(value any, limits *projectionLimits) any {
	teams, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	count := 0
	for team, tv := range teams {
		buildings, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		checkID(team, limits)
		bounded := map[string]any{}
		for id, v := range buildings {
			count++
			checkID(id, limits)
			if len(bounded) < 65 {
				bounded[id] = boundedFields(v, buildingFields, limits)
			}
		}
		out[team] = bounded
	}
	if count > 64 {
		limits.buildings = true
	}
	return out
}
func boundedScalar(value any, limits *projectionLimits) any {
	switch v := value.(type) {
	case string:
		if len([]byte(v)) > 256 {
			limits.sourceString = true
		}
		return v
	case json.Number:
		if len(v.String()) > 128 {
			limits.number = true
		}
		return v
	case nil, bool:
		return v
	default:
		return nil
	}
}
func checkID(value string, limits *projectionLimits) {
	if len([]byte(value)) > 128 {
		limits.identifier = true
	}
}
