package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// fakeRegistry records calls and returns canned results.
type fakeRegistry struct {
	source    domain.Source
	list      []domain.Source
	err       error
	gotFilter sources.ListFilter
	gotRef    string
	gotNew    domain.NewSource
	gotPatch  domain.SourcePatch
}

func (f *fakeRegistry) Types() []sources.TypeInfo {
	return []sources.TypeInfo{{
		Type: domain.SourceTypeArxiv, Description: "arXiv papers",
		DefaultConfig: json.RawMessage(`{"categories":["cs.AI"]}`),
		FetchPolicy:   sources.FetchPolicy{DefaultInterval: 3 * time.Hour, MinInterval: 30 * time.Minute},
	}}
}

func (f *fakeRegistry) List(_ context.Context, filter sources.ListFilter) ([]domain.Source, error) {
	f.gotFilter = filter
	return f.list, f.err
}

func (f *fakeRegistry) Get(_ context.Context, ref string) (domain.Source, error) {
	f.gotRef = ref
	return f.source, f.err
}

func (f *fakeRegistry) Register(_ context.Context, n domain.NewSource) (domain.Source, error) {
	f.gotNew = n
	return f.source, f.err
}

func (f *fakeRegistry) Update(_ context.Context, ref string, p domain.SourcePatch) (domain.Source, error) {
	f.gotRef, f.gotPatch = ref, p
	return f.source, f.err
}

func sampleSource() domain.Source {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return domain.Source{
		ID: uuid.MustParse("0199a000-0000-7000-8000-000000000001"), Slug: "arxiv-ai", Name: "arXiv AI",
		Type: domain.SourceTypeArxiv, URL: "https://arxiv.org", Status: domain.SourceStatusActive, Priority: 40,
		Config: json.RawMessage(`{"categories":["cs.AI"]}`), State: json.RawMessage(`{}`),
		MinFetchInterval: 3 * time.Hour, ConsecutiveFailures: 1, LastFailureAt: &now, LastError: "503",
		CreatedAt: now, UpdatedAt: now,
	}
}

func newSourceServer(t *testing.T) (*Server, *fakeRegistry) {
	t.Helper()
	reg := &fakeRegistry{source: sampleSource(), list: []domain.Source{sampleSource()}}
	return New(Options{Version: "test", Sources: reg}), reg
}

func send(t *testing.T, h http.Handler, method, path, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSourceRoutesNotMountedWithoutRegistry(t *testing.T) {
	s, _ := newTestServer(t)
	if rec := do(t, s, http.MethodGet, "/api/v1/sources", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no registry is configured", rec.Code)
	}
}

func TestListSourceTypes(t *testing.T) {
	s, _ := newSourceServer(t)
	rec := do(t, s, http.MethodGet, "/api/v1/source-types", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decode[struct {
		SourceTypes []sourceTypeJSON `json:"source_types"`
	}](t, rec)
	if len(body.SourceTypes) != 1 {
		t.Fatalf("types = %+v", body.SourceTypes)
	}
	got := body.SourceTypes[0]
	if got.Type != "arxiv" || got.FetchPolicy.DefaultIntervalSeconds != 10800 || got.FetchPolicy.MinIntervalSeconds != 1800 ||
		string(got.DefaultConfig) != `{"categories":["cs.AI"]}` {
		t.Errorf("type = %+v", got)
	}
}

func TestListSources(t *testing.T) {
	s, reg := newSourceServer(t)
	rec := do(t, s, http.MethodGet, "/api/v1/sources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := decode[sourceListJSON](t, rec)
	if body.Count != 1 || len(body.Sources) != 1 || body.Sources[0].Slug != "arxiv-ai" {
		t.Errorf("body = %+v", body)
	}
	if want := []domain.SourceStatus{domain.SourceStatusActive, domain.SourceStatusPaused}; !slices.Equal(reg.gotFilter.Statuses, want) {
		t.Errorf("default statuses = %v, want %v (retired hidden)", reg.gotFilter.Statuses, want)
	}
}

func TestListSourcesEmptyIsArray(t *testing.T) {
	s, reg := newSourceServer(t)
	reg.list = []domain.Source{}
	rec := do(t, s, http.MethodGet, "/api/v1/sources", nil)
	if !strings.Contains(rec.Body.String(), `"sources":[]`) {
		t.Errorf("empty list body = %s, want sources: []", rec.Body)
	}
}

func TestListSourcesQueryParsing(t *testing.T) {
	tests := []struct {
		query    string
		statuses []domain.SourceStatus
		typ      domain.SourceType
	}{
		{"?status=all", nil, ""},
		{"?status=retired", []domain.SourceStatus{domain.SourceStatusRetired}, ""},
		{"?status=active,paused&status=retired", []domain.SourceStatus{domain.SourceStatusActive, domain.SourceStatusPaused, domain.SourceStatusRetired}, ""},
		{"?status=paused&status=all", nil, ""},
		{"?type=github", []domain.SourceStatus{domain.SourceStatusActive, domain.SourceStatusPaused}, domain.SourceTypeGitHub},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			s, reg := newSourceServer(t)
			if rec := do(t, s, http.MethodGet, "/api/v1/sources"+tt.query, nil); rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			if !slices.Equal(reg.gotFilter.Statuses, tt.statuses) || reg.gotFilter.Type != tt.typ {
				t.Errorf("filter = %+v, want statuses %v type %q", reg.gotFilter, tt.statuses, tt.typ)
			}
		})
	}

	for _, bad := range []string{"?status=deleted", "?type=reddit", "?status=active,,bogus"} {
		s, _ := newSourceServer(t)
		rec := do(t, s, http.MethodGet, "/api/v1/sources"+bad, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, rec.Code)
			continue
		}
		if body := decode[errorBody](t, rec); body.Error.Code != codeInvalidQuery || len(body.Error.Details) == 0 {
			t.Errorf("%s: error = %+v", bad, body.Error)
		}
	}
}

