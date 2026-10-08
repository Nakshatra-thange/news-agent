package scheduler_test

import (
	"context"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/ingest"
	"synergy/internal/scheduler"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/sources/sourcestest"
	"synergy/internal/store"
	"synergy/internal/store/storetest"
)

// TestSchedulerWithRealIngestion runs the scheduler against PostgreSQL and
// the real ingestion service, with scripted adapters: Hacker News blocks
// until released, arXiv fails, GitHub succeeds.
func TestSchedulerWithRealIngestion(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	reg, err := sources.NewRegistry(st, nil, hackernews.Spec{}, arxiv.Spec{}, github.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	hnStarted, hnRelease := make(chan struct{}), make(chan struct{})
	hn := &sourcestest.FakeAdapter{SourceType: domain.SourceTypeHackerNews, Steps: []sourcestest.Step{
		{Started: hnStarted, Block: hnRelease}, {},
	}}
	ax := &sourcestest.FakeAdapter{SourceType: domain.SourceTypeArxiv, Steps: []sourcestest.Step{
		{Err: errFake("arXiv is down")},
	}}
	gh := &sourcestest.FakeAdapter{SourceType: domain.SourceTypeGitHub, Steps: []sourcestest.Step{
		{Result: sources.FetchResult{Items: sourcestest.LoadCandidates(t, "../ingest/testdata/github_batch.json")}},
	}}
	svc, err := ingest.New(st, []sources.Adapter{hn, ax, gh}, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(reg, svc, time.Hour, nil)

	// First tick: every seeded source is due (never fetched).
	if n := sched.Tick(ctx); n != 3 {
		t.Fatalf("first tick started %d fetches, want 3", n)
	}
	<-hnStarted
	arxivRun := waitFinished(t, st, reg, "arxiv-ai")
	githubRun := waitFinished(t, st, reg, "github-ai-repos")

	// One source failing does not affect the others.
	if arxivRun.Status != domain.RunFailed || arxivRun.Trigger != domain.TriggerScheduler {
		t.Errorf("arxiv run = %s/%s, want failed/scheduler", arxivRun.Status, arxivRun.Trigger)
	}
	if githubRun.Status != domain.RunSucceeded || githubRun.Trigger != domain.TriggerScheduler || githubRun.Stats.Inserted == 0 {
		t.Errorf("github run = %s/%s with %+v, want succeeded/scheduler with items", githubRun.Status, githubRun.Trigger, githubRun.Stats)
	}

	// Second tick: Hacker News is still running, so it is not started again
	// (the database allows one running run per source); arXiv and GitHub
	// just completed and are within their min_fetch_interval.
	if n := sched.Tick(ctx); n != 0 {
		t.Errorf("second tick started %d fetches, want 0", n)
	}
	if hn.Calls() != 1 {
		t.Errorf("hacker news adapter called %d times, want 1 (no overlapping fetch)", hn.Calls())
	}
	if runs := runsOf(t, st, reg, "hn-ai"); len(runs) != 1 || runs[0].Status != domain.RunRunning {
		t.Errorf("hn-ai runs = %+v, want exactly one running", runs)
	}

	// Graceful shutdown with a scheduled fetch in flight: the scheduler
	// stops, the ingestion service cancels the fetch and records it.
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		sched.Run(runCtx)
		close(done)
	}()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop")
	}
	sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_ = svc.Shutdown(sctx) // deadline passes: the blocked fetch is cancelled
	close(hnRelease)

	runs := runsOf(t, st, reg, "hn-ai")
	if len(runs) != 1 || runs[0].Status != domain.RunFailed || runs[0].Error != "fetch aborted: service shutting down" {
		t.Errorf("hn-ai runs after shutdown = %+v, want one failed with the shutdown reason", runs)
	}
	if n := sched.Tick(ctx); n != 0 {
		t.Errorf("tick after ingestion shutdown started %d fetches, want 0", n)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

func runsOf(t *testing.T, st *store.Store, reg *sources.Registry, slug string) []domain.FetchRun {
	t.Helper()
	src, err := reg.Get(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListFetchRuns(context.Background(), src.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// waitFinished waits for the source's only run to leave "running".
func waitFinished(t *testing.T, st *store.Store, reg *sources.Registry, slug string) domain.FetchRun {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if runs := runsOf(t, st, reg, slug); len(runs) == 1 && runs[0].Status != domain.RunRunning {
			return runs[0]
		}
	}
	t.Fatalf("%s: fetch did not finish", slug)
	return domain.FetchRun{}
}
