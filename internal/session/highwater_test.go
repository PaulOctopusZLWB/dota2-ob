package session_test

import (
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestHighWaterCoalescesToNewestCommittedSequence(t *testing.T) {
	wake := session.NewHighWater("session-a", 0)
	wake.Publish(1)
	wake.Publish(2)
	wake.Publish(3)

	if got := len(wake.C()); got != 1 {
		t.Fatalf("notification occupancy = %d, want 1", got)
	}
	mark := <-wake.C()
	if mark.SessionID != "session-a" || mark.Sequence != 3 {
		t.Fatalf("notification = %#v, want session-a/3", mark)
	}
}

func TestStorePublishesOnlySuccessfulCompleteAppends(t *testing.T) {
	wake := session.NewHighWater("committed", 0)
	f := &fakeRawFile{writeLimit: 4}
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("committed"), session.WithHighWater(wake), session.WithRawFile(func(string) (session.RawFile, error) { return f, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("short append succeeded")
	}
	if got := len(wake.C()); got != 0 {
		t.Fatalf("failed append published %d notification(s)", got)
	}
	f.writeLimit = 0
	record, err := store.Append([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if mark := <-wake.C(); mark.Sequence != record.Sequence {
		t.Fatalf("mark sequence = %d, want %d", mark.Sequence, record.Sequence)
	}
}