func TestGetSource(t *testing.T) {
	s, reg := newSourceServer(t)
	rec := do(t, s, http.MethodGet, "/api/v1/sources/arxiv-ai", nil)
	if rec.Code != http.StatusOK || reg.gotRef != "arxiv-ai" {
		t.Fatalf("status = %d ref = %q", rec.Code, reg.gotRef)
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "slug", "name", "type", "url", "status", "priority", "config", "state",
		"min_fetch_interval_seconds", "health", "created_at", "updated_at", "retired_at"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("response missing %q", key)
		}
	}
	body := decode[sourceJSON](t, rec)
	if body.MinFetchIntervalSeconds != 10800 {
		t.Errorf("min_fetch_interval_seconds = %d, want 10800", body.MinFetchIntervalSeconds)
	}
	if h := body.Health; h.Status != "degraded" || h.ConsecutiveFailures != 1 || h.LastError != "503" || h.LastFailureAt == nil {
		t.Errorf("health = %+v", h)
	}
}

func TestServiceErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{"not found", domain.ErrNotFound, http.StatusNotFound, codeNotFound},
		{"conflict", fmt.Errorf("%w: a source with slug \"x\" already exists", domain.ErrConflict), http.StatusConflict, codeConflict},
		{"validation", &domain.ValidationError{Fields: []domain.FieldError{{Field: "config.sort", Message: "bad"}}}, http.StatusUnprocessableEntity, codeValidationFailed},
		{"db constraint", fmt.Errorf("%w: database rejected value (x)", domain.ErrInvalid), http.StatusUnprocessableEntity, codeValidationFailed},
		{"unexpected", errors.New("pq: connection to 10.0.0.9 reset"), http.StatusInternalServerError, codeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, reg := newSourceServer(t)
			reg.err = tt.err
			rec := do(t, s, http.MethodGet, "/api/v1/sources/x", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Code != tt.wantErr {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantErr)
			}
			if strings.Contains(rec.Body.String(), "10.0.0.9") {
				t.Error("internal error details leaked to client")
			}
		})
	}
}

func TestConflictMessageIsReadable(t *testing.T) {
	s, reg := newSourceServer(t)
	reg.err = fmt.Errorf("%w: a source with slug \"x\" already exists", domain.ErrConflict)
	rec := send(t, s, http.MethodPost, "/api/v1/sources", `{"slug":"x","name":"X","type":"arxiv"}`, nil)
	if body := decode[errorBody](t, rec); body.Error.Message != `a source with slug "x" already exists` {
		t.Errorf("message = %q", body.Error.Message)
	}
}

func TestValidationErrorDetails(t *testing.T) {
	s, reg := newSourceServer(t)
	reg.err = &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "slug", Message: "bad slug"}, {Field: "config.queries", Message: "too many"},
	}}
	rec := send(t, s, http.MethodPost, "/api/v1/sources", `{"slug":"X"}`, nil)
	body := decode[errorBody](t, rec)
	if len(body.Error.Details) != 2 || body.Error.Details[1].Field != "config.queries" {
		t.Errorf("details = %+v", body.Error.Details)
	}
}

