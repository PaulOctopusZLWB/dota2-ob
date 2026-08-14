package session_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestProjectionBoundsUseFinalDuplicateValuesAndFixedPrecedence(t *testing.T) {
	participants := make([]string, 11)
	for i := range participants {
		participants[i] = fmt.Sprintf(`"p%d":{"gold":1}`, i)
	}
	over := `{"player":{"team2":{` + strings.Join(participants, ",") + `}},"buildings":{"radiant":{` + manyEntries("tower", 65, `{"health":1}`) + `}}}`
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("bounds"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Append([]byte(over))
	if err != nil {
		t.Fatal(err)
	}
	if record.ProjectionResult != session.ProjectionConsumedNoOutput || record.ProjectionCode != "gsi_projection_bounds_exceeded" || record.ProjectionReason != "participant_count" {
		t.Fatalf("classification=%#v", record)
	}

	// An earlier out-of-bounds duplicate is replaced by the final in-bounds object.
	finalWins := `{"player":{"team2":{` + strings.Join(participants, ",") + `}},"player":{"team2":{"p0":{"gold":2}}}}`
	store2, _ := session.NewStore(t.TempDir(), session.WithSessionID("duplicates"))
	record, err = store2.Append([]byte(finalWins))
	if err != nil {
		t.Fatal(err)
	}
	if record.ProjectionResult != session.ProjectionProduced {
		t.Fatalf("final duplicate did not replace bounds failure: %#v", record)
	}
}

func TestProjectionBoundsAllStableReasons(t *testing.T) {
	cases := []struct{ name, body, reason string }{
		{"structural-teams", `{"player":{` + manyEntries("t", 12, `{}`) + `}}`, "participant_count"},
		{"items", `{"items":{"t":{"p":{` + manyEntries("i", 33, `{"name":"x"}`) + `}}}}`, "item_count"},
		{"abilities", `{"abilities":{"t":{"p":{` + manyEntries("a", 33, `{"name":"x"}`) + `}}}}`, "ability_count"},
		{"buildings", `{"buildings":{"t":{` + manyEntries("b", 65, `{"health":1}`) + `}}}`, "building_count"},
		{"identifier", `{"player":{"` + strings.Repeat("t", 129) + `":{"p":{}}}}`, "identifier_bytes"},
		{"string", `{"provider":{"name":"` + strings.Repeat("x", 257) + `"}}`, "string_bytes"},
		{"number", `{"provider":{"timestamp":` + strings.Repeat("1", 129) + `}}`, "number_bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := session.NewStore(t.TempDir(), session.WithSessionID("reason"))
			r, err := store.Append([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if r.ProjectionResult != session.ProjectionConsumedNoOutput || r.ProjectionReason != tc.reason {
				t.Fatalf("record=%#v", r)
			}
		})
	}
}

func TestProjectionDuplicateKeysAreFinalLastWinsAtEveryProjectedLevel(t *testing.T) {
	tooLong := strings.Repeat("x", 257)
	body := `{"provider":{"name":"` + tooLong + `","name":"ok"},` +
		`"player":{"t":{"p":{"gold":1},"p":{"gold":2}},"t":{"p":{"gold":3}}},` +
		`"items":{"t":{"p":{"i":{"name":"` + tooLong + `","name":"old"},"i":{"name":"new"}}}},` +
		`"abilities":{"t":{"p":{"a":{"name":"old","name":"new"}}}},` +
		`"buildings":{"t":{"b":{"health":1,"health":2}}}}`
	store, _ := session.NewStore(t.TempDir(), session.WithSessionID("nested-duplicates"))
	record, err := store.Append([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if record.ProjectionResult != session.ProjectionProduced {
		t.Fatalf("classification=%#v", record)
	}
	root := record.Payload.(map[string]any)
	if root["provider"].(map[string]any)["name"] != "ok" {
		t.Fatalf("provider=%#v", root["provider"])
	}
	if root["items"].(map[string]any)["t"].(map[string]any)["p"].(map[string]any)["i"].(map[string]any)["name"] != "new" {
		t.Fatalf("items=%#v", root["items"])
	}
}

func manyEntries(prefix string, count int, value string) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"%s%d":%s`, prefix, i, value)
	}
	return strings.Join(parts, ",")
}
