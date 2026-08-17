// Package archive implements the frozen manifest, content-identity
// verification, and terminal archive states for the replay corpus.
package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/version"
)

// Terminal states for an archive/demo pair. Every manifest entry must reach an
// explicit terminal state; nothing is silently skipped.
const (
	StateMissing     = "missing"      // expected file absent
	StateCorrupt     = "corrupt"      // container/hash/size/magic mismatch
	StateVerified    = "verified"     // identity + container gates pass
	StateParseFailed = "parse_failed" // pinned parser failed to complete
	StateQuarantined = "quarantined"  // explicit quarantine with auditable reason
)

// Terminal reports whether s is a terminal state.
func Terminal(s string) bool {
	switch s {
	case StateMissing, StateCorrupt, StateVerified, StateParseFailed, StateQuarantined:
		return true
	}
	return false
}

// Manifest is the frozen probe/109-match manifest.
type Manifest struct {
	SchemaVersion string  `json:"schema_version"`
	Issue         string  `json:"issue"`
	CorpusRoot    string  `json:"corpus_root_runtime_only,omitempty"`
	Matches       []Match `json:"matches"`
}

// Match is one manifest entry.
type Match struct {
	Category              string           `json:"category"`
	MatchID               string           `json:"match_id"`
	Cluster               int              `json:"cluster"`
	ReplaySalt            int64            `json:"replay_salt"`
	StartTime             int64            `json:"start_time"`
	PublicDurationSeconds int              `json:"public_duration_seconds"`
	PublicRadiantTeam     string           `json:"public_radiant_team"`
	PublicDireTeam        string           `json:"public_dire_team"`
	PublicScore           string           `json:"public_score"`
	ArchiveRelativePath   string           `json:"archive_relative_path"`
	ArchiveBytes          int64            `json:"archive_bytes"`
	ArchiveSHA256         string           `json:"archive_sha256"`
	ArchiveMagicHex       string           `json:"archive_magic_hex"`
	DemoRelativePath      string           `json:"demo_relative_path"`
	DemoBytes             int64            `json:"demo_bytes"`
	DemoSHA256            string           `json:"demo_sha256"`
	DemoMagic             string           `json:"demo_magic"`
	ExpectedTeams         []ExpectedTeam   `json:"expected_teams"`
	ExpectedParticipants  []ExpectedPlayer `json:"expected_participants"`
	InitialStates         InitialStates    `json:"initial_states"`
}

// ExpectedTeam is the frozen team binding.
type ExpectedTeam struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Side     string `json:"side"`
}

// ExpectedPlayer is the frozen participant binding.
type ExpectedPlayer struct {
	RoleRecordID   string `json:"role_record_id"`
	AccountID      string `json:"account_id"`
	PlayerName     string `json:"player_name"`
	MatchName      string `json:"match_name"`
	TeamID         string `json:"team_id"`
	TeamName       string `json:"team_name"`
	Side           string `json:"side"`
	ExpectedHeroID int    `json:"expected_hero_id"`
	NominalRole    string `json:"nominal_role"`
	RoleConfidence string `json:"role_confidence"`
}

// InitialStates captures the pre-parse manifest states.
type InitialStates struct {
	ArchiveState     string  `json:"archive_state"`
	IdentityState    string  `json:"identity_state"`
	ParseState       string  `json:"parse_state"`
	PublicationState string  `json:"publication_state"`
	TerminalState    *string `json:"terminal_state"`
	Reason           string  `json:"reason"`
}

