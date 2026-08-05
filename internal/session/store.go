package session

import (
	"bytes"
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
	sealed    bool
	closed    bool
	mu        sync.Mutex
}

type Record struct {
	SchemaVersion int             `json:"schema_version"`
	SessionID     string          `json:"session_id"`
	Sequence      uint64          `json:"sequence"`
	ReceivedAt    time.Time       `json:"received_at"`
	Source        string          `json:"source"`
	Payload       any             `json:"payload"`
	Raw           json.RawMessage `json:"raw"`
}

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

	if store.openFile == nil {
		sequence, err := recoverRawFile(store.RawPath(), store.sessionID)
		if err != nil {
			return nil, err
		}
		store.sequence = sequence
		store.openFile = func(path string) (RawFile, error) {
			return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		}
	}
	return store, nil
}

func (s *Store) SessionID() string  { return s.sessionID }
func (s *Store) SessionDir() string { return filepath.Join(s.root, s.sessionID) }
func (s *Store) RawPath() string    { return filepath.Join(s.SessionDir(), "raw.jsonl") }

func (s *Store) Append(raw []byte) (*Record, error) {
	payload, err := decodeJSON(raw)
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
	prior, err := s.file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("%w: seek", ErrAppendFailed)
	}
	next := s.sequence + 1
	record := &Record{
		SchemaVersion: 2, SessionID: s.sessionID, Sequence: next,
		ReceivedAt: s.clock().UTC(), Source: "gsi", Payload: payload,
		Raw: append(json.RawMessage(nil), raw...),
	}
	line, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}
	line = append(line, '\n')
	n, writeErr := s.file.Write(line)
	if writeErr != nil || n != len(line) {
		if rollbackErr := s.rollback(prior); rollbackErr != nil {
			s.sealed = true
			return nil, ErrStoreSealed
		}
		return nil, ErrAppendFailed
	}
	s.sequence = next
	return record, nil
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

func recoverRawFile(path, sessionID string) (uint64, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read raw jsonl: %w", err)
	}
	committed := len(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if last := bytes.LastIndexByte(data, '\n'); last >= 0 {
			committed = last + 1
		} else {
			committed = 0
		}
	}
	sequence := uint64(0)
	lines := bytes.Split(data[:committed], []byte{'\n'})
	for index, line := range lines {
		if len(line) == 0 {
			if index == len(lines)-1 {
				continue
			}
			return 0, fmt.Errorf("invalid committed raw record %d", sequence+1)
		}
		sequence++
		if err := validatePersistedRecord(line, sessionID, sequence); err != nil {
			return 0, fmt.Errorf("invalid committed raw record %d", sequence)
		}
	}
	if committed != len(data) {
		if err := os.Truncate(path, int64(committed)); err != nil {
			return 0, fmt.Errorf("recover raw tail: %w", err)
		}
	}
	return sequence, nil
}

func validatePersistedRecord(line []byte, sessionID string, sequence uint64) error {
	var header struct {
		SchemaVersion *int            `json:"schema_version"`
		SessionID     string          `json:"session_id"`
		Sequence      uint64          `json:"sequence"`
		Source        string          `json:"source"`
		ReceivedAt    time.Time       `json:"received_at"`
		Payload       json.RawMessage `json:"payload"`
		Raw           json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(line, &header); err != nil {
		return err
	}
	if header.SchemaVersion == nil {
		if header.ReceivedAt.IsZero() || len(header.Payload) == 0 || len(header.Raw) == 0 {
			return errors.New("invalid v1 record")
		}
		return validateSemanticRaw(header.Payload, header.Raw)
	}
	if *header.SchemaVersion != 2 || header.SessionID != sessionID || header.Sequence != sequence || header.Source != "gsi" || header.ReceivedAt.IsZero() || len(header.Payload) == 0 || len(header.Raw) == 0 {
		return errors.New("invalid v2 record")
	}
	return validateSemanticRaw(header.Payload, header.Raw)
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
	if strings.TrimSpace(sessionID) == "" || sessionID == "." || sessionID == ".." {
		return false
	}
	return !strings.ContainsAny(sessionID, `/\`)
}
