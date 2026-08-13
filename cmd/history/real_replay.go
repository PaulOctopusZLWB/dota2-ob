package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/history"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
)

type realReplayConfig struct {
	SourcePath      string
	Root            string
	MatchID         string
	ReplayURL       string
	SourceEventTime time.Time
	PatchID         string
	GameBuild       uint32
	RadiantTeamID   string
	DireTeamID      string
	DurationSeconds int64
}

type realReplayResult struct {
	ManifestSHA256 string
	ReplayIdentity replay.Source2DemoIdentity
	Batch          history.StageBatch
	Processed      history.ProcessedReplayEvidence
	Facts          history.NormalizedMatchFacts
	AggregateSHA   string
	StageCalls     map[string]int
	ParseMetrics   []replay.Metrics
	ArtifactTree   string
	StorageBytes   int64
	ArtifactFiles  int
	ExecutionRoot  string
}

type realParseIndex struct {
	SchemaVersion string              `json:"schema_version"`
	MatchID       string              `json:"match_id"`
	ReplaySHA256  string              `json:"replay_sha256"`
	Runs          []realParseIndexRun `json:"runs"`
}

type realParseIndexRun struct {
	ExecutionID string `json:"execution_id"`
	PayloadPath string `json:"payload_path"`
	PayloadSHA  string `json:"payload_sha256"`
	FactsHash   string `json:"facts_hash"`
}

type realReplayComposition struct {
	cfg           realReplayConfig
	manifest      history.DiscoveryManifestV1
	roster        history.RosterManifestV1
	windows       history.CutoffWindow
	calls         map[string]int
	processed     history.ProcessedReplayEvidence
	facts         history.NormalizedMatchFacts
	parseIndexSHA string
	aggregateSHA  string
	metrics       []replay.Metrics
}

func runRealReplayComposition(cfg realReplayConfig) (*realReplayResult, error) {
	if cfg.SourcePath == "" || cfg.Root == "" || cfg.MatchID == "" || cfg.ReplayURL == "" || cfg.SourceEventTime.IsZero() || cfg.PatchID == "" || cfg.GameBuild == 0 || cfg.RadiantTeamID == "" || cfg.DireTeamID == "" || cfg.DurationSeconds <= 0 {
		return nil, errors.New("real replay composition: incomplete input")
	}
	identity, err := replay.InspectSource2DemoFile(cfg.SourcePath)
	if err != nil {
		return nil, err
	}
	manifest, roster, windows, err := realReplayManifest(cfg, identity.SHA256)
	if err != nil {
		return nil, err
	}
	c := &realReplayComposition{cfg: cfg, manifest: manifest, roster: roster, windows: windows, calls: map[string]int{}}
	stages, reconcilers := c.ports()
	statePath := filepath.Join(cfg.Root, "batch-state.json")
	p := &history.StagePipeline{
		Stages: stages, Reconcile: reconcilers, ValidateCompleted: c.validateCompleted,
		StageOrder: []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate},
		MaxRetries: 1, Save: func(b history.StageBatch) error { return saveBatch(statePath, b) },
	}
	batch, err := p.Run(manifest, history.NewStageBatch(manifest))
	if err != nil {
		return nil, err
	}
	loaded, err := loadBatch(statePath)
	if err != nil {
		return nil, err
	}
	if _, err := p.Run(manifest, loaded); err != nil {
		return nil, fmt.Errorf("real replay resume: %w", err)
	}
	tree, storage, files, err := artifactTreeSHA(cfg.Root)
	if err != nil {
		return nil, err
	}
	return &realReplayResult{ManifestSHA256: manifest.ContentSHA256, ReplayIdentity: identity, Batch: batch, Processed: c.processed, Facts: c.facts, AggregateSHA: c.aggregateSHA, StageCalls: c.calls, ParseMetrics: c.metrics, ArtifactTree: tree, StorageBytes: storage, ArtifactFiles: files, ExecutionRoot: c.executionRoot()}, nil
}

