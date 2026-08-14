package v3fixture_test

import (
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture/v3fixture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestMaximumRelevantBodyIsExactLimitAndFullyProduced(t *testing.T) {
	body, err := v3fixture.MaximumRelevantBody()
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 10<<20 {
		t.Fatalf("bytes=%d", len(body))
	}
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("maximum"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Append(body)
	if err != nil {
		t.Fatal(err)
	}
	if record.ProjectionResult != session.ProjectionProduced {
		t.Fatalf("projection=%s/%s", record.ProjectionCode, record.ProjectionReason)
	}
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Participants) != 10 || len(observation.Buildings) != 64 {
		t.Fatalf("participants/buildings=%d/%d", len(observation.Participants), len(observation.Buildings))
	}
	for _, participant := range observation.Participants {
		if len(participant.Items) != 32 || len(participant.Abilities) != 32 {
			t.Fatalf("items/abilities=%d/%d", len(participant.Items), len(participant.Abilities))
		}
	}
}
