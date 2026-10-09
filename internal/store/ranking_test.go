package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// rankFixture stores items whose ranked order differs from newest first:
// importance shifts, ignored enrichments (fake provider, stale content),
// and ties broken by discovery time, then ID.
func rankFixture(t *testing.T, st *Store) (hn, ax domain.Source, byExt map[string]domain.Item) {
	t.Helper()
	ctx := context.Background()
	hn = mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	ax = mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mk := func(src uuid.UUID, ext string, hoursAgo int) domain.NewItem {
		n := newItem(src, ext, "https://x.test/"+ext)
		n.PublishedAt = ptr(testNow.Add(-time.Duration(hoursAgo) * time.Hour))
		return n
	}
	paper := func(n domain.NewItem) domain.NewItem {
		n.Kind, n.Tags, n.Title = domain.ItemKindPaper, []string{"cs.LG"}, n.Title+" transformers"
		return n
	}
	mustUpsert(t, st, testNow,
		mk(hn.ID, "new", 1),                   // no enrichment: -1h
		paper(mk(ax.ID, "old-important", 30)), // importance 5: -30h + 24h = -6h
		mk(hn.ID, "mid-minor", 2),             // importance 1: -2h - 24h = -26h
		mk(hn.ID, "fake", 3),                  // fake importance 5 ignored: -3h
		paper(mk(ax.ID, "stale", 4)),          // stale importance 5 ignored: -4h
		mk(hn.ID, "neutral", 5),               // importance 3: -5h
		mk(hn.ID, "tie-a", 10),                // importance 3: -10h, discovered first
		mk(hn.ID, "tie-c", 12),                // no enrichment: -12h
		mk(hn.ID, "tie-d", 24),                // importance 4: -24h + 12h = -12h, same discovery as tie-c
	)
	mustUpsert(t, st, testNow.Add(time.Minute), mk(hn.ID, "tie-b", 22)) // importance 4: -10h, discovered later

	byExt = map[string]domain.Item{}
	for _, it := range itemsToEmbed(t, st, "m", 3, 100) {
		byExt[it.ExternalID] = it
	}
	for ext, imp := range map[string]int{"old-important": 5, "mid-minor": 1, "stale": 5, "neutral": 3, "tie-a": 3, "tie-d": 4, "tie-b": 4} {
		it := byExt[ext]
		if _, err := st.UpsertEnrichment(ctx, domain.Enrichment{
			ItemID: it.ID, Topics: []string{"ai"}, Importance: imp, Category: domain.Categories[0],
			Provider: "anthropic", Model: "claude-opus-5-5", PromptVersion: 1, ContentHash: it.ContentHash,
		}); err != nil {
			t.Fatalf("enrich %s: %v", ext, err)
		}
	}
	it := byExt["fake"]
	if _, err := st.UpsertEnrichment(ctx, domain.Enrichment{
		ItemID: it.ID, Topics: []string{"ai"}, Importance: 5, Category: domain.Categories[0],
		Provider: "fake", Model: "fake-1", PromptVersion: 1, ContentHash: it.ContentHash,
	}); err != nil {
		t.Fatal(err)
	}
	stale := paper(mk(ax.ID, "stale", 4))
	stale.Title, stale.ContentHash = "Item stale revised transformers", hash("revised")
	mustUpsert(t, st, testNow, stale)
	return hn, ax, byExt
}

