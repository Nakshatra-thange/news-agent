package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"synergy/internal/config"
	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/ingest"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/store"
)

// adapters lists the fetch adapter for each source type. Adding a source
// type means adding its adapter here; nothing else in the pipeline changes.
// All adapters share one set of per-upstream rate limiters.
func adapters(cfg config.IngestConfig, logger *slog.Logger) []sources.Adapter {
	opts := sources.ClientOptions{
		UserAgent: cfg.UserAgent,
		Limiters:  httpx.NewLimiters(),
		Logger:    logger,
	}
	return []sources.Adapter{
		hackernews.NewAdapter(opts),
		arxiv.NewAdapter(opts),
		github.NewAdapter(opts, cfg.GitHubToken),
	}
}

func newIngest(st *store.Store, cfg config.Config, logger *slog.Logger) (*ingest.Service, error) {
	return ingest.New(st, adapters(cfg.Ingest, logger), ingest.Options{FetchTimeout: cfg.Ingest.FetchTimeout, Logger: logger})
}

const fetchUsage = `Usage:
  synergy fetch <source>...   Fetch the given sources (slug or UUID) now
  synergy fetch --all         Fetch every active source, highest priority first

Flags:
  --force   Ignore each source's min_fetch_interval cooldown
`

// fetch runs ingestion synchronously from the command line and prints one
// summary line per source. It exits non-zero if any requested fetch failed.
// With --all, sources that cannot be fetched right now (cooldown, no adapter)
// are reported as skipped rather than treated as failures.
func fetch(ctx context.Context, args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	var (
		all, force bool
		refs       []string
	)
	for _, a := range args {
		switch a {
		case "--all", "-all":
			all = true
		case "--force", "-force":
			force = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, fetchUsage)
			return nil
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(logOut, "unknown flag %q\n\n%s", a, fetchUsage)
				return errUsage
			}
			refs = append(refs, a)
		}
	}
	if all == (len(refs) > 0) {
		fmt.Fprint(logOut, "specify source slugs/IDs or --all (not both)\n\n"+fetchUsage)
		return errUsage
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config:\n%w", err)
	}
	logger := newLogger(logOut, cfg.Log)
	st, err := openStore(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := pingDB(ctx, st, cfg.Database); err != nil {
		logger.Error("database unreachable", targetAttrs(st), "err", err)
		return err
	}
	reg, err := newRegistry(st, logger)
	if err != nil {
		return err
	}
	svc, err := newIngest(st, cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = svc.Shutdown(context.Background()) }()

	var targets []domain.Source
	if all {
		targets, err = reg.List(ctx, sources.ListFilter{Statuses: []domain.SourceStatus{domain.SourceStatusActive}})
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			fmt.Fprintln(stdout, "no active sources (run `synergy seed` to register the defaults)")
			return nil
		}
	} else {
		for _, ref := range refs {
			src, err := reg.Get(ctx, ref)
			if err != nil {
				return fmt.Errorf("source %q: %w", ref, err)
			}
			targets = append(targets, src)
		}
	}

	failed := 0
	for _, src := range targets {
		if ctx.Err() != nil {
			break
		}
		run, err := svc.Run(ctx, src.ID, ingest.RunOptions{Trigger: domain.TriggerCLI, Force: force})
		switch {
		case err != nil && all && (errors.Is(err, domain.ErrTooSoon) || errors.Is(err, domain.ErrUnsupported)):
			fmt.Fprintf(stdout, "%-18s skipped: %v\n", src.Slug, err)
		case err != nil:
			failed++
			fmt.Fprintf(stdout, "%-18s not run: %v\n", src.Slug, err)
		case run.Status == domain.RunFailed:
			failed++
			fmt.Fprintf(stdout, "%-18s failed after %s: %s\n", src.Slug, run.Duration().Round(time.Millisecond), run.Error)
		default:
			s := run.Stats
			fmt.Fprintf(stdout, "%-18s ok in %s: fetched %d, inserted %d (%d duplicates), updated %d, unchanged %d, rejected %d\n",
				src.Slug, run.Duration().Round(time.Millisecond), s.Fetched, s.Inserted, s.Duplicate, s.Updated, s.Unchanged, s.Rejected)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d fetches failed", failed, len(targets))
	}
	return nil
}
