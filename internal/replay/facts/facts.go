// Package facts normalizes the raw observation stream into typed, versioned
// fact families. Facts are emitted in a deterministic JSONL stream; a summary
// record reports per-family coverage, null rates, and explicit unavailable
// reasons. Unavailable fields are null plus a reason code, never numeric zero.
package facts

import (
	"bufio"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Family names for the normalized fact partitions.
const (
	FamilyMatchState      = "match_state"
	FamilyParticipant     = "participant_binding"
	FamilyHeroState       = "hero_state_sample"
	FamilyEconomy         = "economy_sample"
	FamilyCombat          = "combat_event"
	FamilyObjective       = "objective_event"
	FamilyDeathRespawn    = "death_respawn_buyback_event"
	FamilyItem            = "item_event"
	FamilyAbility         = "ability_event"
	FamilyModifier        = "modifier_event"
	FamilyVision          = "vision_event"
	FamilyEntityLifecycle = "entity_lifecycle_event"
)

// Combat event kinds.
const (
	CombatDeath    = "death"
	CombatDamage   = "damage"
	CombatHeal     = "heal"
	CombatAbility  = "ability"
	CombatItem     = "item"
	CombatModifier = "modifier"
)

// Fact is one normalized fact line.
type Fact struct {
	Seq           int64           `json:"seq"`
	Family        string          `json:"family"`
	MatchID       string          `json:"match_id"`
	GameSecond    float64         `json:"game_second"`
	GameSecondOK  bool            `json:"game_second_ok"`
	Tick          uint32          `json:"tick"`
	SourceSeq     int64           `json:"source_seq"`
	SchemaVersion string          `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

// MatchState is the single match-level fact.
type MatchState struct {
	MatchID       string   `json:"match_id"`
	GameMode      int32    `json:"game_mode"`
	GameWinner    int32    `json:"game_winner"`
	LeagueID      uint32   `json:"league_id"`
	GameBuild     uint32   `json:"game_build"`
	DurationSec   *float64 `json:"duration_seconds"`
	WinnerSide    string   `json:"winner_side"`
	SourceKind    string   `json:"source_kind"`
	SchemaVersion string   `json:"schema_version"`
}

// ParticipantFact is one participant binding fact.
type ParticipantFact struct {
	Slot          int32  `json:"slot"`
	AccountID     string `json:"account_id"`
	SteamID64     uint64 `json:"steam_id64"`
	PlayerName    string `json:"player_name"`
	HeroName      string `json:"hero_name"`
	HeroID        int32  `json:"hero_id"`
	Side          string `json:"side"`
	Team          int32  `json:"team"`
	BindingSource string `json:"binding_source"`
	SchemaVersion string `json:"schema_version"`
}

// HeroStateSample is a resampled per-player position/economy snapshot.
type HeroStateSample struct {
	AccountID        string   `json:"account_id"`
	HeroName         string   `json:"hero_name"`
	Side             string   `json:"side"`
	PosX             *float64 `json:"pos_x"`
	PosY             *float64 `json:"pos_y"`
	PosZ             *float64 `json:"pos_z"`
	Health           *int64   `json:"health"`
	MaxHealth        *int64   `json:"max_health"`
	Level            *int32   `json:"level"`
	XP               *int64   `json:"xp"`
	Alive            *bool    `json:"alive"`
	RespawnRemaining *float64 `json:"respawn_remaining"`
	Missing          []string `json:"missing"`
}

// EconomySample is a gold/xp/net-worth/last-hits sample.
type EconomySample struct {
	AccountID string   `json:"account_id"`
	HeroName  string   `json:"hero_name"`
	Networth  *uint32  `json:"networth"`
	LastHits  *uint32  `json:"last_hits"`
	Gold      *int64   `json:"gold"`
	Xp        *int64   `json:"xp"`
	Reason    *uint32  `json:"reason"`
	Missing   []string `json:"missing"`
}

// CombatFact is a normalized combat event.
type CombatFact struct {
	Kind          string   `json:"kind"`
	ActorAccount  string   `json:"actor_account,omitempty"`
	ActorName     string   `json:"actor_name,omitempty"`
	TargetAccount string   `json:"target_account,omitempty"`
	TargetName    string   `json:"target_name,omitempty"`
	Inflictor     string   `json:"inflictor,omitempty"`
	Value         *int64   `json:"value"`
	LocationX     *float64 `json:"location_x"`
	LocationY     *float64 `json:"location_y"`
	AttackerTeam  *uint32  `json:"attacker_team"`
	TargetTeam    *uint32  `json:"target_team"`
	DamageType    *uint32  `json:"damage_type"`
	IsHealSave    *bool    `json:"is_heal_save"`
	IsUltimate    *bool    `json:"is_ultimate"`
	AssistPlayers []string `json:"assist_players"`
	Missing       []string `json:"missing"`
}

// ObjectiveFact is a building/objective transition.
type ObjectiveFact struct {
	BuildingName   string   `json:"building_name"`
	BuildingKind   string   `json:"building_kind"`
	BuildingTier   *int     `json:"building_tier"`
	BuildingLane   string   `json:"building_lane"`
	Team           string   `json:"team"`
	ActorAccount   string   `json:"actor_account,omitempty"`
	ActorName      string   `json:"actor_name,omitempty"`
	Attackers      []string `json:"attackers"`
	IsRealBuilding bool     `json:"is_real_building"`
	Exclusion      string   `json:"exclusion,omitempty"`
	Missing        []string `json:"missing"`
}

// DeathRespawnBuyback is one death, respawn, or buyback round event.
type DeathRespawnBuyback struct {
	Kind           string   `json:"kind"` // death|respawn|buyback
	AccountID      string   `json:"account_id"`
	HeroName       string   `json:"hero_name"`
	Side           string   `json:"side"`
	KillerAccount  string   `json:"killer_account,omitempty"`
	KillerName     string   `json:"killer_name,omitempty"`
	AssistAccounts []string `json:"assist_accounts"`
	RespawnTime    *float64 `json:"respawn_time"`
	BuybackCost    *int64   `json:"buyback_cost"`
	DeathValue     *int64   `json:"death_value"`
	Missing        []string `json:"missing"`
}

// ItemFact is an item purchase or use.
type ItemFact struct {
	Kind      string   `json:"kind"` // purchase|use
	AccountID string   `json:"account_id"`
	HeroName  string   `json:"hero_name"`
	ItemName  string   `json:"item_name"`
	Cost      *int64   `json:"cost"`
	Slot      *uint32  `json:"slot"`
	Missing   []string `json:"missing"`
}

// AbilityFact is an ability cast.
type AbilityFact struct {
	AccountID     string   `json:"account_id"`
	HeroName      string   `json:"hero_name"`
	AbilityName   string   `json:"ability_name"`
	Level         *uint32  `json:"level"`
	TargetAccount string   `json:"target_account,omitempty"`
	TargetName    string   `json:"target_name,omitempty"`
	Missing       []string `json:"missing"`
}

// ModifierFact is a modifier add/remove.
type ModifierFact struct {
	Kind          string   `json:"kind"` // add|remove
	AccountID     string   `json:"account_id"`
	HeroName      string   `json:"hero_name"`
	Modifier      string   `json:"modifier"`
	Duration      *float64 `json:"duration"`
	SourceAccount string   `json:"source_account,omitempty"`
	SourceName    string   `json:"source_name,omitempty"`
	Missing       []string `json:"missing"`
}

// VisionFact is a ward placement/destruction.
type VisionFact struct {
	Kind      string   `json:"kind"` // obs_ward_placed|sentry_ward_placed|ward_destroyed
	AccountID string   `json:"account_id"`
	HeroName  string   `json:"hero_name"`
	Team      string   `json:"team"`
	LocationX *float64 `json:"location_x"`
	LocationY *float64 `json:"location_y"`
}

// EntityLifecycle is a hero spawn/death lifecycle event.
type EntityLifecycle struct {
	Kind      string `json:"kind"` // spawn|death
	AccountID string `json:"account_id"`
	HeroName  string `json:"hero_name"`
	Side      string `json:"side"`
}

// HeroBinding maps a hero entity class to a participant.
type HeroBinding struct {
	AccountID string `json:"account_id"`
	HeroName  string `json:"hero_name"`
	Side      string `json:"side"`
	Class     string `json:"class"`
}

// Coverage reports availability of a fact family.
type Coverage struct {
	Family            string `json:"family"`
	Count             int64  `json:"count"`
	Eligible          int64  `json:"eligible_count"`
	Missing           int64  `json:"missing_count"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	Available         bool   `json:"available"`
}

// Summary is the aggregated facts record.
type Summary struct {
	SchemaVersion   string           `json:"schema_version"`
	MatchID         string           `json:"match_id"`
	Families        []Coverage       `json:"families"`
	HeroBindings    []HeroBinding    `json:"hero_bindings"`
	GameSecondMin   float64          `json:"game_second_min"`
	GameSecondMax   float64          `json:"game_second_max"`
	EligibleSeconds int64            `json:"eligible_seconds"`
	PositionGaps    map[string]int64 `json:"position_gap_seconds"`
	RawEvents       int64            `json:"raw_events"`
	CombatEvents    int64            `json:"combat_events"`
}

// Builder consumes a raw stream and emits normalized facts.
type Builder struct {
	clk               *clock.Clock
	idn               *identity.Identity
	heroNameToAccount map[string]string
	accountToName     map[string]string
	accountToSide     map[string]string
	accountToSlot     map[string]int32
	heroClassToName   map[string]string
}

// NewBuilder creates a fact builder bound to a calibrated clock and verified
// identity.
func NewBuilder(clk *clock.Clock, idn *identity.Identity) *Builder {
	b := &Builder{
		clk:               clk,
		idn:               idn,
		heroNameToAccount: map[string]string{},
		accountToName:     map[string]string{},
		accountToSide:     map[string]string{},
		accountToSlot:     map[string]int32{},
		heroClassToName:   map[string]string{},
	}
	for _, p := range idn.Participants {
		normalized := strings.TrimPrefix(p.HeroName, "npc_dota_hero_")
		b.heroNameToAccount[p.HeroName] = p.AccountID
		b.accountToName[p.AccountID] = p.PlayerName
		b.accountToSide[p.AccountID] = p.Side
		b.accountToSlot[p.AccountID] = p.Slot
		cls := "CDOTA_Unit_Hero_" + pascal(normalized)
		b.heroClassToName[cls] = p.HeroName
	}
	return b
}

func pascal(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// AccountForHero resolves an npc hero name to an account id.
func (b *Builder) AccountForHero(heroName string) (string, bool) {
	a, ok := b.heroNameToAccount[heroName]
	return a, ok
}

// AccountForClass resolves a hero entity class to an account id.
func (b *Builder) AccountForClass(class string) (string, bool) {
	name, ok := b.heroClassToName[class]
	if !ok {
		return "", false
	}
	return b.AccountForHero(name)
}

func (b *Builder) heroAccount(account string) string { return account }

// PlayerRef resolves a combat-log actor/target name to an account id.
func (b *Builder) playerRef(name string) (account string, isHero bool) {
	if a, ok := b.heroNameToAccount[name]; ok {
		return a, true
	}
	return "", false
}

// Build processes a raw stream and writes facts to an Emitter.
func (b *Builder) Build(r *raw.Reader, emit func(*Fact) error) (*Summary, error) {
	sum := &Summary{
		SchemaVersion: version.FactsSchema,
		MatchID:       b.idn.MatchID,
		Families:      []Coverage{},
		HeroBindings:  []HeroBinding{},
		PositionGaps:  map[string]int64{},
	}

	var seq int64
	var matchFact *MatchState
	lastEmit := map[string]float64{}
	lastPos := map[string]struct{ x, y float64 }{}
	lastLevel := map[string]int32{}
	lastHealth := map[string]int64{}
	lastRespawn := map[string]float64{}
	lastAlive := map[string]bool{}
	heroSeen := map[string]bool{}
	lastSeenSecond := map[string]float64{}

	counts := map[string]*Coverage{}
	initCount := func(f string) *Coverage {
		c := counts[f]
		if c == nil {
			c = &Coverage{Family: f}
			counts[f] = c
		}
		return c
	}

	// match_state fact
	ms := &MatchState{
		MatchID:       b.idn.MatchID,
		GameMode:      b.idn.GameMode,
		GameWinner:    b.idn.GameWinner,
		LeagueID:      b.idn.LeagueID,
		GameBuild:     b.idn.GameBuild,
		DurationSec:   b.clk.GameDurationSeconds,
		WinnerSide:    sideOfWinner(b.idn.GameWinner),
		SourceKind:    "replay_file_info",
		SchemaVersion: version.FactsSchema,
	}
	if payload, err := marshal(ms); err == nil {
		emitFact(&seq, FamilyMatchState, b.idn.MatchID, 0, 0, 0, payload, emit)
		initCount(FamilyMatchState).Count = 1
	}
	matchFact = ms

	// participant facts
	for _, p := range b.idn.Participants {
		pf := &ParticipantFact{
			Slot:          p.Slot,
			AccountID:     p.AccountID,
			SteamID64:     p.SteamID64,
			PlayerName:    p.PlayerName,
			HeroName:      p.HeroName,
			HeroID:        p.HeroID,
			Side:          p.Side,
			Team:          p.Team,
			BindingSource: "demo_file_info+manifest_crosscheck",
			SchemaVersion: version.FactsSchema,
		}
		if payload, err := marshal(pf); err == nil {
			emitFact(&seq, FamilyParticipant, b.idn.MatchID, 0, 0, 0, payload, emit)
			initCount(FamilyParticipant).Count++
		}
		sum.HeroBindings = append(sum.HeroBindings, HeroBinding{
			AccountID: p.AccountID, HeroName: p.HeroName, Side: p.Side,
			Class: classForHero(p.HeroName),
		})
	}

	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		sum.RawEvents++
		switch e.Kind {
		case raw.KindCombat:
			sum.CombatEvents++
			gs := b.clk.GameSecondOfCombatTS(e.Combat.TS)
			if err := b.emitCombat(e.Combat, gs, emit, &seq, counts); err != nil {
				return nil, err
			}
		case raw.KindHeroState:
			gs := b.clk.GameSecondOfTick(e.HeroState.Tick)
			sum.GameSecondMax = maxF(sum.GameSecondMax, gs)
			sum.GameSecondMin = minF(sum.GameSecondMin, gs)
			account, ok := b.AccountForClass(e.HeroState.Class)
			if !ok {
				initCount(FamilyHeroState).Missing++
				continue
			}
			heroSeen[account] = true
			bin := mathRoundHalf(gs)
			if bin < 0 {
				lastSeenSecond[account] = bin
				continue
			}
			if last, ok := lastEmit[account]; ok && bin-last < 0.5 {
				// Already emitted for this half-second bin.
				break
			}
			lastEmit[account] = bin
			h := &HeroStateSample{
				AccountID: account,
				HeroName:  heroNameFor(account, b),
				Side:      b.accountToSide[account],
				PosX:      e.HeroState.PosX,
				PosY:      e.HeroState.PosY,
				PosZ:      e.HeroState.PosZ,
				Health:    e.HeroState.Health,
				MaxHealth: e.HeroState.MaxHealth,
				Level:     e.HeroState.Level,
				XP:        e.HeroState.XP,
				Alive:     e.HeroState.Alive,
			}
			if e.HeroState.RespawnTime != nil {
				rem := *e.HeroState.RespawnTime
				h.RespawnRemaining = &rem
			}
			if h.PosX == nil || h.PosY == nil {
				h.Missing = append(h.Missing, "position")
			}
			if h.Health == nil {
				h.Missing = append(h.Missing, "health")
			}
			if payload, err := marshal(h); err == nil {
				emitFact(&seq, FamilyHeroState, b.idn.MatchID, gs, e.HeroState.Tick, 0, payload, emit)
				initCount(FamilyHeroState).Count++
			}
			// Track position gaps: seconds between consecutive emitted samples.
			if prev, ok := lastSeenSecond[account]; ok && bin > prev {
				gap := int64(bin - prev - 0.5)
				if gap > 0 {
					sum.PositionGaps[account] += gap
				}
			}
			lastSeenSecond[account] = bin
			_ = lastPos
			_ = lastLevel
			_ = lastHealth
			_ = lastRespawn
			_ = lastAlive
		case raw.KindParseDone:
			// no-op
		}
	}

	// Finalize counts and availability.
	names := make([]string, 0, len(counts))
	for f := range counts {
		names = append(names, f)
	}
	sort.Strings(names)
	var eligible int64
	if b.clk.GameDurationSeconds != nil {
		eligible = int64(*b.clk.GameDurationSeconds) + 1
	}
	sum.EligibleSeconds = eligible
	for _, f := range names {
		c := counts[f]
		if f == FamilyHeroState && c.Count > 0 {
			c.Eligible = eligible * int64(len(b.heroNameToAccount))
		}
		if f == FamilyHeroState && c.Count == 0 {
			c.UnavailableReason = "no_hero_snapshots_in_eligible_seconds"
		}
		if f == FamilyCombat && c.Count == 0 {
			c.UnavailableReason = "no_combat_events"
		}
		c.Available = c.Count > 0
		sum.Families = append(sum.Families, *c)
	}

	// Position gaps summary.
	if len(sum.PositionGaps) > 0 {
		keys := make([]string, 0, len(sum.PositionGaps))
		for k := range sum.PositionGaps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		top := make(map[string]int64, 10)
		for _, k := range keys {
			if len(top) >= 10 {
				break
			}
			top[k] = sum.PositionGaps[k]
		}
		sum.PositionGaps = top
	}
	_ = matchFact
	return sum, nil
}

func (b *Builder) emitCombat(c *raw.Combat, gs float64, emit func(*Fact) error, seq *int64, counts map[string]*Coverage) error {
	initCount := func(f string) *Coverage {
		cov := counts[f]
		if cov == nil {
			cov = &Coverage{Family: f}
			counts[f] = cov
		}
		return cov
	}
	var f *Fact
	switch c.Type {
	case "DOTA_COMBATLOG_DEATH":
		f = b.factDeath(c, gs)
	case "DOTA_COMBATLOG_BUYBACK":
		f = b.factBuyback(c, gs)
	case "DOTA_COMBATLOG_GOLD", "DOTA_COMBATLOG_XP":
		f = b.factEconomy(c, gs)
	case "DOTA_COMBATLOG_PURCHASE":
		f = b.factItemPurchase(c, gs)
	case "DOTA_COMBATLOG_ITEM":
		f = b.factItemUse(c, gs)
	case "DOTA_COMBATLOG_ABILITY":
		f = b.factAbility(c, gs)
	case "DOTA_COMBATLOG_MODIFIER_ADD", "DOTA_COMBATLOG_MODIFIER_REMOVE", "DOTA_COMBATLOG_MODIFIER_STACK_EVENT":
		f = b.factModifier(c, gs)
	case "DOTA_COMBATLOG_TEAM_BUILDING_KILL":
		f = b.factObjective(c, gs)
	case "DOTA_COMBATLOG_DAMAGE", "DOTA_COMBATLOG_HEAL":
		f = b.factCombat(c, gs)
	case "DOTA_COMBATLOG_FIRST_BLOOD", "DOTA_COMBATLOG_MULTIKILL", "DOTA_COMBATLOG_KILLSTREAK", "DOTA_COMBATLOG_PLAYERSTATS", "DOTA_COMBATLOG_CRITICAL_DAMAGE", "DOTA_COMBATLOG_GAME_STATE":
		return nil
	default:
		return nil
	}
	if f == nil {
		return nil
	}
	fact := &Fact{
		Seq:           *seq + 1,
		Family:        f.Family,
		MatchID:       b.idn.MatchID,
		GameSecond:    gs,
		GameSecondOK:  gs >= 0,
		Tick:          c.Tick,
		SourceSeq:     c.Seq,
		SchemaVersion: version.FactsSchema,
		Payload:       f.Payload,
	}
	*seq = fact.Seq
	initCount(f.Family).Count++
	return emit(fact)
}

func marshal(v interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func emitFact(seq *int64, family, matchID string, gs float64, tick uint32, sourceSeq int64, payload json.RawMessage, emit func(*Fact) error) {
	*seq++
	emit(&Fact{Seq: *seq, Family: family, MatchID: matchID, GameSecond: gs, GameSecondOK: gs >= 0, Tick: tick, SourceSeq: sourceSeq, SchemaVersion: version.FactsSchema, Payload: payload})
}

// newCombatFact returns a combat fact with the actor/target bound to players
// when possible; unbound actors stay empty with a missing reason.
func (b *Builder) newCombatFact(c *raw.Combat) *CombatFact {
	f := &CombatFact{}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			f.ActorAccount = a
		}
		f.ActorName = c.AttackerName
	}
	if c.TargetName != "" {
		if a, ok := b.playerRef(c.TargetName); ok {
			f.TargetAccount = a
		}
		f.TargetName = c.TargetName
	}
	f.Inflictor = c.InflictorName
	f.Value = c.Value
	f.LocationX = c.LocationX
	f.LocationY = c.LocationY
	f.AttackerTeam = c.AttackerTeam
	f.TargetTeam = c.TargetTeam
	f.DamageType = c.DamageType
	f.IsHealSave = c.IsHealSave
	f.IsUltimate = c.IsUltimate
	if len(c.AssistPlayers) > 0 {
		f.AssistPlayers = b.accountIDsFromSlots(c.AssistPlayers)
	}
	if f.ActorAccount == "" && c.IsAttackerHero != nil && *c.IsAttackerHero {
		f.Missing = append(f.Missing, "attacker_account_unresolved")
	}
	if f.TargetAccount == "" && c.IsTargetHero != nil && *c.IsTargetHero {
		f.Missing = append(f.Missing, "target_account_unresolved")
	}
	return f
}

