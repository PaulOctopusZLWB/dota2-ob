package scoring

import (
	"fmt"
	"os"
	"sort"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// MatchScores is the persisted per-match scoring snapshot: one PlayerMatch row
// per participant (the per-match grain for drilldown).
type MatchScores struct {
	SchemaVersion string         `json:"schema_version"`
	RuleVersion   string         `json:"rule_version"`
	MatchID       string         `json:"match_id"`
	Players       []*PlayerMatch `json:"players"`
}

// CorpusScores is the corpus-level scoring catalog (rebuildable): player
// tournament snapshots within fixed roles, team tournament snapshots, and the
// per-match rows for drilldown.
type CorpusScores struct {
	SchemaVersion        string                  `json:"schema_version"`
	RuleVersion          string                  `json:"rule_version"`
	ContractVersion      string                  `json:"contract_version"`
	TeamScoringVersion   string                  `json:"team_scoring_version"`
	ComparisonPopulation string                  `json:"comparison_population"`
	CorpusMatches        int                     `json:"corpus_matches"`
	Players              []*PlayerScore          `json:"players"`
	Teams                []*TeamScore            `json:"teams"`
	Matches              map[string]*MatchScores `json:"matches"`
}

// BuildCorpusFromStore loads every verified match's report + metrics from the
// store and builds the scoring corpus (player-match rows). Matches that are not
// verified or have no metrics are excluded from the comparison population.
// tc is the frozen team scoring registry (may be nil → team scoring fails
// closed). Roles are resolved authoritatively from roleReg + the effective
// override store (data-root overrides win over the frozen registry), never
// copied from a possibly-stale persisted report.
func BuildCorpusFromStore(st *store.Store, c *Contract, tc *TeamContract, mreg *metrics.Registry, roleReg *roles.Registry, overrides *roles.OverrideFile) (*Corpus, error) {
	var cat store.Catalog
	if err := st.ReadJSONFile(st.CatalogPath(), &cat); err != nil {
		if os.IsNotExist(err) {
			return NewCorpusWithTeam(c, tc, mreg, nil), nil
		}
		return nil, fmt.Errorf("scoring: read catalog: %w", err)
	}
	// Effective overrides: the authoritative data-root override store wins;
	// fall back to the passed-in overrides (frozen manifest file).
	effective := overrides
	var of roles.OverrideFile
	if err := st.ReadJSONFile(st.Root+"/role-overrides-effective.json", &of); err == nil && len(of.Overrides) > 0 {
		effective = &of
	}
	var players []*PlayerMatch
	for _, row := range cat.Matches {
		if row.Status != store.StatusVerified {
			continue
		}
		// Participants: prefer the persisted report, fall back to the identity
		// artifact when the report is absent. Roles are NEVER taken from the
		// report — they are resolved from roleReg + effective overrides below.
		var part []partLite
		var haveReport bool
		var rep reportLite
		if err := st.ReadJSON(row.MatchID, store.ArtifactReport, &rep); err == nil {
			haveReport = true
			for _, p := range rep.Participants {
				part = append(part, partLite{
					AccountID: p.AccountID, TeamID: p.TeamID, NominalRole: p.NominalRole, Side: p.Side,
					SourceNominalRole: p.SourceNominalRole, RoleRecordVersion: p.RoleRecordVersion,
					OverrideApplied: p.OverrideApplied, OverrideAuthor: p.OverrideAuthor, OverrideVersion: p.OverrideVersion,
				})
			}
		} else {
			var idn identity.Identity
			if err := st.ReadJSON(row.MatchID, store.ArtifactIdentity, &idn); err == nil {
				teamBySide := map[string]string{}
				for _, tm := range idn.Teams {
					teamBySide[tm.Side] = tm.TeamID
				}
				for _, p := range idn.Participants {
					part = append(part, partLite{AccountID: p.AccountID, TeamID: teamBySide[p.Side], NominalRole: "", Side: p.Side})
				}
			}
		}
		_ = haveReport
		var met metrics.Output
		if err := st.ReadJSON(row.MatchID, store.ArtifactMetrics, &met); err != nil {
			return nil, fmt.Errorf("scoring: metrics artifact match=%s path=%s: %w", row.MatchID, st.ArtifactPath(row.MatchID, store.ArtifactMetrics), err)
		}
		if err := validateMetricsArtifact(row.MatchID, st.ArtifactPath(row.MatchID, store.ArtifactMetrics), &met, mreg); err != nil {
			return nil, err
		}
		teamByAcct := map[string]string{}
		for _, p := range part {
			if p.AccountID != "" {
				teamByAcct[p.AccountID] = p.TeamID
			}
		}
		// Resolve the effective nominal role from the registry + overrides.
		roleByAcct := map[string]string{}
		provenanceByAcct := map[string]RoleProvenance{}
		if roleReg != nil {
			for _, p := range part {
				if p.AccountID == "" {
					continue
				}
				if eff, ok := roleReg.Effective(row.MatchID, p.AccountID, effective); ok {
					roleByAcct[p.AccountID] = eff.NominalRole
					provenanceByAcct[p.AccountID] = RoleProvenance{
						MatchID: row.MatchID, SourceNominalRole: eff.SourceNominalRole, NominalRole: eff.NominalRole,
						RoleRecordVersion: eff.RecordVersion, OverrideApplied: eff.OverrideApplied,
						OverrideAuthor: stringValue(eff.OverrideAuthor), OverrideVersion: stringValue(eff.OverrideVersion),
					}
				} else if p.NominalRole != "" {
					roleByAcct[p.AccountID] = p.NominalRole
				}
			}
		}
		byAcct := map[string]map[string]MetricValue{}
		for _, v := range met.Values {
			if v.AccountID == "" || v.Value == nil {
				continue
			}
			// Per-phase metric rows are audit/drilldown observations. Scoring
			// consumes the explicitly labelled whole-match reconciliation only,
			// avoiding phase-order overwrite and double counting.
			if v.OfficialPhase != "" && v.OfficialPhase != "whole_match" {
				continue
			}
			if byAcct[v.AccountID] == nil {
				byAcct[v.AccountID] = map[string]MetricValue{}
			}
			mv := MetricValue{
				MetricID: v.MetricID, MetricVersion: v.MetricVersion, Value: *v.Value,
				Direction:            Direction(v.Direction),
				OfficialEligible:     v.OfficialScoreEligible,
				ExperimentalEligible: v.ExperimentalScoreEligible,
				OpportunityCount:     v.OpportunityCount,
			}
			if v.Numerator != nil {
				f := *v.Numerator
				mv.Numerator = &f
			}
			if v.Denominator != nil {
				f := *v.Denominator
				mv.Denominator = &f
			}
			// Preserve the typed match-qualified fact->episode/phase->
			// metric_observation->algorithm lineage verbatim. Evidence refs
			// already carry (match_id, kind, id, rule_version); they are never
			// relabeled or fabricated at the scoring boundary.
			mv.Lineage = append(mv.Lineage, metricsToEvidenceRefs(row.MatchID, v.Evidence)...)
			byAcct[v.AccountID][v.MetricID] = mv
		}
		for acct, mvs := range byAcct {
			prov := provenanceByAcct[acct]
			players = append(players, &PlayerMatch{
				MatchID: row.MatchID, AccountID: acct, TeamID: teamByAcct[acct],
				SourceNominalRole: prov.SourceNominalRole, NominalRole: roleByAcct[acct],
				RoleRecordVersion: prov.RoleRecordVersion, OverrideApplied: prov.OverrideApplied,
				OverrideAuthor: prov.OverrideAuthor, OverrideVersion: prov.OverrideVersion,
				Metrics: mvs,
			})
		}
	}
	sort.Slice(players, func(i, j int) bool {
		if players[i].MatchID != players[j].MatchID {
			return players[i].MatchID < players[j].MatchID
		}
		return players[i].AccountID < players[j].AccountID
	})
	return NewCorpusWithTeam(c, tc, mreg, players), nil
}

func printableVersion(v string) string {
	if v == "" {
		return "<missing>"
	}
	return v
}

// validateMetricsArtifact is the scoring migration boundary. Verified catalog
// membership is not sufficient: every selected artifact and observation must
// match the current schema/rule and the loaded registry's metric version.
func validateMetricsArtifact(matchID, path string, met *metrics.Output, reg *metrics.Registry) error {
	if met == nil {
		return fmt.Errorf("scoring: stale metrics artifact match=%s path=%s field=document expected=present actual=nil", matchID, path)
	}
	if met.SchemaVersion != version.MetricsSchema {
		return fmt.Errorf("scoring: stale metrics artifact match=%s path=%s field=schema_version expected=%s actual=%s", matchID, path, version.MetricsSchema, printableVersion(met.SchemaVersion))
	}
	if met.RuleVersion != version.MetricsRuleVersion {
		return fmt.Errorf("scoring: stale metrics artifact match=%s path=%s field=rule_version expected=%s actual=%s", matchID, path, version.MetricsRuleVersion, printableVersion(met.RuleVersion))
	}
	if met.MatchID != "" && met.MatchID != matchID {
		return fmt.Errorf("scoring: stale metrics artifact match=%s path=%s field=match_id expected=%s actual=%s", matchID, path, matchID, met.MatchID)
	}
	if reg == nil {
		return fmt.Errorf("scoring: metrics registry unavailable match=%s path=%s", matchID, path)
	}
	check := func(kind string, values []metrics.Value) error {
		for i := range values {
			v := &values[i]
			def := reg.Find(v.MetricID)
			if def == nil {
				return fmt.Errorf("scoring: stale metrics artifact match=%s path=%s field=%s[%d].metric_id expected=registered actual=%s", matchID, path, kind, i, printableVersion(v.MetricID))
			}
			if v.MetricVersion != def.MetricVersion {
				return fmt.Errorf("scoring: stale metrics artifact match=%s path=%s metric=%s field=%s[%d].metric_version expected=%s actual=%s", matchID, path, v.MetricID, kind, i, def.MetricVersion, printableVersion(v.MetricVersion))
			}
		}
		return nil
	}
	if err := check("values", met.Values); err != nil {
		return err
	}
	return check("unavailable", met.Unavailable)
}

// metricsToEvidenceRefs converts typed metric-boundary evidence refs to the
// scoring lineage refs, preserving identity (match, kind, id, rule version)
// and source fact sequence. No relabeling and no invented success refs: a
// metric that published without evidence stays lineage-empty (the metrics
// layer fails those closed with no_evidence_lineage).
func metricsToEvidenceRefs(matchID string, ev []metrics.EvidenceRef) []EvidenceRef {
	out := make([]EvidenceRef, 0, len(ev))
	for _, r := range ev {
		out = append(out, EvidenceRef{
			MatchID:       matchID,
			Kind:          r.Kind,
			ID:            r.ID,
			RuleVersion:   r.RuleVersion,
			SourceFactSeq: r.SourceFactSeq,
		})
	}
	return out
}

// reportLite is the report subset the scorer needs (participants with roles).
type reportLite struct {
	Participants []struct {
		AccountID         string  `json:"account_id"`
		TeamID            string  `json:"team_id"`
		SourceNominalRole string  `json:"source_nominal_role"`
		NominalRole       string  `json:"nominal_role"`
		Side              string  `json:"side"`
		RoleRecordVersion string  `json:"role_record_version"`
		OverrideApplied   bool    `json:"override_applied"`
		OverrideAuthor    *string `json:"override_author"`
		OverrideVersion   *string `json:"override_version"`
	} `json:"participants"`
}

// partLite is a participant row for role/team resolution.
type partLite struct {
	AccountID         string
	TeamID            string
	SourceNominalRole string
	NominalRole       string
	Side              string
	RoleRecordVersion string
	OverrideApplied   bool
	OverrideAuthor    *string
	OverrideVersion   *string
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// teamScoringVersionOf returns the team registry version (or empty).
func teamScoringVersionOf(tc *TeamContract) string {
	if tc == nil {
		return ""
	}
	return tc.SchemaVersion
}

// ComputeAndPersist computes player-tournament and team-tournament scores from
// persisted metrics and writes per-match rows plus the corpus catalog. It is
// deterministic and rebuildable.
func ComputeAndPersist(st *store.Store, c *Contract, tc *TeamContract, mreg *metrics.Registry, roleReg *roles.Registry, overrides *roles.OverrideFile) (*CorpusScores, error) {
	cor, err := BuildCorpusFromStore(st, c, tc, mreg, roleReg, overrides)
	if err != nil {
		return nil, err
	}
	cs := &CorpusScores{
		SchemaVersion:        version.ScoreSchema,
		RuleVersion:          version.ScoreRuleVersion,
		ContractVersion:      c.SchemaVersion,
		TeamScoringVersion:   teamScoringVersionOf(tc),
		ComparisonPopulation: c.ComparisonPopulation,
		CorpusMatches:        cor.MatchCount(),
		Matches:              map[string]*MatchScores{},
		Players:              []*PlayerScore{},
		Teams:                []*TeamScore{},
	}

	// Player tournament snapshots (one per account+role).
	keys := make([]string, 0, len(cor.Tournaments))
	for k := range cor.Tournaments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pt := cor.Tournaments[k]
		if ps := cor.ScorePlayer(pt.AccountID, pt.NominalRole); ps != nil {
			cs.Players = append(cs.Players, ps)
		}
	}

	// Team tournament snapshots.
	tids := make([]string, 0, len(cor.Teams))
	for t := range cor.Teams {
		tids = append(tids, t)
	}
	sort.Strings(tids)
	for _, tid := range tids {
		if ts := cor.ScoreTeam(tid); ts != nil {
			cs.Teams = append(cs.Teams, ts)
		}
	}

	// Per-match rows for drilldown.
	ids := make([]string, 0, len(cor.Matches))
	for id := range cor.Matches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, matchID := range ids {
		ms := &MatchScores{
			SchemaVersion: version.ScoreSchema,
			RuleVersion:   version.ScoreRuleVersion,
			MatchID:       matchID,
			Players:       []*PlayerMatch{},
		}
		pm := cor.Matches[matchID]
		sort.Slice(pm, func(i, j int) bool { return pm[i].AccountID < pm[j].AccountID })
		ms.Players = append(ms.Players, pm...)
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
