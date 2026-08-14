package liveprojection

import "github.com/PaulOctopusZLWB/dota2-ob/internal/session"

var ErrOutOfOrderHighWater = session.ErrOutOfOrderHighWater

type Projection = session.LiveProjection
type Health = session.LiveProjectionHealth
type Follower = session.LiveFollower
type Option = session.LiveFollowerOption
type CursorFile = session.CursorFile
type CursorIO = session.CursorIO
type StartupPublicationBarrier = session.StartupPublicationBarrier
type RejectionHealthSink = session.RejectionHealthSink
type RejectionTransition = session.RejectionTransition
type ScanRange = session.ScanRange
type ScanObserver = session.ScanObserver

func New(sessionID, rawPath, cursorPath string, projections []Projection, opts ...Option) *Follower {
	return session.NewLiveFollower(sessionID, rawPath, cursorPath, projections, opts...)
}

var WithCursorIO = session.WithCursorIO
var WithHighWater = session.WithFollowerHighWater
var WithStartupBarrier = session.WithStartupBarrier
var WithRejectionHealthSink = session.WithRejectionHealthSink
var WithScanObserver = session.WithScanObserver
