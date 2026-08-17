package scoring

import (
	"fmt"
	"os"
	"sort"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// MatchScores is the persisted per-match scoring snapshot: one PlayerScore
// per participant with a role and at least one published metric.
type MatchScores struct {
	SchemaVersion string         `json:"schema_version"`
	RuleVersion   string         `json:"rule_version"`
	MatchID       string         `json:"match_id"`
	Players       []*PlayerScore `json:"players"`
}

// CorpusScores is the corpus-level scoring catalog (rebuildable).
type CorpusScores struct {
	SchemaVersion        string                  `json:"schema_version"`
	RuleVersion          string                  `json:"rule_version"`
	ContractVersion      string                  `json:"contract_version"`
	ComparisonPopulation string                  `json:"comparison_population"`
	CorpusMatches        int                     `json:"corpus_matches"`
	Matches              map[string]*MatchScores `json:"matches"`
}

// BuildCorpusFromStore loads every verified match's report + metrics from the
// store and builds the scoring corpus. Matches that are not verified or have
// no metrics are excluded from the comparison population (their absence is
// disclosed by the API coverage, never silently imputed).
func BuildCorpusFromStore(st *store.Store, c *Contract, roleReg *roles.Registry, overrides *roles.OverrideFile) (*Corpus, error) {
	var cat store.Catalog
	if err := st.ReadJSONFile(st.CatalogPath(), &cat); err != nil {
		if os.IsNotExist(err) {
			return NewCorpus(c, nil), nil
		}
		return nil, fmt.Errorf("scoring: read catalog: %w", err)
	}
	var players []*PlayerMatch
	for _, row := range cat.Matches {
		if row.Status != store.StatusVerified {
			continue
		}
		var rep reportLite
		if err := st.ReadJSON(row.MatchID, store.ArtifactReport, &rep); err != nil {
			continue
		}
		var met metrics.Output
		if err := st.ReadJSON(row.MatchID, store.ArtifactMetrics, &met); err != nil {
			continue
		}
		roleByAcct := map[string]string{}
		teamByAcct := map[string]string{}
		for _, p := range rep.Participants {
			if p.AccountID != "" {
				roleByAcct[p.AccountID] = p.NominalRole
				teamByAcct[p.AccountID] = p.TeamID
			}
		}
		byAcct := map[string]map[string]MetricValue{}
		for _, v := range met.Values {
			if v.AccountID == "" || v.Value == nil {
				continue
			}
			if byAcct[v.AccountID] == nil {
				byAcct[v.AccountID] = map[string]MetricValue{}
			}
			byAcct[v.AccountID][v.MetricID] = MetricValue{
				MetricID: v.MetricID, Value: *v.Value,
				Direction:            Direction(v.Direction),
				OfficialEligible:     v.OfficialScoreEligible,
				ExperimentalEligible: v.ExperimentalScoreEligible,
			}
		}
		for acct, mv := range byAcct {
			players = append(players, &PlayerMatch{
				MatchID: row.MatchID, AccountID: acct, TeamID: teamByAcct[acct], NominalRole: roleByAcct[acct],
				Metrics: mv,
			})
		}
	}
	sort.Slice(players, func(i, j int) bool {
		if players[i].MatchID != players[j].MatchID {
			return players[i].MatchID < players[j].MatchID
		}
		return players[i].AccountID < players[j].AccountID
	})
	return NewCorpus(c, players), nil
}

// reportLite is the report subset the scorer needs (participants with roles).
type reportLite struct {
	Participants []struct {
		AccountID   string `json:"account_id"`
		TeamID      string `json:"team_id"`
		NominalRole string `json:"nominal_role"`
	} `json:"participants"`
}

// ComputeAndPersist computes scores for every player in every verified match
// and writes per-match scores.json artifacts plus a corpus scores catalog.
// It is deterministic and rebuildable from persisted metrics.
func ComputeAndPersist(st *store.Store, c *Contract, roleReg *roles.Registry, overrides *roles.OverrideFile) (*CorpusScores, error) {
	cor, err := BuildCorpusFromStore(st, c, roleReg, overrides)
	if err != nil {
		return nil, err
	}
	cs := &CorpusScores{
		SchemaVersion:        version.ScoreSchema,
		RuleVersion:          version.ScoreRuleVersion,
		ContractVersion:      c.SchemaVersion,
		ComparisonPopulation: c.ComparisonPopulation,
		CorpusMatches:        cor.MatchCount(),
		Matches:              map[string]*MatchScores{},
	}
	// Group corpus by match, preserving deterministic order.
	byMatch := map[string][]*PlayerMatch{}
	for _, p := range cor.Players {
		byMatch[p.MatchID] = append(byMatch[p.MatchID], p)
	}
	ids := make([]string, 0, len(byMatch))
	for id := range byMatch {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, matchID := range ids {
		ms := &MatchScores{
			SchemaVersion: version.ScoreSchema,
			RuleVersion:   version.ScoreRuleVersion,
			MatchID:       matchID,
			Players:       []*PlayerScore{},
		}
		pm := byMatch[matchID]
		sort.Slice(pm, func(i, j int) bool { return pm[i].AccountID < pm[j].AccountID })
		for _, p := range pm {
			ps := cor.ScorePlayer(p)
			ms.Players = append(ms.Players, ps)
		}
		if err := st.WriteJSON(matchID, store.ArtifactScores, ms); err != nil {
			return nil, fmt.Errorf("scoring: persist %s: %w", matchID, err)
		}
		cs.Matches[matchID] = ms
	}
	// Corpus-level scoring catalog (rebuildable; not part of any match's
	// canonical tree).
	if err := st.WriteRootJSON("scores-corpus.json", cs); err != nil {
		return nil, err
	}
	return cs, nil
}
