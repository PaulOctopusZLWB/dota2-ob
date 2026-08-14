package session

import (
	"bufio"
	"bytes"
	"context"
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

var ErrOutOfOrderHighWater = errors.New("out-of-order high-water mark")

type LiveProjection interface {
	Apply(context.Context, *Record) error
}

// StartupPublicationBarrier is a capture-owned dependency-inversion seam. A
// delivery implementation keeps publication closed between these calls while
// raw cursor authority is restored; this package does not import delivery,
// presentation, or policy.
type StartupPublicationBarrier interface {
	BeginRestore(context.Context) error
	CompleteRestore(context.Context) error
}

type RejectionTransition struct {
	Active   bool
	Sequence uint64
	Count    uint64
	Code     string
	Reason   string
}

type RejectionHealthSink interface {
	ProjectionHealth(context.Context, RejectionTransition) error
}
type LiveProjectionHealth struct {
	HighWater                       uint64        `json:"high_water"`
	ProjectedSequence               uint64        `json:"projected_sequence"`
	LagCount                        uint64        `json:"lag_count"`
	OldestLagAge                    time.Duration `json:"oldest_lag_age"`
	LastError                       string        `json:"last_error,omitempty"`
	Degraded                        bool          `json:"degraded"`
	ProjectionRejectionActive       bool          `json:"projection_rejection_active"`
	ProjectionRejectionCount        uint64        `json:"projection_rejection_count"`
	LastProjectionRejectionSequence uint64        `json:"last_projection_rejection_sequence"`
	LastProjectionRejectionCode     string        `json:"last_projection_rejection_code,omitempty"`
	LastProjectionRejectionReason   string        `json:"last_projection_rejection_reason,omitempty"`
}
type liveCursor struct {
	SchemaVersion                   int    `json:"schema_version"`
	SessionID                       string `json:"session_id"`
	Sequence                        uint64 `json:"sequence"`
	ProjectionRejectionActive       bool   `json:"projection_rejection_active"`
	ProjectionRejectionCount        uint64 `json:"projection_rejection_count"`
	LastProjectionRejectionSequence uint64 `json:"last_projection_rejection_sequence"`
	LastProjectionRejectionCode     string `json:"last_projection_rejection_code,omitempty"`
	LastProjectionRejectionReason   string `json:"last_projection_rejection_reason,omitempty"`
}
type CursorFile interface {
	io.Writer
	Name() string
	Chmod(os.FileMode) error
	Close() error
}
type CursorIO interface {
	CreateTemp(dir, pattern string) (CursorFile, error)
	Remove(path string) error
	Rename(oldPath, newPath string) error
}
type osCursorIO struct{}

func (osCursorIO) CreateTemp(dir, pattern string) (CursorFile, error) {
	return os.CreateTemp(dir, pattern)
}
func (osCursorIO) Remove(path string) error             { return os.Remove(path) }
func (osCursorIO) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

type LiveFollowerOption func(*LiveFollower)

func WithCursorIO(ops CursorIO) LiveFollowerOption {
	return func(f *LiveFollower) {
		if ops != nil {
			f.cursorIO = ops
		}
	}
}
func WithFollowerHighWater(highWater *HighWater) LiveFollowerOption {
	return func(f *LiveFollower) { f.highWater = highWater }
}
func WithStartupBarrier(barrier StartupPublicationBarrier) LiveFollowerOption {
	return func(f *LiveFollower) { f.startupBarrier = barrier }
}
func WithRejectionHealthSink(sink RejectionHealthSink) LiveFollowerOption {
	return func(f *LiveFollower) { f.rejectionSink = sink }
}

type LiveFollower struct {
	sessionID, rawPath, cursorPath string
	projections                    []LiveProjection
	runMu                          sync.Mutex
	healthMu                       sync.Mutex
	loaded                         bool
	health                         LiveProjectionHealth
	oldestLagAt                    time.Time
	pendingSequence                uint64
	nextProjection                 int
	cachedSequence                 uint64
	highWater                      *HighWater
	cursorIO                       CursorIO
	startupBarrier                 StartupPublicationBarrier
	rejectionSink                  RejectionHealthSink
	restoring                      bool
}

func NewLiveFollower(sessionID, rawPath, cursorPath string, projections []LiveProjection, opts ...LiveFollowerOption) *LiveFollower {
	f := &LiveFollower{sessionID: sessionID, rawPath: rawPath, cursorPath: cursorPath, projections: append([]LiveProjection(nil), projections...), cursorIO: osCursorIO{}}
	for _, opt := range opts {
		opt(f)
	}
	if f.highWater != nil {
		f.health.HighWater = f.highWater.Current().Sequence
		f.health.LagCount = f.health.HighWater
		f.health.Degraded = f.health.LagCount > 0
		if f.health.HighWater > 0 {
			f.oldestLagAt, _ = f.recordReceivedAt(1)
		}
	}
	return f
}
func (f *LiveFollower) Health() LiveProjectionHealth {
	f.healthMu.Lock()
	defer f.healthMu.Unlock()
	f.refreshHighWaterLocked()
	health := f.health
	if health.LagCount > 0 && !f.oldestLagAt.IsZero() {
		health.OldestLagAge = time.Since(f.oldestLagAt)
	}
	return health
}
func (f *LiveFollower) CatchUp(ctx context.Context, highWater uint64) error {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	f.healthMu.Lock()
	currentHighWater := f.health.HighWater
	f.healthMu.Unlock()
	if f.loaded && highWater < currentHighWater {
		return f.fail(ErrOutOfOrderHighWater)
	}
	if !f.loaded {
		if f.startupBarrier != nil {
			if err := f.startupBarrier.BeginRestore(ctx); err != nil {
				return f.fail(fmt.Errorf("begin startup barrier: %w", err))
			}
		}
		f.restoring = true
		cached, valid := f.loadCursor(highWater)
		if valid {
			authoritative, err := f.summarizeThrough(cached.Sequence)
			if err != nil {
				return f.fail(err)
			}
			valid = cursorSummariesEqual(cached, authoritative)
			if valid {
				f.restoreCursor(authoritative)
				if authoritative.ProjectionRejectionActive {
					if err := f.emitRejectionTransition(ctx, authoritative); err != nil {
						return f.fail(err)
					}
				}
				if err := f.completeStartupBarrier(ctx); err != nil {
					return f.fail(err)
				}
			}
		}
		if !valid {
			f.resetProjectionState()
		}
		f.loaded = true
	}
	f.healthMu.Lock()
	if highWater > f.health.HighWater {
		f.health.HighWater = highWater
	}
	f.refreshHighWaterLocked()
	f.updateLagLocked()
	projected := f.health.ProjectedSequence
	f.healthMu.Unlock()
	if projected >= highWater {
		return f.completeStartupBarrier(ctx)
	}
	err := f.forEachRange(projected+1, highWater, func(record *Record) error {
		f.healthMu.Lock()
		if f.oldestLagAt.IsZero() {
			f.oldestLagAt = record.ReceivedAt
		}
		f.healthMu.Unlock()
		if record.ProjectionResult == ProjectionProduced {
			start := 0
			if f.pendingSequence == record.Sequence {
				start = f.nextProjection
			} else {
				f.pendingSequence = record.Sequence
				f.nextProjection = 0
			}
			for index := start; index < len(f.projections); index++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := f.projections[index].Apply(ctx, record); err != nil {
					return fmt.Errorf("project sequence %d: %w", record.Sequence, err)
				}
				f.nextProjection = index + 1
			}
		}
		f.healthMu.Lock()
		next := cursorFromHealth(f.sessionID, record.Sequence, f.health)
		applyCursorResult(&next, record)
		f.healthMu.Unlock()
		if record.ProjectionResult == ProjectionConsumedNoOutput || next.ProjectionRejectionActive != f.currentRejectionActive() {
			if err := f.emitRejectionTransition(ctx, next); err != nil {
				return err
			}
		}
		f.restoreRejectionSummary(next)
		if err := f.writeCursor(record.Sequence); err != nil {
			return err
		}
		f.cachedSequence = record.Sequence
		f.healthMu.Lock()
		f.health.ProjectedSequence = record.Sequence
		f.pendingSequence = 0
		f.nextProjection = 0
		f.health.LastError = ""
		if record.Sequence < f.health.HighWater {
			if at, err := f.recordReceivedAt(record.Sequence + 1); err == nil {
				f.oldestLagAt = at
			}
		} else {
			f.oldestLagAt = time.Time{}
		}
		f.updateLagLocked()
		f.healthMu.Unlock()
		return nil
	})
	if err != nil {
		return f.fail(err)
	}
	return f.completeStartupBarrier(ctx)
}

