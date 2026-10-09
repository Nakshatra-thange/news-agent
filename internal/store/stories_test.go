package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func TestItemsToClusterSelection(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	arxiv := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	var news []domain.NewItem
	for _, ext := range []string{"ready", "clustered", "stale", "missing", "other-dims", "other-model"} {
		news = append(news, newItem(arxiv.ID, ext, "https://example.com/"+ext))
	}
	mustUpsert(t, st, testNow, news...)
	mustUpsert(t, st, testNow, newItem(hn.ID, "dup", "https://example.com/ready")) // cross-source duplicate

	byExt := map[string]domain.Item{}
	for _, it := range itemsToEmbed(t, st, "fake-embed-1", 3, 100) {
		byExt[it.ExternalID] = it
	}
	embed := func(ext string, edit func(*domain.Embedding)) {
		e := testEmbedding(byExt[ext])
		edit(&e)
		if _, err := st.UpsertEmbedding(ctx, e); err != nil {
			t.Fatalf("embed %s: %v", ext, err)
		}
	}
	for _, ext := range []string{"ready", "clustered", "stale"} {
		embed(ext, func(*domain.Embedding) {})
	}
	embed("other-dims", func(e *domain.Embedding) { e.Dimensions, e.Vector = 2, []float32{1, 0} })
	embed("other-model", func(e *domain.Embedding) { e.Model = "fake-embed-0" })
	if _, err := st.CreateStory(ctx, "fake-embed-1", byExt["clustered"].ID); err != nil {
		t.Fatalf("CreateStory: %v", err)
	}
	stale := newItem(arxiv.ID, "stale", "https://example.com/stale")
	stale.Title, stale.ContentHash = "Item stale, revised", hash("Item stale, revised")
	mustUpsert(t, st, testNow, stale)

	got, err := st.ItemsToCluster(ctx, "fake-embed-1", 3, 100)
	if err != nil || len(got) != 1 || got[0].ItemID != byExt["ready"].ID || !slices.Equal(got[0].Vector, []float32{0.6, 0, -0.8}) {
		t.Errorf("ItemsToCluster = %+v, %v; want only the ready item with its vector", got, err)
	}
	seeds, err := st.StorySeeds(ctx, "fake-embed-1", 3, 10)
	if err != nil || len(seeds) != 1 || !slices.Equal(seeds[0].Vector, []float32{0.6, 0, -0.8}) {
		t.Errorf("StorySeeds = %+v, %v", seeds, err)
	}
	if seeds, _ := st.StorySeeds(ctx, "fake-embed-1", 2, 10); len(seeds) != 0 {
		t.Errorf("seeds with other dimensions = %+v, want none", seeds)
	}
}

func TestStoryConstraints(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://example.com/1"), newItem(src.ID, "2", "https://example.com/2"))
	items := itemsToEmbed(t, st, "m", 3, 10)
	a, b := items[0].ID, items[1].ID

	story, err := st.CreateStory(ctx, "m", a)
	if err != nil {
		t.Fatalf("CreateStory: %v", err)
	}
	if got, err := st.ItemStory(ctx, a, "m"); err != nil || got != story {
		t.Errorf("seed's story = %v, %v; want %v", got, err, story)
	}
	// An item belongs to at most one story per model.
	if _, err := st.CreateStory(ctx, "m", a); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second story for the seed: %v, want conflict", err)
	}
	if err := st.AddStoryItem(ctx, story, a, "m", 0.9); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate membership: %v, want conflict", err)
	}
	// The failed CreateStory left no story behind.
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM stories`).Scan(&n); err != nil || n != 1 {
		t.Errorf("stories = %d (%v), want 1", n, err)
	}
	// Membership must use the story's model, an existing story and item,
	// and a valid similarity.
	for name, err := range map[string]error{
		"other model":   st.AddStoryItem(ctx, story, b, "other", 0.9),
		"missing story": st.AddStoryItem(ctx, uuid.New(), b, "m", 0.9),
		"missing item":  st.AddStoryItem(ctx, story, uuid.New(), "m", 0.9),
	} {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v, want not found", name, err)
		}
	}
	if err := st.AddStoryItem(ctx, story, b, "m", 1.5); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("similarity 1.5: %v, want invalid", err)
	}
	if err := st.AddStoryItem(ctx, story, b, "m", 0.9); err != nil {
		t.Fatalf("AddStoryItem: %v", err)
	}
	// The same item may belong to a story of another model.
	if _, err := st.CreateStory(ctx, "other", b); err != nil {
		t.Errorf("story for another model: %v", err)
	}
	if _, err := st.ItemStory(ctx, uuid.New(), "m"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ItemStory of unknown item: %v", err)
	}
}
