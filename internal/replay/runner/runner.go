// Package runner orchestrates the per-match replay pipeline: verify, parse,
// identity, clock, facts, episodes, phases, and V1 metrics, persisted to a
// content-addressed store. It is resumable (a completed, unchanged match is
// skipped atomically), deterministic, and never lets one bad match abort the
// rest of a batch.
package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/archive"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/clock"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/episodes"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/facts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/identity"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/metrics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/parser"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/phase"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/report"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/roles"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/store"
)

// ProgressFn is called periodically with a human-readable progress line.
type ProgressFn func(line string)

// ParseStage produces a raw artifact from a decompressed demo. The default
// implementation uses the manta adapter; tests inject a deterministic stub.
type ParseStage func(demoPath, rawPath, matchID string, progress ProgressFn) (*rawMetaPayload, error)

// DefaultParseStage is the manta-backed parse stage.
func DefaultParseStage(demoPath, rawPath, matchID string, progress ProgressFn) (*rawMetaPayload, error) {
	return parseToRaw(demoPath, rawPath, matchID, progress)
}

// MatchResult is the outcome of one match run.
type MatchResult struct {
	MatchID string
	Status  string
	Reason  string
	// Canonical tree hash when the match completed a verified artifact tree.
	TreeSHA256 string
	Elapsed    time.Duration
	PeakHeapMiB uint64
	RawEvents  int64
	FactsEvents int64
	OutputBytes int64
}

