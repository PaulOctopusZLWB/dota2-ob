package clock

import (
	"testing"
)

func testTransitions() []Transition {
	return []Transition{
		{State: StatePregame, CombatTS: 100, Tick: 1000},
		{State: StateGameInProgress, CombatTS: 120, Tick: 1200},
		{State: StatePostgame, CombatTS: 3720, Tick: 37200},
	}
}

func TestBuildCalibrated(t *testing.T) {
	tr := testTransitions()
	c := Build(tr, BuildOptions{PublicDurationSeconds: 3600, EndTimeUnix: 1000000, PlaybackSeconds: 200})
	if c.State != StateCalibrated {
		t.Fatalf("state=%s reason=%s", c.State, c.Reason)
	}
	if c.AnchorCombatTS != 120 {
		t.Fatalf("anchor=%v want 120", c.AnchorCombatTS)
	}
	if c.GameDurationSeconds == nil || *c.GameDurationSeconds != 3600 {
		t.Fatalf("duration=%v want 3600", c.GameDurationSeconds)
	}
	if !c.DurationGateOK {
		t.Fatalf("duration gate should pass (delta 0)")
	}
	// game start = end - playback + pregame = 1000000 - 200 + 120 = 999920
	if c.GameStartUnix == nil || *c.GameStartUnix != 999920 {
		t.Fatalf("start=%v want 999920", c.GameStartUnix)
	}
}

func TestBuildMissingAnchor(t *testing.T) {
	tr := []Transition{{State: StatePregame, CombatTS: 100, Tick: 1000}}
	c := Build(tr, BuildOptions{PublicDurationSeconds: 3600})
	if c.State != StateFailClosed {
		t.Fatalf("state=%s want fail_closed", c.State)
	}
}

func TestBuildRightCensoredNoPostgame(t *testing.T) {
	tr := testTransitions()[:2]
	c := Build(tr, BuildOptions{PublicDurationSeconds: 3600})
	if c.State != StateUnavailable {
		t.Fatalf("state=%s want unavailable", c.State)
	}
	if c.Reason != "game_end_missing_right_censored" {
		t.Fatalf("reason=%s", c.Reason)
	}
}

func TestBuildDurationMismatchFailsClosed(t *testing.T) {
	tr := testTransitions()
	c := Build(tr, BuildOptions{PublicDurationSeconds: 3600 + 1000}) // delta 1000 > 0.1% tolerance
	if c.State != StateFailClosed {
		t.Fatalf("state=%s want fail_closed", c.State)
	}
	if c.Reason != "duration_mismatch_exceeds_tolerance" {
		t.Fatalf("reason=%s", c.Reason)
	}
}

func TestBuildZeroPublicDuration(t *testing.T) {
	c := Build(testTransitions(), BuildOptions{PublicDurationSeconds: 0})
	if c.State != StateFailClosed || c.Reason != "public_duration_missing" {
		t.Fatalf("state=%s reason=%s", c.State, c.Reason)
	}
}

func TestGameSecondOf(t *testing.T) {
	c := Build(testTransitions(), BuildOptions{PublicDurationSeconds: 3600})
	if c.State != StateCalibrated {
		t.Fatal(c.State)
	}
	if got := c.GameSecondOfCombatTS(120); got != 0 {
		t.Fatalf("combat second=%v want 0", got)
	}
	if got := c.GameSecondOfTick(1200); got != 0 {
		t.Fatalf("tick second=%v want 0", got)
	}
	if got := c.GameSecondOfTick(1230); got != 1.0 {
		t.Fatalf("tick second=%v want 1.0", got)
	}
}

func TestStateName(t *testing.T) {
	if StateName(StateGameInProgress) != "game_in_progress" {
		t.Fatal("state name")
	}
}
