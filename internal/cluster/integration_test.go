package cluster_test

import (
	"context"
	"crypto/sha256"
	"reflect"
	"testing"
	"time"

	"synergy/internal/cluster"
	"synergy/internal/domain"
	"synergy/internal/embed"
	"synergy/internal/store/storetest"
)

// TestClusteringWithPostgres embeds items with the offline fake provider and
// clusters them against the real store: two reports of the same story join,
// an unrelated item stays apart, unembedded items wait, repeated runs add
// nothing, and items are never modified.
func TestClusteringWithPostgres(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	hn, err := st.CreateSource(ctx, domain.NewSource{Slug: "hn", Name: "HN", Type: domain.SourceTypeHackerNews})
	if err != nil {
		t.Fatal(err)
	}
	h := func(s string) []byte { b := sha256.Sum256([]byte(s)); return b[:] }
	item := func(id, title, desc string) domain.NewItem {
		url := "https://example.com/" + id
		return domain.NewItem{
			SourceID: hn.ID, ExternalID: id, Kind: domain.ItemKindDiscussion, Title: title, Description: desc,
			URL: url, CanonicalURL: url, URLHash: h(url), ContentHash: h(title + desc),
		}
	}
	seen := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	news := []domain.NewItem{
		item("1", "Acme releases Falcon 3 open weights model", "Acme released Falcon 3, an open weights language model."),
		item("2", "Falcon 3 open weights model released by Acme", "Acme released the Falcon 3 open weights language model today."),
		item("3", "Protein folding benchmark for biology labs", "A new benchmark measures protein structure prediction."),
	}
	for i, n := range news {
		if _, err := st.UpsertItems(ctx, []domain.NewItem{n}, seen.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.ListItems(ctx, domain.ItemFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	before := page.Items
	ids := map[string]domain.FeedItem{}
	for _, it := range before {
		ids[it.ExternalID] = it
	}

	p := embed.FakeProvider{}
	svc := cluster.New(st, cluster.Options{Model: p.Model(), Dimensions: p.Dimensions(), Threshold: cluster.DefaultThreshold}, nil)

	// Nothing is embedded yet: nothing to cluster, nothing invented.
	if res, err := svc.Run(ctx, 10); err != nil || res.Selected != 0 {
		t.Fatalf("run without embeddings = %+v, %v", res, err)
	}
	if res, err := embed.New(st, p, nil).Run(ctx, 10); err != nil || res.Embedded != 3 {
		t.Fatalf("embed = %+v, %v", res, err)
	}

	// The limit bounds the run; the rest follows in the next one.
	if res, err := svc.Run(ctx, 1); err != nil || res.Selected != 1 || res.Created != 1 {
		t.Fatalf("first run = %+v, %v", res, err)
	}
	if res, err := svc.Run(ctx, 10); err != nil || res.Selected != 2 || res.Joined != 1 || res.Created != 1 {
		t.Fatalf("second run = %+v, %v; want 1 joined and 1 new story", res, err)
	}
	if res, err := svc.Run(ctx, 10); err != nil || res.Selected != 0 {
		t.Fatalf("third run = %+v, %v; want nothing selected (idempotent)", res, err)
	}

	story := func(ext string) string {
		id, err := st.ItemStory(ctx, ids[ext].ID, p.Model())
		if err != nil {
			t.Fatalf("story of %s: %v", ext, err)
		}
		return id.String()
	}
	if story("1") != story("2") {
		t.Error("two reports of the same story were not grouped")
	}
	if story("3") == story("1") {
		t.Error("an unrelated item joined the story")
	}

	// Stories of one model are invisible to another.
	other := cluster.New(st, cluster.Options{Model: "voyage-4-lite", Dimensions: embed.VoyageDimensions, Threshold: 0.8}, nil)
	if res, err := other.Run(ctx, 10); err != nil || res.Selected != 0 {
		t.Errorf("run for a model without embeddings = %+v, %v", res, err)
	}

	page, err = st.ListItems(ctx, domain.ItemFilter{Limit: 10})
	if err != nil || !reflect.DeepEqual(page.Items, before) {
		t.Errorf("clustering modified items (%v)", err)
	}
}
