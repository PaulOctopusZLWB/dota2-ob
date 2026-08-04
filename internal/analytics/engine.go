package analytics

import (
	"sync"
	"time"
)

// Confidence labels how directly an event was observed.
const (
	ConfidenceObserved = "observed" // value changed in an observed field
	ConfidenceInferred = "inferred" // derived from observed field absence/presence
)

// Baseline event type identifiers.
const (
	EventHeroDeath            = "hero_death"
	EventHeroRespawn          = "hero_respawn"
	EventKillCounter          = "kill_counter"
	EventDeathCounter         = "death_counter"
	EventAssistCounter        = "assist_counter"
	EventGoldChanged          = "gold_changed"
	EventNetWorthChanged      = "net_worth_changed"
	EventItemAcquired         = "item_acquired"
	EventItemRemoved          = "item_removed"
	EventItemReplaced         = "item_replaced"
	EventItemSlotChanged      = "item_slot_changed"
	EventAbilityLevelChanged  = "ability_level_changed"
	EventAbilityCooldownState = "ability_cooldown_state"
	EventBuildingHealthLoss   = "building_health_loss"
	EventBuildingDestroyed    = "building_destroyed"
	EventRoshanStateChanged    = "roshan_state_changed"
	EventTormentorStateChanged = "tormentor_state_changed"
	EventWardCounterChanged   = "ward_counter_changed"
	EventWardPurchaseCooldown = "ward_purchase_cooldown_changed"
	EventWardPurchaseStarted   = "ward_purchase_started"
)

// Thresholds for "meaningful" economy events. Per-tick passive income is well
// below these, so steady-state growth does not flood the event log; purchases,
// deaths, and objective gold do.
const (
	meaningfulGoldDelta     = 100.0
	meaningfulNetWorthDelta = 150.0
	buildingHealthMinDelta  = 1.0

	// gameInProgress is the GSI game_state value for an actively running match.
	// Building-from-absence destruction is only inferred while the match was in
	// progress, so post-game cleanup of intact structures is not reported as
	// destruction.
	gameInProgress = "DOTA_GAMERULES_STATE_GAME_IN_PROGRESS"
)

// maxEvents caps the in-memory event ring to keep live/API responses bounded.
const maxEvents = 10000

// recentEventsAPI bounds the /api/events and summary recent-feed surface.
const recentEventsAPI = 200

// Event is one delta-derived observation. It only ever references fields that
// were observed in two consecutive ticks.
type Event struct {
	ReceivedAt   time.Time `json:"received_at"`
	Type         string    `json:"type"`
	Team         string    `json:"team,omitempty"`
	TeamKey      string    `json:"team_key,omitempty"`
	Player       string    `json:"player,omitempty"`
	Field        string    `json:"field,omitempty"`
	Before       any       `json:"before,omitempty"`
	After        any       `json:"after,omitempty"`
	SourcePaths  []string  `json:"source_paths,omitempty"`
	Confidence   string    `json:"confidence"`
	MatchID      string    `json:"match_id,omitempty"`
	MapClockTime *int64    `json:"map_clock_time,omitempty"`
}

// Engine is a stateful analytics component that derives events from observed
// deltas between consecutive normalized ticks. It never fabricates state that
// was not present in the raw payload.
type Engine struct {
	mu                       sync.Mutex
	prev                     *NormalizedTick
	// latestPlayerTick is the most recent tick that observed at least one
	// player/hero. It backs the per-player economy summary, so a trailing
	// post-game tick with an empty player block does not wipe the summary.
	latestPlayerTick         *NormalizedTick
	events                   []Event
	tickCount                uint64
	completeTenPlayerFrames  uint64
	startedAt                time.Time
	lastSeenAt               time.Time
	eventCounts              map[string]int
	roshanObservations       []Event
	buildingDestroyedEvents  []Event
	wardObservations         []Event
}

// NewEngine returns a fresh analytics engine.
func NewEngine() *Engine {
	return &Engine{eventCounts: make(map[string]int)}
}

