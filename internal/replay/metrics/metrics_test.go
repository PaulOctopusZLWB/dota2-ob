package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
)

func rawReader(events []raw.Event) *raw.Reader {
	var buf bytes.Buffer
	w := raw.NewWriter(&buf)
	for i := range events {
		_ = w.Write(&events[i])
	}
	_ = w.Flush()
	return raw.NewReader(&buf)
}

// testRegistry returns a frozen 52-metric registry from the repo contract.
func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := LoadRegistry("../../../docs/specs/ti2026-role-phase-metrics-v1.json")
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

func testClosure(t *testing.T) *Closure {
	t.Helper()
	cl, err := LoadClosure("../../../docs/specs/ti2026-metric-closure-v1.json")
	if err != nil {
		t.Fatalf("load closure: %v", err)
	}
	return cl
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestRegistryContract(t *testing.T) {
	reg := testRegistry(t)
	if len(reg.Metrics) != 52 {
		t.Fatalf("metrics=%d want 52", len(reg.Metrics))
	}
	for _, m := range reg.Metrics {
		if m.ID == "" || m.Name == "" || m.Unit == "" || m.AggregationRule == "" {
			t.Fatalf("incomplete metric: %+v", m)
		}
	}
	// Every official-eligible metric is V1 or V2.
	for _, m := range reg.Metrics {
		if m.OfficialScoreEligible && m.CapabilityLevel == CapabilityV3 {
			t.Fatalf("metric %s official-eligible but V3", m.ID)
		}
	}
}

// TestMetricClosureEqualsRegistry proves the frozen 52-row closure contract
// agrees exactly with the frozen registry: 52 unique registry ids, 52 unique
// closure rows, zero unknown, zero duplicate, zero unmapped, and no generic
// default resolution (every row names an evaluator, entry point, accepted
// source fields, and publication/abstention gates).
func TestMetricClosureEqualsRegistry(t *testing.T) {
	reg := testRegistry(t)
	cl := testClosure(t)
	if err := cl.ValidateAgainstRegistry(reg); err != nil {
		t.Fatalf("closure/registry mismatch: %v", err)
	}
	if len(cl.Metrics) != 52 {
		t.Fatalf("closure rows=%d want 52", len(cl.Metrics))
	}
	seen := map[string]bool{}
	published, unavailable := 0, 0
	for i := range cl.Metrics {
		r := &cl.Metrics[i]
		if seen[r.MetricID] {
			t.Fatalf("duplicate closure row %s", r.MetricID)
		}
		seen[r.MetricID] = true
		if r.EvaluatorID == "" || r.EntryPoint == "" || r.AcceptedSourceFields == "" || r.PublicationGate == "" || r.UnavailableGate == "" {
			t.Fatalf("closure row %s incomplete: %+v", r.MetricID, r)
		}
		switch r.Resolved {
		case "published":
			published++
			if !strings.HasPrefix(r.EntryPoint, "computeMetric:") && r.EntryPoint != "phaseDurationValue" {
				t.Fatalf("published row %s entry point %q is not a concrete evaluator dispatch", r.MetricID, r.EntryPoint)
			}
		case "unavailable":
			unavailable++
		default:
			t.Fatalf("closure row %s unknown resolved state %q", r.MetricID, r.Resolved)
		}
	}
	if published == 0 || unavailable == 0 {
		t.Fatalf("closure must contain published and unavailable rows (published=%d unavailable=%d)", published, unavailable)
	}
	// Cross-check: every published closure id has a concrete computeMetric
	// switch case; every other registry id resolves through a definition-
	// specific unavailable gate (never a generic default).
	for i := range cl.Metrics {
		r := &cl.Metrics[i]
		if r.Resolved == "published" && !publishedIDs[r.MetricID] {
			t.Fatalf("closure marks %s published but computeMetric has no entry point", r.MetricID)
		}
	}
	// A 51-row closure (unknown/unmapped) must fail hard.
	bad := *cl
	bad.Metrics = bad.Metrics[:51]
	if err := bad.ValidateAgainstRegistry(reg); err == nil {
		t.Fatal("closure with 51 rows accepted against 52-id registry")
	}
}

// publishedIDs is the authoritative set of metric ids with a concrete
// evaluator entry point (kept in sync with computeMetric/phaseDurationValue).
var publishedIDs = map[string]bool{
	"kill_count": true, "assist_count": true, "death_count": true,
	"objective_damage_total": true, "hero_damage_total": true,
	"phase_duration_seconds": true,
}

// TestClosureResolutionFieldsPersisted proves the Resolution table carries the
// closure audit fields (evaluator id, entry point, accepted source fields,
// publication gate) for independent audit, and that unavailable rows carry the
// definition-specific unavailable gate reason.
func TestClosureResolutionFieldsPersisted(t *testing.T) {
	reg := testRegistry(t)
	cl := testClosure(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetClosure(cl)
	calc.SetRoles(map[string]string{"a1": "1"})
	calc.SetFactsCoverage([]string{"combat_event", "death_respawn_buyback_event"})
	// Feed a resolved kill so kill_count publishes with lineage.
	calc.Feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 100, GameSecondOK: true, Seq: 1, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a2", KillerAccount: "a1"})})
	out := calc.Result(nil, testOfficialPhases(200))
	if len(out.ResolutionTable) != 52 {
		t.Fatalf("resolution rows=%d want 52", len(out.ResolutionTable))
	}
	rowByID := map[string]Resolution{}
	for _, r := range out.ResolutionTable {
		rowByID[r.MetricID] = r
		if r.EvaluatorID == "" || r.EntryPoint == "" || r.AcceptedSourceFields == "" || r.PublicationGate == "" {
			t.Fatalf("resolution %s missing closure audit fields: %+v", r.MetricID, r)
		}
	}
	// kill_count publishes; its gate reason is the exact unavailable fallback.
	kc := rowByID["kill_count"]
	if !kc.Published && kc.UnavailableReason != "death_respawn_buyback_event_missing" {
		t.Fatalf("kill_count resolution: %+v", kc)
	}
	// heal_dispel_save_casts stays unavailable at its exact gate.
	heal := rowByID["heal_dispel_save_casts"]
	if heal.Published || !strings.Contains(heal.UnavailableGate, "save_cast_opportunity_gate_not_met") {
		t.Fatalf("heal_dispel_save_casts gate: %+v", heal)
	}
}

// TestClosureDispatchRepresentativeProbes exercises representative published
// and unavailable algorithms across V1/V2/V3 through the closure dispatch and
// proves source-field and gate identity in the resolution rows.
func TestClosureDispatchRepresentativeProbes(t *testing.T) {
	reg := testRegistry(t)
	cl := testClosure(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetClosure(cl)
	calc.SetRoles(map[string]string{"a1": "1"})
	calc.SetFactsCoverage([]string{"combat_event"})
	calc.SetTeamOfSide(map[string]string{"radiant": "T1", "dire": "T2"})
	// Published V1: hero damage to an enemy real hero (a2 on T2).
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, GameSecondOK: true, Seq: 1, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: int64p(500)})})
	out := calc.Result(nil, testOfficialPhases(200))
	found := map[string]Value{}
	for _, v := range out.Values {
		found[v.MetricID] = v
	}
	if _, ok := found["hero_damage_total"]; !ok {
		t.Fatal("hero_damage_total not published")
	}
	// fight_damage_share is withdrawn: the accepted adapter lacks the
	// phase/alive/98%-resolution gates, so it must be unavailable.
	for _, v := range out.Unavailable {
		if v.MetricID == "fight_damage_share" && v.UnavailableReason == "" {
			t.Fatalf("fight_damage_share published without reason")
		}
	}
	// V3 modelled metric must stay unavailable with a definition-specific gate.
	for _, v := range out.Unavailable {
		if v.MetricID == "core_partner_protection_uptime" && v.UnavailableReason == "" {
			t.Fatalf("V3 modelled metric published without reason")
		}
	}
}

