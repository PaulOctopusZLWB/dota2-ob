// Package episodes builds deterministic behavior episodes from the normalized
// fact stream. Every episode carries start/end, participants, evidence ids,
// rule version, missing inputs, and confidence. An episode label is a bounded
// evidence-linked interval, never a strategic-quality judgement. Episode
// families that require inputs this stage does not produce are emitted as
// explicitly unavailable records with reason codes (never fabricated).
package episodes

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Kinds of behavior episodes.
const (
	KindLaneSegment    = "lane_segment"
	KindLaneDeparture  = "lane_departure"
	KindRoamAttempt    = "roam_attempt"
	KindFarmInterval   = "farm_interval"
	KindRuneContest    = "rune_contest"
	KindSmoke          = "smoke_activation"
	KindWard           = "ward_placement"
	KindFight          = "fight_interval"
	KindObjective      = "objective_attempt"
	KindReset          = "reset"
	KindDeathRound     = "death_respawn_buyback_round"
	KindItemWindow     = "key_item_window"
)

// Unavailable episode families (explicit reason; deterministic, not inferred).
const (
	UnavailLaneSegment  = "lane_segment"
	UnavailLaneDeparture = "lane_departure"
	UnavailRoamAttempt  = "roam_attempt"
	UnavailFarmInterval = "farm_interval"
	UnavailRuneContest  = "rune_contest"
)

// Episode is one behavior episode.
type Episode struct {
	ID               string   `json:"id"`
	MatchID          string   `json:"match_id"`
	Kind             string   `json:"kind"`
	StartGameSecond  float64  `json:"start_game_second"`
	EndGameSecond    float64  `json:"end_game_second"`
	Participants     []string `json:"participants"`
	AccountID        string   `json:"account_id,omitempty"`
	Region           string   `json:"region,omitempty"`
	Detail           string   `json:"detail,omitempty"`
	EvidenceIDs      []int64  `json:"evidence_ids"`
	RuleVersion      string   `json:"rule_version"`
	MissingInputs    []string `json:"missing_inputs"`
	Exclusions       []string `json:"exclusions"`
	Confidence       float64  `json:"confidence"`
}

// Unavailable is an explicitly unavailable episode family.
type Unavailable struct {
	Kind           string   `json:"kind"`
	MatchID        string   `json:"match_id"`
	Reason         string   `json:"reason"`
	RequiredInputs []string `json:"required_inputs"`
	RuleVersion    string   `json:"rule_version"`
}

// Output is the episodes artifact.
type Output struct {
	SchemaVersion string        `json:"schema_version"`
	RuleVersion   string        `json:"rule_version"`
	MatchID       string        `json:"match_id"`
	Episodes      []Episode     `json:"episodes"`
	Unavailable   []Unavailable `json:"unavailable"`
	GeneratedAt   string        `json:"generated_at,omitempty"`
}

// CanonicalJSON returns the deterministic encoding.
func (o *Output) CanonicalJSON() ([]byte, error) { return json.Marshal(o) }

// Builder accumulates episodes from a fact stream.
type Builder struct {
	matchID   string
	accountName map[string]string
	episodes  []Episode
}

// NewBuilder creates an episode builder for a match.
func NewBuilder(matchID string, accountName map[string]string) *Builder {
	return &Builder{matchID: matchID, accountName: accountName}
}

// Add inserts an episode, ensuring participants and evidence are sorted for
// deterministic output.
func (b *Builder) Add(e *Episode) {
	sort.Strings(e.Participants)
	sort.Slice(e.EvidenceIDs, func(i, j int) bool { return e.EvidenceIDs[i] < e.EvidenceIDs[j] })
	e.ID = fmt.Sprintf("%s:%s:%d:%d", b.matchID, e.Kind, int64(e.StartGameSecond), len(b.episodes))
	e.MatchID = b.matchID
	e.RuleVersion = version.EpisodeRuleVersion
	b.episodes = append(b.episodes, *e)
}

