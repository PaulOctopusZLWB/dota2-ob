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
		EventWardPurchaseStarted:   true,
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
	engine.Observe(mk(0))        // baseline ready
	d1 := engine.Observe(mk(120)) // start
	d2 := engine.Observe(mk(119)) // decrement
	d3 := engine.Observe(mk(0))    // cleared

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
func TestAnalyzeSessionWritesArtifacts(t *testing.T) {
	dir := t.TempDir()
	raw := `{"received_at":"2026-07-07T14:51:01Z","payload":` + miniSnapshot("item_boots", 1, 0) + `}
{"received_at":"2026-07-07T14:51:02Z","payload":` + miniSnapshot("item_power_treads", 1, 12) + `}
`
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	snap, err := AnalyzeSession(dir, "test-session")
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

// ---- helpers -----------------------------------------------------------------

func miniSnapshot(item string, abilityLevel int, abilityCooldown float64) string {
	return fmt.Sprintf(`{
		"provider":{"name":"Dota 2","appid":570,"version":48,"timestamp":1783435861},
		"league":{"league_id":0,"match_id":"8885589324"},
		"map":{"game_state":"DOTA_GAMERULES_STATE_GAME_IN_PROGRESS","clock_time":100,"game_time":200,"radiant_score":14,"dire_score":13,"radiant_ward_purchase_cooldown":0,"dire_ward_purchase_cooldown":0,"roshan_state":"alive","roshan_state_end_seconds":0},
		"hero":{"team2":{"player0":{"alive":true,"level":6,"xpos":-100,"ypos":-100,"health":1000,"max_health":1000,"mana":300,"max_mana":600}}},
		"player":{"team2":{"player0":{"name":"A","team_name":"radiant","player_slot":0,"steamid":"76561198","accountid":"123","kills":0,"deaths":0,"assists":0,"gold":500,"net_worth":5000,"gpm":700,"xpm":800,"last_hits":10,"denies":1,"wards_placed":2,"wards_destroyed":0,"wards_purchased":3}}},
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