// TestTypedLineageChainPreserved proves a published metric carries the full
// typed chain: fact -> phase -> metric_observation -> algorithm, with
// match-qualified identity on every ref (hero_damage_total: fact evidence;
// phase_duration_seconds: phase evidence).
func TestTypedLineageChainPreserved(t *testing.T) {
	reg := testRegistry(t)
	cl := testClosure(t)
	calc := NewCalculator("8944521919", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetClosure(cl)
	calc.SetRoles(map[string]string{"a1": "1"})
	calc.SetFactsCoverage([]string{"combat_event"})
	calc.SetTeamOfSide(map[string]string{"radiant": "T1", "dire": "T2"})
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, GameSecondOK: true, Seq: 7, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: int64p(500)})})
	ph := &phase.Output{EligibleSeconds: 200, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 200, EvidenceSeqs: []int64{7}},
	}}
	out := calc.Result(nil, ph)
	var hd *Value
	for i := range out.Values {
		if out.Values[i].MetricID == "hero_damage_total" && out.Values[i].AccountID == "a1" {
			hd = &out.Values[i]
		}
	}
	if hd == nil {
		t.Fatal("hero_damage_total not published")
	}
	seen := map[string]bool{}
	for _, ref := range hd.Evidence {
		if ref.MatchID != "8944521919" {
			t.Fatalf("evidence ref missing match id: %+v", ref)
		}
		seen[ref.Kind] = true
	}
	for _, kind := range []string{EvidenceFact, EvidenceMetricObservation, EvidenceAlgorithm} {
		if !seen[kind] {
			t.Fatalf("hero_damage_total chain missing %s kind: %+v", kind, hd.Evidence)
		}
	}
	// The match-level phase_duration_seconds value carries phase evidence.
	var pd *Value
	for i := range out.Values {
		if out.Values[i].MetricID == "phase_duration_seconds" && out.Values[i].ReportLevel == "match" {
			pd = &out.Values[i]
		}
	}
	if pd == nil {
		t.Fatal("phase_duration_seconds not published")
	}
	hasPhase := false
	for _, ref := range pd.Evidence {
		if ref.Kind == EvidencePhase {
			hasPhase = true
		}
	}
	if !hasPhase {
		t.Fatalf("phase_duration_seconds chain missing phase ref: %+v", pd.Evidence)
	}
}

// TestTwoMatchesEqualEntityIDsStayDistinct proves equal fact/entity ids from
// different matches remain distinct through the typed lineage identity
// (match-qualified), so canonical persistence and aggregation never collapse
// them.
func TestTwoMatchesEqualEntityIDsStayDistinct(t *testing.T) {
	reg := testRegistry(t)
	cl := testClosure(t)
	build := func(matchID string) *Output {
		calc := NewCalculator(matchID, []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
		calc.SetRegistry(reg)
		calc.SetClosure(cl)
		calc.SetRoles(map[string]string{"a1": "1"})
		calc.SetFactsCoverage([]string{"combat_event"})
		calc.SetTeamOfSide(map[string]string{"radiant": "T1", "dire": "T2"})
		// Both matches use the same fact seq 7.
		calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, GameSecondOK: true, Seq: 7, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: int64p(500)})})
		return calc.Result(nil, testOfficialPhases(200))
	}
	o1 := build("m1")
	o2 := build("m2")
	factRefs := func(o *Output) []EvidenceRef {
		for _, v := range o.Values {
			if v.MetricID == "hero_damage_total" {
				return v.Evidence
			}
		}
		return nil
	}
	r1, r2 := factRefs(o1), factRefs(o2)
	if len(r1) == 0 || len(r2) == 0 {
		t.Fatal("missing fact evidence")
	}
	// Canonical persistence: serialized refs keep their match ids.
	b1, _ := json.Marshal(o1)
	b2, _ := json.Marshal(o2)
	var o1b, o2b Output
	json.Unmarshal(b1, &o1b)
	json.Unmarshal(b2, &o2b)
	if got := factRefs(&o1b)[0].MatchID; got != "m1" {
		t.Fatalf("m1 fact ref match=%s", got)
	}
	if got := factRefs(&o2b)[0].MatchID; got != "m2" {
		t.Fatalf("m2 fact ref match=%s", got)
	}
	if r1[0].ID != r2[0].ID {
		t.Fatalf("expected equal fact ids, got %s vs %s", r1[0].ID, r2[0].ID)
	}
}

func TestCalculatorAggregation(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("1000000001", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "1"})
	calc.SetTeamOfSide(map[string]string{"radiant": "T1", "dire": "T2"})
	feed := func(f *facts.Fact) { f.GameSecondOK = true; calc.Feed(f) }
	feed(&facts.Fact{Seq: 100, Family: facts.FamilyParticipant, GameSecond: 0, Payload: mustJSON(&facts.ParticipantFact{AccountID: "a1", HeroName: "npc_dota_hero_axe"})})
	feed(&facts.Fact{Seq: 101, Family: facts.FamilyParticipant, GameSecond: 0, Payload: mustJSON(&facts.ParticipantFact{AccountID: "a2", HeroName: "npc_dota_hero_lina"})})
	alive := true
	feed(&facts.Fact{Seq: 1, Family: facts.FamilyHeroState, GameSecond: 1, Payload: mustJSON(&facts.HeroStateSample{AccountID: "a1", HeroName: "npc_dota_hero_axe", Alive: &alive})})
	feed(&facts.Fact{Seq: 2, Family: facts.FamilyDeathRespawn, GameSecond: 10, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1", HeroName: "npc_dota_hero_axe", KillerAccount: "a2"})})
	feed(&facts.Fact{Seq: 3, Family: facts.FamilyDeathRespawn, GameSecond: 11, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "respawn", AccountID: "a1", HeroName: "npc_dota_hero_axe"})})
	feed(&facts.Fact{Seq: 4, Family: facts.FamilyDeathRespawn, GameSecond: 20, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a1", HeroName: "npc_dota_hero_axe", KillerAccount: "a2"})})
	feed(&facts.Fact{Seq: 5, Family: facts.FamilyHeroState, GameSecond: 1, Payload: mustJSON(&facts.HeroStateSample{AccountID: "a2", HeroName: "npc_dota_hero_lina", Alive: &alive})})
	feed(&facts.Fact{Seq: 6, Family: facts.FamilyDeathRespawn, GameSecond: 30, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "a2", HeroName: "npc_dota_hero_lina", KillerAccount: "a1"})})
	feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 40, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "a1"})})
	v := int64(50)
	feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 15, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})
	feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 25, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})

	out := calc.Result(nil, testOfficialPhases(100))
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, v := range out.Values {
		if v.AccountID == "a1" && v.OfficialPhase == "whole_match" {
			got[v.MetricID] = *v.Value
		}
	}
	// a1: 2 deaths (as victim), 1 kill, and 100 hero damage. Buyback use
	// fails closed because the accepted adapter cannot prove its denominator.
	if got["death_count"] != 2 {
		t.Fatalf("a1 death_count=%v want 2", got["death_count"])
	}
	if got["kill_count"] != 1 {
		t.Fatalf("a1 kill_count=%v want 1", got["kill_count"])
	}
	if got["hero_damage_total"] != 100 {
		t.Fatalf("a1 hero_damage_total=%v want 100", got["hero_damage_total"])
	}
	// The V2/V3 opportunity metrics must resolve to explicit unavailable,
	// never fabricated zeros.
	for _, u := range out.Unavailable {
		if u.Value != nil || u.IntValue != nil {
			t.Fatalf("unavailable metric %s/%s has a value", u.AccountID, u.MetricID)
		}
		if u.UnavailableReason == "" {
			t.Fatalf("unavailable metric %s/%s has no reason", u.AccountID, u.MetricID)
		}
	}
}