func realReplayManifest(cfg realReplayConfig, replaySHA string) (history.DiscoveryManifestV1, history.RosterManifestV1, history.CutoffWindow, error) {
	cutoff := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	patchRelease := time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC)
	scope := buildScopeFixture(cutoff)
	roster := buildRosterFixture(scope)
	m := history.DiscoveryMatch{
		MatchID: cfg.MatchID, SourceEventTime: cfg.SourceEventTime, PatchID: cfg.PatchID, GameBuild: cfg.GameBuild,
		RadiantTeamID: cfg.RadiantTeamID, DireTeamID: cfg.DireTeamID, ReplayURL: cfg.ReplayURL,
		Providers: []string{history.ProviderOpenDota, history.ProviderValveCDN}, State: history.MatchReplayQuarantined,
		IdentityStatus: contracts.IdentityQuarantined, ReplaySHA256: replaySHA,
	}
	dm := history.DiscoveryManifestV1{
		SchemaVersion: history.DiscoverySchema, TournamentScopeID: scope.ScopeID, TournamentScopeSHA: scope.ContentSHA256,
		RosterManifestID: roster.ManifestID, CutoffTime: cutoff,
		Request: history.DiscoveryRequest{ContractVersion: history.DiscoveryContractVersion, Providers: []string{history.ProviderOpenDota, history.ProviderValveCDN}, Endpoint: "https://api.opendota.com/api/matches/" + cfg.MatchID, PageLimit: 1, CutoffTime: cutoff, RetrievedAt: cutoff, PageSHA256: []string{replaySHA}},
		Matches: []history.DiscoveryMatch{m},
	}
	dm.Coverage = history.SummarizeCoverage(dm.Matches)
	if err := history.SealDiscoveryManifestV1(&dm); err != nil {
		return history.DiscoveryManifestV1{}, history.RosterManifestV1{}, history.CutoffWindow{}, err
	}
	return dm, roster, history.NewCutoffWindow(cutoff, history.PatchWindow{PatchID: cfg.PatchID, DotaPatch: "7.41"}, patchRelease), nil
}

func (c *realReplayComposition) ports() (map[string]history.StageFunc, map[string]history.StageReconcileFunc) {
	execute := map[string]history.StageFunc{}
	reconcile := map[string]history.StageReconcileFunc{}
	for _, stage := range []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate} {
		stage := stage
		execute[stage] = func(e history.StageEntry, ctx history.MatchContext) (history.StageEntry, *history.StageFailure) {
			c.calls[stage]++
			var err error
			switch stage {
			case history.StageAcquisition:
				err = copySource2Demo(c.cfg.SourcePath, c.replayPath(), ctx.Discovery.ReplaySHA256)
			case history.StageVerification:
				err = c.verifyReplay(ctx.Discovery.ReplaySHA256)
			case history.StageParse:
				err = c.parseTwice()
			case history.StageNormalize:
				err = c.normalizeTwice()
			case history.StageAggregate:
				err = c.aggregate()
			}
			if err != nil {
				return e, &history.StageFailure{Stage: stage, Reason: err.Error(), Terminal: true}
			}
			return c.applyStage(e, ctx, stage), nil
		}
		reconcile[stage] = func(e history.StageEntry, ctx history.MatchContext) (history.StageEntry, bool, *history.StageFailure) {
			if err := c.validateStage(e, ctx, stage); err != nil {
				if os.IsNotExist(err) {
					return e, false, nil
				}
				return e, false, &history.StageFailure{Stage: stage, Reason: err.Error(), Terminal: true}
			}
			return c.applyStage(e, ctx, stage), true, nil
		}
	}
	return execute, reconcile
}

func (c *realReplayComposition) applyStage(e history.StageEntry, ctx history.MatchContext, stage string) history.StageEntry {
	if e.ArtifactInputSHA256 == nil {
		e.ArtifactInputSHA256 = map[string]string{}
	}
	if e.ArtifactSHA256 == nil {
		e.ArtifactSHA256 = map[string]string{}
	}
	switch stage {
	case history.StageAcquisition:
		e.ArtifactInputSHA256[stage], e.ArtifactSHA256[stage], e.ReplaySHA256 = ctx.EntrySHA256, ctx.Discovery.ReplaySHA256, ctx.Discovery.ReplaySHA256
	case history.StageVerification:
		e.ArtifactInputSHA256[stage], e.ArtifactSHA256[stage] = ctx.Discovery.ReplaySHA256, ctx.Discovery.ReplaySHA256
	case history.StageParse:
		e.ArtifactInputSHA256[stage], e.ArtifactSHA256[stage] = ctx.Discovery.ReplaySHA256, c.parseIndexSHA
	case history.StageNormalize:
		e.ArtifactInputSHA256[stage], e.ArtifactSHA256[stage], e.FactsSHA256 = e.ArtifactSHA256[history.StageParse], c.facts.ContentSHA256, c.facts.ContentSHA256
	case history.StageAggregate:
		e.ArtifactInputSHA256[stage], e.ArtifactSHA256[stage] = e.FactsSHA256, c.aggregateSHA
	}
	return e
}

