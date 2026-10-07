package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func TestUpsertInsertsAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)

	published := testNow.Add(-6 * time.Hour)
	in := domain.NewItem{
		SourceID:      src.ID,
		ExternalID:    "2410.12345",
		Kind:          domain.ItemKindPaper,
		Title:         "Scaling Laws for Agents",
		Description:   "We study...",
		URL:           "https://arxiv.org/abs/2410.12345v2",
		CanonicalURL:  "https://arxiv.org/abs/2410.12345",
		URLHash:       hash("https://arxiv.org/abs/2410.12345"),
		DiscussionURL: "",
		Authors:       []string{"A. Author", "B. Author"},
		Tags:          []string{"cs.LG", "cs.AI"},
		ContentHash:   hash("Scaling Laws for Agents|We study..."),
		Metadata:      json.RawMessage(`{"primary_category":"cs.LG","pdf_url":"https://arxiv.org/pdf/2410.12345"}`),
		PublishedAt:   &published,
	}
	res := mustUpsert(t, st, testNow, in)
	if len(res) != 1 || res[0].Outcome != domain.UpsertInserted || res[0].DuplicateOf != nil {
		t.Fatalf("result = %+v, want one insert without duplicate", res)
	}
	if res[0].ID.Version() != 7 {
		t.Errorf("item ID version = %d, want 7", res[0].ID.Version())
	}

	got, err := st.GetItem(ctx, res[0].ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.SourceID != src.ID || got.ExternalID != in.ExternalID || got.Kind != in.Kind || got.Title != in.Title ||
		got.Description != in.Description || got.URL != in.URL || got.CanonicalURL != in.CanonicalURL {
		t.Errorf("scalar fields mismatch: %+v", got)
	}
	if !bytes.Equal(got.URLHash, in.URLHash) || !bytes.Equal(got.ContentHash, in.ContentHash) {
		t.Error("hashes did not round-trip")
	}
	if !slices.Equal(got.Authors, in.Authors) || !slices.Equal(got.Tags, in.Tags) {
		t.Errorf("authors=%v tags=%v", got.Authors, got.Tags)
	}
	var meta map[string]string
	if err := json.Unmarshal(got.Metadata, &meta); err != nil || meta["primary_category"] != "cs.LG" {
		t.Errorf("metadata = %s (%v)", got.Metadata, err)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(published) {
		t.Errorf("PublishedAt = %v, want %v", got.PublishedAt, published)
	}
	if !got.DiscoveredAt.Equal(testNow) || !got.LastSeenAt.Equal(testNow) || !got.UpdatedAt.Equal(testNow) {
		t.Errorf("discovered=%v last_seen=%v updated=%v, want all %v", got.DiscoveredAt, got.LastSeenAt, got.UpdatedAt, testNow)
	}
	if !got.FeedAt.Equal(published) {
		t.Errorf("FeedAt = %v, want PublishedAt %v", got.FeedAt, published)
	}
}

func TestFeedAtFallsBackToDiscoveredAt(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "gh", domain.SourceTypeGitHub)

	res := mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://github.com/a/b"))
	got, err := st.GetItem(ctx, res[0].ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.PublishedAt != nil || !got.FeedAt.Equal(testNow) {
		t.Errorf("PublishedAt=%v FeedAt=%v, want nil and %v", got.PublishedAt, got.FeedAt, testNow)
	}
	if len(got.Authors) != 0 || got.Authors == nil || string(got.Metadata) != "{}" {
		t.Errorf("defaults: authors=%#v metadata=%s", got.Authors, got.Metadata)
	}
}

func TestUpsertSameSourceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	item := newItem(src.ID, "42", "https://example.com/post")

	first := mustUpsert(t, st, testNow, item)[0]
	later := testNow.Add(time.Hour)
	second := mustUpsert(t, st, later, item)[0]

	if second.Outcome != domain.UpsertUnchanged || second.ID != first.ID {
		t.Fatalf("second upsert = %+v, want unchanged with same ID %s", second, first.ID)
	}
	got, _ := st.GetItem(ctx, first.ID)
	if !got.LastSeenAt.Equal(later) {
		t.Errorf("LastSeenAt = %v, want %v", got.LastSeenAt, later)
	}
	if !got.DiscoveredAt.Equal(testNow) || !got.UpdatedAt.Equal(testNow) {
		t.Errorf("discovered=%v updated=%v, want both unchanged at %v", got.DiscoveredAt, got.UpdatedAt, testNow)
	}

	// An out-of-order (older) sighting never moves last_seen_at backwards.
	mustUpsert(t, st, testNow.Add(-time.Hour), item)
	got, _ = st.GetItem(ctx, first.ID)
	if !got.LastSeenAt.Equal(later) {
		t.Errorf("LastSeenAt moved backwards to %v", got.LastSeenAt)
	}

	var n int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Errorf("row count = %d (%v), want 1", n, err)
	}
}

