// Package commitlog is the policy-owned durable adapter for atomic policy commits.
package commitlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const (
	MaxSegmentBytes    int64 = 10 << 20
	MaxSessionBytes    int64 = 1 << 30
	MaxSessionSegments       = 104
	lengthBytes              = 4
	hashBytes                = sha256.Size
	markerBytes              = 8
)

var (
	commitMarker         = [markerBytes]byte{'P', 'C', 'O', 'M', 'M', 'I', 'T', 1}
	ErrInvalidCommit     = errors.New("policy_commit_invalid")
	ErrSequence          = errors.New("policy_commit_sequence")
	ErrAppendFailed      = errors.New("policy_commit_append_failed")
	ErrSealed            = errors.New("policy_log_sealed_hidden")
	ErrCorrupt           = errors.New("policy_log_corrupt_hidden")
	ErrCapacity          = errors.New("policy_log_capacity_hidden")
	ErrInvalidCheckpoint = errors.New("policy_checkpoint_invalid")
)

// Hooks are durability seams. Zero fields retain the production OS operation.
type Hooks struct {
	OpenFile  func(string, int, os.FileMode) (*os.File, error)
	Write     func(*os.File, []byte) (int, error)
	SyncFile  func(*os.File) error
	Truncate  func(*os.File, int64) error
	Rename    func(string, string) error
	SyncDir   func(string) error
	Interrupt func(stage string) error
}

type Option func(*Store)

func WithHooks(h Hooks) Option { return func(s *Store) { s.hooks = mergeHooks(h) } }
func WithSegmentLimit(n int64) Option {
	return func(s *Store) {
		if n > 0 {
			s.segmentLimit = n
		}
	}
}
func WithSessionLimits(bytes int64, segments int) Option {
	return func(s *Store) {
		if bytes > 0 {
			s.maxSessionBytes = bytes
		}
		if segments > 0 {
			s.maxSessionSegments = segments
		}
	}
}

type Committed struct {
	Commit  contracts.PolicyCommitV1
	Payload []byte
	Hash    string
}

type State struct {
	SessionID               string
	CommitSequence          uint64
	PolicyRevision          uint64
	StateHash               string
	LastObservationSequence uint64
	CommandResults          map[string]contracts.OperatorCommandResultV1
	Commits                 []Committed
}

type Store struct {
	mu                 sync.Mutex
	dir                string
	sessionID          string
	hooks              Hooks
	segmentLimit       int64
	maxSessionBytes    int64
	maxSessionSegments int
	file               *os.File
	fileSize           int64
	totalBytes         int64
	segmentCount       int
	state              State
	sealed             bool
	closed             bool
}

func defaultHooks() Hooks {
	return Hooks{
		OpenFile: os.OpenFile,
		Write:    func(f *os.File, p []byte) (int, error) { return f.Write(p) },
		SyncFile: func(f *os.File) error { return f.Sync() },
		Truncate: func(f *os.File, n int64) error { return f.Truncate(n) },
		Rename:   os.Rename,
		SyncDir: func(path string) error {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			return f.Sync()
		},
		Interrupt: func(string) error { return nil },
	}
}
func mergeHooks(h Hooks) Hooks {
	d := defaultHooks()
	if h.OpenFile != nil {
		d.OpenFile = h.OpenFile
	}
	if h.Write != nil {
		d.Write = h.Write
	}
	if h.SyncFile != nil {
		d.SyncFile = h.SyncFile
	}
	if h.Truncate != nil {
		d.Truncate = h.Truncate
	}
	if h.Rename != nil {
		d.Rename = h.Rename
	}
	if h.SyncDir != nil {
		d.SyncDir = h.SyncDir
	}
	if h.Interrupt != nil {
		d.Interrupt = h.Interrupt
	}
	return d
}

