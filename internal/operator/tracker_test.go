package operator_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
)

func TestTrackerWaitingReceivingStaleWithInjectedClock(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tracker := operator.NewTracker("session-1", now, 10*time.Second, func() time.Time { return now })
	if got := tracker.Snapshot().State; got != operator.StateWaiting {
		t.Fatalf("state = %s", got)
	}
	tracker.Request()
	tracker.Accepted(now)
	if got := tracker.Snapshot().State; got != operator.StateReceiving {
		t.Fatalf("state = %s", got)
	}
	now = now.Add(10 * time.Second)
	if got := tracker.Snapshot().State; got != operator.StateStale {
		t.Fatalf("state = %s", got)
	}
	snap := tracker.Snapshot()
	if snap.RequestCount != 1 || snap.AcceptedCount != 1 || snap.FirstAcceptedTime == nil || !snap.FirstAcceptedTime.Equal(time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
}

func TestTrackerSubsystemFailuresRecoverIndependentlyAndRemainBounded(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tracker := operator.NewTracker("session-1", now, time.Minute, func() time.Time { return now })
	tracker.Failure(operator.SubsystemLatest, "latest_failed", "latest projection failed")
	tracker.Failure(operator.SubsystemAnalytics, "analytics_failed", "analytics projection failed")
	tracker.Success(operator.SubsystemLatest)
	if snap := tracker.Snapshot(); snap.State != operator.StateDegraded || snap.ActiveFailures[operator.SubsystemAnalytics].Code != "analytics_failed" {
		t.Fatalf("latest success cleared unrelated failure: %#v", snap)
	}
	tracker.Success(operator.SubsystemAnalytics)
	if got := tracker.Snapshot().State; got != operator.StateWaiting {
		t.Fatalf("state = %s", got)
	}
	for i := 0; i < 50; i++ {
		tracker.Failure(operator.SubsystemProfile, "profile_failed", fmt.Sprintf("safe failure %d", i))
	}
	snap := tracker.Snapshot()
	if len(snap.Errors) > operator.MaxErrors {
		t.Fatalf("errors = %d, max %d", len(snap.Errors), operator.MaxErrors)
	}
	if snap.PostProcessingFailureCount != 52 {
		t.Fatalf("failure count = %d, want 52", snap.PostProcessingFailureCount)
	}
}

func TestTrackerRawCountersAndFirstAcceptedAreIndependent(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tracker := operator.NewTracker("session", now, time.Minute, func() time.Time { return now })
	tracker.Request()
	tracker.Rejected()
	tracker.Request()
	tracker.RawFailure("raw_append_failed", "raw append failed")
	if snap := tracker.Snapshot(); snap.State != operator.StateDegraded || snap.FirstAcceptedTime != nil || snap.RejectedCount != 1 || snap.RawWriteFailureCount != 1 {
		t.Fatalf("pre-accept snapshot: %#v", snap)
	}
	now = now.Add(time.Second)
	tracker.Accepted(now)
	tracker.Success(operator.SubsystemRaw)
	first := tracker.Snapshot().FirstAcceptedTime
	now = now.Add(time.Second)
	tracker.Accepted(now)
	if first == nil || !tracker.Snapshot().FirstAcceptedTime.Equal(*first) {
		t.Fatal("first accepted time changed")
	}
}
