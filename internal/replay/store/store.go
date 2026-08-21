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
	"io"
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
	ArtifactScores       = "scores.json"
	ArtifactCorrections  = "corrections.json"
	ArtifactReport       = "report.json"
	ArtifactStatus       = "status.json"
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
// plus every output-affecting pipeline version (parser, adapter, schemas, and
// rule algorithms). It is the canonical resume key: a change in any covered
// version invalidates prior outputs so stale artifacts are never reused.
type InputFingerprint struct {
	SchemaVersion  string `json:"schema_version"`
	MatchID        string `json:"match_id"`
	ArchiveSHA256  string `json:"archive_sha256"`
	DemoSHA256     string `json:"demo_sha256"`
	ParserName     string `json:"parser_name"`
	ParserVersion  string `json:"parser_version"`
	AdapterVersion string `json:"adapter_version"`
	// Schema versions of every persisted artifact family.
	RawSchema      string `json:"raw_schema"`
	FactsSchema    string `json:"facts_schema"`
	ClockSchema    string `json:"clock_schema"`
	IdentitySchema string `json:"identity_schema"`
	EpisodeSchema  string `json:"episode_schema"`
	PhaseSchema    string `json:"phase_schema"`
	MetricsSchema  string `json:"metrics_schema"`
	ScoreSchema    string `json:"score_schema"`
	ReportSchema   string `json:"report_schema"`
	RoleSchema     string `json:"role_schema"`
	// Rule/algorithm versions of every derived artifact.
	PhaseRuleVersion   string `json:"phase_rule_version"`
	EpisodeRuleVersion string `json:"episode_rule_version"`
	LaneRuleVersion    string `json:"lane_rule_version"`
	MetricsRuleVersion string `json:"metrics_rule_version"`
	ScoreRuleVersion   string `json:"score_rule_version"`
	// Effective role-registry identity: registry content hash plus override
	// content hash when overrides exist (both affect published results).
	RoleRegistrySHA256  string `json:"role_registry_sha256"`
	RoleOverridesSHA256 string `json:"role_overrides_sha256,omitempty"`
	// Metric registry and scoring contract content hashes (affect published
	// metric values and score snapshots).
	MetricRegistrySHA256  string `json:"metric_registry_sha256,omitempty"`
	ScoringContractSHA256 string `json:"scoring_contract_sha256,omitempty"`
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

// StatusRecord is the persisted per-match terminal status. It is part of the
// canonical completion tree, so it must be byte-identical across unchanged
// reruns: it carries no wall-clock timestamp (audit timestamps live in the
// operator run logs, not in the content-addressed artifact).
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
	DurationSec   *float64 `json:"duration_seconds,omitempty"`
	GameStartUnix *int64   `json:"game_start_unix,omitempty"`
	// InputHash is the version-complete resume fingerprint hash; it lets a
	// durable terminal state (e.g. quarantined) be resumed only for the same
	// unchanged input.
	InputHash string `json:"input_hash,omitempty"`
}

// WriteStatus atomically persists the authoritative terminal state for a
// match. This record is the single source of truth for report/catalog/API
// status; it is written only after the publication gate is evaluated.
func (s *Store) WriteStatus(matchID string, sr *StatusRecord) error {
	return s.WriteJSON(matchID, ArtifactStatus, sr)
}

// ReadStatus loads the persisted authoritative terminal state for a match.
// os.ErrNotExist is returned when no status has been recorded yet.
func (s *Store) ReadStatus(matchID string) (*StatusRecord, error) {
	var sr StatusRecord
	if err := s.ReadJSON(matchID, ArtifactStatus, &sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// StatusExists reports whether an authoritative status record exists.
func (s *Store) StatusExists(matchID string) bool {
	_, err := s.ReadStatus(matchID)
	return err == nil
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
	if err := SyncParent(path); err != nil {
		return fmt.Errorf("store: sync promoted parent %s: %w", path, err)
	}
	return nil
}

// SyncParent durably records directory metadata after a rename or unlink.
// On Linux, fsync of the file alone does not make the directory entry crash-
// durable; callers must not acknowledge a promotion/removal before this
// succeeds.
func SyncParent(path string) error {
	dir := filepath.Dir(path)
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent %s: %w", dir, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync parent %s: %w", dir, err)
	}
	return nil
}

