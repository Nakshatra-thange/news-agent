package store

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"synergy/migrations"
)

// migrator applies the embedded migrations using goose. A PostgreSQL
// advisory lock ensures only one process migrates at a time.
type migrator struct {
	db       *sql.DB
	provider *goose.Provider
}

func newMigrator(pool *pgxpool.Pool) (*migrator, error) {
	// Shares the pool's connections; closing db does not close the pool.
	db := stdlib.OpenDBFromPool(pool)
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return &migrator{db: db, provider: p}, nil
}

func (m *migrator) close() error {
	return m.provider.Close()
}

// MigrationResult describes one applied or rolled-back migration.
type MigrationResult struct {
	Version  int64
	Name     string
	Duration time.Duration
}

// MigrationStatus describes one known migration.
type MigrationStatus struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time
}

// MigrateUp applies all pending migrations and returns what it applied.
func (s *Store) MigrateUp(ctx context.Context) ([]MigrationResult, error) {
	results, err := s.migrator.provider.Up(ctx)
	if err != nil {
		return toResults(results), fmt.Errorf("migrate up: %w", err)
	}
	return toResults(results), nil
}

// MigrateDown rolls back the most recently applied migration.
func (s *Store) MigrateDown(ctx context.Context) (MigrationResult, error) {
	r, err := s.migrator.provider.Down(ctx)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("migrate down: %w", err)
	}
	return toResult(r), nil
}

// MigrateDownTo rolls back every migration newer than version.
func (s *Store) MigrateDownTo(ctx context.Context, version int64) ([]MigrationResult, error) {
	results, err := s.migrator.provider.DownTo(ctx, version)
	if err != nil {
		return toResults(results), fmt.Errorf("migrate down to %d: %w", version, err)
	}
	return toResults(results), nil
}

// MigrationStatuses lists every embedded migration and whether it is applied.
func (s *Store) MigrationStatuses(ctx context.Context) ([]MigrationStatus, error) {
	st, err := s.migrator.provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration status: %w", err)
	}
	out := make([]MigrationStatus, len(st))
	for i, m := range st {
		out[i] = MigrationStatus{
			Version:   m.Source.Version,
			Name:      path.Base(m.Source.Path),
			Applied:   m.State == goose.StateApplied,
			AppliedAt: m.AppliedAt,
		}
	}
	return out, nil
}

// SchemaVersions returns the database's current schema version and the latest
// version embedded in this binary.
func (s *Store) SchemaVersions(ctx context.Context) (current, latest int64, err error) {
	current, latest, err = s.migrator.provider.GetVersions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("schema versions: %w", err)
	}
	return current, latest, nil
}

func toResult(r *goose.MigrationResult) MigrationResult {
	if r == nil || r.Source == nil {
		return MigrationResult{}
	}
	return MigrationResult{Version: r.Source.Version, Name: path.Base(r.Source.Path), Duration: r.Duration}
}

func toResults(rs []*goose.MigrationResult) []MigrationResult {
	out := make([]MigrationResult, 0, len(rs))
	for _, r := range rs {
		out = append(out, toResult(r))
	}
	return out
}
