// Package ingest is Synergy's source-agnostic ingestion pipeline:
//
//	adapter.Fetch -> normalize/validate -> canonicalize URL + hash
//	  -> dedup + store (one transaction) -> finish fetch run -> source health
//
// It knows nothing about any particular upstream. A source type takes part
// by implementing sources.Adapter and being passed to New.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// Store is the persistence the pipeline needs.
type Store interface {
	GetSource(ctx context.Context, id uuid.UUID) (domain.Source, error)
	StartFetchRun(ctx context.Context, sourceID uuid.UUID, trigger domain.RunTrigger) (domain.FetchRun, error)
	FinishFetchRun(ctx context.Context, runID uuid.UUID, c domain.RunCompletion) (domain.FetchRun, error)
	UpsertItems(ctx context.Context, items []domain.NewItem, seenAt time.Time) ([]domain.UpsertResult, error)
	FailAbandonedRuns(ctx context.Context, cutoff time.Time, reason string) (int64, error)
	GetFetchRun(ctx context.Context, id uuid.UUID) (domain.FetchRun, error)
	ListFetchRuns(ctx context.Context, sourceID uuid.UUID, limit int) ([]domain.FetchRun, error)
}

// Defaults and fixed bounds.
const (
	DefaultFetchTimeout = 2 * time.Minute
	// storeTimeout bounds persisting a batch. It runs on a context detached
	// from fetch cancellation so items already fetched are not thrown away.
	storeTimeout = 30 * time.Second
	// finishTimeout bounds recording the run's outcome, which must happen
	// even when the fetch was cancelled.
	finishTimeout = 10 * time.Second
)

// ErrClosed is returned by Start and Run after Shutdown.
var ErrClosed = errors.New("ingestion service is shut down")

var errShutdown = errors.New("service shutting down")

// Options configures a Service.
type Options struct {
	// FetchTimeout bounds one adapter Fetch call (all its requests).
	FetchTimeout time.Duration
	Logger       *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

// RunOptions controls one fetch.
type RunOptions struct {
	Trigger domain.RunTrigger
	// Force bypasses the source's min_fetch_interval cooldown. It never
	// bypasses the source status or the one-run-per-source rule.
	Force bool
}

// Service runs fetches. It is safe for concurrent use.
type Service struct {
	store        Store
	adapters     map[domain.SourceType]sources.Adapter
	fetchTimeout time.Duration
	logger       *slog.Logger
	now          func() time.Time

	// Background runs (Start) derive from base; Shutdown cancels it.
	base       context.Context
	cancelBase context.CancelCauseFunc
	mu         sync.Mutex
	closed     bool
	wg         sync.WaitGroup
}

// New builds a Service with one adapter per source type.
func New(store Store, adapters []sources.Adapter, o Options) (*Service, error) {
	if o.FetchTimeout <= 0 {
		o.FetchTimeout = DefaultFetchTimeout
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := make(map[domain.SourceType]sources.Adapter, len(adapters))
	for _, a := range adapters {
		if _, dup := m[a.Type()]; dup {
			return nil, fmt.Errorf("two adapters registered for source type %q", a.Type())
		}
		m[a.Type()] = a
	}
	base, cancel := context.WithCancelCause(context.Background())
	return &Service{
		store: store, adapters: m, fetchTimeout: o.FetchTimeout, logger: o.Logger, now: o.Now,
		base: base, cancelBase: cancel,
	}, nil
}

// SupportsType reports whether an adapter is registered for t.
func (s *Service) SupportsType(t domain.SourceType) bool {
	_, ok := s.adapters[t]
	return ok
}

// Run fetches a source synchronously and returns the finished run. A fetch
// that fails still returns its (failed) run with a nil error; errors are
// reserved for fetches that could not start (unknown source, inactive,
// cooldown, already running, no adapter) or whose outcome could not be
// recorded.
func (s *Service) Run(ctx context.Context, sourceID uuid.UUID, o RunOptions) (domain.FetchRun, error) {
	if err := s.acquire(); err != nil {
		return domain.FetchRun{}, err
	}
	defer s.wg.Done()

	// Cancel on the caller's context or on Shutdown, whichever comes first.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(s.base, func() { cancel(context.Cause(s.base)) })
	defer stop()

	src, adapter, run, err := s.begin(ctx, sourceID, o)
	if err != nil {
		return domain.FetchRun{}, err
	}
	return s.execute(ctx, src, adapter, run)
}

// Start begins a fetch in the background and returns the run as soon as it
// is recorded (status "running"). Poll it with GetRun. Pre-flight failures
// are returned synchronously, exactly as from Run.
func (s *Service) Start(ctx context.Context, sourceID uuid.UUID, o RunOptions) (domain.FetchRun, error) {
	if err := s.acquire(); err != nil {
		return domain.FetchRun{}, err
	}
	src, adapter, run, err := s.begin(ctx, sourceID, o)
	if err != nil {
		s.wg.Done()
		return domain.FetchRun{}, err
	}
	go func() {
		defer s.wg.Done()
		// Detached from the request: the fetch outlives the HTTP call.
		_, _ = s.execute(s.base, src, adapter, run)
	}()
	return run, nil
}

// Shutdown stops accepting fetches and waits for running ones. If ctx ends
// first, running fetches are cancelled; they are still recorded as failed
// before Shutdown returns.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.cancelBase(errShutdown)
		return nil
	case <-ctx.Done():
	}
	s.logger.Warn("cancelling in-flight fetches for shutdown")
	s.cancelBase(errShutdown)
	<-done // bounded by storeTimeout + finishTimeout
	return ctx.Err()
}

