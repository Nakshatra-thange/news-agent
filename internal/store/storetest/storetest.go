// Package storetest gives other packages' integration tests a real,
// isolated PostgreSQL store. Tests are skipped when TEST_DATABASE_URL is
// unset. Each call creates a fresh schema, applies all migrations, and drops
// the schema when the test ends.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"synergy/internal/store"
)

// New returns a migrated Store bound to a new, throwaway schema.
func New(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL is not a valid PostgreSQL URL")
	}
	if !strings.HasSuffix(cfg.Database, "_test") {
		t.Fatalf("refusing to run: TEST_DATABASE_URL database %q must end in _test", cfg.Database)
	}

	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "test_" + hex.EncodeToString(b[:])

	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("parse TEST_DATABASE_URL")
	}
	q := u.Query()
	q.Set("search_path", schema) // pgx passes unknown URL params as session settings
	u.RawQuery = q.Encode()

	st, err := store.Open(ctx, u.String(), store.Options{MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if _, err := st.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return st
}
