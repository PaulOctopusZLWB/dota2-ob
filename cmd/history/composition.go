package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
)

type stageComposition struct {
	stages    map[string]history.StageFunc
	reconcile map[string]history.StageReconcileFunc
	order     []string
	calls     map[string]int
	processed map[string]history.ProcessedReplayEvidence
	validate  func(history.StageEntry, history.MatchContext) error
}

type fixtureArtifact struct {
	Stage        string `json:"stage"`
	MatchID      string `json:"match_id"`
	InputSHA256  string `json:"input_sha256"`
	OutputSHA256 string `json:"output_sha256"`
}

type fileArtifactPort struct {
	stage     string
	root      string
	facts     map[string]history.NormalizedMatchFacts
	calls     map[string]int
	processed map[string]history.ProcessedReplayEvidence
	aggregate func(history.NormalizedMatchFacts) (string, error)
}

type localReplayFixture struct {
	Facts history.NormalizedMatchFacts `json:"facts"`
}

func localReplayBytes(f history.NormalizedMatchFacts) ([]byte, error) {
	f.ContentSHA256, f.ReplaySHA256 = "", ""
	return contracts.MarshalCanonical(localReplayFixture{Facts: f})
}

func bindLocalReplayFixture(f history.NormalizedMatchFacts) (history.NormalizedMatchFacts, error) {
	b, err := localReplayBytes(f)
	if err != nil {
		return f, err
	}
	h := sha256.Sum256(b)
	f.ReplaySHA256 = hex.EncodeToString(h[:])
	return f, history.SealNormalizedMatchFacts(&f)
}

func newLocalReplayStageComposition(facts []history.NormalizedMatchFacts, root string, aggregate func(history.NormalizedMatchFacts) (string, error)) stageComposition {
	byID := map[string]history.NormalizedMatchFacts{}
	for _, f := range facts {
		byID[f.MatchID] = f
	}
	calls := map[string]int{}
	processed := map[string]history.ProcessedReplayEvidence{}
	order := []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate}
	makePort := func(stage string) *fileArtifactPort {
		return &fileArtifactPort{stage: stage, root: root, facts: byID, calls: calls, processed: processed, aggregate: aggregate}
	}
	acquire, verify, parse := makePort(history.StageAcquisition), makePort(history.StageVerification), makePort(history.StageParse)
	normalize, aggregatePort := makePort(history.StageNormalize), makePort(history.StageAggregate)
	return stageComposition{
		stages:    map[string]history.StageFunc{history.StageAcquisition: acquire.Execute, history.StageVerification: verify.Execute, history.StageParse: parse.Execute, history.StageNormalize: normalize.Execute, history.StageAggregate: aggregatePort.Execute},
		reconcile: map[string]history.StageReconcileFunc{history.StageAcquisition: acquire.Reconcile, history.StageVerification: verify.Reconcile, history.StageParse: parse.Reconcile, history.StageNormalize: normalize.Reconcile, history.StageAggregate: aggregatePort.Reconcile},
		order:     order, calls: calls, processed: processed,
		validate: func(e history.StageEntry, ctx history.MatchContext) error {
			for _, port := range []*fileArtifactPort{acquire, verify, parse, normalize, aggregatePort} {
				if _, done, failure := port.Reconcile(e, ctx); failure != nil || !done {
					if failure != nil {
						return fmt.Errorf("%s: %s", failure.Stage, failure.Reason)
					}
					return fmt.Errorf("%s artifact missing", port.stage)
				}
			}
			return nil
		},
	}
}