func cursorFromHealth(sessionID string, sequence uint64, h LiveProjectionHealth) liveCursor {
	return liveCursor{SchemaVersion: 2, SessionID: sessionID, Sequence: sequence, ProjectionRejectionActive: h.ProjectionRejectionActive, ProjectionRejectionCount: h.ProjectionRejectionCount, LastProjectionRejectionSequence: h.LastProjectionRejectionSequence, LastProjectionRejectionCode: h.LastProjectionRejectionCode, LastProjectionRejectionReason: h.LastProjectionRejectionReason}
}
func applyCursorResult(c *liveCursor, record *Record) {
	c.Sequence = record.Sequence
	if record.ProjectionResult == ProjectionConsumedNoOutput {
		c.ProjectionRejectionActive = true
		if c.LastProjectionRejectionSequence != record.Sequence {
			c.ProjectionRejectionCount++
			c.LastProjectionRejectionSequence = record.Sequence
			c.LastProjectionRejectionCode = record.ProjectionCode
			c.LastProjectionRejectionReason = record.ProjectionReason
		}
	} else {
		c.ProjectionRejectionActive = false
	}
}
func (f *LiveFollower) summarizeThrough(sequence uint64) (liveCursor, error) {
	summary := liveCursor{SchemaVersion: 2, SessionID: f.sessionID}
	if sequence == 0 {
		return summary, nil
	}
	err := f.forEachRange(1, sequence, func(record *Record) error { applyCursorResult(&summary, record); return nil })
	return summary, err
}
func cursorSummariesEqual(a, b liveCursor) bool { return a == b }
func (f *LiveFollower) restoreCursor(c liveCursor) {
	f.cachedSequence = c.Sequence
	f.healthMu.Lock()
	f.health.ProjectedSequence = c.Sequence
	f.health.ProjectionRejectionActive = c.ProjectionRejectionActive
	f.health.ProjectionRejectionCount = c.ProjectionRejectionCount
	f.health.LastProjectionRejectionSequence = c.LastProjectionRejectionSequence
	f.health.LastProjectionRejectionCode = c.LastProjectionRejectionCode
	f.health.LastProjectionRejectionReason = c.LastProjectionRejectionReason
	f.updateLagLocked()
	f.healthMu.Unlock()
}
func (f *LiveFollower) restoreRejectionSummary(c liveCursor) {
	f.healthMu.Lock()
	f.health.ProjectionRejectionActive = c.ProjectionRejectionActive
	f.health.ProjectionRejectionCount = c.ProjectionRejectionCount
	f.health.LastProjectionRejectionSequence = c.LastProjectionRejectionSequence
	f.health.LastProjectionRejectionCode = c.LastProjectionRejectionCode
	f.health.LastProjectionRejectionReason = c.LastProjectionRejectionReason
	f.healthMu.Unlock()
}
func (f *LiveFollower) resetProjectionState() {
	f.cachedSequence = 0
	f.healthMu.Lock()
	f.health.ProjectedSequence = 0
	f.health.ProjectionRejectionActive = false
	f.health.ProjectionRejectionCount = 0
	f.health.LastProjectionRejectionSequence = 0
	f.health.LastProjectionRejectionCode = ""
	f.health.LastProjectionRejectionReason = ""
	f.healthMu.Unlock()
}
func (f *LiveFollower) currentRejectionActive() bool {
	f.healthMu.Lock()
	defer f.healthMu.Unlock()
	return f.health.ProjectionRejectionActive
}
func (f *LiveFollower) emitRejectionTransition(ctx context.Context, c liveCursor) error {
	if f.rejectionSink == nil {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := f.rejectionSink.ProjectionHealth(bounded, RejectionTransition{Active: c.ProjectionRejectionActive, Sequence: c.LastProjectionRejectionSequence, Count: c.ProjectionRejectionCount, Code: c.LastProjectionRejectionCode, Reason: c.LastProjectionRejectionReason})
	if err != nil {
		return fmt.Errorf("projection health transition: %w", err)
	}
	return nil
}
func (f *LiveFollower) completeStartupBarrier(ctx context.Context) error {
	if !f.restoring {
		return nil
	}
	if f.startupBarrier != nil {
		if err := f.startupBarrier.CompleteRestore(ctx); err != nil {
			return fmt.Errorf("complete startup barrier: %w", err)
		}
	}
	f.restoring = false
	return nil
}
func (f *LiveFollower) Run(ctx context.Context, updates <-chan HighWaterMark) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case mark, ok := <-updates:
			if !ok {
				return nil
			}
			if mark.SessionID != f.sessionID {
				_ = f.fail(errors.New("high-water session mismatch"))
				continue
			}
			for {
				target := mark.Sequence
				if f.highWater != nil {
					target = f.highWater.Current().Sequence
				}
				if err := f.CatchUp(ctx, target); err == nil {
					break
				}
				timer := time.NewTimer(10 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}
		}
	}
}
func (f *LiveFollower) loadCursor(highWater uint64) (liveCursor, bool) {
	data, err := os.ReadFile(f.cursorPath)
	if err != nil {
		return liveCursor{}, false
	}
	var cached liveCursor
	if json.Unmarshal(data, &cached) != nil || cached.SchemaVersion != 2 || cached.SessionID != f.sessionID || cached.Sequence > highWater || !validCursorSummary(cached) {
		return liveCursor{}, false
	}
	return cached, true
}

