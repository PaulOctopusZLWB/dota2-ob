package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidJSON  = errors.New("invalid json")
	ErrAppendFailed = errors.New("raw append failed")
	ErrStoreSealed  = errors.New("raw store sealed")
)

type Clock func() time.Time

// RawFile is the narrow session-owned append boundary. *os.File implements it.
type RawFile interface {
	io.Writer
	io.Seeker
	Truncate(size int64) error
	Stat() (os.FileInfo, error)
	Close() error
}

type OpenRawFile func(path string) (RawFile, error)
type Option func(*Store)

type Store struct {
	root      string
	sessionID string
	clock     Clock
	openFile  OpenRawFile
	file      RawFile
	sequence  uint64
	highWater *HighWater
	sealed    bool
	closed    bool
	mu        sync.Mutex
}

type Record struct {
	SchemaVersion    int              `json:"schema_version"`
	SessionID        string           `json:"session_id"`
	Sequence         uint64           `json:"sequence"`
	ReceivedAt       time.Time        `json:"received_at"`
	Source           string           `json:"source"`
	Payload          any              `json:"payload"`
	Raw              json.RawMessage  `json:"raw"`
	ProjectionResult ProjectionResult `json:"-"`
	ProjectionCode   string           `json:"-"`
	ProjectionReason string           `json:"-"`
}

type ProjectionResult string

const (
	ProjectionProduced         ProjectionResult = "produced"
	ProjectionConsumedNoOutput ProjectionResult = "consumed_no_output"
	maxRawBodyBytes                             = 10 << 20
)

func WithClock(clock Clock) Option {
	return func(store *Store) {
		if clock != nil {
			store.clock = clock
		}
	}
}

func WithSessionID(sessionID string) Option {
	return func(store *Store) { store.sessionID = sessionID }
}

func WithRawFile(open OpenRawFile) Option {
	return func(store *Store) {
		if open != nil {
			store.openFile = open
		}
	}
}

func WithHighWater(highWater *HighWater) Option {
	return func(store *Store) { store.highWater = highWater }
}

func NewStore(root string, opts ...Option) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("session root is required")
	}
	store := &Store{root: root, clock: time.Now}
	for _, opt := range opts {
		opt(store)
	}
	if store.sessionID == "" {
		store.sessionID = store.clock().UTC().Format("20060102T150405.000000000Z")
	}
	if !isSafeSessionID(store.sessionID) {
		return nil, fmt.Errorf("unsafe session id %q", store.sessionID)
	}
	if err := os.MkdirAll(store.SessionDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	sequence, version, err := recoverRawFile(store.RawPath(), store.sessionID)
	if err != nil {
		return nil, err
	}
	if sequence > 0 && version != 3 {
		return nil, errors.New("legacy raw session is read-only; start a new V3 session")
	}
	store.sequence = sequence
	if err := ensureCaptureLineageV3(store.SessionDir(), store.sequence); err != nil {
		return nil, err
	}
	if store.openFile == nil {
		store.openFile = func(path string) (RawFile, error) {
			return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		}
	}
	if store.highWater == nil {
		store.highWater = NewHighWater(store.sessionID, store.sequence)
	}
	return store, nil
}

func (s *Store) SessionID() string     { return s.sessionID }
func (s *Store) SessionDir() string    { return filepath.Join(s.root, s.sessionID) }
func (s *Store) RawPath() string       { return filepath.Join(s.SessionDir(), "raw.jsonl") }
func (s *Store) HighWater() *HighWater { return s.highWater }

