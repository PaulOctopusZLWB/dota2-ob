package presentation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
)

func evidence(sequence uint64) contracts.EvidenceRefV1 {
	return contracts.EvidenceRefV1{
		RecordSchemaVersion: 1,
		SessionID:           "session-m3",
		Sequence:            sequence,
		ReceiveTime:         time.UnixMilli(900 + int64(sequence)).UTC(),
		Source:              "gsi",
		ProviderVersion:     contracts.Absent[int64](),
		RawPayloadSHA256:    strings.Repeat("a", 64),
	}
}

func parameter(name, kind, value string) contracts.TypedParameterV1 {
	p := contracts.TypedParameterV1{Name: name, Type: kind}
	if kind == "decimal" {
		decimal := contracts.Decimal(value)
		p.DecimalValue = &decimal
	} else {
		p.StringValue = &value
	}
	return p
}

func candidate(key string, parameters ...contracts.TypedParameterV1) contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{
		SchemaVersion: contracts.InsightCandidateSchemaV1,
		CandidateID:   "candidate-m3", SessionID: "session-m3",
		RuleVersion: "rule.v1", ConfigVersion: "ti15.v1",
		LocalizationKey: key, Parameters: parameters, Evidence: []contracts.EvidenceRefV1{evidence(1)},
		Confidence: "observed", SampleSize: 12, Priority: 80,
		CreatedTimeMS: 800, ExpiryTimeMS: 4_000, Availability: "available",
	}
}

func decision() contracts.BroadcastDecisionV1 {
	return contracts.BroadcastDecisionV1{
		SchemaVersion: contracts.BroadcastDecisionSchemaV1,
		DecisionID:    "decision-m3", SessionID: "session-m3", PolicyRevision: 3,
		CandidateID: "candidate-m3", PriorState: contracts.DecisionQueued,
		ResultingState: contracts.DecisionShown, PolicyTimeMS: 1_000, Reason: "operator_approved",
	}
}

func TestCatalogCoversEveryAudienceKeyInBothLocales(t *testing.T) {
	want := []string{
		"insight.draft_context",
		"insight.economy_lead",
		"insight.item_timing",
		"insight.lane_checkpoint",
		"insight.objective_exchange",
		"insight.teamfight_readiness",
	}
	if got := presentation.AudienceKeys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audience keys = %v, want %v", got, want)
	}
	if presentation.CatalogVersion() == "" || presentation.TerminologyVersion() == "" {
		t.Fatal("catalog and terminology versions must be pinned")
	}
	for _, locale := range []string{"zh-CN", "en-US"} {
		for _, key := range want {
			if !presentation.HasMessage(locale, key) {
				t.Errorf("%s missing %s", locale, key)
			}
		}
	}
	if presentation.HasMessage("zh-CN", "unused.key") {
		t.Fatal("unexpected unused message key")
	}
}

func TestVersionedTerminologyCoversBroadcastKindsAndPreservesOfficialHandles(t *testing.T) {
	tests := []struct{ kind, key, zh, en string }{
		{"team", "radiant", "天辉", "Radiant"},
		{"role", "hard_support", "五号位", "Hard Support"},
		{"hero", "npc_dota_hero_axe", "斧王", "Axe"},
		{"item", "item_blink", "闪烁匕首", "Blink Dagger"},
		{"ability", "axe_berserkers_call", "狂战士之吼", "Berserker's Call"},
		{"metric", "net_worth", "经济", "Net Worth"},
		{"objective", "roshan", "肉山", "Roshan"},
	}
	for _, tc := range tests {
		if got, ok := presentation.ResolveTerm("zh-CN", tc.kind, tc.key); !ok || got != tc.zh {
			t.Errorf("zh term %s/%s = %q, %v", tc.kind, tc.key, got, ok)
		}
		if got, ok := presentation.ResolveTerm("en-US", tc.kind, tc.key); !ok || got != tc.en {
			t.Errorf("en term %s/%s = %q, %v", tc.kind, tc.key, got, ok)
		}
	}
	if got, ok := presentation.ResolveTerm("zh-CN", "player_handle", "Ame"); !ok || got != "Ame" {
		t.Fatalf("official handle changed: %q, %v", got, ok)
	}
	if _, ok := presentation.ResolveTerm("zh-CN", "hero", "unknown_hero"); ok {
		t.Fatal("unknown hero was guessed")
	}
}