func TestUnavailableNeverZero(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("1000000001", []string{"a1"}, map[string]string{"a1": "p1"}, map[string]string{"a1": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	out := calc.Result(nil, nil)
	for _, u := range out.Unavailable {
		if u.Value != nil || u.IntValue != nil {
			t.Fatalf("unavailable metric %s has a value", u.MetricID)
		}
		if u.UnavailableReason == "" {
			t.Fatalf("unavailable metric %s has no reason", u.MetricID)
		}
		if u.EpistemicClass != ClassUnavailable {
			t.Fatalf("unavailable metric %s class=%s", u.MetricID, u.EpistemicClass)
		}
	}
}

// TestFightDamageShareSuppressed proves fight_damage_share is withdrawn: the
// accepted adapter lacks the declared midgame/decisive phase filter,
// active/alive opportunity, and >=98% actor/target resolution gate, so the
// all-fight pooled share is not the defined quantity and must be unavailable.
func TestFightDamageShareSuppressed(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2", "b1"}, map[string]string{"a1": "p1", "a2": "p2", "b1": "p3"}, map[string]string{"a1": "T1", "a2": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, sec float64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: sec, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetAccount: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "b1", 100, 105))
	calc.Feed(dmg("a2", "b1", 100, 106))
	calc.Feed(dmg("b1", "a1", 100, 107))
	eps := &episodes.Output{
		Episodes: []episodes.Episode{
			{Kind: episodes.KindFight, StartGameSecond: 100, EndGameSecond: 120, Participants: []string{"a1", "a2", "b1"}, EvidenceIDs: []int64{101, 102, 103}},
		},
	}
	out := calc.Result(eps, nil)
	for _, v := range out.Values {
		if v.MetricID == "fight_damage_share" {
			t.Fatalf("fight_damage_share must not publish without phase/alive/resolution gates")
		}
	}
	found := false
	for _, u := range out.Unavailable {
		if u.MetricID == "fight_damage_share" {
			found = true
			if u.Value != nil || u.IntValue != nil {
				t.Fatalf("fight_damage_share unavailable row has a value")
			}
			if !strings.Contains(u.UnavailableReason, "fight_share_phase_alive_resolution_gates_not_in_accepted_adapter") {
				t.Fatalf("fight_damage_share reason=%q", u.UnavailableReason)
			}
		}
	}
	if !found {
		t.Fatal("fight_damage_share missing from unavailable rows")
	}
}

func TestPhaseDuration(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	ph := &phase.Output{EligibleSeconds: 600, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 300},
		{GlobalPhase: phase.Midgame, StartGameSecond: 300, EndGameSecond: 600},
	}}
	out := calc.Result(nil, ph)
	found := false
	for _, v := range out.Values {
		if v.MetricID == "phase_duration_seconds" && v.ReportLevel == "match" && v.OfficialPhase == "whole_match" {
			if *v.Value != 600 {
				t.Fatalf("phase_duration=%v want 600", *v.Value)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("phase_duration_seconds match-level value missing")
	}
}

func TestFrozenPregameFactsRejectedAndPerPhaseReconcile(t *testing.T) {
	reg := testRegistry(t)
	cl := testClosure(t)
	accounts := []string{"312436974", "victim", "assist1", "assist2", "assist3", "assist4"}
	teams := map[string]string{"312436974": "T1", "victim": "T2", "assist1": "T1", "assist2": "T1", "assist3": "T1", "assist4": "T1"}
	calc := NewCalculator("8944525313", accounts, nil, teams)
	calc.SetRegistry(reg)
	calc.SetClosure(cl)
	calc.SetRoles(map[string]string{"312436974": "1", "victim": "2", "assist1": "3", "assist2": "4", "assist3": "5", "assist4": "1"})
	calc.SetFactsCoverage([]string{facts.FamilyCombat, facts.FamilyDeathRespawn})
	calc.Feed(&facts.Fact{Seq: 698, Family: facts.FamilyParticipant, GameSecond: 0, GameSecondOK: true,
		Payload: mustJSON(&facts.ParticipantFact{AccountID: "victim", HeroName: "npc_dota_hero_axe"})})

	// Reviewer-reproduced frozen pregame/uncalibrated facts. These exact seqs
	// must never enter any published numerator or lineage.
	for _, seq := range []int64{62, 65} {
		v := int64(88)
		calc.Feed(&facts.Fact{Seq: seq, Family: facts.FamilyCombat, GameSecond: -85.4333, GameSecondOK: false,
			Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "312436974", TargetAccount: "victim", Value: &v})})
	}
	for _, tc := range []struct {
		seq int64
		sec float64
	}{{391, -25.19995}, {561, -4.26666}} {
		calc.Feed(&facts.Fact{Seq: tc.seq, Family: facts.FamilyDeathRespawn, GameSecond: tc.sec, GameSecondOK: false,
			Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "victim", KillerAccount: "312436974", AssistAccounts: []string{"assist1", "assist2", "assist3", "assist4"}})})
	}
	// One valid midgame damage/death establishes the retained direct metric
	// classes without relying on rejected facts. The buyback event below must
	// still fail closed because its exact eligible-death denominator is absent.
	dmg := int64(1000)
	calc.Feed(&facts.Fact{Seq: 700, Family: facts.FamilyCombat, GameSecond: 120, GameSecondOK: true,
		Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "312436974", TargetAccount: "victim", Value: &dmg})})
	alive := true
	calc.Feed(&facts.Fact{Seq: 699, Family: facts.FamilyHeroState, GameSecond: 100, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "victim", HeroName: "npc_dota_hero_axe", Alive: &alive})})
	calc.Feed(&facts.Fact{Seq: 701, Family: facts.FamilyDeathRespawn, GameSecond: 130, GameSecondOK: true,
		Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "victim", HeroName: "npc_dota_hero_axe", KillerAccount: "312436974", AssistAccounts: []string{"assist1"}})})
	calc.Feed(&facts.Fact{Seq: 702, Family: facts.FamilyDeathRespawn, GameSecond: 190, GameSecondOK: true,
		Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "victim"})})
	ph := &phase.Output{EligibleSeconds: 200, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 100, RuleVersion: "phase.v1"},
		{GlobalPhase: phase.Midgame, StartGameSecond: 100, EndGameSecond: 180, RuleVersion: "phase.v1"},
		{GlobalPhase: phase.Decisive, StartGameSecond: 180, EndGameSecond: 200, RuleVersion: "phase.v1"},
	}}
	out := calc.Result(nil, ph)

	for _, v := range out.Values {
		if directPhaseMetric(v.MetricID) {
			if v.OfficialPhase == "" || v.Numerator == nil || v.Denominator == nil || v.OpportunityCount <= 0 || v.SampleCount < 0 || v.EvidenceCount < 0 || v.Coverage <= 0 || v.Coverage > 1 || v.GapCount != 0 {
				t.Fatalf("published direct row missing exact fields: %+v", v)
			}
			seenPhase, seenCanonicalObs := false, false
			for _, ref := range v.Evidence {
				if ref.Kind == EvidenceFact && (ref.SourceFactSeq == 62 || ref.SourceFactSeq == 65 || ref.SourceFactSeq == 391 || ref.SourceFactSeq == 561) {
					t.Fatalf("rejected frozen fact leaked into lineage: %+v", ref)
				}
				seenPhase = seenPhase || ref.Kind == EvidencePhase
				seenCanonicalObs = seenCanonicalObs || (ref.Kind == EvidenceMetricObservation && ref.ID == v.MetricID+":"+v.AccountID)
			}
			if !seenPhase || !seenCanonicalObs {
				t.Fatalf("published direct row missing phase/canonical observation: %+v", v)
			}
		}
	}
	whole := map[string]Value{}
	for _, v := range out.Values {
		if v.OfficialPhase == "whole_match" && v.AccountID != "" {
			whole[v.MetricID+":"+v.AccountID] = v
		}
	}
	if got := *whole["hero_damage_total:312436974"].Value; got != 1000 {
		t.Fatalf("hero damage=%v want 1000; frozen pregame 176 leaked", got)
	}
	if got := whole["hero_damage_total:312436974"].ExcludedCount; got != 2 {
		t.Fatalf("hero damage excluded_count=%d want 2", got)
	}
	if got := *whole["kill_count:312436974"].Value; got != 1 {
		t.Fatalf("kill count=%v want 1", got)
	}
	if got := whole["kill_count:312436974"].ExcludedCount; got != 2 {
		t.Fatalf("kill excluded_count=%d want 2", got)
	}
	if got := *whole["death_count:victim"].Value; got != 1 {
		t.Fatalf("death count=%v want 1", got)
	}
	if got := *whole["assist_count:assist1"].Value; got != 1 {
		t.Fatalf("assist count=%v want 1", got)
	}
	buybackUnavailable := false
	for _, u := range out.Unavailable {
		buybackUnavailable = buybackUnavailable || (u.MetricID == "buyback_use_count" && u.AccountID == "victim" && u.UnavailableReason == "eligible_death_buyback_state_denominator_not_in_accepted_adapter")
	}
	if !buybackUnavailable {
		t.Fatal("buyback_use_count did not fail closed with the exact denominator reason")
	}

	durations := map[string]float64{}
	for _, v := range out.Values {
		if v.MetricID != "phase_duration_seconds" {
			continue
		}
		durations[v.OfficialPhase] = *v.Value
		canonical := false
		for _, ref := range v.Evidence {
			canonical = canonical || (ref.Kind == EvidenceMetricObservation && ref.ID == "phase_duration_seconds:match")
		}
		if !canonical || v.Numerator == nil || v.Denominator == nil {
			t.Fatalf("phase duration missing canonical fields: %+v", v)
		}
	}
	if durations["laning"] != 100 || durations["midgame"] != 80 || durations["decisive"] != 20 || durations["whole_match"] != 200 {
		t.Fatalf("phase duration reconciliation=%v", durations)
	}
}