// RunMatch executes the full pipeline for one manifest entry. replayRoot is
// the corpus root; st is the store; roleReg supplies nominal-role provenance
// for the report (may be nil). parseStage is the parse adapter (nil uses the
// manta-backed default). If the match already has a canonical artifact
// matching the input fingerprint, it is skipped (resumed) and the result
// records StatusVerified with the existing hash.
func RunMatch(st *store.Store, mt *archive.Match, replayRoot string, roleReg *roles.Registry, parseStage ParseStage, progress ProgressFn) (*MatchResult, error) {
	if parseStage == nil {
		parseStage = DefaultParseStage
	}
	t0 := time.Now()
	res := &MatchResult{MatchID: mt.MatchID}
	if progress == nil {
		progress = func(string) {}
	}

	fp := store.Fingerprint(mt.ArchiveSHA256, mt.DemoSHA256, mt.MatchID)
	if ok, err := st.CanonicalExists(mt.MatchID, fp); err != nil {
		return nil, fmt.Errorf("runner: resume check: %w", err)
	} else if ok {
		var can store.Canonical
		_ = st.ReadJSON(mt.MatchID, store.ArtifactCanonical, &can)
		res.Status = store.StatusVerified
		res.Reason = "resumed_completed_match"
		res.TreeSHA256 = can.TreeSHA256
		res.Elapsed = time.Since(t0)
		progress(fmt.Sprintf("match %s: resumed (canonical %s)", mt.MatchID, can.TreeSHA256))
		return res, nil
	}

	// Persist the frozen input entry so the resume key is auditable.
	if err := st.WriteJSON(mt.MatchID, store.ArtifactInput, mt); err != nil {
		return nil, err
	}

	// Stage 1: verify (streaming hash + magic + size). Never loads corpus
	// into RAM; writes the decompressed demo to a temp file for the parse
	// stage to avoid double decompression.
	progress(fmt.Sprintf("match %s: verifying archive+demo", mt.MatchID))
	demoOut, err := os.CreateTemp("", "replay-*.dem")
	if err != nil {
		return nil, fmt.Errorf("runner: temp demo: %w", err)
	}
	demoPath := demoOut.Name()
	demoOut.Close()
	defer os.Remove(demoPath)

	ver, err := archive.Verify(replayRoot, mt, archive.VerifyOptions{WriteDemo: true, DemoOut: demoPath})
	if err != nil {
		return nil, fmt.Errorf("runner: verify: %w", err)
	}
	if err := st.WriteJSON(mt.MatchID, store.ArtifactVerification, ver); err != nil {
		return nil, err
	}
	if ver.State != archive.StateVerified {
		res.Status = store.StatusCorrupt
		if ver.State == archive.StateMissing {
			res.Status = store.StatusMissing
		}
		res.Reason = ver.Reason
		res.Elapsed = time.Since(t0)
		progress(fmt.Sprintf("match %s: terminal %s (%s)", mt.MatchID, res.Status, ver.Reason))
		return res, nil
	}

	// Stage 2: parse raw observations to raw.jsonl (streaming, deterministic).
	progress(fmt.Sprintf("match %s: parsing raw stream", mt.MatchID))
	rawPath := st.ArtifactPath(mt.MatchID, store.ArtifactRaw)
	rawMeta, err := parseStage(demoPath, rawPath, mt.MatchID, progress)
	if err != nil {
		rawErr := &rawMetaPayload{Outcome: "parse_error", Error: err.Error()}
		_ = st.WriteJSON(mt.MatchID, store.ArtifactRawMeta, rawErr)
		res.Status = store.StatusParseFailed
		res.Reason = err.Error()
		res.Elapsed = time.Since(t0)
		progress(fmt.Sprintf("match %s: parse failed: %v", mt.MatchID, err))
		return res, nil
	}
	if err := st.WriteJSON(mt.MatchID, store.ArtifactRawMeta, rawMeta); err != nil {
		return nil, err
	}
	res.RawEvents = rawMeta.Events
	res.PeakHeapMiB = rawMeta.PeakHeapMiB

	// Stage 3: identity + clock gates from the raw summary.
	progress(fmt.Sprintf("match %s: identity+clock gates", mt.MatchID))
	rf, err := os.Open(rawPath)
	if err != nil {
		return nil, err
	}
	sum, err := raw.ScanSummary(raw.NewReader(rf))
	rf.Close()
	if err != nil {
		return nil, fmt.Errorf("runner: summary: %w", err)
	}

	idn := identity.Build(sum, mt)
	if err := st.WriteJSON(mt.MatchID, store.ArtifactIdentity, idn); err != nil {
		return nil, err
	}
	if idn.State != identity.StateVerified {
		res.Status = store.StatusQuarantined
		res.Reason = "identity_" + idn.Reason
		res.Elapsed = time.Since(t0)
		progress(fmt.Sprintf("match %s: quarantined identity (%s)", mt.MatchID, idn.Reason))
		return res, nil
	}

	var endTime uint32
	if sum.FileInfo != nil {
		endTime = sum.FileInfo.EndTime
	}
	var playback float64
	if sum.FileInfo != nil {
		playback = float64(sum.FileInfo.PlaybackTime)
	}
	clk := clock.Build(sum.Transitions, clock.BuildOptions{
		PublicDurationSeconds: mt.PublicDurationSeconds,
		EndTimeUnix:           endTime,
		PlaybackSeconds:       playback,
	})
	if err := st.WriteJSON(mt.MatchID, store.ArtifactClock, clk); err != nil {
		return nil, err
	}
	if clk.State != clock.StateCalibrated {
		res.Status = store.StatusQuarantined
		res.Reason = "clock_" + clk.Reason
		res.Elapsed = time.Since(t0)
		progress(fmt.Sprintf("match %s: quarantined clock (%s)", mt.MatchID, clk.Reason))
		return res, nil
	}

	// Stage 4: normalized facts.
	progress(fmt.Sprintf("match %s: normalizing facts", mt.MatchID))
	factsPath := st.ArtifactPath(mt.MatchID, store.ArtifactFacts)
	factsSummary, err := buildFacts(rawPath, factsPath, clk, idn)
	if err != nil {
		return nil, fmt.Errorf("runner: facts: %w", err)
	}
	if err := st.WriteJSON(mt.MatchID, store.ArtifactFactsSummary, factsSummary); err != nil {
		return nil, err
	}
	res.FactsEvents = factsSummary.RawEvents

	// Stage 5: episodes + phases + metrics (each reads the facts artifact).
	progress(fmt.Sprintf("match %s: episodes/phases/metrics", mt.MatchID))
	accountName := map[string]string{}
	teamByAcct := map[string]string{}
	accounts := []string{}
	for _, p := range idn.Participants {
		accounts = append(accounts, p.AccountID)
		accountName[p.AccountID] = p.PlayerName
		teamByAcct[p.AccountID] = teamIDFor(mt, p.Side)
	}

	epOut, err := buildEpisodes(factsPath, mt.MatchID, accountName)
	if err != nil {
		return nil, fmt.Errorf("runner: episodes: %w", err)
	}
	if err := st.WriteJSON(mt.MatchID, store.ArtifactEpisodes, epOut); err != nil {
		return nil, err
	}

	phaseOut, err := buildPhases(factsPath, clk)
	if err != nil {
		return nil, fmt.Errorf("runner: phases: %w", err)
	}
	if err := st.WriteJSON(mt.MatchID, store.ArtifactPhases, phaseOut); err != nil {
		return nil, err
	}

	metOut, err := buildMetrics(factsPath, mt.MatchID, accounts, accountName, teamByAcct)
	if err != nil {
		return nil, fmt.Errorf("runner: metrics: %w", err)
	}
	if err := st.WriteJSON(mt.MatchID, store.ArtifactMetrics, metOut); err != nil {
		return nil, err
	}

	// Stage 6: canonical tree over the recomputable source artifacts, then the
	// report (a derived view that reads the canonical from disk).
	progress(fmt.Sprintf("match %s: writing canonical artifact tree", mt.MatchID))
	sourceArtifacts := []string{
		store.ArtifactInput, store.ArtifactVerification, store.ArtifactRaw,
		store.ArtifactRawMeta, store.ArtifactIdentity, store.ArtifactClock,
		store.ArtifactFacts, store.ArtifactFactsSummary, store.ArtifactEpisodes,
		store.ArtifactPhases, store.ArtifactMetrics,
	}
	can, err := st.WriteCanonical(mt.MatchID, fp, sourceArtifacts)
	if err != nil {
		return nil, err
	}
	rep, err := report.Build(st, mt.MatchID, roleReg)
	if err != nil {
		return nil, fmt.Errorf("runner: report: %w", err)
	}
	rep.SortParticipants()
	if err := st.WriteJSON(mt.MatchID, store.ArtifactReport, rep); err != nil {
		return nil, err
	}
	res.Status = store.StatusVerified
	res.Reason = "all_stages_complete"
	res.TreeSHA256 = can.TreeSHA256
	res.Elapsed = time.Since(t0)
	res.OutputBytes = outputBytes(st, mt.MatchID, append(sourceArtifacts, store.ArtifactReport))
	progress(fmt.Sprintf("match %s: verified (canonical %s, %s, %.1f MiB output, %.1fs)",
		mt.MatchID, can.TreeSHA256, metOut.Summary(), float64(res.OutputBytes)/(1<<20), res.Elapsed.Seconds()))
	return res, nil
}