// Observe feeds one accepted, persisted tick into the engine and returns the
// events derived from its delta against the previous tick.
func (e *Engine) Observe(tick NormalizedTick) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.tickCount++
	if e.startedAt.IsZero() {
		e.startedAt = tick.ReceivedAt
	}
	e.lastSeenAt = tick.ReceivedAt

	if isCompleteTenPlayerFrame(tick) {
		e.completeTenPlayerFrames++
	}

	var derived []Event
	if e.prev != nil {
		derived = e.derive(tick, *e.prev)
		for _, ev := range derived {
			e.recordEvent(ev)
		}
	}

	// Advance state. Keep a copy so callers cannot mutate retained state.
	next := tick
	e.prev = &next
	// Retain the most recent tick that observed players so the economy
	// summary survives trailing post-game ticks with an empty player block.
	if len(tick.Players) > 0 {
		playerCopy := tick
		e.latestPlayerTick = &playerCopy
	}
	return derived
}

// recordEvent appends to the bounded ring and the aggregate counters.
func (e *Engine) recordEvent(ev Event) {
	if e.events == nil {
		e.events = make([]Event, 0, 256)
	}
	e.events = append(e.events, ev)
	if len(e.events) > maxEvents {
		// Drop the oldest quarter to avoid frequent copies.
		drop := len(e.events) - maxEvents + maxEvents/4
		e.events = append([]Event(nil), e.events[drop:]...)
	}
	e.eventCounts[ev.Type]++
	switch ev.Type {
	case EventRoshanStateChanged:
		e.roshanObservations = append(e.roshanObservations, ev)
	case EventBuildingDestroyed:
		e.buildingDestroyedEvents = append(e.buildingDestroyedEvents, ev)
	case EventWardCounterChanged, EventWardPurchaseCooldown, EventWardPurchaseStarted:
		e.wardObservations = append(e.wardObservations, ev)
	}
}

// TickCount returns the number of ticks observed.
func (e *Engine) TickCount() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tickCount
}

// Events returns a bounded slice of the most recent events.
func (e *Engine) Events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return recentEventSlice(e.events)
}

// AllEvents returns every retained event (for offline artifact writing).
func (e *Engine) AllEvents() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, len(e.events))
	copy(out, e.events)
	return out
}

func recentEventSlice(events []Event) []Event {
	if len(events) <= recentEventsAPI {
		out := make([]Event, len(events))
		copy(out, events)
		return out
	}
	start := len(events) - recentEventsAPI
	out := make([]Event, recentEventsAPI)
	copy(out, events[start:])
	return out
}

func (e *Engine) eventCountsSnapshot() map[string]int {
	out := make(map[string]int, len(e.eventCounts))
	for k, v := range e.eventCounts {
		out[k] = v
	}
	return out
}

func (e *Engine) roshanObservationsSnapshot() []Event {
	return cloneEvents(e.roshanObservations)
}

func (e *Engine) buildingDestroyedSnapshot() []Event {
	return cloneEvents(e.buildingDestroyedEvents)
}

func (e *Engine) wardObservationsSnapshot() []Event {
	return cloneEvents(e.wardObservations)
}

func cloneEvents(in []Event) []Event {
	if len(in) == 0 {
		return nil
	}
	out := make([]Event, len(in))
	copy(out, in)
	return out
}

// isCompleteTenPlayerFrame reports whether a tick observed all ten heroes with
// the core telemetry fields (position, life state, level, economy).
func isCompleteTenPlayerFrame(tick NormalizedTick) bool {
	if len(tick.Players) != 10 {
		return false
	}
	for _, p := range tick.Players {
		if p.XPos == nil || p.YPos == nil || p.Alive == nil ||
			p.Level == nil || p.NetWorth == nil || p.Kills == nil {
			return false
		}
	}
	return true
}

// ---- delta derivation ---------------------------------------------------------

