package history

import (
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

// buildScope constructs a valid, sealed TournamentScopeV1 with 16 teams and
// 5 players per team for use across the history tests.
func buildScope(t *testing.T) contracts.TournamentScopeV1 {
	t.Helper()
	cutoff := mustParseTime(t, "2026-08-12T00:00:00Z")
	effFrom := mustParseTime(t, "2026-02-13T00:00:00Z")
	effUntil := mustParseTime(t, "2026-08-12T01:00:00Z")
	teams := make([]contracts.TournamentTeamV1, 16)
	participants := make([]contracts.TournamentParticipantV1, 0, 16*5)
	teamIDs := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"}
	for i, id := range teamIDs {
		teamID := "team-" + id
		teams[i] = contracts.TournamentTeamV1{TeamID: teamID, RosterID: "roster-" + id, EffectiveFrom: effFrom, EffectiveUntil: effUntil}
		for j := 0; j < 5; j++ {
			pid := "person-" + id + pidSuffix(j)
			participants = append(participants, contracts.TournamentParticipantV1{
				PersonID: pid, TeamID: teamID, Handle: "player-" + id + pidSuffix(j), Role: "player",
				EffectiveFrom: effFrom, EffectiveUntil: effUntil,
			})
		}
	}
	scope := contracts.TournamentScopeV1{
		SchemaVersion:            contracts.TournamentScopeSchemaV1,
		Edition:                  "ti-2026",
		SampledAt:                cutoff,
		HistoryCutoff:            cutoff,
		DiscoveryContractVersion: DiscoveryContractVersion,
		PatchID:                  "60",
		DotaPatch:                "7.41",
		Teams:                    teams,
		Participants:             participants,
		Sources:                  []contracts.PublicSourceV1{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: cutoff}},
		Discovery: contracts.DiscoveryPolicyV1{
			ContractVersion:         DiscoveryContractVersion,
			Providers:               []string{ProviderOpenDota, ProviderSteam},
			PageLimit:               100,
			FullHistoryReplayTarget: 100,
			MinimumTeamMatches:      5,
			AllowedOutcomes:         []string{ReadinessFullHistoryGo, ReadinessHistoricalNoGo, ReadinessRestrictedGo},
		},
	}
	if err := contracts.SealTournamentScopeV1(&scope); err != nil {
		t.Fatalf("seal scope: %v", err)
	}
	return scope
}

func pidSuffix(j int) string {
	return string(rune('0' + j))
}

func buildRoster(t *testing.T, scope contracts.TournamentScopeV1) RosterManifestV1 {
	t.Helper()
	roster := RosterManifestV1{
		SchemaVersion:      RosterSchema,
		TournamentScopeID:  scope.ScopeID,
		TournamentScopeSHA: scope.ContentSHA256,
		Edition:            scope.Edition,
		SampledAt:          scope.SampledAt,
		EffectiveCutoff:    scope.HistoryCutoff,
		Sources:            []ProvenanceRef{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: scope.SampledAt}},
	}
	for _, team := range scope.Teams {
		roster.Teams = append(roster.Teams, RosterTeam{
			TeamID: team.TeamID, RosterID: team.RosterID, Handle: "Team " + team.TeamID,
			Aliases: []string{"T" + team.TeamID}, EffectiveFrom: team.EffectiveFrom, EffectiveUntil: team.EffectiveUntil,
			Provenance: []ProvenanceRef{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: scope.SampledAt}},
		})
	}
	for _, p := range scope.Participants {
		roster.Players = append(roster.Players, RosterPlayer{
			PersonID: p.PersonID, TeamID: p.TeamID, Handle: p.Handle, Aliases: []string{p.Handle + "-alt"},
			Role: p.Role, EffectiveFrom: p.EffectiveFrom, EffectiveUntil: p.EffectiveUntil,
			Provenance: []ProvenanceRef{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: scope.SampledAt}},
		})
	}
	if err := SealRosterManifestV1(&roster); err != nil {
		t.Fatalf("seal roster: %v", err)
	}
	return roster
}

func TestRosterManifestSealAndBinding(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	if err := roster.ValidateAgainstScope(scope); err != nil {
		t.Fatalf("roster binding: %v", err)
	}
	// Mutation invalidates the content hash.
	roster.Players[0].Handle = "tampered"
	if err := roster.Validate(); err == nil {
		t.Fatalf("expected tampered roster to fail validation")
	}
}

func TestRosterManifestRejectsTooFewPlayers(t *testing.T) {
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	roster.Players = roster.Players[:len(roster.Players)-1]
	if err := SealRosterManifestV1(&roster); err == nil {
		t.Fatalf("expected seal to reject roster with a team below five players")
	}
}