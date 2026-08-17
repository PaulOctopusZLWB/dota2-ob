package episodes

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
)

// factLine builds a facts.Reader from a sequence of Fact structs.
func factReader(t *testing.T, facts2 []facts.Fact) *facts.Reader {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, f := range facts2 {
		if err := enc.Encode(&f); err != nil {
			t.Fatal(err)
		}
	}
	return facts.NewReader(&buf)
}

func TestBuildDeathRoundsAndFights(t *testing.T) {
	items := []facts.Fact{
		{Seq: 1, Family: facts.FamilyDeathRespawn, GameSecond: 100, SourceSeq: 11, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1"})},
		{Seq: 2, Family: facts.FamilyDeathRespawn, GameSecond: 102, SourceSeq: 12, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a2"})},
		{Seq: 3, Family: facts.FamilyDeathRespawn, GameSecond: 105, SourceSeq: 13, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "respawn", AccountID: "a1"})},
		{Seq: 4, Family: facts.FamilyDeathRespawn, GameSecond: 500, SourceSeq: 14, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "b1"})},
		{Seq: 5, Family: facts.FamilyDeathRespawn, GameSecond: 505, SourceSeq: 15, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "b1"})},
	}
	out, err := Build("1000000001", map[string]string{"a1": "p1"}, factReader(t, items))
	if err != nil {
		t.Fatal(err)
	}
	// Expect 2 death rounds (a1 respawn, b1 buyback) + 1 fight (2-death cluster).
	var deathRounds, fights int
	for _, e := range out.Episodes {
		switch e.Kind {
		case KindDeathRound:
			deathRounds++
		case KindFight:
			fights++
		}
	}
	if deathRounds != 2 {
		t.Fatalf("death rounds=%d want 2: %+v", deathRounds, out.Episodes)
	}
	if fights != 1 {
		t.Fatalf("fights=%d want 1", fights)
	}
	// Determinism: second build is identical.
	out2, err := Build("1000000001", map[string]string{"a1": "p1"}, factReader(t, items))
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out)) != string(mustJSON(out2)) {
		t.Fatal("episodes not deterministic")
	}
}

func TestObjectiveAndUnavailable(t *testing.T) {
	items := []facts.Fact{
		{Seq: 1, Family: facts.FamilyObjective, GameSecond: 600, SourceSeq: 21, Payload: mustJSON(&facts.ObjectiveFact{BuildingName: "badguys_tower1_mid", BuildingKind: "tower", Team: "dire", IsRealBuilding: true, Attackers: []string{"a1"}})},
		{Seq: 2, Family: facts.FamilyObjective, GameSecond: 601, SourceSeq: 22, Payload: mustJSON(&facts.ObjectiveFact{BuildingName: "nonsense", BuildingKind: "other", IsRealBuilding: false})},
	}
	out, err := Build("1000000001", nil, factReader(t, items))
	if err != nil {
		t.Fatal(err)
	}
	objCount := 0
	for _, e := range out.Episodes {
		if e.Kind == KindObjective {
			objCount++
		}
	}
	if objCount != 1 {
		t.Fatalf("objective episodes=%d want 1", objCount)
	}
	// Unavailable families present with reason codes (never inferred).
	if len(out.Unavailable) == 0 {
		t.Fatal("expected unavailable episode families")
	}
	for _, u := range out.Unavailable {
		if u.Reason == "" {
			t.Fatalf("unavailable family %s has empty reason", u.Kind)
		}
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