func validCursorSummary(c liveCursor) bool {
	if c.ProjectionRejectionCount == 0 {
		return !c.ProjectionRejectionActive && c.LastProjectionRejectionSequence == 0 && c.LastProjectionRejectionCode == "" && c.LastProjectionRejectionReason == ""
	}
	if c.LastProjectionRejectionSequence == 0 || c.LastProjectionRejectionSequence > c.Sequence {
		return false
	}
	if c.ProjectionRejectionActive && c.LastProjectionRejectionSequence != c.Sequence {
		return false
	}
	switch c.LastProjectionRejectionCode {
	case "gsi_projection_non_object":
		return c.LastProjectionRejectionReason == "top_level_non_object"
	case "gsi_projection_bounds_exceeded":
		for _, reason := range []string{"participant_count", "item_count", "ability_count", "building_count", "identifier_bytes", "string_bytes", "number_bytes"} {
			if c.LastProjectionRejectionReason == reason {
				return true
			}
		}
	}
	return false
}
func (f *LiveFollower) forEachRange(from, through uint64, apply func(*Record) error) error {
	return scanRecordRange(f.rawPath, f.sessionID, from, through, true, apply)
}

// StreamRecords decodes and validates one bounded raw frame at a time. The
// record and its exact raw bytes are owned only for the duration of apply.
func StreamRecords(rawPath, sessionID string, apply func(*Record) error) error {
	if !rawPathIsV3(rawPath) {
		return scanLegacyRecords(rawPath, sessionID, apply)
	}
	return scanRecordRange(rawPath, sessionID, 1, 0, false, apply)
}

