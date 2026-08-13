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

var (
	commitMarkerV2  = [markerBytes]byte{'P', 'C', 'O', 'M', 'M', 'I', 'T', 2}
	ErrMixedLineage = errors.New("policy_log_mixed_lineage_hidden")
	ErrLineage      = errors.New("policy_lineage_invalid_hidden")
	ErrCommandLimit = errors.New("session_command_limit")
)

type V2Option func(*StoreV2)

func WithV2Hooks(h Hooks) V2Option { return func(s *StoreV2) { s.hooks = mergeHooks(h) } }

// WithV2ReplayVerifier is mandatory for OpenV2. It is an application-owned
// composition boundary that binds recovered frames to immutable source inputs
// and byte-equivalent pure semantic replay before the store becomes writable.
func WithV2ReplayVerifier(verifier ReplayVerifierV2) V2Option {
	return func(s *StoreV2) { s.verifier = &verifier }
}

func WithV2SegmentLimit(n int64) V2Option {
	return func(s *StoreV2) {
		if n > 0 {
			s.segmentLimit = n
		}
	}
}

func WithV2SessionLimits(bytes int64, segments int) V2Option {
	return func(s *StoreV2) {
		if bytes > 0 {
			s.maxSessionBytes = bytes
		}
		if segments > 0 {
			s.maxSessionSegments = segments
		}
	}
}

type CommittedV2 struct {
	Commit  contracts.PolicyCommitV2
	Payload []byte
	Hash    string
	Locator contracts.PolicyCommandLocatorV2
}

type StateV2 struct {
	SessionID               string
	LineageManifestID       string
	CommitSequence          uint64
	PolicyRevision          uint64
	StateHash               string
	LastObservationSequence uint64
	LastPolicyTimeMS        int64
	CommandResults          map[string]string
	CommandLocators         map[string]contracts.PolicyCommandLocatorV2
	// Commits is retained for source compatibility and is always empty.
	// Recovery verifies frames as a stream instead of retaining payloads.
	Commits []CommittedV2
}

type StoreV2 struct {
	mu                 sync.Mutex
	dir                string
	sessionID          string
	manifestID         string
	hooks              Hooks
	segmentLimit       int64
	maxSessionBytes    int64
	maxSessionSegments int
	file               *os.File
	fileSize           int64
	totalBytes         int64
	segmentCount       int
	state              StateV2
	verifier           *ReplayVerifierV2
	sealed             bool
	closed             bool
}

// OpenV2 seals or verifies the immutable lineage manifest before opening any
// production V2 policy frames. A directory containing V1 frames is rejected.
func OpenV2(root, sessionID string, manifest contracts.PolicyLineageManifestV2, opts ...V2Option) (*StoreV2, StateV2, error) {
	if strings.TrimSpace(root) == "" || !safeID(sessionID) || manifest.SessionID != sessionID {
		return nil, StateV2{}, fmt.Errorf("%w: root/session mismatch", ErrLineage)
	}
	manifestID, err := manifest.ContentID()
	if err != nil {
		return nil, StateV2{}, fmt.Errorf("%w: %v", ErrLineage, err)
	}
	dir := filepath.Join(root, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, StateV2{}, fmt.Errorf("create policy log: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, StateV2{}, fmt.Errorf("protect policy log: %w", err)
	}
	s := &StoreV2{
		dir: dir, sessionID: sessionID, manifestID: manifestID, hooks: defaultHooks(),
		segmentLimit: MaxSegmentBytes, maxSessionBytes: MaxSessionBytes, maxSessionSegments: MaxSessionSegments,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.verifier == nil || s.verifier.VerifyObservation == nil || s.verifier.VerifyCommand == nil || s.verifier.Reevaluate == nil {
		return nil, StateV2{}, errors.New("v2 recovery verifier and re-evaluator required")
	}
	if err := s.rejectV1Frames(); err != nil {
		return nil, StateV2{}, err
	}
	if err := s.sealManifest(manifest); err != nil {
		return nil, StateV2{}, err
	}
	state, err := s.recover()
	if err != nil {
		return nil, StateV2{}, err
	}
	// A prior process may have renamed a segment and then lost the directory
	// sync result. Re-syncing the parent after structural and semantic recovery
	// proves every recovered segment entry durable before one is reopened.
	if s.segmentCount > 0 {
		if err := s.hooks.SyncDir(s.dir); err != nil {
			return nil, StateV2{}, fmt.Errorf("%w: recovered segment directory sync: %v", ErrSealed, err)
		}
	}
	s.state = state
	if err := s.reopenLastSegment(); err != nil {
		return nil, StateV2{}, err
	}
	return s, cloneStateV2(state), nil
}

func (s *StoreV2) rejectV1Frames() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl") {
			return ErrMixedLineage
		}
	}
	return nil
}

