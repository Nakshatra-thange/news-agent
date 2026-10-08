package enrich_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/enrich"
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

// badProvider returns malformed output for every item.
type badProvider struct{}

func (badProvider) Name() string  { return "fake" }
func (badProvider) Model() string { return "fake-1" }
func (badProvider) Complete(context.Context, enrich.Prompt) (string, error) {
	return `{"topics": ["x"], "importance": "very"}`, nil
}

// TestEnrichmentWithPostgres runs the service against the real store with
// the fake provider: storage, idempotency, staleness after content changes,
// and malformed output leaving items and existing enrichments untouched.
func TestEnrichmentWithPostgres(t *testing.T) {
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
	svc := enrich.New(st, enrich.FakeProvider{}, nil)

	// The limit bounds the run.
	res, err := svc.Run(ctx, 2)
	if err != nil || res.Selected != 2 || res.Enriched != 2 {
		t.Fatalf("first run = %+v, %v; want 2 enriched", res, err)
	}
	// The rest, then nothing: repeating a run does no work.
	if res, err = svc.Run(ctx, 10); err != nil || res.Selected != 1 || res.Enriched != 1 {
		t.Fatalf("second run = %+v, %v; want the remaining 1", res, err)
	}
	if res, err = svc.Run(ctx, 10); err != nil || res.Selected != 0 {
		t.Fatalf("third run = %+v, %v; want nothing selected (idempotent)", res, err)
	}
	first, err := st.GetEnrichment(ctx, items[0].ID)
	if err != nil {
		t.Fatalf("GetEnrichment: %v", err)
	}
	if first.Provider != "fake" || first.Model != "fake-1" || len(first.Topics) == 0 || first.Importance < 1 {
		t.Errorf("stored enrichment = %+v", first)
	}

	// Storing the identical enrichment again changes nothing.
	again, err := st.UpsertEnrichment(ctx, first)
	if err != nil || !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("identical upsert: updated_at %v -> %v (%v), want unchanged", first.UpdatedAt, again.UpdatedAt, err)
	}

	// Enrichment never modified the items.
	if after := listItems(t, st); !reflect.DeepEqual(after, items) {
		t.Error("items changed during enrichment")
	}

	// New item content makes its enrichment stale; a malformed reply then
	// fails without touching the item or the old enrichment.
	changed := newItem(src, "1", "Agents at Scale, revised", "https://arxiv.org/abs/1")
	if _, err := st.UpsertItems(ctx, []domain.NewItem{changed}, seen.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := listItems(t, st)
	res, err = enrich.New(st, badProvider{}, nil).Run(ctx, 10)
	if err != nil || res.Selected != 1 || res.Enriched != 0 || len(res.Failures) != 1 ||
		!errors.Is(res.Failures[0].Err, enrich.ErrInvalidOutput) {
		t.Fatalf("malformed run = %+v, %v; want 1 invalid-output failure", res, err)
	}
	if after := listItems(t, st); !reflect.DeepEqual(after, before) {
		t.Error("a failed enrichment modified items")
	}
	if kept, _ := st.GetEnrichment(ctx, items[0].ID); !reflect.DeepEqual(kept, first) {
		t.Error("a failed enrichment modified the existing enrichment")
	}

	// A good provider then refreshes it from the new content.
	if res, err = svc.Run(ctx, 10); err != nil || res.Enriched != 1 {
		t.Fatalf("refresh run = %+v, %v; want 1", res, err)
	}
	refreshed, _ := st.GetEnrichment(ctx, items[0].ID)
	if reflect.DeepEqual(refreshed.ContentHash, first.ContentHash) || !refreshed.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("refresh: content hash unchanged or row recreated (%+v)", refreshed)
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
