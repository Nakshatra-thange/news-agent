package api

import (
	"context"
	"net/http"
	"time"
)

// DBHealth is what the database health check needs from the data layer.
type DBHealth interface {
	Ping(ctx context.Context) error
	SchemaVersions(ctx context.Context) (current, latest int64, err error)
}

// dbHealthTimeout bounds /health/db so a hung database cannot hang probes.
const dbHealthTimeout = 2 * time.Second

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Database health statuses.
const (
	dbStatusOK                = "ok"
	dbStatusUnavailable       = "unavailable"
	dbStatusMigrationsPending = "migrations_pending"
)

type dbHealthResponse struct {
	Status        string `json:"status"`
	SchemaVersion *int64 `json:"schema_version,omitempty"`
	LatestVersion *int64 `json:"latest_version,omitempty"`
	LatencyMS     int64  `json:"latency_ms"`
	Error         string `json:"error,omitempty"`
}

// handleHealth is a liveness probe: it reports that the process is serving
// HTTP and deliberately does not touch dependencies such as the database.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: s.version})
}

// handleDBHealth checks that PostgreSQL answers queries and that the schema
// is fully migrated. It returns 503 otherwise. Error details are logged, not
// returned, so connection information never leaks to clients.
func (s *Server) handleDBHealth(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, dbHealthResponse{Status: dbStatusUnavailable, Error: "database not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dbHealthTimeout)
	defer cancel()
	start := time.Now()

	unavailable := func(stage string, err error) {
		s.logger.WarnContext(ctx, "database health check failed",
			"request_id", RequestIDFromContext(ctx), "stage", stage, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, dbHealthResponse{
			Status:    dbStatusUnavailable,
			LatencyMS: time.Since(start).Milliseconds(),
			Error:     "database unreachable",
		})
	}

	if err := s.db.Ping(ctx); err != nil {
		unavailable("ping", err)
		return
	}
	current, latest, err := s.db.SchemaVersions(ctx)
	if err != nil {
		unavailable("schema_version", err)
		return
	}

	resp := dbHealthResponse{
		Status:        dbStatusOK,
		SchemaVersion: &current,
		LatestVersion: &latest,
		LatencyMS:     time.Since(start).Milliseconds(),
	}
	status := http.StatusOK
	if current < latest {
		resp.Status = dbStatusMigrationsPending
		resp.Error = "run `synergy migrate up`"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}