func (s *Store) Append(raw []byte) (*Record, error) {
	if len(raw) > maxRawBodyBytes {
		return nil, fmt.Errorf("%w: body exceeds 10 MiB", ErrInvalidJSON)
	}
	payload, result, code, reason, err := decodeBoundedGSI(raw)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.closed {
		return nil, ErrStoreSealed
	}
	if s.file == nil {
		s.file, err = s.openFile(s.RawPath())
		if err != nil {
			return nil, fmt.Errorf("%w: open", ErrAppendFailed)
		}
	}
	if valid, available := rawContentGuardState(s.RawPath()); available && !valid {
		s.sealed = true
		return nil, ErrStoreSealed
	}
	prior, err := s.file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("%w: seek", ErrAppendFailed)
	}
	next := s.sequence + 1
	record := &Record{
		SchemaVersion: 3, SessionID: s.sessionID, Sequence: next,
		ReceivedAt: s.clock().UTC(), Source: "gsi", Payload: payload,
		Raw:              append(json.RawMessage(nil), raw...),
		ProjectionResult: result, ProjectionCode: code, ProjectionReason: reason,
	}
	writeErr := writeRawRecordV3(s.file, record)
	if writeErr != nil {
		if rollbackErr := s.rollback(prior); rollbackErr != nil {
			s.sealed = true
			return nil, ErrStoreSealed
		}
		_ = refreshRawContentGuard(s.RawPath())
		return nil, ErrAppendFailed
	}
	_ = refreshRawContentGuard(s.RawPath())
	s.sequence = next
	if s.highWater != nil {
		s.highWater.Publish(next)
	}
	return record, nil
}