func Open(root, sessionID string, opts ...Option) (*Store, State, error) {
	if strings.TrimSpace(root) == "" || !safeID(sessionID) {
		return nil, State{}, errors.New("policy commit root and safe session id required")
	}
	dir := filepath.Join(root, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, State{}, fmt.Errorf("create policy log: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, State{}, fmt.Errorf("protect policy log: %w", err)
	}
	s := &Store{dir: dir, sessionID: sessionID, hooks: defaultHooks(), segmentLimit: MaxSegmentBytes, maxSessionBytes: MaxSessionBytes, maxSessionSegments: MaxSessionSegments}
	for _, opt := range opts {
		opt(s)
	}
	state, err := s.recover()
	if err != nil {
		return nil, State{}, err
	}
	s.state = state
	if err := s.reopenLastSegment(); err != nil {
		return nil, State{}, err
	}
	return s, cloneState(state), nil
}

func (s *Store) Append(commit contracts.PolicyCommitV1) (Committed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.closed {
		return Committed{}, ErrSealed
	}
	if err := commit.Validate(); err != nil {
		return Committed{}, fmt.Errorf("%w: %v", ErrInvalidCommit, err)
	}
	if commit.SessionID != s.sessionID {
		return Committed{}, fmt.Errorf("%w: session mismatch", ErrInvalidCommit)
	}
	if commit.CommandID != "" {
		for _, c := range s.state.Commits {
			if c.Commit.CommandID == commit.CommandID && c.Commit.CommandResult != nil {
				return cloneCommitted(c), nil
			}
		}
	}
	if err := validateNext(s.state, commit); err != nil {
		return Committed{}, err
	}
	payload, err := contracts.MarshalCanonical(commit)
	if err != nil || len(payload) > contracts.MaxPolicyCommitBytes {
		return Committed{}, fmt.Errorf("%w: canonical payload", ErrInvalidCommit)
	}
	frame, sum := encodeFrame(payload)
	if err := s.ensureSegment(commit.CommitSequence, int64(len(frame))); err != nil {
		s.sealed = true
		return Committed{}, fmt.Errorf("%w: segment: %w", ErrSealed, err)
	}
	prior := s.fileSize
	if err := s.hooks.Interrupt("before_append"); err != nil {
		return Committed{}, s.rollback(prior, err)
	}
	n, writeErr := s.hooks.Write(s.file, frame)
	if writeErr != nil || n != len(frame) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return Committed{}, s.rollback(prior, writeErr)
	}
	if err := s.hooks.Interrupt("before_sync"); err != nil {
		return Committed{}, s.rollback(prior, err)
	}
	if err := s.hooks.SyncFile(s.file); err != nil {
		return Committed{}, s.rollback(prior, err)
	}
	s.fileSize += int64(len(frame))
	s.totalBytes += int64(len(frame))
	var stored contracts.PolicyCommitV1
	if err := contracts.DecodeStrict(payload, &stored); err != nil {
		s.sealed = true
		return Committed{}, fmt.Errorf("%w: internal canonical decode", ErrSealed)
	}
	c := Committed{Commit: stored, Payload: append([]byte(nil), payload...), Hash: hex.EncodeToString(sum[:])}
	apply(&s.state, c)
	return cloneCommitted(c), nil
}

func (s *Store) rollback(prior int64, cause error) error {
	if err := s.hooks.Truncate(s.file, prior); err != nil {
		s.sealed = true
		return fmt.Errorf("%w: rollback_truncate", ErrSealed)
	}
	if _, err := s.file.Seek(prior, io.SeekStart); err != nil {
		s.sealed = true
		return fmt.Errorf("%w: rollback_seek", ErrSealed)
	}
	if err := s.hooks.SyncFile(s.file); err != nil {
		s.sealed = true
		return fmt.Errorf("%w: rollback_sync", ErrSealed)
	}
	info, err := s.file.Stat()
	if err != nil || info.Size() != prior {
		s.sealed = true
		return fmt.Errorf("%w: rollback_unverified", ErrSealed)
	}
	s.fileSize = prior
	return fmt.Errorf("%w: %v", ErrAppendFailed, cause)
}

