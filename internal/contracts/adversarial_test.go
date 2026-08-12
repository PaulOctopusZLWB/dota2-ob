package contracts_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

var (
	cutoff       = time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	sessionStart = cutoff.Add(time.Hour)
)

func validScope(t *testing.T) contracts.TournamentScopeV1 {
	t.Helper()
	teams := make([]contracts.TournamentTeamV1, 16)
	participants := make([]contracts.TournamentParticipantV1, 0, 80)
	for i := range teams {
		teams[i] = contracts.TournamentTeamV1{TeamID: "team-" + string(rune('a'+i)), RosterID: "roster-" + string(rune('a'+i)), EffectiveFrom: cutoff.Add(-180 * 24 * time.Hour), EffectiveUntil: sessionStart}
		for j := 0; j < 5; j++ {
			participants = append(participants, contracts.TournamentParticipantV1{PersonID: "person-" + string(rune('a'+i)) + string(rune('0'+j)), TeamID: teams[i].TeamID, Handle: "player", Role: "player", EffectiveFrom: cutoff.Add(-180 * 24 * time.Hour), EffectiveUntil: sessionStart})
		}
	}
	v := contracts.TournamentScopeV1{SchemaVersion: contracts.TournamentScopeSchemaV1, Edition: "ti-2026", SampledAt: cutoff, HistoryCutoff: cutoff, DiscoveryContractVersion: "discovery.v1", PatchID: "60", DotaPatch: "7.41", Teams: teams, Participants: participants, Sources: []contracts.PublicSourceV1{{URL: "https://www.dota2.com.cn/international/2026", RetrievedAt: cutoff}}, Discovery: contracts.DiscoveryPolicyV1{ContractVersion: "discovery.v1", Providers: []string{"opendota", "steam"}, PageLimit: 100, FullHistoryReplayTarget: 100, MinimumTeamMatches: 5, AllowedOutcomes: []string{"full_history_go", "historical_no_go", "restricted_history_go"}}}
	if err := contracts.SealTournamentScopeV1(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func validManifest(t *testing.T) contracts.HistoricalSnapshotManifestV1 {
	t.Helper()
	scope := validScope(t)
	v := contracts.HistoricalSnapshotManifestV1{
		SchemaVersion:     contracts.HistoricalSnapshotManifestSchemaV1,
		TournamentScopeID: scope.ScopeID, TournamentScopeSHA256: scope.ContentSHA256,
		Binding:    contracts.LiveSessionBindingV1{SessionID: "session-1", ActiveMatchID: "42", SessionStartTime: sessionStart, TournamentScopeID: scope.ScopeID, TournamentScopeSHA256: scope.ContentSHA256},
		CutoffTime: cutoff, MaximumSourceEventTime: cutoff.Add(-time.Hour), SealedAt: cutoff.Add(time.Minute),
		DiscoveryManifestSHA256: strings.Repeat("a", 64), InputManifestSHA256: strings.Repeat("b", 64), ParserVersion: "parser.v1", AggregateVersion: "aggregate.v1",
		IncludedMatches:       []contracts.HistoricalMatchRefV1{{MatchID: "41", Completed: true, SourceEventTime: cutoff.Add(-time.Hour), ReplaySHA256: strings.Repeat("c", 64), IdentityStatus: contracts.IdentityVerified}},
		ExcludedMatches:       []contracts.ExcludedMatchV1{{MatchID: "42", Reason: contracts.ExclusionActiveMatch}},
		BaselineContentSHA256: []string{strings.Repeat("d", 64)},
	}
	if err := contracts.SealHistoricalSnapshotManifestV1(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTournamentScopeAndHistoricalSnapshotEligibility(t *testing.T) {
	scope := validScope(t)
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	manifest := validManifest(t)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := manifest.ValidateAgainstScope(scope); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*contracts.HistoricalSnapshotManifestV1)
	}{
		{"late fact", func(v *contracts.HistoricalSnapshotManifestV1) {
			v.IncludedMatches[0].SourceEventTime = v.CutoffTime.Add(time.Millisecond)
		}},
		{"active match included", func(v *contracts.HistoricalSnapshotManifestV1) {
			v.IncludedMatches[0].MatchID = v.Binding.ActiveMatchID
		}},
		{"sealed after session", func(v *contracts.HistoricalSnapshotManifestV1) {
			v.SealedAt = v.Binding.SessionStartTime.Add(time.Millisecond)
		}},
		{"identity quarantined", func(v *contracts.HistoricalSnapshotManifestV1) {
			v.IncludedMatches[0].IdentityStatus = contracts.IdentityQuarantined
		}},
		{"scope mismatch", func(v *contracts.HistoricalSnapshotManifestV1) {
			v.Binding.TournamentScopeSHA256 = strings.Repeat("e", 64)
		}},
		{"content mismatch", func(v *contracts.HistoricalSnapshotManifestV1) { v.AggregateVersion = "other" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := manifest
			v.IncludedMatches = append([]contracts.HistoricalMatchRefV1(nil), manifest.IncludedMatches...)
			tc.mutate(&v)
			if err := v.Validate(); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestHistoricalBaselineEligibilityAndNullability(t *testing.T) {
	m := validManifest(t)
	value := contracts.Decimal("650.000")
	v := contracts.HistoricalBaselineV1{SchemaVersion: contracts.HistoricalBaselineSchemaV1, BaselineID: strings.Repeat("e", 64), SnapshotID: m.SnapshotID, SnapshotContentSHA256: m.ContentSHA256, TournamentScopeID: m.TournamentScopeID, TournamentScopeSHA256: m.TournamentScopeSHA256, SessionID: m.Binding.SessionID, ActiveMatchID: m.Binding.ActiveMatchID, GeneratedTime: m.SealedAt, Key: contracts.HistoricalBaselineKeyV1{RosterID: "roster-a", Patch: "7.41", Metric: "gpm", Window: "current_patch", SampleDefinition: "completed_matches"}, Value: contracts.HistoricalValueV1{State: contracts.ValuePresent, Value: &value, SampleSize: 1, PeriodStart: cutoff.Add(-time.Hour), PeriodEnd: cutoff.Add(-time.Minute), SourceCoveragePPM: 1_000_000}}
	if err := contracts.SealHistoricalBaselineV1(&v); err != nil {
		t.Fatal(err)
	}
	m.BaselineContentSHA256 = []string{v.BaselineID}
	if err := contracts.SealHistoricalSnapshotManifestV1(&m); err != nil {
		t.Fatal(err)
	}
	v.SnapshotID = m.SnapshotID
	v.SnapshotContentSHA256 = m.ContentSHA256
	if err := v.ValidateAgainst(m); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*contracts.HistoricalBaselineV1)
	}{
		{"absent value", func(v *contracts.HistoricalBaselineV1) { v.Value.State = contracts.ValueAbsent }},
		{"reversed period", func(v *contracts.HistoricalBaselineV1) { v.Value.PeriodStart = v.Value.PeriodEnd.Add(time.Second) }},
		{"coverage", func(v *contracts.HistoricalBaselineV1) { v.Value.SourceCoveragePPM = 1_000_001 }},
		{"snapshot mismatch", func(v *contracts.HistoricalBaselineV1) { v.SnapshotContentSHA256 = strings.Repeat("f", 64) }},
		{"active match mismatch", func(v *contracts.HistoricalBaselineV1) { v.ActiveMatchID = "other" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x := v
			tc.mutate(&x)
			if err := x.ValidateAgainst(m); err == nil {
				t.Fatal("invalid baseline accepted")
			}
		})
	}
}

func TestPolicyPortsRejectUnknownAndIncoherentRecords(t *testing.T) {
	unknown := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "c", SessionID: "s", Action: "overwrite_state", PolicyTimeMS: 1}
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown action accepted")
	}
	approve := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "c", SessionID: "s", Action: contracts.ActionApprove, TargetCandidateID: "candidate", PolicyTimeMS: 1}
	if err := approve.Validate(); err != nil {
		t.Fatal(err)
	}

	commit := contracts.PolicyCommitV1{SchemaVersion: contracts.PolicyCommitSchemaV1, SessionID: "s", CommitSequence: 1, CommandID: "c", PriorPolicyRevision: 0, ResultingPolicyRevision: 1, ResultingStateHash: strings.Repeat("a", 64), Publication: contracts.PublicationPublish, Decisions: []contracts.BroadcastDecisionV1{{SchemaVersion: "wrong", DecisionID: "d", SessionID: "other", PolicyRevision: 1, CommandID: "c", PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionShown, PolicyTimeMS: 1, Reason: "approved"}}, AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "a", SessionID: "s", EventType: "command_accepted", PolicyTimeMS: 1, CommandID: "c", Reason: "approved"}}}
	if err := commit.Validate(); err == nil {
		t.Fatal("incoherent nested decision accepted")
	}
}

