package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type LiveProjectionHealth struct {
	HighWater         uint64        `json:"high_water"`
	ProjectedSequence uint64        `json:"projected_sequence"`
	LagCount          uint64        `json:"lag_count"`
	OldestLagAge      time.Duration `json:"oldest_lag_age"`
	LastError         string        `json:"last_error,omitempty"`
	Degraded          bool          `json:"degraded"`
}
type liveCursor struct {
	SessionID string `json:"session_id"`
	Sequence  uint64 `json:"sequence"`
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
}

func NewLiveFollower(sessionID, rawPath, cursorPath string, projections []LiveProjection) *LiveFollower {
	return &LiveFollower{sessionID: sessionID, rawPath: rawPath, cursorPath: cursorPath, projections: append([]LiveProjection(nil), projections...)}
}
func (f *LiveFollower) Health() LiveProjectionHealth {
	f.healthMu.Lock()
	defer f.healthMu.Unlock()
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
		projected := f.loadCursor(highWater)
		f.healthMu.Lock()
		f.health.ProjectedSequence = projected
		f.healthMu.Unlock()
		f.loaded = true
	}
	f.healthMu.Lock()
	if highWater > f.health.HighWater {
		f.health.HighWater = highWater
	}
	f.updateLagLocked()
	projected := f.health.ProjectedSequence
	f.healthMu.Unlock()
	if projected >= highWater {
		return nil
	}
	records, err := f.readRange(projected+1, highWater)
	if err != nil {
		return f.fail(err)
	}
	for _, record := range records {
		start := 0
		if f.pendingSequence == record.Sequence {
			start = f.nextProjection
		} else {
			f.pendingSequence = record.Sequence
			f.nextProjection = 0
		}
		for index := start; index < len(f.projections); index++ {
			if err := ctx.Err(); err != nil {
				return f.fail(err)
			}
			if err := f.projections[index].Apply(ctx, record); err != nil {
				return f.fail(fmt.Errorf("project sequence %d: %w", record.Sequence, err))
			}
			f.nextProjection = index + 1
		}
		if err := f.writeCursor(record.Sequence); err != nil {
			return f.fail(err)
		}
		f.healthMu.Lock()
		f.health.ProjectedSequence = record.Sequence
		f.pendingSequence = 0
		f.nextProjection = 0
		f.health.LastError = ""
		f.updateLagLocked()
		f.healthMu.Unlock()
	}
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
				if err := f.CatchUp(ctx, mark.Sequence); err == nil {
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
func (f *LiveFollower) loadCursor(highWater uint64) uint64 {
	data, err := os.ReadFile(f.cursorPath)
	if err != nil {
		return 0
	}
	var cached liveCursor
	if json.Unmarshal(data, &cached) != nil || cached.SessionID != f.sessionID || cached.Sequence > highWater {
		return 0
	}
	return cached.Sequence
}
func (f *LiveFollower) readRange(from, through uint64) ([]*Record, error) {
	file, err := os.Open(f.rawPath)
	if err != nil {
		return nil, fmt.Errorf("open raw log: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 11<<20)
	want := uint64(1)
	var records []*Record
	for scanner.Scan() {
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode raw sequence %d: %w", want, err)
		}
		if record.SchemaVersion == 0 && record.SessionID == "" && record.Sequence == 0 && record.Source == "" && !record.ReceivedAt.IsZero() && record.Payload != nil && len(record.Raw) > 0 {
			record.SchemaVersion = 1
			record.SessionID = f.sessionID
			record.Sequence = want
			record.Source = "gsi"
		}
		if record.SessionID != f.sessionID || record.Sequence != want || (record.SchemaVersion != 1 && record.SchemaVersion != 2) || record.Source != "gsi" {
			return nil, fmt.Errorf("invalid raw sequence %d", want)
		}
		if want >= from && want <= through {
			copy := record
			records = append(records, &copy)
		}
		want++
		if want > through {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if want <= through {
		return nil, fmt.Errorf("raw sequence %d is not committed", want)
	}
	return records, nil
}
func (f *LiveFollower) writeCursor(sequence uint64) error {
	tmp, err := os.CreateTemp(filepath.Dir(f.cursorPath), ".live-projection-cursor-*.tmp")
	if err != nil {
		return fmt.Errorf("create cursor temp: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	data, err := json.Marshal(liveCursor{SessionID: f.sessionID, Sequence: sequence})
	if err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}
	if err := os.Rename(tmpPath, f.cursorPath); err != nil {
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
	f.health.Degraded = f.health.LagCount > 0 || f.health.LastError != ""
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
