package gsi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

const maxSnapshotBytes = 10 << 20

type Server struct {
	store     *session.Store
	latest    *state.Latest
	profiler  *profile.Profiler
	engine    *analytics.Engine
	processor *capture.Processor
	tracker   *operator.Tracker
	dashboard http.Handler
	mux       *http.ServeMux
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

func NewServer(store *session.Store, opts ...Option) http.Handler {
	server := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(server)
	}
	if server.tracker == nil {
		now := time.Now().UTC()
		server.tracker = operator.NewTracker(store.SessionID(), now, 15*time.Second, time.Now)
	}
	if server.processor == nil {
		var processorOpts []capture.Option
		if server.latest != nil {
			processorOpts = append(processorOpts, capture.WithLatest(capture.NewLatestProjection(server.latest)))
		}
		if server.profiler != nil {
			processorOpts = append(processorOpts, capture.WithProfile(capture.NewProfileProjection(server.profiler, store.SessionDir())))
		}
		if server.engine != nil {
			processorOpts = append(processorOpts, capture.WithAnalytics(capture.NewAnalyticsProjection(server.engine, store.SessionDir(), store.SessionID())))
		}
		server.processor = capture.NewProcessor(store, server.tracker, processorOpts...)
	}
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
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
	if err := json.NewEncoder(w).Encode(s.tracker.Snapshot()); err != nil {
		http.Error(w, "failed to encode status", http.StatusInternalServerError)
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
