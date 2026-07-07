package profile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
)

func TestProfilerDiscoversNestedFieldsAndCountsNulls(t *testing.T) {
	profiler := profile.NewProfiler()
	first := time.Date(2026, 7, 5, 14, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Second)

	profiler.Observe(first, map[string]any{
		"hero": map[string]any{
			"team2": map[string]any{
				"player0": map[string]any{
					"name": "npc_dota_hero_axe",
					"xpos": float64(100),
					"ypos": nil,
				},
			},
		},
	})
	profiler.Observe(second, map[string]any{
		"hero": map[string]any{
			"team2": map[string]any{
				"player0": map[string]any{
					"name": "npc_dota_hero_axe",
					"xpos": float64(110),
					"ypos": float64(210),
				},
			},
		},
	})

	snapshot := profiler.Snapshot()
	if snapshot.SnapshotCount != 2 {
		t.Fatalf("snapshot count = %d, want 2", snapshot.SnapshotCount)
	}

	xpos := findField(t, snapshot.Fields, "hero.team2.player0.xpos")
	if xpos.SeenCount != 2 || xpos.NullCount != 0 {
		t.Fatalf("xpos counts = seen %d null %d, want seen 2 null 0", xpos.SeenCount, xpos.NullCount)
	}
	if xpos.Sample != float64(100) {
		t.Fatalf("xpos sample = %#v, want 100", xpos.Sample)
	}

	ypos := findField(t, snapshot.Fields, "hero.team2.player0.ypos")
	if ypos.SeenCount != 2 || ypos.NullCount != 1 {
		t.Fatalf("ypos counts = seen %d null %d, want seen 2 null 1", ypos.SeenCount, ypos.NullCount)
	}
}

func TestWriteSummaryClassifiesCoreAvailability(t *testing.T) {
	profiler := profile.NewProfiler()
	profiler.Observe(time.Date(2026, 7, 5, 14, 30, 0, 0, time.UTC), completePayloadWithWardStats())

	summaryPath := filepath.Join(t.TempDir(), "session_summary.md")
	if err := profile.WriteSummary(summaryPath, profiler.Snapshot()); err != nil {
		t.Fatalf("WriteSummary returned error: %v", err)
	}

	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := string(data)
	for _, want := range []string{
		"Ten-player hero/player data: available",
		"Hero positions: available",
		"Economy fields: available",
		"Item fields: available",
		"Ability fields: available",
		"Building/Roshan/map fields: available",
		"Ward-related fields: available",
		"Exact ward coordinates: not available",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestWriteSummaryRecognizesDotaSpectatorPlayerSlots(t *testing.T) {
	profiler := profile.NewProfiler()
	payload := map[string]any{
		"hero": map[string]any{
			"team2": map[string]any{},
			"team3": map[string]any{},
		},
		"player": map[string]any{
			"team2": map[string]any{},
			"team3": map[string]any{},
		},
	}
	for player := 0; player < 10; player++ {
		team := "team2"
		if player >= 5 {
			team = "team3"
		}
		slot := "player" + string(rune('0'+player))
		payload["hero"].(map[string]any)[team].(map[string]any)[slot] = map[string]any{
			"name": "npc_dota_hero_axe",
			"xpos": float64(100 + player),
			"ypos": float64(200 + player),
		}
		payload["player"].(map[string]any)[team].(map[string]any)[slot] = map[string]any{
			"name": "player",
			"gold": float64(500 + player),
		}
	}

	profiler.Observe(time.Date(2026, 7, 7, 14, 47, 46, 0, time.UTC), payload)

	summary := profile.RenderSummary(profiler.Snapshot())
	if !strings.Contains(summary, "Ten-player hero/player data: available") {
		t.Fatalf("summary did not recognize team2 player0-4 plus team3 player5-9:\n%s", summary)
	}
}

func findField(t *testing.T, fields []profile.Field, path string) profile.Field {
	t.Helper()
	for _, field := range fields {
		if field.Path == path {
			return field
		}
	}
	t.Fatalf("field %q not found in %#v", path, fields)
	return profile.Field{}
}

func completePayloadWithWardStats() map[string]any {
	hero := map[string]any{"team2": map[string]any{}, "team3": map[string]any{}}
	player := map[string]any{"team2": map[string]any{}, "team3": map[string]any{}}
	for _, team := range []string{"team2", "team3"} {
		for i := 0; i < 5; i++ {
			slot := "player" + string(rune('0'+i))
			hero[team].(map[string]any)[slot] = map[string]any{
				"name":  "npc_dota_hero_axe",
				"xpos":  float64(100 + i),
				"ypos":  float64(200 + i),
				"level": float64(12),
			}
			player[team].(map[string]any)[slot] = map[string]any{
				"gold":      float64(500),
				"net_worth": float64(9000),
				"gpm":       float64(420),
				"xpm":       float64(560),
				"wards":     float64(2),
			}
		}
	}

	return map[string]any{
		"provider":  map[string]any{"name": "Dota 2"},
		"map":       map[string]any{"game_time": float64(123), "roshan_state": "alive"},
		"hero":      hero,
		"player":    player,
		"items":     map[string]any{"team2": map[string]any{"player0": map[string]any{"slot0": map[string]any{"name": "item_blink"}}}},
		"abilities": map[string]any{"team2": map[string]any{"player0": map[string]any{"ability0": map[string]any{"name": "axe_berserkers_call"}}}},
		"buildings": map[string]any{"radiant": map[string]any{"dota_goodguys_tower1_top": float64(2048)}},
	}
}
