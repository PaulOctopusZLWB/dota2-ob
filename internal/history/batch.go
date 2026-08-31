package history

import (
	"errors"
	"sort"
)

// StageState is the terminal state of one match across the M1 stage pipeline.
// Every entry in the discovery manifest advances through discovery,
// acquisition, verification, parse, normalize, aggregate and ends with one
// terminal, explainable state.
type StageState string

const (
	StageQueued         StageState = "queued"
	StageRunning        StageState = "running"
	StageSucceeded      StageState = "succeeded"
	StageFailedTerminal StageState = "failed_terminal"
)

// StageEntry is the resumable per-match state. It records the furthest stage
// reached, terminal reason, attempt count, and content identities so resume
// does not duplicate facts or aggregates.
type StageEntry struct {
	MatchID             string            `json:"match_id"`
	Status              StageState        `json:"status"`
	ReachedStage        string            `json:"reached_stage"`
	InFlightStage       string            `json:"in_flight_stage,omitempty"`
	Attempts            int               `json:"attempts"`
	ReplaySHA256        string            `json:"replay_sha256,omitempty"`
	FactsSHA256         string            `json:"facts_sha256,omitempty"`
	ArtifactSHA256      map[string]string `json:"artifact_sha256,omitempty"`
	ArtifactInputSHA256 map[string]string `json:"artifact_input_sha256,omitempty"`
	LastError           string            `json:"last_error,omitempty"`
	TerminalReason      string            `json:"terminal_reason,omitempty"`
}

// StageBatch is the complete resumable state for one discovery manifest run.
// It binds to the schema version AND the discovery manifest's content identity
// so a checkpoint written for one manifest can never be silently applied to a
// different manifest (which could skip or replay matching ids).
type StageBatch struct {
	SchemaVersion string                `json:"schema_version"`
	ManifestID    string                `json:"manifest_id"`
	Entries       map[string]StageEntry `json:"entries"`
}

// NewStageBatch builds a fresh, identity-bound batch for a discovery manifest.
func NewStageBatch(manifest DiscoveryManifestV1) StageBatch {
	return StageBatch{
		SchemaVersion: StageSchema,
		ManifestID:    manifest.ContentSHA256,
		Entries:       map[string]StageEntry{},
	}
}

// ErrRetryablePending is returned by Run when retryable work remains queued
// (an entry failed non-terminally below MaxRetries). A caller must NOT read
// this as a successful run: work remains and the batch must be resumed.
type ErrRetryablePending struct{ IDs []string }

func (e *ErrRetryablePending) Error() string {
	return "history: retryable work pending for " + lenStr(len(e.IDs)) + " entry(ies): " + joinIDs(e.IDs)
}
func (e *ErrRetryablePending) Unwrap() error { return nil }

// ErrBatchManifestMismatch is returned when a prior batch does not bind to the
// discovery manifest being run (wrong schema or manifest id).
var ErrBatchManifestMismatch = errors.New("history: batch checkpoint does not bind to this discovery manifest")

// StageFunc is the seam for one stage. It receives the current entry and the
// adapter-provided match context, and returns an updated entry plus a terminal
// reason on failure. Stages are pure with respect to state: the batch driver
// owns persistence (injected), stages own their stage-specific work.
type StageFunc func(entry StageEntry, ctx MatchContext) (StageEntry, *StageFailure)

type StageReconcileFunc func(entry StageEntry, ctx MatchContext) (StageEntry, bool, *StageFailure)

// MatchContext is the read-only per-match context supplied by the adapter
// (discovery manifest entry, local replay path, parsed facts, etc.).
type MatchContext struct {
	Discovery       DiscoveryMatch
	ManifestID      string
	EntrySHA256     string
	ReplayPath      string
	NormalizedFacts *NormalizedMatchFacts
}

// DiscoveryEntrySHA256 binds acquisition to both the sealed discovery
// manifest and the exact entry being acquired. It is an input identity, not an
// authenticity claim about public replay transport.
func DiscoveryEntrySHA256(manifest DiscoveryManifestV1, match DiscoveryMatch) (string, error) {
	return contentSHA256("history.discovery-entry.v1", struct {
		ManifestID string         `json:"manifest_id"`
		Match      DiscoveryMatch `json:"match"`
	}{ManifestID: manifest.ContentSHA256, Match: match})
}