func TestListItemsRanked(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	_, _, byExt := rankFixture(t, st)

	page, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	tieCD := []string{"tie-c", "tie-d"} // same rank and discovery time: higher ID first
	if byExt["tie-c"].ID.String() < byExt["tie-d"].ID.String() {
		tieCD = []string{"tie-d", "tie-c"}
	}
	want := append([]string{"new", "fake", "stale", "neutral", "old-important", "tie-b", "tie-a"}, tieCD...)
	want = append(want, "mid-minor")
	if got := externalIDs(page.Items); !slices.Equal(got, want) {
		t.Errorf("ranked = %v\nwant     %v", got, want)
	}

	// The SQL score matches domain.RankAt.
	imp := map[string]int{"old-important": 5, "mid-minor": 1, "neutral": 3, "tie-a": 3, "tie-d": 4, "tie-b": 4}
	for _, it := range page.Items {
		var p *int
		if v, ok := imp[it.ExternalID]; ok {
			p = &v
		}
		if want := domain.RankAt(it.FeedAt, p, 0); !it.RankAt.Equal(want) {
			t.Errorf("%s: rank_at %v, want %v", it.ExternalID, it.RankAt, want)
		}
	}

	// The default order is unchanged: newest first.
	def, err := st.ListItems(ctx, domain.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := externalIDs(def.Items)[:4], []string{"new", "mid-minor", "fake", "stale"}; !slices.Equal(got, want) {
		t.Errorf("default order starts %v, want %v", got, want)
	}

	// Identical data gives an identical order.
	again, _ := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked})
	if !slices.Equal(feedIDs(again.Items), feedIDs(page.Items)) {
		t.Error("ranked order is not deterministic")
	}
}

func TestListItemsRankedPagination(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rankFixture(t, st)
	all, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked})
	if err != nil || len(all.Items) != 10 {
		t.Fatalf("full listing = %d items (%v)", len(all.Items), err)
	}
	for limit := 1; limit <= 4; limit++ {
		var paged []domain.FeedItem
		var after *domain.ItemCursor
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatal("pagination did not terminate")
			}
			page, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Limit: limit, After: after})
			if err != nil {
				t.Fatalf("limit %d: %v", limit, err)
			}
			paged = append(paged, page.Items...)
			if page.Next == nil {
				break
			}
			after = page.Next
		}
		if !slices.Equal(feedIDs(paged), feedIDs(all.Items)) {
			t.Errorf("limit %d: paged %v != full %v", limit, externalIDs(paged), externalIDs(all.Items))
		}
	}

	// A cursor only works with its own sort; unknown sorts are rejected.
	first, _ := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Limit: 2})
	if _, err := st.ListItems(ctx, domain.ItemFilter{After: first.Next}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("ranked cursor in newest order: %v, want invalid", err)
	}
	if _, err := st.ListItems(ctx, domain.ItemFilter{Sort: "top"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown sort: %v, want invalid", err)
	}
}

func TestListItemsRankedFilters(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	hn, ax, _ := rankFixture(t, st)
	at := func(h int) *time.Time { return ptr(testNow.Add(time.Duration(-h) * time.Hour)) }
	// Every filter selects the same items in both orders.
	filters := map[string]domain.ItemFilter{
		"source":           {SourceIDs: []uuid.UUID{ax.ID}},
		"source type":      {SourceTypes: []domain.SourceType{domain.SourceTypeHackerNews}},
		"source status":    {SourceStatuses: []domain.SourceStatus{domain.SourceStatusActive}},
		"kind":             {Kinds: []domain.ItemKind{domain.ItemKindPaper}},
		"tag":              {Tags: []string{"cs.LG"}},
		"since until":      {Since: at(11), Until: at(2)},
		"discovered range": {DiscoveredSince: ptr(testNow.Add(30 * time.Second))},
		"query":            {Query: "transformers"},
		"duplicates":       {IncludeDuplicates: true, SourceIDs: []uuid.UUID{hn.ID}},
	}
	for name, f := range filters {
		newest, err := st.ListItems(ctx, f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f.Sort = domain.SortRanked
		ranked, err := st.ListItems(ctx, f)
		if err != nil {
			t.Fatalf("%s ranked: %v", name, err)
		}
		a, b := externalIDs(newest.Items), externalIDs(ranked.Items)
		slices.Sort(a)
		slices.Sort(b)
		if len(a) == 0 || !slices.Equal(a, b) {
			t.Errorf("%s: newest selects %v, ranked %v", name, a, b)
		}
	}
}
