package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func testEnrichment(item domain.Item) domain.Enrichment {
	return domain.Enrichment{
		ItemID: item.ID, Topics: []string{"agents"}, Entities: []domain.Entity{{Name: "Anthropic", Type: "organization"}},
		Importance: 3, Category: "research", Provider: "fake", Model: "fake-1", PromptVersion: 1,
		ContentHash: item.ContentHash,
	}
}

func TestEnrichmentOnePerItem(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"))
	item := itemsToEnrich(t, st, "fake-1", 1, 10)[0]

	e := testEnrichment(item)
	first, err := st.UpsertEnrichment(ctx, e)
	if err != nil {
		t.Fatalf("UpsertEnrichment: %v", err)
	}
	if first.ItemID != item.ID || !slices.Equal(first.Topics, []string{"agents"}) || first.Entities[0].Name != "Anthropic" {
		t.Errorf("stored = %+v", first)
	}

	e.Topics, e.Importance = []string{"reasoning"}, 5
	second, err := st.UpsertEnrichment(ctx, e)
	if err != nil || second.Importance != 5 || !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("re-enrichment = %+v (%v), want the same row updated", second, err)
	}
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM item_enrichments WHERE item_id = $1`, item.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows for item = %d (%v), want 1", n, err)
	}

	// The primary key enforces one row per item even for raw inserts.
	_, err = st.pool.Exec(ctx, `INSERT INTO item_enrichments (item_id, topics, entities, importance, category, provider, model, prompt_version, content_hash)
		VALUES ($1, '{a}', '[]', 1, 'other', 'fake', 'm', 1, $2)`, item.ID, item.ContentHash)
	if !errors.Is(mapErr(err), domain.ErrConflict) {
		t.Errorf("second raw insert error = %v, want conflict", err)
	}
}

func TestEnrichmentErrors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"))
	item := itemsToEnrich(t, st, "fake-1", 1, 10)[0]

	missing := testEnrichment(item)
	missing.ItemID = uuid.New()
	if _, err := st.UpsertEnrichment(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("enrichment for unknown item: %v, want ErrNotFound", err)
	}
	invalid := testEnrichment(item)
	invalid.Importance = 7
	var ve *domain.ValidationError
	if _, err := st.UpsertEnrichment(ctx, invalid); !errors.As(err, &ve) {
		t.Errorf("invalid enrichment: %v, want ValidationError", err)
	}
	if _, err := st.GetEnrichment(ctx, item.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetEnrichment after failures: %v, want ErrNotFound (nothing stored)", err)
	}

	// The schema itself rejects out-of-bounds values.
	for name, sql := range map[string]string{
		"importance": `INSERT INTO item_enrichments (item_id, topics, entities, importance, category, provider, model, prompt_version, content_hash) VALUES ($1, '{a}', '[]', 0, 'other', 'fake', 'm', 1, $2)`,
		"entities":   `INSERT INTO item_enrichments (item_id, topics, entities, importance, category, provider, model, prompt_version, content_hash) VALUES ($1, '{a}', '{}', 1, 'other', 'fake', 'm', 1, $2)`,
		"category":   `INSERT INTO item_enrichments (item_id, topics, entities, importance, category, provider, model, prompt_version, content_hash) VALUES ($1, '{a}', '[]', 1, 'Bad Category', 'fake', 'm', 1, $2)`,
		"hash":       `INSERT INTO item_enrichments (item_id, topics, entities, importance, category, provider, model, prompt_version, content_hash) VALUES ($1, '{a}', '[]', 1, 'other', 'fake', 'm', 1, '\x01')`,
	} {
		args := []any{item.ID, item.ContentHash}
		if name == "hash" {
			args = args[:1]
		}
		if _, err := st.pool.Exec(ctx, sql, args...); !errors.Is(mapErr(err), domain.ErrInvalid) {
			t.Errorf("%s: raw insert error = %v, want constraint violation", name, err)
		}
	}
}

func TestItemsToEnrich(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	arxiv := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	mustUpsert(t, st, testNow,
		newItem(arxiv.ID, "fresh", "https://example.com/fresh"),
		newItem(arxiv.ID, "stale", "https://example.com/stale"),
		newItem(arxiv.ID, "other-model", "https://example.com/other-model"),
		newItem(arxiv.ID, "old-prompt", "https://example.com/old-prompt"),
		newItem(arxiv.ID, "todo", "https://example.com/todo"),
	)
	// A cross-source duplicate is never selected.
	mustUpsert(t, st, testNow, newItem(hn.ID, "dup", "https://example.com/todo"))

	byExt := map[string]domain.Item{}
	for _, it := range itemsToEnrich(t, st, "fake-1", 1, 100) {
		byExt[it.ExternalID] = it
	}
	if len(byExt) != 5 {
		t.Fatalf("selected %d items, want the 5 primaries", len(byExt))
	}
	enrichAs := func(ext string, edit func(*domain.Enrichment)) {
		e := testEnrichment(byExt[ext])
		edit(&e)
		if _, err := st.UpsertEnrichment(ctx, e); err != nil {
			t.Fatalf("enrich %s: %v", ext, err)
		}
	}
	enrichAs("fresh", func(*domain.Enrichment) {})
	enrichAs("stale", func(e *domain.Enrichment) { e.ContentHash = hash("older content") })
	enrichAs("other-model", func(e *domain.Enrichment) { e.Model = "fake-0" })
	enrichAs("old-prompt", func(*domain.Enrichment) {}) // current at prompt v1, stale at v2

	var got []string
	for _, it := range itemsToEnrich(t, st, "fake-1", 2, 100) {
		got = append(got, it.ExternalID)
	}
	slices.Sort(got)
	// With prompt version 2 every enrichment above is stale.
	if want := []string{"fresh", "old-prompt", "other-model", "stale", "todo"}; !slices.Equal(got, want) {
		t.Errorf("prompt v2 selection = %v, want %v", got, want)
	}

	got = nil
	for _, it := range itemsToEnrich(t, st, "fake-1", 1, 100) {
		got = append(got, it.ExternalID)
	}
	slices.Sort(got)
	if want := []string{"other-model", "stale", "todo"}; !slices.Equal(got, want) {
		t.Errorf("selection = %v, want %v (missing, stale content or other model)", got, want)
	}
	if n := len(itemsToEnrich(t, st, "fake-1", 1, 2)); n != 2 {
		t.Errorf("limit 2 returned %d", n)
	}
}

func itemsToEnrich(t *testing.T, st *Store, model string, prompt, limit int) []domain.Item {
	t.Helper()
	items, err := st.ItemsToEnrich(context.Background(), model, prompt, limit)
	if err != nil {
		t.Fatalf("ItemsToEnrich: %v", err)
	}
	return items
}
