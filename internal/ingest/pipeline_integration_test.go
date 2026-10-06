package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/ingest"
	"synergy/internal/sources"
	"synergy/internal/sources/sourcestest"
	"synergy/internal/store"
	"synergy/internal/store/storetest"
)

// End-to-end ingestion against real PostgreSQL, driven by fake adapters
// replaying fixtures. No network access.

type fixtureEnv struct {
	st                  *store.Store
	svc                 *ingest.Service
	arxiv, github, hn   domain.Source
	arxivFA, ghFA, hnFA *sourcestest.FakeAdapter
	arxivC, ghC, hnC    []domain.Candidate
	ctx                 context.Context
}

func newFixtureEnv(t *testing.T, wrap func(*store.Store) ingest.Store) *fixtureEnv {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	e := &fixtureEnv{st: st, ctx: ctx,
		arxivC: sourcestest.LoadCandidates(t, "testdata/arxiv_batch.json"),
		ghC:    sourcestest.LoadCandidates(t, "testdata/github_batch.json"),
		hnC:    sourcestest.LoadCandidates(t, "testdata/hn_batch.json"),
	}
	mk := func(slug string, typ domain.SourceType) domain.Source {
		src, err := st.CreateSource(ctx, domain.NewSource{Slug: slug, Name: slug, Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	e.arxiv, e.github, e.hn = mk("arxiv", domain.SourceTypeArxiv), mk("github", domain.SourceTypeGitHub), mk("hn", domain.SourceTypeHackerNews)
	step := func(c []domain.Candidate) []sourcestest.Step {
		return []sourcestest.Step{{Result: sources.FetchResult{Items: c, State: json.RawMessage(`{"cursor":"v1"}`)}}}
	}
	e.arxivFA = &sourcestest.FakeAdapter{SourceType: domain.SourceTypeArxiv, Steps: step(e.arxivC)}
	e.ghFA = &sourcestest.FakeAdapter{SourceType: domain.SourceTypeGitHub, Steps: step(e.ghC)}
	e.hnFA = &sourcestest.FakeAdapter{SourceType: domain.SourceTypeHackerNews, Steps: step(e.hnC)}

	var backend ingest.Store = st
	if wrap != nil {
		backend = wrap(st)
	}
	svc, err := ingest.New(backend, []sources.Adapter{e.arxivFA, e.ghFA, e.hnFA}, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	e.svc = svc
	return e
}

func (e *fixtureEnv) run(t *testing.T, src domain.Source) domain.FetchRun {
	t.Helper()
	run, err := e.svc.Run(e.ctx, src.ID, ingest.RunOptions{Trigger: domain.TriggerCLI, Force: true})
	if err != nil {
		t.Fatalf("Run(%s): %v", src.Slug, err)
	}
	return run
}

func (e *fixtureEnv) items(t *testing.T, includeDuplicates bool) []domain.Item {
	t.Helper()
	page, err := e.st.ListItems(e.ctx, domain.ItemFilter{IncludeDuplicates: includeDuplicates, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func findItem(items []domain.Item, sourceID uuid.UUID, externalID string) (domain.Item, bool) {
	for _, it := range items {
		if it.SourceID == sourceID && it.ExternalID == externalID {
			return it, true
		}
	}
	return domain.Item{}, false
}

func TestPipelineCrossSourceDedup(t *testing.T) {
	e := newFixtureEnv(t, nil)

	if r := e.run(t, e.arxiv); r.Status != domain.RunSucceeded || r.Stats != (domain.FetchStats{Fetched: 2, Inserted: 2}) {
		t.Fatalf("arxiv run = %+v", r)
	}
	if r := e.run(t, e.github); r.Status != domain.RunSucceeded || r.Stats != (domain.FetchStats{Fetched: 1, Inserted: 1}) {
		t.Fatalf("github run = %+v", r)
	}
	// HN: 7 candidates, 1 folded, 2 rejected, 4 stored, 2 of them linking to
	// the arXiv paper and the GitHub repo through different URL forms.
	hnRun := e.run(t, e.hn)
	if want := (domain.FetchStats{Fetched: 6, Rejected: 2, Inserted: 4, Duplicate: 2}); hnRun.Status != domain.RunSucceeded || hnRun.Stats != want {
		t.Fatalf("hn run = %+v, want %+v", hnRun, want)
	}

	all := e.items(t, true)
	visible := e.items(t, false)
	if len(all) != 7 || len(visible) != 5 {
		t.Errorf("items: all=%d visible=%d, want 7 and 5", len(all), len(visible))
	}

	paper, _ := findItem(all, e.arxiv.ID, "2410.12345")
	repo, _ := findItem(all, e.github.ID, "998877")
	hnPaper, _ := findItem(all, e.hn.ID, "41000001")
	hnRepo, _ := findItem(all, e.hn.ID, "41000002")
	if hnPaper.DuplicateOf == nil || *hnPaper.DuplicateOf != paper.ID {
		t.Errorf("HN post about the paper: duplicate_of = %v, want %s", hnPaper.DuplicateOf, paper.ID)
	}
	if hnRepo.DuplicateOf == nil || *hnRepo.DuplicateOf != repo.ID {
		t.Errorf("HN post about the repo: duplicate_of = %v, want %s", hnRepo.DuplicateOf, repo.ID)
	}
	if paper.CanonicalURL != "https://arxiv.org/abs/2410.12345" || hnPaper.CanonicalURL != paper.CanonicalURL {
		t.Errorf("canonical URLs: paper=%q hn=%q", paper.CanonicalURL, hnPaper.CanonicalURL)
	}
	// Provenance is kept: the original URL and the HN signal survive.
	if hnPaper.URL != "http://www.arxiv.org/pdf/2410.12345v1.pdf?utm_source=hn" ||
		hnPaper.DiscussionURL != "https://news.ycombinator.com/item?id=41000001" ||
		!strings.Contains(string(hnPaper.Metadata), `"points": 312`) {
		t.Errorf("HN duplicate lost provenance: %+v", hnPaper)
	}
	dups, err := e.st.ListDuplicates(e.ctx, paper.ID)
	if err != nil || len(dups) != 1 || dups[0].ID != hnPaper.ID {
		t.Errorf("ListDuplicates(paper) = %v, %v", dups, err)
	}

	// Sources' health and state advanced.
	for _, s := range []domain.Source{e.arxiv, e.github, e.hn} {
		got, _ := e.st.GetSource(e.ctx, s.ID)
		if got.Health() != domain.HealthHealthy || string(got.State) != `{"cursor": "v1"}` {
			t.Errorf("%s: health=%s state=%s", s.Slug, got.Health(), got.State)
		}
	}
}

func TestPipelineRerunAndChangeDetection(t *testing.T) {
	e := newFixtureEnv(t, nil)
	e.run(t, e.hn)
	before := e.items(t, true)

	// Same data again: nothing new, nothing changed.
	r := e.run(t, e.hn)
	if r.Stats != (domain.FetchStats{Fetched: 6, Rejected: 2, Unchanged: 4}) {
		t.Errorf("identical re-run stats = %+v", r.Stats)
	}

	// Whitespace-only edits normalize to the same content: still unchanged.
	// A metadata change (more points) is an update.
	changed := append([]domain.Candidate(nil), e.hnC...)
	changed[2].Title = "An independent blog post about RLHF" // was padded with extra whitespace
	changed[0].Metadata = json.RawMessage(`{"points": 500, "num_comments": 120}`)
	e.hnFA.Steps = []sourcestest.Step{{Result: sources.FetchResult{Items: changed}}}
	r = e.run(t, e.hn)
	if r.Stats != (domain.FetchStats{Fetched: 6, Rejected: 2, Updated: 1, Unchanged: 3}) {
		t.Errorf("changed re-run stats = %+v", r.Stats)
	}

	after := e.items(t, true)
	if len(after) != len(before) {
		t.Errorf("re-runs changed the item count: %d -> %d", len(before), len(after))
	}
	hnPaper, _ := findItem(after, e.hn.ID, "41000001")
	if !strings.Contains(string(hnPaper.Metadata), `"points": 500`) {
		t.Errorf("metadata not updated: %s", hnPaper.Metadata)
	}
	prev, _ := findItem(before, e.hn.ID, "41000001")
	if hnPaper.ID != prev.ID || !hnPaper.DiscoveredAt.Equal(prev.DiscoveredAt) {
		t.Error("an update must keep the item's identity and discovery time")
	}
}

func TestPipelineConcurrentFetchRefused(t *testing.T) {
	e := newFixtureEnv(t, nil)
	release, started := make(chan struct{}), make(chan struct{})
	e.hnFA.Steps = []sourcestest.Step{{Block: release, Started: started, Result: sources.FetchResult{Items: e.hnC}}}

	run, err := e.svc.Start(e.ctx, e.hn.ID, ingest.RunOptions{Trigger: domain.TriggerAPI, Force: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	// Both entry points are refused by the database's one-running-run rule.
	if _, err := e.svc.Start(e.ctx, e.hn.ID, ingest.RunOptions{Force: true}); !errors.Is(err, domain.ErrFetchInProgress) {
		t.Errorf("concurrent Start = %v, want ErrFetchInProgress", err)
	}
	if _, err := e.svc.Run(e.ctx, e.hn.ID, ingest.RunOptions{Force: true}); !errors.Is(err, domain.ErrFetchInProgress) {
		t.Errorf("concurrent Run = %v, want ErrFetchInProgress", err)
	}
	// Other sources are unaffected.
	if r := e.run(t, e.arxiv); r.Status != domain.RunSucceeded {
		t.Errorf("other source blocked: %+v", r)
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := e.svc.GetRun(e.ctx, run.ID)
		if got.Status != domain.RunRunning {
			if got.Status != domain.RunSucceeded || got.Stats.Inserted != 4 {
				t.Errorf("background run = %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runs, _ := e.svc.ListRuns(e.ctx, e.hn.ID, 10)
	if len(runs) != 1 {
		t.Errorf("hn runs = %d, want exactly 1 (refusals must not create runs)", len(runs))
	}
}

// poisonStore smuggles an item for a non-existent source into every batch,
// after the real ones, so PostgreSQL fails the transaction midway.
type poisonStore struct{ *store.Store }

func (p poisonStore) UpsertItems(ctx context.Context, items []domain.NewItem, seenAt time.Time) ([]domain.UpsertResult, error) {
	if len(items) > 0 {
		bad := items[0]
		bad.SourceID, bad.ExternalID = uuid.New(), "poison"
		items = append(append([]domain.NewItem(nil), items...), bad)
	}
	return p.Store.UpsertItems(ctx, items, seenAt)
}

func TestPipelineStoreFailureRollsBackWholeBatch(t *testing.T) {
	e := newFixtureEnv(t, func(st *store.Store) ingest.Store { return poisonStore{st} })

	r := e.run(t, e.hn)
	if r.Status != domain.RunFailed || !strings.HasPrefix(r.Error, "store items:") {
		t.Fatalf("run = %+v, want a failed run with a store error", r)
	}
	if r.Stats.Inserted != 0 || r.Stats.Fetched != 6 || r.Stats.Rejected != 2 {
		t.Errorf("stats = %+v, want fetched/rejected counted and nothing inserted", r.Stats)
	}
	if n := len(e.items(t, true)); n != 0 {
		t.Errorf("%d items persisted; the batch transaction must roll back completely", n)
	}
	src, _ := e.st.GetSource(e.ctx, e.hn.ID)
	if src.ConsecutiveFailures != 1 || !strings.HasPrefix(src.LastError, "store items:") || string(src.State) != "{}" {
		t.Errorf("source after failure: failures=%d last_error=%q state=%s", src.ConsecutiveFailures, src.LastError, src.State)
	}
}

func TestPipelineHealthProgression(t *testing.T) {
	e := newFixtureEnv(t, nil)
	e.arxivFA.Steps = []sourcestest.Step{
		{Err: errors.New("HTTP 503")},
		{Err: errors.New("HTTP 503")},
		{Err: errors.New("HTTP 503")},
		{Result: sources.FetchResult{Items: e.arxivC, State: json.RawMessage(`{"last":"2410.22222"}`)}},
		{Result: sources.FetchResult{State: json.RawMessage(`{"last":"ignored"}`)}, Err: errors.New("HTTP 500")},
	}
	want := []struct {
		health   domain.Health
		failures int
		state    string
	}{
		{domain.HealthDegraded, 1, `{}`},
		{domain.HealthDegraded, 2, `{}`},
		{domain.HealthFailing, 3, `{}`},
		{domain.HealthHealthy, 0, `{"last": "2410.22222"}`},
		{domain.HealthDegraded, 1, `{"last": "2410.22222"}`}, // state kept on failure
	}
	for i, w := range want {
		e.run(t, e.arxiv)
		got, _ := e.st.GetSource(e.ctx, e.arxiv.ID)
		if got.Health() != w.health || got.ConsecutiveFailures != w.failures || string(got.State) != w.state {
			t.Errorf("after run %d: health=%s failures=%d state=%s; want %s %d %s",
				i+1, got.Health(), got.ConsecutiveFailures, got.State, w.health, w.failures, w.state)
		}
		if got.LastFetchAt == nil {
			t.Errorf("after run %d: last_fetch_at not set", i+1)
		}
	}
	got, _ := e.st.GetSource(e.ctx, e.arxiv.ID)
	if got.LastError != "fetch: HTTP 500" || got.LastSuccessAt == nil || got.LastFailureAt == nil {
		t.Errorf("final source: last_error=%q success=%v failure=%v", got.LastError, got.LastSuccessAt, got.LastFailureAt)
	}
}

func TestPipelineRespectsSourceStatus(t *testing.T) {
	e := newFixtureEnv(t, nil)
	paused := domain.SourceStatusPaused
	if _, err := e.st.UpdateSource(e.ctx, e.hn.ID, domain.SourcePatch{Status: &paused}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Run(e.ctx, e.hn.ID, ingest.RunOptions{Force: true})
	if !errors.Is(err, domain.ErrConflict) || e.hnFA.Calls() != 0 {
		t.Errorf("paused source: err=%v adapter calls=%d", err, e.hnFA.Calls())
	}
}

func TestPipelineRecoverAbandonedRuns(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	src, _ := st.CreateSource(ctx, domain.NewSource{Slug: "hn", Name: "hn", Type: domain.SourceTypeHackerNews})
	stale, err := st.StartFetchRun(ctx, src.ID, domain.TriggerAPI) // as if the process died mid-fetch
	if err != nil {
		t.Fatal(err)
	}

	// A fresh run is never touched: it may belong to a live process.
	live, _ := ingest.New(st, nil, ingest.Options{})
	if n, err := live.RecoverAbandoned(ctx); err != nil || n != 0 {
		t.Fatalf("recovered %d (%v), want 0 for a fresh run", n, err)
	}
	// An hour later it is clearly abandoned.
	later, _ := ingest.New(st, nil, ingest.Options{Now: func() time.Time { return time.Now().Add(time.Hour) }})
	if n, err := later.RecoverAbandoned(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d (%v), want 1", n, err)
	}
	got, _ := st.GetFetchRun(ctx, stale.ID)
	if got.Status != domain.RunFailed || !strings.HasPrefix(got.Error, "abandoned") {
		t.Errorf("stale run = %+v", got)
	}
	if _, err := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI); err != nil {
		t.Errorf("source still blocked: %v", err)
	}
}