func (p *fileArtifactPort) Execute(e history.StageEntry, ctx history.MatchContext) (history.StageEntry, *history.StageFailure) {
	p.calls[p.stage+":"+e.MatchID]++
	if p.stage == history.StageAcquisition && ctx.Discovery.State != history.MatchReplayAccessible {
		reason := "replay_not_accessible:" + ctx.Discovery.State
		if ctx.Discovery.State == history.MatchReplayQuarantined {
			reason = "identity_not_correlated"
		}
		return e, &history.StageFailure{Stage: p.stage, Reason: reason, Terminal: true}
	}
	if sf := p.executeSourceOperation(e, ctx); sf != nil {
		return e, sf
	}
	a, ok := p.expected(e, ctx)
	if !ok {
		return e, &history.StageFailure{Stage: p.stage, Reason: "missing_fixture_input", Terminal: true}
	}
	if p.stage == history.StageNormalize {
		payload, err := localReplayBytes(p.facts[e.MatchID])
		if err != nil {
			return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
		}
		proof := history.ProcessedReplayEvidence{MatchID: e.MatchID}
		for _, id := range []string{"independent-pass-a", "independent-pass-b"} {
			var raw localReplayFixture
			if err := contracts.DecodeStrict(payload, &raw); err != nil {
				return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
			}
			raw.Facts.ReplaySHA256 = ctx.Discovery.ReplaySHA256
			if err := history.SealNormalizedMatchFacts(&raw.Facts); err != nil || raw.Facts.ContentSHA256 != p.facts[e.MatchID].ContentSHA256 {
				return e, &history.StageFailure{Stage: p.stage, Reason: "nondeterministic_normalize", Terminal: true}
			}
			pass := history.ParseExecutionEvidence{SchemaVersion: "history.parse-execution.v1", ExecutionID: id, MatchID: e.MatchID, ReplaySHA256: raw.Facts.ReplaySHA256, FactsSHA256: raw.Facts.ContentSHA256, ParserVersion: "local-replay-fixture/v1", AdapterVersion: history.AdapterName + "/" + history.AdapterVersion, ConfigSHA256: sha256Text("local-replay-config-v1"), ParsedArtifactSHA256: sha256Text(id + ":parsed:" + raw.Facts.ContentSHA256), NormalizedArtifactSHA256: sha256Text(id + ":normalized:" + raw.Facts.ContentSHA256), RunArtifactSHA256: sha256Text(id + ":run:" + raw.Facts.ContentSHA256), CheckpointSHA256: sha256Text(id + ":checkpoint:" + raw.Facts.ContentSHA256), Deterministic: true}
			normalizedJSON, err := contracts.MarshalCanonical(raw.Facts)
			if err != nil {
				return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
			}
			pass, err = materializeParseExecution(filepath.Join(p.root, "executions"), pass, payload, normalizedJSON)
			if err != nil {
				return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
			}
			proof.Passes = append(proof.Passes, pass)
		}
		p.processed[e.MatchID] = proof
		proofBytes, err := contracts.MarshalCanonical(proof)
		if err != nil {
			return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
		}
		if err := atomicfile.WriteFile(p.parseProofPath(e.MatchID), append(proofBytes, '\n'), 0o644); err != nil {
			return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: false}
		}
	}
	b, err := contracts.MarshalCanonical(a)
	if err != nil {
		return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
	}
	if err := atomicfile.WriteFile(p.path(e.MatchID), append(b, '\n'), 0o644); err != nil {
		return e, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: false}
	}
	return p.apply(e, a), nil
}

func (p *fileArtifactPort) Reconcile(e history.StageEntry, ctx history.MatchContext) (history.StageEntry, bool, *history.StageFailure) {
	if p.stage != history.StageAcquisition {
		if sf := p.verifyReplaySource(e, ctx); sf != nil {
			return e, false, sf
		}
	}
	want, ok := p.expected(e, ctx)
	if !ok {
		return e, false, nil
	}
	b, err := os.ReadFile(p.path(e.MatchID))
	if os.IsNotExist(err) {
		return e, false, nil
	}
	if err != nil {
		return e, false, &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: false}
	}
	var got fixtureArtifact
	if err := contracts.DecodeStrict(b, &got); err != nil {
		return e, false, &history.StageFailure{Stage: p.stage, Reason: "corrupt_artifact", Terminal: true}
	}
	if got != want {
		return e, false, &history.StageFailure{Stage: p.stage, Reason: "artifact_identity_mismatch", Terminal: true}
	}
	if p.stage == history.StageNormalize {
		b, err := os.ReadFile(p.parseProofPath(e.MatchID))
		if err != nil {
			return e, false, &history.StageFailure{Stage: p.stage, Reason: "parse_evidence_missing", Terminal: true}
		}
		var proof history.ProcessedReplayEvidence
		if err := contracts.DecodeStrict(b, &proof); err != nil || proof.MatchID != e.MatchID || len(proof.Passes) < 2 {
			return e, false, &history.StageFailure{Stage: p.stage, Reason: "parse_evidence_corrupt", Terminal: true}
		}
		seen := map[string]bool{}
		for _, pass := range proof.Passes {
			if validateParseExecutionArtifacts(filepath.Join(p.root, "executions"), pass) != nil || pass.MatchID != e.MatchID || pass.ReplaySHA256 != ctx.Discovery.ReplaySHA256 || pass.FactsSHA256 != p.facts[e.MatchID].ContentSHA256 || seen[pass.ContentSHA256] {
				return e, false, &history.StageFailure{Stage: p.stage, Reason: "parse_evidence_mismatch", Terminal: true}
			}
			seen[pass.ContentSHA256] = true
		}
		p.processed[e.MatchID] = proof
	}
	return p.apply(e, got), true, nil
}

