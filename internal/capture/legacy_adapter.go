package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

// EvidenceResolver is the capture-owned boundary that can recover the raw
// record referenced by a public observation. M1-M3 receive neither this port
// nor the private legacy result.
type EvidenceResolver interface {
	Resolve(contracts.EvidenceRefV1) (*session.Record, error)
}
type EvidenceResolverFunc func(contracts.EvidenceRefV1) (*session.Record, error)

func (f EvidenceResolverFunc) Resolve(e contracts.EvidenceRefV1) (*session.Record, error) {
	return f(e)
}

// LegacyNormalizedTickV1 resolves private source evidence, verifies its exact
// identity/hash, and reconstructs the accepted legacy tick with public fields
// taken from LiveObservationV1. It is the sole observation-to-legacy adapter.
func LegacyNormalizedTickV1(observation contracts.LiveObservationV1, resolver EvidenceResolver) (analytics.NormalizedTick, error) {
	if err := observation.Validate(); err != nil {
		return analytics.NormalizedTick{}, err
	}
	if resolver == nil {
		return analytics.NormalizedTick{}, errors.New("evidence resolver is required")
	}
	record, err := resolver.Resolve(observation.Evidence)
	if err != nil {
		return analytics.NormalizedTick{}, err
	}
	if err := verifyEvidence(record, observation.Evidence); err != nil {
		return analytics.NormalizedTick{}, err
	}
	legacy := analytics.Normalize(record.ReceivedAt, record.Payload)
	applyPublicObservation(&legacy, observation)
	return legacy, nil
}

func verifyEvidence(record *session.Record, e contracts.EvidenceRefV1) error {
	if record == nil || record.SchemaVersion != e.RecordSchemaVersion || record.SessionID != e.SessionID || record.Sequence != e.Sequence || !record.ReceivedAt.UTC().Equal(e.ReceiveTime) || record.Source != e.Source {
		return errors.New("resolved evidence identity mismatch")
	}
	h := sha256.Sum256(record.Raw)
	rawHash := record.RawPayloadSHA256
	if rawHash == "" {
		rawHash = hex.EncodeToString(h[:])
	}
	if rawHash != e.RawPayloadSHA256 {
		return errors.New("resolved evidence hash mismatch")
	}
	return nil
}
func currentRecordResolver(record *session.Record) EvidenceResolver {
	return EvidenceResolverFunc(func(e contracts.EvidenceRefV1) (*session.Record, error) {
		if record == nil || record.SessionID != e.SessionID || record.Sequence != e.Sequence {
			return nil, errors.New("evidence not found")
		}
		return record, nil
	})
}

