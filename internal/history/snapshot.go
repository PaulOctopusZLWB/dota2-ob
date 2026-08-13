package history

import (
	"errors"
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// InputManifestEntry pins one included fact by its content identity so the
// snapshot manifest's InputManifestSHA256 is a content-addressed digest of
// exactly the facts that contributed.
type InputManifestEntry struct {
	MatchID         string    `json:"match_id"`
	ReplaySHA256    string    `json:"replay_sha256"`
	FactsSHA256     string    `json:"facts_sha256"`
	SourceEventTime time.Time `json:"source_event_time"`
}

// SnapshotInput is the pure, deterministic input to snapshot sealing. The
// baselines are RECOMPUTED from the included facts via Aggregate so every
// sealed baseline is provably derived from the sealed input manifest rather
// than trusted from caller-supplied cells.
type SnapshotInput struct {
	Scope            contracts.TournamentScopeV1
	Roster           RosterManifestV1
	Discovery        DiscoveryManifestV1
	Facts            []NormalizedMatchFacts
	Windows          CutoffWindow
	Patch            PatchWindow
	Binding          contracts.LiveSessionBindingV1
	SealedAt         time.Time
	GeneratedAt      time.Time
	ParserVersion    string
	AggregateVersion string
}

// SnapshotResult bundles the sealed snapshot manifest and the sealed baseline
// values it pins.
type SnapshotResult struct {
	Snapshot  contracts.HistoricalSnapshotManifestV1
	Baselines []contracts.HistoricalBaselineV1
}

// ErrActiveMatchIncluded is returned when the active match id appears among the
// verified facts; this is a hard rejection — the active match must never
// contribute facts to its own prematch snapshot.
var ErrActiveMatchIncluded = errors.New("snapshot: active match must not contribute facts")

// BuildSnapshot assembles, seals, and validates a HistoricalSnapshotManifestV1
// plus its HistoricalBaselineV1 values from pure inputs. It enforces:
//   - cutoff eligibility: any fact whose source event time is after the cutoff
//     is rejected as a late fact (excluded, never included);
//   - active-match exclusion: the active match is always excluded with reason
//     "active_match"; if it is somehow present among verified facts the build
//     fails closed (ErrActiveMatchIncluded);
//   - identity: only verified, replay-accessible completed matches are
//     included;
//   - content-addressed input manifest digest over the included facts.
//
// There is no ambient clock; SealedAt is supplied by the caller and must be
// before the live session start time.
func BuildSnapshot(in SnapshotInput) (*SnapshotResult, error) {
	if err := in.Scope.Validate(); err != nil {
		return nil, err
	}
	if err := in.Roster.ValidateAgainstScope(in.Scope); err != nil {
		return nil, err
	}
	if err := in.Discovery.ValidateAgainstScope(in.Scope, in.Roster); err != nil {
		return nil, err
	}
	if in.Binding.SessionID == "" || in.Binding.ActiveMatchID == "" || in.Binding.SessionStartTime.IsZero() || in.Binding.TournamentScopeID != in.Scope.ScopeID || in.Binding.TournamentScopeSHA256 != in.Scope.ContentSHA256 {
		return nil, errors.New("snapshot: invalid binding")
	}
	if in.SealedAt.IsZero() || !in.SealedAt.Before(in.Binding.SessionStartTime) {
		return nil, errors.New("snapshot: sealed_at must precede session start")
	}
	if in.GeneratedAt.IsZero() || in.GeneratedAt.After(in.Binding.SessionStartTime) {
		return nil, errors.New("snapshot: generated_at must not be after session start")
	}
	if in.ParserVersion == "" || in.AggregateVersion == "" {
		return nil, errors.New("snapshot: missing parser/aggregate version")
	}

	var included []contracts.HistoricalMatchRefV1
	var excluded []contracts.ExcludedMatchV1
	var inputEntries []InputManifestEntry
	var includedFacts []NormalizedMatchFacts
	maximum := time.Time{}
	factsByID := map[string]NormalizedMatchFacts{}
	for _, f := range in.Facts {
		if err := f.Validate(); err != nil {
			return nil, err
		}
		factsByID[f.MatchID] = f
	}
	// Active match must be excluded even if no fact exists for it.
	excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: in.Binding.ActiveMatchID, Reason: contracts.ExclusionActiveMatch})

	// First reject late facts explicitly so late-fact rejection is observable.
	for _, f := range in.Facts {
		if f.IdentityStatus != contracts.IdentityVerified {
			continue
		}
		if f.SourceEventTime.After(in.Scope.HistoryCutoff) {
			excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: f.MatchID, Reason: "late_fact"})
		}
		if f.MatchID == in.Binding.ActiveMatchID && f.IdentityStatus == contracts.IdentityVerified {
			return nil, ErrActiveMatchIncluded
		}
	}
	// Then collect included matches from the discovery manifest. A match is
	// included only when it is replay-accessible AND its fact fully correlates
	// with the discovery record on replay SHA, source time, patch, teams, and
	// participant ids. Any mismatch quarantines the match (excluded) rather
	// than entering the snapshot.
	for _, m := range in.Discovery.Matches {
		if m.MatchID == in.Binding.ActiveMatchID {
			continue
		}
		if m.State != MatchReplayAccessible {
			if m.State != MatchOmitted {
				excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: m.MatchID, Reason: "replay_not_accessible:" + m.State})
			}
			continue
		}
		f, ok := factsByID[m.MatchID]
		if !ok {
			excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: m.MatchID, Reason: "no_normalized_facts"})
			continue
		}
		if f.IdentityStatus != contracts.IdentityVerified {
			excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: m.MatchID, Reason: "identity_not_verified"})
			continue
		}
		if reason := correlateMatch(m, f); reason != "" {
			excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: m.MatchID, Reason: reason})
			continue
		}
		if f.SourceEventTime.IsZero() || f.SourceEventTime.After(in.Scope.HistoryCutoff) {
			excluded = append(excluded, contracts.ExcludedMatchV1{MatchID: m.MatchID, Reason: "late_fact"})
			continue
		}
		included = append(included, contracts.HistoricalMatchRefV1{
			MatchID:         m.MatchID,
			Completed:       true,
			SourceEventTime: f.SourceEventTime,
			ReplaySHA256:    f.ReplaySHA256,
			IdentityStatus:  contracts.IdentityVerified,
		})
		inputEntries = append(inputEntries, InputManifestEntry{
			MatchID:         m.MatchID,
			ReplaySHA256:    f.ReplaySHA256,
			FactsSHA256:     f.ContentSHA256,
			SourceEventTime: f.SourceEventTime,
		})
		includedFacts = append(includedFacts, f)
		if maximum.IsZero() || f.SourceEventTime.After(maximum) {
			maximum = f.SourceEventTime
		}
	}
	if len(included) == 0 {
		return nil, errors.New("snapshot: no eligible included matches")
	}
	sort.Slice(included, func(i, j int) bool { return included[i].MatchID < included[j].MatchID })
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].MatchID < excluded[j].MatchID })
	sort.Slice(inputEntries, func(i, j int) bool { return inputEntries[i].MatchID < inputEntries[j].MatchID })

	inputSHA, err := contentSHA256("input_manifest", inputEntries)
	if err != nil {
		return nil, err
	}
	discoverySHA := in.Discovery.ContentSHA256

	// Recompute baselines from the included facts so every sealed baseline is
	// provably derived from the sealed input manifest, not caller-supplied cells.
	cells, err := Aggregate(AggregateInput{
		Facts:         includedFacts,
		Roster:        in.Roster,
		Windows:       in.Windows,
		Patch:         in.Patch,
		ActiveMatchID: in.Binding.ActiveMatchID,
		GeneratedAt:   in.GeneratedAt,
	})
	if err != nil {
		return nil, err
	}
	// Seal baselines first to collect their content ids.
	var baselines []contracts.HistoricalBaselineV1
	var baselineIDs []string
	for _, c := range cells {
		b := contracts.HistoricalBaselineV1{
			SchemaVersion:         contracts.HistoricalBaselineSchemaV1,
			SnapshotID:            "", // filled after snapshot seal
			SnapshotContentSHA256: "", // filled after snapshot seal
			TournamentScopeID:     in.Scope.ScopeID,
			TournamentScopeSHA256: in.Scope.ContentSHA256,
			SessionID:             in.Binding.SessionID,
			ActiveMatchID:         in.Binding.ActiveMatchID,
			GeneratedTime:         in.GeneratedAt,
			Key:                   c.Key,
			Value:                 c.Value,
		}
		if err := contracts.SealHistoricalBaselineV1(&b); err != nil {
			return nil, err
		}
		baselines = append(baselines, b)
		baselineIDs = append(baselineIDs, b.BaselineID)
	}
	sort.Strings(baselineIDs)
	baselineIDs = sortedDedupe(baselineIDs)

	snap := contracts.HistoricalSnapshotManifestV1{
		SchemaVersion:           contracts.HistoricalSnapshotManifestSchemaV1,
		TournamentScopeID:       in.Scope.ScopeID,
		TournamentScopeSHA256:   in.Scope.ContentSHA256,
		Binding:                 in.Binding,
		CutoffTime:              in.Scope.HistoryCutoff,
		MaximumSourceEventTime:  maximum,
		SealedAt:                in.SealedAt,
		DiscoveryManifestSHA256: discoverySHA,
		InputManifestSHA256:     inputSHA,
		ParserVersion:           in.ParserVersion,
		AggregateVersion:        in.AggregateVersion,
		IncludedMatches:         included,
		ExcludedMatches:         excluded,
		BaselineContentSHA256:   baselineIDs,
	}
	if err := contracts.SealHistoricalSnapshotManifestV1(&snap); err != nil {
		return nil, err
	}
	// Backfill snapshot identity into baselines and re-seal so each baseline
	// is bound to the sealed snapshot, then validate the binding.
	for i := range baselines {
		baselines[i].SnapshotID = snap.SnapshotID
		baselines[i].SnapshotContentSHA256 = snap.ContentSHA256
		if err := contracts.SealHistoricalBaselineV1(&baselines[i]); err != nil {
			return nil, err
		}
		if err := baselines[i].ValidateAgainst(snap); err != nil {
			return nil, err
		}
	}
	if err := snap.ValidateAgainstScope(in.Scope); err != nil {
		return nil, err
	}
	return &SnapshotResult{Snapshot: snap, Baselines: baselines}, nil
}

