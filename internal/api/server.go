// Package api is Synergy's HTTP layer: routing, middleware, request parsing
// and JSON responses. It contains no business logic; handlers delegate to
// services injected through Options.
package api

import (
	"log/slog"
	"net/http"
)

// Options are the dependencies of the HTTP API.
type Options struct {
	Logger  *slog.Logger
	Version string
}

// Server is the root http.Handler for the Synergy API.
type Server struct {
	logger  *slog.Logger
	version string
	mux     *http.ServeMux
	handler http.Handler
}

// New builds the API handler with all routes and middleware installed.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		logger:  logger,
		version: opts.Version,
		mux:     http.NewServeMux(),
	}
	s.routes()

	// Outermost first: the request ID must exist before logging, and logging
	// wraps panic recovery so recovered 500s are still logged.
	s.handler = requestID(logRequests(logger)(recoverPanics(logger)(http.HandlerFunc(s.dispatch))))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
}

// dispatch routes the request, replacing net/http's plain-text 404 and 405
// responses with the API's JSON error format.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	h, pattern := s.mux.Handler(r)
	if pattern != "" {
		h.ServeHTTP(w, r)
		return
	}

	// No route matched. Let the mux decide between 404 and 405 (it also
	// computes the Allow header), but discard its plain-text body.
	probe := &statusProbe{header: http.Header{}}
	h.ServeHTTP(probe, r)
	if probe.status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", probe.header.Get("Allow"))
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method "+r.Method+" not allowed")
		return
	}
	writeError(w, http.StatusNotFound, codeNotFound, "no route for "+r.URL.Path)
}

// statusProbe is a ResponseWriter that only records the status and headers.
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header { return p.header }

func (p *statusProbe) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}
	return len(b), nil
}

func (p *statusProbe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}
