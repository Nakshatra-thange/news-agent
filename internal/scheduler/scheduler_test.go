package scheduler

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/ingest"
	"synergy/internal/sources"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time {
	t := t0.Add(d)
	return &t
}

func source(slug string, interval time.Duration) domain.Source {
	return domain.Source{ID: uuid.New(), Slug: slug, Status: domain.SourceStatusActive, MinFetchInterval: interval}
}

// fakeSources returns a fixed list of active sources.
type fakeSources struct {
	mu   sync.Mutex
	list []domain.Source
	err  error
	seen []sources.ListFilter
}

func (f *fakeSources) List(_ context.Context, filter sources.ListFilter) ([]domain.Source, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, filter)
	return slices.Clone(f.list), f.err
}

// complete marks a source's fetch as finished at t, as the ingestion
// service would.
func (f *fakeSources) complete(id uuid.UUID, t time.Time, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.list {
		if f.list[i].ID == id {
			if ok {
				f.list[i].LastSuccessAt = &t
			} else {
				f.list[i].LastFailureAt = &t
			}
		}
	}
}

// fakeFetcher records Start calls and returns scripted errors.
type fakeFetcher struct {
	mu       sync.Mutex
	started  []uuid.UUID
	triggers []domain.RunTrigger
	errs     map[uuid.UUID]error
	recovers int
	onStart  func(id uuid.UUID)
}

func (f *fakeFetcher) Start(_ context.Context, id uuid.UUID, o ingest.RunOptions) (domain.FetchRun, error) {
	f.mu.Lock()
	err := f.errs[id]
	if err == nil {
		f.started = append(f.started, id)
		f.triggers = append(f.triggers, o.Trigger)
	}
	onStart := f.onStart
	f.mu.Unlock()
	if err != nil {
		return domain.FetchRun{}, err
	}
	if onStart != nil {
		onStart(id)
	}
	return domain.FetchRun{ID: uuid.New(), SourceID: id, Trigger: o.Trigger, Status: domain.RunRunning}, nil
}

func (f *fakeFetcher) RecoverAbandoned(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovers++
	return 0, nil
}

func (f *fakeFetcher) startedIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

func newTestScheduler(src *fakeSources, f *fakeFetcher, now *time.Time) *Scheduler {
	s := New(src, f, time.Minute, nil)
	s.now = func() time.Time { return *now }
	return s
}

