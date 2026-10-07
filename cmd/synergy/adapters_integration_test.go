package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/ingest"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/store"
	"synergy/internal/store/storetest"
)

// End-to-end: the real adapters, pointed at fixture servers replaying each
// adapter package's recorded testdata, run through the real ingestion
// pipeline into PostgreSQL with the seeded source configurations. No
// network access.

var fixtureNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const testdataRoot = "../../internal/sources"

func serveFixture(w http.ResponseWriter, path string) {
	body, err := os.ReadFile(filepath.Join(testdataRoot, path))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(body)
}

// fixtureServers starts one fake upstream per source type. failHNItem, if
// set, makes that Hacker News item answer 404.
func fixtureServers(t *testing.T, failHNItem string) (hn, ax, gh string) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/v0/")
		if failHNItem != "" && p == "item/"+failHNItem+".json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		serveFixture(w, "hackernews/testdata/"+strings.Replace(p, "item/", "items/", 1))
	}))
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFixture(w, "arxiv/testdata/page1.xml")
	}))
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term, _, _ := strings.Cut(r.URL.Query().Get("q"), " ")
		switch term {
		case "topic:llm":
			serveFixture(w, "github/testdata/search_llm.json")
		case "topic:ai-agents":
			serveFixture(w, "github/testdata/search_agents.json")
		default:
			serveFixture(w, "github/testdata/empty.json")
		}
	}))
	for _, s := range []*httptest.Server{h, a, g} {
		t.Cleanup(s.Close)
	}
	return h.URL + "/v0", a.URL, g.URL
}

type e2e struct {
	st  *store.Store
	reg *sources.Registry
	svc *ingest.Service
	ctx context.Context
}

func newE2E(t *testing.T, failHNItem string) *e2e {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	reg, err := newRegistry(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	hnURL, axURL, ghURL := fixtureServers(t, failHNItem)
	opts := func(base string) sources.ClientOptions {
		return sources.ClientOptions{
			BaseURL: base, Limiters: httpx.NewLimiters(), RequestInterval: time.Millisecond,
			Retry: httpx.RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
			Now:   func() time.Time { return fixtureNow },
		}
	}
	svc, err := ingest.New(st, []sources.Adapter{
		hackernews.NewAdapter(opts(hnURL)),
		arxiv.NewAdapter(opts(axURL)),
		github.NewAdapter(opts(ghURL), ""),
	}, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return &e2e{st: st, reg: reg, svc: svc, ctx: ctx}
}

func (e *e2e) fetch(t *testing.T, slug string) (domain.FetchRun, domain.Source) {
	t.Helper()
	src, err := e.reg.Get(e.ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.svc.Run(e.ctx, src.ID, ingest.RunOptions{Trigger: domain.TriggerCLI, Force: true})
	if err != nil {
		t.Fatalf("Run(%s): %v", slug, err)
	}
	after, err := e.reg.Get(e.ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	return run, after
}

func TestAdaptersEndToEnd(t *testing.T) {
	e := newE2E(t, "")
	tests := []struct {
		slug    string
		fetched int
		state   string
	}{
		{"hn-ai", 3, `{"old_id_floor": 950}`},
		{"arxiv-ai", 3, `{}`},
		{"github-ai-repos", 3, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			run, src := e.fetch(t, tt.slug)
			s := run.Stats
			if run.Status != domain.RunSucceeded || s.Fetched != tt.fetched || s.Inserted != tt.fetched || s.Rejected != 0 {
				t.Fatalf("first run = %+v (error %q)", s, run.Error)
			}
			if src.LastSuccessAt == nil || src.ConsecutiveFailures != 0 || src.LastError != "" {
				t.Errorf("health after success = %+v", src)
			}
			if string(src.State) != tt.state {
				t.Errorf("state = %s, want %s", src.State, tt.state)
			}

			// The same upstream data again: nothing new, nothing changed.
			run, _ = e.fetch(t, tt.slug)
			if s := run.Stats; run.Status != domain.RunSucceeded || s.Inserted != 0 || s.Updated != 0 || s.Unchanged != tt.fetched {
				t.Errorf("second run = %+v (error %q)", s, run.Error)
			}
		})
	}

	page, err := e.st.ListItems(e.ctx, domain.ItemFilter{Limit: 100, IncludeDuplicates: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 9 {
		t.Errorf("stored %d items, want 9", len(page.Items))
	}
	for _, it := range page.Items {
		if it.Kind == domain.ItemKindPaper && it.CanonicalURL != "https://arxiv.org/abs/"+it.ExternalID {
			t.Errorf("arXiv canonical URL = %s for %s", it.CanonicalURL, it.ExternalID)
		}
	}
}

func TestAdapterPartialFailureKeepsItemsButNotCursor(t *testing.T) {
	e := newE2E(t, "1002")
	run, src := e.fetch(t, "hn-ai")
	if run.Status != domain.RunFailed || !strings.Contains(run.Error, "item 1002") {
		t.Fatalf("run = %s %q, want failed naming item 1002", run.Status, run.Error)
	}
	if run.Stats.Inserted != 2 {
		t.Errorf("stats = %+v; the items that were fetched must be stored", run.Stats)
	}
	if string(src.State) != `{}` {
		t.Errorf("state = %s; the cursor must not advance on a failed run", src.State)
	}
	if src.ConsecutiveFailures != 1 || src.LastFailureAt == nil || src.LastError == "" {
		t.Errorf("health after failure = %+v", src)
	}
}