func TestUpsertDetectsChanges(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "gh", domain.SourceTypeGitHub)

	base := newItem(src.ID, "repo-1", "https://github.com/o/r")
	base.Metadata = json.RawMessage(`{"stars": 10, "language": "Go"}`)
	id := mustUpsert(t, st, testNow, base)[0].ID

	tests := []struct {
		name   string
		mutate func(*domain.NewItem)
		want   domain.UpsertOutcome
	}{
		{"metadata key order only", func(n *domain.NewItem) { n.Metadata = json.RawMessage(`{"language":"Go","stars":10}`) }, domain.UpsertUnchanged},
		{"nil vs empty authors", func(n *domain.NewItem) { n.Authors = []string{} }, domain.UpsertUnchanged},
		{"metadata value", func(n *domain.NewItem) { n.Metadata = json.RawMessage(`{"stars": 11, "language": "Go"}`) }, domain.UpsertUpdated},
		{"title and content hash", func(n *domain.NewItem) { n.Title = "Renamed"; n.ContentHash = hash("Renamed") }, domain.UpsertUpdated},
		{"tags", func(n *domain.NewItem) { n.Tags = []string{"llm"} }, domain.UpsertUpdated},
		{"published date", func(n *domain.NewItem) { n.PublishedAt = ptr(testNow.Add(-time.Hour)) }, domain.UpsertUpdated},
	}
	cur := base
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mutate(&cur)
			seen := testNow.Add(time.Duration(i+1) * time.Minute)
			r := mustUpsert(t, st, seen, cur)[0]
			if r.Outcome != tt.want || r.ID != id {
				t.Fatalf("outcome = %s (id %s), want %s (id %s)", r.Outcome, r.ID, tt.want, id)
			}
			got, _ := st.GetItem(ctx, id)
			if tt.want == domain.UpsertUpdated && !got.UpdatedAt.Equal(seen) {
				t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, seen)
			}
			if tt.want == domain.UpsertUnchanged && got.UpdatedAt.Equal(seen) {
				t.Error("UpdatedAt moved for an unchanged item")
			}
		})
	}
	got, _ := st.GetItem(ctx, id)
	if got.Title != "Renamed" || !slices.Equal(got.Tags, []string{"llm"}) {
		t.Errorf("final item = %+v", got)
	}
}

func TestCrossSourceDuplicateLinking(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	ax := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	gh := mustCreateSource(t, st, "gh", domain.SourceTypeGitHub)
	const shared = "https://arxiv.org/abs/2410.00001"

	primary := mustUpsert(t, st, testNow, newItem(hn.ID, "hn-1", shared))[0]
	if primary.DuplicateOf != nil {
		t.Fatalf("first sighting linked as duplicate: %+v", primary)
	}

	dup := mustUpsert(t, st, testNow.Add(time.Minute), newItem(ax.ID, "2410.00001", shared))[0]
	if dup.Outcome != domain.UpsertInserted || dup.DuplicateOf == nil || *dup.DuplicateOf != primary.ID {
		t.Fatalf("second source result = %+v, want insert linked to %s", dup, primary.ID)
	}

	// A third source links to the primary, never to another duplicate.
	third := mustUpsert(t, st, testNow.Add(2*time.Minute), newItem(gh.ID, "gh-1", shared))[0]
	if third.DuplicateOf == nil || *third.DuplicateOf != primary.ID {
		t.Errorf("third source linked to %v, want primary %s", third.DuplicateOf, primary.ID)
	}

	// The same URL twice within one source is not a cross-source duplicate.
	repost := mustUpsert(t, st, testNow.Add(3*time.Minute), newItem(hn.ID, "hn-2", shared))[0]
	if repost.DuplicateOf != nil {
		t.Errorf("same-source repost linked as duplicate of %v", *repost.DuplicateOf)
	}

	// Re-seeing a duplicate keeps its link and reports no new link.
	again := mustUpsert(t, st, testNow.Add(4*time.Minute), newItem(ax.ID, "2410.00001", shared))[0]
	if again.Outcome != domain.UpsertUnchanged || again.DuplicateOf != nil {
		t.Errorf("re-upsert = %+v, want unchanged without a new link", again)
	}
	stored, _ := st.GetItem(ctx, dup.ID)
	if stored.DuplicateOf == nil || *stored.DuplicateOf != primary.ID {
		t.Errorf("stored duplicate_of = %v, want %s", stored.DuplicateOf, primary.ID)
	}

	dups, err := st.ListDuplicates(ctx, primary.ID)
	if err != nil {
		t.Fatalf("ListDuplicates: %v", err)
	}
	if len(dups) != 2 || dups[0].ID != dup.ID || dups[1].ID != third.ID {
		t.Errorf("duplicates = %v, want [%s %s]", itemIDs(dups), dup.ID, third.ID)
	}

	// Different URLs never link.
	other := mustUpsert(t, st, testNow, newItem(ax.ID, "2410.99999", "https://arxiv.org/abs/2410.99999"))[0]
	if other.DuplicateOf != nil {
		t.Error("unrelated URL linked as duplicate")
	}
}

