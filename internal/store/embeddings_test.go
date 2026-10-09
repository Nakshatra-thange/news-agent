package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func testEmbedding(item domain.Item) domain.Embedding {
	return domain.Embedding{
		ItemID: item.ID, Provider: "fake", Model: "fake-embed-1", Dimensions: 3,
		Vector: []float32{0.6, 0, -0.8}, ContentHash: item.ContentHash,
	}
}

func TestEmbeddingOnePerItemAndModel(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"))
	item := itemsToEmbed(t, st, "fake-embed-1", 3, 10)[0]

	first, err := st.UpsertEmbedding(ctx, testEmbedding(item))
	if err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}
	if !slices.Equal(first.Vector, []float32{0.6, 0, -0.8}) || first.Dimensions != 3 {
		t.Errorf("stored = %+v, want the vector round-tripped", first)
	}
	same, err := st.UpsertEmbedding(ctx, testEmbedding(item))
	if err != nil || !same.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("identical upsert changed the row (%v -> %v, %v)", first.UpdatedAt, same.UpdatedAt, err)
	}
	next := testEmbedding(item)
	next.Vector = []float32{1, 0, 0}
	second, err := st.UpsertEmbedding(ctx, next)
	if err != nil || !slices.Equal(second.Vector, next.Vector) || !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("re-embedding = %+v (%v), want the same row updated", second, err)
	}
	other := testEmbedding(item)
	other.Model = "fake-embed-2"
	if _, err := st.UpsertEmbedding(ctx, other); err != nil {
		t.Fatalf("second model: %v", err)
	}
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM item_embeddings WHERE item_id = $1`, item.ID).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows for item = %d (%v), want one per model", n, err)
	}
	_, err = st.pool.Exec(ctx, `INSERT INTO item_embeddings (item_id, provider, model, dimensions, embedding, content_hash)
		VALUES ($1, 'fake', 'fake-embed-1', 1, '{1}', $2)`, item.ID, item.ContentHash)
	if !errors.Is(mapErr(err), domain.ErrConflict) {
		t.Errorf("duplicate raw insert error = %v, want conflict", err)
	}
}

func TestEmbeddingErrorsAndConstraints(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"))
	item := itemsToEmbed(t, st, "fake-embed-1", 3, 10)[0]

	missing := testEmbedding(item)
	missing.ItemID = uuid.New()
	if _, err := st.UpsertEmbedding(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("embedding for unknown item: %v, want ErrNotFound", err)
	}
	wrongDims := testEmbedding(item)
	wrongDims.Dimensions = 4
	var ve *domain.ValidationError
	if _, err := st.UpsertEmbedding(ctx, wrongDims); !errors.As(err, &ve) {
		t.Errorf("wrong dimensions: %v, want ValidationError", err)
	}
	if _, err := st.GetEmbedding(ctx, item.ID, "fake-embed-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetEmbedding after failures: %v, want ErrNotFound", err)
	}

	// The schema itself rejects malformed vectors and provenance.
	insert := `INSERT INTO item_embeddings (item_id, provider, model, dimensions, embedding, content_hash) VALUES ($1, $2, $3, $4, $5::real[], $6)`
	for name, args := range map[string][]any{
		"length mismatch": {item.ID, "fake", "m", 3, "{1,2}", item.ContentHash},
		"empty":           {item.ID, "fake", "m", 0, "{}", item.ContentHash},
		"too many dims":   {item.ID, "fake", "m", 4097, "{1}", item.ContentHash},
		"null element":    {item.ID, "fake", "m", 2, "{1,NULL}", item.ContentHash},
		"nan":             {item.ID, "fake", "m", 2, "{1,NaN}", item.ContentHash},
		"infinity":        {item.ID, "fake", "m", 2, "{1,-Infinity}", item.ContentHash},
		"bad provider":    {item.ID, "Fake Provider", "m", 1, "{1}", item.ContentHash},
		"blank model":     {item.ID, "fake", " ", 1, "{1}", item.ContentHash},
		"bad hash":        {item.ID, "fake", "m", 1, "{1}", []byte{1}},
	} {
		if _, err := st.pool.Exec(ctx, insert, args...); !errors.Is(mapErr(err), domain.ErrInvalid) {
			t.Errorf("%s: raw insert error = %v, want constraint violation", name, err)
		}
	}
	if _, err := st.pool.Exec(ctx, insert, item.ID, "fake", "m", 2, "{{1},{2}}", item.ContentHash); err == nil {
		t.Error("2-D array accepted")
	}
}

func TestItemsToEmbed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	arxiv := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	mustUpsert(t, st, testNow,
		newItem(arxiv.ID, "fresh", "https://example.com/fresh"),
		newItem(arxiv.ID, "changed", "https://example.com/changed"),
		newItem(arxiv.ID, "other-model", "https://example.com/other-model"),
		newItem(arxiv.ID, "other-dims", "https://example.com/other-dims"),
		newItem(arxiv.ID, "todo", "https://example.com/todo"),
	)
	mustUpsert(t, st, testNow, newItem(hn.ID, "dup", "https://example.com/todo")) // duplicate: never selected

	byExt := map[string]domain.Item{}
	for _, it := range itemsToEmbed(t, st, "fake-embed-1", 3, 100) {
		byExt[it.ExternalID] = it
	}
	if len(byExt) != 5 {
		t.Fatalf("selected %d items, want the 5 primaries", len(byExt))
	}
	embed := func(ext string, edit func(*domain.Embedding)) {
		e := testEmbedding(byExt[ext])
		edit(&e)
		if _, err := st.UpsertEmbedding(ctx, e); err != nil {
			t.Fatalf("embed %s: %v", ext, err)
		}
	}
	for _, ext := range []string{"fresh", "changed"} {
		embed(ext, func(*domain.Embedding) {})
	}
	embed("other-model", func(e *domain.Embedding) { e.Model = "fake-embed-0" })
	embed("other-dims", func(e *domain.Embedding) { e.Dimensions, e.Vector = 2, []float32{1, 0} })

	// The "changed" item's content changes after it was embedded.
	changed := newItem(arxiv.ID, "changed", "https://example.com/changed")
	changed.Title, changed.ContentHash = "Item changed, revised", hash("Item changed, revised")
	mustUpsert(t, st, testNow, changed)

	var got []string
	for _, it := range itemsToEmbed(t, st, "fake-embed-1", 3, 100) {
		got = append(got, it.ExternalID)
	}
	slices.Sort(got)
	if want := []string{"changed", "other-dims", "other-model", "todo"}; !slices.Equal(got, want) {
		t.Errorf("selection = %v, want %v", got, want)
	}
	if got := itemsToEmbed(t, st, "fake-embed-1", 3, 1); len(got) != 1 {
		t.Errorf("limit 1 returned %d", len(got))
	}
}

func itemsToEmbed(t *testing.T, st *Store, model string, dims, limit int) []domain.Item {
	t.Helper()
	items, err := st.ItemsToEmbed(context.Background(), model, dims, limit)
	if err != nil {
		t.Fatalf("ItemsToEmbed: %v", err)
	}
	return items
}