func (b *Builder) accountIDsFromSlots(slots []int32) []string {
	out := []string{}
	for _, s := range slots {
		for _, p := range b.idn.Participants {
			if p.Slot == s {
				out = append(out, p.AccountID)
				break
			}
		}
	}
	return out
}

func (b *Builder) factCombat(c *raw.Combat, gs float64) *Fact {
	kind := CombatDamage
	if c.Type == "DOTA_COMBATLOG_HEAL" {
		kind = CombatHeal
	}
	f := b.newCombatFact(c)
	f.Kind = kind
	payload, _ := marshal(f)
	return &Fact{Family: FamilyCombat, Payload: payload}
}

func (b *Builder) factDeath(c *raw.Combat, gs float64) *Fact {
	drb := &DeathRespawnBuyback{Kind: "death"}
	if c.TargetName != "" {
		if a, ok := b.playerRef(c.TargetName); ok {
			drb.AccountID = a
			drb.HeroName = c.TargetName
			drb.Side = b.accountToSide[a]
		} else {
			drb.HeroName = c.TargetName
			drb.Missing = append(drb.Missing, "target_account_unresolved")
		}
	} else {
		drb.Missing = append(drb.Missing, "target_name_missing")
	}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			drb.KillerAccount = a
		}
		drb.KillerName = c.AttackerName
	}
	if len(c.AssistPlayers) > 0 {
		drb.AssistAccounts = b.accountIDsFromSlots(c.AssistPlayers)
	}
	drb.DeathValue = c.Value
	if c.IsTargetHero != nil && !*c.IsTargetHero {
		drb.Missing = append(drb.Missing, "non_hero_death")
	}
	payload, _ := marshal(drb)
	return &Fact{Family: FamilyDeathRespawn, Payload: payload}
}