func TestDuplicateStatsInBatch(t *testing.T) {
	st := newTestStore(t)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	ax := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)
	mustUpsert(t, st, testNow, newItem(hn.ID, "1", "https://x.test/a"), newItem(hn.ID, "2", "https://x.test/b"))

	res := mustUpsert(t, st, testNow,
		newItem(ax.ID, "a", "https://x.test/a"), // duplicate
		newItem(ax.ID, "c", "https://x.test/c"), // new
	)
	var stats domain.FetchStats
	for _, r := range res {
		stats.Record(r)
	}
	if stats != (domain.FetchStats{Inserted: 2, Duplicate: 1}) {
		t.Errorf("stats = %+v, want 2 inserted, 1 duplicate", stats)
	}
}

func TestUpsertBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	// Invalid input is rejected before any SQL runs.
	bad := newItem(src.ID, "2", "https://x.test/2")
	bad.Title = ""
	_, err := st.UpsertItems(ctx, []domain.NewItem{newItem(src.ID, "1", "https://x.test/1"), bad}, testNow)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}

	// A database failure mid-batch rolls back earlier items.
	_, err = st.UpsertItems(ctx, []domain.NewItem{
		newItem(src.ID, "1", "https://x.test/1"),
		newItem(uuid.New(), "2", "https://x.test/2"), // unknown source
	}, testNow)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound for unknown source", err)
	}

	page, err := st.ListItems(ctx, domain.ItemFilter{IncludeDuplicates: true})
	if err != nil || len(page.Items) != 0 {
		t.Errorf("items after failed batches = %d (%v), want 0", len(page.Items), err)
	}
}

