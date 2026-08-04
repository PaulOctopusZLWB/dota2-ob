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

// TestDashboardExposesAnalyticsPanels verifies the analytics-baseline panels
// required by the MVP2-2 dashboard extension are present in index.html.
func TestDashboardExposesAnalyticsPanels(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	html := string(data)

	// Ten-slot display must remain.
	for _, want := range []string{
		`<section class="lanes" id="lanes">`,
		`renderSlot`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard missing latest-state element %q", want)
		}
	}

	// Freshness / counts row.
	for _, want := range []string{
		`id="snapshots"`,
		`id="ticks"`,
		`id="frames"`,
		`id="event-total"`,
		`id="freshness"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard missing freshness/count element %q", want)
		}
	}

	// Required analytics panels.
	for _, want := range []string{
		`id="events-feed"`,
		`id="economy"`,
		`id="objectives"`,
		`id="wards"`,
		`id="ward-conclusion"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard missing analytics panel element %q", want)
		}
	}

	// Panel handlers must reference the derived event categories.
	for _, want := range []string{
		`gold_changed`,
		`net_worth_changed`,
		`building_destroyed`,
		`roshan_state_changed`,
		`ward_counter_changed`,
		`ward_coordinate_conclusion`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard missing analytics category reference %q", want)
		}
	}
}

// TestDashboardUsesLocalAPIsOnly asserts the dashboard only fetches local API
// endpoints and references no external CDN or third-party resource.
func TestDashboardUsesLocalAPIsOnly(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	html := string(data)

	for _, endpoint := range []string{
		"/api/latest",
		"/api/analytics",
		"/api/events",
		"/api/profile",
	} {
		if !strings.Contains(html, endpoint) {
			t.Fatalf("dashboard does not reference %s", endpoint)
		}
	}

	// No absolute external URLs (http://, https://, //) inside the page.
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("dashboard references external resource via %q", scheme)
		}
	}
}
