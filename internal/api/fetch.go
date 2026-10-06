package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/ingest"
)

// Ingestor is what the fetch endpoints need from the ingestion service.
type Ingestor interface {
	Start(ctx context.Context, sourceID uuid.UUID, o ingest.RunOptions) (domain.FetchRun, error)
	GetRun(ctx context.Context, id uuid.UUID) (domain.FetchRun, error)
	ListRuns(ctx context.Context, sourceID uuid.UUID, limit int) ([]domain.FetchRun, error)
}

type fetchRunJSON struct {
	ID         uuid.UUID      `json:"id"`
	SourceID   uuid.UUID      `json:"source_id"`
	Trigger    string         `json:"trigger"`
	Status     string         `json:"status"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	DurationMS *int64         `json:"duration_ms"`
	Stats      fetchStatsJSON `json:"stats"`
	Error      string         `json:"error"`
}

type fetchStatsJSON struct {
	Fetched   int `json:"fetched"`
	Inserted  int `json:"inserted"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Duplicate int `json:"duplicate"`
	Rejected  int `json:"rejected"`
}

func toFetchRunJSON(r domain.FetchRun) fetchRunJSON {
	out := fetchRunJSON{
		ID: r.ID, SourceID: r.SourceID, Trigger: string(r.Trigger), Status: string(r.Status),
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Error: r.Error,
		Stats: fetchStatsJSON{
			Fetched: r.Stats.Fetched, Inserted: r.Stats.Inserted, Updated: r.Stats.Updated,
			Unchanged: r.Stats.Unchanged, Duplicate: r.Stats.Duplicate, Rejected: r.Stats.Rejected,
		},
	}
	if r.FinishedAt != nil {
		ms := r.Duration().Milliseconds()
		out.DurationMS = &ms
	}
	return out
}

// handleStartFetch triggers an asynchronous fetch of a source and returns
// 202 with the new run; poll GET /api/v1/fetch-runs/{id} for the outcome.
// ?force=true bypasses the source's min_fetch_interval cooldown.
func (s *Server) handleStartFetch(w http.ResponseWriter, r *http.Request) {
	force := false
	if v := r.URL.Query().Get("force"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeQueryError(w, "force", "must be true or false")
			return
		}
		force = b
	}
	src, err := s.sources.Get(r.Context(), r.PathValue("ref"))
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	run, err := s.ingest.Start(r.Context(), src.ID, ingest.RunOptions{Trigger: domain.TriggerAPI, Force: force})
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	w.Header().Set("Location", "/api/v1/fetch-runs/"+run.ID.String())
	writeJSON(w, http.StatusAccepted, toFetchRunJSON(run))
}

func (s *Server) handleGetFetchRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "fetch run not found")
		return
	}
	run, err := s.ingest.GetRun(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, r, "fetch run", err)
		return
	}
	writeJSON(w, http.StatusOK, toFetchRunJSON(run))
}

// handleListSourceRuns lists a source's recent runs, newest first.
// ?limit= (1-100, default 20).
func (s *Server) handleListSourceRuns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeQueryError(w, "limit", "must be an integer between 1 and 100")
			return
		}
		limit = n
	}
	src, err := s.sources.Get(r.Context(), r.PathValue("ref"))
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	runs, err := s.ingest.ListRuns(r.Context(), src.ID, limit)
	if err != nil {
		s.writeServiceError(w, r, "fetch run", err)
		return
	}
	out := make([]fetchRunJSON, len(runs))
	for i, run := range runs {
		out[i] = toFetchRunJSON(run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out, "count": len(out)})
}

func writeQueryError(w http.ResponseWriter, field, msg string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
		Code: codeInvalidQuery, Message: "invalid query parameters",
		Details: []domain.FieldError{{Field: field, Message: msg}},
	}})
}
