package capture

import (
	"bytes"
	"encoding/json"
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
	if p.Gold.State != contracts.ValuePresent || p.Gold.Value == nil || *p.Gold.Value != 0 {
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
}
