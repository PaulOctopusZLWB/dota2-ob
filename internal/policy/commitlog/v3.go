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
	commitMarkerV3 = [markerBytes]byte{'P', 'C', 'O', 'M', 'M', 'I', 'T', 3}
)

type V3Option func(*StoreV3)

func WithV3Hooks(h Hooks) V3Option { return func(s *StoreV3) { s.hooks = mergeHooks(h) } }

// WithV3ReplayVerifier is mandatory for OpenV3. It is an application-owned
// composition boundary that binds recovered frames to immutable source inputs
// and byte-equivalent pure semantic replay before the store becomes writable.
func WithV3ReplayVerifier(verifier ReplayVerifierV3) V3Option {
	return func(s *StoreV3) { s.verifier = &verifier }
}

func WithV3SegmentLimit(n int64) V3Option {
	return func(s *StoreV3) {
		if n > 0 {
			s.segmentLimit = n
		}
	}
}

func WithV3SessionLimits(bytes int64, segments int) V3Option {
	return func(s *StoreV3) {
		if bytes > 0 {
			s.maxSessionBytes = bytes
		}
		if segments > 0 {
			s.maxSessionSegments = segments
		}
	}
}

type CommittedV3 struct {
	Commit  contracts.PolicyCommitV3
	Payload []byte
	Hash    string
	Locator contracts.PolicyCommandLocatorV3
}

type StateV3 struct {
	SessionID               string
	LineageManifestID       string
	CommitSequence          uint64
	PolicyRevision          uint64
	StateHash               string
	LastObservationSequence uint64
	LastPolicyTimeMS        int64
	CommandResults          map[string]string
	CommandLocators         map[string]contracts.PolicyCommandLocatorV3
	// Commits is retained for source compatibility and is always empty.
	// Recovery verifies frames as a stream instead of retaining payloads.
	Commits []CommittedV3
}

type StoreV3 struct {
	mu                 sync.Mutex
	dir                string
	sessionID          string
	manifestID         string
	bindingID          string
	hooks              Hooks
	segmentLimit       int64
	maxSessionBytes    int64
	maxSessionSegments int
	file               *os.File
	fileSize           int64
	totalBytes         int64
	segmentCount       int
	state              StateV3
	verifier           *ReplayVerifierV3
	sealed             bool
	closed             bool
}

// OpenV3 seals or verifies the immutable lineage manifest before opening any
// production V3 policy frames. A directory containing V1 frames is rejected.
func OpenV3(root, sessionID string, binding contracts.HistoryAvailabilityBindingV1, manifest contracts.PolicyLineageManifestV3, opts ...V3Option) (*StoreV3, StateV3, error) {
	if strings.TrimSpace(root) == "" || !safeID(sessionID) || manifest.SessionID != sessionID {
		return nil, StateV3{}, fmt.Errorf("%w: root/session mismatch", ErrLineage)
	}
	manifestID, err := manifest.ContentID()
	if err != nil {
		return nil, StateV3{}, fmt.Errorf("%w: %v", ErrLineage, err)
	}
	bindingID, err := binding.ContentID()
	if err != nil || binding.Mode != contracts.HistoryModeNoGo || manifest.HistoryAvailabilityBindingID != bindingID || manifest.HistoryAvailabilityBindingSHA256 != bindingID {
		return nil, StateV3{}, fmt.Errorf("%w: history binding mismatch", ErrLineage)
	}
	dir := filepath.Join(root, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, StateV3{}, fmt.Errorf("create policy log: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, StateV3{}, fmt.Errorf("protect policy log: %w", err)
	}
	s := &StoreV3{
		dir: dir, sessionID: sessionID, manifestID: manifestID, bindingID: bindingID, hooks: defaultHooks(),
		segmentLimit: MaxSegmentBytes, maxSessionBytes: MaxSessionBytes, maxSessionSegments: MaxSessionSegments,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.verifier == nil || s.verifier.VerifyObservation == nil || s.verifier.VerifyCommand == nil || s.verifier.Reevaluate == nil {
		return nil, StateV3{}, errors.New("v2 recovery verifier and re-evaluator required")
	}
	if err := s.rejectMixedFrames(); err != nil {
		return nil, StateV3{}, err
	}
	if err := s.sealArtifact("history-binding.v1.json", binding); err != nil {
		return nil, StateV3{}, err
	}
	if err := s.sealManifest(manifest); err != nil {
		return nil, StateV3{}, err
	}
	state, err := s.recover()
	if err != nil {
		return nil, StateV3{}, err
	}
	// A prior process may have renamed a segment and then lost the directory
	// sync result. Re-syncing the parent after structural and semantic recovery
	// proves every recovered segment entry durable before one is reopened.
	if s.segmentCount > 0 {
		if err := s.hooks.SyncDir(s.dir); err != nil {
			return nil, StateV3{}, fmt.Errorf("%w: recovered segment directory sync: %v", ErrSealed, err)
		}
	}
	s.state = state
	if err := s.reopenLastSegment(); err != nil {
		return nil, StateV3{}, err
	}
	return s, cloneStateV3(state), nil
}

func (s *StoreV3) rejectMixedFrames() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".pcl") || strings.HasSuffix(entry.Name(), ".pcl2")) {
			return ErrMixedLineage
		}
	}
	return nil
}

