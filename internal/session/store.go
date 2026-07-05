package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrInvalidJSON = errors.New("invalid json")

type Clock func() time.Time

type Option func(*Store)

type Store struct {
	root      string
	sessionID string
	clock     Clock
	mu        sync.Mutex
}

type Record struct {
	ReceivedAt time.Time       `json:"received_at"`
	Payload    any             `json:"payload"`
	Raw        json.RawMessage `json:"raw"`
}

func WithClock(clock Clock) Option {
	return func(store *Store) {
		if clock != nil {
			store.clock = clock
		}
	}
}

func WithSessionID(sessionID string) Option {
	return func(store *Store) {
		store.sessionID = sessionID
	}
}

func NewStore(root string, opts ...Option) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("session root is required")
	}

	store := &Store{
		root:  root,
		clock: time.Now,
	}
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

	return store, nil
}

func (s *Store) SessionID() string {
	return s.sessionID
}

func (s *Store) SessionDir() string {
	return filepath.Join(s.root, s.sessionID)
}

func (s *Store) RawPath() string {
	return filepath.Join(s.SessionDir(), "raw.jsonl")
}

func (s *Store) Append(raw []byte) (*Record, error) {
	payload, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}

	record := &Record{
		ReceivedAt: s.clock().UTC(),
		Payload:    payload,
		Raw:        append(json.RawMessage(nil), raw...),
	}
	line, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.SessionDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	file, err := os.OpenFile(s.RawPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open raw JSONL: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("write raw JSONL: %w", err)
	}

	return record, nil
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data", ErrInvalidJSON)
	}

	return payload, nil
}

func isSafeSessionID(sessionID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	if sessionID == "." || sessionID == ".." {
		return false
	}
	return !strings.ContainsAny(sessionID, `/\`)
}
