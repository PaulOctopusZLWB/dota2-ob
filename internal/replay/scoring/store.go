package scoring

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

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
	SchemaVersion        string                           `json:"schema_version"`
	RuleVersion          string                           `json:"rule_version"`
	ContractVersion      string                           `json:"contract_version"`
	TeamScoringVersion   string                           `json:"team_scoring_version"`
	ComparisonPopulation string                           `json:"comparison_population"`
	CorpusMatches        int                              `json:"corpus_matches"`
	Players              []*PlayerScore                   `json:"players"`
	Teams                []*TeamScore                     `json:"teams"`
	Matches              map[string]*MatchScores          `json:"matches"`
	TeamMatches          map[string]map[string]*TeamMatch `json:"team_matches"`
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
		unavailableByAcct := map[string]map[string]UnavailableMetric{}
		for _, p := range part {
			if p.AccountID != "" {
				byAcct[p.AccountID] = map[string]MetricValue{}
				unavailableByAcct[p.AccountID] = map[string]UnavailableMetric{}
			}
		}
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
		for _, v := range met.Unavailable {
			if v.AccountID == "" || (v.OfficialPhase != "" && v.OfficialPhase != "whole_match") {
				continue
			}
			if unavailableByAcct[v.AccountID] == nil {
				unavailableByAcct[v.AccountID] = map[string]UnavailableMetric{}
			}
			unavailableByAcct[v.AccountID][v.MetricID] = UnavailableMetric{
				MetricID: v.MetricID, MetricVersion: v.MetricVersion,
				UnavailableReason: v.UnavailableReason,
				Lineage:           metricsToEvidenceRefs(row.MatchID, v.Evidence),
			}
		}
		for acct, mvs := range byAcct {
			prov := provenanceByAcct[acct]
			players = append(players, &PlayerMatch{
				MatchID: row.MatchID, AccountID: acct, TeamID: teamByAcct[acct],
				SourceNominalRole: prov.SourceNominalRole, NominalRole: roleByAcct[acct],
				RoleRecordVersion: prov.RoleRecordVersion, OverrideApplied: prov.OverrideApplied,
				OverrideAuthor: prov.OverrideAuthor, OverrideVersion: prov.OverrideVersion,
				Metrics: mvs, UnavailableMetrics: unavailableByAcct[acct],
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

func validateRegistryMetric(reg *metrics.Registry, context, mapKey, metricID, metricVersion string) error {
	if mapKey != "" && mapKey != metricID {
		return fmt.Errorf("scoring: corrupt score artifact context=%s field=metric_id expected=%s actual=%s", context, mapKey, printableVersion(metricID))
	}
	if reg == nil {
		return fmt.Errorf("scoring: score artifact validation context=%s metrics registry unavailable", context)
	}
	def := reg.Find(metricID)
	if def == nil {
		return fmt.Errorf("scoring: corrupt score artifact context=%s field=metric_id expected=registered actual=%s", context, printableVersion(metricID))
	}
	if metricVersion != def.MetricVersion {
		return fmt.Errorf("scoring: corrupt score artifact context=%s metric=%s field=metric_version expected=%s actual=%s", context, metricID, def.MetricVersion, printableVersion(metricVersion))
	}
	return nil
}

func validateMetricMaps(reg *metrics.Registry, context string, published map[string]MetricValue, unavailable map[string]UnavailableMetric) error {
	for key, value := range published {
		if err := validateRegistryMetric(reg, context+".metrics["+key+"]", key, value.MetricID, value.MetricVersion); err != nil {
			return err
		}
	}
	for key, value := range unavailable {
		if err := validateRegistryMetric(reg, context+".unavailable_metrics["+key+"]", key, value.MetricID, value.MetricVersion); err != nil {
			return err
		}
		if value.UnavailableReason == "" {
			return fmt.Errorf("scoring: corrupt score artifact context=%s.unavailable_metrics[%s] field=unavailable_reason expected=non-empty actual=<missing>", context, key)
		}
	}
	return nil
}

func validateAggregatedMap(reg *metrics.Registry, context, scope, subject, role, contractVersion string, values map[string]AggregatedMetric) error {
	for key, value := range values {
		itemContext := context + ".aggregated_metrics[" + key + "]"
		if err := validateRegistryMetric(reg, itemContext, key, value.MetricID, value.MetricVersion); err != nil {
			return err
		}
		expectedID := fmt.Sprintf("aggregation:%s:%s:%s:%s:%s:%s", scope, subject, role, value.MetricID, value.MetricVersion, version.ScoreRuleVersion)
		found := 0
		for _, ref := range value.Lineage {
			if ref.Kind != "aggregation" {
				continue
			}
			if ref.RuleVersion != version.ScoreRuleVersion {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.rule_version expected=%s actual=%s", itemContext, version.ScoreRuleVersion, printableVersion(ref.RuleVersion))
			}
			if !strings.HasSuffix(ref.ID, ":"+version.ScoreRuleVersion) {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.id expected_score_rule_suffix=%s actual=%s", itemContext, version.ScoreRuleVersion, ref.ID)
			}
			if ref.ContractVersion != contractVersion {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.contract_version expected=%s actual=%s", itemContext, printableVersion(contractVersion), printableVersion(ref.ContractVersion))
			}
			if ref.ID == expectedID {
				found++
			} else {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.id expected=%s actual=%s", itemContext, expectedID, ref.ID)
			}
		}
		if found != 1 {
			return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref_count expected=1 actual=%d", itemContext, found)
		}
	}
	return nil
}

func teamMatchAggregationID(teamID, matchID, metricID, metricVersion string) string {
	return fmt.Sprintf("aggregation:team_match:%s:%s:%s:%s:%s", teamID, matchID, metricID, metricVersion, version.ScoreRuleVersion)
}

func validateTeamMatchEntities(cs *CorpusScores, reg *metrics.Registry) error {
	for teamKey, byMatch := range cs.TeamMatches {
		for matchKey, entity := range byMatch {
			context := fmt.Sprintf("corpus.team_matches[%s][%s]", teamKey, matchKey)
			if entity == nil {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=document expected=present actual=nil", context)
			}
			if entity.TeamID != teamKey {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_id expected=%s actual=%s", context, teamKey, printableVersion(entity.TeamID))
			}
			if entity.MatchID != matchKey {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=match_id expected=%s actual=%s", context, matchKey, printableVersion(entity.MatchID))
			}
			if _, ok := cs.Matches[matchKey]; !ok {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=match_id expected=persisted_match actual=%s", context, matchKey)
			}
			if entity.RuleVersion != version.ScoreRuleVersion {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=rule_version expected=%s actual=%s", context, version.ScoreRuleVersion, printableVersion(entity.RuleVersion))
			}
			if entity.ContractVersion != cs.TeamScoringVersion {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=contract_version expected=%s actual=%s", context, cs.TeamScoringVersion, printableVersion(entity.ContractVersion))
			}
			for key, value := range entity.Metrics {
				itemContext := context + ".metrics[" + key + "]"
				if err := validateRegistryMetric(reg, itemContext, key, value.MetricID, value.MetricVersion); err != nil {
					return err
				}
				expectedID := teamMatchAggregationID(teamKey, matchKey, value.MetricID, value.MetricVersion)
				own := 0
				for _, ref := range value.Lineage {
					if ref.Kind == "aggregation" {
						if ref.ID != expectedID {
							return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.id expected=%s actual=%s", itemContext, expectedID, ref.ID)
						}
						if ref.MatchID != matchKey {
							return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.match_id expected=%s actual=%s", itemContext, matchKey, printableVersion(ref.MatchID))
						}
						if ref.RuleVersion != version.ScoreRuleVersion || ref.ContractVersion != cs.TeamScoringVersion {
							return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.provenance expected=%s/%s actual=%s/%s", itemContext, version.ScoreRuleVersion, cs.TeamScoringVersion, printableVersion(ref.RuleVersion), printableVersion(ref.ContractVersion))
						}
						own++
					} else if ref.MatchID != matchKey {
						return fmt.Errorf("scoring: corrupt score artifact context=%s field=child_ref.match_id expected=%s actual=%s", itemContext, matchKey, ref.MatchID)
					}
				}
				if own != 1 {
					return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref_count expected=1 actual=%d", itemContext, own)
				}
			}
		}
	}
	return nil
}

func validateTeamTournamentAggregates(cs *CorpusScores, reg *metrics.Registry) error {
	cor := &Corpus{MetricReg: reg}
	seenTeams := map[string]bool{}
	for i, team := range cs.Teams {
		if team == nil {
			continue
		}
		context := fmt.Sprintf("corpus.teams[%d]", i)
		if seenTeams[team.TeamID] {
			return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_id duplicate=%s", context, team.TeamID)
		}
		seenTeams[team.TeamID] = true
		byMatch := cs.TeamMatches[team.TeamID]
		allowedMatches := map[string]bool{}
		for _, matchID := range team.SubjectCoverage.MatchIDs {
			if allowedMatches[matchID] {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=subject_coverage.match_ids duplicate=%s", context, matchID)
			}
			allowedMatches[matchID] = true
		}
		if len(allowedMatches) != team.SubjectCoverage.EligibleMatches {
			return fmt.Errorf("scoring: corrupt score artifact context=%s field=subject_coverage.eligible_matches expected=%d actual=%d", context, len(allowedMatches), team.SubjectCoverage.EligibleMatches)
		}
		for matchID := range byMatch {
			if !allowedMatches[matchID] {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_matches extra_match=%s", context, matchID)
			}
		}
		for matchID := range allowedMatches {
			if byMatch[matchID] == nil {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_entity match=%s expected=present actual=missing", context, matchID)
			}
		}
		for key, tournament := range team.AggregatedMetrics {
			itemContext := context + ".aggregated_metrics[" + key + "]"
			if err := validateRegistryMetric(reg, itemContext, key, tournament.MetricID, tournament.MetricVersion); err != nil {
				return err
			}
			ownID := fmt.Sprintf("aggregation:team_tournament:%s::%s:%s:%s", team.TeamID, tournament.MetricID, tournament.MetricVersion, version.ScoreRuleVersion)
			expectedChildren := map[string]string{}
			var childValues []MetricValue
			for _, matchID := range team.SubjectCoverage.MatchIDs {
				entity := byMatch[matchID]
				if entity == nil {
					return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_entity match=%s expected=present actual=missing", itemContext, matchID)
				}
				child, ok := entity.Metrics[key]
				if !ok {
					continue
				}
				childID := teamMatchAggregationID(team.TeamID, matchID, child.MetricID, child.MetricVersion)
				expectedChildren[childID] = matchID
				n, d := child.Numerator, child.Denominator
				childValues = append(childValues, MetricValue{MetricID: child.MetricID, MetricVersion: child.MetricVersion, Value: child.Value, Numerator: &n, Denominator: &d, OpportunityCount: child.OpportunityCount, Direction: child.Direction, OfficialEligible: child.OfficialEligible, ExperimentalEligible: child.ExperimentalEligible, Lineage: child.Lineage})
			}
			if len(expectedChildren) != tournament.EligibleMatches {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=eligible_matches expected=%d actual=%d", itemContext, len(expectedChildren), tournament.EligibleMatches)
			}
			seenChildren := map[string]bool{}
			own := 0
			for _, ref := range tournament.Lineage {
				if ref.Kind != "aggregation" {
					continue
				}
				if ref.ID == ownID {
					if ref.MatchID != "" || ref.RuleVersion != version.ScoreRuleVersion || ref.ContractVersion != cs.TeamScoringVersion {
						return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref.provenance", itemContext)
					}
					own++
					continue
				}
				matchID, ok := expectedChildren[ref.ID]
				if !ok {
					return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_child unexpected=%s", itemContext, ref.ID)
				}
				if ref.MatchID != matchID {
					return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_child.match_id expected=%s actual=%s", itemContext, matchID, printableVersion(ref.MatchID))
				}
				if ref.RuleVersion != version.ScoreRuleVersion || ref.ContractVersion != cs.TeamScoringVersion {
					return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_child.provenance id=%s", itemContext, ref.ID)
				}
				if seenChildren[ref.ID] {
					return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_child duplicate=%s", itemContext, ref.ID)
				}
				seenChildren[ref.ID] = true
			}
			if own != 1 {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=aggregation_ref_count expected=1 actual=%d", itemContext, own)
			}
			if len(seenChildren) != len(expectedChildren) {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=team_match_child_count expected=%d actual=%d", itemContext, len(expectedChildren), len(seenChildren))
			}
			reconciled, ok := cor.aggregateValues(key, childValues)
			if !ok || math.Abs(reconciled.Value-tournament.Value) > 1e-9 || math.Abs(reconciled.Numerator-tournament.Numerator) > 1e-9 || math.Abs(reconciled.Denominator-tournament.Denominator) > 1e-9 || reconciled.OpportunityCount != tournament.OpportunityCount {
				return fmt.Errorf("scoring: corrupt score artifact context=%s field=reconciliation expected_value=%v actual_value=%v", itemContext, reconciled.Value, tournament.Value)
			}
		}
	}
	for teamID := range cs.TeamMatches {
		if !seenTeams[teamID] {
			return fmt.Errorf("scoring: corrupt score artifact context=corpus.team_matches field=team_id non_resolving=%s", teamID)
		}
	}
	return nil
}

func validateComponents(reg *metrics.Registry, context string, axes map[string]AxisResult) error {
	for axisKey, axis := range axes {
		for key, component := range axis.Components {
			if err := validateRegistryMetric(reg, context+"["+axisKey+"].components["+key+"]", key, component.MetricID, component.MetricVersion); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateMatchScores rejects a current-tagged per-match score document whose
// nested registry identity or version is stale/corrupt.
func ValidateMatchScores(ms *MatchScores, reg *metrics.Registry) error {
	if ms == nil {
		return fmt.Errorf("scoring: corrupt score artifact context=match field=document expected=present actual=nil")
	}
	if ms.SchemaVersion != version.ScoreSchema {
		return fmt.Errorf("scoring: stale score artifact match=%s field=schema_version expected=%s actual=%s", ms.MatchID, version.ScoreSchema, printableVersion(ms.SchemaVersion))
	}
	if ms.RuleVersion != version.ScoreRuleVersion {
		return fmt.Errorf("scoring: stale score artifact match=%s field=rule_version expected=%s actual=%s", ms.MatchID, version.ScoreRuleVersion, printableVersion(ms.RuleVersion))
	}
	for i, player := range ms.Players {
		if player == nil {
			return fmt.Errorf("scoring: corrupt score artifact match=%s field=players[%d] expected=present actual=nil", ms.MatchID, i)
		}
		if err := validateMetricMaps(reg, fmt.Sprintf("match[%s].players[%d]", ms.MatchID, i), player.Metrics, player.UnavailableMetrics); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCorpusScores validates every nested registry metric and canonical
// aggregation reference before a current score corpus is persisted or served.
func ValidateCorpusScores(cs *CorpusScores, reg *metrics.Registry) error {
	if cs == nil {
		return fmt.Errorf("scoring: corrupt score artifact context=corpus field=document expected=present actual=nil")
	}
	if cs.SchemaVersion != version.ScoreSchema {
		return fmt.Errorf("scoring: stale score artifact context=corpus field=schema_version expected=%s actual=%s", version.ScoreSchema, printableVersion(cs.SchemaVersion))
	}
	if cs.RuleVersion != version.ScoreRuleVersion {
		return fmt.Errorf("scoring: stale score artifact context=corpus field=rule_version expected=%s actual=%s", version.ScoreRuleVersion, printableVersion(cs.RuleVersion))
	}
	if cs.ContractVersion != SchemaVersion {
		return fmt.Errorf("scoring: stale score artifact context=corpus field=contract_version expected=%s actual=%s", SchemaVersion, printableVersion(cs.ContractVersion))
	}
	if cs.TeamScoringVersion != TeamSchemaVersion {
		return fmt.Errorf("scoring: stale score artifact context=corpus field=team_scoring_version expected=%s actual=%s", TeamSchemaVersion, printableVersion(cs.TeamScoringVersion))
	}
	for key, match := range cs.Matches {
		if match == nil || match.MatchID != key {
			return fmt.Errorf("scoring: corrupt score artifact context=corpus.matches[%s] field=match_id expected=%s actual=%s", key, key, func() string {
				if match == nil {
					return "<nil>"
				}
				return printableVersion(match.MatchID)
			}())
		}
		if err := ValidateMatchScores(match, reg); err != nil {
			return err
		}
	}
	if err := validateTeamMatchEntities(cs, reg); err != nil {
		return err
	}
	for i, player := range cs.Players {
		if player == nil {
			return fmt.Errorf("scoring: corrupt score artifact context=corpus.players[%d] expected=present actual=nil", i)
		}
		context := fmt.Sprintf("corpus.players[%d]", i)
		if player.ScoringVersion != cs.ContractVersion {
			return fmt.Errorf("scoring: corrupt score artifact context=%s field=scoring_version expected=%s actual=%s", context, cs.ContractVersion, printableVersion(player.ScoringVersion))
		}
		for key, percentile := range player.MetricPercentiles {
			if err := validateRegistryMetric(reg, context+".metric_percentiles["+key+"]", key, percentile.MetricID, percentile.MetricVersion); err != nil {
				return err
			}
		}
		if err := validateAggregatedMap(reg, context, "player_tournament", player.AccountID, player.NominalRole, cs.ContractVersion, player.AggregatedMetrics); err != nil {
			return err
		}
		for key, unavailable := range player.UnavailableMetrics {
			if err := validateRegistryMetric(reg, context+".unavailable_metrics["+key+"]", key, unavailable.MetricID, unavailable.MetricVersion); err != nil {
				return err
			}
			if unavailable.UnavailableReason == "" {
				return fmt.Errorf("scoring: corrupt score artifact context=%s.unavailable_metrics[%s] field=unavailable_reason expected=non-empty actual=<missing>", context, key)
			}
		}
		if err := validateComponents(reg, context+".official_axes", player.OfficialAxes); err != nil {
			return err
		}
		if err := validateComponents(reg, context+".experimental_axes", player.ExperimentalAxes); err != nil {
			return err
		}
	}
	for i, team := range cs.Teams {
		if team == nil {
			return fmt.Errorf("scoring: corrupt score artifact context=corpus.teams[%d] expected=present actual=nil", i)
		}
		context := fmt.Sprintf("corpus.teams[%d]", i)
		if team.ScoringVersion != cs.TeamScoringVersion {
			return fmt.Errorf("scoring: corrupt score artifact context=%s field=scoring_version expected=%s actual=%s", context, cs.TeamScoringVersion, printableVersion(team.ScoringVersion))
		}
		for key, percentile := range team.MetricPercentiles {
			if err := validateRegistryMetric(reg, context+".metric_percentiles["+key+"]", key, percentile.MetricID, percentile.MetricVersion); err != nil {
				return err
			}
		}
		for key, unavailable := range team.UnavailableMetrics {
			if err := validateRegistryMetric(reg, context+".unavailable_metrics["+key+"]", key, unavailable.MetricID, unavailable.MetricVersion); err != nil {
				return err
			}
			if unavailable.UnavailableReason == "" {
				return fmt.Errorf("scoring: corrupt score artifact context=%s.unavailable_metrics[%s] field=unavailable_reason expected=non-empty actual=<missing>", context, key)
			}
		}
		if err := validateComponents(reg, context+".official_axes", team.OfficialAxes); err != nil {
			return err
		}
		if err := validateComponents(reg, context+".experimental_axes", team.ExperimentalAxes); err != nil {
			return err
		}
	}
	if err := validateTeamTournamentAggregates(cs, reg); err != nil {
		return err
	}
	return nil
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
		TeamMatches:          cor.TeamMatches,
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
		if err := ValidateMatchScores(ms, mreg); err != nil {
			return nil, err
		}
		cs.Matches[matchID] = ms
	}
	if err := ValidateCorpusScores(cs, mreg); err != nil {
		return nil, err
	}
	// Validate the complete graph before writing any score artifact. In
	// particular this proves every tournament child resolves to one persisted
	// team-match entity, so a corrupt graph cannot partially replace the
	// authoritative per-match snapshots.
	for _, matchID := range ids {
		if err := st.WriteJSON(matchID, store.ArtifactScores, cs.Matches[matchID]); err != nil {
			return nil, fmt.Errorf("scoring: persist %s: %w", matchID, err)
		}
	}
	// Corpus-level scoring catalog (rebuildable; not part of any match's
	// canonical tree).
	if err := st.WriteRootJSON("scores-corpus.json", cs); err != nil {
		return nil, err
	}
	return cs, nil
}