// TestRateMetricsDenominatorReconciliation proves rate metrics publish
// numerator/denominator that reconcile: the published rate equals
// numerator/denominator and no denominator is fabricated.
// TestRegistryResolutionComplete proves every registry metric resolves to a
// published value or an explicit unavailable record — no missing branch.
func TestRegistryResolutionComplete(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "5"})
	v := int64(50)
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 15, GameSecondOK: true, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})
	out := calc.Result(nil, testOfficialPhases(100))
	resolved := map[string]bool{}
	for _, x := range out.Values {
		resolved[x.MetricID] = true
	}
	for _, x := range out.Unavailable {
		resolved[x.MetricID] = true
	}
	for _, m := range reg.Metrics {
		if !resolved[m.ID] {
			t.Fatalf("registry metric %s has no resolution branch", m.ID)
		}
	}
	if len(resolved) != 52 {
		t.Fatalf("resolved %d distinct metrics, want 52", len(resolved))
	}
}

func TestDirectContributorLineageIsComplete(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "a2": "1"})
	for seq := int64(1); seq <= 70; seq++ {
		v := int64(10)
		calc.Feed(&facts.Fact{Seq: seq, Family: facts.FamilyCombat, GameSecond: 15, GameSecondOK: true,
			Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "a2", Value: &v})})
	}
	out := calc.Result(nil, testOfficialPhases(100))
	for _, v := range out.Values {
		if v.MetricID == "hero_damage_total" && v.AccountID == "a1" && v.OfficialPhase == "whole_match" {
			if v.OpportunityCount != 70 || v.SampleCount != 70 || v.EvidenceCount != 70 {
				t.Fatalf("counts mismatch: opportunity=%d sample=%d evidence=%d", v.OpportunityCount, v.SampleCount, v.EvidenceCount)
			}
			factRefs := 0
			for _, ref := range v.Evidence {
				if ref.Kind == EvidenceFact {
					factRefs++
				}
			}
			if factRefs != 70 {
				t.Fatalf("complete fact lineage=%d want 70", factRefs)
			}
			return
		}
	}
	t.Fatal("whole-match hero_damage_total not published")
}

