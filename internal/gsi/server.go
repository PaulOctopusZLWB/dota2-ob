package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

const maxSnapshotBytes = 10 << 20

type Server struct {
	store               *session.Store
	latest              *state.Latest
	profiler            *profile.Profiler
	engine              *analytics.Engine
	processor           *capture.Processor
	tracker             *operator.Tracker
	dashboard           http.Handler
	mux                 *http.ServeMux
	inflightMu          sync.Mutex
	inflightCond        *sync.Cond
	inflight            int
	stopping            bool
	projector           *liveprojection.Follower
	projectorCancel     context.CancelFunc
	projectorDone       chan error
	projectionDrain     time.Duration
	projectionOverrides []liveprojection.Projection
}

type Option func(*Server)

func WithLatest(latest *state.Latest) Option {
	return func(server *Server) {
		server.latest = latest
	}
}

func WithDashboard(handler http.Handler) Option {
	return func(server *Server) {
		server.dashboard = handler
	}
}

func WithProfiler(profiler *profile.Profiler) Option {
	return func(server *Server) {
		server.profiler = profiler
	}
}

func WithAnalytics(engine *analytics.Engine) Option {
	return func(server *Server) {
		server.engine = engine
	}
}

func WithProcessor(processor *capture.Processor) Option {
	return func(server *Server) { server.processor = processor }
}
func WithTracker(tracker *operator.Tracker) Option {
	return func(server *Server) { server.tracker = tracker }
}

func WithLiveProjections(projections ...liveprojection.Projection) Option {
	return func(server *Server) {
		server.projectionOverrides = append([]liveprojection.Projection(nil), projections...)
	}
}

func WithProjectionDrainTimeout(timeout time.Duration) Option {
	return func(server *Server) {
		if timeout > 0 {
			server.projectionDrain = timeout
		}
	}
}

