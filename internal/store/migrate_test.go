package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

func queryStrings(t *testing.T, st *Store, sql string) []string {
	t.Helper()
	rows, err := st.pool.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return out
}

func appTables(t *testing.T, st *Store) []string {
	return queryStrings(t, st, `
		SELECT table_name::text FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name <> 'goose_db_version'
		ORDER BY table_name`)
}

var coreTables = []string{"fetch_runs", "item_enrichments", "item_summaries", "items", "sources"}

func TestMigrateUpFromEmptySchema(t *testing.T) {
	ctx := context.Background()
	st := newUnmigratedStore(t)

	current, latest, err := st.SchemaVersions(ctx)
	if err != nil {
		t.Fatalf("SchemaVersions: %v", err)
	}
	if current != 0 || latest < 1 {
		t.Fatalf("before migrating: current=%d latest=%d, want 0 and >=1", current, latest)
	}

	applied, err := st.MigrateUp(ctx)
	if err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	if int64(len(applied)) != latest {
		t.Errorf("applied %d migrations, want %d", len(applied), latest)
	}
	if got := appTables(t, st); !slices.Equal(got, coreTables) {
		t.Errorf("tables = %v, want %v", got, coreTables)
	}
	if current, _, _ = st.SchemaVersions(ctx); current != latest {
		t.Errorf("after migrating: current=%d, want %d", current, latest)
	}

	// Idempotent: a second run applies nothing.
	again, err := st.MigrateUp(ctx)
	if err != nil || len(again) != 0 {
		t.Errorf("second MigrateUp applied %d (err %v), want 0", len(again), err)
	}
}

func TestMigrateDownIsReversible(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, err := st.MigrateDownTo(ctx, 0); err != nil {
		t.Fatalf("MigrateDownTo(0): %v", err)
	}
	if got := appTables(t, st); len(got) != 0 {
		t.Errorf("tables after full rollback = %v, want none", got)
	}
	if current, _, _ := st.SchemaVersions(ctx); current != 0 {
		t.Errorf("version after rollback = %d, want 0", current)
	}

	if _, err := st.MigrateUp(ctx); err != nil {
		t.Fatalf("MigrateUp after rollback: %v", err)
	}
	if got := appTables(t, st); !slices.Equal(got, coreTables) {
		t.Errorf("tables after re-apply = %v, want %v", got, coreTables)
	}
}

func TestMigrationStatuses(t *testing.T) {
	st := newTestStore(t)
	statuses, err := st.MigrationStatuses(context.Background())
	if err != nil {
		t.Fatalf("MigrationStatuses: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("no migrations reported")
	}
	names := make([]string, len(statuses))
	for i, m := range statuses {
		names[i] = m.Name
	}
	if want := []string{"00001_core_schema.sql", "00002_fetch_runs_rejected.sql", "00003_hackernews_firebase_config.sql", "00004_items_search_index.sql", "00005_item_enrichments.sql", "00006_item_summaries.sql"}; !slices.Equal(names, want) {
		t.Errorf("migrations = %v, want %v", names, want)
	}
	for _, m := range statuses {
		if !m.Applied || m.AppliedAt.IsZero() {
			t.Errorf("migration %s: applied=%v at=%v, want applied", m.Name, m.Applied, m.AppliedAt)
		}
	}
}

func TestSchemaIndexes(t *testing.T) {
	st := newTestStore(t)
	got := queryStrings(t, st, `SELECT indexname::text FROM pg_indexes WHERE schemaname = current_schema()`)
	for _, want := range []string{
		"sources_pkey", "sources_slug_key",
		"items_pkey", "items_source_external_key", "items_feed_idx", "items_source_feed_idx",
		"items_url_hash_idx", "items_duplicate_of_idx", "items_tags_idx", "items_search_idx", "item_enrichments_pkey", "item_summaries_pkey",
		"fetch_runs_pkey", "fetch_runs_one_running_per_source", "fetch_runs_source_started_idx",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing index %s (have %v)", want, got)
		}
	}
}

