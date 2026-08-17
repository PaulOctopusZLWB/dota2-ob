// Package store persists the per-match content-addressed artifact tree, a
// rebuildable catalog, and atomic resume state. Artifacts are keyed by input
// replay hash plus parser/schema/rule versions; an interrupted run resumes
// completed matches and never duplicates facts. All partial writes use
// temporary names and atomic promotion.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Artifact file names in a match directory.
const (
	ArtifactInput        = "input.json"
	ArtifactVerification = "verification.json"
	ArtifactRaw          = "raw.jsonl"
	ArtifactRawMeta      = "raw-meta.json"
	ArtifactIdentity     = "identity.json"
	ArtifactClock        = "clock.json"
	ArtifactFacts        = "facts.jsonl"
	ArtifactFactsSummary = "facts-summary.json"
	ArtifactEpisodes     = "episodes.json"
	ArtifactPhases       = "phases.json"
	ArtifactMetrics      = "metrics.json"
	ArtifactReport       = "report.json"
	ArtifactCanonical    = "canonical.json"
)

// Store is a replay data root.
type Store struct {
	Root string
}

// New creates a store rooted at root, creating the layout if needed.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("store: empty data root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("store: resolve root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "matches"), 0o755); err != nil {
		return nil, fmt.Errorf("store: create layout: %w", err)
	}
	return &Store{Root: abs}, nil
}

// MatchDir returns the artifact directory for a match id.
func (s *Store) MatchDir(matchID string) string {
	return filepath.Join(s.Root, "matches", matchID)
}

// ArtifactPath returns the path of one artifact for a match.
func (s *Store) ArtifactPath(matchID, artifact string) string {
	return filepath.Join(s.MatchDir(matchID), artifact)
}

// InputFingerprint is the deterministic identity of the frozen manifest entry
// plus pipeline versions. It is part of the canonical resume key.
type InputFingerprint struct {
	SchemaVersion  string `json:"schema_version"`
	MatchID        string `json:"match_id"`
	ArchiveSHA256  string `json:"archive_sha256"`
	DemoSHA256     string `json:"demo_sha256"`
	ParserName     string `json:"parser_name"`
	ParserVersion  string `json:"parser_version"`
	AdapterVersion string `json:"adapter_version"`
}

// Canonical is the canonical artifact-tree record.
type Canonical struct {
	SchemaVersion string            `json:"schema_version"`
	MatchID       string            `json:"match_id"`
	Fingerprint   InputFingerprint  `json:"input_fingerprint"`
	Files         map[string]string `json:"files"` // artifact name -> sha256
	TreeSHA256    string            `json:"tree_sha256"`
	GeneratedAt   string            `json:"generated_at"`
}

// Terminal statuses of a match (mirrors archive states at match granularity).
const (
	StatusVerified    = "verified"
	StatusCorrupt     = "corrupt"
	StatusMissing     = "missing"
	StatusParseFailed = "parse_failed"
	StatusQuarantined = "quarantined"

	// StatusSchema is the status record schema version.
	StatusSchema = "replay.status.v1"
)

// StatusRecord is the persisted per-match terminal status.
type StatusRecord struct {
	SchemaVersion string   `json:"schema_version"`
	MatchID       string   `json:"match_id"`
	Status        string   `json:"status"`
	ArchiveState  string   `json:"archive_state"`
	IdentityState string   `json:"identity_state,omitempty"`
	ClockState    string   `json:"clock_state,omitempty"`
	ParseState    string   `json:"parse_state,omitempty"`
	Publication   string   `json:"publication_state"`
	Reason        string   `json:"reason"`
	Mismatches    []string `json:"mismatches,omitempty"`
	UpdatedAt     string   `json:"updated_at"`
}

// WriteAtomic writes data to path via a temporary file and atomic rename.
// The file is fsynced before promotion so an interrupted run never observes a
// partial artifact as canonical.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.part")
	if err != nil {
		return fmt.Errorf("store: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: promote %s: %w", path, err)
	}
	return nil
}

