package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestMapLiveObservationPreservesPublicNormalizedFields(t *testing.T) {
	received := time.Date(2026, 8, 12, 10, 11, 12, 0, time.UTC)
	raw := []byte(`{"provider":{"name":"Dota 2","version":1},"map":{"matchid":"42","clock_time":0,"paused":false},"player":{"team2":{"player0":{"accountid":"private-account","steamid":"private-steam","name":"private-name","team_name":"radiant","player_slot":0,"gold":0}}},"hero":{"team2":{"player0":{"name":"npc_dota_hero_axe","id":2,"alive":false,"xpos":0,"ypos":1}}}}`)
	var payload any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	record := &session.Record{SchemaVersion: 2, SessionID: "session-1", Sequence: 7, ReceivedAt: received, Source: "gsi", Payload: payload, Raw: raw}

	observation, err := MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	if observation.SchemaVersion != contracts.LiveObservationSchemaV1 || observation.Evidence.Sequence != 7 || observation.MatchID.Value == nil || *observation.MatchID.Value != "42" {
		t.Fatalf("identity not mapped: %+v", observation)
	}
	if observation.Provider.Name.Value == nil || *observation.Provider.Name.Value != "Dota 2" || observation.Provider.AppID.State != contracts.ValueAbsent || observation.Provider.Timestamp.State != contracts.ValueAbsent {
		t.Fatalf("provider presence not mapped: %+v", observation.Provider)
	}
	if len(observation.Participants) != 1 {
		t.Fatalf("participants=%d", len(observation.Participants))
	}
	p := observation.Participants[0]
	if p.Gold.State != contracts.ValuePresent || p.Gold.Value == nil || *p.Gold.Value != contracts.Decimal("0") {
		t.Fatalf("present zero lost: %+v", p.Gold)
	}
	if p.Alive.State != contracts.ValuePresent || p.Alive.Value == nil || *p.Alive.Value {
		t.Fatalf("present false lost: %+v", p.Alive)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-account", "private-steam", "private-name", "accountid", "steamid"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private field leaked: %s", private)
		}
	}

	legacy := analytics.Normalize(received, payload)
	if observation.Map.ClockTime.Value == nil || legacy.Map.ClockTime == nil || *observation.Map.ClockTime.Value != *legacy.Map.ClockTime {
		t.Fatal("clock differs from legacy")
	}
	if p.HeroName.Value == nil || *p.HeroName.Value != legacy.Players[0].HeroName {
		t.Fatal("hero differs from legacy")
	}
	adapted, err := LegacyNormalizedTickV1(observation, currentRecordResolver(record))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(adapted, legacy) {
		t.Fatalf("adapter changed legacy tick\nwant=%#v\n got=%#v", legacy, adapted)
	}
}