func TestBuildLocalizesAllTemplatesWithoutLeakingSemanticKeys(t *testing.T) {
	tests := []struct {
		key    string
		params []contracts.TypedParameterV1
		wantZH string
		wantEN string
	}{
		{"insight.draft_context", []contracts.TypedParameterV1{parameter("player", "player_handle", "Ame"), parameter("hero", "hero", "npc_dota_hero_axe"), parameter("role", "role", "carry")}, "Ame 的斧王", "Ame on Axe"},
		{"insight.economy_lead", []contracts.TypedParameterV1{parameter("team", "team", "radiant"), parameter("net_worth_lead", "decimal", "5000")}, "天辉建立经济领先", "Radiant builds a net-worth lead"},
		{"insight.item_timing", []contracts.TypedParameterV1{parameter("player", "player_handle", "Ame"), parameter("item", "item", "item_blink"), parameter("timing_delta_seconds", "decimal", "75")}, "Ame 的闪烁匕首", "Ame's Blink Dagger"},
		{"insight.lane_checkpoint", []contracts.TypedParameterV1{parameter("team", "team", "dire"), parameter("checkpoint_minute", "decimal", "10"), parameter("net_worth_delta", "decimal", "1800")}, "夜魇十分钟对线检查点", "Dire 10-minute lane checkpoint"},
		{"insight.objective_exchange", []contracts.TypedParameterV1{parameter("team", "team", "radiant"), parameter("objective", "objective", "roshan"), parameter("net_worth_delta", "decimal", "2200")}, "天辉拿下肉山", "Radiant secures Roshan"},
		{"insight.teamfight_readiness", []contracts.TypedParameterV1{parameter("team", "team", "dire"), parameter("ready_count", "decimal", "4")}, "夜魇团战资源就绪", "Dire teamfight resources ready"},
	}
	for _, tc := range tests {
		for _, locale := range []struct{ value, want string }{{"zh-CN", tc.wantZH}, {"en-US", tc.wantEN}} {
			state, err := presentation.Build(presentation.BuildInput{
				Locale: locale.value, Candidate: candidate(tc.key, tc.params...), Decision: decision(),
				PublicationTimeMS: 1_100, StaleDeadlineMS: 2_600,
			})
			if err != nil {
				t.Fatalf("%s %s build: %v", locale.value, tc.key, err)
			}
			if err := state.Validate(); err != nil {
				t.Fatalf("%s %s invalid state: %v", locale.value, tc.key, err)
			}
			if state.Claim == nil || !strings.Contains(state.Claim.Title, locale.want) || strings.Contains(state.Claim.Body, "insight.") {
				t.Fatalf("%s %s claim = %#v", locale.value, tc.key, state.Claim)
			}
		}
	}
}

func TestPreviewLocalizesQueuedCandidateWithoutInventingDecision(t *testing.T) {
	candidate := candidate(
		"insight.objective_exchange",
		parameter("team", "team", "radiant"),
		parameter("objective", "objective", "roshan"),
		parameter("net_worth_delta", "decimal", "2200"),
	)
	claim, err := presentation.Preview("zh-CN", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Title != "天辉拿下肉山" || !strings.Contains(claim.Body, "2200") {
		t.Fatalf("preview claim = %#v", claim)
	}
}

func TestUnavailablePreviewIsLocalizedAndMakesNoAnalyticalClaim(t *testing.T) {
	claim := presentation.UnavailablePreview("zh-CN")
	if claim.Title != "分析暂不可展示" || claim.Body != "展示参数未通过安全校验，可拒绝此条或使用紧急隐藏。" || claim.AssetKey != "" {
		t.Fatalf("fallback claim = %#v", claim)
	}
}

func TestBuildFailsClosedForIncompatibleOrUnsafeCandidate(t *testing.T) {
	valid := candidate("insight.draft_context", parameter("player", "player_handle", "Ame"), parameter("hero", "hero", "npc_dota_hero_axe"), parameter("role", "role", "carry"))
	tests := []struct {
		name   string
		mutate func(*contracts.InsightCandidateV1, *contracts.BroadcastDecisionV1)
	}{
		{"missing parameter", func(c *contracts.InsightCandidateV1, _ *contracts.BroadcastDecisionV1) {
			c.Parameters = c.Parameters[:2]
		}},
		{"extra parameter", func(c *contracts.InsightCandidateV1, _ *contracts.BroadcastDecisionV1) {
			c.Parameters = append(c.Parameters, parameter("extra", "string", "x"))
		}},
		{"wrong type", func(c *contracts.InsightCandidateV1, _ *contracts.BroadcastDecisionV1) {
			c.Parameters[1] = parameter("hero", "string", "npc_dota_hero_axe")
		}},
		{"unsafe handle", func(c *contracts.InsightCandidateV1, _ *contracts.BroadcastDecisionV1) {
			c.Parameters[0] = parameter("player", "player_handle", "<img>")
		}},
		{"unknown term", func(c *contracts.InsightCandidateV1, _ *contracts.BroadcastDecisionV1) {
			c.Parameters[1] = parameter("hero", "hero", "unknown")
		}},
		{"candidate mismatch", func(_ *contracts.InsightCandidateV1, d *contracts.BroadcastDecisionV1) { d.CandidateID = "other" }},
		{"not displayable", func(_ *contracts.InsightCandidateV1, d *contracts.BroadcastDecisionV1) {
			d.ResultingState = contracts.DecisionRejected
		}},
		{"expired", func(c *contracts.InsightCandidateV1, _ *contracts.BroadcastDecisionV1) { c.ExpiryTimeMS = 1_000 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, d := valid, decision()
			c.Parameters = append([]contracts.TypedParameterV1(nil), valid.Parameters...)
			tc.mutate(&c, &d)
			if _, err := presentation.Build(presentation.BuildInput{Locale: "zh-CN", Candidate: c, Decision: d, PublicationTimeMS: 1_100, StaleDeadlineMS: 2_600}); err == nil {
				t.Fatal("unsafe input produced a visible state")
			}
		})
	}
}

func TestHiddenProducesClaimFreeValidatedState(t *testing.T) {
	state, err := presentation.Hidden("session-m3", 1_100, 2_600, "emergency_hide")
	if err != nil || state.Validate() != nil {
		t.Fatalf("hidden state invalid: %#v err=%v", state, err)
	}
	if state.Visibility != "hidden" || state.Claim != nil || len(state.Evidence) != 0 {
		t.Fatalf("hidden state retained claim: %#v", state)
	}
}
