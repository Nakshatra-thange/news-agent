package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/sources"
	"synergy/internal/sources/sourcestest"
)

// memStore is an in-memory Store for orchestration tests. PostgreSQL
// behavior (dedup, transactions) is covered by the integration tests.
type memStore struct {
	mu        sync.Mutex
	sources   map[uuid.UUID]domain.Source
	runs      map[uuid.UUID]domain.FetchRun
	items     map[string]domain.NewItem // key: source/external id
	upsertErr error
	cutoff    time.Time
}

func newMemStore(srcs ...domain.Source) *memStore {
	m := &memStore{sources: map[uuid.UUID]domain.Source{}, runs: map[uuid.UUID]domain.FetchRun{}, items: map[string]domain.NewItem{}}
	for _, s := range srcs {
		m.sources[s.ID] = s
	}
	return m
}

func (m *memStore) GetSource(_ context.Context, id uuid.UUID) (domain.Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sources[id]
	if !ok {
		return s, domain.ErrNotFound
	}
	return s, nil
}

func (m *memStore) StartFetchRun(_ context.Context, sourceID uuid.UUID, trigger domain.RunTrigger) (domain.FetchRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.SourceID == sourceID && r.Status == domain.RunRunning {
			return r, domain.ErrFetchInProgress
		}
	}
	now := time.Now().UTC()
	r := domain.FetchRun{ID: uuid.New(), SourceID: sourceID, Trigger: trigger, Status: domain.RunRunning, StartedAt: now}
	m.runs[r.ID] = r
	s := m.sources[sourceID]
	s.LastFetchAt = &now
	m.sources[sourceID] = s
	return r, nil
}

func (m *memStore) FinishFetchRun(_ context.Context, id uuid.UUID, c domain.RunCompletion) (domain.FetchRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return r, domain.ErrNotFound
	}
	if r.Status != domain.RunRunning {
		return r, domain.ErrConflict
	}
	now := time.Now().UTC()
	r.FinishedAt, r.Stats, r.Error = &now, c.Stats, c.Err
	s := m.sources[r.SourceID]
	if c.Succeeded() {
		r.Status = domain.RunSucceeded
		s.LastSuccessAt, s.ConsecutiveFailures, s.LastError = &now, 0, ""
		if c.State != nil {
			s.State = c.State
		}
	} else {
		r.Status = domain.RunFailed
		s.LastFailureAt, s.LastError = &now, c.Err
		s.ConsecutiveFailures++
	}
	m.runs[id], m.sources[r.SourceID] = r, s
	return r, nil
}

func (m *memStore) UpsertItems(_ context.Context, items []domain.NewItem, _ time.Time) ([]domain.UpsertResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	out := make([]domain.UpsertResult, len(items))
	for i, it := range items {
		key := it.SourceID.String() + "/" + it.ExternalID
		outcome := domain.UpsertInserted
		if _, ok := m.items[key]; ok {
			outcome = domain.UpsertUnchanged
		}
		m.items[key] = it
		out[i] = domain.UpsertResult{ID: uuid.New(), Outcome: outcome}
	}
	return out, nil
}

func (m *memStore) FailAbandonedRuns(_ context.Context, cutoff time.Time, _ string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cutoff = cutoff
	return 0, nil
}

func (m *memStore) GetFetchRun(_ context.Context, id uuid.UUID) (domain.FetchRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return r, domain.ErrNotFound
	}
	return r, nil
}

func (m *memStore) ListFetchRuns(context.Context, uuid.UUID, int) ([]domain.FetchRun, error) {
	return nil, nil
}

func (m *memStore) source(id uuid.UUID) domain.Source {
	s, _ := m.GetSource(context.Background(), id)
	return s
}

func activeSource(t domain.SourceType) domain.Source {
	return domain.Source{
		ID: uuid.New(), Slug: string(t) + "-src", Type: t, Status: domain.SourceStatusActive,
		MinFetchInterval: 10 * time.Minute, State: json.RawMessage(`{}`),
	}
}

func candidates(n int) []domain.Candidate {
	out := make([]domain.Candidate, n)
	for i := range out {
		out[i] = domain.Candidate{
			ExternalID: uuid.NewString(), Kind: domain.ItemKindDiscussion,
			Title: "Story", URL: "https://example.com/" + uuid.NewString(),
		}
	}
	return out
}