func (s *StoreV3) sealArtifact(name string, value any) error {
	payload, err := contracts.MarshalCanonical(value)
	if err != nil {
		return fmt.Errorf("%w: canonical artifact", ErrLineage)
	}
	final := filepath.Join(s.dir, name)
	if existing, readErr := os.ReadFile(final); readErr == nil {
		if !bytes.Equal(existing, payload) {
			return fmt.Errorf("%w: artifact substitution", ErrLineage)
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("%w: read artifact: %v", ErrLineage, readErr)
	}
	tmpPath := filepath.Join(s.dir, "."+name+".tmp")
	tmp, err := s.hooks.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create artifact: %v", ErrLineage, err)
	}
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if n, writeErr := s.hooks.Write(tmp, payload); writeErr != nil || n != len(payload) {
		return fmt.Errorf("%w: write artifact", ErrLineage)
	}
	if err := s.hooks.SyncFile(tmp); err != nil {
		return fmt.Errorf("%w: sync artifact: %v", ErrLineage, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close artifact: %v", ErrLineage, err)
	}
	if err := s.hooks.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("%w: rename artifact: %v", ErrLineage, err)
	}
	keep = true
	if err := os.Chmod(final, 0o600); err != nil {
		return fmt.Errorf("%w: protect artifact: %v", ErrLineage, err)
	}
	if err := s.hooks.SyncDir(s.dir); err != nil {
		return fmt.Errorf("%w: sync artifact directory: %v", ErrLineage, err)
	}
	return nil
}

func (s *StoreV3) sealManifest(manifest contracts.PolicyLineageManifestV3) error {
	payload, err := contracts.MarshalCanonical(manifest)
	if err != nil {
		return fmt.Errorf("%w: canonical manifest", ErrLineage)
	}
	final := filepath.Join(s.dir, "lineage.v3.json")
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
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl3") {
			return fmt.Errorf("%w: manifest missing for existing frames", ErrLineage)
		}
	}
	tmpPath := filepath.Join(s.dir, ".lineage.v3.json.tmp")
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

