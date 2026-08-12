package replay

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
)

// StageStatus is the explicit batch state for one manifest entry.
type StageStatus string

const (
	StatusQueued         StageStatus = "queued"
	StatusRunning        StageStatus = "running"
	StatusSucceeded      StageStatus = "succeeded"
	StatusFailedTerminal StageStatus = "failed_terminal"
)

// Manifest is the reproducible batch input: a list of matches keyed by stable
// identity plus the local decompressed demo path for each.
type Manifest struct {
	Entries []ManifestEntry `json:"entries"`
}

type ManifestEntry struct {
	MatchID string `json:"match_id"`
	DemPath string `json:"dem_path"`
}

// Validate returns an error if the manifest is malformed: empty entries,
// empty identities/paths, or duplicate match_id values (which would collide
// in the state map and silently merge unrelated work).
func (m *Manifest) Validate() error {
	if m == nil || len(m.Entries) == 0 {
		return errors.New("replay: manifest is empty")
	}
	seen := map[string]bool{}
	for i, e := range m.Entries {
		if e.MatchID == "" {
			return fmt.Errorf("replay: manifest entry %d has empty match_id", i)
		}
		if e.DemPath == "" {
			return fmt.Errorf("replay: manifest entry %d has empty dem_path", i)
		}
		if seen[e.MatchID] {
			return fmt.Errorf("replay: manifest has duplicate match_id %q", e.MatchID)
		}
		seen[e.MatchID] = true
	}
	return nil
}

type EntryState struct {
	Status        StageStatus `json:"status"`
	ContentSHA256 string      `json:"content_sha256,omitempty"`
	FactsHash     string      `json:"facts_hash,omitempty"`
	Attempts      int         `json:"attempts"`
	LastError     string      `json:"last_error,omitempty"`
	UpdatedAt     string      `json:"updated_at"`
}

type BatchState struct {
	Entries map[string]EntryState `json:"entries"`
}

