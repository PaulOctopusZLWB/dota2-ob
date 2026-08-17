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