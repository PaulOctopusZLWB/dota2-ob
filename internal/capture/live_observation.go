package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const liveObservationMappingVersion = "gsi_normalized.v1"

// MapLiveObservationV1 deterministically projects one committed raw GSI record
// into the public cross-track contract. Account IDs, Steam IDs, and observed
// player handles intentionally remain in the capture-owned legacy view.
func MapLiveObservationV1(record *session.Record) (contracts.LiveObservationV1, error) {
	if record == nil || record.SchemaVersion <= 0 || record.SessionID == "" || record.Sequence == 0 || record.ReceivedAt.IsZero() || record.Source != "gsi" || len(record.Raw) == 0 {
		return contracts.LiveObservationV1{}, errors.New("invalid committed GSI record")
	}
	tick := analytics.Normalize(record.ReceivedAt, record.Payload)
	digest := sha256.Sum256(record.Raw)
	root, _ := record.Payload.(map[string]any)
	provider := mapValue(root, "provider")
	mapSection := mapValue(root, "map")

	observation := contracts.LiveObservationV1{
		SchemaVersion:  contracts.LiveObservationSchemaV1,
		MappingVersion: liveObservationMappingVersion,
		Evidence: contracts.EvidenceRefV1{
			RecordSchemaVersion: record.SchemaVersion, SessionID: record.SessionID,
			Sequence: record.Sequence, ReceiveTime: record.ReceivedAt.UTC(), Source: record.Source,
			ProviderVersion: observedInt(provider, "version"), RawPayloadSHA256: hex.EncodeToString(digest[:]),
		},
		Provider: contracts.ProviderObservationV1{Name: observedString(provider, "name"), AppID: observedInt(provider, "appid"), Timestamp: observedInt(provider, "timestamp")},
		MatchID:  observedMatchID(root, tick.MatchID), ClockBasis: "gsi_map_clock_and_game_time",
		Map:       mapMapObservation(mapSection, tick.Map),
		Roshan:    contracts.ObjectiveObservationV1{State: observedString(mapSection, "roshan_state"), Location: contracts.Absent[string](), EndSeconds: observedFloatPointer(tick.Roshan.EndSeconds)},
		Tormentor: contracts.ObjectiveObservationV1{State: observedString(mapSection, "tormentor_state"), Location: observedString(mapSection, "tormentor_state_location"), EndSeconds: observedFloatPointer(tick.Tormentor.EndSeconds)},
		Buildings: mapBuildings(tick.Buildings), Participants: mapParticipants(tick.Players),
		Quality: contracts.SourceQualityV1{Confidence: "observed", Flags: []string{}},
	}
	return observation, nil
}

func mapMapObservation(raw map[string]any, value analytics.MapState) contracts.MapObservationV1 {
	return contracts.MapObservationV1{
		ClockTime: observedIntPointer(value.ClockTime), GameTime: observedIntPointer(value.GameTime), GameState: observedString(raw, "game_state"), Paused: observedBoolPointer(value.Paused), Daytime: observedBoolPointer(value.Daytime), NightstalkerNight: observedBoolPointer(value.NightstalkerNight), WinTeam: observedString(raw, "win_team"),
		RadiantScore: observedIntPointer(value.RadiantScore), DireScore: observedIntPointer(value.DireScore), RadiantGlyphCooldown: observedFloatPointer(value.RadiantGlyphCooldown), DireGlyphCooldown: observedFloatPointer(value.DireGlyphCooldown), RadiantScanCharges: observedIntPointer(value.RadiantScanCharges), DireScanCharges: observedIntPointer(value.DireScanCharges), RadiantLotusPoolCount: observedIntPointer(value.RadiantLotusPoolCount), DireLotusPoolCount: observedIntPointer(value.DireLotusPoolCount), RadiantWardPurchaseCooldown: observedFloatPointer(value.RadiantWardPurchaseCooldown), DireWardPurchaseCooldown: observedFloatPointer(value.DireWardPurchaseCooldown),
	}
}

func mapBuildings(values []analytics.BuildingTick) []contracts.BuildingObservationV1 {
	out := make([]contracts.BuildingObservationV1, 0, len(values))
	for _, value := range values {
		out = append(out, contracts.BuildingObservationV1{Team: value.Team, Name: value.Name, Health: observedFloatPointer(value.Health), MaxHealth: observedFloatPointer(value.MaxHealth)})
	}
	return out
}

