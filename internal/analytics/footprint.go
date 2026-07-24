package analytics

import "sort"

// fieldFootprint records the set of leaf field paths actually observed for a
// player in the raw payload. It is an evidence list, never a source of truth
// for values.
type fieldFootprint struct {
	seen map[string]struct{}
}

func newFieldFootprint() *fieldFootprint {
	return &fieldFootprint{seen: make(map[string]struct{})}
}

func (f *fieldFootprint) add(path string) {
	if f.seen == nil {
		f.seen = make(map[string]struct{})
	}
	f.seen[path] = struct{}{}
}

// paths returns the sorted observed footprint paths for a player tick.
func (f *fieldFootprint) paths() []string {
	if len(f.seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.seen))
	for p := range f.seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// playerKey is a (team, player) coordinate inside the hero/player/items/
// abilities sections.
type playerKey struct {
	team   string
	player string
}

// playerTeamKeys returns the stable, sorted union of (team, player) keys across
// the supplied sections.
func playerTeamKeys(sections ...map[string]any) []playerKey {
	seen := make(map[playerKey]struct{})
	for _, section := range sections {
		for team, playersVal := range section {
			players := asMap(playersVal)
			for player := range players {
				seen[playerKey{team: team, player: player}] = struct{}{}
			}
		}
	}
	out := make([]playerKey, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].team != out[j].team {
			return out[i].team < out[j].team
		}
		return out[i].player < out[j].player
	})
	return out
}