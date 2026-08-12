package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

const FactsSchemaVersion = "replay.facts.spike.v1"

const heroPrefix = "npc_dota_hero_"

// Availability records which fact families the spike successfully extracted
// from the replay, which are feasible but deferred to M1, and which are
// unavailable inside the accepted safety boundary.
type Availability struct {
	Available   []string `json:"available"`
	Deferred    []string `json:"deferred"`
	Unavailable []string `json:"unavailable"`
}

// MatchMeta is the small deterministic header derived only from the demo bytes.
// MaxCombatLogTimestampSec is the largest combat-log event timestamp, which uses
// the replay's combat-log epoch and is NOT the authoritative match duration;
// the authoritative duration comes from Steam/OpenDota match metadata.
type MatchMeta struct {
	GameBuild                uint32  `json:"game_build"`
	ServerName               string  `json:"server_name"`
	LastTick                 uint32  `json:"last_tick"`
	LastNetTick              uint32  `json:"last_net_tick"`
	MaxCombatLogTimestampSec float64 `json:"max_combat_log_timestamp_sec"`
}

// HeroFacts is the per-hero aggregation derived from the combat log.
type HeroFacts struct {
	Name           string            `json:"name"`
	Deaths         uint64            `json:"deaths"`
	HeroKills      uint64            `json:"hero_kills"`
	ItemUses       map[string]uint64 `json:"item_uses"`
	Items          uint64            `json:"items"`
	PurchaseEvents uint64            `json:"purchase_events"`
	PurchaseGold   uint64            `json:"purchase_gold"`
	GoldEvents     uint64            `json:"gold_events"`
	XpEvents       uint64            `json:"xp_events"`
	Buybacks       uint64            `json:"buybacks"`
	BuildingKills  uint64            `json:"building_kills"`
}

// TimelineEvent is a sorted, deduplicated objective/milestone fact.
type TimelineEvent struct {
	Type      string  `json:"type"`
	Timestamp float64 `json:"timestamp"`
	Attacker  string  `json:"attacker"`
	Target    string  `json:"target"`
	Inflictor string  `json:"inflictor"`
	Value     int64   `json:"value"`
}

// ReplayFactsV1 is the spike-level normalized fact set produced from one
// decompressed Source 2 replay. It is derived purely from the demo bytes and
// contains no acquisition metadata, so a deterministic parse of identical
// bytes yields byte-identical JSON and an identical content hash.
//
// This is provisional M0 feasibility evidence. It is NOT the accepted
// HistoricalBaselineV1 contract, which is owned by a separate contract track.
type ReplayFactsV1 struct {
	SchemaVersion        string            `json:"schema_version"`
	Availability         Availability      `json:"availability"`
	Meta                 MatchMeta          `json:"meta"`
	MessageCounts        map[string]uint64 `json:"message_counts"`
	CombatLogTotal       uint64            `json:"combat_log_total"`
	CombatLogTypeCounts  map[string]uint64 `json:"combat_log_type_counts"`
	Heroes               []HeroFacts        `json:"heroes"`
	ItemUses             map[string]uint64 `json:"item_uses"`
	Timeline             []TimelineEvent   `json:"timeline"`
}

// CombatEvent is one combat-log entry collected by the parser adapter with
// name indices already resolved to strings. It is the input to BuildFacts.
type CombatEvent struct {
	Type         string
	Timestamp    float64
	Attacker     string
	Target       string
	Inflictor    string
	Value        int64
	GoldReason   uint32
	BuildingType uint32
}

// Collected is everything the adapter gathers in one parse pass. BuildFacts is
// a pure function over it, which keeps determinism testable without a replay.
type Collected struct {
	GameBuild     uint32
	ServerName    string
	LastTick      uint32
	LastNetTick   uint32
	MaxTimestamp  float64
	MessageCounts map[string]uint64
	Combat        []CombatEvent
}

func isHero(name string) bool { return strings.HasPrefix(name, heroPrefix) }