func (s *StoreV3) Append(commit contracts.PolicyCommitV3) (CommittedV3, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.closed {
		return CommittedV3{}, ErrSealed
	}
	if err := commit.Validate(); err != nil || commit.SessionID != s.sessionID || commit.LineageManifestID != s.manifestID || commit.LineageManifestSHA256 != s.manifestID {
		return CommittedV3{}, fmt.Errorf("%w: v2 contract or lineage mismatch", ErrInvalidCommit)
	}
	if commit.CommandID != "" {
		if locator, ok := s.state.CommandLocators[commit.CommandID]; ok {
			stored, err := s.readLocator(locator, s.state.CommandResults[commit.CommandID])
			if err != nil {
				s.sealed = true
				return CommittedV3{}, err
			}
			return stored, nil
		}
		if len(s.state.CommandResults) >= contracts.MaxCheckpointCommandResults {
			return CommittedV3{}, ErrCommandLimit
		}
	}
	if err := validateNextV3(s.state, commit); err != nil {
		return CommittedV3{}, err
	}
	payload, err := contracts.MarshalCanonical(commit)
	if err != nil || len(payload) > contracts.MaxPolicyCommitBytes {
		return CommittedV3{}, fmt.Errorf("%w: canonical payload", ErrInvalidCommit)
	}
	frame, sum := encodeFrameV3(payload)
	if err := s.ensureSegment(commit.CommitSequence, int64(len(frame))); err != nil {
		s.sealed = true
		return CommittedV3{}, fmt.Errorf("%w: segment: %w", ErrSealed, err)
	}
	prior := s.fileSize
	if err := s.hooks.Interrupt("before_append"); err != nil {
		return CommittedV3{}, s.rollback(prior, err)
	}
	n, writeErr := s.hooks.Write(s.file, frame)
	if writeErr != nil || n != len(frame) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return CommittedV3{}, s.rollback(prior, writeErr)
	}
	if err := s.hooks.Interrupt("before_sync"); err != nil {
		return CommittedV3{}, s.rollback(prior, err)
	}
	if err := s.hooks.SyncFile(s.file); err != nil {
		return CommittedV3{}, s.rollback(prior, err)
	}
	s.fileSize += int64(len(frame))
	s.totalBytes += int64(len(frame))
	var stored contracts.PolicyCommitV3
	if err := contracts.DecodeStrict(payload, &stored); err != nil {
		s.sealed = true
		return CommittedV3{}, fmt.Errorf("%w: internal canonical decode", ErrSealed)
	}
	hash := hex.EncodeToString(sum[:])
	locator := contracts.PolicyCommandLocatorV3{}
	if stored.CommandID != "" {
		locator = contracts.PolicyCommandLocatorV3{
			CommandID: stored.CommandID, SegmentID: filepath.Base(s.file.Name()), FrameOffset: prior,
			CommitSequence: stored.CommitSequence, FrameSHA256: hash,
		}
	}
	committed := CommittedV3{Commit: stored, Payload: append([]byte(nil), payload...), Hash: hash, Locator: locator}
	applyV3(&s.state, committed)
	return cloneCommittedV3(committed), nil
}

// AppendPolicyCommit adapts StoreV3 to policy.CommitAppender while preserving
// Append's synchronous frame-and-sync durability boundary.
func (s *StoreV3) AppendPolicyCommit(commit contracts.PolicyCommitV3) error {
	_, err := s.Append(commit)
	return err
}