func rawPathIsV3(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	prefix := make([]byte, 64)
	n, _ := file.Read(prefix)
	return bytes.HasPrefix(bytes.TrimSpace(prefix[:n]), []byte(`{"schema_version":3,`))
}

func scanLegacyRecords(rawPath, sessionID string, apply func(*Record) error) error {
	file, err := os.Open(rawPath)
	if err != nil {
		return fmt.Errorf("open raw log: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	sequence := uint64(0)
	version := 0
	for {
		frame, terminated, err := readLegacyFrame(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !terminated {
			break
		}
		sequence++
		record, err := decodePersistedRecord(frame, sessionID, sequence)
		if err != nil {
			return fmt.Errorf("decode raw sequence %d: %w", sequence, err)
		}
		if version == 0 {
			version = record.SchemaVersion
		} else if version != record.SchemaVersion {
			return errors.New("mixed raw schema versions")
		}
		if err := apply(record); err != nil {
			return err
		}
	}
	return nil
}

func readLegacyFrame(reader *bufio.Reader) ([]byte, bool, error) {
	const maxLegacyFrame = 32 << 20
	frame := make([]byte, 0, 64*1024)
	depth := 0
	started := false
	complete := false
	inString := false
	escaped := false
	for {
		b, err := reader.ReadByte()
		if err == io.EOF {
			return frame, false, io.EOF
		}
		if err != nil {
			return nil, false, err
		}
		if len(frame) >= maxLegacyFrame {
			return nil, false, errors.New("raw frame exceeds bounded limit")
		}
		if complete {
			if b == '\n' {
				return frame, true, nil
			}
			if b != ' ' && b != '\t' && b != '\r' {
				return nil, false, errors.New("multiple JSON values on one raw line")
			}
			frame = append(frame, b)
			continue
		}
		frame = append(frame, b)
		if !started {
			if b == '\n' {
				trimmed := bytes.TrimSpace(frame[:len(frame)-1])
				if len(trimmed) == 0 {
					return nil, false, errors.New("blank raw record")
				}
				return append([]byte(nil), trimmed...), true, nil
			}
			if b == ' ' || b == '\t' || b == '\r' {
				continue
			}
			if b != '{' {
				continue
			}
			started = true
			depth = 1
			continue
		}
		if inString {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}
		if b == '"' {
			inString = true
			continue
		}
		if b == '{' || b == '[' {
			depth++
		} else if b == '}' || b == ']' {
			depth--
			if depth == 0 {
				complete = true
			}
		}
	}
}

func scanRecordRange(rawPath, sessionID string, from, through uint64, requireThrough bool, apply func(*Record) error) error {
	file, err := os.Open(rawPath)
	if err != nil {
		return fmt.Errorf("open raw log: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	want := uint64(1)
	version := 0
	for {
		line, terminated, err := readBoundedLine(reader)
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return err
		}
		if !terminated {
			return fmt.Errorf("raw sequence %d has unterminated tail", want)
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		record, err := decodePersistedRecord(line, sessionID, want)
		if err != nil {
			return fmt.Errorf("decode raw sequence %d: %w", want, err)
		}
		if version == 0 {
			version = record.SchemaVersion
		} else if record.SchemaVersion != version {
			return errors.New("mixed raw schema versions")
		}
		if want >= from && (!requireThrough || want <= through) {
			if err := apply(record); err != nil {
				return err
			}
		}
		want++
		if requireThrough && want > through {
			break
		}
	}
	if requireThrough && want <= through {
		return fmt.Errorf("raw sequence %d is not committed", want)
	}
	return nil
}

func readBoundedLine(reader *bufio.Reader) ([]byte, bool, error) {
	const maxLegacyFrame = 32 << 20
	line := make([]byte, 0, 64*1024)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxLegacyFrame {
			return nil, false, errors.New("raw frame exceeds bounded limit")
		}
		line = append(line, fragment...)
		switch err {
		case nil:
			return line, true, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return line, false, io.EOF
		default:
			return nil, false, err
		}
	}
}
func (f *LiveFollower) writeCursor(sequence uint64) error {
	tmp, err := f.cursorIO.CreateTemp(filepath.Dir(f.cursorPath), ".live-projection-cursor-*.tmp")
	if err != nil {
		return fmt.Errorf("create cursor temp: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = f.cursorIO.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	f.healthMu.Lock()
	cursor := cursorFromHealth(f.sessionID, sequence, f.health)
	f.healthMu.Unlock()
	data, err := json.Marshal(cursor)
	if err == nil {
		line := append(data, '\n')
		var n int
		n, err = tmp.Write(line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}
	if err := f.cursorIO.Rename(tmpPath, f.cursorPath); err != nil {
		return fmt.Errorf("rename cursor: %w", err)
	}
	remove = false
	return nil
}
func (f *LiveFollower) updateLagLocked() {
	if f.health.HighWater > f.health.ProjectedSequence {
		f.health.LagCount = f.health.HighWater - f.health.ProjectedSequence
		if f.oldestLagAt.IsZero() {
			f.oldestLagAt = time.Now()
		}
	} else {
		f.health.LagCount = 0
		f.oldestLagAt = time.Time{}
	}
	f.health.Degraded = f.health.LagCount > 0 || f.health.LastError != "" || f.health.ProjectionRejectionActive
}
func (f *LiveFollower) refreshHighWaterLocked() {
	if f.highWater != nil {
		if current := f.highWater.Current().Sequence; current > f.health.HighWater {
			f.health.HighWater = current
		}
	}
	f.updateLagLocked()
	if f.health.LagCount > 0 && f.oldestLagAt.IsZero() {
		if at, err := f.recordReceivedAt(f.health.ProjectedSequence + 1); err == nil {
			f.oldestLagAt = at
		}
	}
}
func (f *LiveFollower) recordReceivedAt(sequence uint64) (time.Time, error) {
	var at time.Time
	err := f.forEachRange(sequence, sequence, func(record *Record) error { at = record.ReceivedAt; return nil })
	return at, err
}
func (f *LiveFollower) fail(err error) error {
	message := err.Error()
	if len(message) > 160 {
		message = message[:160]
	}
	if strings.Contains(message, string(filepath.Separator)) {
		message = "live projection failed"
	}
	f.healthMu.Lock()
	f.health.LastError = message
	f.updateLagLocked()
	f.healthMu.Unlock()
	return err
}