func (s *Store) ensureSegment(sequence uint64, frameSize int64) error {
	if frameSize > s.segmentLimit {
		return errors.New("frame exceeds segment bound")
	}
	if frameSize > s.maxSessionBytes-s.totalBytes {
		return capacityError("aggregate_bytes")
	}
	if s.file != nil && s.fileSize+frameSize <= s.segmentLimit {
		return nil
	}
	if s.segmentCount >= s.maxSessionSegments {
		return capacityError("segment_count")
	}
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
	}
	name := fmt.Sprintf("%020d.pcl", sequence)
	final := filepath.Join(s.dir, name)
	tmp, err := s.hooks.OpenFile(filepath.Join(s.dir, "."+name+".tmp"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = s.hooks.Interrupt("segment_created"); err != nil {
		return err
	}
	if err = s.hooks.SyncFile(tmp); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = s.hooks.Rename(tmpPath, final); err != nil {
		return err
	}
	keep = true
	if err = s.hooks.SyncDir(s.dir); err != nil {
		return err
	}
	f, err := s.hooks.OpenFile(final, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err = os.Chmod(final, 0o600); err != nil {
		_ = f.Close()
		return err
	}
	s.file = f
	s.fileSize = 0
	s.segmentCount++
	return nil
}

func capacityError(reason string) error {
	return fmt.Errorf("%w: %w: %s", ErrSealed, ErrCapacity, reason)
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// WriteCheckpoint atomically replaces the optional cache. Committed frames
// remain authoritative when this operation fails.
func (s *Store) WriteCheckpoint(checkpoint contracts.PolicyCheckpointV1) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSealed
	}
	commit, ok := s.commitAt(checkpoint.CommitSequence)
	if !ok || checkpoint.ValidateAgainstCommit(commit.Commit) != nil || checkpoint.ReferencedCommitSHA256 != commit.Hash || !s.completeCheckpointIndex(checkpoint) {
		return ErrInvalidCheckpoint
	}
	payload, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return fmt.Errorf("%w: canonical", ErrInvalidCheckpoint)
	}
	tmpPath := filepath.Join(s.dir, ".checkpoint.v1.json.tmp")
	finalPath := filepath.Join(s.dir, "checkpoint.v1.json")
	tmp, err := s.hooks.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("checkpoint create: %w", err)
	}
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	n, err := s.hooks.Write(tmp, payload)
	if err != nil || n != len(payload) {
		return errors.New("checkpoint write failed")
	}
	if err = s.hooks.Interrupt("checkpoint_before_sync"); err != nil {
		return err
	}
	if err = s.hooks.SyncFile(tmp); err != nil {
		return fmt.Errorf("checkpoint sync: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = s.hooks.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("checkpoint rename: %w", err)
	}
	keep = true
	if err = os.Chmod(finalPath, 0o600); err != nil {
		return err
	}
	if err = s.hooks.SyncDir(s.dir); err != nil {
		return fmt.Errorf("checkpoint directory sync: %w", err)
	}
	return nil
}