// LoadManifest reads a manifest JSON file.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("archive: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("archive: decode manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the manifest structure invariants.
func (m *Manifest) Validate() error {
	if m == nil || len(m.Matches) == 0 {
		return fmt.Errorf("archive: manifest has no matches")
	}
	seen := map[string]bool{}
	for i, mt := range m.Matches {
		if mt.MatchID == "" {
			return fmt.Errorf("archive: match %d has empty match_id", i)
		}
		if seen[mt.MatchID] {
			return fmt.Errorf("archive: duplicate match_id %q", mt.MatchID)
		}
		seen[mt.MatchID] = true
		if len(mt.ExpectedTeams) != 2 {
			return fmt.Errorf("archive: match %s expects %d teams, want 2", mt.MatchID, len(mt.ExpectedTeams))
		}
		if len(mt.ExpectedParticipants) != 10 {
			return fmt.Errorf("archive: match %s expects %d participants, want 10", mt.MatchID, len(mt.ExpectedParticipants))
		}
		if mt.ArchiveSHA256 == "" || mt.DemoSHA256 == "" {
			return fmt.Errorf("archive: match %s missing content hashes", mt.MatchID)
		}
	}
	return nil
}

// Find returns the manifest entry for a match id.
func (m *Manifest) Find(matchID string) (*Match, bool) {
	for i := range m.Matches {
		if m.Matches[i].MatchID == matchID {
			return &m.Matches[i], true
		}
	}
	return nil, false
}

// Verification is the content-identity record for one archive/demo pair.
type Verification struct {
	SchemaVersion         string `json:"schema_version"`
	MatchID               string `json:"match_id"`
	ArchiveRelativePath   string `json:"archive_relative_path"`
	DemoRelativePath      string `json:"demo_relative_path"`
	ArchiveBytesExpected  int64  `json:"archive_bytes_expected"`
	ArchiveBytesActual    int64  `json:"archive_bytes_actual"`
	ArchiveSHA256Expected string `json:"archive_sha256_expected"`
	ArchiveSHA256Actual   string `json:"archive_sha256_actual"`
	ArchiveMagicOK        bool   `json:"archive_magic_ok"`
	DemoBytesExpected     int64  `json:"demo_bytes_expected"`
	DemoBytesActual       int64  `json:"demo_bytes_actual"`
	DemoSHA256Expected    string `json:"demo_sha256_expected"`
	DemoSHA256Actual      string `json:"demo_sha256_actual"`
	DemoMagicOK           bool   `json:"demo_magic_ok"`
	State                 string `json:"state"`
	Reason                string `json:"reason"`
	CheckedAt             string `json:"checked_at"`
	ParserName            string `json:"parser_name"`
	ParserVersion         string `json:"parser_version"`
	AdapterVersion        string `json:"adapter_version"`
}

// VerifyContent checks a manifest entry against the corpus root and returns a
// Verification. It streams the archive, decompresses with zstd on the fly, and
// never loads the demo into memory. A temp demo file is only written when the
// caller requests it (used by the parse stage to avoid double decompression).
type VerifyOptions struct {
	// WriteDemo controls whether the decompressed demo is materialized.
	WriteDemo bool
	// DemoOut is the path used when WriteDemo is true.
	DemoOut string
}