func writeRawRecordV3(w io.Writer, record *Record) error {
	sum := sha256.Sum256(record.Raw)
	prefix := fmt.Sprintf(`{"schema_version":3,"session_id":%q,"sequence":%d,"received_at":%q,"source":"gsi","raw_encoding":"base64_std","raw_byte_length":%d,"raw_base64":"`, record.SessionID, record.Sequence, record.ReceivedAt.UTC().Format(time.RFC3339Nano), len(record.Raw))
	if err := writeComplete(w, []byte(prefix)); err != nil {
		return err
	}
	encoder := base64.NewEncoder(base64.StdEncoding, writerFunc(func(p []byte) (int, error) {
		if err := writeComplete(w, p); err != nil {
			return 0, err
		}
		return len(p), nil
	}))
	if _, err := encoder.Write(record.Raw); err != nil {
		_ = encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	suffix := `","raw_payload_sha256":"` + hex.EncodeToString(sum[:]) + `"}` + "\n"
	return writeComplete(w, []byte(suffix))
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func writeComplete(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err != nil {
		return err
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}

func (s *Store) rollback(offset int64) error {
	if err := s.file.Truncate(offset); err != nil {
		return err
	}
	position, err := s.file.Seek(offset, io.SeekStart)
	if err != nil {
		return err
	}
	if position != offset {
		return errors.New("rollback position mismatch")
	}
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != offset {
		return errors.New("rollback size mismatch")
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

func recoverRawFile(path, sessionID string) (uint64, int, error) {
	var chain [sha256.Size]byte
	framingVersion, detectErr := detectRawSchemaVersion(path)
	if detectErr != nil && !errors.Is(detectErr, os.ErrNotExist) {
		return 0, 0, fmt.Errorf("detect raw schema: %w", detectErr)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("open raw jsonl: %w", err)
	}
	defer file.Close()
	indexPath := rawAuthorityPath(path)
	tmpPath := indexPath + ".tmp"
	index, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, fmt.Errorf("create raw authority index: %w", err)
	}
	keepIndex := false
	defer func() {
		_ = index.Close()
		if !keepIndex {
			_ = os.Remove(tmpPath)
		}
	}()
	reader := bufio.NewReaderSize(file, 64*1024)
	sequence := uint64(0)
	version := 0
	committed := int64(0)
	summary := liveCursor{SchemaVersion: 3, SessionID: sessionID}
	for {
		line, terminated, readErr := readPersistedLine(reader, framingVersion)
		if readErr == io.EOF {
			if len(line) > 0 {
				if err := file.Truncate(committed); err != nil {
					return 0, 0, fmt.Errorf("recover raw tail: %w", err)
				}
			}
			break
		}
		if readErr != nil {
			return 0, 0, readErr
		}
		if !terminated || len(line) == 1 {
			return 0, 0, fmt.Errorf("invalid committed raw record %d", sequence+1)
		}
		sequence++
		frame := line[:len(line)-1]
		record, decodeErr := decodePersistedRecord(frame, sessionID, sequence)
		if decodeErr != nil {
			return 0, 0, fmt.Errorf("invalid committed raw record %d: %w", sequence, decodeErr)
		}
		current := record.SchemaVersion
		if version == 0 {
			version = current
		} else if current != version {
			return 0, 0, errors.New("mixed raw schema versions")
		}
		committed += int64(len(line))
		chain = advanceRawChain(chain, record.Raw)
		applyCursorResult(&summary, record)
		if err := writeComplete(index, encodeRawAuthority(committed, chain, rawAuthoritySummaryHash(summary))); err != nil {
			return 0, 0, err
		}
	}
	if err := index.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(tmpPath, indexPath); err != nil {
		return 0, 0, err
	}
	keepIndex = true
	_ = refreshCompleteRawGuard(path)
	return sequence, version, nil
}

func persistedSchemaVersion(line []byte) (json.RawMessage, error) {
	var header struct {
		SchemaVersion json.RawMessage `json:"schema_version"`
	}
	if err := json.Unmarshal(line, &header); err != nil {
		return nil, err
	}
	return header.SchemaVersion, nil
}

func persistedFramingVersion(schemaVersion json.RawMessage) int {
	switch string(schemaVersion) {
	case "":
		return 1
	case "2":
		return 2
	default:
		return 3
	}
}

func validatePersistedRecord(line []byte, schemaVersion json.RawMessage, sessionID string, sequence uint64) error {
	var header struct {
		SessionID  string          `json:"session_id"`
		Sequence   uint64          `json:"sequence"`
		Source     string          `json:"source"`
		ReceivedAt time.Time       `json:"received_at"`
		Payload    json.RawMessage `json:"payload"`
		Raw        json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(line, &header); err != nil {
		return err
	}
	if len(schemaVersion) == 0 {
		if header.ReceivedAt.IsZero() || len(header.Payload) == 0 || len(header.Raw) == 0 {
			return errors.New("invalid v1 record")
		}
		return validateSemanticRaw(header.Payload, header.Raw)
	}
	var version int
	if string(schemaVersion) == "3" {
		_, err := DecodeRecordV3(line, sessionID, sequence)
		return err
	}
	if string(schemaVersion) == "null" || json.Unmarshal(schemaVersion, &version) != nil || version != 2 || header.SessionID != sessionID || header.Sequence != sequence || header.Source != "gsi" || header.ReceivedAt.IsZero() || len(header.Payload) == 0 || len(header.Raw) == 0 {
		return errors.New("invalid v2 record")
	}
	return validateSemanticRaw(header.Payload, header.Raw)
}

func decodePersistedRecord(line []byte, sessionID string, sequence uint64) (*Record, error) {
	schemaVersion, err := persistedSchemaVersion(line)
	if err != nil {
		return nil, err
	}
	if string(schemaVersion) == "3" {
		return DecodeRecordV3(line, sessionID, sequence)
	}
	if err := validatePersistedRecord(line, schemaVersion, sessionID, sequence); err != nil {
		return nil, err
	}
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	if len(schemaVersion) == 0 {
		record.SchemaVersion = 1
		record.SessionID = sessionID
		record.Sequence = sequence
		record.Source = "gsi"
	}
	record.Payload, record.ProjectionResult, record.ProjectionCode, record.ProjectionReason = boundedGSIProjection(record.Payload)
	return &record, nil
}

func validateSemanticRaw(payloadRaw, sourceRaw json.RawMessage) error {
	payload, err := decodeJSON(payloadRaw)
	if err != nil {
		return err
	}
	source, err := decodeJSON(sourceRaw)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(payload, source) {
		return errors.New("raw payload mismatch")
	}
	return nil
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: malformed value", ErrInvalidJSON)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data", ErrInvalidJSON)
	}
	return payload, nil
}

func isSafeSessionID(sessionID string) bool {
	if len(sessionID) < 1 || len(sessionID) > 128 || sessionID == "." || sessionID == ".." {
		return false
	}
	for i := 0; i < len(sessionID); i++ {
		c := sessionID[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
