package history

import (
	"errors"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

// stageCounts is a tiny adapter that records how many times each stage ran
// and succeeds/fails per a programmable policy. It proves resume skips
// succeeded work, terminal entries are skipped, and retries converge.
type stageCounts struct {
	acquireFail map[string]bool
	parseFail   map[string]bool
	calls       map[string]int
}

func newStageCounts() *stageCounts {
	return &stageCounts{acquireFail: map[string]bool{}, parseFail: map[string]bool{}, calls: map[string]int{}}
}

func (sc *stageCounts) buildPipeline(maxRetries int, save func(StageBatch) error) *StagePipeline {
	acquire := func(e StageEntry, ctx MatchContext) (StageEntry, *StageFailure) {
		sc.calls["acquire:"+e.MatchID]++
		if sc.acquireFail[e.MatchID] {
			return e, &StageFailure{Stage: StageAcquisition, Reason: "checksum_mismatch", Terminal: true}
		}
		e.ReplaySHA256 = testSHA(e.MatchID)
		return e, nil
	}
	verify := func(e StageEntry, ctx MatchContext) (StageEntry, *StageFailure) {
		sc.calls["verify:"+e.MatchID]++
		return e, nil
	}
	parse := func(e StageEntry, ctx MatchContext) (StageEntry, *StageFailure) {
		sc.calls["parse:"+e.MatchID]++
		if sc.parseFail[e.MatchID] {
			return e, &StageFailure{Stage: StageParse, Reason: "parse_error", Terminal: false}
		}
		return e, nil
	}
	normalize := func(e StageEntry, ctx MatchContext) (StageEntry, *StageFailure) {
		sc.calls["normalize:"+e.MatchID]++
		e.FactsSHA256 = testSHA(e.MatchID + "_facts")
		return e, nil
	}
	aggregate := func(e StageEntry, ctx MatchContext) (StageEntry, *StageFailure) {
		sc.calls["aggregate:"+e.MatchID]++
		return e, nil
	}
	return &StagePipeline{
		Stages: map[string]StageFunc{
			StageAcquisition: acquire, StageVerification: verify,
			StageParse: parse, StageNormalize: normalize, StageAggregate: aggregate,
		},
		Reconcile: map[string]StageReconcileFunc{
			StageAcquisition:  func(e StageEntry, _ MatchContext) (StageEntry, bool, *StageFailure) { return e, false, nil },
			StageVerification: func(e StageEntry, _ MatchContext) (StageEntry, bool, *StageFailure) { return e, false, nil },
			StageParse:        func(e StageEntry, _ MatchContext) (StageEntry, bool, *StageFailure) { return e, false, nil },
			StageNormalize:    func(e StageEntry, _ MatchContext) (StageEntry, bool, *StageFailure) { return e, false, nil },
			StageAggregate:    func(e StageEntry, _ MatchContext) (StageEntry, bool, *StageFailure) { return e, false, nil },
		},
		StageOrder: []string{StageAcquisition, StageVerification, StageParse, StageNormalize, StageAggregate},
		MaxRetries: maxRetries,
		Save:       save,
	}
}

func buildBatchManifest(t *testing.T, ids ...string) DiscoveryManifestV1 {
	t.Helper()
	scope := buildScope(t)
	roster := buildRoster(t, scope)
	var matches []DiscoveryMatch
	base := mustParseTime(t, "2026-07-01T00:00:00Z")
	for i, id := range ids {
		m := makeMatch(id, base.Add(time.Duration(i)*time.Hour), MatchReplayAccessible, "team-a", "team-b")
		matches = append(matches, m)
	}
	return buildDiscovery(t, scope, roster, matches)
}

func TestBatchResumeSkipsSucceeded(t *testing.T) {
	manifest := buildBatchManifest(t, "m1", "m2", "m3")
	var saved StageBatch
	var saves int
	sc := newStageCounts()
	p := sc.buildPipeline(1, func(b StageBatch) error { saved = b; saves++; return nil })
	if _, err := p.Run(manifest, StageBatch{Entries: map[string]StageEntry{}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if saved.Entries["m1"].Status != StageSucceeded {
		t.Fatalf("m1 should be succeeded, got %s", saved.Entries["m1"].Status)
	}
	// Resume: succeeded entries must not re-run any stage.
	priorCalls := map[string]int{}
	for k, v := range sc.calls {
		priorCalls[k] = v
	}
	if _, err := p.Run(manifest, saved); err != nil {
		t.Fatalf("resume: %v", err)
	}
	for k, v := range sc.calls {
		if v != priorCalls[k] {
			t.Fatalf("resume re-ran stage %s (was %d now %d)", k, priorCalls[k], v)
		}
	}
	_ = saves
}

func TestBatchTerminalDeadLetterSkippedOnResume(t *testing.T) {
	manifest := buildBatchManifest(t, "m1", "m_bad")
	sc := newStageCounts()
	sc.acquireFail["m_bad"] = true
	var saved StageBatch
	p := sc.buildPipeline(1, func(b StageBatch) error { saved = b; return nil })
	_, err := p.Run(manifest, StageBatch{Entries: map[string]StageEntry{}})
	if err == nil {
		t.Fatalf("expected terminal failure error")
	}
	var tf *TerminalFailures
	if !errors.As(err, &tf) || len(tf.IDs) != 1 || tf.IDs[0] != "m_bad" {
		t.Fatalf("expected TerminalFailures{m_bad}, got %v", err)
	}
	if saved.Entries["m_bad"].Status != StageFailedTerminal {
		t.Fatalf("m_bad should be terminal, got %s", saved.Entries["m_bad"].Status)
	}
	if sc.calls["acquire:m_bad"] != 1 {
		t.Fatalf("m_bad should be attempted exactly once before terminal, got %d", sc.calls["acquire:m_bad"])
	}
	// Resume: terminal entry must not be re-attempted.
	before := sc.calls["acquire:m_bad"]
	if _, err := p.Run(manifest, saved); err == nil {
		t.Fatalf("resume should still report terminal failure")
	}
	if sc.calls["acquire:m_bad"] != before {
		t.Fatalf("resume re-attempted terminal entry: %d -> %d", before, sc.calls["acquire:m_bad"])
	}
}

func TestBatchRetriesUntilTerminal(t *testing.T) {
	manifest := buildBatchManifest(t, "m_flaky")
	sc := newStageCounts()
	sc.parseFail["m_flaky"] = true // never succeeds
	var saved StageBatch
	p := sc.buildPipeline(3, func(b StageBatch) error { saved = b; return nil })
	var err error
	for i := 0; i < 4; i++ {
		_, err = p.Run(manifest, saved)
		var tf *TerminalFailures
		if errors.As(err, &tf) {
			break // terminal failure reached
		}
		var pending *ErrRetryablePending
		if errors.As(err, &pending) {
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if err == nil {
		t.Fatalf("expected terminal failure after retries")
	}
	if sc.calls["parse:m_flaky"] != 3 {
		t.Fatalf("expected 3 parse attempts, got %d", sc.calls["parse:m_flaky"])
	}
	if saved.Entries["m_flaky"].Status != StageFailedTerminal {
		t.Fatalf("flaky should be terminal after retries, got %s", saved.Entries["m_flaky"].Status)
	}
}

func TestBatchCheckpointFailurePropagates(t *testing.T) {
	manifest := buildBatchManifest(t, "m1", "m2")
	sc := newStageCounts()
	saves := 0
	p := sc.buildPipeline(1, func(b StageBatch) error {
		saves++
		if saves == 2 {
			return errors.New("disk full")
		}
		return nil
	})
	if _, err := p.Run(manifest, StageBatch{Entries: map[string]StageEntry{}}); err == nil {
		t.Fatalf("expected checkpoint failure to propagate")
	}
}

func TestBatchDuplicateMatchManifestRejected(t *testing.T) {
	// A discovery manifest cannot contain duplicate match ids (Dedupe merges,
	// but a hand-built invalid manifest must fail validation).
	dm := buildBatchManifest(t, "m1")
	dm.Matches = append(dm.Matches, DiscoveryMatch{MatchID: "m1", SourceEventTime: mustParseTime(t, "2026-07-01T00:00:00Z"), State: MatchDiscovered})
	if err := dm.Validate(); err == nil {
		t.Fatalf("expected duplicate match ids to fail validation")
	}
}

func TestBatchResumesMidStage(t *testing.T) {
	manifest := buildBatchManifest(t, "m1")
	sc := newStageCounts()
	var saved StageBatch
	p := sc.buildPipeline(1, func(b StageBatch) error { saved = b; return nil })
	// Start from an identity-bound state where m1 reached acquire but not succeeded.
	prior := NewStageBatch(manifest)
	prior.Entries["m1"] = StageEntry{MatchID: "m1", Status: StageQueued, ReachedStage: StageAcquisition, ReplaySHA256: testSHA("m1")}
	if _, err := p.Run(manifest, prior); err != nil {
		t.Fatalf("resume mid-stage: %v", err)
	}
	if saved.Entries["m1"].Status != StageSucceeded {
		t.Fatalf("m1 should reach succeeded: %s", saved.Entries["m1"].Status)
	}
	// Acquire was already reached, so resume must NOT re-run it.
	if sc.calls["acquire:m1"] != 0 {
		t.Fatalf("resume re-ran already-reached acquire: %d", sc.calls["acquire:m1"])
	}
	_ = contracts.IdentityVerified
}

// TestBatchRejectsMismatchedManifestCheckpoint proves a checkpoint bound to
// one discovery manifest cannot be applied to a different manifest (which
// could skip or replay matching ids). The run must fail closed.
func TestBatchRejectsMismatchedManifestCheckpoint(t *testing.T) {
	manifestA := buildBatchManifest(t, "m1")
	manifestB := buildBatchManifest(t, "m2")
	sc := newStageCounts()
	p := sc.buildPipeline(1, func(b StageBatch) error { return nil })
	stale := NewStageBatch(manifestA)
	stale.Entries["m1"] = StageEntry{MatchID: "m1", Status: StageSucceeded, ReachedStage: StageAggregate}
	if _, err := p.Run(manifestB, stale); !errors.Is(err, ErrBatchManifestMismatch) {
		t.Fatalf("expected ErrBatchManifestMismatch, got %v", err)
	}
}

// TestBatchRetryablePendingIsNotSuccess proves a non-terminal failure below
// MaxRetries surfaces ErrRetryablePending rather than nil, so a caller cannot
// read a partially failed run as success.
func TestBatchRetryablePendingIsNotSuccess(t *testing.T) {
	manifest := buildBatchManifest(t, "m_flaky")
	sc := newStageCounts()
	sc.parseFail["m_flaky"] = true
	p := sc.buildPipeline(3, func(b StageBatch) error { return nil })
	_, err := p.Run(manifest, NewStageBatch(manifest))
	if err == nil {
		t.Fatalf("expected ErrRetryablePending, got nil")
	}
	var pending *ErrRetryablePending
	if !errors.As(err, &pending) {
		t.Fatalf("expected ErrRetryablePending, got %v", err)
	}
}

func TestBatchReconcilesCompletedEffectAfterCursorCheckpointFailure(t *testing.T) {
	manifest := buildBatchManifest(t, "m1")
	effects := 0
	artifacts := map[string]StageEntry{}
	saves := 0
	var durable StageBatch
	p := &StagePipeline{
		StageOrder: []string{StageAcquisition}, MaxRetries: 1,
		Stages: map[string]StageFunc{StageAcquisition: func(e StageEntry, _ MatchContext) (StageEntry, *StageFailure) {
			effects++
			e.ReplaySHA256 = testSHA("artifact")
			artifacts[e.MatchID] = e
			return e, nil
		}},
		Reconcile: map[string]StageReconcileFunc{StageAcquisition: func(e StageEntry, _ MatchContext) (StageEntry, bool, *StageFailure) {
			a, ok := artifacts[e.MatchID]
			return a, ok, nil
		}},
		Save: func(b StageBatch) error {
			saves++
			if saves == 3 {
				return errors.New("cursor checkpoint failed")
			}
			durable = cloneStageBatch(b)
			return nil
		},
	}
	if _, err := p.Run(manifest, NewStageBatch(manifest)); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if effects != 1 {
		t.Fatalf("effect calls=%d", effects)
	}
	p.Save = func(b StageBatch) error { durable = cloneStageBatch(b); return nil }
	if _, err := p.Run(manifest, durable); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if effects != 1 {
		t.Fatalf("resume duplicated completed effect: %d", effects)
	}
}

func cloneStageBatch(in StageBatch) StageBatch {
	out := in
	out.Entries = map[string]StageEntry{}
	for k, v := range in.Entries {
		out.Entries[k] = v
	}
	return out
}