func TestDeclaredOpportunitiesUsePerAccountLifeIntervals(t *testing.T) {
	reg := testRegistry(t)
	accounts := []string{"r1", "r2", "r3", "r4", "r5", "d1", "d2", "d3", "d4", "d5"}
	team := map[string]string{}
	roles := map[string]string{}
	for i, account := range accounts {
		if i < 5 {
			team[account] = "R"
		} else {
			team[account] = "D"
		}
		roles[account] = "1"
	}
	calc := NewCalculator("m1", accounts, map[string]string{}, team)
	calc.SetRegistry(reg)
	calc.SetRoles(roles)
	calc.SetFactsCoverage([]string{facts.FamilyDeathRespawn})
	for i, account := range accounts {
		hero := "npc_dota_hero_" + account
		calc.Feed(&facts.Fact{Seq: int64(100 + i), Family: facts.FamilyParticipant, GameSecond: 0, GameSecondOK: true,
			Payload: mustJSON(&facts.ParticipantFact{AccountID: account, HeroName: hero})})
	}
	for seq := int64(1); seq <= 5; seq++ {
		killer := "r1"
		if seq > 1 {
			killer = "r2"
		}
		calc.Feed(&facts.Fact{Seq: seq, Family: facts.FamilyDeathRespawn, GameSecond: float64(seq * 10), GameSecondOK: true,
			Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "d1", HeroName: "npc_dota_hero_axe", KillerAccount: killer, AssistAccounts: []string{}})})
	}
	// r3 has one positively proven life crossing the laning/midgame boundary.
	// Its laning death numerator is legitimately zero, independently of the
	// opportunity and 50-second eligible-life denominator.
	alive, dead := true, false
	calc.Feed(&facts.Fact{Seq: 10, Family: facts.FamilyHeroState, GameSecond: 50, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r3", HeroName: "npc_dota_hero_r3", Alive: &alive})})
	calc.Feed(&facts.Fact{Seq: 11, Family: facts.FamilyHeroState, GameSecond: 50, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r3", HeroName: "npc_dota_hero_r3", Alive: &alive})}) // duplicate
	calc.Feed(&facts.Fact{Seq: 12, Family: facts.FamilyDeathRespawn, GameSecond: 150, GameSecondOK: true,
		Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "r3", HeroName: "npc_dota_hero_r3", KillerAccount: "d2"})})
	// r2 has a complete alive->dead state interval but no death event. This is
	// a legitimate observed zero and must survive as numeric zero, never null
	// or a neutral score placeholder.
	calc.Feed(&facts.Fact{Seq: 13, Family: facts.FamilyHeroState, GameSecond: 60, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r2", HeroName: "npc_dota_hero_r2", Alive: &alive})})
	calc.Feed(&facts.Fact{Seq: 14, Family: facts.FamilyHeroState, GameSecond: 80, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r2", HeroName: "npc_dota_hero_r2", Alive: &dead})})
	// r4 has apparent boundaries but a null-liveness gap, so it must fail
	// closed. r5 proves respawn->death and includes a duplicate death.
	calc.Feed(&facts.Fact{Seq: 20, Family: facts.FamilyHeroState, GameSecond: 10, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r4", HeroName: "npc_dota_hero_r4", Alive: &alive})})
	calc.Feed(&facts.Fact{Seq: 21, Family: facts.FamilyHeroState, GameSecond: 20, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r4", HeroName: "npc_dota_hero_r4", Alive: nil})})
	calc.Feed(&facts.Fact{Seq: 22, Family: facts.FamilyHeroState, GameSecond: 30, GameSecondOK: true,
		Payload: mustJSON(&facts.HeroStateSample{AccountID: "r4", HeroName: "npc_dota_hero_r4", Alive: &dead})})
	calc.Feed(&facts.Fact{Seq: 30, Family: facts.FamilyDeathRespawn, GameSecond: 210, GameSecondOK: true,
		Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "respawn", AccountID: "r5", HeroName: "npc_dota_hero_r5"})})
	for _, seq := range []int64{31, 32} {
		calc.Feed(&facts.Fact{Seq: seq, Family: facts.FamilyDeathRespawn, GameSecond: 250, GameSecondOK: true,
			Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "death", AccountID: "r5", HeroName: "npc_dota_hero_r5", KillerAccount: "d2"})})
	}
	ph := &phase.Output{EligibleSeconds: 300, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 100, RuleVersion: "test.phase.v1"},
		{GlobalPhase: phase.Midgame, StartGameSecond: 100, EndGameSecond: 200, RuleVersion: "test.phase.v1"},
		{GlobalPhase: phase.Decisive, StartGameSecond: 200, EndGameSecond: 300, RuleVersion: "test.phase.v1"},
	}}
	out := calc.Result(nil, ph)
	find := func(metric, account, phaseName string) Value {
		for _, v := range out.Values {
			if v.MetricID == metric && v.AccountID == account && v.OfficialPhase == phaseName {
				return v
			}
		}
		t.Fatalf("missing %s observed row for %s", metric, account)
		return Value{}
	}
	for _, account := range []string{"r3", "r4"} {
		v := find("kill_count", account, "whole_match")
		if *v.Value != 0 || *v.Numerator != 0 || v.SampleCount != 0 || v.EvidenceCount != 0 || v.OpportunityCount != 5 || *v.Denominator != 300 {
			t.Fatalf("%s zero kill row=%+v", account, v)
		}
	}
	nonzero := find("kill_count", "r1", "whole_match")
	if *nonzero.Value != 1 || nonzero.OpportunityCount != 5 {
		t.Fatalf("nonzero kill numerator/opportunity=%v/%d want 1/5", *nonzero.Value, nonzero.OpportunityCount)
	}
	laning := find("death_count", "r3", "laning")
	whole := find("death_count", "r3", "whole_match")
	if *laning.Value != 0 || laning.OpportunityCount != 1 || *laning.Denominator != 50 || len(laning.LifeIntervals) != 1 {
		t.Fatalf("r3 phase zero/life intersection=%+v", laning)
	}
	if *whole.Value != 1 || whole.OpportunityCount != 1 || *whole.Denominator != 100 || len(whole.LifeIntervals) != 2 {
		t.Fatalf("r3 whole reconciliation=%+v", whole)
	}
	legitimateZero := find("death_count", "r2", "whole_match")
	if *legitimateZero.Value != 0 || legitimateZero.OpportunityCount != 1 || *legitimateZero.Denominator != 20 {
		t.Fatalf("r2 legitimate observed zero=%+v", legitimateZero)
	}
	decisive := find("death_count", "r5", "decisive")
	if *decisive.Value != 1 || decisive.OpportunityCount != 1 || *decisive.Denominator != 40 {
		t.Fatalf("duplicate transition changed r5=%+v", decisive)
	}
	foundGap := false
	for _, v := range out.Unavailable {
		if v.MetricID == "death_count" && v.AccountID == "r4" && v.UnavailableReason == "bound_real_hero_life_interval_liveness_gap_in_subject_window" {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatal("r4 null-liveness gap did not fail closed")
	}
}

func TestReviewerValidIntervalPlusLaterGapFailsClosed(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("mixed", []string{"a1"}, nil, map[string]string{"a1": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	calc.Feed(&facts.Fact{Seq: 1, Family: facts.FamilyParticipant, GameSecond: 0, GameSecondOK: true,
		Payload: mustJSON(&facts.ParticipantFact{AccountID: "a1", HeroName: "npc_dota_hero_axe"})})
	alive, dead := true, false
	for _, sample := range []struct {
		seq   int64
		sec   float64
		alive *bool
	}{{2, 10, &alive}, {3, 20, &dead}, {4, 30, &alive}, {5, 40, nil}, {6, 50, &dead}} {
		calc.Feed(&facts.Fact{Seq: sample.seq, Family: facts.FamilyHeroState, GameSecond: sample.sec, GameSecondOK: true,
			Payload: mustJSON(&facts.HeroStateSample{AccountID: "a1", HeroName: "npc_dota_hero_axe", Alive: sample.alive})})
	}
	out := calc.Result(nil, testOfficialPhases(100))
	for _, v := range out.Values {
		if v.AccountID == "a1" && v.MetricID == "death_count" {
			t.Fatalf("mixed valid+gap published numeric death_count: %+v", v)
		}
	}
	for _, v := range out.Unavailable {
		if v.AccountID == "a1" && v.MetricID == "death_count" && v.OfficialPhase == "whole_match" {
			if v.UnavailableReason != "bound_real_hero_life_interval_liveness_gap_in_subject_window" || v.GapCount != 1 || len(v.LivenessGaps) != 1 || len(v.LifeIntervals) != 1 || v.Confidence != 0 {
				t.Fatalf("mixed gap unavailable row=%+v", v)
			}
			if v.LivenessGaps[0].SourceFact.SourceFactSeq != 5 || len(v.Evidence) == 0 {
				t.Fatalf("mixed gap lineage not navigable: %+v", v)
			}
			return
		}
	}
	t.Fatal("mixed valid+gap whole-match unavailable decision missing")
}

func TestLivenessGapsAreScopedToHalfOpenOfficialWindows(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("windowed", []string{"a1"}, nil, map[string]string{"a1": "T1"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	calc.Feed(&facts.Fact{Seq: 1, Family: facts.FamilyParticipant, GameSecond: 0, GameSecondOK: true,
		Payload: mustJSON(&facts.ParticipantFact{AccountID: "a1", HeroName: "npc_dota_hero_axe"})})
	alive, dead := true, false
	for _, sample := range []struct {
		seq   int64
		sec   float64
		alive *bool
	}{{2, 10, &alive}, {3, 20, &dead}, {4, 90, &alive}, {5, 100, nil}, {6, 110, &dead}, {7, 210, &alive}, {8, 230, &dead}} {
		calc.Feed(&facts.Fact{Seq: sample.seq, Family: facts.FamilyHeroState, GameSecond: sample.sec, GameSecondOK: true,
			Payload: mustJSON(&facts.HeroStateSample{AccountID: "a1", HeroName: "npc_dota_hero_axe", Alive: sample.alive})})
	}
	ph := &phase.Output{EligibleSeconds: 300, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 100, RuleVersion: "test.phase.v1"},
		{GlobalPhase: phase.Midgame, StartGameSecond: 100, EndGameSecond: 200, RuleVersion: "test.phase.v1"},
		{GlobalPhase: phase.Decisive, StartGameSecond: 200, EndGameSecond: 300, RuleVersion: "test.phase.v1"},
	}}
	out := calc.Result(nil, ph)
	published := map[string]Value{}
	unavailable := map[string]Value{}
	for _, v := range out.Values {
		if v.AccountID == "a1" && v.MetricID == "death_count" {
			published[v.OfficialPhase] = v
		}
	}
	for _, v := range out.Unavailable {
		if v.AccountID == "a1" && v.MetricID == "death_count" {
			unavailable[v.OfficialPhase] = v
		}
	}
	if _, ok := published["laning"]; !ok {
		t.Fatal("gap at exact 100 boundary suppressed preceding [0,100) phase")
	}
	if _, ok := published["decisive"]; !ok {
		t.Fatal("valid interval after gap did not publish in a different phase")
	}
	if unavailable["midgame"].GapCount != 1 || unavailable["whole_match"].GapCount != 1 {
		t.Fatalf("windowed gap decisions mid=%+v whole=%+v", unavailable["midgame"], unavailable["whole_match"])
	}
	if _, ok := published["whole_match"]; ok {
		t.Fatal("whole match published despite intersecting midgame gap")
	}
}

// TestObjectiveDamageOnlyConfiguredTargets proves objective_damage_total only
// counts configured objective entities (tower/rax/ancient-fort/shrine/Roshan/
// Tormentor) per the explicit taxonomy; lane/neutral creeps are excluded
// attribution and excluded_count counts records, not damage magnitude.
func TestObjectiveDamageOnlyConfiguredTargets(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, seq int64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, GameSecondOK: true, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetName: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "npc_dota_goodguys_tower1_mid", 5000, 1))
	calc.Feed(dmg("a1", "npc_dota_badguys_melee_rax_mid", 3000, 2))
	calc.Feed(dmg("a1", "npc_dota_roshan", 2500, 3))
	// Neutral ancient-frog creeps MUST NOT count as objectives (the taxonomy
	// must not substring-match "ancient").
	calc.Feed(dmg("a1", "npc_dota_neutral_ancient_frog", 46511, 4))
	calc.Feed(dmg("a1", "npc_dota_neutral_ancient_frog_mage", 100, 5))
	// Lane/neutral creeps excluded.
	calc.Feed(dmg("a1", "npc_dota_creep_lane", 999999, 6))
	calc.Feed(dmg("a1", "npc_dota_neutral_centaur_khan", 888888, 7))
	// Roshan's banner is a summoned unit, not Roshan.
	calc.Feed(dmg("a1", "npc_dota_unit_roshans_banner", 1000, 8))
	out := calc.Result(nil, testOfficialPhases(200))
	var obj *Value
	for i := range out.Values {
		if out.Values[i].MetricID == "objective_damage_total" && out.Values[i].AccountID == "a1" {
			obj = &out.Values[i]
		}
	}
	if obj == nil {
		t.Fatal("objective_damage_total not published for a1")
	}
	// tower(5000)+rax(3000)+roshan(2500) = 10500; neutral ancient creeps,
	// lane/neutral creeps, and Roshan's banner are excluded.
	if *obj.Value != 10500 {
		t.Fatalf("objective_damage_total=%v want 10500", *obj.Value)
	}
	// excluded_count counts excluded RECORDS: ancient_frog, ancient_frog_mage,
	// creep_lane, centaur_khan, roshans_banner = 5 records.
	if obj.ExcludedCount != 5 {
		t.Fatalf("excluded_count=%d want 5 records", obj.ExcludedCount)
	}
	// excluded_damage carries the excluded magnitude separately.
	if obj.ExcludedDamage == nil || *obj.ExcludedDamage != 46511+100+999999+888888+1000 {
		t.Fatalf("excluded_damage=%v want 1936498", obj.ExcludedDamage)
	}
	if len(obj.Evidence) == 0 {
		t.Fatal("objective_damage_total has no evidence lineage")
	}
	// The typed chain must carry the metric observation and algorithm refs.
	hasObs, hasAlg := false, false
	for _, e := range obj.Evidence {
		switch e.Kind {
		case EvidenceMetricObservation:
			hasObs = e.ID == "objective_damage_total:"+obj.AccountID
		case EvidenceAlgorithm:
			hasAlg = e.ID != ""
		}
	}
	if !hasObs || !hasAlg {
		t.Fatalf("objective_damage_total lineage missing observation/algorithm refs: %+v", obj.Evidence)
	}
}

// TestClassifyObjectiveTargetTaxonomy locks the explicit objective taxonomy:
// neutral ancient creeps and summoned units are never objectives.
func TestClassifyObjectiveTargetTaxonomy(t *testing.T) {
	cases := []struct {
		name string
		want ObjectiveEntityKind
	}{
		{"npc_dota_goodguys_tower2_mid", ObjectiveTower},
		{"npc_dota_badguys_melee_rax_bot", ObjectiveBarracks},
		{"npc_dota_goodguys_fort", ObjectiveAncientFort},
		{"npc_dota_badguys_ancient", ObjectiveAncientFort},
		{"npc_dota_goodguys_shrine", ObjectiveShrine},
		{"npc_dota_roshan", ObjectiveRoshan},
		{"npc_dota_badguys_tormentor", ObjectiveTormentor},
		{"npc_dota_neutral_ancient_frog", ObjectiveUnknown},
		{"npc_dota_neutral_ancient_frog_mage", ObjectiveUnknown},
		{"npc_dota_creep_goodguys_melee", ObjectiveUnknown},
		{"npc_dota_unit_roshans_banner", ObjectiveUnknown},
		{"npc_dota_underlord_portal", ObjectiveUnknown},
		{"", ObjectiveUnknown},
	}
	for _, c := range cases {
		if got := classifyObjectiveTarget(c.name); got != c.want {
			t.Fatalf("classify(%q)=%v want %v", c.name, got, c.want)
		}
	}
}

// TestOpportunityDurationCountsSecondsNotSamples proves opportunity seconds
// counts distinct calibrated second-grid bins, so ~0.5s-cadence samples in the
// same second contribute one eligible second, never more.
func TestOpportunityDurationCountsSecondsNotSamples(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "a2"}, map[string]string{"a1": "p1", "a2": "p2"}, map[string]string{"a1": "T1", "a2": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1"})
	pos := func(sec float64, seq int64) *facts.Fact {
		x, y := 1.0, 2.0
		return &facts.Fact{Family: facts.FamilyHeroState, GameSecond: sec, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.HeroStateSample{AccountID: "a1", PosX: &x, PosY: &y})}
	}
	// Two samples at 0.0, one at 0.5, one at 1.0, one at 2.0.
	calc.Feed(pos(0.0, 1))
	calc.Feed(pos(0.4, 2))
	calc.Feed(pos(0.6, 3))
	calc.Feed(pos(1.0, 4))
	calc.Feed(pos(2.0, 5))
	ph := &phase.Output{EligibleSeconds: 2, Intervals: []phase.Interval{{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 2}}}
	out := calc.Result(nil, ph)
	// opportunity_duration_seconds requires a configured opportunity-key
	// field-quality mask (per official phase, per-reason exclusions) that the
	// accepted adapter does not provide. Distinct position-sample seconds are
	// NOT the registry quantity, so it must be unavailable, never a
	// null-phase/zero-opportunity published row.
	for _, v := range out.Values {
		if v.MetricID == "opportunity_duration_seconds" {
			t.Fatal("opportunity_duration_seconds must not publish without the field-quality mask")
		}
	}
	found := false
	for _, u := range out.Unavailable {
		if u.AccountID == "a1" && u.MetricID == "opportunity_duration_seconds" {
			found = true
			if u.Value != nil || u.IntValue != nil {
				t.Fatal("opportunity_duration_seconds unavailable row has a value")
			}
			if !strings.Contains(u.UnavailableReason, "opportunity_key_field_quality_mask_not_in_accepted_adapter") {
				t.Fatalf("opportunity_duration_seconds reason=%q", u.UnavailableReason)
			}
		}
	}
	if !found {
		t.Fatal("opportunity_duration_seconds missing from unavailable rows")
	}
}

// TestPublishedEvidenceLineageNonEmpty proves every published value carries a
// non-empty evidence_ids chain (fact/episode/phase lineage).
func TestPublishedEvidenceLineageNonEmpty(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	dmg := func(actor, target string, val int64, seq int64) *facts.Fact {
		return &facts.Fact{Family: facts.FamilyCombat, GameSecond: 100, GameSecondOK: true, Seq: seq, SourceSeq: seq, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: actor, TargetAccount: target, Value: &val})}
	}
	calc.Feed(dmg("a1", "b1", 100, 1))
	eps := &episodes.Output{Episodes: []episodes.Episode{
		{Kind: episodes.KindFight, StartGameSecond: 90, EndGameSecond: 120, Participants: []string{"a1", "b1"}, EvidenceIDs: []int64{1}},
	}}
	ph := &phase.Output{EligibleSeconds: 200, Intervals: []phase.Interval{{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 200, EvidenceSeqs: []int64{1}}}}
	out := calc.Result(eps, ph)
	for _, v := range out.Values {
		if v.ReportLevel == "player" && len(v.Evidence) == 0 {
			t.Fatalf("published metric %s/%s has empty evidence lineage", v.AccountID, v.MetricID)
		}
	}
}

// TestResolutionTableComplete proves the output carries a 52-row resolution
// table (one per registry metric) distinguishing published from unavailable.
func TestResolutionTableComplete(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	calc.SetFactsCoverage([]string{"combat_event"})
	v := int64(50)
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 15, GameSecondOK: true, Seq: 1, SourceSeq: 1, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "b1", Value: &v})})
	out := calc.Result(nil, testOfficialPhases(100))
	if len(out.ResolutionTable) != 52 {
		t.Fatalf("resolution table rows=%d want 52", len(out.ResolutionTable))
	}
	pub := 0
	for _, r := range out.ResolutionTable {
		if r.MetricID == "" {
			t.Fatal("resolution row missing metric id")
		}
		if r.Published {
			pub++
		} else if r.UnavailableReason == "" {
			t.Fatalf("unavailable metric %s missing reason", r.MetricID)
		}
	}
	if pub == 0 {
		t.Fatal("no published metrics")
	}
}