func (s *Service) acquire() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.wg.Add(1)
	return nil
}

// RecoverAbandoned marks runs left "running" by a crashed process as failed
// so their sources are not blocked forever. Only runs older than any live run
// could be (fetch timeout plus persistence bounds) are touched, so a fetch
// running in another process (e.g. the CLI) is never disturbed.
func (s *Service) RecoverAbandoned(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-(s.fetchTimeout + storeTimeout + finishTimeout + time.Minute))
	n, err := s.store.FailAbandonedRuns(ctx, cutoff, "abandoned: the process running this fetch stopped before it finished")
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.logger.Warn("marked abandoned fetch runs as failed", "count", n)
	}
	return n, nil
}

// GetRun returns a fetch run.
func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (domain.FetchRun, error) {
	return s.store.GetFetchRun(ctx, id)
}

// ListRuns returns a source's most recent runs, newest first.
func (s *Service) ListRuns(ctx context.Context, sourceID uuid.UUID, limit int) ([]domain.FetchRun, error) {
	return s.store.ListFetchRuns(ctx, sourceID, limit)
}

// begin performs the pre-flight checks and records the run.
func (s *Service) begin(ctx context.Context, sourceID uuid.UUID, o RunOptions) (domain.Source, sources.Adapter, domain.FetchRun, error) {
	var zero domain.FetchRun
	src, err := s.store.GetSource(ctx, sourceID)
	if err != nil {
		return src, nil, zero, err
	}
	if src.Status != domain.SourceStatusActive {
		return src, nil, zero, fmt.Errorf("%w: source %q is %s; only active sources are fetched", domain.ErrConflict, src.Slug, src.Status)
	}
	adapter, ok := s.adapters[src.Type]
	if !ok {
		return src, nil, zero, fmt.Errorf("%w: no fetch adapter is registered for source type %q", domain.ErrUnsupported, src.Type)
	}
	if !o.Force && src.LastFetchAt != nil {
		if wait := src.LastFetchAt.Add(src.MinFetchInterval).Sub(s.now()); wait > 0 {
			return src, nil, zero, &domain.CooldownError{Slug: src.Slug, Remaining: wait}
		}
	}
	trigger := o.Trigger
	if trigger == "" {
		trigger = domain.TriggerCLI
	}
	run, err := s.store.StartFetchRun(ctx, src.ID, trigger)
	if err != nil {
		return src, nil, zero, err
	}
	return src, adapter, run, nil
}

