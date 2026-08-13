package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const liveObservationMappingVersion = "gsi_normalized.v1"

// MapLiveObservationV1 is the source parser for the canonical cross-track
// observation. It reads the accepted raw record directly and never constructs
// a private legacy tick.
func MapLiveObservationV1(record *session.Record) (contracts.LiveObservationV1, error) {
	if record == nil || record.SchemaVersion <= 0 || record.SessionID == "" || record.Sequence == 0 || record.ReceivedAt.IsZero() || record.Source != "gsi" || len(record.Raw) == 0 {
		return contracts.LiveObservationV1{}, errors.New("invalid committed GSI record")
	}
	root, ok := record.Payload.(map[string]any)
	if !ok {
		return contracts.LiveObservationV1{}, errors.New("GSI payload is not an object")
	}
	provider := mapValue(root, "provider")
	m := mapValue(root, "map")
	h := sha256.Sum256(record.Raw)
	o := contracts.LiveObservationV1{SchemaVersion: contracts.LiveObservationSchemaV1, MappingVersion: liveObservationMappingVersion, Evidence: contracts.EvidenceRefV1{RecordSchemaVersion: record.SchemaVersion, SessionID: record.SessionID, Sequence: record.Sequence, ReceiveTime: record.ReceivedAt.UTC(), Source: record.Source, ProviderVersion: observedInt(provider, "version"), RawPayloadSHA256: hex.EncodeToString(h[:])}, Provider: contracts.ProviderObservationV1{Name: observedString(provider, "name"), AppID: observedInt(provider, "appid"), Timestamp: observedInt(provider, "timestamp")}, MatchID: observedMatchID(root), ClockBasis: "gsi_map_clock_and_game_time", Map: mapObservation(m), Roshan: contracts.ObjectiveObservationV1{State: observedString(m, "roshan_state"), Location: contracts.Absent[string](), EndSeconds: observedDecimal(m, "roshan_state_end_seconds")}, Tormentor: contracts.ObjectiveObservationV1{State: observedString(m, "tormentor_state"), Location: observedString(m, "tormentor_state_location"), EndSeconds: observedDecimal(m, "tormentor_state_end_seconds")}, Buildings: mapBuildings(mapValue(root, "buildings")), Participants: mapParticipants(root), Quality: contracts.SourceQualityV1{Confidence: "observed", Flags: []string{}}}
	return o, nil
}

func mapObservation(m map[string]any) contracts.MapObservationV1 {
	return contracts.MapObservationV1{ClockTime: observedInt(m, "clock_time"), GameTime: observedInt(m, "game_time"), GameState: observedString(m, "game_state"), Paused: observedBool(m, "paused"), Daytime: observedBool(m, "daytime"), NightstalkerNight: observedBool(m, "nightstalker_night"), WinTeam: observedString(m, "win_team"), RadiantScore: observedInt(m, "radiant_score"), DireScore: observedInt(m, "dire_score"), RadiantGlyphCooldown: observedDecimal(m, "radiant_glyph_cooldown"), DireGlyphCooldown: observedDecimal(m, "dire_glyph_cooldown"), RadiantScanCharges: observedInt(m, "radiant_scan_charges"), DireScanCharges: observedInt(m, "dire_scan_charges"), RadiantLotusPoolCount: observedInt(m, "radiant_lotus_pool_count"), DireLotusPoolCount: observedInt(m, "dire_lotus_pool_count"), RadiantWardPurchaseCooldown: observedDecimal(m, "radiant_ward_purchase_cooldown"), DireWardPurchaseCooldown: observedDecimal(m, "dire_ward_purchase_cooldown")}
}
func mapBuildings(teams map[string]any) []contracts.BuildingObservationV1 {
	if teams == nil {
		return nil
	}
	var out []contracts.BuildingObservationV1
	for _, team := range sortedKeys(teams) {
		for _, name := range sortedKeys(mapValue(teams, team)) {
			stats := mapValue(mapValue(teams, team), name)
			out = append(out, contracts.BuildingObservationV1{Team: team, Name: name, Health: observedDecimal(stats, "health"), MaxHealth: observedDecimal(stats, "max_health")})
		}
	}
	return out
}