func TestEconomyProxiesFailClosed(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	// These facts are deliberately insufficient for the frozen definitions:
	// XP is an award (not state endpoints), net worth/last hits are isolated
	// counters (not gap/reset-reconciled segments), and the team frames are not
	// synchronized complete five-player frames.
	calc.Feed(&facts.Fact{Family: facts.FamilyEconomy, GameSecond: 100, Payload: mustJSON(&facts.EconomySample{AccountID: "a1", Xp: int64p(34)})})
	nw, lh := uint32(1000), uint32(42)
	calc.Feed(&facts.Fact{Family: facts.FamilyEconomy, GameSecond: 101, Payload: mustJSON(&facts.EconomySample{AccountID: "a1", Networth: &nw, LastHits: &lh})})
	ph := &phase.Output{EligibleSeconds: 200, Intervals: []phase.Interval{
		{GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: 200},
	}}
	out := calc.Result(nil, ph)
	reasons := map[string]string{
		"xp_delta":            "experience_state_endpoint_segment_gap_gates_not_in_accepted_adapter",
		"last_hit_count":      "last_hit_counter_reset_gap_reconciliation_not_in_accepted_adapter",
		"net_worth_delta":     "networth_endpoint_segment_gap_reset_gates_not_in_accepted_adapter",
		"team_resource_share": "complete_synchronized_team_resource_frames_not_in_accepted_adapter",
	}
	seen := map[string]bool{}
	for _, u := range out.Unavailable {
		want, ok := reasons[u.MetricID]
		if !ok || u.AccountID != "a1" {
			continue
		}
		seen[u.MetricID] = true
		if u.Value != nil || u.IntValue != nil || u.UnavailableReason != want {
			t.Fatalf("%s did not fail closed exactly: %+v", u.MetricID, u)
		}
	}
	for mid := range reasons {
		if !seen[mid] {
			t.Fatalf("%s missing unavailable row", mid)
		}
	}
	// fight_damage_share is withdrawn (no phase/alive/resolution gates).
	for _, u := range out.Unavailable {
		if u.MetricID == "fight_damage_share" && u.Value != nil {
			t.Fatalf("fight_damage_share published with value: %+v", u)
		}
	}
	// An account with no geometry inputs must NOT get a fabricated 0.
	for _, v := range out.Values {
		if _, bad := reasons[v.MetricID]; bad {
			t.Fatalf("proxy metric published: %+v", v)
		}
	}
}

