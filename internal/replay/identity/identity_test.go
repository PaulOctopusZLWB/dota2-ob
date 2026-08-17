package identity

import (
	"strconv"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
)

// accountFor converts a zero-based player index to a numeric account id string.
func accountFor(i int) string {
	return strconv.Itoa(1000 + i)
}

// makePlayers builds 10 consistent player bindings (hero id 1 radiant, hero id
// 2 dire) with account ids 1000..1009.
func makePlayers() []raw.PlayerInfo {
	out := make([]raw.PlayerInfo, 0, 10)
	for i := 0; i < 10; i++ {
		hero := 1
		team := int32(2)
		if i >= 5 {
			hero = 2
			team = 3
		}
		out = append(out, raw.PlayerInfo{
			HeroName:   heroName(hero),
			PlayerName: "p" + strconv.Itoa(i),
			SteamID:    SteamID64Offset + uint64(1000+i),
			GameTeam:   team,
		})
	}
	return out
}

func manifestFor() *archive.Match {
	mt := &archive.Match{MatchID: "1000000001", ExpectedTeams: []archive.ExpectedTeam{
		{TeamID: "726228", TeamName: "VG", Side: "radiant"},
		{TeamID: "10149530", TeamName: "HLG", Side: "dire"},
	}}
	for i := 0; i < 10; i++ {
		hero := 1
		side := "radiant"
		if i >= 5 {
			hero = 2
			side = "dire"
		}
		mt.ExpectedParticipants = append(mt.ExpectedParticipants, archive.ExpectedPlayer{
			AccountID: accountFor(i), Side: side, ExpectedHeroID: hero,
		})
	}
	return mt
}

func summaryFor(players []raw.PlayerInfo) *raw.Summary {
	return &raw.Summary{
		FileInfo: &raw.FileInfo{
			MatchID:       1000000001,
			RadiantTeamID: 726228,
			DireTeamID:    10149530,
			Players:       players,
		},
		Teams:     map[int32]*raw.TeamState{},
		ParseDone: &raw.ParseDone{GameBuild: 6902},
	}
}

func heroName(id int) string {
	switch id {
	case 1:
		return "npc_dota_hero_antimage"
	case 2:
		return "npc_dota_hero_axe"
	}
	return ""
}

func TestBuildVerified(t *testing.T) {
	id := Build(summaryFor(makePlayers()), manifestFor())
	if id.State != StateVerified {
		t.Fatalf("state=%s reason=%s mismatches=%v", id.State, id.Reason, id.Mismatches)
	}
	if !id.GateMatch || !id.GateTeams || !id.GateParticipants || !id.GateBuild {
		t.Fatalf("gates: %+v", id)
	}
	if len(id.Participants) != 10 {
		t.Fatalf("participants=%d", len(id.Participants))
	}
}

func TestBuildMatchIDMismatch(t *testing.T) {
	mt := manifestFor()
	s := summaryFor(makePlayers())
	s.FileInfo.MatchID = 999999
	id := Build(s, mt)
	if id.State != StateFailed || id.GateMatch {
		t.Fatalf("state=%s gate_match=%v", id.State, id.GateMatch)
	}
}

func TestBuildMissingParticipant(t *testing.T) {
	players := makePlayers()[:9] // only 9
	id := Build(summaryFor(players), manifestFor())
	if id.State != StateFailed || id.GateParticipants {
		t.Fatalf("state=%s gate_participants=%v", id.State, id.GateParticipants)
	}
}

func TestBuildDuplicateParticipant(t *testing.T) {
	players := makePlayers()
	players[1].SteamID = players[0].SteamID
	id := Build(summaryFor(players), manifestFor())
	if id.State != StateFailed || id.GateParticipants {
		t.Fatalf("state=%s gate_participants=%v", id.State, id.GateParticipants)
	}
}

func TestBuildHeroMismatch(t *testing.T) {
	players := makePlayers()
	for i := 5; i < 10; i++ {
		players[i].HeroName = "npc_dota_hero_antimage" // dire expects axe
	}
	id := Build(summaryFor(players), manifestFor())
	if id.State == StateVerified {
		t.Fatalf("state=verified with hero mismatch")
	}
}

func TestBuildTeamMismatch(t *testing.T) {
	mt := manifestFor()
	s := summaryFor(makePlayers())
	s.FileInfo.RadiantTeamID = 555
	id := Build(s, mt)
	if id.State == StateVerified {
		t.Fatalf("state=verified with team mismatch")
	}
}

func TestBuildFakeClient(t *testing.T) {
	players := makePlayers()
	players[2].IsFakeClient = true
	id := Build(summaryFor(players), manifestFor())
	if id.State == StateVerified {
		t.Fatalf("state=verified with fake client")
	}
}

func TestAccountIDConversion(t *testing.T) {
	steamID := uint64(76561198000000000)
	account := steamID - SteamID64Offset
	if account != 39734272 {
		t.Fatalf("account=%d", account)
	}
}