func (b *BatchState) SortedKeys() []string {
	keys := make([]string, 0, len(b.Entries))
	for k := range b.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseFunc is the parse seam. The default implementation is ParseFile; tests
// inject a fake so no replay or network is required.
type ParseFunc func(demPath string) (*ParseResult, error)

// HashFile is the content-addressing seam for the decompressed demo.
type HashFile func(path string) (string, error)

type Runner struct {
	StatePath  string
	FactsDir   string
	MaxRetries int
	Parse      ParseFunc
	HashFile   HashFile
	Now        func() time.Time
	RetryAll   bool
}

// DefaultHashFile hashes a file's full content with SHA-256.
func DefaultHashFile(path string) (string, error) {
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

// LoadState reads existing state or returns a fresh empty state.
func LoadState(path string) (*BatchState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &BatchState{Entries: map[string]EntryState{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s BatchState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("replay: decode state: %w", err)
	}
	if s.Entries == nil {
		s.Entries = map[string]EntryState{}
	}
	return &s, nil
}

// SaveState writes state atomically next to the state path, creating the
// parent directory. A non-nil error means the on-disk state diverged from the
// in-memory state and callers must not continue as if the checkpoint landed.
func (r *Runner) SaveState(s *BatchState) error {
	if r.StatePath == "" {
		return nil
	}
	if dir := filepath.Dir(r.StatePath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("replay: state dir: %w", err)
		}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("replay: write state tmp: %w", err)
	}
	if err := os.Rename(tmp, r.StatePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replay: rename state: %w", err)
	}
	return nil
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) hashFile() HashFile {
	if r.HashFile != nil {
		return r.HashFile
	}
	return DefaultHashFile
}

func (r *Runner) parseFn() ParseFunc {
	if r.Parse != nil {
		return r.Parse
	}
	return ParseFile
}

func (r *Runner) maxRetries() int {
	if r.MaxRetries > 0 {
		return r.MaxRetries
	}
	return 1
}

// Run processes a manifest idempotently. Succeeded entries with unchanged
// content are skipped; succeeded entries whose bytes changed are reprocessed
// (content identity is enforced, not assumed). Duplicate content (identical
// SHA) shares one facts artifact and one provenance sidecar. Entry failures
// advance attempts and become terminal after MaxRetries; the loop processes
// the whole bounded manifest before returning. A non-nil error is returned
// when any entry reached terminal failure (aggregate) or when a state
// checkpoint could not be persisted (immediate, since resume safety is lost).
// State is persisted after every state transition.
func (r *Runner) Run(m *Manifest) (*BatchState, []error) {
	if err := m.Validate(); err != nil {
		return nil, []error{err}
	}
	state, err := LoadState(r.StatePath)
	if err != nil {
		return nil, []error{err}
	}
	parse := r.parseFn()
	hash := r.hashFile()

	// Best-effort facts reuse for duplicate content seen earlier this run.
	contentToFacts := map[string]string{}
	if !r.RetryAll {
		for _, e := range state.Entries {
			if e.Status == StatusSucceeded && e.ContentSHA256 != "" {
				contentToFacts[e.ContentSHA256] = e.FactsHash
			}
		}
	}

	var errs []error

	checkpoint := func() bool {
		if cerr := r.SaveState(state); cerr != nil {
			errs = append(errs, fmt.Errorf("replay: checkpoint: %w", cerr))
			return false
		}
		return true
	}

	setFail := func(id string, prev EntryState, contentSHA, msg string, terminal bool) {
		attempts := prev.Attempts + 1
		st := EntryState{
			Status: StatusQueued, ContentSHA256: contentSHA,
			Attempts: attempts, LastError: msg,
			UpdatedAt: r.now().Format(time.RFC3339Nano),
		}
		if terminal {
			st.Status = StatusFailedTerminal
		}
		state.Entries[id] = st
	}

	for _, entry := range m.Entries {
		id := entry.MatchID
		prev := state.Entries[id]

		// A terminal entry stays terminal until an explicit retry-all; it is
		// skipped before any hashing so resume does not re-attempt or
		// re-increment it.
		if !r.RetryAll && prev.Status == StatusFailedTerminal {
			continue
		}

		// Content identity is verified by hashing now; the digest is never
		// discarded. A succeeded entry with changed bytes is reprocessed.
		contentSHA, herr := hash(entry.DemPath)
		if herr != nil {
			setFail(id, prev, prev.ContentSHA256, fmt.Sprintf("verify: %v", herr), prev.Attempts+1 >= r.maxRetries())
			if !checkpoint() {
				return state, errs
			}
			continue
		}

		if !r.RetryAll && prev.Status == StatusSucceeded && contentSHA == prev.ContentSHA256 {
			continue // unchanged: skip, content identity confirmed
		}

		// Reuse an already-persisted facts artifact for identical content.
		if fh, ok := contentToFacts[contentSHA]; ok {
			state.Entries[id] = EntryState{
				Status: StatusSucceeded, ContentSHA256: contentSHA,
				FactsHash: fh, Attempts: prev.Attempts,
				UpdatedAt: r.now().Format(time.RFC3339Nano),
			}
			if !checkpoint() {
				return state, errs
			}
			continue
		}

		state.Entries[id] = EntryState{
			Status: StatusRunning, ContentSHA256: contentSHA,
			Attempts: prev.Attempts, UpdatedAt: r.now().Format(time.RFC3339Nano),
		}
		if !checkpoint() {
			return state, errs
		}

		pr, perr := parse(entry.DemPath)
		if perr != nil {
			msg := "parse error"
			if pr != nil && pr.Metrics.ParseError != "" {
				msg = pr.Metrics.ParseError
			} else {
				msg = perr.Error()
			}
			setFail(id, prev, contentSHA, msg, prev.Attempts+1 >= r.maxRetries())
			if !checkpoint() {
				return state, errs
			}
			continue
		}

		if err := r.persistFactsAndSidecar(id, contentSHA, pr); err != nil {
			setFail(id, prev, contentSHA, fmt.Sprintf("persist: %v", err), prev.Attempts+1 >= r.maxRetries())
			if !checkpoint() {
				return state, errs
			}
			continue
		}
		contentToFacts[contentSHA] = pr.Hash
		state.Entries[id] = EntryState{
			Status: StatusSucceeded, ContentSHA256: contentSHA,
			FactsHash: pr.Hash, Attempts: prev.Attempts + 1,
			UpdatedAt: r.now().Format(time.RFC3339Nano),
		}
		if !checkpoint() {
			return state, errs
		}
	}

	// Aggregate terminal signaling is driven by the final state, not only by
	// failures newly produced this run, so a resume over a state that still
	// holds terminal entries does not read as success to an operator or CI.
	finalTerminal := []string{}
	if !r.RetryAll {
		for _, id := range state.SortedKeys() {
			if state.Entries[id].Status == StatusFailedTerminal {
				finalTerminal = append(finalTerminal, id)
			}
		}
	}
	if len(finalTerminal) > 0 {
		errs = append(errs, fmt.Errorf("replay: terminal failures for %d entry(ies): %s", len(finalTerminal), joinIDs(finalTerminal)))
	}
	return state, errs
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

// ProvenanceSidecar pins the full input digest, facts hash, and code
// versions for one persisted facts artifact. It is keyed by the full SHA-256
// of the decompressed demo and is deterministic except for MatchID (provided
// by the manifest) and GeneratedAt (wall clock for human inspection), which
// are not part of the facts content hash.
type ProvenanceSidecar struct {
	InputSHA256    string `json:"input_sha256"`
	FactsHash      string `json:"facts_hash"`
	MatchID        string `json:"match_id"`
	ParserName     string `json:"parser_name"`
	ParserVersion  string `json:"parser_version"`
	AdapterName    string `json:"adapter_name"`
	AdapterVersion string `json:"adapter_version"`
	SchemaVersion  string `json:"schema_version"`
	GeneratedAt    string `json:"generated_at"`
}

func (r *Runner) persistFactsAndSidecar(matchID, contentSHA string, pr *ParseResult) error {
	if r.FactsDir == "" {
		return nil
	}
	if err := os.MkdirAll(r.FactsDir, 0o755); err != nil {
		return err
	}
	b, err := pr.Facts.CanonicalJSON()
	if err != nil {
		return err
	}
	factsPath := filepath.Join(r.FactsDir, contentSHA+".facts.json")
	if err := atomicWrite(factsPath, append(b, '\n')); err != nil {
		return fmt.Errorf("facts: %w", err)
	}
	side := ProvenanceSidecar{
		InputSHA256:    contentSHA,
		FactsHash:      pr.Hash,
		MatchID:        matchID,
		ParserName:     ParserName,
		ParserVersion:  ParserVersion,
		AdapterName:    AdapterName,
		AdapterVersion: AdapterVersion,
		SchemaVersion:  FactsSchema,
		GeneratedAt:    r.now().Format(time.RFC3339Nano),
	}
	sb, err := json.MarshalIndent(side, "", "  ")
	if err != nil {
		return err
	}
	sidePath := filepath.Join(r.FactsDir, contentSHA+".provenance.json")
	if err := atomicWrite(sidePath, append(sb, '\n')); err != nil {
		return fmt.Errorf("sidecar: %w", err)
	}
	return nil
}

// atomicWrite writes data to path+".tmp", fsyncs, then renames over path. It
// removes the temp file on any failure path so partials never look canonical.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	n, err := f.Write(data)
	if err != nil {
		f.Close()
		cleanup()
		return err
	}
	if n != len(data) {
		f.Close()
		cleanup()
		return fmt.Errorf("short write %d of %d", n, len(data))
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	return nil
}