func (p *fileArtifactPort) replayPath(matchID string) string {
	// This path is exclusively a deterministic unit/integration fixture input.
	// It is deliberately labeled JSON and can never be mistaken for real Dota
	// replay evidence; real PBDEMS2 inputs use real_replay.go.
	return filepath.Join(p.root, "fixture-inputs", sha256Text(matchID)+".fixture.json")
}

func (p *fileArtifactPort) parseProofPath(matchID string) string {
	return filepath.Join(p.root, "parse-evidence", sha256Text(matchID)+".json")
}

func (p *fileArtifactPort) verifyReplaySource(e history.StageEntry, ctx history.MatchContext) *history.StageFailure {
	b, err := os.ReadFile(p.replayPath(e.MatchID))
	if err != nil {
		return &history.StageFailure{Stage: p.stage, Reason: "replay_source_missing", Terminal: false}
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != ctx.Discovery.ReplaySHA256 {
		return &history.StageFailure{Stage: p.stage, Reason: "replay_checksum_mismatch", Terminal: true}
	}
	return nil
}

func (p *fileArtifactPort) executeSourceOperation(e history.StageEntry, ctx history.MatchContext) *history.StageFailure {
	f, ok := p.facts[e.MatchID]
	if !ok {
		return &history.StageFailure{Stage: p.stage, Reason: "missing_source_input", Terminal: true}
	}
	if p.stage == history.StageAcquisition {
		b, err := localReplayBytes(f)
		if err != nil {
			return &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
		}
		if err := atomicfile.WriteFile(p.replayPath(e.MatchID), b, 0o644); err != nil {
			return &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: false}
		}
		return p.verifyReplaySource(e, ctx)
	}
	if sf := p.verifyReplaySource(e, ctx); sf != nil {
		return sf
	}
	if p.stage == history.StageParse || p.stage == history.StageNormalize {
		b, _ := os.ReadFile(p.replayPath(e.MatchID))
		var raw localReplayFixture
		if err := contracts.DecodeStrict(b, &raw); err != nil {
			return &history.StageFailure{Stage: p.stage, Reason: "parse_failed", Terminal: true}
		}
		raw.Facts.ReplaySHA256 = ctx.Discovery.ReplaySHA256
		if err := history.SealNormalizedMatchFacts(&raw.Facts); err != nil || raw.Facts.ContentSHA256 != f.ContentSHA256 {
			return &history.StageFailure{Stage: p.stage, Reason: "normalize_identity_mismatch", Terminal: true}
		}
	}
	if p.stage == history.StageAggregate {
		if p.aggregate == nil {
			return &history.StageFailure{Stage: p.stage, Reason: "aggregate_not_configured", Terminal: true}
		}
		if _, err := p.aggregate(f); err != nil {
			return &history.StageFailure{Stage: p.stage, Reason: err.Error(), Terminal: true}
		}
	}
	return nil
}

func (p *fileArtifactPort) expected(e history.StageEntry, ctx history.MatchContext) (fixtureArtifact, bool) {
	f, ok := p.facts[e.MatchID]
	if !ok {
		return fixtureArtifact{}, false
	}
	input := ctx.EntrySHA256
	order := []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate}
	for i, stage := range order {
		if stage == p.stage && i > 0 {
			input = e.ArtifactSHA256[order[i-1]]
		}
	}
	output := sha256Text(p.stage + ":" + e.MatchID + ":" + input + ":" + f.ContentSHA256)
	switch p.stage {
	case history.StageAcquisition:
		output = f.ReplaySHA256
	case history.StageVerification:
		output = f.ReplaySHA256
	case history.StageNormalize:
		output = f.ContentSHA256
	case history.StageAggregate:
		if p.aggregate == nil {
			return fixtureArtifact{}, false
		}
		var err error
		output, err = p.aggregate(f)
		if err != nil {
			return fixtureArtifact{}, false
		}
	}
	return fixtureArtifact{Stage: p.stage, MatchID: e.MatchID, InputSHA256: input, OutputSHA256: output}, true
}