func (b *Builder) factBuyback(c *raw.Combat, gs float64) *Fact {
	drb := &DeathRespawnBuyback{Kind: "buyback"}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			drb.AccountID = a
			drb.HeroName = c.AttackerName
			drb.Side = b.accountToSide[a]
		} else {
			drb.HeroName = c.AttackerName
			drb.Missing = append(drb.Missing, "attacker_account_unresolved")
		}
	}
	drb.BuybackCost = c.Value
	payload, _ := marshal(drb)
	return &Fact{Family: FamilyDeathRespawn, Payload: payload}
}

func (b *Builder) factEconomy(c *raw.Combat, gs float64) *Fact {
	es := &EconomySample{}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			es.AccountID = a
			es.HeroName = c.AttackerName
		} else {
			es.HeroName = c.AttackerName
			es.Missing = append(es.Missing, "attacker_account_unresolved")
		}
	}
	es.Networth = c.Networth
	es.LastHits = c.LastHits
	es.Xp = c.Value
	if c.Type == "DOTA_COMBATLOG_GOLD" {
		es.Gold = c.Value
		es.Xp = nil
		es.Reason = c.GoldReason
	}
	payload, _ := marshal(es)
	return &Fact{Family: FamilyEconomy, Payload: payload}
}

func (b *Builder) factItemPurchase(c *raw.Combat, gs float64) *Fact {
	f := &ItemFact{Kind: "purchase", ItemName: c.InflictorName, Cost: c.Value}
	if c.TargetName != "" {
		if a, ok := b.playerRef(c.TargetName); ok {
			f.AccountID = a
			f.HeroName = c.TargetName
		} else {
			f.HeroName = c.TargetName
			f.Missing = append(f.Missing, "target_account_unresolved")
		}
	}
	payload, _ := marshal(f)
	return &Fact{Family: FamilyItem, Payload: payload}
}

