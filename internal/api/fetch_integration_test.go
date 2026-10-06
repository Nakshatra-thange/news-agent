package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"synergy/internal/api"
	"synergy/internal/domain"
	"synergy/internal/ingest"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/sources/sourcestest"
	"synergy/internal/store/storetest"
)

// End-to-end: HTTP -> api -> ingest (background) -> fake adapter -> store -> PostgreSQL.
func TestIntegrationAsyncFetch(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	reg, err := sources.NewRegistry(st, nil, hackernews.Spec{}, arxiv.Spec{}, github.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	fa := &sourcestest.FakeAdapter{SourceType: domain.SourceTypeHackerNews, Steps: []sourcestest.Step{{
		Result: sources.FetchResult{Items: sourcestest.LoadCandidates(t, "../ingest/testdata/hn_batch.json")},
	}}}
	svc, err := ingest.New(st, []sources.Adapter{fa}, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(api.Options{Version: "test", DB: st, Sources: reg, Ingest: svc}))
	t.Cleanup(srv.Close)

	// Trigger: 202 with a running run.
	started := call(t, srv, "POST", "/api/v1/sources/hn-ai/fetch", "")
	if started.code != http.StatusAccepted || started.str("status") != "running" || started.str("trigger") != "api" {
		t.Fatalf("start: %d %v", started.code, started.body)
	}
	runID := started.str("id")

	// Poll until the background fetch finishes.
	var final apiResp
	for deadline := time.Now().Add(5 * time.Second); ; {
		final = call(t, srv, "GET", "/api/v1/fetch-runs/"+runID, "")
		if final.str("status") != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fetch did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stats, _ := json.Marshal(final.body["stats"])
	if final.str("status") != "succeeded" || string(stats) != `{"duplicate":0,"fetched":6,"inserted":4,"rejected":2,"unchanged":0,"updated":0}` {
		t.Errorf("final run: %v stats=%s", final.body, stats)
	}

	// Source health reflects the success.
	src := call(t, srv, "GET", "/api/v1/sources/hn-ai", "")
	if h := src.body["health"].(map[string]any); h["status"] != "healthy" || h["last_fetch_at"] == nil {
		t.Errorf("health = %v", h)
	}

	// Immediately again: cooldown, with Retry-After.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/sources/hn-ai/fetch", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Errorf("cooldown: status=%d Retry-After=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	// Forced: accepted.
	if forced := call(t, srv, "POST", "/api/v1/sources/hn-ai/fetch?force=true", ""); forced.code != http.StatusAccepted {
		t.Errorf("forced: %d %v", forced.code, forced.body)
	}

	// No adapter for arXiv yet (Stage 5): 501, and no run is created.
	if na := call(t, srv, "POST", "/api/v1/sources/arxiv-ai/fetch", ""); na.code != http.StatusNotImplemented || na.errCode() != "not_implemented" {
		t.Errorf("no adapter: %d %v", na.code, na.body)
	}
	if runs := call(t, srv, "GET", "/api/v1/sources/arxiv-ai/runs", ""); runs.body["count"] != float64(0) {
		t.Errorf("arxiv runs = %v, want none", runs.body)
	}
	// Wait for the forced fetch to finish, then both runs are listed.
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if runs := call(t, srv, "GET", "/api/v1/sources/hn-ai/runs", ""); runs.body["count"] != float64(2) {
		t.Errorf("hn runs = %v, want 2", runs.body["count"])
	}
}