func TestCanonicalPathTenPlayerFullFieldEquivalenceAndPrivacy(t *testing.T) {
	root := map[string]any{"provider": map[string]any{"name": "Dota 2", "appid": 570, "version": 1, "timestamp": 123}, "league": map[string]any{"match_id": "42"}, "map": map[string]any{"clock_time": 0, "game_time": 1, "game_state": "playing", "paused": false, "daytime": true, "nightstalker_night": false, "win_team": "none", "radiant_score": 1, "dire_score": 2, "radiant_glyph_cooldown": 0.0004, "dire_glyph_cooldown": 4.125, "radiant_scan_charges": 1, "dire_scan_charges": 2, "radiant_lotus_pool_count": 3, "dire_lotus_pool_count": 4, "radiant_ward_purchase_cooldown": 5.125, "dire_ward_purchase_cooldown": 6.125, "roshan_state": "alive", "roshan_state_end_seconds": 7.125, "tormentor_state": "alive", "tormentor_state_location": "radiant", "tormentor_state_end_seconds": 8.125}, "player": map[string]any{"team2": map[string]any{}, "team3": map[string]any{}}, "hero": map[string]any{"team2": map[string]any{}, "team3": map[string]any{}}, "items": map[string]any{"team2": map[string]any{}, "team3": map[string]any{}}, "abilities": map[string]any{"team2": map[string]any{}, "team3": map[string]any{}}, "buildings": map[string]any{"radiant": map[string]any{"tower": map[string]any{"health": 100.125, "max_health": 200.125}}}}
	for i := 0; i < 10; i++ {
		team := "team2"
		slot := fmt.Sprintf("player%d", i)
		if i >= 5 {
			team = "team3"
		}
		root["player"].(map[string]any)[team].(map[string]any)[slot] = map[string]any{"accountid": fmt.Sprintf("private-account-%d", i), "steamid": fmt.Sprintf("private-steam-%d", i), "name": fmt.Sprintf("private-name-%d", i), "team_name": team, "player_slot": i, "gold": float64(i) + 0.125, "net_worth": 1000.125, "gpm": 500, "xpm": 600, "gold_reliable": 10.125, "gold_unreliable": 20.125, "kills": 1, "deaths": 2, "assists": 3, "last_hits": 4, "denies": 5, "wards_placed": 6, "wards_destroyed": 7, "wards_purchased": 8}
		root["hero"].(map[string]any)[team].(map[string]any)[slot] = map[string]any{"name": "npc_dota_hero_axe", "id": 2, "xpos": 1.23456, "ypos": 2.125, "health": 3.125, "max_health": 4.125, "health_percent": 5.125, "mana": 6.125, "max_mana": 7.125, "mana_percent": 8.125, "alive": i%2 == 0, "respawn_seconds": 9.125, "level": 10, "xp": 11.125, "buyback_cost": 12.125, "buyback_cooldown": 13.125, "stunned": false, "silenced": false, "disarmed": false, "hexed": false, "muted": false, "break": false, "has_debuff": false, "magicimmune": false, "smoked": false}
		root["items"].(map[string]any)[team].(map[string]any)[slot] = map[string]any{"slot0": map[string]any{"name": "item_blink", "item_level": 1, "cooldown": 1.125, "max_cooldown": 2.125, "can_cast": true, "charges": 3, "passive": false}}
		root["abilities"].(map[string]any)[team].(map[string]any)[slot] = map[string]any{"ability0": map[string]any{"name": "axe_berserkers_call", "level": 1, "cooldown": 1.125, "max_cooldown": 2.125, "can_cast": true, "passive": false, "ultimate": false}}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var payload any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	record := &session.Record{SchemaVersion: 2, SessionID: "session-1", Sequence: 1, ReceivedAt: time.Unix(1, 0).UTC(), Source: "gsi", Payload: payload, Raw: raw}
	observation, err := MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Participants) != 10 {
		t.Fatalf("participants=%d", len(observation.Participants))
	}
	if observation.Map.RadiantGlyphCooldown.Value == nil || *observation.Map.RadiantGlyphCooldown.Value != contracts.Decimal("0.0004") || observation.Participants[0].XPos.Value == nil || *observation.Participants[0].XPos.Value != contracts.Decimal("1.23456") {
		t.Fatalf("source numeric tokens were not preserved: cooldown=%+v xpos=%+v", observation.Map.RadiantGlyphCooldown, observation.Participants[0].XPos)
	}
	encoded, _ := contracts.MarshalCanonical(observation)
	for i := 0; i < 10; i++ {
		for _, prefix := range []string{"private-account-", "private-steam-", "private-name-"} {
			if strings.Contains(string(encoded), fmt.Sprintf("%s%d", prefix, i)) {
				t.Fatalf("private identity leaked: %s", encoded)
			}
		}
	}
	got, err := LegacyNormalizedTickV1(observation, currentRecordResolver(record))
	if err != nil {
		t.Fatal(err)
	}
	want := analytics.Normalize(record.ReceivedAt, payload)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full-field legacy mismatch\nwant=%#v\n got=%#v", want, got)
	}
	bad := *record
	bad.Raw = []byte(`{}`)
	if _, err := LegacyNormalizedTickV1(observation, currentRecordResolver(&bad)); err == nil {
		t.Fatal("mismatched evidence accepted")
	}
}

func TestCanonicalRebuildIsByteCompatible(t *testing.T) {
	raw := []byte(`{"provider":{"name":"Dota 2","version":1},"map":{"matchid":"42","clock_time":1,"radiant_glyph_cooldown":0.0004},"player":{"team2":{"player0":{"accountid":"private","name":"handle","gold":1.23456}}},"hero":{"team2":{"player0":{"name":"npc_dota_hero_axe","alive":true,"xpos":1.23456}}}}`)
	var payload any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	record := session.Record{SchemaVersion: 2, SessionID: "session-1", Sequence: 1, ReceivedAt: time.Unix(1, 0).UTC(), Source: "gsi", Payload: payload, Raw: raw}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	legacyDir := filepath.Join(t.TempDir(), "session-1")
	canonicalDir := filepath.Join(t.TempDir(), "session-1")
	for _, dir := range []string{legacyDir, canonicalDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), line, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := analytics.AnalyzeSessionWithNormalizer(legacyDir, "session-1", func(_ int, r analytics.RebuildRecord) (analytics.NormalizedTick, error) {
		return analytics.Normalize(r.ReceivedAt, r.Payload), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeSession(canonicalDir, "session-1"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"normalized_ticks.jsonl", "derived_events.jsonl", "analytics_summary.json", "analytics_summary.md"} {
		want, err := os.ReadFile(filepath.Join(legacyDir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(canonicalDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s changed\nwant=%s\n got=%s", name, want, got)
		}
	}
}

func TestCanonicalRebuildStreamsRawRecordV3(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("v3-rebuild"), session.WithClock(func() time.Time { return time.Unix(1, 0).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"map":{"game_time":1,"clock_time":1}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`null`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"map":{"game_time":2,"clock_time":2}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := AnalyzeSession(store.SessionDir(), store.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TickCount != 2 {
		t.Fatalf("tick count=%d", snapshot.TickCount)
	}
}

func TestLegacyV2RebuildSkipsOutOfDomainRecordAndContinues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "legacy-bounds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	players := make([]string, 11)
	for i := range players {
		players[i] = fmt.Sprintf(`"p%d":{}`, i)
	}
	over := `{"player":{"t":{` + strings.Join(players, ",") + `}}}`
	valid := `{"map":{"game_time":2}}`
	line := func(seq int, payload string) string {
		return fmt.Sprintf(`{"schema_version":2,"session_id":"legacy-bounds","sequence":%d,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":%s,"raw":%s}`+"\n", seq, payload, payload)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(line(1, over)+line(2, valid)), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := AnalyzeSession(dir, "legacy-bounds")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TickCount != 1 {
		t.Fatalf("tick count=%d", snapshot.TickCount)
	}
}
