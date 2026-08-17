package web

import (
	"os"
	"strings"
	"testing"
)

// TestReplayCorpusPage verifies the replay corpus page uses only local APIs,
// renders Chinese-first labels, and exposes terminal states.
func TestReplayCorpusPage(t *testing.T) {
	data, err := os.ReadFile("replay/index.html")
	if err != nil {
		t.Fatalf("read replay/index.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		`lang="zh-CN"`,
		"/api/replay/v1/corpus",
		"verified",
		"quarantined",
		"corrupt",
		"parse_failed",
		"missing",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay corpus page missing %q", want)
		}
	}
	// No external CDN resources.
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("replay corpus page references external resource via %q", scheme)
		}
	}
}

// TestReplayMatchPage verifies the match page renders phases, roles, metrics,
// and unavailable reasons from the local API.
func TestReplayMatchPage(t *testing.T) {
	data, err := os.ReadFile("replay/match.html")
	if err != nil {
		t.Fatalf("read replay/match.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		`lang="zh-CN"`,
		`/api/replay/v1/matches/`,
		"对线期",
		"中期",
		"决胜期",
		"选手与名义角色",
		"不可用字段",
		"官方阶段时间线",
		"reset",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay match page missing %q", want)
		}
	}
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("replay match page references external resource via %q", scheme)
		}
	}
}

// TestReplayMatchPageEscapesUntrustedValues verifies the match page renders
// every replay/public-record value through an HTML escaping layer and only
// emits http/https source links. This is the malicious-fixture regression for
// markup/script injection and unsafe URLs.
func TestReplayMatchPageEscapesUntrustedValues(t *testing.T) {
	data, err := os.ReadFile("replay/match.html")
	if err != nil {
		t.Fatalf("read replay/match.html: %v", err)
	}
	html := string(data)

	// The escaping layer must exist and be used for every untrusted value.
	for _, want := range []string{
		"function esc(",
		"function safeUrl(",
		".replace(/&/g, \"&amp;\")",
		"/^https?:\\/\\//i",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay match page missing escaping marker %q", want)
		}
	}
	// Untrusted interpolations must pass through esc()/safeUrl(): no raw
	// player/team/episode/reason interpolation may remain.
	for _, forbidden := range []string{
		`${p.player_name}</td>`,
		`${p.team_name}</td>`,
		`${ep.detail || ""}</td>`,
		`${u}</li>`,
		`<a href="${p.role_source_url}"`,
		`${t.team_name}</td>`,
		`${name.player_name}`,
		`${v.metric_id}</th>`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("replay match page has unescaped interpolation %q", forbidden)
		}
	}
	// The escaped variants must be present instead.
	for _, want := range []string{
		"esc(p.player_name)",
		"esc(p.team_name ||",
		"esc(ep.detail ||",
		"esc(u)",
		"esc(url)",
		"esc(t.team_name)",
		"esc(name ? name.player_name",
		"esc(h)",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay match page missing escaped usage %q", want)
		}
	}
}

// TestReplayMatchPageRendersOverrideProvenance verifies the match page renders
// the effective manual-override provenance (source manual_override, reason,
// timestamp) for an overridden role, escaped and with no raw interpolation.
func TestReplayMatchPageRendersOverrideProvenance(t *testing.T) {
	data, err := os.ReadFile("replay/match.html")
	if err != nil {
		t.Fatalf("read replay/match.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		"手动覆盖",
		"p.override_applied",
		"p.override_reason",
		"p.override_at",
		"esc(p.override_reason",
		"esc(p.override_at",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay match page missing override-provenance marker %q", want)
		}
	}
	// No raw override interpolation without escaping.
	for _, forbidden := range []string{
		`${p.override_reason}`,
		`${p.override_at}`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("replay match page has unescaped override interpolation %q", forbidden)
		}
	}
}

// TestReplayCorpusPageEscapesRows verifies the corpus page does not embed
// untrusted match/category strings into HTML without escaping.
func TestReplayCorpusPageEscapesRows(t *testing.T) {
	data, err := os.ReadFile("replay/index.html")
	if err != nil {
		t.Fatalf("read replay/index.html: %v", err)
	}
	html := string(data)
	for _, forbidden := range []string{
		`${m.match_id}</a>`,
		`${m.category || "-"}`,
		`${m.reason || ""}`,
		`${m.tree_sha256`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("replay corpus page has unescaped interpolation %q", forbidden)
		}
	}
}