func newService(t *testing.T, st Store, o Options, adapters ...sources.Adapter) *Service {
	t.Helper()
	svc, err := New(st, adapters, o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return svc
}

func TestNewRejectsDuplicateAdapters(t *testing.T) {
	a := &sourcestest.FakeAdapter{SourceType: domain.SourceTypeArxiv}
	if _, err := New(newMemStore(), []sources.Adapter{a, a}, Options{}); err == nil {
		t.Error("expected an error for two adapters of one type")
	}
}

func TestRunSuccess(t *testing.T) {
	src := activeSource(domain.SourceTypeHackerNews)
	st := newMemStore(src)
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{
		Result: sources.FetchResult{Items: candidates(3), State: json.RawMessage(`{"cursor":7}`), Requests: 2},
	}}}
	svc := newService(t, st, Options{}, fa)

	run, err := svc.Run(context.Background(), src.ID, RunOptions{Trigger: domain.TriggerAPI})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != domain.RunSucceeded || run.Trigger != domain.TriggerAPI || run.Stats != (domain.FetchStats{Fetched: 3, Inserted: 3}) {
		t.Errorf("run = %+v", run)
	}
	if got := st.source(src.ID); string(got.State) != `{"cursor":7}` || got.Health() != domain.HealthHealthy {
		t.Errorf("source state=%s health=%s", got.State, got.Health())
	}
	if seen := fa.Seen(); len(seen) != 1 || seen[0].ID != src.ID {
		t.Errorf("adapter saw %v", seen)
	}
}

func TestPreflightRefusals(t *testing.T) {
	ctx := context.Background()
	paused := activeSource(domain.SourceTypeArxiv)
	paused.Status = domain.SourceStatusPaused
	retired := activeSource(domain.SourceTypeArxiv)
	retired.Status = domain.SourceStatusRetired
	noAdapter := activeSource(domain.SourceTypeGitHub)
	st := newMemStore(paused, retired, noAdapter)
	fa := &sourcestest.FakeAdapter{SourceType: domain.SourceTypeArxiv}
	svc := newService(t, st, Options{}, fa)

	tests := []struct {
		name string
		id   uuid.UUID
		want error
	}{
		{"paused", paused.ID, domain.ErrConflict},
		{"retired", retired.ID, domain.ErrConflict},
		{"no adapter", noAdapter.ID, domain.ErrUnsupported},
		{"unknown source", uuid.New(), domain.ErrNotFound},
	}
	for _, tt := range tests {
		for _, start := range []bool{false, true} {
			var err error
			if start {
				_, err = svc.Start(ctx, tt.id, RunOptions{Force: true})
			} else {
				_, err = svc.Run(ctx, tt.id, RunOptions{Force: true})
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("%s (start=%v): error = %v, want %v", tt.name, start, err, tt.want)
			}
		}
	}
	if fa.Calls() != 0 || len(st.runs) != 0 {
		t.Errorf("refused fetches still ran: calls=%d runs=%d", fa.Calls(), len(st.runs))
	}
}

func TestCooldown(t *testing.T) {
	ctx := context.Background()
	src := activeSource(domain.SourceTypeHackerNews)
	recent := time.Now().Add(-time.Minute)
	src.LastFetchAt = &recent
	st := newMemStore(src)
	fa := &sourcestest.FakeAdapter{SourceType: src.Type}
	svc := newService(t, st, Options{}, fa)

	_, err := svc.Run(ctx, src.ID, RunOptions{})
	var ce *domain.CooldownError
	if !errors.As(err, &ce) || !errors.Is(err, domain.ErrTooSoon) {
		t.Fatalf("error = %v, want CooldownError", err)
	}
	if ce.Remaining < 8*time.Minute || ce.Remaining > 9*time.Minute {
		t.Errorf("remaining = %v, want ~9m", ce.Remaining)
	}

	// Force bypasses the cooldown...
	if run, err := svc.Run(ctx, src.ID, RunOptions{Force: true}); err != nil || run.Status != domain.RunSucceeded {
		t.Fatalf("forced run = %+v, %v", run, err)
	}
	// ...and that fetch restarts the cooldown.
	if _, err := svc.Run(ctx, src.ID, RunOptions{}); !errors.Is(err, domain.ErrTooSoon) {
		t.Errorf("error = %v, want cooldown after the forced fetch", err)
	}
}

func TestCooldownExpires(t *testing.T) {
	src := activeSource(domain.SourceTypeHackerNews)
	old := time.Now().Add(-11 * time.Minute)
	src.LastFetchAt = &old
	svc := newService(t, newMemStore(src), Options{}, &sourcestest.FakeAdapter{SourceType: src.Type})
	if _, err := svc.Run(context.Background(), src.ID, RunOptions{}); err != nil {
		t.Errorf("Run after the interval: %v", err)
	}
}