// RemoveDurable unlinks path and fsyncs its parent before returning success.
func RemoveDurable(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return SyncParent(path)
}

// StreamWriter writes a large artifact atomically: bytes go to a temporary
// file in the destination directory and are fsynced and renamed into place on
// Close. Abort removes the temp file without touching the destination, so an
// interrupted write never coexists with (or clobbers) a valid artifact.
type StreamWriter struct {
	path string
	tmp  *os.File
	done bool
}

// NewStreamWriter opens an atomic streaming writer for path.
func NewStreamWriter(path string) (*StreamWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.part")
	if err != nil {
		return nil, fmt.Errorf("store: temp file: %w", err)
	}
	return &StreamWriter{path: path, tmp: tmp}, nil
}

// Write implements io.Writer.
func (w *StreamWriter) Write(p []byte) (int, error) { return w.tmp.Write(p) }

// Abort discards the partial write and removes the temp file.
func (w *StreamWriter) Abort() {
	if w == nil || w.tmp == nil {
		return
	}
	name := w.tmp.Name()
	w.tmp.Close()
	os.Remove(name)
	w.done = true
}

// Close flushes, fsyncs, and atomically renames the temp file into place.
func (w *StreamWriter) Close() error {
	if w == nil || w.tmp == nil {
		return nil
	}
	name := w.tmp.Name()
	defer os.Remove(name)
	if err := w.tmp.Sync(); err != nil {
		w.tmp.Close()
		return fmt.Errorf("store: sync temp: %w", err)
	}
	if err := w.tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp: %w", err)
	}
	if err := os.Rename(name, w.path); err != nil {
		return fmt.Errorf("store: promote %s: %w", w.path, err)
	}
	if err := SyncParent(w.path); err != nil {
		return fmt.Errorf("store: sync promoted parent %s: %w", w.path, err)
	}
	w.done = true
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

// WriteRootJSON writes a store-level JSON file (not under a match dir)
// atomically. Used for rebuildable corpus-level catalogs.
func (s *Store) WriteRootJSON(name string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", name, err)
	}
	return WriteAtomic(filepath.Join(s.Root, name), b)
}

// OpenArtifact opens a match artifact for streaming reads.
func (s *Store) OpenArtifact(matchID, artifact string) (*os.File, error) {
	return os.Open(s.ArtifactPath(matchID, artifact))
}

// Fingerprint builds the version-complete input fingerprint for a manifest
// entry. roleRegistrySHA and roleOverridesSHA are content hashes of the
// effective role registry and override inputs (empty when absent).
// metricRegistrySHA and scoringContractSHA are content hashes of the frozen
// metric registry and scoring contract inputs.
func Fingerprint(archiveSHA, demoSHA, matchID, roleRegistrySHA, roleOverridesSHA string) InputFingerprint {
	return FingerprintWithContracts(archiveSHA, demoSHA, matchID, roleRegistrySHA, roleOverridesSHA, "", "")
}