func (c *realReplayComposition) parseTwice() error {
	runs := []realParseIndexRun{}
	c.metrics = nil
	for _, id := range []string{"manta-pass-a", "manta-pass-b"} {
		result, err := replay.ParseFile(c.replayPath())
		if err != nil {
			return err
		}
		payload, err := result.Facts.CanonicalJSON()
		if err != nil {
			return err
		}
		path := c.parsePayloadPath(id)
		if err := atomicfile.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
			return err
		}
		runs = append(runs, realParseIndexRun{ExecutionID: id, PayloadPath: filepath.Base(path), PayloadSHA: shaBytes(payload), FactsHash: result.Hash})
		c.metrics = append(c.metrics, result.Metrics)
	}
	if runs[0].FactsHash != runs[1].FactsHash || runs[0].PayloadSHA != runs[1].PayloadSHA {
		return errors.New("manta parse is not deterministic")
	}
	index := realParseIndex{SchemaVersion: "history.real-parse-index.v1", MatchID: c.cfg.MatchID, ReplaySHA256: c.manifest.Matches[0].ReplaySHA256, Runs: runs}
	b, err := contracts.MarshalCanonical(index)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(c.parseIndexPath(), append(b, '\n'), 0o644); err != nil {
		return err
	}
	c.parseIndexSHA, err = fileSHA(c.parseIndexPath())
	return err
}

func (c *realReplayComposition) normalizeTwice() error {
	index, err := c.readParseIndex()
	if err != nil {
		return err
	}
	proof := history.ProcessedReplayEvidence{MatchID: c.cfg.MatchID}
	configSHA, err := c.normalizeConfigSHA()
	if err != nil {
		return err
	}
	var first history.NormalizedMatchFacts
	for _, run := range index.Runs {
		parsedJSON, err := os.ReadFile(c.parsePayloadPath(run.ExecutionID))
		if err != nil {
			return err
		}
		parsedJSON = trimNewline(parsedJSON)
		var parsed replay.ReplayFactsV1
		if err := json.Unmarshal(parsedJSON, &parsed); err != nil {
			return err
		}
		duration := c.cfg.DurationSeconds
		normalized, err := replay.Normalize(&parsed, replay.NormalizeMeta{MatchID: c.cfg.MatchID, ReplaySHA256: c.manifest.Matches[0].ReplaySHA256, SourceEventTime: c.cfg.SourceEventTime, PatchID: c.cfg.PatchID, RadiantTeamID: c.cfg.RadiantTeamID, DireTeamID: c.cfg.DireTeamID, GameBuild: c.cfg.GameBuild, DurationSeconds: &duration}, nil)
		if err != nil {
			return err
		}
		pass, err := materializeParseExecution(c.executionRoot(), history.ParseExecutionEvidence{SchemaVersion: "history.parse-execution.v1", ExecutionID: run.ExecutionID, MatchID: c.cfg.MatchID, ReplaySHA256: c.manifest.Matches[0].ReplaySHA256, FactsSHA256: normalized.ContentSHA256, ParserVersion: replay.ParserName + "/" + replay.ParserVersion, AdapterVersion: replay.AdapterName + "/" + replay.AdapterVersion, ConfigSHA256: configSHA, Deterministic: true}, parseExecutionMaterial{Parsed: parsed, Normalized: *normalized})
		if err != nil {
			return err
		}
		proof.Passes = append(proof.Passes, pass)
		if first.MatchID == "" {
			first = *normalized
		} else if first.ContentSHA256 != normalized.ContentSHA256 {
			return errors.New("normalizer is not deterministic")
		}
	}
	c.facts, c.processed = first, proof
	factsJSON, err := contracts.MarshalCanonical(c.facts)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(c.factsPath(), append(factsJSON, '\n'), 0o644); err != nil {
		return err
	}
	proofJSON, err := contracts.MarshalCanonical(proof)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(c.proofPath(), append(proofJSON, '\n'), 0o644)
}