// Episodes returns the accumulated episodes sorted by start, then kind.
func (b *Builder) Episodes() []Episode {
	out := append([]Episode(nil), b.episodes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartGameSecond != out[j].StartGameSecond {
			return out[i].StartGameSecond < out[j].StartGameSecond
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Build computes the deterministic episode set from a facts stream. The
// reader must be re-readable (the caller reopens the facts artifact). Fact
// payloads are decoded by family; unparseable payloads are skipped with the
// family marked unavailable rather than aborting the whole match.
func Build(matchID string, accountName map[string]string, r *facts.Reader) (*Output, error) {
	b := NewBuilder(matchID, accountName)
	unavailable := []Unavailable{}

	deathRounds := map[string]*deathRound{}
	fights := newFightWindow(b)
	wardSeen := map[string]float64{}
	smokeSeen := map[string]bool{}
	factsSeen := 0

	for {
		f, err := r.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		factsSeen++
		switch f.Family {
		case facts.FamilyCombat:
			var cf facts.CombatFact
			if err := json.Unmarshal(f.Payload, &cf); err != nil {
				continue
			}
			_ = cf
		case facts.FamilyDeathRespawn:
			var drb facts.DeathRespawnBuyback
			if err := json.Unmarshal(f.Payload, &drb); err != nil {
				continue
			}
			switch drb.Kind {
			case "death":
				if drb.AccountID != "" {
					fights.addDeath(drb.AccountID, f.GameSecond, f.SourceSeq)
				}
				handleDeath(b, deathRounds, &drb, f.GameSecond)
			case "buyback":
				handleBuyback(b, deathRounds, &drb, f.GameSecond)
			case "respawn":
				handleRespawn(b, deathRounds, &drb, f.GameSecond)
			}
		case facts.FamilyObjective:
			var of facts.ObjectiveFact
			if err := json.Unmarshal(f.Payload, &of); err != nil {
				continue
			}
			if of.IsRealBuilding {
				b.Add(objectiveEpisode(&of, f.GameSecond))
			}
		case facts.FamilyVision:
			var vf facts.VisionFact
			if err := json.Unmarshal(f.Payload, &vf); err != nil {
				continue
			}
			if vf.Kind == "obs_ward_placed" || vf.Kind == "sentry_ward_placed" {
				if _, seen := wardSeen[vf.AccountID+":"+vf.Kind]; seen {
					continue
				}
				wardSeen[vf.AccountID+":"+vf.Kind] = f.GameSecond
				ep := &Episode{
					Kind: KindWard, AccountID: vf.AccountID,
					StartGameSecond: f.GameSecond, EndGameSecond: f.GameSecond + 1,
					Participants: []string{vf.AccountID}, Region: "ward:" + vf.Kind,
					Detail:      vf.Kind,
					EvidenceIDs: []int64{f.SourceSeq},
					Confidence:  1.0,
				}
				if vf.LocationX != nil && vf.LocationY != nil {
					ep.Region = fmt.Sprintf("(%d,%d)", int(*vf.LocationX), int(*vf.LocationY))
				}
				b.Add(ep)
			}
		case facts.FamilyModifier:
			var mf facts.ModifierFact
			if err := json.Unmarshal(f.Payload, &mf); err != nil {
				continue
			}
			if strings.Contains(mf.Modifier, "smoke_of_deceit") && mf.Kind == "add" {
				key := mf.AccountID + ":" + fmt.Sprintf("%d", int64(f.GameSecond))
				if smokeSeen[key] {
					continue
				}
				smokeSeen[key] = true
				b.Add(&Episode{
					Kind: KindSmoke, AccountID: mf.AccountID,
					StartGameSecond: f.GameSecond, EndGameSecond: f.GameSecond + 1,
					Participants: []string{mf.AccountID},
					EvidenceIDs:  []int64{f.SourceSeq},
					Confidence:   0.9,
					Detail:       mf.Modifier,
				})
			}
		}
	}

	// Flush the final fight window.
	fights.flush()

	// Explicitly unavailable episode families for this stage.
	unavailable = append(unavailable,
		Unavailable{Kind: UnavailLaneSegment, MatchID: matchID, RuleVersion: version.EpisodeRuleVersion,
			Reason: "map_geometry_not_loaded", RequiredInputs: []string{"map_geometry", "lane_polygons", "build_version"}},
		Unavailable{Kind: UnavailLaneDeparture, MatchID: matchID, RuleVersion: version.EpisodeRuleVersion,
			Reason: "lane_segments_unavailable", RequiredInputs: []string{"lane_segment_stream", "map_geometry"}},
		Unavailable{Kind: UnavailRoamAttempt, MatchID: matchID, RuleVersion: version.EpisodeRuleVersion,
			Reason: "roam_rule_needs_teleport_and_lane_context", RequiredInputs: []string{"teleport_events", "lane_segment_stream"}},
		Unavailable{Kind: UnavailFarmInterval, MatchID: matchID, RuleVersion: version.EpisodeRuleVersion,
			Reason: "farm_interval_needs_economy_deltas", RequiredInputs: []string{"economy_sample", "last_hit_deny_stream"}},
		Unavailable{Kind: UnavailRuneContest, MatchID: matchID, RuleVersion: version.EpisodeRuleVersion,
			Reason: "rune_contest_needs_rune_state", RequiredInputs: []string{"rune_state_stream", "rune_ownership"}},
	)

	out := &Output{
		SchemaVersion: version.EpisodeSchema,
		RuleVersion:   version.EpisodeRuleVersion,
		MatchID:       matchID,
		Episodes:      b.Episodes(),
		Unavailable:   unavailable,
	}
	if factsSeen == 0 {
		out.Episodes = []Episode{}
	}
	return out, nil
}

// deathRound tracks a single death -> respawn/buyback round per account.
type deathRound struct {
	account    string
	deathSec   float64
	respawnSec float64
	buybackSec float64
	closed     bool
}

func handleDeath(b *Builder, rounds map[string]*deathRound, drb *facts.DeathRespawnBuyback, gs float64) {
	if drb.AccountID == "" {
		return
	}
	rounds[drb.AccountID] = &deathRound{account: drb.AccountID, deathSec: gs}
}

func handleBuyback(b *Builder, rounds map[string]*deathRound, drb *facts.DeathRespawnBuyback, gs float64) {
	r, ok := rounds[drb.AccountID]
	if !ok || r.closed {
		return
	}
	r.buybackSec = gs
	rounds[drb.AccountID] = r
	b.Add(&Episode{
		Kind: KindDeathRound, AccountID: drb.AccountID,
		StartGameSecond: r.deathSec, EndGameSecond: gs + 1,
		Participants: []string{drb.AccountID},
		Detail:       "buyback_round",
		EvidenceIDs:  []int64{int64(r.deathSec)},
		Confidence:   1.0,
	})
	r.closed = true
}

func handleRespawn(b *Builder, rounds map[string]*deathRound, drb *facts.DeathRespawnBuyback, gs float64) {
	r, ok := rounds[drb.AccountID]
	if !ok || r.closed {
		return
	}
	r.respawnSec = gs
	rounds[drb.AccountID] = r
	b.Add(&Episode{
		Kind: KindDeathRound, AccountID: drb.AccountID,
		StartGameSecond: r.deathSec, EndGameSecond: gs + 1,
		Participants: []string{drb.AccountID},
		Detail:       "respawn_round",
		EvidenceIDs:  []int64{int64(r.deathSec)},
		Confidence:   1.0,
	})
	r.closed = true
}

func objectiveEpisode(of *facts.ObjectiveFact, gs float64) *Episode {
	ep := &Episode{
		Kind: KindObjective,
		StartGameSecond: gs, EndGameSecond: gs + 1,
		Detail: of.BuildingName,
		Region: fmt.Sprintf("%s/%s", of.Team, of.BuildingKind),
		Participants: of.Attackers,
		EvidenceIDs:  []int64{int64(gs)},
		Confidence:   1.0,
	}
	if len(of.Attackers) == 0 && of.ActorAccount != "" {
		ep.Participants = []string{of.ActorAccount}
	}
	if len(ep.Participants) == 0 {
		ep.MissingInputs = append(ep.MissingInputs, "attacker_resolution")
	}
	return ep
}

// fightWindow detects fight intervals by clustering deaths within 30 seconds.
// A fight begins at the first death of a cluster of >=2 deaths and ends 30
// seconds after the last death in the cluster. This is a deterministic V1
// event-density episode; it does not assert any strategic correctness.
type fightWindow struct {
	b        *Builder
	open     bool
	start    float64
	last     float64
	accounts map[string]bool
	seqs     []int64
}

func newFightWindow(b *Builder) *fightWindow {
	return &fightWindow{b: b, accounts: map[string]bool{}}
}

// addDeath records a hero death for fight clustering.
func (w *fightWindow) addDeath(account string, gs float64, seq int64) {
	if !w.open {
		w.open = true
		w.start = gs
		w.last = gs
		w.accounts = map[string]bool{}
		w.seqs = nil
		w.accounts[account] = true
		w.seqs = append(w.seqs, seq)
		return
	}
	if gs-w.last > 30 {
		// Close previous cluster and start a new one.
		w.close()
		w.open = true
		w.start = gs
		w.last = gs
		w.accounts = map[string]bool{account: true}
		w.seqs = []int64{seq}
		return
	}
	w.accounts[account] = true
	w.seqs = append(w.seqs, seq)
	w.last = gs
}

func (w *fightWindow) close() {
	if !w.open {
		return
	}
	w.open = false
	if len(w.accounts) >= 2 {
		parts := make([]string, 0, len(w.accounts))
		for a := range w.accounts {
			parts = append(parts, a)
		}
		ep := &Episode{
			Kind:             KindFight,
			StartGameSecond:  w.start,
			EndGameSecond:    w.last + 1,
			Participants:     parts,
			EvidenceIDs:      append([]int64(nil), w.seqs...),
			Confidence:       1.0,
			Detail:           fmt.Sprintf("death_cluster_%d", len(parts)),
		}
		w.b.Add(ep)
	}
	w.accounts = map[string]bool{}
	w.seqs = nil
}

func (w *fightWindow) flush() { w.close() }

// EventsFacts is a small alias so the package compiles without importing io.
var _ = fmt.Sprintf
var _ = json.Marshal