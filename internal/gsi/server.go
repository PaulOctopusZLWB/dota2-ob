package gsi

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const maxSnapshotBytes = 10 << 20

type Server struct {
	store *session.Store
	mux   *http.ServeMux
}

func NewServer(store *session.Store) http.Handler {
	server := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/gsi", s.handleGSI)
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
	if _, err := s.store.Append(body); err != nil {
		if errors.Is(err, session.ErrInvalidJSON) {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to persist snapshot", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}
