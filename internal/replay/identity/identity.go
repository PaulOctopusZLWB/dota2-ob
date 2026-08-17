// Package identity verifies the replay's self-asserted match identity and
// binds all ten participants before any analytical output is published. Any
// mismatch fails closed; the match is quarantined with an auditable reason.
package identity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/herolookup"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// SteamID64Offset converts a 64-bit Steam ID to the 32-bit account id.
const SteamID64Offset uint64 = 76561197960265728

// State values for an Identity record.
const (
	StateVerified  = "verified"
	StateFailed    = "fail_closed"
	StateUnavailable = "unavailable"
)

// Participant is one verified participant binding.
type Participant struct {
	Slot       int32  `json:"slot"`
	AccountID  string `json:"account_id"`
	SteamID64  uint64 `json:"steam_id64"`
	PlayerName string `json:"player_name"`
	HeroName   string `json:"hero_name"`
	HeroID     int32  `json:"hero_id"`
	Team       int32  `json:"team"`
	Side       string `json:"side"`
}

// Team is one verified team binding.
type Team struct {
	TeamID       string `json:"team_id"`
	TeamName     string `json:"team_name"`
	Tag          string `json:"tag"`
	Side         string `json:"side"`
	TournamentID *uint32 `json:"tournament_id"`
}

// Identity is the verification record.
type Identity struct {
	SchemaVersion string        `json:"schema_version"`
	MatchID       string        `json:"match_id"`
	State         string        `json:"state"`
	Reason        string        `json:"reason"`
	GameBuild     uint32        `json:"game_build"`
	GameMode      int32         `json:"game_mode"`
	GameWinner    int32         `json:"game_winner"`
	LeagueID      uint32        `json:"league_id"`
	HeaderServer  string        `json:"header_server"`
	Teams         []Team        `json:"teams"`
	Participants  []Participant `json:"participants"`
	GateMatch     bool          `json:"gate_match"`
	GateBuild     bool          `json:"gate_build"`
	GateTeams     bool          `json:"gate_teams"`
	GateParticipants bool       `json:"gate_participants"`
	Mismatches    []string      `json:"mismatches"`
	Missing       []string      `json:"missing_inputs"`
}

// Build verifies the raw summary against the frozen manifest entry. teamByNum
// maps numeric team ids (2=radiant, 3=dire) to side names.
func Build(s *raw.Summary, mt *archive.Match) *Identity {
	id := &Identity{
		SchemaVersion: version.IdentitySchema,
		MatchID:       mt.MatchID,
		State:         StateFailed,
		Reason:        "identity_gate_pending",
		Teams:         []Team{},
		Participants:  []Participant{},
		Mismatches:    []string{},
		Missing:       []string{},
	}

	if s.FileInfo == nil {
		id.Reason = "file_info_missing"
		return id
	}

	// Gate 1: match id.
	want, err := strconv.ParseUint(mt.MatchID, 10, 64)
	if err != nil {
		id.Mismatches = append(id.Mismatches, "manifest_match_id_unparseable")
		id.Reason = "manifest_match_id_unparseable"
		return id
	}
	id.GateMatch = s.FileInfo.MatchID == want
	if !id.GateMatch {
		id.Mismatches = append(id.Mismatches,
			fmt.Sprintf("match_id: replay=%d manifest=%d", s.FileInfo.MatchID, want))
		id.Reason = "match_id_mismatch"
		return id
	}

	// Gate 2: build. The build is extracted by manta from the game dir and
	// reported in the parse-done event.
	build := uint32(0)
	if s.ParseDone != nil {
		build = s.ParseDone.GameBuild
	}
	if build == 0 {
		id.Missing = append(id.Missing, "game_build")
		id.Reason = "build_missing"
		return id
	}
	id.GameBuild = build
	id.GateBuild = true
	if s.Engine == nil {
		id.Missing = append(id.Missing, "engine_info")
	}

	id.GameMode = s.FileInfo.GameMode
	id.GameWinner = s.FileInfo.GameWinner
	id.LeagueID = s.FileInfo.LeagueID
	if s.Header != nil {
		id.HeaderServer = s.Header.ServerName
	}

	// Gate 3: teams. Build the side map from the manifest.
	sideByTeamID := map[string]string{}
	for _, et := range mt.ExpectedTeams {
		sideByTeamID[et.TeamID] = et.Side
	}
	teamNameByID := map[string]string{}
	for _, et := range mt.ExpectedTeams {
		teamNameByID[et.TeamID] = et.TeamName
	}

	// Authoritative replay team ids come from the demo file info (radiant and
	// dire team ids). CDOTATeam entities carry the tournament id but the
	// numeric team field is not reliably populated, so the entity stream is
	// used only for corroboration and display names, never for side.
	teamIDs := []string{}
	fileTeams := map[string]string{} // team id -> side
	if s.FileInfo.RadiantTeamID != 0 {
		rid := strconv.FormatUint(uint64(s.FileInfo.RadiantTeamID), 10)
		fileTeams[rid] = "radiant"
		teamIDs = append(teamIDs, rid)
	}
	if s.FileInfo.DireTeamID != 0 {
		did := strconv.FormatUint(uint64(s.FileInfo.DireTeamID), 10)
		fileTeams[did] = "dire"
		teamIDs = append(teamIDs, did)
	}
	// Corroborate with entity team states (tournament id is authoritative).
	entityTeams := map[string]string{}
	for _, ts := range s.Teams {
		if ts.TournamentID != nil && *ts.TournamentID != 0 {
			tid := strconv.FormatUint(uint64(*ts.TournamentID), 10)
			if side, ok := fileTeams[tid]; ok {
				entityTeams[tid] = side
			} else {
				entityTeams[tid] = "entity_only"
			}
		}
	}
	if len(entityTeams) > 0 {
		for tid := range entityTeams {
			if !contains(teamIDs, tid) {
				teamIDs = append(teamIDs, tid)
			}
		}
	}

	sort.Strings(teamIDs)
	var gateTeamsOK = true
	for _, tid := range teamIDs {
		side, ok := sideByTeamID[tid]
		if !ok {
			id.Mismatches = append(id.Mismatches, fmt.Sprintf("team %s not in manifest", tid))
			gateTeamsOK = false
			continue
		}
		if fs, ok := fileTeams[tid]; ok && fs != side {
			id.Mismatches = append(id.Mismatches, fmt.Sprintf("team %s side replay=%s manifest=%s", tid, fs, side))
			gateTeamsOK = false
		}
		teamName := teamNameByID[tid]
		// Prefer entity name for display.
		if ts, ok := s.Teams[teamNumForID(s, tid)]; ok && ts.Name != "" && ts.Name != "Unassigned" && ts.Name != "Spectator" {
			teamName = ts.Name
		}
		var tidU32 *uint32
		if v, err := strconv.ParseUint(tid, 10, 32); err == nil {
			u := uint32(v)
			tidU32 = &u
		}
		id.Teams = append(id.Teams, Team{
			TeamID:       tid,
			TeamName:     teamName,
			Tag:          teamTag(s, tid),
			Side:         side,
			TournamentID: tidU32,
		})
	}
	// Both manifest teams must be present.
	if len(teamIDs) < 2 {
		id.Mismatches = append(id.Mismatches, fmt.Sprintf("only %d distinct teams found, want 2", len(teamIDs)))
		gateTeamsOK = false
	}
	id.GateTeams = gateTeamsOK

	// Gate 4: participants.
	id.GateParticipants = buildParticipants(id, s.FileInfo.Players, mt)
	if !id.GateParticipants {
		id.Reason = "participant_binding_mismatch"
		return id
	}

	if !id.GateTeams {
		id.Reason = "team_binding_mismatch"
		return id
	}

	id.State = StateVerified
	id.Reason = "all_identity_gates_pass"
	return id
}

