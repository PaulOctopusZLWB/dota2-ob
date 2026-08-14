package analytics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

func payloadFromJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var p map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return p
}

// Normalize populates observed fields and leaves absent fields nil.
func TestNormalizePopulatesObservedFields(t *testing.T) {
	ts := mustParseTime(t, "2026-07-07T14:51:01.877Z")
	p := payloadFromJSON(t, miniSnapshot("item_boots", 1, 0))

	tick := Normalize(ts, p)
	if !tick.ReceivedAt.Equal(ts) {
		t.Fatalf("received_at not propagated: %v", tick.ReceivedAt)
	}
	if tick.Provider.Name != "Dota 2" || tick.Provider.AppID != 570 {
		t.Fatalf("provider not normalized: %+v", tick.Provider)
	}
	if tick.Map.GameState != "DOTA_GAMERULES_STATE_GAME_IN_PROGRESS" {
		t.Fatalf("map game_state not normalized: %+v", tick.Map)
	}
	if tick.MatchID != "8885589324" {
		t.Fatalf("match id = %q, want 8885589324", tick.MatchID)
	}
	if tick.Roshan.State != "alive" {
		t.Fatalf("roshan state = %q, want alive", tick.Roshan.State)
	}
	if len(tick.Buildings) == 0 {
		t.Fatalf("buildings not normalized")
	}
	pw := tick.Buildings[0]
	if pw.Team != "radiant" || pw.Name != "dota_goodguys_fort" {
		t.Fatalf("first building = %+v, want radiant/dota_goodguys_fort", pw)
	}
	if len(tick.Players) != 1 {
		t.Fatalf("players = %d, want 1", len(tick.Players))
	}
	player := tick.Players[0]
	if player.TeamName != "radiant" || player.Slot != "player0" {
		t.Fatalf("player identity = %+v", player)
	}
	if player.Alive == nil || !*player.Alive {
		t.Fatalf("alive not normalized: %+v", player.Alive)
	}
	if player.Kills == nil || *player.Kills != 0 {
		t.Fatalf("kills not normalized: %+v", player.Kills)
	}
	if player.XPos == nil || *player.XPos != -100 {
		t.Fatalf("xpos not normalized: %+v", player.XPos)
	}
	if len(player.Items) == 0 || player.Items[0].Name != "item_boots" {
		t.Fatalf("items not normalized: %+v", player.Items)
	}
	if len(player.Abilities) == 0 || player.Abilities[0].Name != "axe_berserkers_call" {
		t.Fatalf("abilities not normalized: %+v", player.Abilities)
	}
}

// Normalize on an empty payload must not fabricate state.
func TestNormalizeEmptyPayloadIsHarmless(t *testing.T) {
	tick := Normalize(time.Now(), map[string]any{})
	if len(tick.Players) != 0 || len(tick.Buildings) != 0 {
		t.Fatalf("empty payload should yield no players/buildings: %+v", tick)
	}
	if tick.MatchID != "" {
		t.Fatalf("empty payload match id = %q, want empty", tick.MatchID)
	}
}