// LookupCommand resolves an admitted duplicate directly from its cache-only
// locator and revalidates the complete frame against the semantic result hash.
func (s *StoreV3) LookupCommand(commandID string) (contracts.OperatorCommandResultV1, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.closed {
		return contracts.OperatorCommandResultV1{}, false, ErrSealed
	}
	if !contracts.ValidPolicyIdentifierV3(commandID) {
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
func (s *StoreV3) LookupPolicyCommand(commandID string) (contracts.PolicyCommitV3, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resultHash, ok := s.state.CommandResults[commandID]
	if !ok {
		return contracts.PolicyCommitV3{}, false, nil
	}
	locator, ok := s.state.CommandLocators[commandID]
	if !ok {
		s.sealed = true
		return contracts.PolicyCommitV3{}, false, ErrCorrupt
	}
	committed, err := s.readLocator(locator, resultHash)
	if err != nil {
		s.sealed = true
		return contracts.PolicyCommitV3{}, false, err
	}
	return committed.Commit, true, nil
}

func (s *StoreV3) rollback(prior int64, cause error) error {
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

func (s *StoreV3) ensureSegment(sequence uint64, frameSize int64) error {
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
	name := fmt.Sprintf("%020d.pcl3", sequence)
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

func (s *StoreV3) WriteCheckpoint(checkpoint contracts.PolicyCheckpointV3) error {
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
	tmpPath := filepath.Join(s.dir, ".checkpoint.v3.json.tmp")
	finalPath := filepath.Join(s.dir, "checkpoint.v3.json")
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
func (s *StoreV3) LoadCheckpoint(visit func(CommittedV3) error) (*contracts.PolicyCheckpointV3, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if visit == nil {
		return nil, errors.New("v2 checkpoint continuation visitor required")
	}
	payload, err := os.ReadFile(filepath.Join(s.dir, "checkpoint.v3.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint contracts.PolicyCheckpointV3
	if contracts.DecodeStrict(payload, &checkpoint) != nil {
		return nil, nil
	}
	canonical, err := contracts.MarshalCanonical(checkpoint)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, nil
	}
	if !locatorShapeMatchesV3(checkpoint) {
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
	if err := s.scanFrames(func(committed CommittedV3) error {
		if committed.Commit.CommitSequence <= checkpoint.CommitSequence {
			return nil
		}
		return visit(committed)
	}); err != nil {
		return nil, err
	}
	copyCheckpoint := cloneCheckpointV3(checkpoint)
	return &copyCheckpoint, nil
}

// VisitAll streams the complete committed history without retaining payloads.
// It is used when the checkpoint cache is missing or corrupt.
func (s *StoreV3) VisitAll(visit func(CommittedV3) error) error {
	if visit == nil {
		return errors.New("v2 replay visitor required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanFrames(visit)
}

func locatorShapeMatchesV3(checkpoint contracts.PolicyCheckpointV3) bool {
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

func (s *StoreV3) validateCheckpointLocators(checkpoint contracts.PolicyCheckpointV3) error {
	if !locatorShapeMatchesV3(checkpoint) {
		return ErrInvalidCheckpoint
	}
	expected := make([]contracts.PolicyCommandResultRefV3, 0, len(checkpoint.CommandResults))
	err := s.scanFrames(func(committed CommittedV3) error {
		if committed.Commit.CommitSequence > checkpoint.CommitSequence {
			return io.EOF
		}
		if committed.Commit.CommandResult != nil {
			hash, err := contracts.CanonicalSHA256(*committed.Commit.CommandResult)
			if err != nil {
				return err
			}
			expected = append(expected, contracts.PolicyCommandResultRefV3{CommandID: committed.Commit.CommandID, ResultSHA256: hash})
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

func (s *StoreV3) readLocator(locator contracts.PolicyCommandLocatorV3, resultHash string) (CommittedV3, error) {
	if filepath.Base(locator.SegmentID) != locator.SegmentID || !strings.HasSuffix(locator.SegmentID, ".pcl3") || locator.FrameOffset < 0 {
		return CommittedV3{}, ErrCorrupt
	}
	path := filepath.Join(s.dir, locator.SegmentID)
	f, err := os.Open(path)
	if err != nil {
		return CommittedV3{}, fmt.Errorf("%w: locator open", ErrCorrupt)
	}
	defer f.Close()
	if _, err := f.Seek(locator.FrameOffset, io.SeekStart); err != nil {
		return CommittedV3{}, fmt.Errorf("%w: locator seek", ErrCorrupt)
	}
	committed, _, err := readFrameV3(f, locator.SegmentID, locator.FrameOffset)
	if err != nil || committed.Hash != locator.FrameSHA256 || committed.Commit.CommitSequence != locator.CommitSequence || committed.Commit.CommandID != locator.CommandID || committed.Commit.CommandResult == nil {
		return CommittedV3{}, fmt.Errorf("%w: locator frame mismatch", ErrCorrupt)
	}
	gotResultHash, err := contracts.CanonicalSHA256(*committed.Commit.CommandResult)
	if err != nil || gotResultHash != resultHash {
		return CommittedV3{}, fmt.Errorf("%w: locator semantic result mismatch", ErrCorrupt)
	}
	committed.Locator = locator
	return committed, nil
}

func (s *StoreV3) commitAt(sequence uint64) (CommittedV3, bool, error) {
	var found CommittedV3
	err := s.scanFrames(func(commit CommittedV3) error {
		if commit.Commit.CommitSequence == sequence {
			found = commit
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return CommittedV3{}, false, err
	}
	return found, found.Commit.CommitSequence != 0, nil
}

func (s *StoreV3) recover() (StateV3, error) {
	state := StateV3{
		SessionID: s.sessionID, LineageManifestID: s.manifestID,
		CommandResults: map[string]string{}, CommandLocators: map[string]contracts.PolicyCommandLocatorV3{},
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return state, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl3") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) > s.maxSessionSegments {
		return StateV3{}, capacityError("recovered_segment_count")
	}
	s.segmentCount = len(names)
	for index, name := range names {
		path := filepath.Join(s.dir, name)
		if err := s.recoverSegment(path, index == len(names)-1, &state); err != nil {
			return StateV3{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return StateV3{}, err
		}
		if info.Size() > s.maxSessionBytes-s.totalBytes {
			return StateV3{}, capacityError("recovered_aggregate_bytes")
		}
		s.totalBytes += info.Size()
	}
	return state, nil
}

func (s *StoreV3) recoverSegment(path string, last bool, state *StateV3) error {
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
		committed, consumed, err := readFrameV3(reader, filepath.Base(path), offset)
		if err != nil || consumed != frameSize {
			return fmt.Errorf("%w: invalid terminated v2 frame", ErrCorrupt)
		}
		if committed.Commit.SessionID != s.sessionID || committed.Commit.LineageManifestID != s.manifestID || committed.Commit.LineageManifestSHA256 != s.manifestID {
			return fmt.Errorf("%w: v2 lineage mismatch", ErrCorrupt)
		}
		if err := validateNextV3(*state, committed.Commit); err != nil {
			return fmt.Errorf("%w: v2 sequence", ErrCorrupt)
		}
		if committed.Commit.CommandID != "" {
			if _, duplicate := state.CommandResults[committed.Commit.CommandID]; duplicate || len(state.CommandResults) >= contracts.MaxCheckpointCommandResults {
				return fmt.Errorf("%w: duplicate or over-limit command", ErrCorrupt)
			}
			committed.Locator = contracts.PolicyCommandLocatorV3{
				CommandID: committed.Commit.CommandID, SegmentID: filepath.Base(path), FrameOffset: offset,
				CommitSequence: committed.Commit.CommitSequence, FrameSHA256: committed.Hash,
			}
		}
		if err := verifyCommitV3(committed.Commit, *s.verifier); err != nil {
			return fmt.Errorf("%w: recovery activation: %v", ErrCorrupt, err)
		}
		applyV3(state, committed)
		offset += frameSize
	}
	return nil
}

func readFrameV3(r io.Reader, segment string, offset int64) (CommittedV3, int64, error) {
	var length [lengthBytes]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return CommittedV3{}, 0, err
	}
	n := int(binary.BigEndian.Uint32(length[:]))
	if n <= 0 || n > contracts.MaxPolicyCommitBytes {
		return CommittedV3{}, 0, ErrCorrupt
	}
	payload := make([]byte, n)
	storedHash := make([]byte, hashBytes)
	marker := make([]byte, markerBytes)
	if _, err := io.ReadFull(r, payload); err != nil {
		return CommittedV3{}, 0, err
	}
	if _, err := io.ReadFull(r, storedHash); err != nil {
		return CommittedV3{}, 0, err
	}
	if _, err := io.ReadFull(r, marker); err != nil {
		return CommittedV3{}, 0, err
	}
	sum := sha256.Sum256(payload)
	if !bytes.Equal(storedHash, sum[:]) || !bytes.Equal(marker, commitMarkerV3[:]) {
		return CommittedV3{}, 0, ErrCorrupt
	}
	var commit contracts.PolicyCommitV3
	if err := contracts.DecodeStrict(payload, &commit); err != nil || commit.Validate() != nil {
		return CommittedV3{}, 0, ErrCorrupt
	}
	canonical, err := contracts.MarshalCanonical(commit)
	if err != nil || !bytes.Equal(canonical, payload) {
		return CommittedV3{}, 0, ErrCorrupt
	}
	hash := hex.EncodeToString(sum[:])
	return CommittedV3{Commit: commit, Payload: payload, Hash: hash}, int64(lengthBytes + n + hashBytes + markerBytes), nil
}

func (s *StoreV3) recoverTail(f *os.File, last bool, offset int64) error {
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

func (s *StoreV3) reopenLastSegment() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl3") {
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

func (s *StoreV3) Close() error {
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

func encodeFrameV3(payload []byte) ([]byte, [32]byte) {
	sum := sha256.Sum256(payload)
	frame := make([]byte, lengthBytes+len(payload)+hashBytes+markerBytes)
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[lengthBytes:], payload)
	copy(frame[lengthBytes+len(payload):], sum[:])
	copy(frame[lengthBytes+len(payload)+hashBytes:], commitMarkerV3[:])
	return frame, sum
}

func validateNextV3(state StateV3, commit contracts.PolicyCommitV3) error {
	if commit.CommitSequence != state.CommitSequence+1 || commit.PriorPolicyRevision != state.PolicyRevision || commit.ResultingObservationSequence < state.LastObservationSequence || commit.ResultingPolicyTimeMS < state.LastPolicyTimeMS {
		return ErrSequence
	}
	if state.CommitSequence > 0 && commit.PriorStateHash != state.StateHash {
		return ErrSequence
	}
	return nil
}

func applyV3(state *StateV3, committed CommittedV3) {
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

func cloneCommittedV3(committed CommittedV3) CommittedV3 {
	committed.Payload = append([]byte(nil), committed.Payload...)
	var decoded contracts.PolicyCommitV3
	if err := contracts.DecodeStrict(committed.Payload, &decoded); err == nil {
		committed.Commit = decoded
	}
	return committed
}

func cloneStateV3(state StateV3) StateV3 {
	copyState := state
	copyState.CommandResults = make(map[string]string, len(state.CommandResults))
	for key, value := range state.CommandResults {
		copyState.CommandResults[key] = value
	}
	copyState.CommandLocators = make(map[string]contracts.PolicyCommandLocatorV3, len(state.CommandLocators))
	for key, value := range state.CommandLocators {
		copyState.CommandLocators[key] = value
	}
	copyState.Commits = nil
	return copyState
}

func (s *StoreV3) scanFrames(visit func(CommittedV3) error) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pcl3") {
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
			committed, consumed, err := readFrameV3(f, name, offset)
			if err != nil {
				_ = f.Close()
				return err
			}
			if committed.Commit.CommandID != "" {
				committed.Locator = contracts.PolicyCommandLocatorV3{CommandID: committed.Commit.CommandID, SegmentID: name, FrameOffset: offset, CommitSequence: committed.Commit.CommitSequence, FrameSHA256: committed.Hash}
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

func cloneCheckpointV3(checkpoint contracts.PolicyCheckpointV3) contracts.PolicyCheckpointV3 {
	payload, err := contracts.MarshalCanonical(checkpoint)
	if err != nil {
		return checkpoint
	}
	var clone contracts.PolicyCheckpointV3
	if err := contracts.DecodeStrict(payload, &clone); err != nil {
		return checkpoint
	}
	return clone
}

type ReplayVerifierV3 struct {
	VerifyObservation func(contracts.PolicyCommitV3) error
	VerifyCommand     func(contracts.PolicyCommitV3) error
	Reevaluate        func(contracts.PolicyCommitV3) error
}

// VerifyReplayV3 enforces source verification before semantic re-evaluation.
// The application supplies lineage-bound raw projection and pure-engine
// callbacks; the durable adapter never imports capture or policy evaluation.
func VerifyReplayV3(commits []contracts.PolicyCommitV3, verifier ReplayVerifierV3) error {
	for _, commit := range commits {
		if err := verifyCommitV3(commit, verifier); err != nil {
			return err
		}
	}
	return nil
}

func verifyCommitV3(commit contracts.PolicyCommitV3, verifier ReplayVerifierV3) error {
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