func TestCreateSource(t *testing.T) {
	s, reg := newSourceServer(t)
	rec := send(t, s, http.MethodPost, "/api/v1/sources", `{
		"slug": "arxiv-ai", "name": "arXiv AI", "type": "arxiv", "url": "https://arxiv.org",
		"status": "paused", "priority": 40, "config": {"categories": ["cs.AI"]},
		"min_fetch_interval_seconds": 7200
	}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/sources/"+sampleSource().ID.String() {
		t.Errorf("Location = %q", loc)
	}
	n := reg.gotNew
	if n.Slug != "arxiv-ai" || n.Type != domain.SourceTypeArxiv || n.Status != domain.SourceStatusPaused || n.Priority != 40 ||
		n.URL != "https://arxiv.org" || n.MinFetchInterval == nil || *n.MinFetchInterval != 2*time.Hour {
		t.Errorf("registered = %+v", n)
	}
	var cfg map[string][]string
	if err := json.Unmarshal(n.Config, &cfg); err != nil || cfg["categories"][0] != "cs.AI" {
		t.Errorf("config passed through = %s", n.Config)
	}
}

func TestCreateSourceOmittedIntervalUsesTypeDefault(t *testing.T) {
	s, reg := newSourceServer(t)
	send(t, s, http.MethodPost, "/api/v1/sources", `{"slug":"a","name":"A","type":"arxiv"}`, nil)
	if reg.gotNew.MinFetchInterval != nil {
		t.Errorf("MinFetchInterval = %v, want nil so the registry applies the type default", *reg.gotNew.MinFetchInterval)
	}
}

func TestCreateSourceBadRequests(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		header   http.Header
		wantCode int
		wantErr  string
	}{
		{"empty body", "", nil, http.StatusBadRequest, codeInvalidJSON},
		{"malformed", `{"slug":`, nil, http.StatusBadRequest, codeInvalidJSON},
		{"array", `[]`, nil, http.StatusBadRequest, codeInvalidJSON},
		{"unknown field", `{"slug":"a","nmae":"typo"}`, nil, http.StatusBadRequest, codeInvalidJSON},
		{"wrong type", `{"priority":"high"}`, nil, http.StatusBadRequest, codeInvalidJSON},
		{"trailing data", `{"slug":"a"} {"slug":"b"}`, nil, http.StatusBadRequest, codeInvalidJSON},
		{"wrong content type", `{"slug":"a"}`, http.Header{"Content-Type": {"text/plain"}}, http.StatusUnsupportedMediaType, codeUnsupportedMediaType},
		{"too large", `{"name":"` + strings.Repeat("x", maxBodyBytes) + `"}`, nil, http.StatusRequestEntityTooLarge, codePayloadTooLarge},
		{"negative interval", `{"slug":"a","min_fetch_interval_seconds":-5}`, nil, http.StatusUnprocessableEntity, codeValidationFailed},
		{"huge interval", `{"slug":"a","min_fetch_interval_seconds":99999999999}`, nil, http.StatusUnprocessableEntity, codeValidationFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, reg := newSourceServer(t)
			rec := send(t, s, http.MethodPost, "/api/v1/sources", tt.body, tt.header)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			if body := decode[errorBody](t, rec); body.Error.Code != tt.wantErr {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantErr)
			}
			if reg.gotNew.Slug != "" {
				t.Error("registry was called for a bad request")
			}
		})
	}
}

func TestCreateSourceAcceptsJSONWithCharset(t *testing.T) {
	s, _ := newSourceServer(t)
	rec := send(t, s, http.MethodPost, "/api/v1/sources", `{"slug":"a","name":"A","type":"arxiv"}`,
		http.Header{"Content-Type": {"application/json; charset=utf-8"}})
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestUpdateSource(t *testing.T) {
	s, reg := newSourceServer(t)
	rec := send(t, s, http.MethodPatch, "/api/v1/sources/arxiv-ai", `{
		"name": "New", "status": "paused", "priority": 7,
		"config": {"max_results": 50}, "min_fetch_interval_seconds": 3600
	}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	p := reg.gotPatch
	if reg.gotRef != "arxiv-ai" || *p.Name != "New" || *p.Status != domain.SourceStatusPaused || *p.Priority != 7 ||
		string(*p.Config) != `{"max_results": 50}` || *p.MinFetchInterval != time.Hour || p.URL != nil {
		t.Errorf("patch = %+v", p)
	}
}

func TestUpdateSourceNullMeansUnchanged(t *testing.T) {
	s, reg := newSourceServer(t)
	send(t, s, http.MethodPatch, "/api/v1/sources/x", `{"config": null, "name": null, "priority": 3}`, nil)
	if reg.gotPatch.Config != nil || reg.gotPatch.Name != nil || *reg.gotPatch.Priority != 3 {
		t.Errorf("patch = %+v, want only priority set", reg.gotPatch)
	}
}

func TestUpdateSourceRejectsImmutableFields(t *testing.T) {
	for _, body := range []string{`{"slug":"renamed"}`, `{"type":"github"}`, `{"id":"x"}`} {
		s, _ := newSourceServer(t)
		rec := send(t, s, http.MethodPatch, "/api/v1/sources/x", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestSourceMethodNotAllowed(t *testing.T) {
	s, _ := newSourceServer(t)
	rec := do(t, s, http.MethodDelete, "/api/v1/sources/x", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405 (sources are retired, not deleted)", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "PATCH") {
		t.Errorf("Allow = %q", allow)
	}
}