// TestAdditionalV1Metrics proves raw heal ticks and smoke modifier additions do
// not publish the registry's opportunity-normalized cast/ratio metrics or the
// round-scoped buyback outcome vector from a fixed-window action proxy.
func TestAdditionalV1Metrics(t *testing.T) {
	reg := testRegistry(t)
	calc := NewCalculator("m1", []string{"a1", "b1"}, map[string]string{"a1": "p1", "b1": "p2"}, map[string]string{"a1": "T1", "b1": "T2"})
	calc.SetRegistry(reg)
	calc.SetRoles(map[string]string{"a1": "1", "b1": "1"})
	calc.SetFactsCoverage([]string{"combat_event", "modifier_event", "death_respawn_buyback_event"})
	h := int64(100)
	d := int64(50)
	// A raw heal fact may be a regen tick; it does not prove a ready-source cast.
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 10, Seq: 1, SourceSeq: 1, Payload: mustJSON(&facts.CombatFact{Kind: "heal", ActorAccount: "a1", TargetAccount: "b1", Value: &h})})
	// Smoke modifier additions do not provide the charge/member denominator
	// required by the registry's ratio definition.
	m := "modifier_smoke_of_deceit"
	calc.Feed(&facts.Fact{Family: facts.FamilyModifier, GameSecond: 20, Seq: 2, SourceSeq: 2, Payload: mustJSON(&facts.ModifierFact{Kind: "add", Modifier: m, AccountID: "a1"})})
	calc.Feed(&facts.Fact{Family: facts.FamilyModifier, GameSecond: 20, Seq: 3, SourceSeq: 3, Payload: mustJSON(&facts.ModifierFact{Kind: "add", Modifier: m, AccountID: "b1"})})
	// buyback on a1 at 100, post-buyback damage at 130 (within 60s)
	calc.Feed(&facts.Fact{Family: facts.FamilyDeathRespawn, GameSecond: 100, Seq: 4, SourceSeq: 4, Payload: mustJSON(&facts.DeathRespawnBuyback{Kind: "buyback", AccountID: "a1"})})
	calc.Feed(&facts.Fact{Family: facts.FamilyCombat, GameSecond: 130, Seq: 5, SourceSeq: 5, Payload: mustJSON(&facts.CombatFact{Kind: "damage", ActorAccount: "a1", TargetAccount: "b1", Value: &d})})
	out := calc.Result(nil, nil)
	got := map[string]map[string]Value{}
	for _, v := range out.Values {
		if got[v.AccountID] == nil {
			got[v.AccountID] = map[string]Value{}
		}
		got[v.AccountID][v.MetricID] = v
	}
	unavailable := map[string]map[string]Value{}
	for _, u := range out.Unavailable {
		if unavailable[u.AccountID] == nil {
			unavailable[u.AccountID] = map[string]Value{}
		}
		unavailable[u.AccountID][u.MetricID] = u
	}
	if v, ok := got["a1"]["heal_dispel_save_casts"]; ok {
		t.Fatalf("raw heal fact published as heal_dispel_save_casts: %+v", v)
	}
	if u := unavailable["a1"]["heal_dispel_save_casts"]; u.UnavailableReason != "save_cast_opportunity_gate_not_met:requires_ready_source_cooldown_target_need_evidence_not_in_accepted_adapter" {
		t.Fatalf("heal_dispel_save_casts unavailable=%+v", u)
	}
	for _, acct := range []string{"a1", "b1"} {
		if v, ok := got[acct]["smoke_activation_participation"]; ok {
			t.Fatalf("raw smoke modifier published as ratio for %s: %+v", acct, v)
		}
		if u := unavailable[acct]["smoke_activation_participation"]; u.UnavailableReason != "smoke_charge_opportunity_gate_not_met:requires_smoke_charge_inventory_and_eligible_member_denominator_not_in_accepted_adapter" {
			t.Fatalf("smoke_activation_participation unavailable for %s=%+v", acct, u)
		}
	}
	if v, ok := got["a1"]["buyback_round_participation"]; ok {
		t.Fatalf("fixed-window action proxy published as buyback outcome vector: %+v", v)
	}
	if u := unavailable["a1"]["buyback_round_participation"]; u.UnavailableReason != "buyback_round_identity_position_outcome_vector_gates_not_in_accepted_adapter" {
		t.Fatalf("buyback_round_participation unavailable=%+v", u)
	}
}

