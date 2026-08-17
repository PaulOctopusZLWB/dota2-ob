package gsi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
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
	diagnostics         *DiagnosticConfig
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
	projectionOptions   []liveprojection.Option
}

type Option func(*Server)

type DiagnosticConfig struct {
	BearerToken   string
	AllowedOrigin string
}

type RuntimeCapacityStatus struct {
	SchemaVersion        string `json:"schema_version"`
	NotificationCapacity int    `json:"notification_capacity"`
	CandidateCapacity    int    `json:"candidate_queue_capacity"`
	PolicyHealthy        bool   `json:"policy_healthy"`
}

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

// WithDiagnostics re-enables the deprecated capture-owned GET surfaces. An
// empty token or origin leaves them disabled (the production default).
func WithDiagnostics(config DiagnosticConfig) Option {
	return func(server *Server) {
		if strings.TrimSpace(config.BearerToken) != "" && validDiagnosticOrigin(config.AllowedOrigin) {
			config.AllowedOrigin = strings.TrimRight(config.AllowedOrigin, "/")
			server.diagnostics = &config
		}
	}
}

func validDiagnosticOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
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

func WithProjectionStartupBarrier(barrier liveprojection.StartupPublicationBarrier) Option {
	return func(server *Server) {
		server.projectionOptions = append(server.projectionOptions, liveprojection.WithStartupBarrier(barrier))
	}
}

func WithProjectionRejectionHealthSink(sink liveprojection.RejectionHealthSink) Option {
	return func(server *Server) {
		server.projectionOptions = append(server.projectionOptions, liveprojection.WithRejectionHealthSink(sink))
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
	if len(projections) > 0 || len(server.projectionOptions) > 0 {
		projectorOptions := append([]liveprojection.Option{liveprojection.WithHighWater(store.HighWater())}, server.projectionOptions...)
		server.projector = liveprojection.New(store.SessionID(), store.RawPath(), filepath.Join(store.SessionDir(), "live_projection_cursor.json"), projections, projectorOptions...)
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
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/gsi", s.handleGSI)
	if s.diagnostics != nil {
		s.mux.Handle("/api/latest", s.diagnostic(http.HandlerFunc(s.handleLatest)))
		s.mux.Handle("/api/profile", s.diagnostic(http.HandlerFunc(s.handleProfile)))
		s.mux.Handle("/api/analytics", s.diagnostic(http.HandlerFunc(s.handleAnalytics)))
		s.mux.Handle("/api/events", s.diagnostic(http.HandlerFunc(s.handleEvents)))
		if s.dashboard != nil {
			s.mux.Handle("/", s.diagnostic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					http.NotFound(w, r)
					return
				}
				s.dashboard.ServeHTTP(w, r)
			})))
		}
	}
}

func (s *Server) diagnostic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setDiagnosticHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !diagnosticBearerMatches(r.Header.Get("Authorization"), s.diagnostics.BearerToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")
		if origin == "null" || (origin != "" && origin != s.diagnostics.AllowedOrigin) {
			http.Error(w, "origin rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setDiagnosticHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func diagnosticBearerMatches(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	return len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
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
	capacity := RuntimeCapacityStatus{SchemaVersion: "runtime_capacity.v2", NotificationCapacity: cap(s.store.HighWater().C())}
	if policyStatus, ok := policy.RuntimeStatusForSession(s.store.SessionID()); ok {
		capacity.CandidateCapacity = policyStatus.CandidateCapacity
		capacity.PolicyHealthy = policyStatus.Healthy && trackerSnapshot.State != operator.StateDegraded
	}
	status := struct {
		operator.Snapshot
		LiveProjection  liveprojection.Health `json:"live_projection"`
		RuntimeCapacity RuntimeCapacityStatus `json:"runtime_capacity"`
	}{Snapshot: trackerSnapshot, LiveProjection: projectionHealth, RuntimeCapacity: capacity}
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