func (e *Engine) derive(cur, prev NormalizedTick) []Event {
	var out []Event
	ctx := eventContext{
		matchID:    cur.MatchID,
		clockTime:  cur.Map.ClockTime,
		receivedAt: cur.ReceivedAt,
	}

	out = append(out, e.deriveRoshan(ctx, cur.Roshan, prev.Roshan)...)
	out = append(out, e.deriveTormentor(ctx, cur.Tormentor, prev.Tormentor)...)
	out = append(out, e.deriveBuildings(ctx, cur.Buildings, prev.Buildings, cur.Map.GameState)...)
	out = append(out, e.deriveWardPurchaseCooldowns(ctx, cur.Map, prev.Map)...)

	prevPlayers := indexPlayers(prev.Players)
	for _, p := range cur.Players {
		prevP, ok := prevPlayers[playerKey{team: p.TeamKey, player: p.Slot}]
		if !ok {
			continue
		}
		out = append(out, e.derivePlayer(ctx, p, prevP)...)
	}
	return out
}

type eventContext struct {
	matchID    string
	clockTime  *int64
	receivedAt time.Time
}

func (e *Engine) deriveRoshan(ctx eventContext, cur, prev RoshanState) []Event {
	if cur.State == "" || prev.State == "" {
		return nil
	}
	if cur.State == prev.State {
		return nil
	}
	return []Event{{
		ReceivedAt:   ctx.receivedAt,
		Type:         EventRoshanStateChanged,
		Field:        "map.roshan_state",
		Before:       prev.State,
		After:        cur.State,
		SourcePaths:  []string{"map.roshan_state"},
		Confidence:   ConfidenceObserved,
		MatchID:      ctx.matchID,
		MapClockTime: ctx.clockTime,
	}}
}

func (e *Engine) deriveTormentor(ctx eventContext, cur, prev TormentorState) []Event {
	if cur.State == "" || prev.State == "" {
		return nil
	}
	if cur.State == prev.State {
		return nil
	}
	return []Event{{
		ReceivedAt:   ctx.receivedAt,
		Type:         EventTormentorStateChanged,
		Field:        "map.tormentor_state",
		Before:       prev.State,
		After:        cur.State,
		SourcePaths:  []string{"map.tormentor_state"},
		Confidence:   ConfidenceObserved,
		MatchID:      ctx.matchID,
		MapClockTime: ctx.clockTime,
	}}
}

func (e *Engine) deriveBuildings(ctx eventContext, cur, prev []BuildingTick, curGameState string) []Event {
	prevByID := make(map[string]BuildingTick)
	for _, b := range prev {
		prevByID[b.Team+":"+b.Name] = b
	}
	var out []Event
	for _, b := range cur {
		key := b.Team + ":" + b.Name
		pb, ok := prevByID[key]
		if !ok {
			continue
		}
		curHealth, curOK := val(b.Health)
		prevHealth, prevOK := val(pb.Health)
		if !curOK || !prevOK {
			continue
		}
		delta := prevHealth - curHealth
		if delta >= buildingHealthMinDelta {
			out = append(out, Event{
				ReceivedAt:   ctx.receivedAt,
				Type:         EventBuildingHealthLoss,
				Team:         b.Team,
				Field:        "buildings." + b.Team + "." + b.Name + ".health",
				Before:       prevHealth,
				After:        curHealth,
				SourcePaths:  []string{"buildings." + b.Team + "." + b.Name + ".health"},
				Confidence:   ConfidenceObserved,
				MatchID:      ctx.matchID,
				MapClockTime: ctx.clockTime,
			})
		}
		// Destroyed: previously positive health, now zero or absent.
		if prevHealth > 0 && curHealth <= 0 {
			out = append(out, Event{
				ReceivedAt:   ctx.receivedAt,
				Type:         EventBuildingDestroyed,
				Team:         b.Team,
				Field:        "buildings." + b.Team + "." + b.Name,
				Before:       prevHealth,
				After:        curHealth,
				SourcePaths:  []string{"buildings." + b.Team + "." + b.Name + ".health"},
				Confidence:   ConfidenceObserved,
				MatchID:      ctx.matchID,
				MapClockTime: ctx.clockTime,
			})
		}
	}
	// Buildings present in prev but absent in cur with positive health are
	// also treated as destroyed (structure removed from GSI on demolition) —
	// but only while the match is still actively in progress. When the game ends
	// GSI drops the whole buildings block, which is post-game cleanup and not
	// combat destruction, so an absent-from-post-game structure is not reported.
	curInProgress := curGameState == gameInProgress
	if curInProgress {
		curByID := make(map[string]BuildingTick)
		for _, b := range cur {
			curByID[b.Team+":"+b.Name] = b
		}
		for key, pb := range prevByID {
			if _, ok := curByID[key]; ok {
				continue
			}
			prevHealth, prevOK := val(pb.Health)
			if !prevOK || prevHealth <= 0 {
				continue
			}
			out = append(out, Event{
				ReceivedAt:   ctx.receivedAt,
				Type:         EventBuildingDestroyed,
				Team:         pb.Team,
				Field:        "buildings." + pb.Team + "." + pb.Name,
				Before:       prevHealth,
				After:        nil,
				SourcePaths:  []string{"buildings." + pb.Team + "." + pb.Name + ".health"},
				Confidence:   ConfidenceInferred,
				MatchID:      ctx.matchID,
				MapClockTime: ctx.clockTime,
			})
		}
	}
	return out
}