func NewServer(store *session.Store, opts ...Option) *Server {
	server := &Server{
		store: store, mux: http.NewServeMux(), projectionDrain: 10 * time.Second,
	}
	server.inflightCond = sync.NewCond(&server.inflightMu)
	for _, opt := range opts {
		opt(server)
	}
	if server.tracker == nil {
		now := time.Now().UTC()
		server.tracker = operator.NewTracker(store.SessionID(), now, 15*time.Second, time.Now)
	}
	if server.processor == nil {
		server.processor = capture.NewProcessor(store, server.tracker)
	}
	var projections []liveprojection.Projection
	if server.latest != nil {
		projections = append(projections, trackedProjection{capture.NewLatestProjection(server.latest), server.tracker, operator.SubsystemLatest, "latest_failed"})
	}
	if server.profiler != nil {
		projections = append(projections, trackedProjection{capture.NewProfileProjection(server.profiler, store.SessionDir()), server.tracker, operator.SubsystemProfile, "profile_failed"})
	}
	if server.engine != nil {
		projections = append(projections, trackedProjection{capture.NewAnalyticsProjection(server.engine, store.SessionDir(), store.SessionID()), server.tracker, operator.SubsystemAnalytics, "analytics_failed"})
	}
	if server.projectionOverrides != nil {
		projections = server.projectionOverrides
	}
	if len(projections) > 0 {
		server.projector = liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "live_projection_cursor.json"), projections)
		projectorContext, cancel := context.WithCancel(context.Background())
		server.projectorCancel = cancel
		server.projectorDone = make(chan error, 1)
		go func() { server.projectorDone <- server.projector.Run(projectorContext, store.HighWater().C()) }()
		store.HighWater().Wake()
	}
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.inflightMu.Lock()
	if s.stopping {
		s.inflightMu.Unlock()
		http.Error(w, "server stopping", http.StatusServiceUnavailable)
		return
	}
	s.inflight++
	s.inflightMu.Unlock()
	defer func() {
		s.inflightMu.Lock()
		s.inflight--
		s.inflightCond.Broadcast()
		s.inflightMu.Unlock()
	}()
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Wait() {
	s.inflightMu.Lock()
	s.stopping = true
	for s.inflight > 0 {
		s.inflightCond.Wait()
	}
	s.inflightMu.Unlock()
	if s.projector != nil {
		s.projectorCancel()
		stopped := false
		select {
		case <-s.projectorDone:
			stopped = true
		case <-time.After(s.projectionDrain):
		}
		if stopped {
			ctx, cancel := context.WithTimeout(context.Background(), s.projectionDrain)
			_ = s.projector.CatchUp(ctx, s.store.HighWater().Current().Sequence)
			cancel()
		}
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/api/latest", s.handleLatest)
	s.mux.HandleFunc("/api/profile", s.handleProfile)
	s.mux.HandleFunc("/api/analytics", s.handleAnalytics)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/gsi", s.handleGSI)
	if s.dashboard != nil {
		s.mux.Handle("/", s.dashboard)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *Server) handleGSI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.tracker.Request()
	if s.store == nil {
		http.Error(w, "session store is not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSnapshotBytes))
	if err != nil {
		s.tracker.Rejected()
		http.Error(w, "request body is too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	_, err = s.processor.Process(body)
	if err != nil {
		if errors.Is(err, session.ErrInvalidJSON) {
			s.tracker.Rejected()
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrStoreSealed) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, "failed to persist snapshot", status)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	projectionHealth := liveprojection.Health{}
	if s.projector != nil {
		projectionHealth = s.projector.Health()
	}
	trackerSnapshot := s.tracker.Snapshot()
	if projectionHealth.Degraded {
		trackerSnapshot.State = operator.StateDegraded
	}
	status := struct {
		operator.Snapshot
		LiveProjection liveprojection.Health `json:"live_projection"`
	}{Snapshot: trackerSnapshot, LiveProjection: projectionHealth}
	if err := json.NewEncoder(w).Encode(status); err != nil {
		http.Error(w, "failed to encode status", http.StatusInternalServerError)
	}
}

type trackedProjection struct {
	projection      capture.Projection
	tracker         *operator.Tracker
	subsystem, code string
}

func (p trackedProjection) Apply(ctx context.Context, record *session.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.projection.Apply(record); err != nil {
		p.tracker.Failure(p.subsystem, p.code, safeProjectionMessage(p.code))
		return errors.New(p.code)
	}
	if p.subsystem == operator.SubsystemAnalytics {
		p.tracker.AnalyticsSuccess(record.ReceivedAt)
	} else {
		p.tracker.Success(p.subsystem)
	}
	return nil
}

func safeProjectionMessage(code string) string {
	switch code {
	case "latest_failed":
		return "latest projection failed"
	case "profile_failed":
		return "profile projection failed"
	case "analytics_failed":
		return "analytics projection failed"
	default:
		return "projection failed"
	}
}

func (s *Server) handleLatest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	sessionID := ""
	if s.store != nil {
		sessionID = s.store.SessionID()
	}
	latest := s.latest
	if latest == nil {
		latest = state.NewLatest()
	}
	if err := json.NewEncoder(w).Encode(latest.Snapshot(sessionID)); err != nil {
		http.Error(w, "failed to encode latest state", http.StatusInternalServerError)
	}
}

func (s *Server) handleProfile(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	profiler := s.profiler
	if profiler == nil {
		profiler = profile.NewProfiler()
	}
	if err := json.NewEncoder(w).Encode(profiler.Snapshot()); err != nil {
		http.Error(w, "failed to encode field profile", http.StatusInternalServerError)
	}
}

func (s *Server) handleAnalytics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	engine := s.engine
	if engine == nil {
		engine = analytics.NewEngine()
	}
	sessionID := ""
	if s.store != nil {
		sessionID = s.store.SessionID()
	}
	if err := json.NewEncoder(w).Encode(engine.Snapshot(sessionID)); err != nil {
		http.Error(w, "failed to encode analytics snapshot", http.StatusInternalServerError)
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	engine := s.engine
	if engine == nil {
		engine = analytics.NewEngine()
	}
	if err := json.NewEncoder(w).Encode(engine.Events()); err != nil {
		http.Error(w, "failed to encode events", http.StatusInternalServerError)
	}
}