func buildParticipants(id *Identity, players []raw.PlayerInfo, mt *archive.Match) bool {
	if len(players) != 10 {
		id.Mismatches = append(id.Mismatches, fmt.Sprintf("player_info has %d entries, want 10", len(players)))
		return false
	}

	expected := map[string]*archive.ExpectedPlayer{} // account_id -> expected
	for i := range mt.ExpectedParticipants {
		p := &mt.ExpectedParticipants[i]
		expected[p.AccountID] = p
	}

	seen := map[string]bool{}
	ok := true
	for i, pi := range players {
		if pi.IsFakeClient {
			id.Mismatches = append(id.Mismatches, fmt.Sprintf("player %d is a fake client", i))
			ok = false
			continue
		}
		account := strconv.FormatUint(pi.SteamID-SteamID64Offset, 10)
		if seen[account] {
			id.Mismatches = append(id.Mismatches, fmt.Sprintf("duplicate account_id %s", account))
			ok = false
		}
		seen[account] = true
		heroID, heroOK := herolookup.NameToHeroID[pi.HeroName]
		p := Participant{
			Slot:       int32(i),
			AccountID:  account,
			SteamID64:  pi.SteamID,
			PlayerName: pi.PlayerName,
			HeroName:   pi.HeroName,
			HeroID:     heroID,
			Team:       pi.GameTeam,
			Side:       sideName(pi.GameTeam),
		}
		id.Participants = append(id.Participants, p)

		exp, found := expected[account]
		if !found {
			id.Mismatches = append(id.Mismatches, fmt.Sprintf("account %s not in manifest", account))
			ok = false
			continue
		}
		if !heroOK {
			id.Mismatches = append(id.Mismatches, fmt.Sprintf("account %s hero %q unknown to frozen hero registry", account, pi.HeroName))
			ok = false
			continue
		}
		if int(heroID) != exp.ExpectedHeroID {
			id.Mismatches = append(id.Mismatches,
				fmt.Sprintf("account %s hero replay=%d(%s) manifest=%d(%s)", account, heroID, pi.HeroName, exp.ExpectedHeroID, heroNameFor(exp.ExpectedHeroID)))
			ok = false
			continue
		}
		if p.Side != exp.Side {
			id.Mismatches = append(id.Mismatches,
				fmt.Sprintf("account %s side replay=%s manifest=%s", account, p.Side, exp.Side))
			ok = false
		}
	}
	if len(seen) != 10 {
		id.Mismatches = append(id.Mismatches, fmt.Sprintf("only %d unique participants, want 10", len(seen)))
		ok = false
	}
	return ok
}

func sideName(team int32) string {
	switch team {
	case 2:
		return "radiant"
	case 3:
		return "dire"
	case 0:
		return "none"
	default:
		return "spectator"
	}
}

func heroNameFor(id int) string {
	return herolookup.HeroIDByName[int32(id)]
}

func teamNumForID(s *raw.Summary, tid string) int32 {
	for k, ts := range s.Teams {
		if ts.TournamentID != nil && strconv.FormatUint(uint64(*ts.TournamentID), 10) == tid {
			return k
		}
	}
	return 0
}

func teamTag(s *raw.Summary, tid string) string {
	for _, ts := range s.Teams {
		if ts.TournamentID != nil && strconv.FormatUint(uint64(*ts.TournamentID), 10) == tid {
			return ts.Tag
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// CanonicalJSON returns the deterministic encoding.
func (i *Identity) CanonicalJSON() ([]byte, error) { return json.Marshal(i) }