// correlateMatch verifies a discovery record and a normalized fact refer to the
// same completed match by replay content identity, source event time, patch,
// teams, and (when discovery lists players) the exact ten-player participant
// set. A non-empty reason means the match must be quarantined, not included.
func correlateMatch(m DiscoveryMatch, f NormalizedMatchFacts) string {
	if m.ReplaySHA256 == "" || m.ReplaySHA256 != f.ReplaySHA256 {
		return "replay_identity_mismatch"
	}
	if !m.SourceEventTime.Equal(f.SourceEventTime) {
		return "source_time_mismatch"
	}
	if m.PatchID == "" || m.PatchID != f.PatchID {
		return "patch_mismatch"
	}
	if m.GameBuild == 0 || f.GameBuild == 0 || m.GameBuild != f.GameBuild {
		return "game_build_mismatch"
	}
	if m.RadiantTeamID != f.RadiantTeamID || m.DireTeamID != f.DireTeamID {
		return "team_mismatch"
	}
	if len(f.Participants) != 10 {
		return "incomplete_participants"
	}
	if len(m.PlayerPersonIDs) != 10 {
		return "discovery_participant_count_mismatch"
	}
	fset := map[string]bool{}
	for _, p := range f.Participants {
		fset[p.PersonID] = true
	}
	for _, pid := range m.PlayerPersonIDs {
		if !fset[pid] {
			return "participant_id_mismatch"
		}
	}
	return ""
}
