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
	StatusQueued          StageStatus = "queued"
	StatusRunning         StageStatus = "running"
	StatusSucceeded       StageStatus = "succeeded"
	StatusFailedTerminal  StageStatus = "failed_terminal"
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

// SaveState writes state atomically next to the state path.
func (r *Runner) SaveState(s *BatchState) error {
	if r.StatePath == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.StatePath)
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
// content are skipped. Duplicate inputs (identical content SHA) reuse one facts
// artifact. Parse failures advance attempts and become terminal after
// MaxRetries. State is persisted after every entry so an interruption resumes
// without duplicate facts or duplicate work.
func (r *Runner) Run(m *Manifest) (*BatchState, error) {
	state, err := LoadState(r.StatePath)
	if err != nil {
		return nil, err
	}
	parse := r.parseFn()
	hash := r.hashFile()

	contentToFacts := map[string]string{}
	if !r.RetryAll {
		for _, e := range state.Entries {
			if e.Status == StatusSucceeded && e.ContentSHA256 != "" {
				contentToFacts[e.ContentSHA256] = e.FactsHash
			}
		}
	}

	for _, entry := range m.Entries {
		id := entry.MatchID
		if id == "" {
			id = entry.DemPath
		}
		prev := state.Entries[id]

		if !r.RetryAll && prev.Status == StatusSucceeded && prev.ContentSHA256 != "" {
			if _, err := hash(entry.DemPath); err != nil {
				state.Entries[id] = r.fail(prev, fmt.Sprintf("verify: %v", err))
				_ = r.SaveState(state)
				continue
			}
			continue
		}
		if !r.RetryAll && prev.Status == StatusFailedTerminal {
			continue
		}

		contentSHA, err := hash(entry.DemPath)
		if err != nil {
			state.Entries[id] = r.fail(prev, fmt.Sprintf("verify: %v", err))
			_ = r.SaveState(state)
			continue
		}

		if fh, ok := contentToFacts[contentSHA]; ok {
			state.Entries[id] = EntryState{
				Status: StatusSucceeded, ContentSHA256: contentSHA,
				FactsHash: fh, Attempts: prev.Attempts,
				UpdatedAt: r.now().Format(time.RFC3339Nano),
			}
			_ = r.SaveState(state)
			continue
		}

		state.Entries[id] = EntryState{
			Status: StatusRunning, ContentSHA256: contentSHA,
			Attempts: prev.Attempts, UpdatedAt: r.now().Format(time.RFC3339Nano),
		}
		_ = r.SaveState(state)

		pr, perr := parse(entry.DemPath)
		if perr != nil {
			msg := "parse error"
			if pr != nil && pr.Metrics.ParseError != "" {
				msg = pr.Metrics.ParseError
			} else {
				msg = perr.Error()
			}
			attempts := prev.Attempts + 1
			terminal := attempts >= r.maxRetries()
			st := EntryState{
				Status: StatusRunning, ContentSHA256: contentSHA,
				Attempts: attempts, LastError: msg,
				UpdatedAt: r.now().Format(time.RFC3339Nano),
			}
			if terminal {
				st.Status = StatusFailedTerminal
			} else {
				st.Status = StatusQueued
			}
			state.Entries[id] = st
			_ = r.SaveState(state)
			if terminal {
				return state, fmt.Errorf("replay: %s: %s", id, msg)
			}
			continue
		}

		if err := r.persistFacts(contentSHA, pr.Facts); err != nil {
			state.Entries[id] = r.fail(prev, fmt.Sprintf("persist: %v", err))
			_ = r.SaveState(state)
			continue
		}
		contentToFacts[contentSHA] = pr.Hash
		state.Entries[id] = EntryState{
			Status: StatusSucceeded, ContentSHA256: contentSHA,
			FactsHash: pr.Hash, Attempts: prev.Attempts + 1,
			UpdatedAt: r.now().Format(time.RFC3339Nano),
		}
		_ = r.SaveState(state)
	}

	return state, nil
}

func (r *Runner) fail(prev EntryState, msg string) EntryState {
	attempts := prev.Attempts + 1
	terminal := attempts >= r.maxRetries()
	st := EntryState{
		Status: StatusRunning, ContentSHA256: prev.ContentSHA256,
		Attempts: attempts, LastError: msg,
		UpdatedAt: r.now().Format(time.RFC3339Nano),
	}
	if terminal {
		st.Status = StatusFailedTerminal
	} else {
		st.Status = StatusQueued
	}
	return st
}

func (r *Runner) persistFacts(contentSHA string, facts *ReplayFactsV1) error {
	if r.FactsDir == "" {
		return nil
	}
	if err := os.MkdirAll(r.FactsDir, 0o755); err != nil {
		return err
	}
	b, err := facts.CanonicalJSON()
	if err != nil {
		return err
	}
	path := filepath.Join(r.FactsDir, contentSHA[:16]+".facts.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}