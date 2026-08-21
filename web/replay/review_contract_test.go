package replayweb

import (
	"os"
	"strings"
	"testing"
)

func TestReviewPageHasFailClosedHumanGateControls(t *testing.T) {
	b, err := os.ReadFile("review.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"语料审核进度", "机器分类", "replay_sha256", "请选择，不自动接受", "role_provenance_evidence", "official_experimental_acknowledged", "/reviews/category-decisions", "/reviews/finalize", "data-machine-category"} {
		if !strings.Contains(s, want) {
			t.Fatalf("review.html missing %q", want)
		}
	}
	if strings.Contains(s, `type="checkbox" checked`) {
		t.Fatal("review checklist must not be pre-accepted")
	}
}