func (s *StoreV2) sealManifest(manifest contracts.PolicyLineageManifestV2) error {
	payload, err := contracts.MarshalCanonical(manifest)
	if err != nil {
		return fmt.Errorf("%w: canonical manifest", ErrLineage)
	}
	final := filepath.Join(s.dir, "lineage.v2.json")
	existing, err := os.ReadFile(final)
	if err == nil {
		if !bytes.Equal(existing, payload) {
			return fmt.Errorf("%w: manifest substitution", ErrLineage)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: read manifest: %v", ErrLineage, err)
	}
	entries, readErr := os.ReadDir(s.dir)
	if readErr != nil {
		return fmt.Errorf("%w: inspect lineage: %v", ErrLineage, readErr)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl2") {
			return fmt.Errorf("%w: manifest missing for existing frames", ErrLineage)
		}
	}
	tmpPath := filepath.Join(s.dir, ".lineage.v2.json.tmp")
	tmp, err := s.hooks.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create manifest: %v", ErrLineage, err)
	}
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if n, writeErr := s.hooks.Write(tmp, payload); writeErr != nil || n != len(payload) {
		return fmt.Errorf("%w: write manifest", ErrLineage)
	}
	if err := s.hooks.SyncFile(tmp); err != nil {
		return fmt.Errorf("%w: sync manifest: %v", ErrLineage, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close manifest: %v", ErrLineage, err)
	}
	if err := s.hooks.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("%w: rename manifest: %v", ErrLineage, err)
	}
	keep = true
	if err := os.Chmod(final, 0o600); err != nil {
		return fmt.Errorf("%w: protect manifest: %v", ErrLineage, err)
	}
	if err := s.hooks.SyncDir(s.dir); err != nil {
		return fmt.Errorf("%w: sync manifest directory: %v", ErrLineage, err)
	}
	return nil
}

func (s *StoreV2) Append(commit contracts.PolicyCommitV2) (CommittedV2, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.closed {
		return CommittedV2{}, ErrSealed
	}
	if err := commit.Validate(); err != nil || commit.SessionID != s.sessionID || commit.LineageManifestID != s.manifestID || commit.LineageManifestSHA256 != s.manifestID {
		return CommittedV2{}, fmt.Errorf("%w: v2 contract or lineage mismatch", ErrInvalidCommit)
	}
	if commit.CommandID != "" {
		if locator, ok := s.state.CommandLocators[commit.CommandID]; ok {
			stored, err := s.readLocator(locator, s.state.CommandResults[commit.CommandID])
			if err != nil {
				s.sealed = true
				return CommittedV2{}, err
			}
			return stored, nil
		}
		if len(s.state.CommandResults) >= contracts.MaxCheckpointCommandResults {
			return CommittedV2{}, ErrCommandLimit
		}
	}
	if err := validateNextV2(s.state, commit); err != nil {
		return CommittedV2{}, err
	}
	payload, err := contracts.MarshalCanonical(commit)
	if err != nil || len(payload) > contracts.MaxPolicyCommitBytes {
		return CommittedV2{}, fmt.Errorf("%w: canonical payload", ErrInvalidCommit)
	}
	frame, sum := encodeFrameV2(payload)
	if err := s.ensureSegment(commit.CommitSequence, int64(len(frame))); err != nil {
		s.sealed = true
		return CommittedV2{}, fmt.Errorf("%w: segment: %w", ErrSealed, err)
	}
	prior := s.fileSize
	if err := s.hooks.Interrupt("before_append"); err != nil {
		return CommittedV2{}, s.rollback(prior, err)
	}
	n, writeErr := s.hooks.Write(s.file, frame)
	if writeErr != nil || n != len(frame) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return CommittedV2{}, s.rollback(prior, writeErr)
	}
	if err := s.hooks.Interrupt("before_sync"); err != nil {
		return CommittedV2{}, s.rollback(prior, err)
	}
	if err := s.hooks.SyncFile(s.file); err != nil {
		return CommittedV2{}, s.rollback(prior, err)
	}
	s.fileSize += int64(len(frame))
	s.totalBytes += int64(len(frame))
	var stored contracts.PolicyCommitV2
	if err := contracts.DecodeStrict(payload, &stored); err != nil {
		s.sealed = true
		return CommittedV2{}, fmt.Errorf("%w: internal canonical decode", ErrSealed)
	}
	hash := hex.EncodeToString(sum[:])
	locator := contracts.PolicyCommandLocatorV2{}
	if stored.CommandID != "" {
		locator = contracts.PolicyCommandLocatorV2{
			CommandID: stored.CommandID, SegmentID: filepath.Base(s.file.Name()), FrameOffset: prior,
			CommitSequence: stored.CommitSequence, FrameSHA256: hash,
		}
	}
	committed := CommittedV2{Commit: stored, Payload: append([]byte(nil), payload...), Hash: hash, Locator: locator}
	applyV2(&s.state, committed)
	return cloneCommittedV2(committed), nil
}

