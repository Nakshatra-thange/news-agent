package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/ingest"
)

type fakeIngestor struct {
	run      domain.FetchRun
	runs     []domain.FetchRun
	err      error
	gotOpts  ingest.RunOptions
	gotID    uuid.UUID
	gotLimit int
}

func (f *fakeIngestor) Start(_ context.Context, id uuid.UUID, o ingest.RunOptions) (domain.FetchRun, error) {
	f.gotID, f.gotOpts = id, o
	return f.run, f.err
}

func (f *fakeIngestor) GetRun(_ context.Context, id uuid.UUID) (domain.FetchRun, error) {
	f.gotID = id
	return f.run, f.err
}

func (f *fakeIngestor) ListRuns(_ context.Context, id uuid.UUID, limit int) ([]domain.FetchRun, error) {
	f.gotID, f.gotLimit = id, limit
	return f.runs, f.err
}

func sampleRun() domain.FetchRun {
	start := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	return domain.FetchRun{
		ID: uuid.MustParse("0199b000-0000-7000-8000-000000000001"), SourceID: sampleSource().ID,
		Trigger: domain.TriggerAPI, Status: domain.RunRunning, StartedAt: start,
	}
}

func newFetchServer(t *testing.T) (*Server, *fakeRegistry, *fakeIngestor) {
	t.Helper()
	reg := &fakeRegistry{source: sampleSource()}
	ing := &fakeIngestor{run: sampleRun()}
	return New(Options{Sources: reg, Ingest: ing}), reg, ing
}

func TestFetchRoutesNeedIngestor(t *testing.T) {
	s := New(Options{Sources: &fakeRegistry{source: sampleSource()}})
	if rec := do(t, s, http.MethodPost, "/api/v1/sources/x/fetch", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 without an ingestor", rec.Code)
	}
}

func TestStartFetch(t *testing.T) {
	s, reg, ing := newFetchServer(t)
	rec := do(t, s, http.MethodPost, "/api/v1/sources/arxiv-ai/fetch", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if reg.gotRef != "arxiv-ai" || ing.gotID != sampleSource().ID {
		t.Errorf("ref=%q source id=%s", reg.gotRef, ing.gotID)
	}
	if ing.gotOpts != (ingest.RunOptions{Trigger: domain.TriggerAPI}) {
		t.Errorf("options = %+v, want api trigger without force", ing.gotOpts)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/fetch-runs/"+sampleRun().ID.String() {
		t.Errorf("Location = %q", loc)
	}
	body := decode[fetchRunJSON](t, rec)
	if body.Status != "running" || body.FinishedAt != nil || body.DurationMS != nil {
		t.Errorf("body = %+v", body)
	}
}

func TestStartFetchForce(t *testing.T) {
	for q, want := range map[string]bool{"?force=true": true, "?force=1": true, "?force=false": false} {
		s, _, ing := newFetchServer(t)
		do(t, s, http.MethodPost, "/api/v1/sources/x/fetch"+q, nil)
		if ing.gotOpts.Force != want {
			t.Errorf("%s: force = %v, want %v", q, ing.gotOpts.Force, want)
		}
	}
	s, _, _ := newFetchServer(t)
	if rec := do(t, s, http.MethodPost, "/api/v1/sources/x/fetch?force=maybe", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("force=maybe status = %d, want 400", rec.Code)
	}
}

func TestStartFetchErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{"cooldown", &domain.CooldownError{Slug: "hn-ai", Remaining: 90*time.Second + time.Millisecond}, http.StatusTooManyRequests, codeTooManyRequests},
		{"in progress", domain.ErrFetchInProgress, http.StatusConflict, codeConflict},
		{"inactive", fmt.Errorf("%w: source %q is paused", domain.ErrConflict, "hn-ai"), http.StatusConflict, codeConflict},
		{"no adapter", fmt.Errorf("%w: no fetch adapter is registered for source type %q", domain.ErrUnsupported, "github"), http.StatusNotImplemented, codeNotImplemented},
		{"shutting down", ingest.ErrClosed, http.StatusServiceUnavailable, codeUnavailable},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError, codeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, ing := newFetchServer(t)
			ing.err = tt.err
			rec := do(t, s, http.MethodPost, "/api/v1/sources/hn-ai/fetch", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Code != tt.wantErr {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantErr)
			}
			if tt.name == "cooldown" {
				if ra := rec.Header().Get("Retry-After"); ra != "91" {
					t.Errorf("Retry-After = %q, want 91 (rounded up)", ra)
				}
				if !strings.Contains(body.Error.Message, "force=true") {
					t.Errorf("message = %q, want a hint about force", body.Error.Message)
				}
			}
			if tt.name == "no adapter" && body.Error.Message != `no fetch adapter is registered for source type "github"` {
				t.Errorf("message = %q", body.Error.Message)
			}
		})
	}
}