func (c *realReplayComposition) aggregate() error {
	cells, err := history.Aggregate(history.AggregateInput{Facts: []history.NormalizedMatchFacts{c.facts}, Roster: c.roster, Windows: c.windows, Patch: history.PatchWindow{PatchID: c.cfg.PatchID, DotaPatch: "7.41"}, GeneratedAt: c.manifest.CutoffTime.Add(-time.Hour)})
	if err != nil {
		return err
	}
	b, err := contracts.MarshalCanonical(cells)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(c.aggregatePath(), append(b, '\n'), 0o644); err != nil {
		return err
	}
	c.aggregateSHA, err = fileSHA(c.aggregatePath())
	return err
}

func (c *realReplayComposition) validateStage(e history.StageEntry, ctx history.MatchContext, stage string) error {
	switch stage {
	case history.StageAcquisition, history.StageVerification:
		return c.verifyReplay(ctx.Discovery.ReplaySHA256)
	case history.StageParse:
		_, err := c.readParseIndex()
		if err != nil {
			return err
		}
		sha, err := fileSHA(c.parseIndexPath())
		if err != nil {
			return err
		}
		if checkpointSHA := e.ArtifactSHA256[stage]; checkpointSHA != "" && sha != checkpointSHA {
			return errors.New("parse index artifact identity mismatch")
		}
		c.parseIndexSHA = sha
		return nil
	case history.StageNormalize:
		return c.validateNormalized()
	case history.StageAggregate:
		sha, err := fileSHA(c.aggregatePath())
		if err != nil {
			return err
		}
		if sha != e.ArtifactSHA256[stage] {
			return errors.New("aggregate artifact identity mismatch")
		}
		return nil
	}
	return errors.New("unknown stage")
}

func (c *realReplayComposition) validateCompleted(e history.StageEntry, ctx history.MatchContext) error {
	for _, stage := range []string{history.StageAcquisition, history.StageVerification, history.StageParse, history.StageNormalize, history.StageAggregate} {
		if err := c.validateStage(e, ctx, stage); err != nil {
			return fmt.Errorf("real replay completed %s: %w", stage, err)
		}
		if stage == e.ReachedStage {
			break
		}
	}
	return nil
}

func (c *realReplayComposition) verifyReplay(want string) error {
	id, err := replay.InspectSource2DemoFile(c.replayPath())
	if err != nil {
		return err
	}
	if id.SHA256 != want {
		return errors.New("verified replay sha256 mismatch")
	}
	return nil
}

func (c *realReplayComposition) readParseIndex() (realParseIndex, error) {
	b, err := os.ReadFile(c.parseIndexPath())
	if err != nil {
		return realParseIndex{}, err
	}
	var index realParseIndex
	if err := json.Unmarshal(b, &index); err != nil {
		return realParseIndex{}, err
	}
	if index.SchemaVersion != "history.real-parse-index.v1" || index.MatchID != c.cfg.MatchID || index.ReplaySHA256 != c.manifest.Matches[0].ReplaySHA256 || len(index.Runs) != 2 || index.Runs[0].ExecutionID == index.Runs[1].ExecutionID {
		return realParseIndex{}, errors.New("invalid real parse index")
	}
	for _, run := range index.Runs {
		if run.PayloadPath != filepath.Base(c.parsePayloadPath(run.ExecutionID)) {
			return realParseIndex{}, errors.New("parsed run path identity mismatch")
		}
		b, err := os.ReadFile(c.parsePayloadPath(run.ExecutionID))
		payload := trimNewline(b)
		if err != nil || shaBytes(payload) != run.PayloadSHA || run.FactsHash != run.PayloadSHA {
			return realParseIndex{}, errors.New("parsed run artifact missing or mismatched")
		}
		var facts replay.ReplayFactsV1
		if err := contracts.DecodeStrict(payload, &facts); err != nil || facts.SchemaVersion != replay.FactsSchema || facts.Provenance.ParserName+"/"+facts.Provenance.ParserVersion != replay.ParserName+"/"+replay.ParserVersion || facts.Provenance.AdapterName+"/"+facts.Provenance.AdapterVersion != replay.AdapterName+"/"+replay.AdapterVersion || validateReplayFactsStructure(facts) != nil {
			return realParseIndex{}, errors.New("parsed run typed payload invalid")
		}
		canonical, err := facts.CanonicalJSON()
		if err != nil || string(canonical) != string(payload) {
			return realParseIndex{}, errors.New("parsed run payload is not canonical")
		}
	}
	c.parseIndexSHA, err = fileSHA(c.parseIndexPath())
	if err != nil {
		return realParseIndex{}, err
	}
	return index, nil
}