func TestGetItemNotFound(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.GetItem(context.Background(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestListItemsFilters(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	ax := mustCreateSource(t, st, "arxiv", domain.SourceTypeArxiv)

	at := func(h int) *time.Time { return ptr(testNow.Add(time.Duration(-h) * time.Hour)) }
	mk := func(src uuid.UUID, ext string, kind domain.ItemKind, tags []string, pub *time.Time, url string) domain.NewItem {
		n := newItem(src, ext, url)
		n.Kind, n.Tags, n.PublishedAt = kind, tags, pub
		return n
	}
	mustUpsert(t, st, testNow,
		mk(hn.ID, "h1", domain.ItemKindDiscussion, nil, at(1), "https://x.test/1"),
		mk(hn.ID, "h2", domain.ItemKindDiscussion, nil, at(30), "https://x.test/shared"),
	)
	mustUpsert(t, st, testNow,
		mk(ax.ID, "a1", domain.ItemKindPaper, []string{"cs.LG"}, at(2), "https://x.test/3"),
		mk(ax.ID, "a2", domain.ItemKindPaper, []string{"cs.CL", "cs.LG"}, at(50), "https://x.test/4"),
		mk(ax.ID, "a3", domain.ItemKindPaper, []string{"cs.CL"}, at(3), "https://x.test/shared"), // duplicate of h2
	)

	tests := []struct {
		name   string
		filter domain.ItemFilter
		want   []string
	}{
		{"default hides duplicates, newest first", domain.ItemFilter{}, []string{"h1", "a1", "h2", "a2"}},
		{"include duplicates", domain.ItemFilter{IncludeDuplicates: true}, []string{"h1", "a1", "a3", "h2", "a2"}},
		{"by source", domain.ItemFilter{SourceIDs: []uuid.UUID{ax.ID}}, []string{"a1", "a2"}},
		{"by several sources", domain.ItemFilter{SourceIDs: []uuid.UUID{ax.ID, hn.ID}}, []string{"h1", "a1", "h2", "a2"}},
		{"by kind", domain.ItemFilter{Kinds: []domain.ItemKind{domain.ItemKindDiscussion}}, []string{"h1", "h2"}},
		{"by tag", domain.ItemFilter{Tags: []string{"cs.LG"}}, []string{"a1", "a2"}},
		{"by tag incl duplicates", domain.ItemFilter{Tags: []string{"cs.CL"}, IncludeDuplicates: true}, []string{"a3", "a2"}},
		{"since", domain.ItemFilter{Since: at(24)}, []string{"h1", "a1"}},
		{"until", domain.ItemFilter{Until: at(24)}, []string{"h2", "a2"}},
		{"since and until", domain.ItemFilter{Since: at(40), Until: at(2)}, []string{"h2"}},
		{"combined", domain.ItemFilter{SourceIDs: []uuid.UUID{ax.ID}, Tags: []string{"cs.LG"}, Since: at(24)}, []string{"a1"}},
		{"no match", domain.ItemFilter{Kinds: []domain.ItemKind{domain.ItemKindRelease}}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := st.ListItems(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListItems: %v", err)
			}
			if got := externalIDs(page.Items); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if page.Next != nil {
				t.Error("Next set on a single page")
			}
		})
	}
}

func TestListItemsKeysetPagination(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	// 7 items, several sharing a timestamp, so pagination must tie-break on ID.
	var batch []domain.NewItem
	for i := range 7 {
		n := newItem(src.ID, fmt.Sprint(i), fmt.Sprintf("https://x.test/%d", i))
		n.PublishedAt = ptr(testNow.Add(-time.Duration(i/3) * time.Hour))
		batch = append(batch, n)
	}
	mustUpsert(t, st, testNow, batch...)

	all, err := st.ListItems(ctx, domain.ItemFilter{})
	if err != nil || len(all.Items) != 7 {
		t.Fatalf("full listing = %d items (%v), want 7", len(all.Items), err)
	}

	var (
		paged []domain.FeedItem
		after *domain.ItemCursor
		pages int
	)
	for {
		page, err := st.ListItems(ctx, domain.ItemFilter{Limit: 3, After: after})
		if err != nil {
			t.Fatalf("ListItems page %d: %v", pages, err)
		}
		pages++
		paged = append(paged, page.Items...)
		if page.Next == nil {
			break
		}
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		after = page.Next
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3 (3+3+1)", pages)
	}
	if !slices.Equal(feedIDs(paged), feedIDs(all.Items)) {
		t.Errorf("paged order %v != full order %v", feedIDs(paged), feedIDs(all.Items))
	}
}

func TestListItemsLimits(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	var batch []domain.NewItem
	for i := range domain.MaxItemLimit + 5 {
		batch = append(batch, newItem(src.ID, fmt.Sprint(i), fmt.Sprintf("https://x.test/%d", i)))
	}
	mustUpsert(t, st, testNow, batch...)

	def, err := st.ListItems(ctx, domain.ItemFilter{})
	if err != nil || len(def.Items) != domain.DefaultItemLimit || def.Next == nil {
		t.Errorf("default limit: %d items, next=%v, err=%v", len(def.Items), def.Next, err)
	}
	capped, err := st.ListItems(ctx, domain.ItemFilter{Limit: 10_000})
	if err != nil || len(capped.Items) != domain.MaxItemLimit {
		t.Errorf("capped limit: %d items, err=%v, want %d", len(capped.Items), err, domain.MaxItemLimit)
	}
}

func itemIDs(items []domain.Item) []uuid.UUID {
	out := make([]uuid.UUID, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func externalIDs(items []domain.FeedItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ExternalID
	}
	return out
}

func feedIDs(items []domain.FeedItem) []uuid.UUID {
	out := make([]uuid.UUID, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