// Engine derives only delta-based events and only after two observations.
func TestEngineDerivesDeltaEvents(t *testing.T) {
	engine := NewEngine()
	t1 := Normalize(mustParseTime(t, "2026-07-07T14:51:01Z"), payloadFromJSON(t, miniSnapshot("item_boots", 1, 0)))
	t2 := Normalize(mustParseTime(t, "2026-07-07T14:51:02Z"), payloadFromJSON(t, miniSnapshot("item_power_treads", 1, 12)))
	t2.Players[0].Alive = boolPtr(false)
	t2.Players[0].RespawnSeconds = numPtr(10)
	t2.Players[0].Kills = intPtr(1)
	t2.Players[0].Deaths = intPtr(1)
	t2.Players[0].Assists = intPtr(1)
	t2.Players[0].Gold = numPtr(900)
	t2.Players[0].NetWorth = numPtr(5200)
	t2.Buildings[0].Health = numPtr(4000)
	t2.Map.RadiantWardPurchaseCooldown = numPtr(120)

	if got := engine.Observe(t1); len(got) != 0 {
		t.Fatalf("first tick should derive no events, got %d", len(got))
	}
	derived := engine.Observe(t2)
	types := eventTypes(derived)
	want := map[string]bool{
		EventHeroDeath:            true,
		EventKillCounter:          true,
		EventDeathCounter:         true,
		EventAssistCounter:        true,
		EventGoldChanged:          true,
		EventNetWorthChanged:      true,
		EventItemReplaced:         true,
		EventAbilityCooldownState: true,
		EventBuildingHealthLoss:   true,
		EventWardPurchaseStarted:  true,
	}
	for ty := range want {
		if !types[ty] {
			t.Fatalf("missing expected event %q; got %v", ty, keys(types))
		}
	}
	for _, ev := range derived {
		if ev.Confidence == "" {
			t.Fatalf("event %q missing confidence", ev.Type)
		}
		if ev.ReceivedAt.IsZero() {
			t.Fatalf("event %q missing received_at", ev.Type)
		}
		if len(ev.SourcePaths) == 0 {
			t.Fatalf("event %q missing source paths", ev.Type)
		}
	}
}

// Building-from-absence destruction is only inferred while the game was in
// progress; post-game cleanup of intact structures must not be reported.
func TestBuildingDestroyedSkipsPostGameCleanup(t *testing.T) {
	engine := NewEngine()
	progress := "DOTA_GAMERULES_STATE_GAME_IN_PROGRESS"
	t1 := Normalize(time.Now(), payloadFromJSON(t, `{
		"map":{"game_state":"`+progress+`","clock_time":100},
		"buildings":{"radiant":{"dota_goodguys_tower1_top":{"health":1500,"max_health":1800}},
		             "dire":{"dota_badguys_fort":{"health":4500,"max_health":4500}}}}`))
	t2 := Normalize(time.Now(), payloadFromJSON(t, `{
		"map":{"game_state":"DOTA_GAMERULES_STATE_POST_GAME","clock_time":101,"win_team":"radiant"},
		"buildings":{"dire":{"dota_badguys_fort":{"health":0,"max_health":4500}}}}`))

	engine.Observe(t1)
	derived := engine.Observe(t2)
	for _, ev := range derived {
		if ev.Type == EventBuildingDestroyed && ev.Team == "radiant" {
			t.Fatalf("post-game cleanup of intact radiant tower reported as destroyed: %+v", ev)
		}
	}
	found := false
	for _, ev := range derived {
		if ev.Type == EventBuildingDestroyed && ev.Team == "dire" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dire fort health->0 destruction not emitted; got %v", eventTypes(derived))
	}
}

// Ward purchase cooldown emits lifecycle transitions only, never per-decrement.
func TestWardPurchaseCooldownTransitionsOnly(t *testing.T) {
	engine := NewEngine()
	mk := func(cd float64) NormalizedTick {
		return Normalize(time.Now(), payloadFromJSON(t, `{"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","radiant_ward_purchase_cooldown":`+fmtFloat(cd)+`}}`))
	}
	engine.Observe(mk(0))         // baseline ready
	d1 := engine.Observe(mk(120)) // start
	d2 := engine.Observe(mk(119)) // decrement
	d3 := engine.Observe(mk(0))   // cleared

	if !hasType(d1, EventWardPurchaseStarted) {
		t.Fatalf("start transition not emitted: %v", eventTypes(d1))
	}
	if hasType(d2, EventWardPurchaseCooldown) || hasType(d2, EventWardPurchaseStarted) {
		t.Fatalf("decrement should not emit a ward cooldown event: %v", eventTypes(d2))
	}
	if !hasType(d3, EventWardPurchaseCooldown) {
		t.Fatalf("clear transition not emitted: %v", eventTypes(d3))
	}
}