func (e *Engine) deriveWardPurchaseCooldowns(ctx eventContext, cur, prev MapState) []Event {
	var out []Event
	out = append(out, wardCooldownEvent(ctx, "radiant", cur.RadiantWardPurchaseCooldown, prev.RadiantWardPurchaseCooldown)...)
	out = append(out, wardCooldownEvent(ctx, "dire", cur.DireWardPurchaseCooldown, prev.DireWardPurchaseCooldown)...)
	return out
}

// wardCooldownEvent emits ward purchase-cooldown lifecycle transitions only:
// 0 -> positive marks a ward purchase window starting, and positive -> 0 marks
// it clearing. The per-tick cooldown decrement is not reported, since it is not
// a meaningful state change and would flood the event log.
func wardCooldownEvent(ctx eventContext, team string, curP, prevP *float64) []Event {
	curV, curOK := val(curP)
	prevV, prevOK := val(prevP)
	if !curOK || !prevOK {
		return nil
	}
	var events []Event
	path := "map." + team + "_ward_purchase_cooldown"
	if prevV == 0 && curV > 0 {
		events = append(events, Event{
			ReceivedAt:   ctx.receivedAt,
			Type:         EventWardPurchaseStarted,
			Team:         team,
			Field:        path,
			Before:       prevV,
			After:        curV,
			SourcePaths:  []string{path},
			Confidence:   ConfidenceObserved,
			MatchID:      ctx.matchID,
			MapClockTime: ctx.clockTime,
		})
	}
	if prevV > 0 && curV == 0 {
		events = append(events, Event{
			ReceivedAt:   ctx.receivedAt,
			Type:         EventWardPurchaseCooldown,
			Team:         team,
			Field:        path,
			Before:       prevV,
			After:        curV,
			SourcePaths:  []string{path},
			Confidence:   ConfidenceObserved,
			MatchID:      ctx.matchID,
			MapClockTime: ctx.clockTime,
		})
	}
	return events
}

