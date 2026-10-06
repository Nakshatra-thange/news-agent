package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// SourceRegistry is what the source endpoints need from the registry service.
type SourceRegistry interface {
	Types() []sources.TypeInfo
	List(ctx context.Context, f sources.ListFilter) ([]domain.Source, error)
	Get(ctx context.Context, ref string) (domain.Source, error)
	Register(ctx context.Context, n domain.NewSource) (domain.Source, error)
	Update(ctx context.Context, ref string, p domain.SourcePatch) (domain.Source, error)
}

// ---- Response representations ----

type sourceJSON struct {
	ID                      uuid.UUID        `json:"id"`
	Slug                    string           `json:"slug"`
	Name                    string           `json:"name"`
	Type                    string           `json:"type"`
	URL                     string           `json:"url"`
	Status                  string           `json:"status"`
	Priority                int              `json:"priority"`
	Config                  json.RawMessage  `json:"config"`
	State                   json.RawMessage  `json:"state"`
	MinFetchIntervalSeconds int64            `json:"min_fetch_interval_seconds"`
	Health                  sourceHealthJSON `json:"health"`
	CreatedAt               time.Time        `json:"created_at"`
	UpdatedAt               time.Time        `json:"updated_at"`
	RetiredAt               *time.Time       `json:"retired_at"`
}

type sourceHealthJSON struct {
	Status              string     `json:"status"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastFetchAt         *time.Time `json:"last_fetch_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastFailureAt       *time.Time `json:"last_failure_at"`
	LastError           string     `json:"last_error"`
}

func toSourceJSON(s domain.Source) sourceJSON {
	return sourceJSON{
		ID: s.ID, Slug: s.Slug, Name: s.Name, Type: string(s.Type), URL: s.URL,
		Status: string(s.Status), Priority: s.Priority, Config: s.Config, State: s.State,
		MinFetchIntervalSeconds: int64(s.MinFetchInterval / time.Second),
		Health: sourceHealthJSON{
			Status:              string(s.Health()),
			ConsecutiveFailures: s.ConsecutiveFailures,
			LastFetchAt:         s.LastFetchAt,
			LastSuccessAt:       s.LastSuccessAt,
			LastFailureAt:       s.LastFailureAt,
			LastError:           s.LastError,
		},
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, RetiredAt: s.RetiredAt,
	}
}

type sourceListJSON struct {
	Sources []sourceJSON `json:"sources"`
	Count   int          `json:"count"`
}

type sourceTypeJSON struct {
	Type          string          `json:"type"`
	Description   string          `json:"description"`
	DefaultConfig json.RawMessage `json:"default_config"`
	FetchPolicy   fetchPolicyJSON `json:"fetch_policy"`
}

type fetchPolicyJSON struct {
	DefaultIntervalSeconds int64 `json:"default_interval_seconds"`
	MinIntervalSeconds     int64 `json:"min_interval_seconds"`
}

// ---- Request representations ----

type createSourceRequest struct {
	Slug                    string          `json:"slug"`
	Name                    string          `json:"name"`
	Type                    string          `json:"type"`
	URL                     string          `json:"url"`
	Status                  string          `json:"status"`
	Priority                int             `json:"priority"`
	Config                  json.RawMessage `json:"config"`
	MinFetchIntervalSeconds *int64          `json:"min_fetch_interval_seconds"`
}

// updateSourceRequest is a partial update: absent (or null) fields are left
// unchanged. slug and type are immutable and therefore not accepted.
type updateSourceRequest struct {
	Name                    *string          `json:"name"`
	URL                     *string          `json:"url"`
	Status                  *string          `json:"status"`
	Priority                *int             `json:"priority"`
	Config                  *json.RawMessage `json:"config"`
	MinFetchIntervalSeconds *int64           `json:"min_fetch_interval_seconds"`
}

// ---- Handlers ----