func TestStartFetchUnknownSource(t *testing.T) {
	s, reg, ing := newFetchServer(t)
	reg.err = domain.ErrNotFound
	if rec := do(t, s, http.MethodPost, "/api/v1/sources/nope/fetch", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ing.gotID != uuid.Nil {
		t.Error("ingestor called for an unknown source")
	}
}

func TestGetFetchRun(t *testing.T) {
	s, _, ing := newFetchServer(t)
	run := sampleRun()
	fin := run.StartedAt.Add(1500 * time.Millisecond)
	run.Status, run.FinishedAt = domain.RunFailed, &fin
	run.Stats = domain.FetchStats{Fetched: 6, Inserted: 3, Updated: 1, Unchanged: 0, Duplicate: 1, Rejected: 2}
	run.Error = "fetch: HTTP 503"
	ing.run = run

	rec := do(t, s, http.MethodGet, "/api/v1/fetch-runs/"+run.ID.String(), nil)
	if rec.Code != http.StatusOK || ing.gotID != run.ID {
		t.Fatalf("status = %d id = %s", rec.Code, ing.gotID)
	}
	body := decode[fetchRunJSON](t, rec)
	want := fetchStatsJSON{Fetched: 6, Inserted: 3, Updated: 1, Duplicate: 1, Rejected: 2}
	if body.Stats != want || body.DurationMS == nil || *body.DurationMS != 1500 || body.Error != "fetch: HTTP 503" {
		t.Errorf("body = %+v", body)
	}

	if rec := do(t, s, http.MethodGet, "/api/v1/fetch-runs/not-a-uuid", nil); rec.Code != http.StatusNotFound {
		t.Errorf("bad id status = %d, want 404", rec.Code)
	}
	ing.err = domain.ErrNotFound
	rec = do(t, s, http.MethodGet, "/api/v1/fetch-runs/"+uuid.NewString(), nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "fetch run not found") {
		t.Errorf("missing run: %d %s", rec.Code, rec.Body)
	}
}

func TestListSourceRuns(t *testing.T) {
	s, _, ing := newFetchServer(t)
	ing.runs = []domain.FetchRun{sampleRun(), sampleRun()}
	rec := do(t, s, http.MethodGet, "/api/v1/sources/arxiv-ai/runs?limit=5", nil)
	if rec.Code != http.StatusOK || ing.gotLimit != 5 {
		t.Fatalf("status = %d limit = %d", rec.Code, ing.gotLimit)
	}
	if !strings.Contains(rec.Body.String(), `"count":2`) {
		t.Errorf("body = %s", rec.Body)
	}
	for _, bad := range []string{"0", "101", "x"} {
		if rec := do(t, s, http.MethodGet, "/api/v1/sources/a/runs?limit="+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s status = %d, want 400", bad, rec.Code)
		}
	}
	s2, _, ing2 := newFetchServer(t)
	ing2.runs = nil
	if rec := do(t, s2, http.MethodGet, "/api/v1/sources/a/runs", nil); !strings.Contains(rec.Body.String(), `"runs":[]`) || ing2.gotLimit != 20 {
		t.Errorf("default: limit=%d body=%s", ing2.gotLimit, rec.Body)
	}
}