func (b *Builder) factItemUse(c *raw.Combat, gs float64) *Fact {
	f := &ItemFact{Kind: "use", ItemName: c.InflictorName}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			f.AccountID = a
			f.HeroName = c.AttackerName
		} else {
			f.HeroName = c.AttackerName
			f.Missing = append(f.Missing, "attacker_account_unresolved")
		}
	}
	payload, _ := marshal(f)
	return &Fact{Family: FamilyItem, Payload: payload}
}

func (b *Builder) factAbility(c *raw.Combat, gs float64) *Fact {
	f := &AbilityFact{AbilityName: c.InflictorName, Level: c.AbilityLevel}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			f.AccountID = a
			f.HeroName = c.AttackerName
		} else {
			f.HeroName = c.AttackerName
			f.Missing = append(f.Missing, "attacker_account_unresolved")
		}
	}
	if c.TargetName != "" {
		if a, ok := b.playerRef(c.TargetName); ok {
			f.TargetAccount = a
		}
		f.TargetName = c.TargetName
	}
	payload, _ := marshal(f)
	return &Fact{Family: FamilyAbility, Payload: payload}
}

func (b *Builder) factModifier(c *raw.Combat, gs float64) *Fact {
	kind := "add"
	if c.Type == "DOTA_COMBATLOG_MODIFIER_REMOVE" {
		kind = "remove"
	}
	f := &ModifierFact{Kind: kind, Modifier: c.InflictorName, Duration: c.ModifierDuration}
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			f.SourceAccount = a
		}
		f.SourceName = c.AttackerName
	}
	if c.TargetName != "" {
		if a, ok := b.playerRef(c.TargetName); ok {
			f.AccountID = a
			f.HeroName = c.TargetName
		} else {
			f.HeroName = c.TargetName
			f.Missing = append(f.Missing, "target_account_unresolved")
		}
	}
	payload, _ := marshal(f)
	return &Fact{Family: FamilyModifier, Payload: payload}
}