// AppendPolicyCommit adapts StoreV2 to policy.CommitAppender while preserving
// Append's synchronous frame-and-sync durability boundary.
func (s *StoreV2) AppendPolicyCommit(commit contracts.PolicyCommitV2) error {
	_, err := s.Append(commit)
	return err
}

// LookupCommand resolves an admitted duplicate directly from its cache-only
// locator and revalidates the complete frame against the semantic result hash.
func (s *StoreV2) LookupCommand(commandID string) (contracts.OperatorCommandResultV1, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.closed {
		return contracts.OperatorCommandResultV1{}, false, ErrSealed
	}
	if !contracts.ValidPolicyIdentifierV2(commandID) {
		return contracts.OperatorCommandResultV1{}, false, ErrInvalidCommit
	}
	resultHash, ok := s.state.CommandResults[commandID]
	if !ok {
		return contracts.OperatorCommandResultV1{}, false, nil
	}
	locator, ok := s.state.CommandLocators[commandID]
	if !ok {
		s.sealed = true
		return contracts.OperatorCommandResultV1{}, false, ErrCorrupt
	}
	committed, err := s.readLocator(locator, resultHash)
	if err != nil {
		s.sealed = true
		return contracts.OperatorCommandResultV1{}, false, err
	}
	if committed.Commit.CommandResult == nil {
		s.sealed = true
		return contracts.OperatorCommandResultV1{}, false, ErrCorrupt
	}
	result := *committed.Commit.CommandResult
	result.DecisionIDs = make([]string, len(committed.Commit.CommandResult.DecisionIDs))
	copy(result.DecisionIDs, committed.Commit.CommandResult.DecisionIDs)
	return result, true, nil
}

// LookupPolicyCommand resolves the complete original canonical command commit
// for application-level idempotency after restart.
func (s *StoreV2) LookupPolicyCommand(commandID string) (contracts.PolicyCommitV2, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resultHash, ok := s.state.CommandResults[commandID]
	if !ok {
		return contracts.PolicyCommitV2{}, false, nil
	}
	locator, ok := s.state.CommandLocators[commandID]
	if !ok {
		s.sealed = true
		return contracts.PolicyCommitV2{}, false, ErrCorrupt
	}
	committed, err := s.readLocator(locator, resultHash)
	if err != nil {
		s.sealed = true
		return contracts.PolicyCommitV2{}, false, err
	}
	return committed.Commit, true, nil
}