// execute runs the pipeline for a started run and records its outcome.
func (s *Service) execute(parent context.Context, src domain.Source, adapter sources.Adapter, run domain.FetchRun) (domain.FetchRun, error) {
	log := s.logger.With("run_id", run.ID, "source", src.Slug, "source_type", src.Type, "trigger", run.Trigger)
	log.Info("source fetch started")
	started := s.now()

	ctx, cancel := context.WithTimeoutCause(parent, s.fetchTimeout,
		fmt.Errorf("timed out after %s", s.fetchTimeout))
	res, fetchErr := s.safeFetch(ctx, adapter, src, log)
	var fetchFailure string
	if fetchErr != nil {
		// Must be described before cancel(): afterwards ctx.Err() is always
		// set and every failure would look like a cancellation.
		fetchFailure = describeFetchErr(ctx, fetchErr)
	}
	cancel()

	// Normalize and store whatever was fetched, even on partial failure.
	items, rejected, folded := normalizeBatch(src.ID, res.Items, started)
	stats := domain.FetchStats{Fetched: len(res.Items) - folded, Rejected: len(rejected)}
	if len(rejected) > 0 {
		log.Warn("candidates rejected", "count", len(rejected), "examples", rejectionExamples(rejected, 3))
	}

	var storeErr error
	if len(items) > 0 {
		sctx, scancel := context.WithTimeout(context.WithoutCancel(parent), storeTimeout)
		results, err := s.store.UpsertItems(sctx, items, started)
		scancel()
		if err != nil {
			storeErr = err // the batch transaction rolled back: nothing stored
		} else {
			for _, r := range results {
				stats.Record(r)
			}
		}
	}

	completion := domain.RunCompletion{Stats: stats}
	switch {
	case fetchErr != nil:
		completion.Err = fetchFailure
		if storeErr != nil {
			completion.Err += "; store items: " + storeErr.Error()
		}
	case storeErr != nil:
		completion.Err = "store items: " + storeErr.Error()
	default:
		completion.State = res.State
	}

	fctx, fcancel := context.WithTimeout(context.WithoutCancel(parent), finishTimeout)
	defer fcancel()
	finished, err := s.store.FinishFetchRun(fctx, run.ID, completion)
	if err != nil {
		log.Error("could not record fetch run outcome", "err", err, "intended_error", completion.Err)
		return run, fmt.Errorf("record fetch run %s: %w", run.ID, err)
	}

	attrs := []any{
		"duration", s.now().Sub(started).Round(time.Millisecond).String(),
		"requests", res.Requests,
		"fetched", stats.Fetched, "inserted", stats.Inserted, "updated", stats.Updated,
		"unchanged", stats.Unchanged, "duplicates", stats.Duplicate, "rejected", stats.Rejected,
		"folded", folded,
	}
	if finished.Status == domain.RunFailed {
		log.Error("source fetch failed", append(attrs, "err", finished.Error)...)
	} else {
		log.Info("source fetch completed", attrs...)
	}
	return finished, nil
}

// safeFetch calls the adapter, converting a panic into an error so one buggy
// adapter cannot crash the process.
func (s *Service) safeFetch(ctx context.Context, a sources.Adapter, src domain.Source, log *slog.Logger) (res sources.FetchResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("adapter panicked", "panic", p, "stack", string(debug.Stack()))
			res, err = sources.FetchResult{}, fmt.Errorf("adapter panicked: %v", p)
		}
	}()
	return a.Fetch(ctx, src)
}

// describeFetchErr explains a fetch failure, preferring the cancellation
// cause (timeout, shutdown, caller cancelled) when the context ended.
func describeFetchErr(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		if errors.Is(cause, context.Canceled) {
			return "fetch cancelled"
		}
		return "fetch aborted: " + cause.Error()
	}
	return "fetch: " + err.Error()
}

func rejectionExamples(rs []Rejection, n int) []string {
	out := make([]string, 0, min(n, len(rs)))
	for _, r := range rs[:min(n, len(rs))] {
		out = append(out, fmt.Sprintf("%s: %s", r.ExternalID, r.Reason))
	}
	return out
}