func (c *realReplayComposition) validateNormalized() error {
	b, err := os.ReadFile(c.factsPath())
	if err != nil {
		return err
	}
	var facts history.NormalizedMatchFacts
	if err := json.Unmarshal(b, &facts); err != nil || facts.Validate() != nil {
		return errors.New("normalized facts artifact invalid")
	}
	proofBytes, err := os.ReadFile(c.proofPath())
	if err != nil {
		return err
	}
	var proof history.ProcessedReplayEvidence
	if err := json.Unmarshal(proofBytes, &proof); err != nil || proof.MatchID != c.cfg.MatchID || len(proof.Passes) != 2 {
		return errors.New("parse execution proof invalid")
	}
	for _, pass := range proof.Passes {
		if err := validateParseExecutionArtifacts(c.executionRoot(), pass); err != nil || pass.FactsSHA256 != facts.ContentSHA256 {
			return errors.New("durable parse execution invalid")
		}
	}
	c.facts, c.processed = facts, proof
	return nil
}

func (c *realReplayComposition) normalizeConfigSHA() (string, error) {
	b, err := contracts.MarshalCanonical(struct {
		MatchID, PatchID, RadiantTeamID, DireTeamID string
		GameBuild                                   uint32
		DurationSeconds                             int64
	}{c.cfg.MatchID, c.cfg.PatchID, c.cfg.RadiantTeamID, c.cfg.DireTeamID, c.cfg.GameBuild, c.cfg.DurationSeconds})
	if err != nil {
		return "", err
	}
	return shaBytes(b), nil
}

func (c *realReplayComposition) replayPath() string {
	return filepath.Join(c.cfg.Root, "replay", sha256Text(c.cfg.MatchID)+".dem")
}
func (c *realReplayComposition) parseIndexPath() string {
	return filepath.Join(c.cfg.Root, "parse", sha256Text(c.cfg.MatchID)+".index.json")
}
func (c *realReplayComposition) parsePayloadPath(id string) string {
	return filepath.Join(c.cfg.Root, "parse", sha256Text(c.cfg.MatchID+":"+id)+".json")
}
func (c *realReplayComposition) factsPath() string {
	return filepath.Join(c.cfg.Root, "normalize", sha256Text(c.cfg.MatchID)+".json")
}
func (c *realReplayComposition) proofPath() string {
	return filepath.Join(c.cfg.Root, "normalize", sha256Text(c.cfg.MatchID)+".proof.json")
}
func (c *realReplayComposition) executionRoot() string {
	return filepath.Join(c.cfg.Root, "executions")
}
func (c *realReplayComposition) aggregatePath() string {
	return filepath.Join(c.cfg.Root, "aggregate", sha256Text(c.cfg.MatchID)+".json")
}

func copySource2Demo(src, dst, expectedSHA string) error {
	id, err := replay.InspectSource2DemoFile(src)
	if err != nil {
		return err
	}
	if id.SHA256 != expectedSHA {
		return errors.New("source replay does not match manifest")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := atomicfile.NewTemp(dst, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return err
	}
	return atomicfile.Commit(out, dst, func(path string) error {
		got, err := replay.InspectSource2DemoFile(path)
		if err != nil {
			return err
		}
		if got.SHA256 != expectedSHA {
			return errors.New("copied replay identity mismatch")
		}
		return nil
	})
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func shaBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func trimNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		return b[:len(b)-1]
	}
	return b
}

func sortedStageKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