type rawMetaPayload struct {
	Outcome     string `json:"outcome"`
	Error       string `json:"error,omitempty"`
	Events      int64  `json:"events"`
	CombatTotal uint64 `json:"combat_total"`
	GameBuild   uint32 `json:"game_build"`
	LastTick    uint32 `json:"last_tick"`
	PeakHeapMiB uint64 `json:"-"`
}

// parseToRaw streams a demo into the raw artifact. Progress is emitted at
// least once per minute for long parses.
func parseToRaw(demoPath, rawPath, matchID string, progress ProgressFn) (*rawMetaPayload, error) {
	wf, err := os.Create(rawPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(demoPath)
	if err != nil {
		wf.Close()
		return nil, err
	}
	df, err := os.Open(demoPath)
	if err != nil {
		wf.Close()
		return nil, err
	}
	rw := raw.NewWriter(wf)
	start := time.Now()
	lastProgress := start
	// Wrap the reader to report bytes consumed periodically.
	progReader := &progressReader{r: df, total: info.Size(), progress: progress, matchID: matchID, start: start, last: &lastProgress}
	res, perr := parser.ParseStream(progReader, rw, info.Size())
	if ferr := rw.Flush(); ferr != nil {
		df.Close()
		wf.Close()
		return nil, ferr
	}
	df.Close()
	wf.Close()
	if perr != nil {
		meta := &rawMetaPayload{Outcome: "parse_error", Error: perr.Error(), Events: res.Events}
		return meta, perr
	}
	return &rawMetaPayload{
		Outcome:     "ok",
		Events:      res.Events,
		CombatTotal: res.CombatTotal,
		GameBuild:   res.GameBuild,
		LastTick:    res.LastTick,
	}, nil
}

// progressReader reports decompression+parse progress at least once a minute.
type progressReader struct {
	r        io.Reader
	total    int64
	progress ProgressFn
	matchID  string
	start    time.Time
	last     *time.Time
	read     int64
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.read += int64(n)
	if time.Since(*p.last) >= 30*time.Second {
		*p.last = time.Now()
		pct := float64(0)
		if p.total > 0 {
			pct = 100 * float64(p.read) / float64(p.total)
		}
		p.progress(fmt.Sprintf("match %s: parsing %d/%d MiB (%.0f%%) after %.0fs",
			p.matchID, p.read/(1<<20), p.total/(1<<20), pct, time.Since(p.start).Seconds()))
	}
	return n, err
}

func buildFacts(rawPath, factsPath string, clk *clock.Clock, idn *identity.Identity) (*facts.Summary, error) {
	rf, err := os.Open(rawPath)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	wf, err := os.Create(factsPath)
	if err != nil {
		return nil, err
	}
	defer wf.Close()
	enc := json.NewEncoder(wf)
	b := facts.NewBuilder(clk, idn)
	sum, err := b.Build(raw.NewReader(rf), func(f *facts.Fact) error { return enc.Encode(f) })
	if err != nil {
		return nil, err
	}
	return sum, nil
}

func buildEpisodes(factsPath, matchID string, accountName map[string]string) (*episodes.Output, error) {
	rf, err := os.Open(factsPath)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	return episodes.Build(matchID, accountName, facts.NewReader(rf))
}

func buildPhases(factsPath string, clk *clock.Clock) (*phase.Output, error) {
	rf, err := os.Open(factsPath)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	r := facts.NewReader(rf)
	inputs := []phase.Input{}
	for {
		f, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, phaseInputsFromFact(f)...)
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		if inputs[i].GameSecond != inputs[j].GameSecond {
			return inputs[i].GameSecond < inputs[j].GameSecond
		}
		return inputs[i].Seq < inputs[j].Seq
	})
	endSec := 0
	if clk.GameDurationSeconds != nil {
		endSec = int(*clk.GameDurationSeconds)
	}
	eng := phase.NewEngine(phase.Engine{GameEndSecond: endSec})
	for i := range inputs {
		eng.Feed(inputs[i])
	}
	return eng.End(), nil
}

