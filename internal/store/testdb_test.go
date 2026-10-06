package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

// Integration tests run against a real PostgreSQL database named by
// TEST_DATABASE_URL and are skipped when it is unset. Each test gets its own
// freshly migrated schema, dropped afterwards, so tests are isolated from each
// other and from any existing data.

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL is not a valid PostgreSQL URL")
	}
	// Guard against pointing the test suite at a real database.
	if !strings.HasSuffix(cfg.Database, "_test") {
		t.Fatalf("refusing to run: TEST_DATABASE_URL database %q must end in _test", cfg.Database)
	}
	return raw
}

// newUnmigratedStore returns a Store bound to a new, empty schema.
func newUnmigratedStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	raw := testDatabaseURL(t)

	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "test_" + hex.EncodeToString(b[:])

	admin, err := pgx.Connect(ctx, raw)
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

	// Unknown URL parameters become session settings in pgx.
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("parse TEST_DATABASE_URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	st, err := Open(ctx, u.String(), Options{MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// newTestStore returns a Store bound to a new schema with all migrations applied.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st := newUnmigratedStore(t)
	if _, err := st.MigrateUp(context.Background()); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return st
}

// testNow is a fixed, microsecond-precision reference time.
var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func mustCreateSource(t *testing.T, st *Store, slug string, typ domain.SourceType) domain.Source {
	t.Helper()
	src, err := st.CreateSource(context.Background(), domain.NewSource{Slug: slug, Name: slug, Type: typ})
	if err != nil {
		t.Fatalf("create source %s: %v", slug, err)
	}
	return src
}

// newItem builds a valid NewItem; canonical URL and hashes derive from url.
func newItem(sourceID uuid.UUID, externalID, url string) domain.NewItem {
	return domain.NewItem{
		SourceID:     sourceID,
		ExternalID:   externalID,
		Kind:         domain.ItemKindDiscussion,
		Title:        "Item " + externalID,
		URL:          url,
		CanonicalURL: url,
		URLHash:      hash(url),
		ContentHash:  hash("Item " + externalID),
	}
}

func mustUpsert(t *testing.T, st *Store, seenAt time.Time, items ...domain.NewItem) []domain.UpsertResult {
	t.Helper()
	res, err := st.UpsertItems(context.Background(), items, seenAt)
	if err != nil {
		t.Fatalf("upsert items: %v", err)
	}
	return res
}