func (e *Engine) derivePlayer(ctx eventContext, cur, prev PlayerTick) []Event {
	var out []Event
	team := cur.TeamName
	teamKey := cur.TeamKey
	id := cur.TeamKey + "." + cur.Slot

	out = append(out, e.deriveBool(ctx, team, teamKey, cur, "hero."+id+".alive", "alive", prev.Alive, cur.Alive,
		EventHeroDeath, EventHeroRespawn)...)
	out = append(out, e.deriveIntCounter(ctx, team, teamKey, cur, "player."+id+".kills", "kills", prev.Kills, cur.Kills, EventKillCounter)...)
	out = append(out, e.deriveIntCounter(ctx, team, teamKey, cur, "player."+id+".deaths", "deaths", prev.Deaths, cur.Deaths, EventDeathCounter)...)
	out = append(out, e.deriveIntCounter(ctx, team, teamKey, cur, "player."+id+".assists", "assists", prev.Assists, cur.Assists, EventAssistCounter)...)

	out = append(out, e.deriveNumThreshold(ctx, team, teamKey, cur, "player."+id+".gold", "gold", prev.Gold, cur.Gold, meaningfulGoldDelta, EventGoldChanged)...)
	out = append(out, e.deriveNumThreshold(ctx, team, teamKey, cur, "player."+id+".net_worth", "net_worth", prev.NetWorth, cur.NetWorth, meaningfulNetWorthDelta, EventNetWorthChanged)...)

	out = append(out, e.deriveItems(ctx, cur, prev)...)
	out = append(out, e.deriveAbilities(ctx, cur, prev)...)

	out = append(out, e.deriveWardCounters(ctx, team, teamKey, cur, "player."+id+".wards_placed", "wards_placed", prev.WardsPlaced, cur.WardsPlaced)...)
	out = append(out, e.deriveWardCounters(ctx, team, teamKey, cur, "player."+id+".wards_destroyed", "wards_destroyed", prev.WardsDestroyed, cur.WardsDestroyed)...)
	out = append(out, e.deriveWardCounters(ctx, team, teamKey, cur, "player."+id+".wards_purchased", "wards_purchased", prev.WardsPurchased, cur.WardsPurchased)...)
	return out
}

func (e *Engine) deriveBool(ctx eventContext, team, teamKey string, p PlayerTick, path, field string, prevP, curP *bool, onFalseEvent, onTrueEvent string) []Event {
	curV, curOK := valb(curP)
	prevV, prevOK := valb(prevP)
	if !curOK || !prevOK || curV == prevV {
		return nil
	}
	evType := onFalseEvent
	if curV {
		evType = onTrueEvent
	}
	return []Event{{
		ReceivedAt:   ctx.receivedAt,
		Type:         evType,
		Team:         team,
		TeamKey:      teamKey,
		Player:       p.Slot,
		Field:        path,
		Before:       prevV,
		After:        curV,
		SourcePaths:  []string{path},
		Confidence:   ConfidenceObserved,
		MatchID:      ctx.matchID,
		MapClockTime: ctx.clockTime,
	}}
}

func (e *Engine) deriveIntCounter(ctx eventContext, team, teamKey string, p PlayerTick, path, field string, prevP, curP *int64, evType string) []Event {
	curV, curOK := vali(curP)
	prevV, prevOK := vali(prevP)
	if !curOK || !prevOK || curV <= prevV {
		return nil
	}
	return []Event{{
		ReceivedAt:   ctx.receivedAt,
		Type:         evType,
		Team:         team,
		TeamKey:      teamKey,
		Player:       p.Slot,
		Field:        path,
		Before:       prevV,
		After:        curV,
		SourcePaths:  []string{path},
		Confidence:   ConfidenceObserved,
		MatchID:      ctx.matchID,
		MapClockTime: ctx.clockTime,
	}}
}

func (e *Engine) deriveNumThreshold(ctx eventContext, team, teamKey string, p PlayerTick, path, field string, prevP, curP *float64, threshold float64, evType string) []Event {
	curV, curOK := val(curP)
	prevV, prevOK := val(prevP)
	if !curOK || !prevOK {
		return nil
	}
	delta := curV - prevV
	if delta < 0 {
		delta = -delta
	}
	if delta < threshold {
		return nil
	}
	return []Event{{
		ReceivedAt:   ctx.receivedAt,
		Type:         evType,
		Team:         team,
		TeamKey:      teamKey,
		Player:       p.Slot,
		Field:        path,
		Before:       prevV,
		After:        curV,
		SourcePaths:  []string{path},
		Confidence:   ConfidenceObserved,
		MatchID:      ctx.matchID,
		MapClockTime: ctx.clockTime,
	}}
}