// BuildFacts aggregates a Collected pass into a deterministic ReplayFactsV1.
// All maps and slices are sorted before returning so serialization is stable.
func BuildFacts(c *Collected) *ReplayFactsV1 {
	f := &ReplayFactsV1{
		SchemaVersion: FactsSchemaVersion,
		Availability: Availability{
			Available: []string{
				"match_header",
				"game_build",
				"server_name",
				"duration_last_tick_and_combat_log_clock",
				"combat_log_type_histogram",
				"hero_deaths_and_hero_kills",
				"item_usage_by_name",
				"purchase_gold_ledger_per_hero",
				"building_kills_by_building_name",
				"first_blood_timestamp",
				"killstreak_multikill",
				"message_type_counts",
				"combat_log_name_resolution_partial",
			},
			Deferred: []string{
				"named_item_purchases",
				"per_hero_gold_xp_attribution",
				"combat_log_actor_resolution_for_first_blood_and_building_kills",
				"per_tick_positions",
				"ward_entity_coordinates",
				"exact_net_worth_timeline",
				"draft_picks_bans",
				"gold_xpm_checkpoints_series",
				"full_entity_state_reconstruction",
			},
			Unavailable: []string{
				"replay_salt_or_gc_credentials",
				"hidden_fog_of_war_state",
			},
		},
		Meta: MatchMeta{
			GameBuild:                c.GameBuild,
			ServerName:               c.ServerName,
			LastTick:                 c.LastTick,
			LastNetTick:              c.LastNetTick,
			MaxCombatLogTimestampSec: c.MaxTimestamp,
		},
		MessageCounts:       copySortedCounters(c.MessageCounts),
		CombatLogTotal:      uint64(len(c.Combat)),
		CombatLogTypeCounts: map[string]uint64{},
		Heroes:              nil,
		ItemUses:            map[string]uint64{},
		Timeline:            nil,
	}

	heroes := map[string]*HeroFacts{}
	itemUses := map[string]uint64{}
	typeCounts := map[string]uint64{}

	ensureHero := func(name string) *HeroFacts {
		if !isHero(name) {
			return nil
		}
		h, ok := heroes[name]
		if !ok {
			h = &HeroFacts{Name: name, ItemUses: map[string]uint64{}}
			heroes[name] = h
		}
		return h
	}

	for _, e := range c.Combat {
		typeCounts[e.Type]++
		switch e.Type {
		case "DOTA_COMBATLOG_DEATH":
			if t := ensureHero(e.Target); t != nil {
				t.Deaths++
			}
			if a := ensureHero(e.Attacker); a != nil && isHero(e.Target) {
				a.HeroKills++
			}
		case "DOTA_COMBATLOG_ITEM":
			if a := ensureHero(e.Attacker); a != nil {
				a.Items++
				if e.Inflictor != "" {
					a.ItemUses[e.Inflictor]++
				}
			}
			if e.Inflictor != "" {
				itemUses[e.Inflictor]++
			}
		case "DOTA_COMBATLOG_PURCHASE":
			if t := ensureHero(e.Target); t != nil {
				t.PurchaseEvents++
				if e.Value > 0 {
					t.PurchaseGold += uint64(e.Value)
				}
			}
		case "DOTA_COMBATLOG_GOLD":
			if a := ensureHero(e.Attacker); a != nil {
				a.GoldEvents++
			}
		case "DOTA_COMBATLOG_XP":
			if a := ensureHero(e.Attacker); a != nil {
				a.XpEvents++
			}
		case "DOTA_COMBATLOG_BUYBACK":
			if a := ensureHero(e.Attacker); a != nil {
				a.Buybacks++
			}
			f.Timeline = append(f.Timeline, TimelineEvent{
				Type: "buyback", Timestamp: e.Timestamp, Attacker: e.Attacker,
			})
		case "DOTA_COMBATLOG_TEAM_BUILDING_KILL":
			if a := ensureHero(e.Attacker); a != nil {
				a.BuildingKills++
			}
			f.Timeline = append(f.Timeline, TimelineEvent{
				Type: "building_kill", Timestamp: e.Timestamp,
				Attacker: e.Attacker, Target: e.Target, Inflictor: e.Inflictor,
				Value: int64(e.BuildingType),
			})
		case "DOTA_COMBATLOG_FIRST_BLOOD":
			f.Timeline = append(f.Timeline, TimelineEvent{
				Type: "first_blood", Timestamp: e.Timestamp,
				Attacker: e.Attacker, Target: e.Target, Inflictor: e.Inflictor,
			})
		case "DOTA_COMBATLOG_MULTIKILL":
			f.Timeline = append(f.Timeline, TimelineEvent{
				Type: "multi_kill", Timestamp: e.Timestamp,
				Attacker: e.Attacker, Value: e.Value,
			})
		case "DOTA_COMBATLOG_KILLSTREAK":
			f.Timeline = append(f.Timeline, TimelineEvent{
				Type: "killstreak", Timestamp: e.Timestamp,
				Attacker: e.Attacker, Value: e.Value,
			})
		}
	}

	f.CombatLogTypeCounts = copySortedCounters(typeCounts)
	f.ItemUses = itemUses

	names := make([]string, 0, len(heroes))
	for n := range heroes {
		names = append(names, n)
	}
	sort.Strings(names)
	f.Heroes = make([]HeroFacts, 0, len(names))
	for _, n := range names {
		h := heroes[n]
		h.ItemUses = copySortedCounters(h.ItemUses)
		f.Heroes = append(f.Heroes, *h)
	}

	sort.SliceStable(f.Timeline, func(i, j int) bool {
		a, b := f.Timeline[i], f.Timeline[j]
		if a.Timestamp != b.Timestamp {
			return a.Timestamp < b.Timestamp
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Attacker != b.Attacker {
			return a.Attacker < b.Attacker
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		if a.Inflictor != b.Inflictor {
			return a.Inflictor < b.Inflictor
		}
		return a.Value < b.Value
	})
	return f
}

// CanonicalJSON returns the deterministic JSON encoding of the facts.
// encoding/json sorts map keys; slices are pre-sorted by BuildFacts.
func (f *ReplayFactsV1) CanonicalJSON() ([]byte, error) {
	return json.Marshal(f)
}

// Hash returns the lowercase hex SHA-256 of the canonical JSON.
func (f *ReplayFactsV1) Hash() (string, error) {
	b, err := f.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func copySortedCounters(in map[string]uint64) map[string]uint64 {
	if in == nil {
		return map[string]uint64{}
	}
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}