func (b *Builder) factObjective(c *raw.Combat, gs float64) *Fact {
	of := &ObjectiveFact{BuildingName: c.TargetName}
	of.BuildingKind, of.BuildingTier, of.BuildingLane, of.Team = classifyBuilding(c.TargetName, c.TargetTeam, c.AttackerTeam)
	if c.AttackerName != "" {
		if a, ok := b.playerRef(c.AttackerName); ok {
			of.ActorAccount = a
		}
		of.ActorName = c.AttackerName
	}
	if len(c.AssistPlayers) > 0 {
		of.Attackers = b.accountIDsFromSlots(c.AssistPlayers)
	}
	if c.BuildingType != nil {
		switch *c.BuildingType {
		case 0:
			of.Exclusion = "building_type_0_invalid_classified_by_name"
		}
	}
	of.IsRealBuilding = of.BuildingKind != "unknown" && of.BuildingKind != "other"
	if !of.IsRealBuilding {
		of.Exclusion = "not_a_real_structure_" + of.BuildingKind
	}
	payload, _ := marshal(of)
	return &Fact{Family: FamilyObjective, Payload: payload}
}

func sideOfWinner(winner int32) string {
	switch winner {
	case 2:
		return "radiant"
	case 3:
		return "dire"
	case 0:
		return "none"
	default:
		return "unknown"
	}
}

