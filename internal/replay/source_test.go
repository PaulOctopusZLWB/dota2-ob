package replay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectSource2DemoFileRejectsJSONMasqueradingAsDemo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.dem")
	if err := os.WriteFile(path, []byte(`{"facts":"not a replay"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSource2DemoFile(path); err == nil {
		t.Fatal("JSON payload under .dem suffix was accepted")
	}
}
