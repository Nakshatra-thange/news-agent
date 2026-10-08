// Package scheduler fetches active sources automatically. On every tick it
// starts a fetch, through the ingestion service, for each active source
// whose min_fetch_interval has elapsed since its last completed run.
//
// It adds no fetch logic of its own. The ingestion service runs the fetch
// and records it as a fetch run (trigger "scheduler"), and the database's
// one-running-run-per-source rule prevents overlapping fetches, including
// with fetches started from the CLI or the API.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/ingest"
	"synergy/internal/sources"
)

// Sources lists the registered sources.
type Sources interface {
	List(ctx context.Context, f sources.ListFilter) ([]domain.Source, error)
}

// Fetcher starts background fetches; *ingest.Service implements it.
type Fetcher interface {
	Start(ctx context.Context, sourceID uuid.UUID, o ingest.RunOptions) (domain.FetchRun, error)
	RecoverAbandoned(ctx context.Context) (int64, error)
}

// Scheduler periodically starts fetches for due sources.
type Scheduler struct {
	sources  Sources
	fetcher  Fetcher
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time
}

// New builds a Scheduler that checks for due sources every interval.
func New(src Sources, f Fetcher, interval time.Duration, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{sources: src, fetcher: f, interval: interval, logger: logger, now: time.Now}
}

// Run ticks immediately and then every interval until ctx is cancelled.
// Fetches it started keep running; the ingestion service's Shutdown waits
// for or cancels them.
func (s *Scheduler) Run(ctx context.Context) {
	s.logger.Info("scheduler started", "interval", s.interval.String())
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-t.C:
		}
	}
}

// Tick starts a fetch for every due active source and returns how many it
// started. A source that cannot be started is logged and skipped; it never
// prevents the others from starting.
func (s *Scheduler) Tick(ctx context.Context) int {
	// A crashed process can leave a run "running" forever, which would
	// block its source. Only runs older than any live fetch are touched.
	if _, err := s.fetcher.RecoverAbandoned(ctx); err != nil && ctx.Err() == nil {
		s.logger.Warn("scheduler: could not check for abandoned fetch runs", "err", err)
	}

	active, err := s.sources.List(ctx, sources.ListFilter{Statuses: []domain.SourceStatus{domain.SourceStatusActive}})
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("scheduler: could not list sources", "err", err)
		}
		return 0
	}

	now := s.now()
	started := 0
	for _, src := range active { // highest priority first
		if ctx.Err() != nil {
			break
		}
		if !Due(src, now) {
			continue
		}
		run, err := s.fetcher.Start(ctx, src.ID, ingest.RunOptions{Trigger: domain.TriggerScheduler})
		switch {
		case err == nil:
			started++
			s.logger.Info("scheduled fetch started", "source", src.Slug, "run_id", run.ID)
		case errors.Is(err, ingest.ErrClosed):
			return started // shutting down
		case errors.Is(err, domain.ErrFetchInProgress), errors.Is(err, domain.ErrTooSoon):
			// Already running, or fetched recently from the CLI or API.
			s.logger.Debug("scheduled fetch skipped", "source", src.Slug, "reason", err.Error())
		default:
			s.logger.Warn("scheduled fetch could not start", "source", src.Slug, "err", err)
		}
	}
	return started
}

// Due reports whether src should be fetched at now: it has never completed
// a fetch, or its min_fetch_interval has elapsed since its last completed
// run (succeeded, failed or recovered as abandoned).
func Due(src domain.Source, now time.Time) bool {
	last := lastCompleted(src)
	return last == nil || !now.Before(last.Add(src.MinFetchInterval))
}

func lastCompleted(src domain.Source) *time.Time {
	switch {
	case src.LastSuccessAt == nil:
		return src.LastFailureAt
	case src.LastFailureAt == nil || src.LastSuccessAt.After(*src.LastFailureAt):
		return src.LastSuccessAt
	default:
		return src.LastFailureAt
	}
}