func (p *fileArtifactPort) apply(e history.StageEntry, a fixtureArtifact) history.StageEntry {
	if e.ArtifactSHA256 == nil {
		e.ArtifactSHA256 = map[string]string{}
	}
	if e.ArtifactInputSHA256 == nil {
		e.ArtifactInputSHA256 = map[string]string{}
	}
	e.ArtifactInputSHA256[p.stage] = a.InputSHA256
	e.ArtifactSHA256[p.stage] = a.OutputSHA256
	if p.stage == history.StageAcquisition {
		e.ReplaySHA256 = a.OutputSHA256
	}
	if p.stage == history.StageNormalize {
		e.FactsSHA256 = a.OutputSHA256
	}
	return e
}

func (p *fileArtifactPort) path(matchID string) string {
	return filepath.Join(p.root, p.stage, sha256Text(matchID)+".json")
}

func sha256Text(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func artifactTreeSHA(root string) (string, int64, int, error) {
	type item struct {
		Path, SHA string
		Size      int64
	}
	items := []item{}
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		sum := sha256.Sum256(b)
		items = append(items, item{Path: rel, SHA: hex.EncodeToString(sum[:]), Size: info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	b, err := contracts.MarshalCanonical(items)
	if err != nil {
		return "", 0, 0, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), total, len(items), nil
}

func verifyInterruptionRecovery(manifest history.DiscoveryManifestV1, facts []history.NormalizedMatchFacts, root string, aggregate func(history.NormalizedMatchFacts) (string, error)) (map[string]int, error) {
	var match history.DiscoveryMatch
	for _, m := range manifest.Matches {
		if m.State == history.MatchReplayAccessible {
			match = m
			break
		}
	}
	if match.MatchID == "" {
		return nil, fmt.Errorf("no accessible fixture match")
	}
	one := manifest
	one.ManifestID, one.ContentSHA256 = "", ""
	one.Matches = []history.DiscoveryMatch{match}
	one.Coverage = history.SummarizeCoverage(one.Matches)
	if err := history.SealDiscoveryManifestV1(&one); err != nil {
		return nil, err
	}
	var fact history.NormalizedMatchFacts
	for _, f := range facts {
		if f.MatchID == match.MatchID {
			fact = f
			break
		}
	}
	order := []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate}
	totals := map[string]int{}
	for failAt := 1; failAt <= 12; failAt++ {
		caseRoot := filepath.Join(root, fmt.Sprintf("boundary-%02d", failAt))
		first := newLocalReplayStageComposition([]history.NormalizedMatchFacts{fact}, caseRoot, aggregate)
		var durable []byte
		saves := 0
		p := &history.StagePipeline{Stages: first.stages, Reconcile: first.reconcile, ValidateCompleted: first.validate, StageOrder: first.order, MaxRetries: 1, Save: func(b history.StageBatch) error {
			saves++
			if saves == failAt {
				return fmt.Errorf("injected checkpoint boundary %d", failAt)
			}
			enc, err := contracts.MarshalCanonical(b)
			if err == nil {
				durable = enc
			}
			return err
		}}
		_, err := p.Run(one, history.NewStageBatch(one))
		if err == nil {
			return nil, fmt.Errorf("boundary %d did not interrupt", failAt)
		}
		prior := history.NewStageBatch(one)
		if len(durable) > 0 {
			if err := contracts.DecodeStrict(durable, &prior); err != nil {
				return nil, err
			}
		}
		second := newLocalReplayStageComposition([]history.NormalizedMatchFacts{fact}, caseRoot, aggregate)
		p = &history.StagePipeline{Stages: second.stages, Reconcile: second.reconcile, ValidateCompleted: second.validate, StageOrder: second.order, MaxRetries: 1, Save: func(b history.StageBatch) error { return nil }}
		if _, err := p.Run(one, prior); err != nil {
			return nil, fmt.Errorf("boundary %d resume: %w", failAt, err)
		}
		for _, stage := range order {
			calls := first.calls[stage+":"+match.MatchID] + second.calls[stage+":"+match.MatchID]
			if calls != 1 {
				return nil, fmt.Errorf("boundary %d stage %s effects=%d", failAt, stage, calls)
			}
			totals[stage] += calls
		}
	}
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return totals, nil
}
