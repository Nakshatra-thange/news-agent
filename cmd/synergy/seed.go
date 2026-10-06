package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"synergy/internal/config"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
)

// sourceTypes lists every source type Synergy supports. Adding a source type
// means adding its spec here.
func sourceTypes() []sources.TypeSpec {
	return []sources.TypeSpec{hackernews.Spec{}, arxiv.Spec{}, github.Spec{}}
}

func newRegistry(st sources.Store, logger *slog.Logger) (*sources.Registry, error) {
	return sources.NewRegistry(st, logger, sourceTypes()...)
}

// seed registers the default sources for every type. Existing sources (by
// slug) are left untouched, so it is safe to run repeatedly.
func seed(ctx context.Context, getenv func(string) string, stdout, logOut io.Writer) error {
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
	res, err := reg.Seed(ctx)
	for _, s := range res.Created {
		fmt.Fprintf(stdout, "created  %-18s %-11s priority %d\n", s.Slug, s.Type, s.Priority)
	}
	for _, slug := range res.Skipped {
		fmt.Fprintf(stdout, "exists   %s (left unchanged)\n", slug)
	}
	if err != nil {
		logger.Error("seeding failed", "err", err)
		return err
	}
	return nil
}
