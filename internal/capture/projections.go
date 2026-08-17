package capture

import (
	"errors"
	"path/filepath"
	"sync"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

type latestProjection struct{ latest *state.Latest }

func NewLatestProjection(latest *state.Latest) Projection { return latestProjection{latest} }
func (p latestProjection) Apply(record *session.Record) error {
	p.latest.Update(record.ReceivedAt, record.Payload)
	return nil
}

type profileProjection struct {
	profiler        *profile.Profiler
	sessionDir      string
	mu              sync.Mutex
	appliedSession  string
	appliedSequence uint64
}

func NewProfileProjection(profiler *profile.Profiler, sessionDir string) Projection {
	return &profileProjection{profiler: profiler, sessionDir: sessionDir}
}
func (p *profileProjection) Apply(record *session.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.applyEvidence(record, func() { p.profiler.Observe(record.ReceivedAt, record.Payload) }); err != nil {
		return err
	}
	return profile.WriteSummary(filepath.Join(p.sessionDir, "session_summary.md"), p.profiler.Snapshot())
}

type analyticsProjection struct {
	engine                *analytics.Engine
	sessionDir, sessionID string
	mu                    sync.Mutex
	appliedSession        string
	appliedSequence       uint64
}

func NewAnalyticsProjection(engine *analytics.Engine, sessionDir, sessionID string) Projection {
	return &analyticsProjection{engine: engine, sessionDir: sessionDir, sessionID: sessionID}
}
func (p *analyticsProjection) Apply(record *session.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	observation, err := MapLiveObservationV1(record)
	if err != nil {
		return err
	}
	tick, err := LegacyNormalizedTickV1(observation, currentRecordResolver(record))
	if err != nil {
		return err
	}
	if err := p.applyEvidence(record, func() { p.engine.Observe(tick) }); err != nil {
		return err
	}
	return analytics.WriteSummaryFiles(p.sessionDir, p.sessionID, p.engine)
}

func (p *profileProjection) applyEvidence(record *session.Record, apply func()) error {
	if p.appliedSession == record.SessionID && p.appliedSequence == record.Sequence {
		return nil
	}
	if p.appliedSession == record.SessionID && record.Sequence < p.appliedSequence {
		return errors.New("profile evidence out of order")
	}
	apply()
	p.appliedSession = record.SessionID
	p.appliedSequence = record.Sequence
	return nil
}
func (p *analyticsProjection) applyEvidence(record *session.Record, apply func()) error {
	if p.appliedSession == record.SessionID && p.appliedSequence == record.Sequence {
		return nil
	}
	if p.appliedSession == record.SessionID && record.Sequence < p.appliedSequence {
		return errors.New("analytics evidence out of order")
	}
	apply()
	p.appliedSession = record.SessionID
	p.appliedSequence = record.Sequence
	return nil
}