func TestDue(t *testing.T) {
	const interval = 30 * time.Minute
	tests := []struct {
		name    string
		success *time.Time
		failure *time.Time
		want    bool
	}{
		{"never fetched", nil, nil, true},
		{"succeeded recently", at(-10 * time.Minute), nil, false},
		{"succeeded exactly one interval ago", at(-interval), nil, true},
		{"succeeded long ago", at(-40 * time.Minute), nil, true},
		{"failed recently", nil, at(-time.Minute), false},
		{"failed long ago", nil, at(-31 * time.Minute), true},
		{"old success, recent failure", at(-2 * time.Hour), at(-5 * time.Minute), false},
		{"old failure, recent success", at(-5 * time.Minute), at(-2 * time.Hour), false},
		{"both long ago", at(-3 * time.Hour), at(-2 * time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := source("s", interval)
			src.LastSuccessAt, src.LastFailureAt = tt.success, tt.failure
			if got := Due(src, t0); got != tt.want {
				t.Errorf("Due = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTickStartsOnlyDueSources(t *testing.T) {
	never := source("never", time.Hour)
	recent := source("recent", time.Hour)
	recent.LastSuccessAt = at(-10 * time.Minute)
	stale := source("stale", time.Hour)
	stale.LastSuccessAt = at(-2 * time.Hour)
	failedRecently := source("failed-recently", time.Hour)
	failedRecently.LastFailureAt = at(-time.Minute)

	src := &fakeSources{list: []domain.Source{never, recent, stale, failedRecently}}
	f := &fakeFetcher{}
	now := t0
	if n := newTestScheduler(src, f, &now).Tick(context.Background()); n != 2 {
		t.Errorf("Tick started %d fetches, want 2", n)
	}
	if got, want := f.startedIDs(), []uuid.UUID{never.ID, stale.ID}; !slices.Equal(got, want) {
		t.Errorf("started %v, want never and stale (%v)", got, want)
	}
	for _, tr := range f.triggers {
		if tr != domain.TriggerScheduler {
			t.Errorf("trigger = %q, want scheduler", tr)
		}
	}
	if len(src.seen) != 1 || !slices.Equal(src.seen[0].Statuses, []domain.SourceStatus{domain.SourceStatusActive}) {
		t.Errorf("listed sources with %+v, want active only", src.seen)
	}
	if f.recovers != 1 {
		t.Errorf("RecoverAbandoned called %d times per tick, want 1", f.recovers)
	}
}

func TestTickEnforcesMinInterval(t *testing.T) {
	const interval = 30 * time.Minute
	s0 := source("hn", interval)
	src := &fakeSources{list: []domain.Source{s0}}
	now := t0
	// The fetch completes as soon as it starts, at the current clock.
	f := &fakeFetcher{}
	f.onStart = func(id uuid.UUID) { src.complete(id, now, true) }
	sched := newTestScheduler(src, f, &now)
	ctx := context.Background()

	steps := []struct {
		at        time.Duration
		wantStart bool
	}{
		{0, true},                       // never fetched
		{time.Minute, false},            // just fetched
		{interval - time.Second, false}, // one second early
		{interval, true},                // exactly due
		{interval + time.Minute, false}, // fetched again at `interval`
		{2*interval - time.Second, false},
		{2 * interval, true},
	}
	for _, st := range steps {
		now = t0.Add(st.at)
		if got := sched.Tick(ctx) == 1; got != st.wantStart {
			t.Errorf("at +%v: started = %v, want %v", st.at, got, st.wantStart)
		}
	}
	if n := len(f.startedIDs()); n != 3 {
		t.Errorf("started %d fetches in total, want 3", n)
	}
}

func TestTickFailureDoesNotStopOthers(t *testing.T) {
	broken := source("broken", time.Hour)
	running := source("running", time.Hour)
	cooling := source("cooling", time.Hour)
	healthy := source("healthy", time.Hour)
	src := &fakeSources{list: []domain.Source{broken, running, cooling, healthy}}
	f := &fakeFetcher{errs: map[uuid.UUID]error{
		broken.ID:  errors.New("database exploded"),
		running.ID: domain.ErrFetchInProgress,
		cooling.ID: &domain.CooldownError{Slug: "cooling", Remaining: time.Minute},
	}}
	now := t0
	sched := newTestScheduler(src, f, &now)

	if n := sched.Tick(context.Background()); n != 1 {
		t.Errorf("Tick started %d, want 1", n)
	}
	if got := f.startedIDs(); !slices.Equal(got, []uuid.UUID{healthy.ID}) {
		t.Errorf("started %v, want only the healthy source", got)
	}

	// A failed listing skips the tick; the next tick works normally.
	src.err = errors.New("database down")
	if n := sched.Tick(context.Background()); n != 0 {
		t.Errorf("Tick with failing listing started %d, want 0", n)
	}
	src.err = nil
	delete(f.errs, broken.ID)
	if n := sched.Tick(context.Background()); n != 2 {
		t.Errorf("Tick after recovery started %d, want 2 (broken and healthy)", n)
	}
}

func TestTickStopsWhenIngestionIsClosed(t *testing.T) {
	a, b := source("a", time.Hour), source("b", time.Hour)
	src := &fakeSources{list: []domain.Source{a, b}}
	f := &fakeFetcher{errs: map[uuid.UUID]error{a.ID: ingest.ErrClosed}}
	now := t0
	if n := newTestScheduler(src, f, &now).Tick(context.Background()); n != 0 || len(f.startedIDs()) != 0 {
		t.Errorf("after ErrClosed: started %d (%v), want none", n, f.startedIDs())
	}
}

func TestRunTicksUntilCancelled(t *testing.T) {
	src := &fakeSources{}
	f := &fakeFetcher{}
	sched := New(src, f, 10*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		src.mu.Lock()
		n := len(src.seen)
		src.mu.Unlock()
		if n >= 3 {
			break // ticked immediately, then on the interval
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d ticks before the deadline", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