func TestAdapterPanicBecomesFailedRun(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Panic: "boom"}, {}}}
	svc := newService(t, st, Options{}, fa)

	run, err := svc.Run(context.Background(), src.ID, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != domain.RunFailed || !strings.Contains(run.Error, "adapter panicked: boom") {
		t.Errorf("run = %+v", run)
	}
	// The service keeps working.
	if run, err := svc.Run(context.Background(), src.ID, RunOptions{Force: true}); err != nil || run.Status != domain.RunSucceeded {
		t.Errorf("run after panic = %+v, %v", run, err)
	}
}

func TestFetchTimeout(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Delay: 10 * time.Second}}}
	svc := newService(t, st, Options{FetchTimeout: 50 * time.Millisecond}, fa)

	start := time.Now()
	run, err := svc.Run(context.Background(), src.ID, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout not enforced: took %v", time.Since(start))
	}
	if run.Status != domain.RunFailed || run.Error != "fetch aborted: timed out after 50ms" {
		t.Errorf("run = %+v", run)
	}
	if st.source(src.ID).ConsecutiveFailures != 1 {
		t.Error("timeout not recorded as a failure")
	}
}

func TestCallerCancellation(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	started := make(chan struct{})
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Block: make(chan struct{}), Started: started}}}
	svc := newService(t, st, Options{}, fa)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	run, err := svc.Run(ctx, src.ID, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v (the outcome must be recorded even after cancellation)", err)
	}
	if run.Status != domain.RunFailed || run.Error != "fetch cancelled" {
		t.Errorf("run = %+v", run)
	}
}

func TestPartialFailureKeepsItemsButFailsRun(t *testing.T) {
	src := activeSource(domain.SourceTypeHackerNews)
	src.State = json.RawMessage(`{"cursor":"old"}`)
	st := newMemStore(src)
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{
		Result: sources.FetchResult{Items: candidates(2), State: json.RawMessage(`{"cursor":"new"}`)},
		Err:    errors.New("query 3 of 4: HTTP 503"),
	}}}
	svc := newService(t, st, Options{}, fa)

	run, _ := svc.Run(context.Background(), src.ID, RunOptions{})
	if run.Status != domain.RunFailed || run.Error != "fetch: query 3 of 4: HTTP 503" {
		t.Errorf("run = %+v", run)
	}
	if run.Stats.Inserted != 2 || len(st.items) != 2 {
		t.Errorf("stats=%+v stored=%d, want the 2 fetched items kept", run.Stats, len(st.items))
	}
	if got := st.source(src.ID); string(got.State) != `{"cursor":"old"}` {
		t.Errorf("state advanced on a failed run: %s", got.State)
	}
}

func TestStoreFailureFailsRun(t *testing.T) {
	src := activeSource(domain.SourceTypeHackerNews)
	st := newMemStore(src)
	st.upsertErr = errors.New("connection reset")
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{
		Result: sources.FetchResult{Items: candidates(4), State: json.RawMessage(`{"cursor":1}`)},
	}}}
	svc := newService(t, st, Options{}, fa)

	run, _ := svc.Run(context.Background(), src.ID, RunOptions{})
	if run.Status != domain.RunFailed || run.Error != "store items: connection reset" {
		t.Errorf("run = %+v", run)
	}
	if run.Stats != (domain.FetchStats{Fetched: 4}) {
		t.Errorf("stats = %+v, want only Fetched (nothing was stored)", run.Stats)
	}
	if got := st.source(src.ID); string(got.State) == `{"cursor":1}` {
		t.Error("state advanced although items were not stored")
	}
}

func TestRejectedCandidatesAreCounted(t *testing.T) {
	src := activeSource(domain.SourceTypeHackerNews)
	st := newMemStore(src)
	cands := sourcestest.LoadCandidates(t, "testdata/hn_batch.json")
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Result: sources.FetchResult{Items: cands}}}}
	svc := newService(t, st, Options{}, fa)

	run, _ := svc.Run(context.Background(), src.ID, RunOptions{})
	// 7 candidates, 1 folded repeat -> 6 fetched: 2 rejected, 4 stored.
	want := domain.FetchStats{Fetched: 6, Rejected: 2, Inserted: 4}
	if run.Status != domain.RunSucceeded || run.Stats != want {
		t.Errorf("run = %+v, want succeeded with %+v", run, want)
	}
}

