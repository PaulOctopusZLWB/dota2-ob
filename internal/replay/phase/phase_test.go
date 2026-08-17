package phase

import (
	"testing"
)

// feedSequence feeds a deterministic event stream to a new engine and returns
// its output.
func feedSequence(t *testing.T, endSecond int, in []Input) *Output {
	t.Helper()
	e := NewEngine(Engine{GameEndSecond: endSecond})
	for i := range in {
		e.Feed(in[i])
	}
	return e.End()
}

func TestFastEndingStaysLaning(t *testing.T) {
	// A short match that ends before any tower death or lane-break rule fires
	// must stay laning the whole way; the engine does not fabricate midgame.
	end := 600
	inputs := []Input{}
	for s := 0; s <= end; s++ {
		inputs = append(inputs, Input{GameSecond: s, Seq: int64(s), Kind: InputHeroPosition, Account: "a1", X: 100, Y: 100})
		inputs = append(inputs, Input{GameSecond: s, Seq: int64(s + 100000), Kind: InputHeroPosition, Account: "a2", X: 200, Y: 200})
	}
	// A couple of early deaths but no tower.
	inputs = append(inputs, Input{GameSecond: 120, Seq: 99991, Kind: InputHeroDeath, Account: "a1"})
	inputs = append(inputs, Input{GameSecond: 121, Seq: 99992, Kind: InputHeroDeath, Account: "a2"})
	// Reorder not required; feeds are expected non-decreasing but we sort in
	// the runner; here they are already non-decreasing except death seq tail.
	// Sort to mirror the runner contract.
	sortInputs(inputs)
	out := feedSequence(t, end, inputs)
	if len(out.Intervals) == 0 {
		t.Fatal("no intervals")
	}
	for _, iv := range out.Intervals {
		if iv.GlobalPhase != Laning && iv.GlobalPhase != Ended {
			t.Fatalf("phase=%s at %d, want laning only", iv.GlobalPhase, iv.StartGameSecond)
		}
	}
	if out.CoveredSeconds != out.EligibleSeconds {
		t.Fatalf("coverage %d/%d", out.CoveredSeconds, out.EligibleSeconds)
	}
	if out.State != "complete" {
		t.Fatalf("state=%s", out.State)
	}
}

func TestTowerDeathEntersMidgame(t *testing.T) {
	end := 900
	inputs := []Input{
		{GameSecond: 300, Seq: 1, Kind: InputTowerDeath, Account: "rad_t1", Side: "radiant"},
	}
	sortInputs(inputs)
	out := feedSequence(t, end, inputs)
	var sawLaning, sawMidgame bool
	for _, iv := range out.Intervals {
		switch iv.GlobalPhase {
		case Laning:
			sawLaning = true
		case Midgame:
			sawMidgame = true
		}
	}
	if !sawLaning || !sawMidgame {
		t.Fatalf("intervals=%+v want laning then midgame", out.Intervals)
	}
	// laning must precede midgame and never re-enter.
	laningSeen := false
	for _, iv := range out.Intervals {
		if iv.GlobalPhase == Laning {
			if laningSeen {
				t.Fatal("laning re-entered")
			}
			laningSeen = true
		}
		if iv.GlobalPhase == Midgame && laningSeen == false {
			t.Fatal("midgame before laning")
		}
	}
}

func TestDecisiveResetReentry(t *testing.T) {
	// Long game: decisive entered via barracks, reset via 30s quiet, re-entered
	// via a second barracks death. Round indices must increase monotonically.
	end := 1200
	inputs := []Input{
		{GameSecond: 500, Seq: 1, Kind: InputTowerDeath, Account: "t1", Side: "radiant"},
		{GameSecond: 700, Seq: 2, Kind: InputRaxDeath, Account: "rax", Side: "dire"},
		// A death and buyback keep decisive-relevant evidence alive.
		{GameSecond: 705, Seq: 3, Kind: InputHeroDeath, Account: "p1"},
		{GameSecond: 706, Seq: 4, Kind: InputHeroBuyback, Account: "p1"},
		// 30+ seconds of quiet (no deaths/buybacks/towers/damage) => reset.
		{GameSecond: 760, Seq: 5, Kind: InputDamage, Account: "p2"},
		// Re-enter decisive with a second barracks death.
		{GameSecond: 900, Seq: 6, Kind: InputRaxDeath, Account: "rax2", Side: "dire"},
		{GameSecond: 1000, Seq: 7, Kind: InputAncientDeath, Account: "ancient", Side: "radiant"},
	}
	sortInputs(inputs)
	out := feedSequence(t, end, inputs)
	if len(out.Resets) == 0 {
		t.Fatal("expected at least one reset episode")
	}
	// All intervals must be official or terminal.
	for _, iv := range out.Intervals {
		if iv.GlobalPhase != Laning && iv.GlobalPhase != Midgame && iv.GlobalPhase != Decisive && iv.GlobalPhase != Ended {
			t.Fatalf("illegal phase %s", iv.GlobalPhase)
		}
	}
	// Decisive rounds must be monotonic.
	lastRound := -1
	decisiveCount := 0
	for _, iv := range out.Intervals {
		if iv.GlobalPhase == Decisive {
			decisiveCount++
			if iv.RoundIndex <= lastRound {
				t.Fatalf("round index not monotonic: %d after %d", iv.RoundIndex, lastRound)
			}
			lastRound = iv.RoundIndex
		}
	}
	if decisiveCount < 2 {
		t.Fatalf("expected multiple decisive intervals, got %d", decisiveCount)
	}
	// Full coverage.
	if out.CoveredSeconds != out.EligibleSeconds {
		t.Fatalf("coverage %d/%d", out.CoveredSeconds, out.EligibleSeconds)
	}
	// Reset must never appear as a global_phase.
	for _, iv := range out.Intervals {
		if iv.GlobalPhase == "reset" {
			t.Fatal("reset emitted as phase")
		}
	}
}

func TestPerSecondExactlyOnePhase(t *testing.T) {
	end := 300
	inputs := []Input{
		{GameSecond: 100, Seq: 1, Kind: InputTowerDeath, Account: "t", Side: "radiant"},
	}
	sortInputs(inputs)
	out := feedSequence(t, end, inputs)
	perSec := out.PerSecond()
	if len(perSec) != out.EligibleSeconds {
		t.Fatalf("per-second length %d != eligible %d", len(perSec), out.EligibleSeconds)
	}
	// Every eligible second is exactly one of the three official phases.
	for s, ph := range perSec {
		if ph != Laning && ph != Midgame && ph != Decisive {
			t.Fatalf("second %d has phase %q", s, ph)
		}
	}
	// No 'unavailable' left after a complete run.
	for s, ph := range perSec {
		if ph == "unavailable" {
			t.Fatalf("second %d uncovered", s)
		}
	}
}

func TestMissingGameEndCensored(t *testing.T) {
	// No post-game/ancient event: the output must carry a right-censor reason
	// but still cover all eligible seconds with the final phase.
	end := 400
	inputs := []Input{{GameSecond: 50, Seq: 1, Kind: InputTowerDeath, Account: "t", Side: "radiant"}}
	sortInputs(inputs)
	out := feedSequence(t, end, inputs)
	found := false
	for _, m := range out.MissingInputs {
		if m == "game_end_missing_right_censored" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing right-censor reason: %v", out.MissingInputs)
	}
}

func sortInputs(in []Input) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && (in[j-1].GameSecond > in[j].GameSecond ||
			(in[j-1].GameSecond == in[j].GameSecond && in[j-1].Seq > in[j].Seq)); j-- {
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
}