// AnalyzeSession writes the four required artifacts and parses as JSON/JSONL.
func analyzeSessionLegacy(sessionDir, sessionID string) (Snapshot, error) {
	return AnalyzeSessionWithNormalizer(sessionDir, sessionID, func(_ int, record RebuildRecord) (NormalizedTick, error) {
		return Normalize(record.ReceivedAt, record.Payload), nil
	})
}

func TestAnalyzeSessionWritesArtifacts(t *testing.T) {
	dir := t.TempDir()
	p1, p2 := miniSnapshot("item_boots", 1, 0), miniSnapshot("item_power_treads", 1, 12)
	raw := fmt.Sprintf("{\"received_at\":\"2026-07-07T14:51:01Z\",\"payload\":%s,\"raw\":%s}\n{\"received_at\":\"2026-07-07T14:51:02Z\",\"payload\":%s,\"raw\":%s}\n", p1, p1, p2, p2)
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	snap, err := analyzeSessionLegacy(dir, "test-session")
	if err != nil {
		t.Fatalf("AnalyzeSession: %v", err)
	}
	if snap.TickCount != 2 {
		t.Fatalf("tick count = %d, want 2", snap.TickCount)
	}
	for _, name := range []string{"normalized_ticks.jsonl", "derived_events.jsonl", "analytics_summary.json", "analytics_summary.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("artifact %s missing or empty: %v", name, err)
		}
	}
	// Validate the JSONL/JSON artifacts parse.
	for _, name := range []string{"normalized_ticks.jsonl", "derived_events.jsonl"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		dec := json.NewDecoder(f)
		for {
			var v any
			if err := dec.Decode(&v); err != nil {
				if err.Error() == "EOF" {
					break
				}
				t.Fatalf("decode %s line: %v", name, err)
			}
		}
		f.Close()
	}
	var summaryJSON map[string]any
	sb, err := os.ReadFile(filepath.Join(dir, "analytics_summary.json"))
	if err != nil {
		t.Fatalf("read summary json: %v", err)
	}
	if err := json.Unmarshal(sb, &summaryJSON); err != nil {
		t.Fatalf("parse summary json: %v", err)
	}
	summary, err := os.ReadFile(filepath.Join(dir, "analytics_summary.md"))
	if err != nil {
		t.Fatalf("read summary md: %v", err)
	}
	for _, want := range []string{"Normalized tick count", "Complete ten-player frames", "Event Counts By Type", "Ward Observations", "Exact ward coordinates"} {
		if !strings.Contains(string(summary), want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestAnalyzeSessionReadsVersion2AndIgnoresUnterminatedTail(t *testing.T) {
	dir := t.TempDir()
	raw := `{"schema_version":2,"session_id":"v2-session","sequence":1,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":{"map":{"game_time":1}},"raw":{"map":{"game_time":1}}}` + "\n" + `{"unterminated"`
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := analyzeSessionLegacy(dir, "v2-session")
	if err != nil {
		t.Fatalf("AnalyzeSession: %v", err)
	}
	if snap.TickCount != 1 {
		t.Fatalf("tick count = %d, want 1", snap.TickCount)
	}
}

func TestAnalyzeSessionRejectsUnknownVersionAndTerminatedCorruption(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"unknown-version", `{"schema_version":99,"received_at":"2026-08-05T12:00:00Z","payload":{}}` + "\n"},
		{"null-version", `{"schema_version":null,"received_at":"2026-08-05T12:00:00Z","payload":{},"raw":{}}` + "\n"},
		{"terminated-corruption", "not-json\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(tc.raw), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := analyzeSessionLegacy(dir, "session"); err == nil {
				t.Fatal("AnalyzeSession succeeded")
			} else if len(err.Error()) > 240 {
				t.Fatalf("error is unbounded: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestAnalyzeSessionRejectsTwoValuesOnOneTerminatedLine(t *testing.T) {
	dir := t.TempDir()
	raw := `{"received_at":"2026-08-05T12:00:00Z","payload":{},"raw":{}}{"received_at":"2026-08-05T12:00:01Z","payload":{},"raw":{}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := analyzeSessionLegacy(dir, "session"); err == nil {
		t.Fatal("AnalyzeSession accepted two values on one JSONL line")
	}
}

func TestAnalyzeSessionConsumesLegacyNullWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	raw := `{"schema_version":2,"session_id":"null-session","sequence":1,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":null,"raw":null}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := analyzeSessionLegacy(dir, "null-session")
	if err != nil {
		t.Fatalf("AnalyzeSession: %v", err)
	}
	if snap.TickCount != 0 {
		t.Fatalf("tick count=%d", snap.TickCount)
	}
}

// ---- helpers -----------------------------------------------------------------

func miniSnapshot(item string, abilityLevel int, abilityCooldown float64) string {
	return fmt.Sprintf(`{
		"provider":{"name":"Dota 2","appid":570,"version":48,"timestamp":1783435861},
		"league":{"league_id":0,"match_id":"8885589324"},
		"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100,"game_time":200,"radiant_score":14,"dire_score":13,"radiant_ward_purchase_cooldown":0,"dire_ward_purchase_cooldown":0,"roshan_state":"alive","roshan_state_end_seconds":0},
		"hero":{"team2":{"player0":{"name":"npc_dota_hero_axe","id":42,"alive":true,"level":6,"xpos":-100,"ypos":-100,"health":1000,"max_health":1000,"mana":300,"max_mana":600,"buyback_cost":1500,"buyback_cooldown":0}}},
		"player":{"team2":{"player0":{"name":"A","team_name":"radiant","player_slot":0,"steamid":"76561198","accountid":"123","kills":0,"deaths":0,"assists":0,"gold":500,"net_worth":5000,"gpm":700,"xpm":800,"gold_reliable":200,"gold_unreliable":300,"last_hits":10,"denies":1,"wards_placed":2,"wards_destroyed":0,"wards_purchased":3}}},
		"items":{"team2":{"player0":{"slot0":{"name":"%s","item_level":1,"cooldown":0,"max_cooldown":10,"can_cast":true}}}},
		"abilities":{"team2":{"player0":{"ability0":{"name":"axe_berserkers_call","level":%d,"cooldown":%s,"max_cooldown":12,"can_cast":true}}}},
		"buildings":{"radiant":{"dota_goodguys_fort":{"health":4500,"max_health":4500}}}
	}`, item, abilityLevel, fmtFloat(abilityCooldown))
}

func fmtFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func eventTypes(events []Event) map[string]bool {
	out := make(map[string]bool)
	for _, e := range events {
		out[e.Type] = true
	}
	return out
}

func hasType(events []Event, ty string) bool {
	for _, e := range events {
		if e.Type == ty {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func boolPtr(b bool) *bool      { return &b }
func numPtr(f float64) *float64 { return &f }
func intPtr(i int64) *int64     { return &i }

// Normalized tick includes spec-required hero identity, buyback, and
// unreliable gold when present in raw GSI.
func TestNormalizeIncludesSpecRequiredFields(t *testing.T) {
	ts := mustParseTime(t, "2026-07-07T14:51:01.877Z")
	tick := Normalize(ts, payloadFromJSON(t, miniSnapshot("item_boots", 1, 0)))
	if len(tick.Players) != 1 {
		t.Fatalf("players = %d, want 1", len(tick.Players))
	}
	p := tick.Players[0]
	if p.HeroName != "npc_dota_hero_axe" {
		t.Fatalf("hero_name = %q, want npc_dota_hero_axe", p.HeroName)
	}
	if p.HeroID == nil || *p.HeroID != 42 {
		t.Fatalf("hero_id = %+v, want 42", p.HeroID)
	}
	if p.BuybackCost == nil || *p.BuybackCost != 1500 {
		t.Fatalf("buyback_cost = %+v, want 1500", p.BuybackCost)
	}
	if p.BuybackCooldown == nil || *p.BuybackCooldown != 0 {
		t.Fatalf("buyback_cooldown = %+v, want 0", p.BuybackCooldown)
	}
	if p.GoldUnreliable == nil || *p.GoldUnreliable != 300 {
		t.Fatalf("gold_unreliable = %+v, want 300", p.GoldUnreliable)
	}
	if p.GoldReliable == nil || *p.GoldReliable != 200 {
		t.Fatalf("gold_reliable = %+v, want 200", p.GoldReliable)
	}
	// Observed-field footprint records the new hero/player fields.
	obs := strings.Join(p.ObservedFields, ",")
	for _, want := range []string{"hero.team2.player0.name", "hero.team2.player0.id", "hero.team2.player0.buyback_cost", "hero.team2.player0.buyback_cooldown", "player.team2.player0.gold_unreliable"} {
		if !strings.Contains(obs, want) {
			t.Fatalf("observed fields missing %q: %s", want, obs)
		}
	}
}

// item_slot_changed events use full traceable source paths with team/player.
func TestItemSlotChangedSourcePathsIncludeIdentity(t *testing.T) {
	// tick1: item in slot0; tick2: same item relocated to stash0, slot0 emptied.
	mk := func(slot string) string {
		return `{
			"provider":{"name":"Dota 2","appid":570},
			"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},
			"hero":{"team2":{"player0":{"name":"npc_dota_hero_axe","id":42,"alive":true,"level":6,"xpos":-100,"ypos":-100}}},
			"player":{"team2":{"player0":{"name":"A","team_name":"radiant","net_worth":5000,"gold":500}}},
			"items":{"team2":{"player0":{` + slot + `}}}
		}`
	}
	engine := NewEngine()
	engine.Observe(Normalize(time.Now(), payloadFromJSON(t, mk(`"slot0":{"name":"item_boots"}`))))
	// Relocate: keep slot0 holding a different item name is not relocation.
	// Put the same item name into stash0 and empty slot0 to trigger relocation.
	t2 := Normalize(time.Now(), payloadFromJSON(t, mk(`"stash0":{"name":"item_boots"}`)))
	derived := engine.Observe(t2)

	var reloc *Event
	for i := range derived {
		if derived[i].Type == EventItemSlotChanged {
			reloc = &derived[i]
			break
		}
	}
	if reloc == nil {
		t.Fatalf("no item_slot_changed event emitted; got %v", eventTypes(derived))
	}
	if reloc.TeamKey != "team2" || reloc.Player != "player0" {
		t.Fatalf("relocation identity = %+v", reloc)
	}
	if reloc.Field != "items.team2.player0.slot0->stash0" {
		t.Fatalf("field = %q, want items.team2.player0.slot0->stash0", reloc.Field)
	}
	wantPaths := map[string]bool{"items.team2.player0.slot0.name": true, "items.team2.player0.stash0.name": true}
	for _, p := range reloc.SourcePaths {
		if !wantPaths[p] {
			t.Fatalf("unexpected source path %q; field=%s", p, reloc.Field)
		}
		delete(wantPaths, p)
	}
	if len(wantPaths) != 0 {
		t.Fatalf("missing source paths %v for field=%s", wantPaths, reloc.Field)
	}
}

// Live WriteSummaryFiles reports the actual accepted live tick count, not 0.
func TestWriteSummaryFilesTickCountMatchesLive(t *testing.T) {
	dir := t.TempDir()
	engine := NewEngine()
	engine.Observe(Normalize(mustParseTime(t, "2026-07-07T14:51:01Z"), payloadFromJSON(t, miniSnapshot("item_boots", 1, 0))))
	engine.Observe(Normalize(mustParseTime(t, "2026-07-07T14:51:02Z"), payloadFromJSON(t, miniSnapshot("item_boots", 1, 0))))
	if err := WriteSummaryFiles(dir, "live-session", engine); err != nil {
		t.Fatalf("WriteSummaryFiles: %v", err)
	}
	var s SummaryJSON
	b, err := os.ReadFile(filepath.Join(dir, "analytics_summary.json"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if s.TickCount != 2 {
		t.Fatalf("live summary tick_count = %d, want 2", s.TickCount)
	}
	if len(s.LatestPlayerEconomy) != 1 {
		t.Fatalf("live summary latest_player_economy = %d players, want 1", len(s.LatestPlayerEconomy))
	}
	if s.LatestPlayerEconomy[0].HeroName != "npc_dota_hero_axe" {
		t.Fatalf("live economy hero_name = %q", s.LatestPlayerEconomy[0].HeroName)
	}
	md, err := os.ReadFile(filepath.Join(dir, "analytics_summary.md"))
	if err != nil {
		t.Fatalf("read summary md: %v", err)
	}
	if !strings.Contains(string(md), "## Economy Summary") {
		t.Fatalf("summary md missing economy section:\n%s", md)
	}
}

// Offline derived_events.jsonl is complete even when the live ring buffer cap
// would drop older events.
func TestOfflineDerivedEventsCompleterThanRingCap(t *testing.T) {
	dir := t.TempDir()
	// Each tick advances gold for all ten players by >= meaningfulGoldDelta, so
	// every consecutive tick pair yields ten gold_changed events. We use enough
	// ticks that total events exceed maxEvents, proving offline artifact
	// generation does not silently lose older events the way the live ring would.
	const ticks = maxEvents/10 + 20
	mk := func(gold float64) string {
		players := goldPlayersJSON(gold)
		return fmt.Sprintf(`{
			"provider":{"name":"Dota 2","appid":570},
			"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},
			"player":{%s}
		}`, players)
	}
	var raw strings.Builder
	for i := 0; i < ticks; i++ {
		gold := 500 + float64(i)*meaningfulGoldDelta
		payload := mk(gold)
		fmt.Fprintf(&raw, `{"received_at":"2026-07-07T14:51:00Z","payload":%s,"raw":%s}`+"\n", payload, payload)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw.String()), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if _, err := analyzeSessionLegacy(dir, "long-session"); err != nil {
		t.Fatalf("AnalyzeSession: %v", err)
	}
	f, err := os.Open(filepath.Join(dir, "derived_events.jsonl"))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()
	count := 0
	dec := json.NewDecoder(f)
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode event: %v", err)
		}
		count++
	}
	// Ten players × (ticks-1) deltas = total gold_changed events, all of which
	// must be present in the offline artifact, exceeding the live ring cap.
	want := 10 * (ticks - 1)
	if count != want {
		t.Fatalf("offline derived_events.jsonl count = %d, want %d (all events; ring cap %d)", count, want, maxEvents)
	}
	if want <= maxEvents {
		t.Fatalf("test fixture produced %d events, expected to exceed ring cap %d", want, maxEvents)
	}
}

// goldPlayersJSON returns a ten-player player-section JSON snippet where every
// player's gold is set to the same value, spanning team2 player0-4 and team3
// player5-9.
func goldPlayersJSON(gold float64) string {
	teams := []string{"team2", "team3"}
	perTeam := 5
	var b strings.Builder
	for ti, team := range teams {
		if ti > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + team + `":{`)
		for i := 0; i < perTeam; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			slot := fmt.Sprintf("player%d", ti*perTeam+i)
			b.WriteString(fmt.Sprintf(`"%s":{"name":"P%d","team_name":"%s","net_worth":5000,"gold":%s}`, slot, ti*perTeam+i, teamFromKey(team), fmtFloat(gold)))
		}
		b.WriteString("}")
	}
	return b.String()
}

func teamFromKey(teamKey string) string {
	switch teamKey {
	case "team2":
		return "radiant"
	case "team3":
		return "dire"
	default:
		return ""
	}
}

// The live /api/analytics snapshot bounds per-type observation arrays so a long
// session cannot grow the response linearly; the total count and truncation
// flag preserve traceability while the engine retains only the recent tail.
func TestSnapshotBoundsObservationArrays(t *testing.T) {
	engine := NewEngine()
	// Each tick increments one player's wards_placed counter, producing one
	// ward_counter_changed observation per consecutive tick pair.
	mk := func(wards int64) NormalizedTick {
		return Normalize(time.Now(), payloadFromJSON(t, fmt.Sprintf(`{
			"provider":{"name":"Dota 2","appid":570},
			"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},
			"player":{"team2":{"player0":{"name":"A","team_name":"radiant","wards_placed":%d}}}
		}`, wards)))
	}
	const ticks = maxObservationsAPI + 50
	for i := int64(0); i < ticks; i++ {
		engine.Observe(mk(i))
	}
	snap := engine.Snapshot("bound-session")
	if got := len(snap.WardObservations); got > maxObservationsAPI {
		t.Fatalf("live snapshot ward_observations = %d, want <= %d", got, maxObservationsAPI)
	}
	if got := len(snap.WardObservations); got != maxObservationsAPI {
		t.Fatalf("retained ward_observations = %d, want exactly %d (most recent)", got, maxObservationsAPI)
	}
	wantTotal := int(ticks - 1)
	if snap.WardObservationsTotal != wantTotal {
		t.Fatalf("ward_observations_total = %d, want %d", snap.WardObservationsTotal, wantTotal)
	}
	if !snap.ObservationsTruncated {
		t.Fatalf("observations_truncated = false, want true (wards exceeded cap)")
	}
	// Roshan/buildings were not observed: zero totals, no truncation contribution.
	if snap.RoshanObservationsTotal != 0 || snap.BuildingDestroyedTotal != 0 {
		t.Fatalf("roshan/building totals = %d/%d, want 0/0", snap.RoshanObservationsTotal, snap.BuildingDestroyedTotal)
	}
}

// Offline analytics_summary.json keeps observation arrays complete even when the
// live /api/analytics snapshot would cap them; derived_events.jsonl stays
// complete too.
func TestOfflineSummaryObservationsComplete(t *testing.T) {
	dir := t.TempDir()
	const ticks = maxObservationsAPI + 50
	var raw strings.Builder
	for i := 0; i < ticks; i++ {
		payload := fmt.Sprintf(`{"provider":{"name":"Dota 2","appid":570},"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},"player":{"team2":{"player0":{"name":"A","team_name":"radiant","wards_placed":%d}}}}`, i)
		fmt.Fprintf(&raw, `{"received_at":"2026-07-07T14:51:00Z","payload":%s,"raw":%s}`+"\n", payload, payload)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw.String()), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if _, err := analyzeSessionLegacy(dir, "long-wards"); err != nil {
		t.Fatalf("AnalyzeSession: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "analytics_summary.json"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var s SummaryJSON
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	wantTotal := ticks - 1
	if s.WardObservationsTotal != wantTotal {
		t.Fatalf("offline ward_observations_total = %d, want %d", s.WardObservationsTotal, wantTotal)
	}
	if len(s.WardObservations) != wantTotal {
		t.Fatalf("offline ward_observations array = %d, want full %d (offline must not cap)", len(s.WardObservations), wantTotal)
	}
	if s.ObservationsTruncated {
		t.Fatalf("offline summary marked truncated, want complete")
	}
	// derived_events.jsonl is also complete.
	f, err := os.Open(filepath.Join(dir, "derived_events.jsonl"))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()
	count := 0
	dec := json.NewDecoder(f)
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode event: %v", err)
		}
		count++
	}
	if count != wantTotal {
		t.Fatalf("offline derived_events.jsonl = %d, want %d", count, wantTotal)
	}
	// The live snapshot for the same engine state WOULD cap, proving the bound
	// is enforced for /api/analytics while the offline artifact stays complete.
	engine := NewEngine()
	for i := int64(0); i < ticks; i++ {
		engine.Observe(Normalize(time.Now(), payloadFromJSON(t, fmt.Sprintf(`{
			"provider":{"name":"Dota 2","appid":570},
			"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100},
			"player":{"team2":{"player0":{"name":"A","team_name":"radiant","wards_placed":%d}}}
		}`, i))))
	}
	liveSnap := engine.Snapshot("bound-session")
	if len(liveSnap.WardObservations) > maxObservationsAPI {
		t.Fatalf("live snapshot ward_observations = %d, want <= %d", len(liveSnap.WardObservations), maxObservationsAPI)
	}
}