// WriteJSON writes a value as deterministic JSON (marshal is stable for the
// repo's struct types) to an artifact path atomically.
func (s *Store) WriteJSON(matchID, artifact string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", artifact, err)
	}
	return WriteAtomic(s.ArtifactPath(matchID, artifact), b)
}

// ReadJSON reads an artifact into v. os.ErrNotExist is returned when absent.
func (s *Store) ReadJSON(matchID, artifact string, v interface{}) error {
	b, err := os.ReadFile(s.ArtifactPath(matchID, artifact))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("store: decode %s: %w", artifact, err)
	}
	return nil
}

// ReadJSONFile reads a store-level JSON file (not under a match dir).
func (s *Store) ReadJSONFile(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("store: decode %s: %w", path, err)
	}
	return nil
}

// OpenArtifact opens a match artifact for streaming reads.
func (s *Store) OpenArtifact(matchID, artifact string) (*os.File, error) {
	return os.Open(s.ArtifactPath(matchID, artifact))
}

// Fingerprint builds the input fingerprint for a manifest entry.
func Fingerprint(archiveSHA, demoSHA, matchID string) InputFingerprint {
	return InputFingerprint{
		SchemaVersion:  version.IdentitySchema,
		MatchID:        matchID,
		ArchiveSHA256:  archiveSHA,
		DemoSHA256:     demoSHA,
		ParserName:     version.ParserName,
		ParserVersion:  version.ParserVersion,
		AdapterVersion: version.AdapterVersion,
	}
}

// CanonicalExists reports whether a canonical record matching the fingerprint
// is present for the match (i.e. the stage is already complete and resumable).
func (s *Store) CanonicalExists(matchID string, fp InputFingerprint) (bool, error) {
	var c Canonical
	err := s.ReadJSON(matchID, ArtifactCanonical, &c)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if c.SchemaVersion != version.ReportSchema {
		return false, nil
	}
	return c.Fingerprint == fp, nil
}

// WriteCanonical computes the artifact-tree hash over the persisted artifact
// files and writes the canonical record.
func (s *Store) WriteCanonical(matchID string, fp InputFingerprint, artifacts []string) (*Canonical, error) {
	dir := s.MatchDir(matchID)
	entries := map[string]string{}
	h := sha256.New()
	names := append([]string(nil), artifacts...)
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("store: canonical read %s: %w", name, err)
		}
		fh := sha256.Sum256(b)
		entries[name] = hex.EncodeToString(fh[:])
		fmt.Fprintf(h, "%s\x00%s\x00", name, entries[name])
	}
	tree := hex.EncodeToString(h.Sum(nil))
	c := &Canonical{
		SchemaVersion: version.ReportSchema,
		MatchID:       matchID,
		Fingerprint:   fp,
		Files:         entries,
		TreeSHA256:    tree,
	}
	if err := s.WriteJSON(matchID, ArtifactCanonical, c); err != nil {
		return nil, err
	}
	return c, nil
}

