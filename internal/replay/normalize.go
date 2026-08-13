package replay

import (
	"fmt"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
)

// ParticipantMapping is one upstream-supplied participant identity: a roster
// person bound to a team and the hero they played in this match. It is the
// correlation input that lets the normalizer move a match from quarantined to
// verified without fabricating identity. When no mapping is supplied for a
// parsed hero, that participant stays unbound and the match is quarantined.
type ParticipantMapping struct {
	PersonID string
	TeamID   string
	HeroName string
	Role     string
	Slot     int
}

// NormalizeMeta is the acquisition/metadata side of one match: the public
// facts the adapter correlated against the parsed demo. None of these come
// from the replay bytes alone; they come from Steam/OpenDota public metadata
// and are recorded so identity correlation is explicit, not assumed.
type NormalizeMeta struct {
	MatchID         string
	ReplaySHA256    string
	SourceEventTime time.Time
	PatchID         string
	RadiantTeamID   string
	DireTeamID      string
	RadiantWin      *bool
	GameBuild       uint32
	DurationSeconds *int64
}

// Normalize maps a parsed ReplayFactsV1 plus public metadata into a
// history.NormalizedMatchFacts. It preserves manta's completeness limits:
// only combat-log-derivable per-hero scalars (kills, deaths) are populated;
// assists, GPM, XPM, last-hits, denies, net-worth, level, farm checkpoints,
// and key item timings need entity state and remain nil (missing), never
// fabricated. Identity is quarantined unless a full 10-hero participant
// mapping is supplied and every parsed hero is bound; a single unbound or
// mismatched hero quarantines the whole match so M1 never publishes
// uncorrelated facts.
func Normalize(facts *ReplayFactsV1, meta NormalizeMeta, mapping []ParticipantMapping) (*history.NormalizedMatchFacts, error) {
	if facts == nil {
		return nil, fmt.Errorf("replay: normalize: nil facts")
	}
	if meta.MatchID == "" || meta.SourceEventTime.IsZero() || meta.PatchID == "" {
		return nil, fmt.Errorf("replay: normalize: incomplete metadata")
	}
	if len(meta.ReplaySHA256) != 64 {
		return nil, fmt.Errorf("replay: normalize: replay sha256 required for content identity")
	}

	available := history.FactsAvailability{
		Available:   []string{"match_header", "game_build", history.MetricKills, history.MetricDeaths, history.MetricGames, history.MetricWins},
		Deferred:    []string{history.MetricAssists, history.MetricGPM, history.MetricXPM, history.MetricLastHits, history.MetricDenies, history.MetricNetWorth, history.MetricLevel, history.MetricKillParticipation, history.MetricFarmCheckpoint, history.MetricKeyItemTiming},
		Unavailable: []string{"replay_salt_or_gc_credentials", "hidden_fog_of_war_state"},
	}

	parsedHeroes := map[string]history.ParticipantFacts{}
	var orderedHeroes []string
	for _, h := range facts.Heroes {
		parsedHeroes[h.Name] = history.ParticipantFacts{
			HeroName: h.Name,
			Kills:    int64Ptr(int64(h.HeroKills)),
			Deaths:   int64Ptr(int64(h.Deaths)),
		}
		orderedHeroes = append(orderedHeroes, h.Name)
	}

	identityStatus := contracts.IdentityQuarantined
	var participants []history.ParticipantFacts

	// A replay is identity-verified only when the public metadata and the
	// parsed demo agree on the game build AND a complete ten-player mapping
	// binds ten unique heroes to ten unique people in ten unique slots. A
	// partial mapping, a duplicate hero/person/slot, an unknown hero, or a
	// build mismatch quarantines the whole match so M1 never publishes
	// uncorrelated or wrong-replay facts.
	buildCorrelated := meta.GameBuild != 0 && facts.Meta.GameBuild != 0 && meta.GameBuild == facts.Meta.GameBuild
	const requiredBindings = 10
	if buildCorrelated && len(facts.Heroes) == requiredBindings && len(parsedHeroes) == requiredBindings && len(mapping) == requiredBindings {
		heroesSet := map[string]bool{}
		personsSet := map[string]bool{}
		slotsSet := map[int]bool{}
		participants = make([]history.ParticipantFacts, 0, requiredBindings)
		ok := true
		for _, m := range mapping {
			base, heroFound := parsedHeroes[m.HeroName]
			if !heroFound || m.PersonID == "" || m.TeamID == "" || m.Role != "player" || m.Slot < 0 || m.Slot >= requiredBindings {
				ok = false
				break
			}
			expectedTeam := meta.RadiantTeamID
			if m.Slot >= 5 {
				expectedTeam = meta.DireTeamID
			}
			if expectedTeam == "" || m.TeamID != expectedTeam {
				ok = false
				break
			}
			if heroesSet[m.HeroName] || personsSet[m.PersonID] || slotsSet[m.Slot] {
				ok = false // duplicate binding — quarantine, do not silently dedupe
				break
			}
			heroesSet[m.HeroName] = true
			personsSet[m.PersonID] = true
			slotsSet[m.Slot] = true
			participants = append(participants, history.ParticipantFacts{
				PersonID: m.PersonID, TeamID: m.TeamID, HeroName: m.HeroName,
				Role: m.Role, Slot: m.Slot, Kills: base.Kills, Deaths: base.Deaths,
			})
		}
		if ok && len(participants) == requiredBindings {
			identityStatus = contracts.IdentityVerified
		} else {
			participants = nil
		}
	}

	if identityStatus != contracts.IdentityVerified {
		// Quarantined: publish no participant attribution. The parsed heroes
		// are recorded in the spike-level facts artifact keyed by the demo
		// content hash; the normalized facts for a quarantined match carry
		// only metadata + the quarantined identity status so the match is
		// observable but never publish-able. No person id is fabricated.
		participants = nil
	}

	nf := &history.NormalizedMatchFacts{
		SchemaVersion:   history.FactsSchema,
		MatchID:         meta.MatchID,
		ReplaySHA256:    meta.ReplaySHA256,
		SourceEventTime: meta.SourceEventTime,
		PatchID:         meta.PatchID,
		GameBuild:       meta.GameBuild,
		DurationSeconds: meta.DurationSeconds,
		RadiantTeamID:   meta.RadiantTeamID,
		DireTeamID:      meta.DireTeamID,
		RadiantWin:      meta.RadiantWin,
		Participants:    participants,
		IdentityStatus:  identityStatus,
		Availability:    available,
	}
	if err := history.SealNormalizedMatchFacts(nf); err != nil {
		return nil, fmt.Errorf("replay: normalize: seal: %w", err)
	}
	return nf, nil
}

// int64Ptr is a local helper to avoid pulling the test-only helper from the
// history package into production code.
func int64Ptr(v int64) *int64 { return &v }
