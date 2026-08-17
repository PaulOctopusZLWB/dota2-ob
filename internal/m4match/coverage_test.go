package m4match

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestFieldCoverageIsBaselineComparedValueFreeAndCollisionSafe(t *testing.T) {
	repo, _ := os.Getwd()
	baseline := filepath.Join(filepath.Clean(filepath.Join(repo, "../..")), "internal", "integration", "m4", "testdata", "captured_gsi_schedule.json")
	records := []*session.Record{
		{Sequence: 1, Raw: []byte(`{"map":{"matchid":"secret-match","clock_time":0},"player":{"private-account":{"net_worth":1},"other-account":{"net_worth":2}}}`)},
		{Sequence: 2, Raw: []byte(`{"map":{"clock_time":1},"player":{"private-account":{"net_worth":3}}}`)},
	}
	manifest := []session.RawRecordAttestationV1{{Sequence: 1, RawRecordSHA256: stringsOf('a')}, {Sequence: 2, RawRecordSHA256: stringsOf('b')}}
	delta, err := buildFieldCoverage(records, manifest, baseline)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(delta)
	for _, leaked := range []string{"secret-match", "private-account", "other-account"} {
		if strings.Contains(string(payload), leaked) {
			t.Fatalf("coverage leaked %q", leaked)
		}
	}
	if delta.BaselineFrames != 4 || delta.FrameCount != 2 || delta.BaselineSHA256 != CapturedScheduleSHA256 || delta.ContentSHA256 == "" {
		t.Fatalf("delta identity=%#v", delta)
	}
	foundCollision := false
	for _, entry := range delta.Entries {
		if entry.Path == "$.player.{dynamic}.net_worth" && entry.Classification == "dynamic_collision" {
			foundCollision = true
		}
	}
	if !foundCollision {
		t.Fatal("collision-safe wildcard profile absent")
	}
}

func TestFieldCoverageRejectsBaselineSubstitutionAndDepthOverflow(t *testing.T) {
	badBaseline := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(badBaseline, []byte(`{"updates":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	record := &session.Record{Sequence: 1, Raw: []byte(`{}`)}
	manifest := []session.RawRecordAttestationV1{{Sequence: 1, RawRecordSHA256: stringsOf('a')}}
	if _, err := buildFieldCoverage([]*session.Record{record}, manifest, badBaseline); err == nil {
		t.Fatal("substituted baseline accepted")
	}
	repo, _ := os.Getwd()
	baseline := filepath.Join(filepath.Clean(filepath.Join(repo, "../..")), "internal", "integration", "m4", "testdata", "captured_gsi_schedule.json")
	nested := strings.Repeat(`{"name":`, maxCoverageDepth+2) + `null` + strings.Repeat(`}`, maxCoverageDepth+2)
	record.Raw = []byte(nested)
	if _, err := buildFieldCoverage([]*session.Record{record}, manifest, baseline); err == nil {
		t.Fatal("coverage depth overflow accepted")
	}
}