// LoadCheckpoint returns a trusted cache and only the frames after it. A
// missing, incompatible, corrupt, or mismatched cache returns all log frames.
func (s *Store) LoadCheckpoint() (*contracts.PolicyCheckpointV1, []Committed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := cloneState(s.state).Commits
	payload, err := os.ReadFile(filepath.Join(s.dir, "checkpoint.v1.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, all, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var checkpoint contracts.PolicyCheckpointV1
	if contracts.DecodeStrict(payload, &checkpoint) != nil {
		return nil, all, nil
	}
	canonical, err := contracts.MarshalCanonical(checkpoint)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, all, nil
	}
	commit, ok := s.commitAt(checkpoint.CommitSequence)
	if !ok || checkpoint.ValidateAgainstCommit(commit.Commit) != nil || checkpoint.ReferencedCommitSHA256 != commit.Hash || !s.completeCheckpointIndex(checkpoint) {
		return nil, all, nil
	}
	later := make([]Committed, 0, len(s.state.Commits))
	for _, item := range s.state.Commits {
		if item.Commit.CommitSequence > checkpoint.CommitSequence {
			later = append(later, cloneCommitted(item))
		}
	}
	copyCheckpoint := checkpoint
	copyCheckpoint.CommandResults = append([]contracts.OperatorCommandResultV1(nil), checkpoint.CommandResults...)
	return &copyCheckpoint, later, nil
}

func (s *Store) commitAt(sequence uint64) (Committed, bool) {
	for _, commit := range s.state.Commits {
		if commit.Commit.CommitSequence == sequence {
			return commit, true
		}
	}
	return Committed{}, false
}

func (s *Store) completeCheckpointIndex(checkpoint contracts.PolicyCheckpointV1) bool {
	expected := make([]contracts.OperatorCommandResultV1, 0)
	for _, commit := range s.state.Commits {
		if commit.Commit.CommitSequence > checkpoint.CommitSequence {
			break
		}
		if commit.Commit.CommandResult != nil {
			expected = append(expected, *copyResult(*commit.Commit.CommandResult))
			if len(expected) == contracts.MaxCheckpointCommandResults {
				break
			}
		}
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].PreviousRevision != expected[j].PreviousRevision {
			return expected[i].PreviousRevision < expected[j].PreviousRevision
		}
		if expected[i].ResultingRevision != expected[j].ResultingRevision {
			return expected[i].ResultingRevision < expected[j].ResultingRevision
		}
		return expected[i].CommandID < expected[j].CommandID
	})
	if len(expected) != len(checkpoint.CommandResults) {
		return false
	}
	for i := range expected {
		left, err1 := contracts.MarshalCanonical(expected[i])
		right, err2 := contracts.MarshalCanonical(checkpoint.CommandResults[i])
		if err1 != nil || err2 != nil || !bytes.Equal(left, right) {
			return false
		}
	}
	return true
}

func encodeFrame(payload []byte) ([]byte, [32]byte) {
	sum := sha256.Sum256(payload)
	b := make([]byte, lengthBytes+len(payload)+hashBytes+markerBytes)
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	copy(b[4:], payload)
	copy(b[4+len(payload):], sum[:])
	copy(b[4+len(payload)+hashBytes:], commitMarker[:])
	return b, sum
}