func (e *Engine) deriveItems(ctx eventContext, cur, prev PlayerTick) []Event {
	prevSlots := indexItems(prev.Items)
	curSlots := indexItems(cur.Items)
	var out []Event

	for slot, curItem := range curSlots {
		prevItem := prevSlots[slot]
		curName := itemRealName(curItem)
		prevName := itemRealName(prevItem)
		switch {
		case prevName == "" && curName != "":
			out = append(out, e.itemEvent(ctx, cur, EventItemAcquired, slot, prevItem, curItem))
		case prevName != "" && curName == "":
			out = append(out, e.itemEvent(ctx, cur, EventItemRemoved, slot, prevItem, curItem))
		case prevName != "" && curName != "" && prevName != curName:
			out = append(out, e.itemEvent(ctx, cur, EventItemReplaced, slot, prevItem, curItem))
		}
	}
	// Slots that held an item in prev but vanished in cur.
	for slot, prevItem := range prevSlots {
		if _, ok := curSlots[slot]; ok {
			continue
		}
		if itemRealName(prevItem) == "" {
			continue
		}
		out = append(out, e.itemEvent(ctx, cur, EventItemRemoved, slot, prevItem, ItemSlot{}))
	}

	// Slot relocations: same item name present in a different slot than before.
	out = append(out, e.deriveItemRelocations(ctx, cur, curSlots, prevSlots)...)
	return out
}

func (e *Engine) deriveItemRelocations(ctx eventContext, p PlayerTick, cur, prev map[string]ItemSlot) []Event {
	// Map item name -> slots containing it.
	prevByName := slotsByName(prev)
	curByName := slotsByName(cur)
	var out []Event
	for name, prevSlotsSet := range prevByName {
		curSlotsSet, ok := curByName[name]
		if !ok || name == "" {
			continue
		}
		if slotsEqual(prevSlotsSet, curSlotsSet) {
			continue
		}
		// Relocation only counts as a slot change if the item still exists and
		// its slot set differs (e.g. moved from slot to stash).
		from := anySlot(prevSlotsSet, curSlotsSet)
		to := anySlot(curSlotsSet, prevSlotsSet)
		if from == "" || to == "" || from == to {
			continue
		}
		// Full traceable source paths, consistent with itemEvent below, so a
		// relocation can be traced back to a raw player item path.
		prefix := "items." + p.TeamKey + "." + p.Slot + "."
		out = append(out, Event{
			ReceivedAt: ctx.receivedAt,
			Type:        EventItemSlotChanged,
			Team:        p.TeamName,
			TeamKey:     p.TeamKey,
			Player:      p.Slot,
			Field:       prefix + from + "->" + to,
			Before:      from,
			After:       to,
			SourcePaths: []string{prefix + from + ".name", prefix + to + ".name"},
			Confidence:  ConfidenceObserved,
			MatchID:     ctx.matchID,
			MapClockTime: ctx.clockTime,
		})
	}
	return out
}

func (e *Engine) itemEvent(ctx eventContext, p PlayerTick, evType, slot string, before, after ItemSlot) Event {
	path := "items." + p.TeamKey + "." + p.Slot + "." + slot + ".name"
	return Event{
		ReceivedAt: ctx.receivedAt,
		Type:       evType,
		Team:       p.TeamName,
		TeamKey:    p.TeamKey,
		Player:     p.Slot,
		Field:      path,
		Before:     itemNameOrEmpty(before),
		After:      itemNameOrEmpty(after),
		SourcePaths: []string{path},
		Confidence: ConfidenceObserved,
		MatchID:    ctx.matchID,
		MapClockTime: ctx.clockTime,
	}
}