func applyPublicObservation(t *analytics.NormalizedTick, o contracts.LiveObservationV1) {
	t.ReceivedAt = o.Evidence.ReceiveTime
	t.Provider.Name = stringValue(o.Provider.Name)
	t.Provider.AppID = intValue(o.Provider.AppID)
	t.Provider.Version = intValue(o.Evidence.ProviderVersion)
	t.Provider.Timestamp = intValue(o.Provider.Timestamp)
	t.MatchID = stringValue(o.MatchID)
	t.Map = analytics.MapState{ClockTime: intPointer(o.Map.ClockTime), GameTime: intPointer(o.Map.GameTime), GameState: stringValue(o.Map.GameState), Paused: boolPointer(o.Map.Paused), Daytime: boolPointer(o.Map.Daytime), NightstalkerNight: boolPointer(o.Map.NightstalkerNight), WinTeam: stringValue(o.Map.WinTeam), RadiantScore: intPointer(o.Map.RadiantScore), DireScore: intPointer(o.Map.DireScore), RadiantGlyphCooldown: decimalPointer(o.Map.RadiantGlyphCooldown), DireGlyphCooldown: decimalPointer(o.Map.DireGlyphCooldown), RadiantScanCharges: intPointer(o.Map.RadiantScanCharges), DireScanCharges: intPointer(o.Map.DireScanCharges), RadiantLotusPoolCount: intPointer(o.Map.RadiantLotusPoolCount), DireLotusPoolCount: intPointer(o.Map.DireLotusPoolCount), RadiantWardPurchaseCooldown: decimalPointer(o.Map.RadiantWardPurchaseCooldown), DireWardPurchaseCooldown: decimalPointer(o.Map.DireWardPurchaseCooldown)}
	t.Roshan = analytics.RoshanState{State: stringValue(o.Roshan.State), EndSeconds: decimalPointer(o.Roshan.EndSeconds)}
	t.Tormentor = analytics.TormentorState{State: stringValue(o.Tormentor.State), Location: stringValue(o.Tormentor.Location), EndSeconds: decimalPointer(o.Tormentor.EndSeconds)}
	if o.Buildings == nil {
		t.Buildings = nil
	} else {
		t.Buildings = make([]analytics.BuildingTick, len(o.Buildings))
	}
	for i, x := range o.Buildings {
		t.Buildings[i] = analytics.BuildingTick{Team: x.Team, Name: x.Name, Health: decimalPointer(x.Health), MaxHealth: decimalPointer(x.MaxHealth)}
	}
	private := map[string]analytics.PlayerTick{}
	for _, p := range t.Players {
		private[p.TeamKey+"\x00"+p.Slot] = p
	}
	if o.Participants == nil {
		t.Players = nil
	} else {
		t.Players = make([]analytics.PlayerTick, len(o.Participants))
	}
	for i, x := range o.Participants {
		p := private[x.TeamKey+"\x00"+x.SessionSlot]
		p.Slot = x.SessionSlot
		p.TeamKey = x.TeamKey
		p.TeamName = stringValue(x.TeamName)
		p.PlayerSlot = intPointer(x.PlayerSlot)
		p.HeroName = stringValue(x.HeroName)
		p.HeroID = intPointer(x.HeroID)
		p.Gold = decimalPointer(x.Gold)
		p.NetWorth = decimalPointer(x.NetWorth)
		p.GPM = intPointer(x.GPM)
		p.XPM = intPointer(x.XPM)
		p.GoldReliable = decimalPointer(x.GoldReliable)
		p.GoldUnreliable = decimalPointer(x.GoldUnreliable)
		p.Kills = intPointer(x.Kills)
		p.Deaths = intPointer(x.Deaths)
		p.Assists = intPointer(x.Assists)
		p.LastHits = intPointer(x.LastHits)
		p.Denies = intPointer(x.Denies)
		p.XPos = decimalPointer(x.XPos)
		p.YPos = decimalPointer(x.YPos)
		p.Health = decimalPointer(x.Health)
		p.MaxHealth = decimalPointer(x.MaxHealth)
		p.HealthPercent = decimalPointer(x.HealthPercent)
		p.Mana = decimalPointer(x.Mana)
		p.MaxMana = decimalPointer(x.MaxMana)
		p.ManaPercent = decimalPointer(x.ManaPercent)
		p.Alive = boolPointer(x.Alive)
		p.RespawnSeconds = decimalPointer(x.RespawnSeconds)
		p.Level = intPointer(x.Level)
		p.XP = decimalPointer(x.XP)
		p.BuybackCost = decimalPointer(x.BuybackCost)
		p.BuybackCooldown = decimalPointer(x.BuybackCooldown)
		p.Stunned = boolPointer(x.Stunned)
		p.Silenced = boolPointer(x.Silenced)
		p.Disarmed = boolPointer(x.Disarmed)
		p.Hexed = boolPointer(x.Hexed)
		p.Muted = boolPointer(x.Muted)
		p.Break = boolPointer(x.Break)
		p.HasDebuff = boolPointer(x.HasDebuff)
		p.MagicImmune = boolPointer(x.MagicImmune)
		p.Smoked = boolPointer(x.Smoked)
		p.WardsPlaced = intPointer(x.WardsPlaced)
		p.WardsDestroyed = intPointer(x.WardsDestroyed)
		p.WardsPurchased = intPointer(x.WardsPurchased)
		p.Items = legacyItems(x.Items)
		p.Abilities = legacyAbilities(x.Abilities)
		t.Players[i] = p
	}
}
func legacyItems(in []contracts.ItemObservationV1) []analytics.ItemSlot {
	if in == nil {
		return nil
	}
	out := make([]analytics.ItemSlot, len(in))
	for i, x := range in {
		out[i] = analytics.ItemSlot{Slot: x.Slot, Name: stringValue(x.Name), ItemLevel: intPointer(x.ItemLevel), Cooldown: decimalPointer(x.Cooldown), MaxCooldown: decimalPointer(x.MaxCooldown), CanCast: boolPointer(x.CanCast), Charges: intPointer(x.Charges), Passive: boolPointer(x.Passive)}
	}
	return out
}
func legacyAbilities(in []contracts.AbilityObservationV1) []analytics.AbilitySlot {
	if in == nil {
		return nil
	}
	out := make([]analytics.AbilitySlot, len(in))
	for i, x := range in {
		out[i] = analytics.AbilitySlot{Slot: x.Slot, Name: stringValue(x.Name), Level: intPointer(x.Level), Cooldown: decimalPointer(x.Cooldown), MaxCooldown: decimalPointer(x.MaxCooldown), CanCast: boolPointer(x.CanCast), Passive: boolPointer(x.Passive), Ultimate: boolPointer(x.Ultimate)}
	}
	return out
}
func stringValue(v contracts.ObservedV1[string]) string {
	if v.State == contracts.ValuePresent && v.Value != nil {
		return *v.Value
	}
	return ""
}
func intValue(v contracts.ObservedV1[int64]) int64 {
	if v.State == contracts.ValuePresent && v.Value != nil {
		return *v.Value
	}
	return 0
}
func intPointer(v contracts.ObservedV1[int64]) *int64 {
	if v.State == contracts.ValuePresent {
		return v.Value
	}
	return nil
}
func boolPointer(v contracts.ObservedV1[bool]) *bool {
	if v.State == contracts.ValuePresent {
		return v.Value
	}
	return nil
}
func decimalPointer(v contracts.ObservedV1[contracts.Decimal]) *float64 {
	if v.State != contracts.ValuePresent || v.Value == nil {
		return nil
	}
	n, err := strconv.ParseFloat(string(*v.Value), 64)
	if err != nil {
		return nil
	}
	return &n
}