func (s *Server) handleListSourceTypes(w http.ResponseWriter, r *http.Request) {
	types := s.sources.Types()
	out := make([]sourceTypeJSON, len(types))
	for i, t := range types {
		out[i] = sourceTypeJSON{
			Type: string(t.Type), Description: t.Description, DefaultConfig: t.DefaultConfig,
			FetchPolicy: fetchPolicyJSON{
				DefaultIntervalSeconds: int64(t.FetchPolicy.DefaultInterval / time.Second),
				MinIntervalSeconds:     int64(t.FetchPolicy.MinInterval / time.Second),
			},
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source_types": out})
}

// handleListSources lists sources, highest priority first.
//
// Query parameters:
//
//	status  repeatable or comma-separated: active, paused, retired, or "all".
//	        Default: active and paused (retired sources are hidden).
//	type    hackernews, arxiv or github.
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	f, fields := parseSourceFilter(r.URL.Query())
	if len(fields) > 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
			Code: codeInvalidQuery, Message: "invalid query parameters", Details: fields,
		}})
		return
	}
	list, err := s.sources.List(r.Context(), f)
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	out := sourceListJSON{Sources: make([]sourceJSON, len(list)), Count: len(list)}
	for i, src := range list {
		out.Sources[i] = toSourceJSON(src)
	}
	writeJSON(w, http.StatusOK, out)
}

func parseSourceFilter(q url.Values) (sources.ListFilter, []domain.FieldError) {
	var (
		f      sources.ListFilter
		fields []domain.FieldError
		all    bool
	)
	for _, v := range q["status"] {
		for _, st := range strings.Split(v, ",") {
			st = strings.TrimSpace(st)
			switch {
			case st == "all":
				all = true
			case domain.SourceStatus(st).Valid():
				f.Statuses = append(f.Statuses, domain.SourceStatus(st))
			default:
				fields = append(fields, domain.FieldError{Field: "status", Message: "must be active, paused, retired or all; got " + quote(st)})
			}
		}
	}
	switch {
	case all:
		f.Statuses = nil
	case len(f.Statuses) == 0:
		f.Statuses = []domain.SourceStatus{domain.SourceStatusActive, domain.SourceStatusPaused}
	}
	if t := q.Get("type"); t != "" {
		if !domain.SourceType(t).Valid() {
			fields = append(fields, domain.FieldError{Field: "type", Message: "unknown source type " + quote(t)})
		}
		f.Type = domain.SourceType(t)
	}
	return f, fields
}

// handleGetSource returns one source; {ref} is its UUID or slug.
func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.Context(), r.PathValue("ref"))
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	writeJSON(w, http.StatusOK, toSourceJSON(src))
}

// handleCreateSource registers a new source. Omitted config fields take the
// type's defaults; an omitted interval takes the type's default interval.
func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var req createSourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	n := domain.NewSource{
		Slug: req.Slug, Name: req.Name, Type: domain.SourceType(req.Type), URL: req.URL,
		Status: domain.SourceStatus(req.Status), Priority: req.Priority, Config: req.Config,
	}
	if req.MinFetchIntervalSeconds != nil {
		d, fe := secondsToDuration(*req.MinFetchIntervalSeconds)
		if fe != nil {
			writeValidationError(w, []domain.FieldError{*fe})
			return
		}
		n.MinFetchInterval = &d
	}

	src, err := s.sources.Register(r.Context(), n)
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	w.Header().Set("Location", "/api/v1/sources/"+src.ID.String())
	writeJSON(w, http.StatusCreated, toSourceJSON(src))
}

// handleUpdateSource partially updates a source; {ref} is its UUID or slug.
// Pause, resume, retire and restore are status changes made here.
func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	var req updateSourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := domain.SourcePatch{Name: req.Name, URL: req.URL, Priority: req.Priority, Config: req.Config}
	if req.Status != nil {
		st := domain.SourceStatus(*req.Status)
		p.Status = &st
	}
	if req.MinFetchIntervalSeconds != nil {
		d, fe := secondsToDuration(*req.MinFetchIntervalSeconds)
		if fe != nil {
			writeValidationError(w, []domain.FieldError{*fe})
			return
		}
		p.MinFetchInterval = &d
	}

	src, err := s.sources.Update(r.Context(), r.PathValue("ref"), p)
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	writeJSON(w, http.StatusOK, toSourceJSON(src))
}

func secondsToDuration(sec int64) (time.Duration, *domain.FieldError) {
	if sec < 0 || sec > math.MaxInt32 {
		return 0, &domain.FieldError{Field: "min_fetch_interval_seconds", Message: "must be between 0 and 2147483647"}
	}
	return time.Duration(sec) * time.Second, nil
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