// CatalogRow is one match row in the rebuildable catalog.
type CatalogRow struct {
	MatchID       string   `json:"match_id"`
	Category      string   `json:"category"`
	Status        string   `json:"status"`
	ArchiveState  string   `json:"archive_state"`
	IdentityState string   `json:"identity_state"`
	ClockState    string   `json:"clock_state"`
	ParseState    string   `json:"parse_state"`
	Publication   string   `json:"publication_state"`
	TreeSHA256    string   `json:"tree_sha256,omitempty"`
	DurationSec   *float64 `json:"duration_seconds,omitempty"`
	GameStartUnix *int64   `json:"game_start_unix,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

// Catalog is the rebuildable per-match index.
type Catalog struct {
	SchemaVersion string       `json:"schema_version"`
	BuiltAt       string       `json:"built_at"`
	Matches       []CatalogRow `json:"matches"`
}

// CatalogPath returns the catalog file path.
func (s *Store) CatalogPath() string {
	return filepath.Join(s.Root, "catalog.json")
}

// RebuildCatalog scans the data root and writes the catalog from the
// persisted artifacts. It is deterministic and never touches match artifacts.
func (s *Store) RebuildCatalog(now string) (*Catalog, error) {
	matchesDir := filepath.Join(s.Root, "matches")
	entries, err := os.ReadDir(matchesDir)
	if err != nil {
		if os.IsNotExist(err) {
			cat := &Catalog{SchemaVersion: version.ReportSchema, BuiltAt: now, Matches: []CatalogRow{}}
			return cat, nil
		}
		return nil, fmt.Errorf("store: list matches: %w", err)
	}
	cat := &Catalog{SchemaVersion: version.ReportSchema, BuiltAt: now, Matches: []CatalogRow{}}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		matchID := e.Name()
		row := s.catalogRow(matchID)
		cat.Matches = append(cat.Matches, row)
	}
	sort.Slice(cat.Matches, func(i, j int) bool { return cat.Matches[i].MatchID < cat.Matches[j].MatchID })
	if err := WriteAtomic(s.CatalogPath(), mustJSON(cat)); err != nil {
		return nil, err
	}
	return cat, nil
}

func (s *Store) catalogRow(matchID string) CatalogRow {
	row := CatalogRow{MatchID: matchID}
	var ver struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := s.ReadJSON(matchID, ArtifactVerification, &ver); err == nil {
		row.ArchiveState = ver.State
		row.Reason = ver.Reason
	}
	var idn struct {
		State      string   `json:"state"`
		Reason     string   `json:"reason"`
		Mismatches []string `json:"mismatches"`
	}
	if err := s.ReadJSON(matchID, ArtifactIdentity, &idn); err == nil {
		row.IdentityState = idn.State
		if len(idn.Mismatches) > 0 {
			row.Reason = strings.Join(idn.Mismatches, "; ")
		}
	}
	var clk struct {
		State            string   `json:"state"`
		GameDurationSeconds *float64 `json:"game_duration_seconds"`
		GameStartUnix    *int64   `json:"game_start_unix"`
		Reason           string   `json:"reason"`
	}
	if err := s.ReadJSON(matchID, ArtifactClock, &clk); err == nil {
		row.ClockState = clk.State
		row.DurationSec = clk.GameDurationSeconds
		row.GameStartUnix = clk.GameStartUnix
		if row.Reason == "" {
			row.Reason = clk.Reason
		}
	}
	var pd struct {
		Outcome string `json:"outcome"`
	}
	var rawMeta struct {
		Outcome string `json:"outcome"`
	}
	if err := s.ReadJSON(matchID, ArtifactRawMeta, &rawMeta); err == nil {
		pd.Outcome = rawMeta.Outcome
	}
	if err := s.ReadJSON(matchID, ArtifactRawMeta, &pd); err == nil {
		row.ParseState = pd.Outcome
	}
	var can Canonical
	if err := s.ReadJSON(matchID, ArtifactCanonical, &can); err == nil {
		row.TreeSHA256 = can.TreeSHA256
	}
	row.Status, row.Publication = s.deriveStatus(row)
	return row
}

// deriveStatus computes the terminal status and publication gate from the
// persisted states. Fail-closed: any gate miss quarantines the match.
func (s *Store) deriveStatus(row CatalogRow) (status, publication string) {
	if row.ArchiveState == "missing" {
		return StatusMissing, "suppressed"
	}
	if row.ArchiveState == "corrupt" {
		return StatusCorrupt, "suppressed"
	}
	if row.ParseState == "parse_error" {
		return StatusParseFailed, "suppressed"
	}
	if row.ArchiveState != "verified" {
		return StatusQuarantined, "suppressed"
	}
	if row.IdentityState != "verified" {
		return StatusQuarantined, "suppressed"
	}
	if row.ClockState != "calibrated" {
		return StatusQuarantined, "suppressed"
	}
	if row.TreeSHA256 == "" {
		return StatusQuarantined, "suppressed"
	}
	return StatusVerified, "published"
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}