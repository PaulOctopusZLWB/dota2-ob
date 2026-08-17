package raw

import (
	"io"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
)

// Summary is the cheap first-pass view of a raw stream: only the small,
// identity-relevant events are retained. It is used to run the identity and
// clock gates before the heavy fact-normalization pass.
type Summary struct {
	FileInfo      *FileInfo
	Engine        *Engine
	Header        *Header
	Teams         map[int32]*TeamState
	PlayerRes     []PlayerResource
	Transitions   []clock.Transition
	ParseDone     *ParseDone
	ParseFailed   *ParseFailed
	CombatTotal   uint64
	MaxCombatTS   float64
	LastTick      uint32
	LastNetTick   uint32
	HeroSnapshots int64
	CombatEvents  int64
	GameStates    int
}

// ScanSummary reads a raw stream once and collects the identity-relevant
// events. Combat and hero-state events are counted only (not retained), so
// this pass stays cheap and bounded.
func ScanSummary(r *Reader) (*Summary, error) {
	s := &Summary{Teams: map[int32]*TeamState{}}
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch e.Kind {
		case KindFileInfo:
			s.FileInfo = e.FileInfo
		case KindEngine:
			s.Engine = e.Engine
		case KindHeader:
			s.Header = e.Header
		case KindTeamState:
			s.Teams[e.TeamState.TeamNumValue()] = e.TeamState
		case KindPlayerRes:
			s.PlayerRes = append(s.PlayerRes, *e.PlayerRes)
		case KindGameState:
			s.Transitions = append(s.Transitions, clock.Transition{
				State:    e.GameState.State,
				CombatTS: e.GameState.CombatTS,
				Tick:     e.GameState.Tick,
			})
			s.GameStates++
		case KindCombat:
			s.CombatEvents++
			if e.Combat.TS > s.MaxCombatTS {
				s.MaxCombatTS = e.Combat.TS
			}
		case KindHeroState:
			s.HeroSnapshots++
		case KindParseDone:
			s.ParseDone = e.ParseDone
			s.LastTick = e.ParseDone.LastTick
			s.LastNetTick = e.ParseDone.LastNetTick
		case KindParseFailed:
			s.ParseFailed = e.ParseFailed
		}
	}
	return s, nil
}

// TeamNumValue returns the team's numeric team id (defaulting to the key).
func (t *TeamState) TeamNumValue() int32 {
	if t.TeamNum != nil {
		return *t.TeamNum
	}
	return t.TeamIndex
}
