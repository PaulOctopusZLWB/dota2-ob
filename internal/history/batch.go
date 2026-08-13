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
	StageQueued        StageState = "queued"
	StageRunning       StageState = "running"
	StageSucceeded     StageState = "succeeded"
	StageFailedTerminal StageState = "failed_terminal"
)

// StageEntry is the resumable per-match state. It records the furthest stage
// reached, terminal reason, attempt count, and content identities so resume
// does not duplicate facts or aggregates.
type StageEntry struct {
	MatchID        string     `json:"match_id"`
	Status         StageState `json:"status"`
	ReachedStage   string     `json:"reached_stage"`
	Attempts       int        `json:"attempts"`
	ReplaySHA256   string     `json:"replay_sha256,omitempty"`
	FactsSHA256    string     `json:"facts_sha256,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	TerminalReason string     `json:"terminal_reason,omitempty"`
}

// StageBatch is the complete resumable state for a discovery manifest run.
type StageBatch struct {
	Entries map[string]StageEntry `json:"entries"`
}

// StageFunc is the seam for one stage. It receives the current entry and the
// adapter-provided match context, and returns an updated entry plus a terminal
// reason on failure. Stages are pure with respect to state: the batch driver
// owns persistence (injected), stages own their stage-specific work.
type StageFunc func(entry StageEntry, ctx MatchContext) (StageEntry, *StageFailure)

// MatchContext is the read-only per-match context supplied by the adapter
// (discovery manifest entry, local replay path, parsed facts, etc.).
type MatchContext struct {
	Discovery     DiscoveryMatch
	ReplayPath    string
	NormalizedFacts *NormalizedMatchFacts
}

// StageFailure is a typed stage failure. Terminal marks a dead-letter entry
// that will not be retried until an explicit retry-all.
type StageFailure struct {
	Stage   string
	Reason  string
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
	Stages     map[string]StageFunc
	StageOrder []string
	MaxRetries  int
	Save        func(StageBatch) error
}

// Run processes the discovery manifest matches in sorted match-id order.
func (p *StagePipeline) Run(manifest DiscoveryManifestV1, prior StageBatch) (StageBatch, error) {
	if err := manifest.Validate(); err != nil {
		return prior, err
	}
	if prior.Entries == nil {
		prior.Entries = map[string]StageEntry{}
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
	var terminal []string
	for _, m := range manifest.Matches {
		entry, exists := prior.Entries[m.MatchID]
		if !exists {
			entry = StageEntry{MatchID: m.MatchID, Status: StageQueued, ReachedStage: StageDiscovery}
		}
		if entry.Status == StageSucceeded {
			continue
		}
		if entry.Status == StageFailedTerminal {
			terminal = append(terminal, m.MatchID)
			continue
		}
		ctx := MatchContext{Discovery: m}
		entry.Status = StageRunning
		prior.Entries[m.MatchID] = entry
		if err := checkpoint(prior); err != nil {
			return prior, err
		}

		failed := false
		for _, stageName := range p.StageOrder {
			stageFn, ok := p.Stages[stageName]
			if !ok {
				return prior, errors.New("batch: missing stage " + stageName)
			}
			newEntry, sf := stageFn(entry, ctx)
			entry = newEntry
			prior.Entries[m.MatchID] = entry
			if err := checkpoint(prior); err != nil {
				return prior, err
			}
			if sf == nil {
				entry.ReachedStage = stageName
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
			if err := checkpoint(prior); err != nil {
				return prior, err
			}
		} else if entry.Status == StageFailedTerminal {
			terminal = append(terminal, m.MatchID)
		}
	}
	if len(terminal) > 0 {
		sort.Strings(terminal)
		return prior, &TerminalFailures{IDs: terminal}
	}
	return prior, nil
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