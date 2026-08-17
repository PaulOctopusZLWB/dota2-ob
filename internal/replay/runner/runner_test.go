package runner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
)

// syntheticDemo writes a minimal deterministic raw stream (the exact bytes the
// parse stage would produce for a synthetic demo) and returns its content.
// The runner's parse stage is replaced with a stub that writes these bytes so
// tests exercise the full pipeline without committing real replay bytes.
func syntheticRawEvents(matchID string, withAllTen bool, endSecond float64) []byte {
	// Simulate the raw event sequence for a fast-ish match: file info,
	// engine, game-state transitions, hero positions, a tower death, deaths,
	// buybacks, and a parse-done event.
	type fi struct {
		MatchID      uint64 `json:"match_id"`
		GameMode     int32  `json:"game_mode"`
		GameWinner   int32  `json:"game_winner"`
		LeagueID     uint32 `json:"league_id"`
		RadiantTeamID uint32 `json:"radiant_team_id"`
		DireTeamID   uint32 `json:"dire_team_id"`
		EndTime      uint32 `json:"end_time_unix"`
		PlaybackTime float32 `json:"playback_time"`
		Players      []struct {
			HeroName    string `json:"hero_name"`
			PlayerName  string `json:"player_name"`
			IsFakeClient bool  `json:"is_fake_client"`
			SteamID     uint64 `json:"steam_id"`
			GameTeam    int32  `json:"game_team"`
		} `json:"players"`
	}
	f := &fi{MatchID: mustUint(matchID), GameMode: 22, LeagueID: 19719, EndTime: 1786766917, PlaybackTime: 1180}
	f.RadiantTeamID = 9823272
	f.DireTeamID = 5017210
	n := 10
	if !withAllTen {
		n = 9
	}
	heroesR := []string{"npc_dota_hero_kez", "npc_dota_hero_kez", "npc_dota_hero_kez", "npc_dota_hero_kez", "npc_dota_hero_kez"}
	heroesD := []string{"npc_dota_hero_tiny", "npc_dota_hero_tiny", "npc_dota_hero_tiny", "npc_dota_hero_tiny", "npc_dota_hero_tiny"}
	for i := 0; i < n; i++ {
		team := int32(2)
		hero := ""
		if i < 5 && i < len(heroesR) {
			hero = heroesR[i]
		} else if i-5 >= 0 && i-5 < len(heroesD) {
			team = 3
			hero = heroesD[i-5]
		}
		f.Players = append(f.Players, struct {
			HeroName    string `json:"hero_name"`
			PlayerName  string `json:"player_name"`
			IsFakeClient bool  `json:"is_fake_client"`
			SteamID     uint64 `json:"steam_id"`
			GameTeam    int32  `json:"game_team"`
		}{HeroName: hero, PlayerName: fmt.Sprintf("p%d", i), SteamID: 76561197960265728 + uint64(1000+i), GameTeam: team})
	}

	var out []byte
	emit := func(kind string, v interface{}) {
		b, _ := json.Marshal(map[string]interface{}{"kind": kind, "seq": 0, kind: v})
		out = append(out, b...)
		out = append(out, '\n')
	}
	emit("file_info", f)
	emit("engine", map[string]interface{}{"game_dir": "dota", "map_name": "dota"})
	emit("game_state", map[string]interface{}{"combat_ts": 100, "tick": 1000, "state": 4})
	emit("game_state", map[string]interface{}{"combat_ts": 120, "tick": 1200, "state": 5})
	for s := 0; s <= int(endSecond); s += 2 {
		for i := 0; i < n; i++ {
			acct := fmt.Sprintf("%d", 1000+i)
			cls := "CDOTA_Unit_Hero_Kez"
			if i >= 5 {
				cls = "CDOTA_Unit_Hero_Tiny"
			}
			emit("hero_state", map[string]interface{}{
				"tick": 1200 + s*30, "hero_index": i, "class": cls, "player_id": i, "team_num": 2+int32(i/5),
				"pos_x": float64(100+i), "pos_y": float64(200+i), "pos_z": 0.0,
				"health": 1000, "max_health": 1000, "level": 1, "xp": 0, "alive": true,
				"hero_account": acct,
			})
		}
	}
	// A tower death at 300s (triggers midgame).
	emit("combat", map[string]interface{}{"seq": 1, "type": "DOTA_COMBATLOG_TEAM_BUILDING_KILL", "type_id": 6, "ts": 420, "ts_raw": 420, "tick": 4200, "target_name": "badguys_tower1_mid", "target_team": 3, "attacker_team": 2, "value": 1})
	// Deaths at 400s and 402s (fight cluster).
	emit("combat", map[string]interface{}{"seq": 2, "type": "DOTA_COMBATLOG_DEATH", "ts": 520, "target_name": "npc_dota_hero_kez", "is_target_hero": true})
	emit("combat", map[string]interface{}{"seq": 3, "type": "DOTA_COMBATLOG_DEATH", "ts": 522, "target_name": "npc_dota_hero_tiny", "is_target_hero": true})
	emit("combat", map[string]interface{}{"seq": 4, "type": "DOTA_COMBATLOG_BUYBACK", "ts": 524, "attacker_name": "npc_dota_hero_kez", "value": 500})
	// Game over at endSecond+120 (postgame state).
	emit("game_state", map[string]interface{}{"combat_ts": 120 + endSecond + 120, "tick": 1200 + int(endSecond+120)*30, "state": 6})
	emit("parse_done", map[string]interface{}{"last_tick": 1200 + int(endSecond+120)*30, "last_net_tick": 0, "game_build": 6902, "message_counts": map[string]interface{}{}, "combat_total": 4, "bytes_read": 1000, "elapsed_sec": 0, "outcome": "ok"})
	return out
}

