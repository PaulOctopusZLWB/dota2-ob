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

// historyStagePort is the production-shaped boundary for one durable stage.
// Execute may write an external artifact; Reconcile must recognize that exact
// content identity after a crash before cursor advancement is checkpointed.
type historyStagePort interface {
	Execute(history.StageEntry, history.MatchContext) (history.StageEntry, *history.StageFailure)
	Reconcile(history.StageEntry, history.MatchContext) (history.StageEntry, bool, *history.StageFailure)
}

type stageComposition struct {
	stages    map[string]history.StageFunc
	reconcile map[string]history.StageReconcileFunc
	order     []string
	calls     map[string]int
}

func composeStagePorts(ports map[string]historyStagePort, order []string, calls map[string]int) stageComposition {
	c := stageComposition{stages: map[string]history.StageFunc{}, reconcile: map[string]history.StageReconcileFunc{}, order: append([]string(nil), order...), calls: calls}
	for _, stage := range order {
		port := ports[stage]
		c.stages[stage] = port.Execute
		c.reconcile[stage] = port.Reconcile
	}
	return c
}

type fixtureArtifact struct {
	Stage        string `json:"stage"`
	MatchID      string `json:"match_id"`
	InputSHA256  string `json:"input_sha256"`
	OutputSHA256 string `json:"output_sha256"`
}

type fileArtifactPort struct {
	stage string
	root  string
	facts map[string]history.NormalizedMatchFacts
	calls map[string]int
}

func newFixtureStageComposition(facts []history.NormalizedMatchFacts, root string) stageComposition {
	byID := map[string]history.NormalizedMatchFacts{}
	for _, f := range facts {
		byID[f.MatchID] = f
	}
	calls := map[string]int{}
	order := []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate}
	ports := map[string]historyStagePort{}
	for _, stage := range order {
		ports[stage] = &fileArtifactPort{stage: stage, root: root, facts: byID, calls: calls}
	}
	return composeStagePorts(ports, order, calls)
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
	a, ok := p.expected(e, ctx)
	if !ok {
		return e, &history.StageFailure{Stage: p.stage, Reason: "missing_fixture_input", Terminal: true}
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
	return p.apply(e, got), true, nil
}

func (p *fileArtifactPort) expected(e history.StageEntry, ctx history.MatchContext) (fixtureArtifact, bool) {
	f, ok := p.facts[e.MatchID]
	if !ok {
		return fixtureArtifact{}, false
	}
	input := ctx.Discovery.ReplaySHA256
	output := sha256Text(p.stage + ":" + e.MatchID + ":" + input + ":" + f.ContentSHA256)
	switch p.stage {
	case history.StageAcquisition:
		output = f.ReplaySHA256
	case history.StageNormalize:
		output = f.ContentSHA256
	}
	return fixtureArtifact{Stage: p.stage, MatchID: e.MatchID, InputSHA256: input, OutputSHA256: output}, true
}

func (p *fileArtifactPort) apply(e history.StageEntry, a fixtureArtifact) history.StageEntry {
	if e.ArtifactSHA256 == nil {
		e.ArtifactSHA256 = map[string]string{}
	}
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

func verifyInterruptionRecovery(manifest history.DiscoveryManifestV1, facts []history.NormalizedMatchFacts, root string) (map[string]int, error) {
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
		first := newFixtureStageComposition([]history.NormalizedMatchFacts{fact}, caseRoot)
		var durable []byte
		saves := 0
		p := &history.StagePipeline{Stages: first.stages, Reconcile: first.reconcile, StageOrder: first.order, MaxRetries: 1, Save: func(b history.StageBatch) error {
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
		second := newFixtureStageComposition([]history.NormalizedMatchFacts{fact}, caseRoot)
		p = &history.StagePipeline{Stages: second.stages, Reconcile: second.reconcile, StageOrder: second.order, MaxRetries: 1, Save: func(b history.StageBatch) error { return nil }}
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
