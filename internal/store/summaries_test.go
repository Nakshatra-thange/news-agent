package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func testSummary(item domain.Item) domain.Summary {
	return domain.Summary{
		ItemID: item.ID, Text: "A paper that measures how tool-using agents scale with model size.",
		Provider: "fake", Model: "fake-1", PromptVersion: 1, ContentHash: item.ContentHash,
	}
}

func TestSummaryOnePerItem(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"))
	item := itemsToSummarize(t, st, "fake-1", 1, 10)[0]

	first, err := st.UpsertSummary(ctx, testSummary(item))
	if err != nil {
		t.Fatalf("UpsertSummary: %v", err)
	}
	same, err := st.UpsertSummary(ctx, testSummary(item))
	if err != nil || !same.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("identical upsert changed the row (%v -> %v, %v)", first.UpdatedAt, same.UpdatedAt, err)
	}
	next := testSummary(item)
	next.Text = "A revised summary of the paper about agents and their scaling."
	second, err := st.UpsertSummary(ctx, next)
	if err != nil || second.Text != next.Text || !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("re-summary = %+v (%v), want the same row updated", second, err)
	}
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM item_summaries WHERE item_id = $1`, item.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows for item = %d (%v), want 1", n, err)
	}
	_, err = st.pool.Exec(ctx, `INSERT INTO item_summaries (item_id, summary, provider, model, prompt_version, content_hash)
		VALUES ($1, $2, 'fake', 'm', 1, $3)`, item.ID, strings.Repeat("x", 50), item.ContentHash)
	if !errors.Is(mapErr(err), domain.ErrConflict) {
		t.Errorf("second raw insert error = %v, want conflict", err)
	}
}

func TestSummaryErrorsAndConstraints(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"))
	item := itemsToSummarize(t, st, "fake-1", 1, 10)[0]

	missing := testSummary(item)
	missing.ItemID = uuid.New()
	if _, err := st.UpsertSummary(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("summary for unknown item: %v, want ErrNotFound", err)
	}
	short := testSummary(item)
	short.Text = "Too short."
	var ve *domain.ValidationError
	if _, err := st.UpsertSummary(ctx, short); !errors.As(err, &ve) {
		t.Errorf("invalid summary: %v, want ValidationError", err)
	}
	if _, err := st.GetSummary(ctx, item.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSummary after failures: %v, want ErrNotFound", err)
	}

	// The schema itself rejects out-of-bounds summaries.
	insert := `INSERT INTO item_summaries (item_id, summary, provider, model, prompt_version, content_hash) VALUES ($1, $2, $3, 'm', $4, $5)`
	for name, args := range map[string][]any{
		"too short":    {item.ID, "short", "fake", 1, item.ContentHash},
		"too long":     {item.ID, strings.Repeat("x", 601), "fake", 1, item.ContentHash},
		"multi line":   {item.ID, strings.Repeat("x", 30) + "\n" + strings.Repeat("y", 30), "fake", 1, item.ContentHash},
		"bad provider": {item.ID, strings.Repeat("x", 50), "Fake Provider", 1, item.ContentHash},
		"bad version":  {item.ID, strings.Repeat("x", 50), "fake", 0, item.ContentHash},
		"bad hash":     {item.ID, strings.Repeat("x", 50), "fake", 1, []byte{1}},
	} {
		if _, err := st.pool.Exec(ctx, insert, args...); !errors.Is(mapErr(err), domain.ErrInvalid) {
			t.Errorf("%s: raw insert error = %v, want constraint violation", name, err)
		}
	}
}

func TestItemsToSummarize(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	arxiv := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	mustUpsert(t, st, testNow,
		newItem(arxiv.ID, "fresh", "https://example.com/fresh"),
		newItem(arxiv.ID, "stale", "https://example.com/stale"),
		newItem(arxiv.ID, "other-model", "https://example.com/other-model"),
		newItem(arxiv.ID, "todo", "https://example.com/todo"),
	)
	mustUpsert(t, st, testNow, newItem(hn.ID, "dup", "https://example.com/todo")) // duplicate: never selected

	byExt := map[string]domain.Item{}
	for _, it := range itemsToSummarize(t, st, "fake-1", 1, 100) {
		byExt[it.ExternalID] = it
	}
	if len(byExt) != 4 {
		t.Fatalf("selected %d items, want the 4 primaries", len(byExt))
	}
	summarize := func(ext string, edit func(*domain.Summary)) {
		s := testSummary(byExt[ext])
		edit(&s)
		if _, err := st.UpsertSummary(ctx, s); err != nil {
			t.Fatalf("summarize %s: %v", ext, err)
		}
	}
	summarize("fresh", func(*domain.Summary) {})
	summarize("stale", func(s *domain.Summary) { s.ContentHash = hash("older content") })
	summarize("other-model", func(s *domain.Summary) { s.Model = "fake-0" })

	var got []string
	for _, it := range itemsToSummarize(t, st, "fake-1", 1, 100) {
		got = append(got, it.ExternalID)
	}
	slices.Sort(got)
	if want := []string{"other-model", "stale", "todo"}; !slices.Equal(got, want) {
		t.Errorf("selection = %v, want %v", got, want)
	}
	if got := itemsToSummarize(t, st, "fake-1", 2, 100); len(got) != 4 {
		t.Errorf("a new prompt version selected %d items, want all 4", len(got))
	}
	if got := itemsToSummarize(t, st, "fake-1", 1, 1); len(got) != 1 {
		t.Errorf("limit 1 returned %d", len(got))
	}
}

func itemsToSummarize(t *testing.T, st *Store, model string, prompt, limit int) []domain.Item {
	t.Helper()
	items, err := st.ItemsToSummarize(context.Background(), model, prompt, limit)
	if err != nil {
		t.Fatalf("ItemsToSummarize: %v", err)
	}
	return items
}
