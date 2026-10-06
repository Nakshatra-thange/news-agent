// Package store is Synergy's PostgreSQL data-access layer. It is the only
// package that speaks SQL. Methods accept and return domain types and map
// database errors onto domain sentinel errors (domain.ErrNotFound, ...).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"synergy/internal/domain"
)

// Options tunes the connection pool. Zero values use pgx defaults.
type Options struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// Store wraps a pgx connection pool.
type Store struct {
	pool     *pgxpool.Pool
	migrator *migrator
	target   Target
}

// Target identifies the database a Store points at, without credentials, so
// it is safe to log.
type Target struct {
	Host     string
	Port     uint16
	Database string
	User     string
}

// Open creates a connection pool. Connections are established lazily, so Open
// succeeds even if PostgreSQL is down; use Ping to check reachability.
func Open(ctx context.Context, databaseURL string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx error text can quote the connection string; never surface it.
		return nil, errors.New("parse database url: invalid PostgreSQL connection string")
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	cfg.MinConns = opts.MinConns
	if opts.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = opts.MaxConnLifetime
	}
	if opts.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = opts.MaxConnIdleTime
	}
	if opts.ConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "synergy"
	// Sessions work in UTC so timestamps round-trip predictably.
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	m, err := newMigrator(pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{
		pool:     pool,
		migrator: m,
		target: Target{
			Host:     cfg.ConnConfig.Host,
			Port:     cfg.ConnConfig.Port,
			Database: cfg.ConnConfig.Database,
			User:     cfg.ConnConfig.User,
		},
	}, nil
}

// Target describes the database this store connects to (no credentials).
func (s *Store) Target() Target { return s.target }

// Ping checks that PostgreSQL is reachable and answering queries.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close releases all connections. It waits for acquired connections to be
// released, so call it after the HTTP server has shut down.
func (s *Store) Close() {
	_ = s.migrator.close()
	s.pool.Close()
}

// inTx runs fn in a transaction, committing on success and rolling back on
// error or panic.
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

func newID() uuid.UUID {
	// NewV7 only fails if the system random source fails.
	return uuid.Must(uuid.NewV7())
}

// PostgreSQL error codes we translate.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgNotNullViolation    = "23502"
	pgInvalidText         = "22P02"
)

// mapErr translates pgx/PostgreSQL errors into domain errors, keeping the
// original error in the chain for logging.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case pgUniqueViolation:
		if pgErr.ConstraintName == "fetch_runs_one_running_per_source" {
			return domain.ErrFetchInProgress
		}
		return fmt.Errorf("%w: %s already exists (%s)", domain.ErrConflict, pgErr.TableName, pgErr.ConstraintName)
	case pgForeignKeyViolation:
		return fmt.Errorf("%w: referenced row does not exist (%s)", domain.ErrNotFound, pgErr.ConstraintName)
	case pgCheckViolation, pgNotNullViolation, pgInvalidText:
		return fmt.Errorf("%w: database rejected value (%s): %s", domain.ErrInvalid, pgErr.ConstraintName, pgErr.Message)
	}
	return err
}

// utc normalizes optional timestamps read from the database.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// pgTime truncates t to PostgreSQL's microsecond precision, in UTC.
func pgTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
