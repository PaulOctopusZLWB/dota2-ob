package m4match

import (
	"strings"
	"testing"
)

func TestCoverageIsValueFreeAndCollisionSafe(t *testing.T) {
	frames := [][]byte{
		[]byte(`{"players":{"123":{"account_id":"SECRET-ONE","net_worth":1},"456":{"display_name":"SECRET-TWO","net_worth":2}}}`),
		[]byte(`{"players":{"789":{"account_id":"SECRET-THREE","net_worth":3}}}`),
	}
	profile, err := profileCoverage(frames)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := canonical(profile)
	text := string(payload)
	for _, leaked := range []string{"123", "456", "789", "SECRET", "account_id", "display_name"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("coverage leaked %q: %s", leaked, text)
		}
	}
	foundCollision := false
	for _, path := range profile {
		if path.Path == "/players/{dynamic}" && path.Collisions == 1 {
			foundCollision = true
		}
	}
	if !foundCollision {
		t.Fatalf("dynamic-key collision was not retained value-free: %+v", profile)
	}
}

func TestCoveragePopulationCapClosesBeforeProfiling(t *testing.T) {
	frames := make([][]byte, MaxRehearsalCoverageFrames+1)
	for index := range frames {
		frames[index] = []byte(`{}`)
	}
	if _, err := buildCoverageDelta([][]byte{[]byte(`{}`)}, frames, strings.Repeat("a", 64), strings.Repeat("b", 64)); err == nil {
		t.Fatal("coverage frame cap+1 admitted")
	}
}