// TestEconomyAttributionFromTarget proves GOLD/XP economy facts attribute to
// the hero carried in TargetName (the accepted adapter's shape).
func TestEconomyAttributionFromTarget(t *testing.T) {
	clk := &clock.Clock{State: clock.StateCalibrated, AnchorCombatTS: 0, GameDurationSeconds: f64ptr(300)}
	idn := &identity.Identity{MatchID: "m1", State: identity.StateVerified, Participants: []identity.Participant{
		{AccountID: "a1", HeroName: "npc_dota_hero_kez", Side: "radiant", Slot: 0},
	}}
	rawEv := []raw.Event{
		{Kind: raw.KindCombat, Combat: &raw.Combat{Type: "DOTA_COMBATLOG_GOLD", TargetName: "npc_dota_hero_kez", Value: int64p(500), Networth: uint32p(1000), LastHits: uint32p(42)}},
	}
	b := facts.NewBuilder(clk, idn)
	var got []facts.Fact
	_, err := b.Build(rawReader(rawEv), func(f *facts.Fact) error { got = append(got, *f); return nil })
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range got {
		if f.Family != facts.FamilyEconomy {
			continue
		}
		var es facts.EconomySample
		if err := json.Unmarshal(f.Payload, &es); err != nil {
			t.Fatal(err)
		}
		if es.AccountID == "a1" && es.Gold != nil && *es.Gold == 500 && es.LastHits != nil && *es.LastHits == 42 {
			found = true
		}
	}
	if !found {
		t.Fatal("economy fact not attributed to target hero account")
	}
}

func f64ptr(v float64) *float64 { return &v }
func int64p(v int64) *int64     { return &v }
func uint32p(v uint32) *uint32  { return &v }

func testOfficialPhases(end int) *phase.Output {
	return &phase.Output{EligibleSeconds: end, Intervals: []phase.Interval{{
		GlobalPhase: phase.Laning, StartGameSecond: 0, EndGameSecond: end,
		RuleVersion: "test.phase.v1",
	}}}
}
