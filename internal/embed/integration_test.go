package embed_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/embed"
	"synergy/internal/store"
	"synergy/internal/store/storetest"
)

func newItem(src domain.Source, id, title, url string) domain.NewItem {
	h := func(s string) []byte { b := sha256.Sum256([]byte(s)); return b[:] }
	return domain.NewItem{
		SourceID: src.ID, ExternalID: id, Kind: domain.ItemKindPaper, Title: title,
		Description: "About " + title, URL: url, CanonicalURL: url, URLHash: h(url),
		ContentHash: h(title), Tags: []string{"cs.AI"},
	}
}

// failing returns an error for every item, like a provider outage.
type failing struct{ embed.FakeProvider }

func (failing) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("provider unavailable")
}

// TestEmbeddingsWithPostgres runs the service against the real store with
// the fake provider: the limit, idempotency, re-embedding after content
// changes, and provider failures leaving items and existing embeddings
// untouched.
func TestEmbeddingsWithPostgres(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	src, err := st.CreateSource(ctx, domain.NewSource{Slug: "arxiv-ai", Name: "arXiv", Type: domain.SourceTypeArxiv})
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if _, err := st.UpsertItems(ctx, []domain.NewItem{
		newItem(src, "1", "Agents at Scale", "https://arxiv.org/abs/1"),
		newItem(src, "2", "Reasoning Models", "https://arxiv.org/abs/2"),
		newItem(src, "3", "Tool Use Benchmarks", "https://arxiv.org/abs/3"),
	}, seen); err != nil {
		t.Fatal(err)
	}
	items := listItems(t, st)
	svc := embed.New(st, embed.FakeProvider{}, nil)

	if res, err := svc.Run(ctx, 2); err != nil || res.Selected != 2 || res.Embedded != 2 {
		t.Fatalf("first run = %+v, %v; want 2 embedded", res, err)
	}
	if res, err := svc.Run(ctx, 10); err != nil || res.Selected != 1 || res.Embedded != 1 {
		t.Fatalf("second run = %+v, %v; want the remaining 1", res, err)
	}
	if res, err := svc.Run(ctx, 10); err != nil || res.Selected != 0 {
		t.Fatalf("third run = %+v, %v; want nothing selected (idempotent)", res, err)
	}
	first, err := st.GetEmbedding(ctx, items[0].ID, "fake-embed-1")
	if err != nil || first.Provider != "fake" || first.Dimensions != embed.FakeDimensions || len(first.Vector) != embed.FakeDimensions {
		t.Fatalf("stored embedding = %+v (%v)", first, err)
	}
	// The stored vector round-trips exactly and supports cosine similarity.
	want, _ := embed.FakeProvider{}.Embed(ctx, "Agents at Scale\n\nAbout Agents at Scale")
	if !reflect.DeepEqual(first.Vector, want) {
		t.Error("stored vector differs from the provider's")
	}
	if sim, err := domain.CosineSimilarity(first.Vector, want); err != nil || sim < 0.9999 {
		t.Errorf("similarity with itself = %v, %v", sim, err)
	}
	if after := listItems(t, st); !reflect.DeepEqual(after, items) {
		t.Error("embedding modified items")
	}

	// Item 1 changes: it is due a new embedding. A failing provider leaves
	// both the item and its existing embedding untouched.
	if _, err := st.UpsertItems(ctx, []domain.NewItem{newItem(src, "1", "Agents at Scale, revised", "https://arxiv.org/abs/1")}, seen.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := listItems(t, st)
	res, err := embed.New(st, failing{}, nil).Run(ctx, 10)
	if err != nil || res.Selected != 1 || res.Embedded != 0 || len(res.Failures) != 1 {
		t.Fatalf("failing run = %+v, %v; want 1 failure for the changed item", res, err)
	}
	if after := listItems(t, st); !reflect.DeepEqual(after, before) {
		t.Error("a failed embedding modified items")
	}
	if kept, _ := st.GetEmbedding(ctx, items[0].ID, "fake-embed-1"); !reflect.DeepEqual(kept, first) {
		t.Error("a failed embedding modified the existing embedding")
	}

	// A working provider re-embeds it from the new content, on the same row.
	if res, err := svc.Run(ctx, 10); err != nil || res.Embedded != 1 {
		t.Fatalf("refresh run = %+v, %v; want 1", res, err)
	}
	refreshed, _ := st.GetEmbedding(ctx, items[0].ID, "fake-embed-1")
	if reflect.DeepEqual(refreshed.Vector, first.Vector) || reflect.DeepEqual(refreshed.ContentHash, first.ContentHash) ||
		!refreshed.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("refreshed embedding = %+v, want a new vector and hash on the same row", refreshed)
	}
}

func listItems(t *testing.T, st *store.Store) []domain.FeedItem {
	t.Helper()
	page, err := st.ListItems(context.Background(), domain.ItemFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]domain.FeedItem, len(page.Items))
	for i, it := range page.Items {
		out[len(out)-1-i] = it // oldest first, matching insertion order
	}
	return out
}
