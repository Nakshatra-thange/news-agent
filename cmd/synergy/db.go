package main

import (
	"context"
	"fmt"
	"log/slog"

	"synergy/internal/config"
	"synergy/internal/store"
)

// openStore creates the connection pool. It does not connect; see checkDB.
func openStore(ctx context.Context, cfg config.DatabaseConfig) (*store.Store, error) {
	st, err := store.Open(ctx, cfg.URL, store.Options{
		MaxConns:        cfg.MaxConns,
		MinConns:        cfg.MinConns,
		MaxConnLifetime: cfg.MaxConnLifetime,
		MaxConnIdleTime: cfg.MaxConnIdleTime,
		ConnectTimeout:  cfg.ConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return st, nil
}

// pingDB verifies connectivity, bounded by the connect timeout.
func pingDB(ctx context.Context, st *store.Store, cfg config.DatabaseConfig) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := st.Ping(ctx); err != nil {
		t := st.Target()
		return fmt.Errorf("cannot reach PostgreSQL at %s:%d (database %q, user %q): %w", t.Host, t.Port, t.Database, t.User, err)
	}
	return nil
}

// targetAttrs describes the database for logs, without credentials.
func targetAttrs(st *store.Store) slog.Attr {
	t := st.Target()
	return slog.Group("db",
		slog.String("host", t.Host),
		slog.Int("port", int(t.Port)),
		slog.String("database", t.Database),
		slog.String("user", t.User),
	)
}