// StageFailure is a typed stage failure. Terminal marks a dead-letter entry
// that will not be retried until an explicit retry-all.
type StageFailure struct {
	Stage    string
	Reason   string
	Terminal bool
}

// StagePipeline runs the ordered stages over a discovery manifest idempotently.
// On resume, succeeded entries are skipped, terminal entries are skipped
// before any work (no re-attempt, no re-increment), and failed entries retry
// until MaxRetries is exhausted. State is checkpointed after every transition
// through the injected SaveFunc; a checkpoint failure is propagated, not
// ignored, because resume safety is lost if on-disk state diverges.
//
// The pipeline is pure domain logic: no filesystem or network access happens
// here. Adapters supply StageFunc implementations and the persistence seam.
type StagePipeline struct {
	Stages            map[string]StageFunc
	Reconcile         map[string]StageReconcileFunc
	ValidateCompleted func(StageEntry, MatchContext) error
	StageOrder        []string
	MaxRetries        int
	Save              func(StageBatch) error
}

// Run processes the discovery manifest matches in sorted match-id order.
func (p *StagePipeline) Run(manifest DiscoveryManifestV1, prior StageBatch) (StageBatch, error) {
	if err := manifest.Validate(); err != nil {
		return prior, err
	}
	// Bind the batch to this manifest. A fresh (empty, unbound) batch is
	// initialized; a non-empty batch must already bind to this manifest or the
	// run fails closed rather than replaying/skipping work for the wrong run.
	if prior.SchemaVersion == "" && len(prior.Entries) == 0 {
		prior = NewStageBatch(manifest)
	} else if prior.SchemaVersion != StageSchema || prior.ManifestID != manifest.ContentSHA256 || prior.Entries == nil {
		return prior, ErrBatchManifestMismatch
	}
	if err := p.validateCheckpoint(manifest, prior); err != nil {
		return prior, err
	}
	if p.MaxRetries < 1 {
		p.MaxRetries = 1
	}
	checkpoint := func(b StageBatch) error {
		if p.Save == nil {
			return nil
		}
		return p.Save(b)
	}
	stageIndex := func(name string) int {
		for i, s := range p.StageOrder {
			if s == name {
				return i
			}
		}
		return -1
	}
	var terminal []string
	var retryable []string
	for _, m := range manifest.Matches {
		entrySHA, err := DiscoveryEntrySHA256(manifest, m)
		if err != nil {
			return prior, err
		}
		ctx := MatchContext{Discovery: m, ManifestID: manifest.ContentSHA256, EntrySHA256: entrySHA}
		entry, exists := prior.Entries[m.MatchID]
		if !exists {
			entry = StageEntry{MatchID: m.MatchID, Status: StageQueued, ReachedStage: StageDiscovery}
			prior.Entries[m.MatchID] = entry
		}
		// Every durable stage already reached must be externally validated
		// before its effect is skipped or any downstream effect is allowed to
		// run. Structural checkpoint linkage alone is not proof that the
		// referenced replay/facts/product artifacts exist or have those bytes.
		if entry.ReachedStage != StageDiscovery {
			if p.ValidateCompleted == nil {
				return prior, errors.New("batch: reached entry has no durable artifact validator")
			}
			if err := p.ValidateCompleted(entry, ctx); err != nil {
				return prior, err
			}
		}
		if entry.Status == StageSucceeded {
			continue
		}
		if entry.Status == StageFailedTerminal {
			terminal = append(terminal, m.MatchID)
			continue
		}
		// Resume from the furthest stage reached: skip stages already
		// completed so a crash mid-pipeline never replays side effects.
		startAt := 0
		if idx := stageIndex(entry.ReachedStage); idx >= 0 {
			startAt = idx + 1
		}
		entry.Status = StageRunning
		prior.Entries[m.MatchID] = entry
		if err := checkpoint(prior); err != nil {
			return prior, err
		}

		failed := false
		for i := startAt; i < len(p.StageOrder); i++ {
			stageName := p.StageOrder[i]
			stageFn, ok := p.Stages[stageName]
			if !ok {
				return prior, errors.New("batch: missing stage " + stageName)
			}
			if entry.InFlightStage != "" && entry.InFlightStage != stageName {
				return prior, errors.New("batch: in-flight stage does not match cursor")
			}
			if entry.InFlightStage == stageName {
				reconcile, ok := p.Reconcile[stageName]
				if !ok {
					return prior, errors.New("batch: missing reconciler for in-flight stage " + stageName)
				}
				var done bool
				var rf *StageFailure
				entry, done, rf = reconcile(entry, ctx)
				if rf != nil {
					entry.Attempts++
					entry.LastError = rf.Reason
					if rf.Terminal || entry.Attempts >= p.MaxRetries {
						entry.Status = StageFailedTerminal
						entry.TerminalReason = rf.Stage + ":" + rf.Reason
					} else {
						entry.Status = StageQueued
					}
					prior.Entries[m.MatchID] = entry
					if err := checkpoint(prior); err != nil {
						return prior, err
					}
					failed = true
					break
				}
				if done {
					entry.ReachedStage = stageName
					entry.InFlightStage = ""
					prior.Entries[m.MatchID] = entry
					if err := checkpoint(prior); err != nil {
						return prior, err
					}
					continue
				}
			}
			entry.InFlightStage = stageName
			prior.Entries[m.MatchID] = entry
			if err := checkpoint(prior); err != nil {
				return prior, err
			}
			newEntry, sf := stageFn(entry, ctx)
			entry = newEntry
			if sf == nil {
				entry.ReachedStage = stageName
				entry.InFlightStage = ""
				prior.Entries[m.MatchID] = entry
				if err := checkpoint(prior); err != nil {
					return prior, err
				}
				continue
			}
			entry.Attempts++
			entry.LastError = sf.Reason
			entry.TerminalReason = ""
			if sf.Terminal || entry.Attempts >= p.MaxRetries {
				entry.Status = StageFailedTerminal
				entry.TerminalReason = sf.Stage + ":" + sf.Reason
			} else {
				entry.Status = StageQueued
			}
			prior.Entries[m.MatchID] = entry
			if err := checkpoint(prior); err != nil {
				return prior, err
			}
			failed = true
			break
		}
		if !failed {
			entry.Status = StageSucceeded
			prior.Entries[m.MatchID] = entry
			if err := p.validateCheckpoint(manifest, prior); err != nil {
				return prior, err
			}
			if p.ValidateCompleted == nil {
				return prior, errors.New("batch: succeeded entry has no durable artifact validator")
			}
			if err := p.ValidateCompleted(entry, ctx); err != nil {
				return prior, err
			}
			if err := checkpoint(prior); err != nil {
				return prior, err
			}
		} else if entry.Status == StageFailedTerminal {
			terminal = append(terminal, m.MatchID)
		} else {
			// Non-terminal failure: work remains queued. This must not be read
			// as a successful run.
			retryable = append(retryable, m.MatchID)
		}
	}
	// Terminal failures take precedence: a partially failed run is never
	// reported as success.
	if len(terminal) > 0 {
		sort.Strings(terminal)
		return prior, &TerminalFailures{IDs: terminal}
	}
	if len(retryable) > 0 {
		sort.Strings(retryable)
		return prior, &ErrRetryablePending{IDs: retryable}
	}
	return prior, nil
}