func (s *StoreV2) rollback(prior int64, cause error) error {
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

func (s *StoreV2) ensureSegment(sequence uint64, frameSize int64) error {
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
	name := fmt.Sprintf("%020d.pcl2", sequence)
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

func (s *StoreV2) WriteCheckpoint(checkpoint contracts.PolicyCheckpointV2) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSealed
	}
	commit, ok, scanErr := s.commitAt(checkpoint.CommitSequence)
	if scanErr != nil {
		return scanErr
	}
	if !ok || checkpoint.ValidateAgainstCommit(commit.Commit, commit.Hash) != nil || s.validateCheckpointLocators(checkpoint) != nil {
		return ErrInvalidCheckpoint
	}
	payload, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return fmt.Errorf("%w: canonical", ErrInvalidCheckpoint)
	}
	tmpPath := filepath.Join(s.dir, ".checkpoint.v2.json.tmp")
	finalPath := filepath.Join(s.dir, "checkpoint.v2.json")
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
	if n, writeErr := s.hooks.Write(tmp, payload); writeErr != nil || n != len(payload) {
		return errors.New("checkpoint write failed")
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

// LoadCheckpoint treats an unreadable/noncanonical/semantically corrupt cache
// as absent, but a structurally present locator table that is missing,
// duplicated, substituted, or frame-mismatched fails closed. Later frames are
// delivered one at a time so a stale checkpoint cannot materialize its entire
// continuation in memory.
func (s *StoreV2) LoadCheckpoint(visit func(CommittedV2) error) (*contracts.PolicyCheckpointV2, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if visit == nil {
		return nil, errors.New("v2 checkpoint continuation visitor required")
	}
	payload, err := os.ReadFile(filepath.Join(s.dir, "checkpoint.v2.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint contracts.PolicyCheckpointV2
	if contracts.DecodeStrict(payload, &checkpoint) != nil {
		return nil, nil
	}
	canonical, err := contracts.MarshalCanonical(checkpoint)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, nil
	}
	if !locatorShapeMatches(checkpoint) {
		return nil, ErrInvalidCheckpoint
	}
	commit, ok, scanErr := s.commitAt(checkpoint.CommitSequence)
	if scanErr != nil {
		return nil, scanErr
	}
	if !ok || checkpoint.ValidateAgainstCommit(commit.Commit, commit.Hash) != nil {
		return nil, nil
	}
	if err := s.validateCheckpointLocators(checkpoint); err != nil {
		return nil, ErrInvalidCheckpoint
	}
	if err := s.scanFrames(func(committed CommittedV2) error {
		if committed.Commit.CommitSequence <= checkpoint.CommitSequence {
			return nil
		}
		return visit(committed)
	}); err != nil {
		return nil, err
	}
	copyCheckpoint := cloneCheckpointV2(checkpoint)
	return &copyCheckpoint, nil
}

// VisitAll streams the complete committed history without retaining payloads.
// It is used when the checkpoint cache is missing or corrupt.
func (s *StoreV2) VisitAll(visit func(CommittedV2) error) error {
	if visit == nil {
		return errors.New("v2 replay visitor required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanFrames(visit)
}

func locatorShapeMatches(checkpoint contracts.PolicyCheckpointV2) bool {
	if len(checkpoint.CommandResults) != len(checkpoint.CommandLocators) {
		return false
	}
	seen := make(map[string]bool, len(checkpoint.CommandLocators))
	for i, locator := range checkpoint.CommandLocators {
		if locator.CommandID == "" || seen[locator.CommandID] || locator.CommandID != checkpoint.CommandResults[i].CommandID {
			return false
		}
		seen[locator.CommandID] = true
	}
	return true
}

func (s *StoreV2) validateCheckpointLocators(checkpoint contracts.PolicyCheckpointV2) error {
	if !locatorShapeMatches(checkpoint) {
		return ErrInvalidCheckpoint
	}
	expected := make([]contracts.PolicyCommandResultRefV2, 0, len(checkpoint.CommandResults))
	err := s.scanFrames(func(committed CommittedV2) error {
		if committed.Commit.CommitSequence > checkpoint.CommitSequence {
			return io.EOF
		}
		if committed.Commit.CommandResult != nil {
			hash, err := contracts.CanonicalSHA256(*committed.Commit.CommandResult)
			if err != nil {
				return err
			}
			expected = append(expected, contracts.PolicyCommandResultRefV2{CommandID: committed.Commit.CommandID, ResultSHA256: hash})
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].CommandID < expected[j].CommandID })
	if len(expected) != len(checkpoint.CommandResults) {
		return ErrInvalidCheckpoint
	}
	for i := range expected {
		if expected[i] != checkpoint.CommandResults[i] {
			return ErrInvalidCheckpoint
		}
	}
	for i, locator := range checkpoint.CommandLocators {
		if locator.CommitSequence > checkpoint.CommitSequence {
			return ErrInvalidCheckpoint
		}
		if _, err := s.readLocator(locator, checkpoint.CommandResults[i].ResultSHA256); err != nil {
			return err
		}
	}
	return nil
}

func (s *StoreV2) readLocator(locator contracts.PolicyCommandLocatorV2, resultHash string) (CommittedV2, error) {
	if filepath.Base(locator.SegmentID) != locator.SegmentID || !strings.HasSuffix(locator.SegmentID, ".pcl2") || locator.FrameOffset < 0 {
		return CommittedV2{}, ErrCorrupt
	}
	path := filepath.Join(s.dir, locator.SegmentID)
	f, err := os.Open(path)
	if err != nil {
		return CommittedV2{}, fmt.Errorf("%w: locator open", ErrCorrupt)
	}
	defer f.Close()
	if _, err := f.Seek(locator.FrameOffset, io.SeekStart); err != nil {
		return CommittedV2{}, fmt.Errorf("%w: locator seek", ErrCorrupt)
	}
	committed, _, err := readFrameV2(f, locator.SegmentID, locator.FrameOffset)
	if err != nil || committed.Hash != locator.FrameSHA256 || committed.Commit.CommitSequence != locator.CommitSequence || committed.Commit.CommandID != locator.CommandID || committed.Commit.CommandResult == nil {
		return CommittedV2{}, fmt.Errorf("%w: locator frame mismatch", ErrCorrupt)
	}
	gotResultHash, err := contracts.CanonicalSHA256(*committed.Commit.CommandResult)
	if err != nil || gotResultHash != resultHash {
		return CommittedV2{}, fmt.Errorf("%w: locator semantic result mismatch", ErrCorrupt)
	}
	committed.Locator = locator
	return committed, nil
}

func (s *StoreV2) commitAt(sequence uint64) (CommittedV2, bool, error) {
	var found CommittedV2
	err := s.scanFrames(func(commit CommittedV2) error {
		if commit.Commit.CommitSequence == sequence {
			found = commit
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return CommittedV2{}, false, err
	}
	return found, found.Commit.CommitSequence != 0, nil
}

func (s *StoreV2) recover() (StateV2, error) {
	state := StateV2{
		SessionID: s.sessionID, LineageManifestID: s.manifestID,
		CommandResults: map[string]string{}, CommandLocators: map[string]contracts.PolicyCommandLocatorV2{},
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return state, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl2") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) > s.maxSessionSegments {
		return StateV2{}, capacityError("recovered_segment_count")
	}
	s.segmentCount = len(names)
	for index, name := range names {
		path := filepath.Join(s.dir, name)
		if err := s.recoverSegment(path, index == len(names)-1, &state); err != nil {
			return StateV2{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return StateV2{}, err
		}
		if info.Size() > s.maxSessionBytes-s.totalBytes {
			return StateV2{}, capacityError("recovered_aggregate_bytes")
		}
		s.totalBytes += info.Size()
	}
	return state, nil
}

func (s *StoreV2) recoverSegment(path string, last bool, state *StateV2) error {
	f, err := s.hooks.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > s.segmentLimit {
		return fmt.Errorf("%w: segment bound exceeded", ErrCorrupt)
	}
	offset := int64(0)
	for offset < info.Size() {
		remaining := info.Size() - offset
		if remaining < lengthBytes {
			return s.recoverTail(f, last, offset)
		}
		var length [lengthBytes]byte
		if _, err := f.ReadAt(length[:], offset); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint32(length[:]))
		if n <= 0 || n > contracts.MaxPolicyCommitBytes {
			return fmt.Errorf("%w: invalid v2 length", ErrCorrupt)
		}
		frameSize := int64(lengthBytes + n + hashBytes + markerBytes)
		if remaining < frameSize {
			return s.recoverTail(f, last, offset)
		}
		reader := io.NewSectionReader(f, offset, frameSize)
		committed, consumed, err := readFrameV2(reader, filepath.Base(path), offset)
		if err != nil || consumed != frameSize {
			return fmt.Errorf("%w: invalid terminated v2 frame", ErrCorrupt)
		}
		if committed.Commit.SessionID != s.sessionID || committed.Commit.LineageManifestID != s.manifestID || committed.Commit.LineageManifestSHA256 != s.manifestID {
			return fmt.Errorf("%w: v2 lineage mismatch", ErrCorrupt)
		}
		if err := validateNextV2(*state, committed.Commit); err != nil {
			return fmt.Errorf("%w: v2 sequence", ErrCorrupt)
		}
		if committed.Commit.CommandID != "" {
			if _, duplicate := state.CommandResults[committed.Commit.CommandID]; duplicate || len(state.CommandResults) >= contracts.MaxCheckpointCommandResults {
				return fmt.Errorf("%w: duplicate or over-limit command", ErrCorrupt)
			}
			committed.Locator = contracts.PolicyCommandLocatorV2{
				CommandID: committed.Commit.CommandID, SegmentID: filepath.Base(path), FrameOffset: offset,
				CommitSequence: committed.Commit.CommitSequence, FrameSHA256: committed.Hash,
			}
		}
		if err := verifyCommitV2(committed.Commit, *s.verifier); err != nil {
			return fmt.Errorf("%w: recovery activation: %v", ErrCorrupt, err)
		}
		applyV2(state, committed)
		offset += frameSize
	}
	return nil
}

func readFrameV2(r io.Reader, segment string, offset int64) (CommittedV2, int64, error) {
	var length [lengthBytes]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return CommittedV2{}, 0, err
	}
	n := int(binary.BigEndian.Uint32(length[:]))
	if n <= 0 || n > contracts.MaxPolicyCommitBytes {
		return CommittedV2{}, 0, ErrCorrupt
	}
	payload := make([]byte, n)
	storedHash := make([]byte, hashBytes)
	marker := make([]byte, markerBytes)
	if _, err := io.ReadFull(r, payload); err != nil {
		return CommittedV2{}, 0, err
	}
	if _, err := io.ReadFull(r, storedHash); err != nil {
		return CommittedV2{}, 0, err
	}
	if _, err := io.ReadFull(r, marker); err != nil {
		return CommittedV2{}, 0, err
	}
	sum := sha256.Sum256(payload)
	if !bytes.Equal(storedHash, sum[:]) || !bytes.Equal(marker, commitMarkerV2[:]) {
		return CommittedV2{}, 0, ErrCorrupt
	}
	var commit contracts.PolicyCommitV2
	if err := contracts.DecodeStrict(payload, &commit); err != nil || commit.Validate() != nil {
		return CommittedV2{}, 0, ErrCorrupt
	}
	canonical, err := contracts.MarshalCanonical(commit)
	if err != nil || !bytes.Equal(canonical, payload) {
		return CommittedV2{}, 0, ErrCorrupt
	}
	hash := hex.EncodeToString(sum[:])
	return CommittedV2{Commit: commit, Payload: payload, Hash: hash}, int64(lengthBytes + n + hashBytes + markerBytes), nil
}

func (s *StoreV2) recoverTail(f *os.File, last bool, offset int64) error {
	if !last {
		return fmt.Errorf("%w: incomplete nonfinal v2 segment", ErrCorrupt)
	}
	if err := s.hooks.Truncate(f, offset); err != nil {
		return fmt.Errorf("%w: v2 recovery truncate", ErrSealed)
	}
	if err := s.hooks.SyncFile(f); err != nil {
		return fmt.Errorf("%w: v2 recovery sync", ErrSealed)
	}
	info, err := f.Stat()
	if err != nil || info.Size() != offset {
		return fmt.Errorf("%w: v2 recovery unverified", ErrSealed)
	}
	return nil
}

func (s *StoreV2) reopenLastSegment() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl2") {
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

func (s *StoreV2) Close() error {
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

func encodeFrameV2(payload []byte) ([]byte, [32]byte) {
	sum := sha256.Sum256(payload)
	frame := make([]byte, lengthBytes+len(payload)+hashBytes+markerBytes)
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[lengthBytes:], payload)
	copy(frame[lengthBytes+len(payload):], sum[:])
	copy(frame[lengthBytes+len(payload)+hashBytes:], commitMarkerV2[:])
	return frame, sum
}

func validateNextV2(state StateV2, commit contracts.PolicyCommitV2) error {
	if commit.CommitSequence != state.CommitSequence+1 || commit.PriorPolicyRevision != state.PolicyRevision || commit.ResultingObservationSequence < state.LastObservationSequence || commit.ResultingPolicyTimeMS < state.LastPolicyTimeMS {
		return ErrSequence
	}
	if state.CommitSequence > 0 && commit.PriorStateHash != state.StateHash {
		return ErrSequence
	}
	return nil
}

func applyV2(state *StateV2, committed CommittedV2) {
	state.CommitSequence = committed.Commit.CommitSequence
	state.PolicyRevision = committed.Commit.ResultingPolicyRevision
	state.StateHash = committed.Commit.ResultingStateHash
	state.LastObservationSequence = committed.Commit.ResultingObservationSequence
	state.LastPolicyTimeMS = committed.Commit.ResultingPolicyTimeMS
	if committed.Commit.CommandResult != nil {
		resultHash, _ := contracts.CanonicalSHA256(*committed.Commit.CommandResult)
		state.CommandResults[committed.Commit.CommandID] = resultHash
		state.CommandLocators[committed.Commit.CommandID] = committed.Locator
	}
}

func cloneCommittedV2(committed CommittedV2) CommittedV2 {
	committed.Payload = append([]byte(nil), committed.Payload...)
	var decoded contracts.PolicyCommitV2
	if err := contracts.DecodeStrict(committed.Payload, &decoded); err == nil {
		committed.Commit = decoded
	}
	return committed
}

func cloneStateV2(state StateV2) StateV2 {
	copyState := state
	copyState.CommandResults = make(map[string]string, len(state.CommandResults))
	for key, value := range state.CommandResults {
		copyState.CommandResults[key] = value
	}
	copyState.CommandLocators = make(map[string]contracts.PolicyCommandLocatorV2, len(state.CommandLocators))
	for key, value := range state.CommandLocators {
		copyState.CommandLocators[key] = value
	}
	copyState.Commits = nil
	return copyState
}

func (s *StoreV2) scanFrames(visit func(CommittedV2) error) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl2") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := os.Open(filepath.Join(s.dir, name))
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		for offset := int64(0); offset < info.Size(); {
			committed, consumed, err := readFrameV2(f, name, offset)
			if err != nil {
				_ = f.Close()
				return err
			}
			if committed.Commit.CommandID != "" {
				committed.Locator = contracts.PolicyCommandLocatorV2{CommandID: committed.Commit.CommandID, SegmentID: name, FrameOffset: offset, CommitSequence: committed.Commit.CommitSequence, FrameSHA256: committed.Hash}
			}
			if err := visit(committed); err != nil {
				_ = f.Close()
				return err
			}
			offset += consumed
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func cloneCheckpointV2(checkpoint contracts.PolicyCheckpointV2) contracts.PolicyCheckpointV2 {
	payload, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return checkpoint
	}
	var clone contracts.PolicyCheckpointV2
	if err := contracts.DecodeStrict(payload, &clone); err != nil {
		return checkpoint
	}
	return clone
}

type ReplayVerifierV2 struct {
	VerifyObservation func(contracts.PolicyCommitV2) error
	VerifyCommand     func(contracts.PolicyCommitV2) error
	Reevaluate        func(contracts.PolicyCommitV2) error
}

// VerifyReplayV2 enforces source verification before semantic re-evaluation.
// The application supplies lineage-bound raw projection and pure-engine
// callbacks; the durable adapter never imports capture or policy evaluation.
func VerifyReplayV2(commits []contracts.PolicyCommitV2, verifier ReplayVerifierV2) error {
	for _, commit := range commits {
		if err := verifyCommitV2(commit, verifier); err != nil {
			return err
		}
	}
	return nil
}

func verifyCommitV2(commit contracts.PolicyCommitV2, verifier ReplayVerifierV2) error {
	if err := commit.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if commit.ObservationSequence != 0 {
		if verifier.VerifyObservation == nil {
			return errors.New("v2 replay observation verifier required")
		}
		if err := verifier.VerifyObservation(commit); err != nil {
			return fmt.Errorf("observation source verification: %w", err)
		}
	} else {
		if verifier.VerifyCommand == nil {
			return errors.New("v2 replay command verifier required")
		}
		if err := verifier.VerifyCommand(commit); err != nil {
			return fmt.Errorf("command source verification: %w", err)
		}
	}
	if verifier.Reevaluate == nil {
		return errors.New("v2 replay evaluator required")
	}
	if err := verifier.Reevaluate(commit); err != nil {
		return fmt.Errorf("v2 replay mismatch: %w", err)
	}
	return nil
}