// TestDatabaseConstraints writes invalid rows with raw SQL, bypassing Go
// validation, to prove the schema itself protects data integrity.
func TestDatabaseConstraints(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	item := mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/a"))[0]

	tests := []struct {
		name string
		sql  string
		args []any
		want error
	}{
		{"duplicate slug", `INSERT INTO sources (id, slug, name, type) VALUES ($1, 'hn', 'x', 'hackernews')`,
			[]any{newID()}, domain.ErrConflict},
		{"bad slug", `INSERT INTO sources (id, slug, name, type) VALUES ($1, 'Bad Slug', 'x', 'hackernews')`,
			[]any{newID()}, domain.ErrInvalid},
		{"unknown status", `INSERT INTO sources (id, slug, name, type, status) VALUES ($1, 's1', 'x', 'arxiv', 'deleted')`,
			[]any{newID()}, domain.ErrInvalid},
		{"retired without retired_at", `INSERT INTO sources (id, slug, name, type, status) VALUES ($1, 's2', 'x', 'arxiv', 'retired')`,
			[]any{newID()}, domain.ErrInvalid},
		{"config not object", `INSERT INTO sources (id, slug, name, type, config) VALUES ($1, 's3', 'x', 'arxiv', '[]')`,
			[]any{newID()}, domain.ErrInvalid},
		{"item for missing source", `INSERT INTO items (id, source_id, external_id, kind, title, url, canonical_url, url_hash, content_hash)
			VALUES ($1, $2, 'x', 'paper', 't', 'u', 'u', $3, $3)`, []any{newID(), newID(), hash("x")}, domain.ErrNotFound},
		{"same-source duplicate external id", `INSERT INTO items (id, source_id, external_id, kind, title, url, canonical_url, url_hash, content_hash)
			VALUES ($1, $2, '1', 'paper', 't', 'u', 'u', $3, $3)`, []any{newID(), src.ID, hash("x")}, domain.ErrConflict},
		{"url hash wrong length", `INSERT INTO items (id, source_id, external_id, kind, title, url, canonical_url, url_hash, content_hash)
			VALUES ($1, $2, 'y', 'paper', 't', 'u', 'u', '\x01', $3)`, []any{newID(), src.ID, hash("x")}, domain.ErrInvalid},
		{"blank title", `INSERT INTO items (id, source_id, external_id, kind, title, url, canonical_url, url_hash, content_hash)
			VALUES ($1, $2, 'z', 'paper', '  ', 'u', 'u', $3, $3)`, []any{newID(), src.ID, hash("x")}, domain.ErrInvalid},
		{"item is its own duplicate", `UPDATE items SET duplicate_of = id WHERE id = $1`, []any{item.ID}, domain.ErrInvalid},
		{"unknown trigger", `INSERT INTO fetch_runs (id, source_id, trigger) VALUES ($1, $2, 'cron')`,
			[]any{newID(), src.ID}, domain.ErrInvalid},
		{"running run with finished_at", `INSERT INTO fetch_runs (id, source_id, trigger, finished_at) VALUES ($1, $2, 'cli', now())`,
			[]any{newID(), src.ID}, domain.ErrInvalid},
		{"negative count", `INSERT INTO fetch_runs (id, source_id, trigger, items_inserted) VALUES ($1, $2, 'cli', -1)`,
			[]any{newID(), src.ID}, domain.ErrInvalid},
		{"negative rejected count", `INSERT INTO fetch_runs (id, source_id, trigger, items_rejected) VALUES ($1, $2, 'cli', -1)`,
			[]any{newID(), src.ID}, domain.ErrInvalid},
		{"delete source with items", `DELETE FROM sources WHERE id = $1`, []any{src.ID}, nil /* any error */},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := st.pool.Exec(ctx, tt.sql, tt.args...)
			if err == nil {
				t.Fatal("statement succeeded, want constraint violation")
			}
			if tt.want != nil && !errors.Is(mapErr(err), tt.want) {
				t.Errorf("error = %v (mapped %v), want %v", err, mapErr(err), tt.want)
			}
		})
	}
}

// TestMigrateUpgradesExistingDatabase upgrades a database holding data from
// an earlier Phase 1 schema version and checks the data survives and the
// data migrations apply.
func TestMigrateUpgradesExistingDatabase(t *testing.T) {
	ctx := context.Background()
	st := newUnmigratedStore(t)
	if _, err := st.migrator.provider.UpTo(ctx, 2); err != nil {
		t.Fatalf("UpTo(2): %v", err)
	}

	// A Hacker News source with the pre-00003 search-API configuration.
	srcID := newID()
	if _, err := st.pool.Exec(ctx, `INSERT INTO sources (id, slug, name, type, config)
		VALUES ($1, 'hn-old', 'HN', 'hackernews', '{"max_results_per_query": 50, "max_items": 25}')`, srcID); err != nil {
		t.Fatalf("insert v2 source: %v", err)
	}
	item := newItem(srcID, "1", "https://example.com/agents")
	item.Title = "Autonomous agents in production"
	mustUpsert(t, st, testNow, item)

	applied, err := st.MigrateUp(ctx)
	if err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	if len(applied) != 4 || applied[0].Version != 3 || applied[3].Version != 6 {
		t.Fatalf("applied = %+v, want versions 3 to 6", applied)
	}

	src, err := st.GetSource(ctx, srcID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(src.Config, &cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	if _, old := cfg["max_results_per_query"]; old {
		t.Errorf("config still has max_results_per_query: %s", src.Config)
	}
	if cfg["max_items"] != float64(25) || cfg["lists"] == nil {
		t.Errorf("config = %s, want explicit max_items kept and lists added", src.Config)
	}

	page, err := st.ListItems(ctx, domain.ItemFilter{Query: "agents"})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != item.Title {
		t.Errorf("search after upgrade = %+v, want the pre-existing item", page.Items)
	}
}

// TestConcurrentMigrateUp runs two migrators against one empty schema at
// once: the advisory lock must serialize them so every migration is applied
// exactly once and neither fails.
func TestConcurrentMigrateUp(t *testing.T) {
	ctx := context.Background()
	a := newUnmigratedStore(t)
	b, err := Open(ctx, a.pool.Config().ConnString(), Options{MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(b.Close)

	var (
		wg      sync.WaitGroup
		applied [2]int
		errs    [2]error
	)
	for i, st := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := st.MigrateUp(ctx)
			applied[i], errs[i] = len(r), err
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("MigrateUp errors: %v, %v", errs[0], errs[1])
	}
	_, latest, _ := a.SchemaVersions(ctx)
	if int64(applied[0]+applied[1]) != latest {
		t.Errorf("applied %d + %d migrations, want %d in total", applied[0], applied[1], latest)
	}
	var rows int
	if err := a.pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version WHERE version_id > 0`).Scan(&rows); err != nil || int64(rows) != latest {
		t.Errorf("goose_db_version has %d applied rows (%v), want %d", rows, err, latest)
	}
}
