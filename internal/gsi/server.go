package gsi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

const maxSnapshotBytes = 10 << 20

type Server struct {
	store     *session.Store
	latest    *state.Latest
	profiler  *profile.Profiler
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

func NewServer(store *session.Store, opts ...Option) http.Handler {
	server := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(server)
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
	if s.store == nil {
		http.Error(w, "session store is not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSnapshotBytes))
	if err != nil {
		http.Error(w, "request body is too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	record, err := s.store.Append(body)
	if err != nil {
		if errors.Is(err, session.ErrInvalidJSON) {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to persist snapshot", http.StatusInternalServerError)
		return
	}
	if s.latest != nil {
		s.latest.Update(record.ReceivedAt, record.Payload)
	}
	if s.profiler != nil {
		s.profiler.Observe(record.ReceivedAt, record.Payload)
		if err := profile.WriteSummary(filepath.Join(s.store.SessionDir(), "session_summary.md"), s.profiler.Snapshot()); err != nil {
			http.Error(w, "failed to write session summary", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
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