type participantKey struct{ team, slot string }

func mapParticipants(root map[string]any) []contracts.ParticipantObservationV1 {
	sections := []map[string]any{mapValue(root, "player"), mapValue(root, "hero"), mapValue(root, "items"), mapValue(root, "abilities")}
	keys := map[participantKey]bool{}
	for _, section := range sections {
		for _, team := range sortedKeys(section) {
			for _, slot := range sortedKeys(mapValue(section, team)) {
				keys[participantKey{team, slot}] = true
			}
		}
	}
	ordered := make([]participantKey, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].team == ordered[j].team {
			return ordered[i].slot < ordered[j].slot
		}
		return ordered[i].team < ordered[j].team
	})
	if len(ordered) == 0 {
		return nil
	}
	out := make([]contracts.ParticipantObservationV1, 0, len(ordered))
	for _, k := range ordered {
		p := mapValue(mapValue(mapValue(root, "player"), k.team), k.slot)
		h := mapValue(mapValue(mapValue(root, "hero"), k.team), k.slot)
		items := mapValue(mapValue(mapValue(root, "items"), k.team), k.slot)
		abilities := mapValue(mapValue(mapValue(root, "abilities"), k.team), k.slot)
		out = append(out, contracts.ParticipantObservationV1{SessionSlot: k.slot, TeamKey: k.team, TeamName: observedString(p, "team_name"), PlayerSlot: observedInt(p, "player_slot"), HeroName: observedString(h, "name"), HeroID: observedInt(h, "id"), Gold: observedDecimal(p, "gold"), NetWorth: observedDecimal(p, "net_worth"), GPM: observedInt(p, "gpm"), XPM: observedInt(p, "xpm"), GoldReliable: observedDecimal(p, "gold_reliable"), GoldUnreliable: observedDecimal(p, "gold_unreliable"), Kills: observedInt(p, "kills"), Deaths: observedInt(p, "deaths"), Assists: observedInt(p, "assists"), LastHits: observedInt(p, "last_hits"), Denies: observedInt(p, "denies"), XPos: observedDecimal(h, "xpos"), YPos: observedDecimal(h, "ypos"), Health: observedDecimal(h, "health"), MaxHealth: observedDecimal(h, "max_health"), HealthPercent: observedDecimal(h, "health_percent"), Mana: observedDecimal(h, "mana"), MaxMana: observedDecimal(h, "max_mana"), ManaPercent: observedDecimal(h, "mana_percent"), Alive: observedBool(h, "alive"), RespawnSeconds: observedDecimal(h, "respawn_seconds"), Level: observedInt(h, "level"), XP: observedDecimal(h, "xp"), BuybackCost: observedDecimal(h, "buyback_cost"), BuybackCooldown: observedDecimal(h, "buyback_cooldown"), Stunned: observedBool(h, "stunned"), Silenced: observedBool(h, "silenced"), Disarmed: observedBool(h, "disarmed"), Hexed: observedBool(h, "hexed"), Muted: observedBool(h, "muted"), Break: observedBool(h, "break"), HasDebuff: observedBool(h, "has_debuff"), MagicImmune: observedBool(h, "magicimmune"), Smoked: observedBool(h, "smoked"), WardsPlaced: observedInt(p, "wards_placed"), WardsDestroyed: observedInt(p, "wards_destroyed"), WardsPurchased: observedInt(p, "wards_purchased"), Items: mapItems(items), Abilities: mapAbilities(abilities)})
	}
	return out
}
func mapItems(m map[string]any) []contracts.ItemObservationV1 {
	if m == nil {
		return nil
	}
	out := make([]contracts.ItemObservationV1, 0, len(m))
	for _, slot := range sortedKeys(m) {
		x := mapValue(m, slot)
		charges := observedInt(x, "charges")
		if charges.State == contracts.ValueAbsent {
			charges = observedInt(x, "item_charges")
		}
		out = append(out, contracts.ItemObservationV1{Slot: slot, Name: observedString(x, "name"), ItemLevel: observedInt(x, "item_level"), Cooldown: observedDecimal(x, "cooldown"), MaxCooldown: observedDecimal(x, "max_cooldown"), CanCast: observedBool(x, "can_cast"), Charges: charges, Passive: observedBool(x, "passive")})
	}
	return out
}
func mapAbilities(m map[string]any) []contracts.AbilityObservationV1 {
	if m == nil {
		return nil
	}
	out := make([]contracts.AbilityObservationV1, 0, len(m))
	for _, slot := range sortedKeys(m) {
		x := mapValue(m, slot)
		out = append(out, contracts.AbilityObservationV1{Slot: slot, Name: observedString(x, "name"), Level: observedInt(x, "level"), Cooldown: observedDecimal(x, "cooldown"), MaxCooldown: observedDecimal(x, "max_cooldown"), CanCast: observedBool(x, "can_cast"), Passive: observedBool(x, "passive"), Ultimate: observedBool(x, "ultimate")})
	}
	return out
}
func observedMatchID(root map[string]any) contracts.ObservedV1[string] {
	if league := mapValue(root, "league"); league != nil {
		if _, ok := league["match_id"]; ok {
			return observedString(league, "match_id")
		}
	}
	return observedString(mapValue(root, "map"), "matchid")
}
func observedString(m map[string]any, key string) contracts.ObservedV1[string] {
	v, ok := m[key]
	if !ok {
		return contracts.Absent[string]()
	}
	s, ok := v.(string)
	if !ok {
		return contracts.ObservedV1[string]{State: contracts.ValueInvalid}
	}
	return contracts.Present(s)
}
func observedBool(m map[string]any, key string) contracts.ObservedV1[bool] {
	v, ok := m[key]
	if !ok {
		return contracts.Absent[bool]()
	}
	b, ok := v.(bool)
	if !ok {
		return contracts.ObservedV1[bool]{State: contracts.ValueInvalid}
	}
	return contracts.Present(b)
}
func observedInt(m map[string]any, key string) contracts.ObservedV1[int64] {
	v, ok := m[key]
	if !ok {
		return contracts.Absent[int64]()
	}
	n, ok := number(v)
	if !ok {
		return contracts.ObservedV1[int64]{State: contracts.ValueInvalid}
	}
	return contracts.Present(int64(n))
}
func observedDecimal(m map[string]any, key string) contracts.ObservedV1[contracts.Decimal] {
	v, ok := m[key]
	if !ok {
		return contracts.Absent[contracts.Decimal]()
	}
	d, ok := sourceDecimal(v)
	if !ok {
		return contracts.ObservedV1[contracts.Decimal]{State: contracts.ValueInvalid}
	}
	return contracts.Present(d)
}

func sourceDecimal(v any) (contracts.Decimal, bool) {
	var token string
	switch x := v.(type) {
	case json.Number:
		token = x.String()
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "", false
		}
		token = strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return "", false
		}
		token = strconv.FormatFloat(float64(x), 'g', -1, 32)
	case int:
		token = strconv.FormatInt(int64(x), 10)
	case int64:
		token = strconv.FormatInt(x, 10)
	case int32:
		token = strconv.FormatInt(int64(x), 10)
	case uint64:
		token = strconv.FormatUint(x, 10)
	case string:
		token = x
	default:
		return "", false
	}
	d := contracts.Decimal(token)
	n, err := strconv.ParseFloat(token, 64)
	return d, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && d.Valid()
}
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		n, e := x.Float64()
		return n, e == nil
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case string:
		n, e := strconv.ParseFloat(x, 64)
		return n, e == nil
	}
	return 0, false
}
func mapValue(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
