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
		// Canonical lineage links on the match page.
		"lineageCell",
		"lineageLink",
		"evidence=fact:",
		"evidence=metric_observation:",
		"aggregation.html?id=",
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

// TestReplayPlayerPage verifies the player profile page renders the official
// solid radar and the V3 dashed experimental layer separately, plus the score
// decomposition and suppression states.
func TestReplayPlayerPage(t *testing.T) {
	data, err := os.ReadFile("replay/player.html")
	if err != nil {
		t.Fatalf("read replay/player.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		`lang="zh-CN"`,
		"/api/replay/v1/players/",
		"官方实线（V1/V2）",
		"实验虚线（V3）",
		"官方总分",
		"实验总分",
		"指标分解（原始值",
		"function esc(",
		"function safeUrl(",
		// Canonical lineage links + aggregation drilldown.
		"lineageLink",
		"聚合实体（选手锦标赛聚合",
		"aggregation.html?id=",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay player page missing %q", want)
		}
	}
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("replay player page references external resource via %q", scheme)
		}
	}
}

// TestReplayAggregationPage verifies the aggregation entity view renders the
// canonical aggregation route and its retained child lineage.
func TestReplayAggregationPage(t *testing.T) {
	data, err := os.ReadFile("replay/aggregation.html")
	if err != nil {
		t.Fatalf("read replay/aggregation.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		`lang="zh-CN"`,
		"/api/replay/v1/aggregations/",
		"聚合实体",
		"child_refs",
		"rule_version",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay aggregation page missing %q", want)
		}
	}
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("replay aggregation page references external resource via %q", scheme)
		}
	}
}

// TestReplayTeamPage verifies the team profile page renders the independent
// team scoring product: official (solid) and experimental (dashed) layers,
// separately named totals, decomposition, and per-match player rows.
func TestReplayTeamPage(t *testing.T) {
	data, err := os.ReadFile("replay/team.html")
	if err != nil {
		t.Fatalf("read replay/team.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		`lang="zh-CN"`,
		"/api/replay/v1/teams/",
		"队伍官方总分",
		"队伍实验总分（V3 虚线层）",
		"官方轴分解",
		"实验轴分解（虚线层）",
		"指标分解（可复现显示值）",
		"player.html?id=",
		// Suppressed layer stays visible as suppressed, never a zero polygon.
		"已抑制",
		"总分已抑制",
		"renderTeamRadar",
		"expVals",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay team page missing %q", want)
		}
	}
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("replay team page references external resource via %q", scheme)
		}
	}
}

// TestReplayReviewPage verifies the gold-review UI renders the correction
// workflow: reason required, machine value preserved, session token, and
// Chinese-first labels.
func TestReplayReviewPage(t *testing.T) {
	data, err := os.ReadFile("replay/review.html")
	if err != nil {
		t.Fatalf("read replay/review.html: %v", err)
	}
	html := string(data)
	for _, want := range []string{
		`lang="zh-CN"`,
		"/api/replay/v1/reviews/queue",
		"X-Dota2-OB-Token",
		"机器值",
		"有效值",
		"校正原因（必填）",
		"阶段边界校正",
		"争议事件校正",
		"机器输出永不被修改",
		// Effective-stream selector operates on the persisted overlay.
		"当前有效阶段流（操作目标）",
		"机器阶段流（不可变，仅展示）",
		"phase-ref-",
		"effective_phase_intervals",
		"currentEffectiveStream",
		// Complete typed operation shapes.
		"absorb_into",
		"merge_right",
		"split_second",
		"shape_version",
		"interval@",
		// Operation-specific payload construction for all seven ops.
		`case "accept"`,
		`case "relabel"`,
		`case "split"`,
		`case "move"`,
		`case "add"`,
		`case "delete"`,
		`case "merge"`,
		// Only official phases are legal.
		`VALID_PHASES = ["laning", "midgame", "decisive"]`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("replay review page missing %q", want)
		}
	}
	for _, scheme := range []string{"https://", "http://", "\"//", "'//"} {
		if strings.Contains(html, scheme) {
			t.Fatalf("replay review page references external resource via %q", scheme)
		}
	}
}