// writeSyntheticArchive writes a zstd archive whose decompressed bytes equal
// a synthetic demo (PBDEMS2 magic prefix + raw stream content) so
// archive.Verify passes. The parse stage stub strips the magic and emits the
// raw stream, exercising the full pipeline without committing real bytes.
func writeSyntheticArchive(t *testing.T, matchID string, withAllTen bool, endSecond float64) (*archive.Match, string, string) {
	t.Helper()
	rawEvents := syntheticRawEvents(matchID, withAllTen, endSecond)
	demo := append([]byte{'P', 'B', 'D', 'E', 'M', 'S', '2', 0x00}, rawEvents...)
	compressed := zstd.EncodeTo(nil, demo)
	root := t.TempDir()
	arcPath := filepath.Join(root, matchID+".dem.bz2")
	demPath := filepath.Join(root, "dem", matchID+".dem")
	if err := os.MkdirAll(filepath.Dir(demPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arcPath, compressed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(demPath, demo, 0o644); err != nil {
		t.Fatal(err)
	}
	mt := &archive.Match{
		MatchID:               matchID,
		ArchiveRelativePath:   matchID + ".dem.bz2",
		ArchiveBytes:          int64(len(compressed)),
		DemoRelativePath:      filepath.Join("dem", matchID+".dem"),
		DemoBytes:             int64(len(demo)),
		PublicDurationSeconds: int(endSecond + 120),
		ExpectedTeams: []archive.ExpectedTeam{
			{TeamID: "9823272", TeamName: "Team Yandex", Side: "radiant"},
			{TeamID: "5017210", TeamName: "Team Resilience", Side: "dire"},
		},
	}
	for i := 0; i < 10; i++ {
		hero := 145
		side := "radiant"
		if i >= 5 {
			hero = 19
			side = "dire"
		}
		mt.ExpectedParticipants = append(mt.ExpectedParticipants, archive.ExpectedPlayer{
			AccountID: fmt.Sprintf("%d", 1000+i), Side: side, ExpectedHeroID: hero,
		})
	}
	// Compute real hashes from the written bytes.
	ah, _ := os.ReadFile(arcPath)
	dh, _ := os.ReadFile(demPath)
	mt.ArchiveSHA256 = sha256hex(ah)
	mt.DemoSHA256 = sha256hex(dh)
	return mt, root, demPath
}

func mustUint(s string) uint64 {
	var v uint64
	for _, c := range s {
		v = v*10 + uint64(c-'0')
	}
	return v
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

// mustZstd compresses data with the zstd encoder.
func mustZstd(t *testing.T, data []byte) []byte {
	t.Helper()
	return zstd.EncodeTo(nil, data)
}

// stubParseStage strips the 8-byte PBDEMS2 demo magic and writes the raw
// stream (simulating the parse stage) so tests exercise the full pipeline
// deterministically.
func stubParseStage(demoPath, rawPath, matchID string, progress ProgressFn) (*rawMetaPayload, error) {
	b, err := os.ReadFile(demoPath)
	if err != nil {
		return nil, err
	}
	if len(b) >= 8 {
		b = b[8:]
	}
	if err := os.WriteFile(rawPath, b, 0o644); err != nil {
		return nil, err
	}
	return &rawMetaPayload{Outcome: "ok", Events: 200, CombatTotal: 4, GameBuild: 6902, LastTick: 1200}, nil
}

// fullRoleReg builds a complete role registry for the synthetic fixture:
// accounts 1000-1004 radiant roles 1-5, accounts 1005-1009 dire roles 1-5.
func fullRoleReg() *roles.Registry {
	reg := &roles.Registry{SchemaVersion: "ti2026.roles.v1", TournamentID: "ti2026", Matches: []roles.RoleMatch{{
		MatchID: "1000000001", Teams: []roles.RoleTeam{
			{
				TeamID: "9823272", TeamName: "Team Yandex", Side: "radiant",
				SourceKind: "reliable_public_database", SourceURL: "https://example.com/radiant",
				RetrievedAt: "2026-08-17T00:00:00Z",
			},
			{
				TeamID: "5017210", TeamName: "Team Resilience", Side: "dire",
				SourceKind: "reliable_public_database", SourceURL: "https://example.com/dire",
				RetrievedAt: "2026-08-17T00:00:00Z",
			},
		},
	}}}
	radiantRoles := []string{"1", "2", "3", "4", "5"}
	direRoles := []string{"1", "2", "3", "4", "5"}
	for i, role := range radiantRoles {
		reg.Matches[0].Teams[0].Participants = append(reg.Matches[0].Teams[0].Participants, roles.RoleRecord{
			RoleRecordID: fmt.Sprintf("1000000001:%d", 1000+i), AccountID: fmt.Sprintf("%d", 1000+i),
			PlayerName: fmt.Sprintf("p%d", i), NominalRole: role, RoleConfidence: "high",
		})
	}
	for i, role := range direRoles {
		reg.Matches[0].Teams[1].Participants = append(reg.Matches[0].Teams[1].Participants, roles.RoleRecord{
			RoleRecordID: fmt.Sprintf("1000000001:%d", 1005+i), AccountID: fmt.Sprintf("%d", 1005+i),
			PlayerName: fmt.Sprintf("p%d", 5+i), NominalRole: role, RoleConfidence: "high",
		})
	}
	return reg
}

// testOptions builds the runner options with the full registry.
func testOptions() Options {
	return Options{RoleRegistry: fullRoleReg(), RoleRegistrySHA256: "reg-hash"}
}

// partialRoleReg builds a registry covering only one participant (used to
// prove the role gate quarantines an incomplete registry).
func partialRoleReg() *roles.Registry {
	reg := fullRoleReg()
	reg.Matches[0].Teams[0].Participants = reg.Matches[0].Teams[0].Participants[:1]
	reg.Matches[0].Teams[1].Participants = nil
	return reg
}

func TestRunMatchVerifiedAndRestartDeterministic(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 600)
	rootA := t.TempDir()
	rootB := t.TempDir()
	stA, _ := store.New(rootA)
	stB, _ := store.New(rootB)
	opts := testOptions()
	resA, err := RunMatch(stA, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resA.Status != store.StatusVerified {
		t.Fatalf("status=%s reason=%s", resA.Status, resA.Reason)
	}
	resB, err := RunMatch(stB, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resB.Status != store.StatusVerified {
		t.Fatalf("status=%s reason=%s", resB.Status, resB.Reason)
	}
	if resA.TreeSHA256 != resB.TreeSHA256 {
		t.Fatalf("canonical mismatch runA=%s runB=%s", resA.TreeSHA256, resB.TreeSHA256)
	}
	// Byte-identical artifact sets.
	for _, art := range []string{store.ArtifactRaw, store.ArtifactFacts, store.ArtifactPhases, store.ArtifactEpisodes, store.ArtifactMetrics, store.ArtifactReport, store.ArtifactIdentity, store.ArtifactClock} {
		a, _ := os.ReadFile(stA.ArtifactPath("1000000001", art))
		b, _ := os.ReadFile(stB.ArtifactPath("1000000001", art))
		if string(a) != string(b) {
			t.Fatalf("artifact %s differs between runs", art)
		}
	}
	// Resume: rerun on rootA must skip and preserve the same hash.
	resC, err := RunMatch(stA, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resC.Status != store.StatusVerified || resC.Reason != "resumed_completed_match" {
		t.Fatalf("resume status=%s reason=%s", resC.Status, resC.Reason)
	}
	if resC.TreeSHA256 != resA.TreeSHA256 {
		t.Fatalf("resume changed canonical hash")
	}
}

// TestResumeRebuildsAfterDeletedArtifact: deleting a canonical artifact must
// not resume; the match rebuilds and reaches verified (deterministically the
// rebuilt tree is identical, but the run must not claim "resumed" and the
// deleted artifact must exist again).
func TestResumeRebuildsAfterDeletedArtifact(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	st, _ := store.New(t.TempDir())
	opts := testOptions()
	res, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusVerified {
		t.Fatalf("initial status=%s", res.Status)
	}
	// Delete a canonical artifact, then rerun: must rebuild, not resume.
	if err := os.Remove(st.ArtifactPath("1000000001", store.ArtifactPhases)); err != nil {
		t.Fatal(err)
	}
	res2, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != store.StatusVerified {
		t.Fatalf("status after rebuild=%s reason=%s", res2.Status, res2.Reason)
	}
	if res2.Reason == "resumed_completed_match" {
		t.Fatal("resumed over a deleted artifact")
	}
	// The deleted artifact must exist again.
	if _, err := os.Stat(st.ArtifactPath("1000000001", store.ArtifactPhases)); err != nil {
		t.Fatalf("phases.json not rebuilt: %v", err)
	}
}

// TestResumeRebuildsAfterCorruptedFacts: a corrupted facts.jsonl must not
// resume; the runner rebuilds the match instead of trusting the marker.
func TestResumeRebuildsAfterCorruptedFacts(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	st, _ := store.New(t.TempDir())
	opts := testOptions()
	if _, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {}); err != nil {
		t.Fatal(err)
	}
	factsPath := st.ArtifactPath("1000000001", store.ArtifactFacts)
	if err := os.WriteFile(factsPath, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason == "resumed_completed_match" {
		t.Fatal("resumed over corrupted facts.jsonl")
	}
	if res.Status != store.StatusVerified {
		t.Fatalf("status=%s want verified after rebuild", res.Status)
	}
}

// TestResumeFailsClosedOnMalformedCanonical: a malformed canonical marker must
// not resume; it must be invalidated and the match rebuilt.
func TestResumeFailsClosedOnMalformedCanonical(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	st, _ := store.New(t.TempDir())
	opts := testOptions()
	if _, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// Overwrite canonical.json with garbage.
	canPath := st.ArtifactPath("1000000001", store.ArtifactCanonical)
	if err := os.WriteFile(canPath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason == "resumed_completed_match" {
		t.Fatal("resumed over malformed canonical")
	}
	if res.Status != store.StatusVerified {
		t.Fatalf("status=%s want verified after rebuild", res.Status)
	}
}

// TestRoleGateQuarantinesMissingRegistry: a match whose role registry lacks
// participant records must quarantine with an explicit role reason.
func TestRoleGateQuarantinesMissingRegistry(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	st, _ := store.New(t.TempDir())
	// Registry with only one participant: the gate must fail.
	opts := Options{RoleRegistry: partialRoleReg(), RoleRegistrySHA256: "partial"}
	res, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusQuarantined {
		t.Fatalf("status=%s want quarantined", res.Status)
	}
	if !strings.Contains(res.Reason, "role_provenance_gate") {
		t.Fatalf("reason=%s want role_provenance_gate", res.Reason)
	}
}

// TestRoleGateQuarantinesNilRegistry: a nil registry must quarantine.
func TestRoleGateQuarantinesNilRegistry(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	st, _ := store.New(t.TempDir())
	res, err := RunMatch(st, mt, root, Options{}, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusQuarantined {
		t.Fatalf("status=%s want quarantined", res.Status)
	}
}

// TestFingerprintVersionChangeRebuilds: changing a rule version must change
// the resume fingerprint so stale outputs are never reused.
func TestFingerprintVersionChangeRebuilds(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	st, _ := store.New(t.TempDir())
	opts := testOptions()
	resA, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resA.Status != store.StatusVerified {
		t.Fatalf("initial status=%s", resA.Status)
	}
	// Same options => resume.
	resB, err := RunMatch(st, mt, root, opts, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resB.Reason != "resumed_completed_match" {
		t.Fatalf("expected resume, got %s", resB.Reason)
	}
	// Different override hash => fingerprint changes => rebuild.
	opts2 := Options{RoleRegistry: fullRoleReg(), RoleRegistrySHA256: "reg-hash", RoleOverridesSHA256: "override-changed"}
	resC, err := RunMatch(st, mt, root, opts2, stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resC.Reason == "resumed_completed_match" {
		t.Fatal("reused stale output despite version change")
	}
	if resC.Status != store.StatusVerified {
		t.Fatalf("status=%s want verified after rebuild", resC.Status)
	}
}

func TestRunMatchCorruptIsolated(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000002", true, 300)
	// Corrupt the archive bytes.
	arcPath := filepath.Join(root, "1000000002.dem.bz2")
	f, err := os.OpenFile(arcPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("corrupt")
	f.Close()
	st, _ := store.New(t.TempDir())
	res, err := RunMatch(st, mt, root, testOptions(), stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusCorrupt {
		t.Fatalf("status=%s want corrupt", res.Status)
	}
}

func TestRunMatchMissingParticipantQuarantined(t *testing.T) {
	mt, root, _ := writeSyntheticArchive(t, "1000000003", false, 300)
	st, _ := store.New(t.TempDir())
	res, err := RunMatch(st, mt, root, testOptions(), stubParseStage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusQuarantined {
		t.Fatalf("status=%s want quarantined (9 participants)", res.Status)
	}
	if res.Reason != "identity_participant_binding_mismatch" {
		t.Fatalf("reason=%s", res.Reason)
	}
}

func TestRunBatchOneBadDoesNotAbort(t *testing.T) {
	mtGood, root, _ := writeSyntheticArchive(t, "1000000001", true, 300)
	mtBad, _, _ := writeSyntheticArchive(t, "1000000002", true, 300)
	// Corrupt the bad archive.
	arcPath := filepath.Join(root, "1000000002.dem.bz2")
	f, _ := os.OpenFile(arcPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("corrupt")
	f.Close()
	st, _ := store.New(t.TempDir())
	results := RunBatch(st, []*archive.Match{mtGood, mtBad}, root, testOptions(), stubParseStage, 2, func(string) {})
	if results[0].Status != store.StatusVerified {
		t.Fatalf("good match failed: %s %s", results[0].Status, results[0].Reason)
	}
	if results[1].Status != store.StatusMissing && results[1].Status != store.StatusCorrupt {
		t.Fatalf("bad match status=%s want missing/corrupt", results[1].Status)
	}
}