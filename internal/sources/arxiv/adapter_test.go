package arxiv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/sources"
)

var fixtureNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeArxiv answers requests from a script of responses, in order (the last
// repeats), and records every query string.
type fakeArxiv struct {
	mu      sync.Mutex
	script  []resp
	queries []url.Values
	delay   time.Duration
}

type resp struct {
	status int
	file   string
	header http.Header
}

func newFakeArxiv(t *testing.T, script ...resp) (*fakeArxiv, string) {
	f := &fakeArxiv{script: script}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL + "/api/query"
}

func (f *fakeArxiv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.queries = append(f.queries, r.URL.Query())
	n := len(f.queries)
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	rs := f.script[min(n, len(f.script))-1]
	for k, vs := range rs.header {
		w.Header()[k] = vs
	}
	status := rs.status
	if status == 0 {
		status = 200
	}
	w.Header().Set("Content-Type", "application/atom+xml")
	w.WriteHeader(status)
	if rs.file != "" {
		b, _ := os.ReadFile("testdata/" + rs.file)
		_, _ = w.Write(b)
	}
}

func (f *fakeArxiv) requests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.queries...)
}

func newTestAdapter(base string, pageSize int) *Adapter {
	a := NewAdapter(sources.ClientOptions{
		BaseURL:         base,
		RequestInterval: time.Millisecond,
		Retry:           httpx.RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
		Now:             func() time.Time { return fixtureNow },
	})
	if pageSize > 0 {
		a.PageSize = pageSize
	}
	return a
}

func source(config string) domain.Source {
	if config == "" {
		config = "{}"
	}
	return domain.Source{Slug: "arxiv-ai", Type: domain.SourceTypeArxiv, Config: json.RawMessage(config), State: json.RawMessage("{}")}
}

func ids(items []domain.Candidate) []string {
	out := make([]string, len(items))
	for i, c := range items {
		out[i] = c.ExternalID
	}
	return out
}

func TestFetchParsesEntriesAndBuildsQuery(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{file: "page1.xml"})
	res, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Equal(ids(res.Items), []string{"2410.12345", "2410.22222", "2410.33333"}) || res.Requests != 1 || res.State != nil {
		t.Fatalf("items=%v requests=%d state=%s", ids(res.Items), res.Requests, res.State)
	}

	q := fake.requests()[0]
	wantQuery := "(cat:cs.AI OR cat:cs.LG OR cat:cs.CL) AND submittedDate:[202610031200 TO 202610061200]"
	if q.Get("search_query") != wantQuery || q.Get("sortBy") != "submittedDate" || q.Get("sortOrder") != "descending" ||
		q.Get("start") != "0" || q.Get("max_results") != "100" {
		t.Errorf("query = %v", q)
	}

	p := res.Items[0]
	if p.Kind != domain.ItemKindPaper || p.URL != "https://arxiv.org/abs/2410.12345" ||
		!strings.HasPrefix(p.Title, "Scaling Laws for") || !strings.Contains(p.Description, "tools & their quality") ||
		!slices.Equal(p.Authors, []string{"Ada Lovelace", "Alan Turing"}) || !slices.Equal(p.Tags, []string{"cs.AI", "cs.LG"}) {
		t.Errorf("paper = %+v", p)
	}
	if p.PublishedAt == nil || !p.PublishedAt.Equal(time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("published = %v", p.PublishedAt)
	}
	var meta map[string]string
	_ = json.Unmarshal(p.Metadata, &meta)
	want := map[string]string{
		"version": "v2", "primary_category": "cs.AI", "pdf_url": "http://arxiv.org/pdf/2410.12345v2",
		"comment": "12 pages, 4 figures", "doi": "10.1234/example.5678", "updated_at": "2026-10-05T17:59:00Z",
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("metadata[%s] = %q, want %q", k, meta[k], v)
		}
	}

	sparse := res.Items[2] // missing summary, authors, categories, links
	if sparse.Description != "" || len(sparse.Authors) != 0 || len(sparse.Tags) != 0 || sparse.URL != "https://arxiv.org/abs/2410.33333" {
		t.Errorf("sparse entry = %+v", sparse)
	}
}

func TestFetchPaginatesAndDedupes(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{file: "page1.xml"}, resp{file: "page2.xml"})
	res, err := newTestAdapter(base, 3).Fetch(context.Background(), source(`{"lookback":"7d"}`))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// page2 repeats 2410.22222 (the result set shifted); it is kept once.
	if !slices.Equal(ids(res.Items), []string{"2410.12345", "2410.22222", "2410.33333", "2410.44444"}) || res.Requests != 2 {
		t.Errorf("items=%v requests=%d", ids(res.Items), res.Requests)
	}
	reqs := fake.requests()
	if len(reqs) != 2 || reqs[0].Get("start") != "0" || reqs[1].Get("start") != "3" || reqs[1].Get("max_results") != "3" {
		t.Errorf("pagination requests = %v", reqs)
	}
}

func TestFetchStopsAtLookbackBoundary(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{file: "old.xml"})
	res, err := newTestAdapter(base, 3).Fetch(context.Background(), source(""))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Equal(ids(res.Items), []string{"2410.12345"}) || len(fake.requests()) != 1 {
		t.Errorf("items=%v requests=%d; no page beyond the window should be fetched", ids(res.Items), len(fake.requests()))
	}
}