// FingerprintWithContracts is Fingerprint plus the metric registry and scoring
// contract content hashes, which deterministically invalidate stale metric and
// score artifacts when the frozen contracts change.
func FingerprintWithContracts(archiveSHA, demoSHA, matchID, roleRegistrySHA, roleOverridesSHA, metricRegistrySHA, scoringContractSHA string) InputFingerprint {
	return InputFingerprint{
		SchemaVersion:         version.IdentitySchema,
		MatchID:               matchID,
		ArchiveSHA256:         archiveSHA,
		DemoSHA256:            demoSHA,
		ParserName:            version.ParserName,
		ParserVersion:         version.ParserVersion,
		AdapterVersion:        version.AdapterVersion,
		RawSchema:             version.RawSchema,
		FactsSchema:           version.FactsSchema,
		ClockSchema:           version.ClockSchema,
		IdentitySchema:        version.IdentitySchema,
		EpisodeSchema:         version.EpisodeSchema,
		PhaseSchema:           version.PhaseSchema,
		MetricsSchema:         version.MetricsSchema,
		ScoreSchema:           version.ScoreSchema,
		ReportSchema:          version.ReportSchema,
		RoleSchema:            version.RoleSchema,
		PhaseRuleVersion:      version.PhaseRuleVersion,
		EpisodeRuleVersion:    version.EpisodeRuleVersion,
		LaneRuleVersion:       version.LaneRuleVersion,
		MetricsRuleVersion:    version.MetricsRuleVersion,
		ScoreRuleVersion:      version.ScoreRuleVersion,
		RoleRegistrySHA256:    roleRegistrySHA,
		RoleOverridesSHA256:   roleOverridesSHA,
		MetricRegistrySHA256:  metricRegistrySHA,
		ScoringContractSHA256: scoringContractSHA,
	}
}

// CanonicalExists reports whether a canonical record matching the fingerprint
// is present for the match (i.e. the stage is already complete and resumable).
//
// Deprecated: use ValidateCanonical which also verifies the artifact tree.
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

// ValidateCanonical verifies a completed match tree end to end: the canonical
// record must exist, match the input fingerprint, declare the expected
// artifact set, and every recorded file must exist with a matching SHA-256
// and be covered by the recomputed tree hash. It returns the canonical record
// on success. Any mismatch (missing/corrupt file, tampered record, wrong
// fingerprint, unexpected extra files) returns a typed IntegrityError so the
// caller can fail closed or rebuild safely; a valid unchanged tree resumes.
func (s *Store) ValidateCanonical(matchID string, fp InputFingerprint, expected []string) (*Canonical, error) {
	var c Canonical
	if err := s.ReadJSON(matchID, ArtifactCanonical, &c); err != nil {
		if os.IsNotExist(err) {
			return nil, &IntegrityError{MatchID: matchID, Reason: "canonical_marker_missing"}
		}
		// Unreadable or malformed marker is invalid state; the caller must
		// invalidate it and rebuild (fail closed) rather than resume.
		return nil, &IntegrityError{MatchID: matchID, Reason: "canonical_marker_malformed"}
	}
	if c.SchemaVersion != version.ReportSchema {
		return nil, &IntegrityError{MatchID: matchID, Reason: "canonical_schema_mismatch"}
	}
	if c.MatchID != matchID {
		return nil, &IntegrityError{MatchID: matchID, Reason: "canonical_match_id_mismatch"}
	}
	if c.Fingerprint != fp {
		return nil, &IntegrityError{MatchID: matchID, Reason: "canonical_fingerprint_mismatch"}
	}

	// Expected artifact set must be exactly the recorded file set.
	expectedSet := map[string]bool{}
	for _, name := range expected {
		expectedSet[name] = true
	}
	for name := range c.Files {
		if !expectedSet[name] {
			return nil, &IntegrityError{MatchID: matchID, Reason: fmt.Sprintf("unexpected_artifact_%s", name)}
		}
	}
	for name := range expectedSet {
		if _, ok := c.Files[name]; !ok {
			return nil, &IntegrityError{MatchID: matchID, Reason: fmt.Sprintf("artifact_missing_from_canonical_%s", name)}
		}
	}

	// Re-hash every recorded file (streaming, bounded memory) and recompute
	// the tree hash.
	entries := map[string]string{}
	h := sha256.New()
	names := make([]string, 0, len(c.Files))
	for name := range c.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(s.MatchDir(matchID), name)
		got, err := hashFileStream(path)
		if err != nil {
			return nil, &IntegrityError{MatchID: matchID, Reason: fmt.Sprintf("artifact_missing_%s", name)}
		}
		if got != c.Files[name] {
			return nil, &IntegrityError{MatchID: matchID, Reason: fmt.Sprintf("artifact_hash_mismatch_%s", name)}
		}
		entries[name] = got
		fmt.Fprintf(h, "%s\x00%s\x00", name, got)
	}
	tree := hex.EncodeToString(h.Sum(nil))
	if tree != c.TreeSHA256 {
		return nil, &IntegrityError{MatchID: matchID, Reason: "canonical_tree_hash_mismatch"}
	}
	return &c, nil
}