func TestPolicyCheckpointCarriesBoundedRestartState(t *testing.T) {
	result := contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: "c", SessionID: "s", Status: contracts.CommandAccepted, PreviousRevision: 0, ResultingRevision: 1, DecisionIDs: []string{"d"}, Reason: "approved"}
	v := contracts.PolicyCheckpointV1{SchemaVersion: contracts.PolicyCheckpointSchemaV1, SessionID: "s", SnapshotID: strings.Repeat("b", 64), SnapshotContentSHA256: strings.Repeat("b", 64), CommitSequence: 1, LastObservationSequence: 9, PolicyRevision: 1, RuleVersion: "rules.v1", ConfigVersion: "config.v1", StateHash: strings.Repeat("a", 64), ReferencedCommitSHA256: strings.Repeat("c", 64), CreatedTimeMS: 1, CommandResults: []contracts.OperatorCommandResultV1{result}, PreviewCandidateIDs: []string{"candidate"}, Cooldowns: []contracts.RuleCooldownV1{{RuleID: "rule", UntilPolicyTimeMS: 2}}, Pins: []contracts.PolicyPinV1{{CandidateID: "candidate", DecisionID: "d"}}, EmergencyHide: true}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	commit := contracts.PolicyCommitV1{SchemaVersion: contracts.PolicyCommitSchemaV1, SessionID: "s", CommitSequence: 1, CommandID: "c", PriorPolicyRevision: 0, ResultingPolicyRevision: 1, ResultingStateHash: strings.Repeat("a", 64), Publication: contracts.PublicationHide, Decisions: []contracts.BroadcastDecisionV1{{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: "d", SessionID: "s", PolicyRevision: 1, CommandID: "c", PriorState: contracts.DecisionShown, ResultingState: contracts.DecisionEmergencyHidden, PolicyTimeMS: 1, Reason: "emergency_hide"}}, CommandResult: &result, AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "a", SessionID: "s", EventType: "command_accepted", PolicyTimeMS: 1, CommandID: "c", Reason: "approved"}}}
	hash, err := contracts.CanonicalSHA256(commit)
	if err != nil {
		t.Fatal(err)
	}
	v.ReferencedCommitSHA256 = hash
	if err := v.ValidateAgainstCommit(commit); err != nil {
		t.Fatal(err)
	}
	v.CommandResults = []contracts.OperatorCommandResultV1{result, result}
	if err := v.Validate(); err == nil {
		t.Fatal("duplicate command idempotency entry accepted")
	}
	v.CommandResults = []contracts.OperatorCommandResultV1{result}
	v.CommandResults = make([]contracts.OperatorCommandResultV1, 4097)
	if err := v.Validate(); err == nil {
		t.Fatal("unbounded command results accepted")
	}
	v.CommandResults = nil
	v.PreviewCandidateIDs = make([]string, 65)
	if err := v.Validate(); err == nil {
		t.Fatal("unbounded preview queue accepted")
	}
}