// Verify performs the container and hash gate.
func Verify(root string, mt *Match, opts VerifyOptions) (*Verification, error) {
	v := &Verification{
		SchemaVersion:         version.IdentitySchema,
		MatchID:               mt.MatchID,
		ArchiveRelativePath:   mt.ArchiveRelativePath,
		DemoRelativePath:      mt.DemoRelativePath,
		ArchiveBytesExpected:  mt.ArchiveBytes,
		ArchiveSHA256Expected: mt.ArchiveSHA256,
		DemoBytesExpected:     mt.DemoBytes,
		DemoSHA256Expected:    mt.DemoSHA256,
		ParserName:            version.ParserName,
		ParserVersion:         version.ParserVersion,
		AdapterVersion:        version.AdapterVersion,
	}

	arcPath := filepath.Join(root, mt.ArchiveRelativePath)
	af, err := os.Open(arcPath)
	if err != nil {
		v.State = StateMissing
		v.Reason = "archive_missing"
		return v, nil
	}
	defer af.Close()

	st, err := af.Stat()
	if err != nil {
		v.State = StateMissing
		v.Reason = "archive_stat_failed"
		return v, nil
	}
	v.ArchiveBytesActual = st.Size()
	if st.Size() != mt.ArchiveBytes {
		v.State = StateCorrupt
		v.Reason = "archive_size_mismatch"
		return v, nil
	}

	// Streaming SHA-256 of the archive.
	ah := sha256.New()
	if _, err := io.Copy(ah, af); err != nil {
		v.State = StateCorrupt
		v.Reason = "archive_read_failed"
		return v, nil
	}
	v.ArchiveSHA256Actual = hex.EncodeToString(ah.Sum(nil))
	if v.ArchiveSHA256Actual != mt.ArchiveSHA256 {
		v.State = StateCorrupt
		v.Reason = "archive_sha256_mismatch"
		return v, nil
	}
	if _, err := af.Seek(0, io.SeekStart); err != nil {
		v.State = StateCorrupt
		v.Reason = "archive_seek_failed"
		return v, nil
	}

	// Magic: the first four bytes must be the Zstandard magic.
	magic := make([]byte, 4)
	if _, err := io.ReadFull(af, magic); err != nil {
		v.State = StateCorrupt
		v.Reason = "archive_magic_unreadable"
		return v, nil
	}
	v.ArchiveMagicOK = bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd})
	if !v.ArchiveMagicOK {
		v.State = StateCorrupt
		v.Reason = "archive_magic_mismatch"
		return v, nil
	}
	if _, err := af.Seek(0, io.SeekStart); err != nil {
		v.State = StateCorrupt
		v.Reason = "archive_seek_failed"
		return v, nil
	}

	dec, err := zstd.NewReader(af, zstd.WithDecoderConcurrency(1))
	if err != nil {
		v.State = StateCorrupt
		v.Reason = "zstd_init_failed"
		return v, nil
	}
	defer dec.Close()

	// Verify the Source 2 demo magic on the decompressed stream head, then
	// stream the remaining bytes through the hash while writing to the
	// optional demo output file.
	dmagic := make([]byte, 8)
	if _, err := io.ReadFull(dec, dmagic); err != nil {
		v.State = StateCorrupt
		v.Reason = "demo_magic_unreadable"
		return v, nil
	}
	v.DemoMagicOK = bytes.Equal(dmagic, []byte{'P', 'B', 'D', 'E', 'M', 'S', '2', 0x00})
	if !v.DemoMagicOK {
		v.State = StateCorrupt
		v.Reason = "demo_magic_mismatch"
		return v, nil
	}

	dh := sha256.New()
	dh.Write(dmagic)
	demoOut := io.Discard
	var outFile *os.File
	if opts.WriteDemo {
		outFile, err = os.Create(opts.DemoOut)
		if err != nil {
			v.State = StateCorrupt
			v.Reason = "demo_write_open_failed"
			return v, nil
		}
		defer outFile.Close()
		if _, err := outFile.Write(dmagic); err != nil {
			v.State = StateCorrupt
			v.Reason = "demo_write_failed"
			return v, nil
		}
		demoOut = outFile
	}
	written, err := io.Copy(io.MultiWriter(dh, demoOut), dec)
	if err != nil {
		v.State = StateCorrupt
		v.Reason = "decompress_failed"
		return v, nil
	}
	v.DemoBytesActual = written + int64(len(dmagic))
	if v.DemoBytesActual != mt.DemoBytes {
		v.State = StateCorrupt
		v.Reason = "demo_size_mismatch"
		return v, nil
	}
	v.DemoSHA256Actual = hex.EncodeToString(dh.Sum(nil))
	if v.DemoSHA256Actual != mt.DemoSHA256 {
		v.State = StateCorrupt
		v.Reason = "demo_sha256_mismatch"
		return v, nil
	}

	v.State = StateVerified
	v.Reason = "identity_and_container_gates_pass"
	return v, nil
}

// CanonicalJSON returns the deterministic encoding of the verification.
func (v *Verification) CanonicalJSON() ([]byte, error) { return json.Marshal(v) }