func mapParticipants(values []analytics.PlayerTick) []contracts.ParticipantObservationV1 {
	out := make([]contracts.ParticipantObservationV1, 0, len(values))
	for _, v := range values {
		present := func(path string) bool {
			for _, p := range v.ObservedFields {
				if p == path {
					return true
				}
			}
			return false
		}
		playerPath := func(field string) string { return "player." + v.TeamKey + "." + v.Slot + "." + field }
		heroPath := func(field string) string { return "hero." + v.TeamKey + "." + v.Slot + "." + field }
		p := contracts.ParticipantObservationV1{SessionSlot: v.Slot, TeamKey: v.TeamKey,
			TeamName: observedStringValue(v.TeamName, present(playerPath("team_name"))), PlayerSlot: observedIntPointer(v.PlayerSlot), HeroName: observedStringValue(v.HeroName, present(heroPath("name"))), HeroID: observedIntPointer(v.HeroID),
			Gold: observedFloatPointer(v.Gold), NetWorth: observedFloatPointer(v.NetWorth), GPM: observedIntPointer(v.GPM), XPM: observedIntPointer(v.XPM), GoldReliable: observedFloatPointer(v.GoldReliable), GoldUnreliable: observedFloatPointer(v.GoldUnreliable), Kills: observedIntPointer(v.Kills), Deaths: observedIntPointer(v.Deaths), Assists: observedIntPointer(v.Assists), LastHits: observedIntPointer(v.LastHits), Denies: observedIntPointer(v.Denies), XPos: observedFloatPointer(v.XPos), YPos: observedFloatPointer(v.YPos), Health: observedFloatPointer(v.Health), MaxHealth: observedFloatPointer(v.MaxHealth), HealthPercent: observedFloatPointer(v.HealthPercent), Mana: observedFloatPointer(v.Mana), MaxMana: observedFloatPointer(v.MaxMana), ManaPercent: observedFloatPointer(v.ManaPercent), Alive: observedBoolPointer(v.Alive), RespawnSeconds: observedFloatPointer(v.RespawnSeconds), Level: observedIntPointer(v.Level), XP: observedFloatPointer(v.XP), BuybackCost: observedFloatPointer(v.BuybackCost), BuybackCooldown: observedFloatPointer(v.BuybackCooldown), Stunned: observedBoolPointer(v.Stunned), Silenced: observedBoolPointer(v.Silenced), Disarmed: observedBoolPointer(v.Disarmed), Hexed: observedBoolPointer(v.Hexed), Muted: observedBoolPointer(v.Muted), Break: observedBoolPointer(v.Break), HasDebuff: observedBoolPointer(v.HasDebuff), MagicImmune: observedBoolPointer(v.MagicImmune), Smoked: observedBoolPointer(v.Smoked), WardsPlaced: observedIntPointer(v.WardsPlaced), WardsDestroyed: observedIntPointer(v.WardsDestroyed), WardsPurchased: observedIntPointer(v.WardsPurchased), Items: mapItems(v.Items), Abilities: mapAbilities(v.Abilities)}
		out = append(out, p)
	}
	return out
}

func mapItems(values []analytics.ItemSlot) []contracts.ItemObservationV1 {
	out := make([]contracts.ItemObservationV1, 0, len(values))
	for _, v := range values {
		out = append(out, contracts.ItemObservationV1{Slot: v.Slot, Name: contracts.Present(v.Name), ItemLevel: observedIntPointer(v.ItemLevel), Cooldown: observedFloatPointer(v.Cooldown), MaxCooldown: observedFloatPointer(v.MaxCooldown), CanCast: observedBoolPointer(v.CanCast), Charges: observedIntPointer(v.Charges), Passive: observedBoolPointer(v.Passive)})
	}
	return out
}
func mapAbilities(values []analytics.AbilitySlot) []contracts.AbilityObservationV1 {
	out := make([]contracts.AbilityObservationV1, 0, len(values))
	for _, v := range values {
		out = append(out, contracts.AbilityObservationV1{Slot: v.Slot, Name: contracts.Present(v.Name), Level: observedIntPointer(v.Level), Cooldown: observedFloatPointer(v.Cooldown), MaxCooldown: observedFloatPointer(v.MaxCooldown), CanCast: observedBoolPointer(v.CanCast), Passive: observedBoolPointer(v.Passive), Ultimate: observedBoolPointer(v.Ultimate)})
	}
	return out
}

func observedIntPointer(v *int64) contracts.ObservedV1[int64] {
	if v == nil {
		return contracts.Absent[int64]()
	}
	return contracts.Present(*v)
}
func observedFloatPointer(v *float64) contracts.ObservedV1[float64] {
	if v == nil {
		return contracts.Absent[float64]()
	}
	return contracts.Present(*v)
}
func observedBoolPointer(v *bool) contracts.ObservedV1[bool] {
	if v == nil {
		return contracts.Absent[bool]()
	}
	return contracts.Present(*v)
}
func observedStringValue(v string, present bool) contracts.ObservedV1[string] {
	if !present {
		return contracts.Absent[string]()
	}
	return contracts.Present(v)
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
func observedInt(m map[string]any, key string) contracts.ObservedV1[int64] {
	v, ok := m[key]
	if !ok {
		return contracts.Absent[int64]()
	}
	switch n := v.(type) {
	case json.Number:
		i, e := n.Int64()
		if e == nil {
			return contracts.Present(i)
		}
	case float64:
		return contracts.Present(int64(n))
	case int64:
		return contracts.Present(n)
	}
	return contracts.ObservedV1[int64]{State: contracts.ValueInvalid}
}
func observedMatchID(root map[string]any, value string) contracts.ObservedV1[string] {
	if league := mapValue(root, "league"); league != nil {
		if _, ok := league["match_id"]; ok {
			return observedStringValue(value, true)
		}
	}
	if m := mapValue(root, "map"); m != nil {
		if _, ok := m["matchid"]; ok {
			return observedStringValue(value, true)
		}
	}
	return contracts.Absent[string]()
}
func mapValue(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}