func TestVisibleOverlayRequiresEvidenceAndUnicodePrivacy(t *testing.T) {
	claim := &contracts.OverlayClaimV1{Title: strings.Repeat("界", 160), Body: "安全", AssetKey: "team.radiant"}
	v := contracts.OverlayStateV1{SchemaVersion: contracts.OverlayStateSchemaV1, SessionID: "s", PublicationTimeMS: 1, StaleDeadlineMS: 2, Visibility: "visible", DecisionID: "d", Confidence: "observed", SourceReceiveTime: ptrTime(time.Unix(1, 0).UTC()), Claim: claim}
	if utf8.RuneCountInString(claim.Title) != 160 {
		t.Fatal("fixture")
	}
	if err := v.Validate(); err == nil {
		t.Fatal("visible overlay without evidence accepted")
	}
	v.Evidence = []contracts.EvidenceRefV1{{RecordSchemaVersion: 2, SessionID: "s", Sequence: 1, ReceiveTime: time.Unix(1, 0).UTC(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: strings.Repeat("a", 64)}}
	if err := v.Validate(); err != nil {
		t.Fatalf("160 code points rejected: %v", err)
	}
	for _, bad := range []string{"safe\u202Eevil", "safe\u0007evil"} {
		v.Claim = &contracts.OverlayClaimV1{Title: bad, Body: "安全"}
		if err := v.Validate(); err == nil {
			t.Fatalf("prohibited text accepted: %q", bad)
		}
	}
}

func TestCausalAndOrderingInvariants(t *testing.T) {
	e1 := contracts.EvidenceRefV1{RecordSchemaVersion: 2, SessionID: "s", Sequence: 2, ReceiveTime: time.Unix(2, 0).UTC(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: strings.Repeat("a", 64)}
	e2 := e1
	e2.Sequence = 1
	e2.ReceiveTime = time.Unix(1, 0).UTC()
	overlay := contracts.OverlayStateV1{SchemaVersion: contracts.OverlayStateSchemaV1, SessionID: "s", PublicationTimeMS: 2, StaleDeadlineMS: 3, Visibility: "visible", DecisionID: "d", Evidence: []contracts.EvidenceRefV1{e1, e2}, Confidence: "observed", SourceReceiveTime: ptrTime(time.Unix(2, 0).UTC()), Claim: &contracts.OverlayClaimV1{Title: "安全", Body: "安全"}}
	if err := overlay.Validate(); err == nil {
		t.Fatal("unordered evidence accepted")
	}
	result := contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: "c", SessionID: "s", Status: contracts.CommandAccepted, PreviousRevision: 0, ResultingRevision: 1, DecisionIDs: []string{"missing"}, Reason: "approved"}
	commit := contracts.PolicyCommitV1{SchemaVersion: contracts.PolicyCommitSchemaV1, SessionID: "s", CommitSequence: 1, CommandID: "c", ResultingPolicyRevision: 1, ResultingStateHash: strings.Repeat("a", 64), Decisions: []contracts.BroadcastDecisionV1{{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: "d", SessionID: "s", PolicyRevision: 1, CommandID: "c", PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionShown, PolicyTimeMS: 1, Reason: "approved"}}, CommandResult: &result, AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "a", SessionID: "s", EventType: "command_accepted", PolicyTimeMS: 1, CommandID: "c", Reason: "approved"}}, Publication: contracts.PublicationPublish}
	if err := commit.Validate(); err == nil {
		t.Fatal("unknown result decision accepted")
	}
	checkpoint := contracts.PolicyCheckpointV1{SchemaVersion: contracts.PolicyCheckpointSchemaV1, SessionID: "s", SnapshotID: strings.Repeat("b", 64), SnapshotContentSHA256: strings.Repeat("c", 64), CommitSequence: 1, PolicyRevision: 1, RuleVersion: "r", ConfigVersion: "c", StateHash: strings.Repeat("a", 64), ReferencedCommitSHA256: strings.Repeat("d", 64), CreatedTimeMS: 1, CommandResults: []contracts.OperatorCommandResultV1{}, PreviewCandidateIDs: []string{}, Cooldowns: []contracts.RuleCooldownV1{}, Pins: []contracts.PolicyPinV1{}}
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("mismatched checkpoint snapshot identity accepted")
	}
}

func ptrTime(v time.Time) *time.Time { return &v }