func TestFetchHonorsMaxResults(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{file: "page1.xml"})
	res, err := newTestAdapter(base, 0).Fetch(context.Background(), source(`{"max_results":2}`))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 || fake.requests()[0].Get("max_results") != "2" {
		t.Errorf("items=%d max_results=%s", len(res.Items), fake.requests()[0].Get("max_results"))
	}
}

func TestFetchCategoriesFromConfig(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{file: "empty_results.xml"})
	if _, err := newTestAdapter(base, 0).Fetch(context.Background(), source(`{"categories":["cs.RO","stat.ML"],"lookback":"1d"}`)); err != nil {
		t.Fatal(err)
	}
	if got := fake.requests()[0].Get("search_query"); got != "(cat:cs.RO OR cat:stat.ML) AND submittedDate:[202610051200 TO 202610061200]" {
		t.Errorf("search_query = %q", got)
	}
}

func TestFetchEmptyResults(t *testing.T) {
	_, base := newFakeArxiv(t, resp{file: "empty_results.xml"})
	res, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
	if err != nil || len(res.Items) != 0 || res.Requests != 1 {
		t.Errorf("items=%d requests=%d err=%v", len(res.Items), res.Requests, err)
	}
}

func TestFetchEmptyPageGlitch(t *testing.T) {
	t.Run("retried once", func(t *testing.T) {
		_, base := newFakeArxiv(t, resp{file: "empty_glitch.xml"}, resp{file: "page1.xml"})
		res, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
		if err != nil || len(res.Items) != 3 || res.Requests != 2 {
			t.Errorf("items=%d requests=%d err=%v", len(res.Items), res.Requests, err)
		}
	})
	t.Run("persistent", func(t *testing.T) {
		fake, base := newFakeArxiv(t, resp{file: "empty_glitch.xml"})
		_, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
		if err == nil || !strings.Contains(err.Error(), "empty page") || len(fake.requests()) != 2 {
			t.Errorf("err=%v requests=%d", err, len(fake.requests()))
		}
	})
}

func TestFetchAPIErrorFeed(t *testing.T) {
	t.Run("HTTP 200 error entry", func(t *testing.T) {
		_, base := newFakeArxiv(t, resp{file: "error.xml"})
		_, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
		if err == nil || !strings.Contains(err.Error(), "arXiv API error: incorrect id format for 1234") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("HTTP 400 is not retried", func(t *testing.T) {
		fake, base := newFakeArxiv(t, resp{status: 400, file: "error.xml"})
		_, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
		se, ok := httpx.AsStatusError(err)
		if !ok || se.StatusCode != 400 || len(fake.requests()) != 1 {
			t.Errorf("err=%v requests=%d", err, len(fake.requests()))
		}
	})
}

func TestFetchMalformedFeed(t *testing.T) {
	_, base := newFakeArxiv(t, resp{file: "malformed.xml"})
	_, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
	if err == nil || !strings.Contains(err.Error(), "malformed arXiv feed") {
		t.Errorf("error = %v", err)
	}
}

func TestFetchRetriesTransientErrors(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{status: 503}, resp{file: "page1.xml"})
	res, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
	if err != nil || len(res.Items) != 3 || len(fake.requests()) != 2 || res.Requests != 2 {
		t.Errorf("items=%d hits=%d requests=%d err=%v", len(res.Items), len(fake.requests()), res.Requests, err)
	}
}

func TestFetchRateLimitedFailsFast(t *testing.T) {
	_, base := newFakeArxiv(t, resp{status: 429, header: http.Header{"Retry-After": {"7200"}}})
	start := time.Now()
	_, err := newTestAdapter(base, 0).Fetch(context.Background(), source(""))
	if se, ok := httpx.AsStatusError(err); !ok || !se.RateLimited || time.Since(start) > 2*time.Second {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
}

func TestFetchPartialFailureKeepsEarlierPages(t *testing.T) {
	_, base := newFakeArxiv(t, resp{file: "page1.xml"}, resp{status: 500})
	res, err := newTestAdapter(base, 3).Fetch(context.Background(), source(`{"lookback":"7d"}`))
	if err == nil || !strings.Contains(err.Error(), "page at offset 3") {
		t.Fatalf("error = %v", err)
	}
	if len(res.Items) != 3 {
		t.Errorf("items = %d, want the first page kept", len(res.Items))
	}
}

func TestFetchContextCancellation(t *testing.T) {
	fake, base := newFakeArxiv(t, resp{file: "page1.xml"})
	fake.delay = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newTestAdapter(base, 0).Fetch(ctx, source(""))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
}

func TestFetchInvalidConfig(t *testing.T) {
	_, base := newFakeArxiv(t, resp{file: "page1.xml"})
	if _, err := newTestAdapter(base, 0).Fetch(context.Background(), source(`{"categories":["CS AI"]}`)); err == nil ||
		!strings.Contains(err.Error(), "invalid arxiv config") {
		t.Errorf("error = %v", err)
	}
}

func TestDefaultsArePolite(t *testing.T) {
	a := NewAdapter(sources.ClientOptions{})
	if a.PageSize != 100 || a.base != DefaultBaseURL || requestInterval < 3*time.Second {
		t.Errorf("page size %d, base %s, interval %v", a.PageSize, a.base, requestInterval)
	}
}