func (e *Engine) deriveAbilities(ctx eventContext, cur, prev PlayerTick) []Event {
	prevAbs := indexAbilities(prev.Abilities)
	var out []Event
	for _, a := range cur.Abilities {
		pa, ok := prevAbs[a.Slot]
		if !ok {
			continue
		}
		// Level increases.
		curL, curOK := vali(a.Level)
		prevL, prevOK := vali(pa.Level)
		if curOK && prevOK && curL > prevL {
			path := "abilities." + cur.TeamKey + "." + cur.Slot + "." + a.Slot + ".level"
			out = append(out, Event{
				ReceivedAt: ctx.receivedAt,
				Type:        EventAbilityLevelChanged,
				Team:        cur.TeamName,
				TeamKey:     cur.TeamKey,
				Player:      cur.Slot,
				Field:       path,
				Before:      prevL,
				After:       curL,
				SourcePaths: []string{path},
				Confidence:  ConfidenceObserved,
				MatchID:     ctx.matchID,
				MapClockTime: ctx.clockTime,
			})
		}
		// Cooldown state transition: ready (0) <-> on cooldown (>0).
		curCD, curCDOK := val(a.Cooldown)
		prevCD, prevCDOK := val(pa.Cooldown)
		if curCDOK && prevCDOK {
			wasReady := prevCD <= 0
			nowReady := curCD <= 0
			if wasReady != nowReady {
				path := "abilities." + cur.TeamKey + "." + cur.Slot + "." + a.Slot + ".cooldown"
				out = append(out, Event{
					ReceivedAt: ctx.receivedAt,
					Type:        EventAbilityCooldownState,
					Team:        cur.TeamName,
					TeamKey:     cur.TeamKey,
					Player:      cur.Slot,
					Field:       path,
					Before:      prevCD,
					After:       curCD,
					SourcePaths: []string{path},
					Confidence:  ConfidenceObserved,
					MatchID:     ctx.matchID,
					MapClockTime: ctx.clockTime,
				})
			}
		}
	}
	return out
}

func (e *Engine) deriveWardCounters(ctx eventContext, team, teamKey string, p PlayerTick, path, field string, prevP, curP *int64) []Event {
	curV, curOK := vali(curP)
	prevV, prevOK := vali(prevP)
	if !curOK || !prevOK || curV <= prevV {
		return nil
	}
	return []Event{{
		ReceivedAt:   ctx.receivedAt,
		Type:         EventWardCounterChanged,
		Team:         team,
		TeamKey:      teamKey,
		Player:       p.Slot,
		Field:        path,
		Before:       prevV,
		After:        curV,
		SourcePaths:  []string{path},
		Confidence:   ConfidenceObserved,
		MatchID:      ctx.matchID,
		MapClockTime: ctx.clockTime,
	}}
}

// ---- indexing helpers --------------------------------------------------------

func indexPlayers(players []PlayerTick) map[playerKey]PlayerTick {
	out := make(map[playerKey]PlayerTick, len(players))
	for _, p := range players {
		out[playerKey{team: p.TeamKey, player: p.Slot}] = p
	}
	return out
}

func indexItems(items []ItemSlot) map[string]ItemSlot {
	out := make(map[string]ItemSlot, len(items))
	for _, it := range items {
		out[it.Slot] = it
	}
	return out
}

func indexAbilities(abs []AbilitySlot) map[string]AbilitySlot {
	out := make(map[string]AbilitySlot, len(abs))
	for _, a := range abs {
		out[a.Slot] = a
	}
	return out
}

// itemRealName returns the item name, collapsing the GSI "empty" sentinel to "".
func itemRealName(it ItemSlot) string {
	if it.Name == "" || it.Name == "empty" {
		return ""
	}
	return it.Name
}

func itemNameOrEmpty(it ItemSlot) string {
	if name := itemRealName(it); name != "" {
		return name
	}
	return ""
}

func slotsByName(items map[string]ItemSlot) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for slot, it := range items {
		name := itemRealName(it)
		if name == "" {
			continue
		}
		if out[name] == nil {
			out[name] = make(map[string]struct{})
		}
		out[name][slot] = struct{}{}
	}
	return out
}

func slotsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func anySlot(needle, exclude map[string]struct{}) string {
	for s := range needle {
		if _, excluded := exclude[s]; excluded {
			continue
		}
		return s
	}
	return ""
}

// ---- pointer value unwrap helpers --------------------------------------------

func val(p *float64) (float64, bool) {
	if p == nil {
		return 0, false
	}
	return *p, true
}

func vali(p *int64) (int64, bool) {
	if p == nil {
		return 0, false
	}
	return *p, true
}

func valb(p *bool) (bool, bool) {
	if p == nil {
		return false, false
	}
	return *p, true
}