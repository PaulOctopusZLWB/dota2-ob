package capture

import (
	"path/filepath"

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
	profiler   *profile.Profiler
	sessionDir string
}

func NewProfileProjection(profiler *profile.Profiler, sessionDir string) Projection {
	return profileProjection{profiler, sessionDir}
}
func (p profileProjection) Apply(record *session.Record) error {
	p.profiler.Observe(record.ReceivedAt, record.Payload)
	return profile.WriteSummary(filepath.Join(p.sessionDir, "session_summary.md"), p.profiler.Snapshot())
}

type analyticsProjection struct {
	engine                *analytics.Engine
	sessionDir, sessionID string
}

func NewAnalyticsProjection(engine *analytics.Engine, sessionDir, sessionID string) Projection {
	return analyticsProjection{engine, sessionDir, sessionID}
}
func (p analyticsProjection) Apply(record *session.Record) error {
	p.engine.Observe(analytics.Normalize(record.ReceivedAt, record.Payload))
	return analytics.WriteSummaryFiles(p.sessionDir, p.sessionID, p.engine)
}