func TestStartRunsInBackground(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	release := make(chan struct{})
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{
		Block: release, Result: sources.FetchResult{Items: candidates(1)},
	}}}
	svc := newService(t, st, Options{}, fa)

	run, err := svc.Start(context.Background(), src.ID, RunOptions{Trigger: domain.TriggerAPI})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("Start returned status %s, want running", run.Status)
	}
	// While it runs, a second fetch of the same source is refused.
	if _, err := svc.Start(context.Background(), src.ID, RunOptions{Force: true}); !errors.Is(err, domain.ErrFetchInProgress) {
		t.Errorf("concurrent Start error = %v, want ErrFetchInProgress", err)
	}
	close(release)
	got := waitForRun(t, svc, run.ID)
	if got.Status != domain.RunSucceeded || got.Stats.Inserted != 1 {
		t.Errorf("finished run = %+v", got)
	}
}

func TestShutdownWaitsForRunningFetches(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Delay: 100 * time.Millisecond}}}
	svc, _ := New(st, []sources.Adapter{fa}, Options{})

	run, err := svc.Start(context.Background(), src.ID, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got, _ := st.GetFetchRun(context.Background(), run.ID); got.Status != domain.RunSucceeded {
		t.Errorf("run = %+v, want it to finish before Shutdown returned", got)
	}
	for _, f := range []func() error{
		func() error { _, err := svc.Start(context.Background(), src.ID, RunOptions{Force: true}); return err },
		func() error { _, err := svc.Run(context.Background(), src.ID, RunOptions{Force: true}); return err },
	} {
		if err := f(); !errors.Is(err, ErrClosed) {
			t.Errorf("after Shutdown: %v, want ErrClosed", err)
		}
	}
}

func TestShutdownDeadlineCancelsFetches(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	started := make(chan struct{})
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Block: make(chan struct{}), Started: started}}}
	svc, _ := New(st, []sources.Adapter{fa}, Options{})

	run, _ := svc.Start(context.Background(), src.ID, RunOptions{})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := svc.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want DeadlineExceeded", err)
	}
	got, _ := st.GetFetchRun(context.Background(), run.ID)
	if got.Status != domain.RunFailed || got.Error != "fetch aborted: service shutting down" {
		t.Errorf("run = %+v, want it recorded as failed by shutdown", got)
	}
}

func TestShutdownInterruptsSyncRun(t *testing.T) {
	src := activeSource(domain.SourceTypeArxiv)
	st := newMemStore(src)
	started := make(chan struct{})
	fa := &sourcestest.FakeAdapter{SourceType: src.Type, Steps: []sourcestest.Step{{Block: make(chan struct{}), Started: started}}}
	svc, _ := New(st, []sources.Adapter{fa}, Options{})

	done := make(chan domain.FetchRun, 1)
	go func() {
		run, _ := svc.Run(context.Background(), src.ID, RunOptions{})
		done <- run
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = svc.Shutdown(ctx)
	select {
	case run := <-done:
		if run.Status != domain.RunFailed || !strings.Contains(run.Error, "shutting down") {
			t.Errorf("run = %+v", run)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous run was not interrupted by Shutdown")
	}
}

func TestRecoverAbandonedCutoffSparesLiveRuns(t *testing.T) {
	st := newMemStore()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	svc := newService(t, st, Options{FetchTimeout: 2 * time.Minute, Now: func() time.Time { return now }})
	if _, err := svc.RecoverAbandoned(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Anything younger than fetch timeout + store + finish bounds could
	// still be alive in another process.
	minAge := 2*time.Minute + storeTimeout + finishTimeout
	if age := now.Sub(st.cutoff); age <= minAge {
		t.Errorf("cutoff age = %v, must exceed %v", age, minAge)
	}
}

func TestSupportsType(t *testing.T) {
	svc := newService(t, newMemStore(), Options{}, &sourcestest.FakeAdapter{SourceType: domain.SourceTypeArxiv})
	if !svc.SupportsType(domain.SourceTypeArxiv) || svc.SupportsType(domain.SourceTypeGitHub) {
		t.Error("SupportsType wrong")
	}
}

func waitForRun(t *testing.T, svc *Service, id uuid.UUID) domain.FetchRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := svc.GetRun(context.Background(), id)
		if err == nil && r.Status != domain.RunRunning {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", id)
	return domain.FetchRun{}
}