func (p *StagePipeline) validateCheckpoint(manifest DiscoveryManifestV1, b StageBatch) error {
	wantOrder := []string{StageAcquisition, StageVerification, StageParse, StageNormalize, StageAggregate}
	if len(p.StageOrder) != len(wantOrder) {
		return errors.New("batch: incomplete stage order")
	}
	order := map[string]int{StageDiscovery: -1}
	seen := map[string]bool{StageDiscovery: true}
	for i, stage := range p.StageOrder {
		if stage != wantOrder[i] || seen[stage] {
			return errors.New("batch: invalid stage order")
		}
		seen[stage] = true
		order[stage] = i
	}
	manifestEntries := map[string]DiscoveryMatch{}
	for _, m := range manifest.Matches {
		manifestEntries[m.MatchID] = m
	}
	for key, e := range b.Entries {
		m, exists := manifestEntries[key]
		if !exists || e.MatchID != key {
			return errors.New("batch: checkpoint entry identity mismatch")
		}
		reached, ok := order[e.ReachedStage]
		if !ok {
			return errors.New("batch: invalid reached stage")
		}
		switch e.Status {
		case StageQueued, StageRunning, StageSucceeded, StageFailedTerminal:
		default:
			return errors.New("batch: invalid entry status")
		}
		if e.InFlightStage != "" {
			flight, ok := order[e.InFlightStage]
			if !ok || flight != reached+1 || e.Status == StageSucceeded {
				return errors.New("batch: contradictory in-flight stage")
			}
		}
		for i := 0; i <= reached; i++ {
			stage := p.StageOrder[i]
			if e.ArtifactSHA256 == nil || e.ArtifactInputSHA256 == nil || !isSHA(e.ArtifactSHA256[stage]) || !isSHA(e.ArtifactInputSHA256[stage]) {
				return errors.New("batch: incomplete stage identity chain")
			}
			if i > 0 && e.ArtifactInputSHA256[stage] != e.ArtifactSHA256[p.StageOrder[i-1]] {
				return errors.New("batch: contradictory stage identity chain")
			}
		}
		entrySHA, err := DiscoveryEntrySHA256(manifest, m)
		if err != nil {
			return err
		}
		if reached >= 0 && (e.ArtifactInputSHA256[StageAcquisition] != entrySHA || e.ArtifactSHA256[StageAcquisition] != m.ReplaySHA256 || e.ReplaySHA256 != m.ReplaySHA256) {
			return errors.New("batch: acquisition identity is not bound to discovery manifest replay")
		}
		verify := order[StageVerification]
		if reached >= verify && (e.ArtifactInputSHA256[StageVerification] != m.ReplaySHA256 || e.ArtifactSHA256[StageVerification] != m.ReplaySHA256) {
			return errors.New("batch: verification identity is not bound to manifest replay")
		}
		parse := order[StageParse]
		if reached >= parse && e.ArtifactInputSHA256[StageParse] != m.ReplaySHA256 {
			return errors.New("batch: parse input is not the verified replay")
		}
		normalize := order[StageNormalize]
		if reached >= normalize && e.ArtifactSHA256[StageNormalize] != e.FactsSHA256 {
			return errors.New("batch: normalize output is not normalized facts")
		}
		aggregate := order[StageAggregate]
		if reached >= aggregate && e.ArtifactInputSHA256[StageAggregate] != e.FactsSHA256 {
			return errors.New("batch: aggregate input is not normalized facts")
		}
		if reached >= 0 && !isSHA(e.ReplaySHA256) {
			return errors.New("batch: missing acquisition replay identity")
		}
		if reached >= normalize && !isSHA(e.FactsSHA256) {
			return errors.New("batch: missing normalized facts identity")
		}
		if e.Status == StageSucceeded && (reached != len(p.StageOrder)-1 || e.InFlightStage != "") {
			return errors.New("batch: inconsistent succeeded cursor")
		}
		if e.Status == StageFailedTerminal && e.TerminalReason == "" {
			return errors.New("batch: terminal entry missing reason")
		}
	}
	return nil
}

// ValidateStageBatch validates the complete canonical M1 checkpoint chain
// without executing or skipping work. Readiness uses this to consume durable
// stage state rather than caller-provided counters or labels.
func ValidateStageBatch(manifest DiscoveryManifestV1, batch StageBatch) error {
	p := StagePipeline{StageOrder: []string{StageAcquisition, StageVerification, StageParse, StageNormalize, StageAggregate}}
	if batch.SchemaVersion != StageSchema || batch.ManifestID != manifest.ContentSHA256 || batch.Entries == nil {
		return ErrBatchManifestMismatch
	}
	return p.validateCheckpoint(manifest, batch)
}

// TerminalFailures signals the final state still holds terminal entries so an
// operator or CI cannot read a partially failed run as success.
type TerminalFailures struct{ IDs []string }

func (e *TerminalFailures) Error() string {
	return "history: terminal failures for " + lenStr(len(e.IDs)) + " entry(ies): " + joinIDs(e.IDs)
}
func (e *TerminalFailures) Unwrap() error { return nil }

func lenStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func joinIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += id
	}
	return out
}