func heroNameFor(account string, b *Builder) string {
	for name, acc := range b.heroNameToAccount {
		if acc == account {
			return name
		}
	}
	return ""
}

func classForHero(heroName string) string {
	normalized := strings.TrimPrefix(heroName, "npc_dota_hero_")
	return "CDOTA_Unit_Hero_" + pascal(normalized)
}

func mathRoundHalf(f float64) float64 {
	return float64(int64(f*2)) / 2.0
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// classifyBuilding parses a building name into kind/tier/lane/team.
func classifyBuilding(name string, targetTeam, attackerTeam *uint32) (kind string, tier *int, lane, team string) {
	kind = "unknown"
	lane = "unknown"
	if name == "" {
		return
	}
	lower := strings.ToLower(name)
	// Team: goodguys are radiant, badguys are dire.
	if strings.Contains(lower, "goodguys") {
		team = "radiant"
	} else if strings.Contains(lower, "badguys") {
		team = "dire"
	} else if targetTeam != nil {
		switch *targetTeam {
		case 2:
			team = "radiant"
		case 3:
			team = "dire"
		}
	}
	switch {
	case strings.Contains(lower, "tower"):
		kind = "tower"
	case strings.Contains(lower, "rax"):
		kind = "barracks"
	case strings.Contains(lower, "fort") || strings.Contains(lower, "ancient"):
		kind = "ancient"
	case strings.Contains(lower, "shrine"):
		kind = "shrine"
	case strings.Contains(lower, "filler"):
		kind = "filler"
	case strings.Contains(lower, "underlord_portal") || strings.Contains(lower, "portal"):
		kind = "portal"
	default:
		kind = "other"
	}
	// tier: tower1/2/3/4, rax_melee/rax_ranged
	for t := 1; t <= 4; t++ {
		if strings.Contains(lower, "tower"+strconv.Itoa(t)) {
			tt := t
			tier = &tt
			break
		}
	}
	if strings.Contains(lower, "melee") {
		if strings.Contains(lower, "ranged") {
			lane = "unknown"
		} else {
			kind = "barracks"
		}
	}
	switch {
	case strings.Contains(lower, "_top"):
		lane = "top"
	case strings.Contains(lower, "_mid"):
		lane = "mid"
	case strings.Contains(lower, "_bot") || strings.Contains(lower, "_bottom"):
		lane = "bottom"
	}
	return
}

// Reader streams facts from a deterministic JSONL source.
type Reader struct {
	sc *bufio.Scanner
}

// NewReader reads facts from an io.Reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{sc: bufio.NewScanner(r)}
}

// Next decodes the next fact. Returns io.EOF at end of stream.
func (r *Reader) Next() (*Fact, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var f Fact
	if err := json.Unmarshal(r.sc.Bytes(), &f); err != nil {
		return nil, err
	}
	return &f, nil
}
