package web

import (
	"os"
	"strings"
	"testing"
)

func TestDashboardUsesDotaSpectatorSlotNumbering(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}

	html := string(data)
	for _, want := range []string{
		`{ key: "team2", name: "Radiant", start: 0 }`,
		`{ key: "team3", name: "Dire", start: 5 }`,
		"team.start + index",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard missing spectator slot marker %q", want)
		}
	}
}