// IntegrityError is a typed validation failure for a completed match tree.
type IntegrityError struct {
	MatchID string
	Reason  string
}

// Error implements the error interface.
func (e *IntegrityError) Error() string {
	return fmt.Sprintf("store: canonical integrity %s: %s", e.MatchID, e.Reason)
}

// InvalidateCanonical removes the completion marker so a failed/corrupt tree
// cannot be resumed. It is safe to call when the marker is already absent.
func (s *Store) InvalidateCanonical(matchID string) error {
	path := s.ArtifactPath(matchID, ArtifactCanonical)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: invalidate canonical %s: %w", matchID, err)
	}
	return nil
}

// hashFileStream computes the SHA-256 of a file through a fixed-size buffer
// (64 KiB) so memory usage is bounded regardless of artifact size. Never loads
// the whole file into memory.
func hashFileStream(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteCanonical computes the artifact-tree hash over the persisted artifact
// files (streaming, bounded memory) and writes the canonical record. It must
// be called only after every completion artifact (including report.json and
// status.json) is durably in place; the marker is the terminal publish gate.
func (s *Store) WriteCanonical(matchID string, fp InputFingerprint, artifacts []string) (*Canonical, error) {
	dir := s.MatchDir(matchID)
	entries := map[string]string{}
	h := sha256.New()
	names := append([]string(nil), artifacts...)
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		got, err := hashFileStream(path)
		if err != nil {
			return nil, fmt.Errorf("store: canonical read %s: %w", name, err)
		}
		entries[name] = got
		fmt.Fprintf(h, "%s\x00%s\x00", name, got)
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
	// Category is frozen input, not derived terminal state. Load it before the
	// authoritative status fast path so verified, quarantined, and resumed
	// rows retain the exact manifest classification after every rebuild.
	var input struct {
		Category string `json:"category"`
	}
	if err := s.ReadJSON(matchID, ArtifactInput, &input); err == nil {
		row.Category = input.Category
	}
	// Authoritative gated state: prefer the persisted status record written
	// after the publication gate. It is the single source of truth for
	// terminal status and publication; derivation is only a fallback for
	// matches that predate status persistence.
	if sr, err := s.ReadStatus(matchID); err == nil && sr.Status != "" {
		row.Status = sr.Status
		row.Publication = sr.Publication
		row.Reason = sr.Reason
		row.ArchiveState = sr.ArchiveState
		row.IdentityState = sr.IdentityState
		row.ClockState = sr.ClockState
		row.ParseState = sr.ParseState
		var can Canonical
		if err := s.ReadJSON(matchID, ArtifactCanonical, &can); err == nil {
			row.TreeSHA256 = can.TreeSHA256
		}
		if sr.DurationSec != nil {
			row.DurationSec = sr.DurationSec
		}
		if sr.GameStartUnix != nil {
			row.GameStartUnix = sr.GameStartUnix
		}
		return row
	}
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
		State               string   `json:"state"`
		GameDurationSeconds *float64 `json:"game_duration_seconds"`
		GameStartUnix       *int64   `json:"game_start_unix"`
		Reason              string   `json:"reason"`
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