// phaseInputsFromFact converts one fact line into phase-engine inputs.
func phaseInputsFromFact(f *facts.Fact) []phase.Input {
	switch f.Family {
	case facts.FamilyDeathRespawn:
		var drb facts.DeathRespawnBuyback
		if err := json.Unmarshal(f.Payload, &drb); err != nil {
			return nil
		}
		if drb.AccountID == "" {
			return nil
		}
		sec := int(f.GameSecond)
		switch drb.Kind {
		case "death":
			return []phase.Input{{GameSecond: sec, Seq: f.Seq, Kind: phase.InputHeroDeath, Account: drb.AccountID}}
		case "buyback":
			return []phase.Input{{GameSecond: sec, Seq: f.Seq, Kind: phase.InputHeroBuyback, Account: drb.AccountID}}
		}
	case facts.FamilyObjective:
		var of facts.ObjectiveFact
		if err := json.Unmarshal(f.Payload, &of); err != nil {
			return nil
		}
		if !of.IsRealBuilding {
			return nil
		}
		sec := int(f.GameSecond)
		kind := phase.InputTowerDeath
		switch of.BuildingKind {
		case "ancient":
			kind = phase.InputAncientDeath
		case "barracks":
			kind = phase.InputRaxDeath
		}
		side := "radiant"
		if of.Team == "dire" {
			side = "dire"
		}
		return []phase.Input{{GameSecond: sec, Seq: f.Seq, Kind: kind, Side: side, Account: of.BuildingName}}
	case facts.FamilyHeroState:
		var hs facts.HeroStateSample
		if err := json.Unmarshal(f.Payload, &hs); err != nil {
			return nil
		}
		if hs.AccountID == "" || hs.PosX == nil || hs.PosY == nil {
			return nil
		}
		return []phase.Input{{GameSecond: int(f.GameSecond), Seq: f.Seq, Kind: phase.InputHeroPosition, Account: hs.AccountID, X: *hs.PosX, Y: *hs.PosY}}
	case facts.FamilyCombat:
		var cf facts.CombatFact
		if err := json.Unmarshal(f.Payload, &cf); err != nil {
			return nil
		}
		if cf.Kind == "damage" && cf.ActorAccount != "" {
			return []phase.Input{{GameSecond: int(f.GameSecond), Seq: f.Seq, Kind: phase.InputDamage, Account: cf.ActorAccount, Value: floatValue(cf.Value)}}
		}
	}
	return nil
}