func validateNext(state State, c contracts.PolicyCommitV1) error {
	if c.CommitSequence != state.CommitSequence+1 || c.PriorPolicyRevision != state.PolicyRevision {
		return ErrSequence
	}
	if state.CommitSequence > 0 && c.PriorStateHash != state.StateHash {
		return ErrSequence
	}
	return nil
}
func apply(s *State, c Committed) {
	s.CommitSequence = c.Commit.CommitSequence
	s.PolicyRevision = c.Commit.ResultingPolicyRevision
	s.StateHash = c.Commit.ResultingStateHash
	if c.Commit.ObservationSequence > s.LastObservationSequence {
		s.LastObservationSequence = c.Commit.ObservationSequence
	}
	if c.Commit.CommandResult != nil && len(s.CommandResults) < contracts.MaxCheckpointCommandResults {
		s.CommandResults[c.Commit.CommandID] = *copyResult(*c.Commit.CommandResult)
	}
	s.Commits = append(s.Commits, c)
}
func copyResult(r contracts.OperatorCommandResultV1) *contracts.OperatorCommandResultV1 {
	x := r
	x.DecisionIDs = append([]string(nil), r.DecisionIDs...)
	return &x
}
func cloneCommitted(c Committed) Committed {
	c.Payload = append([]byte(nil), c.Payload...)
	var commit contracts.PolicyCommitV1
	if err := contracts.DecodeStrict(c.Payload, &commit); err == nil {
		c.Commit = commit
	}
	return c
}
func cloneState(s State) State {
	x := s
	x.CommandResults = make(map[string]contracts.OperatorCommandResultV1, len(s.CommandResults))
	for k, v := range s.CommandResults {
		x.CommandResults[k] = *copyResult(v)
	}
	x.Commits = make([]Committed, len(s.Commits))
	for i, c := range s.Commits {
		x.Commits[i] = cloneCommitted(c)
	}
	return x
}
func safeID(v string) bool {
	if v == "" || v == "." || v == ".." {
		return false
	}
	for _, r := range v {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (s *Store) recover() (State, error) {
	state := State{SessionID: s.sessionID, CommandResults: map[string]contracts.OperatorCommandResultV1{}}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return state, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pcl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > s.maxSessionSegments {
		return State{}, capacityError("recovered_segment_count")
	}
	s.segmentCount = len(names)
	for index, name := range names {
		path := filepath.Join(s.dir, name)
		last := index == len(names)-1
		if err := s.recoverSegment(path, last, &state); err != nil {
			return State{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return State{}, err
		}
		if info.Size() > s.maxSessionBytes-s.totalBytes {
			return State{}, capacityError("recovered_aggregate_bytes")
		}
		s.totalBytes += info.Size()
	}
	return state, nil
}
func (s *Store) recoverSegment(path string, last bool, state *State) error {
	f, err := s.hooks.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if int64(len(data)) > s.segmentLimit {
		return fmt.Errorf("%w: segment bound exceeded", ErrCorrupt)
	}
	offset := 0
	for offset < len(data) {
		start := offset
		if len(data)-offset < lengthBytes {
			return s.recoverTail(f, path, last, int64(start))
		}
		n := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if n <= 0 || n > contracts.MaxPolicyCommitBytes {
			return fmt.Errorf("%w: invalid length in %s", ErrCorrupt, filepath.Base(path))
		}
		total := n + hashBytes + markerBytes
		if len(data)-offset < total {
			return s.recoverTail(f, path, last, int64(start))
		}
		payload := data[offset : offset+n]
		stored := data[offset+n : offset+n+hashBytes]
		marker := data[offset+n+hashBytes : offset+total]
		offset += total
		sum := sha256.Sum256(payload)
		if !bytes.Equal(stored, sum[:]) || !bytes.Equal(marker, commitMarker[:]) {
			return fmt.Errorf("%w: invalid terminated frame in %s", ErrCorrupt, filepath.Base(path))
		}
		var c contracts.PolicyCommitV1
		if err := contracts.DecodeStrict(payload, &c); err != nil {
			return fmt.Errorf("%w: decode: %v", ErrCorrupt, err)
		}
		if err := c.Validate(); err != nil || c.SessionID != s.sessionID {
			return fmt.Errorf("%w: nested commit: %v", ErrCorrupt, err)
		}
		canonical, err := contracts.MarshalCanonical(c)
		if err != nil || !bytes.Equal(canonical, payload) {
			return fmt.Errorf("%w: noncanonical payload", ErrCorrupt)
		}
		if err := validateNext(*state, c); err != nil {
			return fmt.Errorf("%w: sequence", ErrCorrupt)
		}
		if c.CommandID != "" && containsCommand(state.Commits, c.CommandID) {
			return fmt.Errorf("%w: duplicate command %q", ErrCorrupt, c.CommandID)
		}
		apply(state, Committed{Commit: c, Payload: append([]byte(nil), payload...), Hash: hex.EncodeToString(sum[:])})
	}
	return nil
}

func containsCommand(commits []Committed, commandID string) bool {
	for _, commit := range commits {
		if commit.Commit.CommandID == commandID {
			return true
		}
	}
	return false
}

func (s *Store) reopenLastSegment() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	path := filepath.Join(s.dir, names[len(names)-1])
	f, err := s.hooks.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return err
	}
	s.file = f
	s.fileSize = offset
	return nil
}
func (s *Store) recoverTail(f *os.File, path string, last bool, offset int64) error {
	if !last {
		return fmt.Errorf("%w: incomplete nonfinal segment", ErrCorrupt)
	}
	if err := s.hooks.Truncate(f, offset); err != nil {
		return fmt.Errorf("%w: recovery truncate", ErrSealed)
	}
	if err := s.hooks.SyncFile(f); err != nil {
		return fmt.Errorf("%w: recovery sync", ErrSealed)
	}
	info, err := f.Stat()
	if err != nil || info.Size() != offset {
		return fmt.Errorf("%w: recovery unverified", ErrSealed)
	}
	return nil
}