func floatValue(p *int64) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}

func buildMetrics(factsPath, matchID string, accounts []string, accountName, teamByAcct map[string]string) (*metrics.Output, error) {
	rf, err := os.Open(factsPath)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	calc := metrics.NewCalculator(matchID, accounts, accountName, teamByAcct)
	r := facts.NewReader(rf)
	for {
		f, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		calc.Feed(f)
	}
	out := calc.Result()
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

func teamIDFor(mt *archive.Match, side string) string {
	for _, t := range mt.ExpectedTeams {
		if t.Side == side {
			return t.TeamID
		}
	}
	return ""
}

func outputBytes(st *store.Store, matchID string, artifacts []string) int64 {
	var total int64
	for _, a := range artifacts {
		if fi, err := os.Stat(st.ArtifactPath(matchID, a)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// RunBatch runs a set of manifest entries with bounded concurrency. One bad
// match is recorded with its terminal status and never aborts the others.
// parseStage is passed through to RunMatch (nil uses the manta default).
func RunBatch(st *store.Store, matches []*archive.Match, replayRoot string, roleReg *roles.Registry, parseStage ParseStage, workers int, progress ProgressFn) []*MatchResult {
	if workers <= 0 {
		workers = 1
	}
	results := make([]*MatchResult, len(matches))
	index := make(chan int, len(matches))
	for i := range matches {
		index <- i
	}
	close(index)
	sem := make(chan struct{}, workers)
	done := make(chan struct{}, len(matches))
	for i := range matches {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			<-index
			sem <- struct{}{}
			defer func() { <-sem }()
			mt := matches[i]
			res, err := RunMatch(st, mt, replayRoot, roleReg, parseStage, progress)
			if err != nil {
				res = &MatchResult{MatchID: mt.MatchID, Status: store.StatusParseFailed, Reason: "runner_error: " + err.Error()}
			}
			results[i] = res
		}(i)
	}
	for i := 0; i < len(matches); i++ {
		<-done
	}
	return results
}

// WriteStatusRecords persists a status record per match (used by CLI for
// explicit terminal reporting).
func WriteStatusRecords(st *store.Store, results []*MatchResult) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range results {
		sr := &store.StatusRecord{
			SchemaVersion: store.StatusSchema,
			MatchID:       r.MatchID,
			Status:        r.Status,
			Reason:        r.Reason,
			UpdatedAt:     now,
		}
		if err := st.WriteJSON(r.MatchID, "status.json", sr); err != nil {
			return err
		}
	}
	return nil
}

// SummarizeResults returns a deterministic multi-line summary of a batch.
func SummarizeResults(results []*MatchResult) string {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%d ", k, counts[k])
	}
	return strings.TrimSpace(b.String())
}

var _ = filepath.Join