package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func TestPreferencesRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	hn := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	empty, err := st.GetPreferences(ctx)
	if err != nil || len(empty.Topics) != 0 || len(empty.SourceIDs) != 0 {
		t.Fatalf("initial preferences = %+v, %v; want none", empty, err)
	}
	set, err := st.SetPreferences(ctx, domain.Preferences{Topics: []string{" Agents ", "agents", "cs.LG"}, SourceIDs: []uuid.UUID{hn.ID, hn.ID}})
	if err != nil {
		t.Fatalf("SetPreferences: %v", err)
	}
	got, err := st.GetPreferences(ctx)
	if err != nil || !slices.Equal(got.Topics, []string{"agents", "cs.lg"}) || !slices.Equal(got.SourceIDs, []uuid.UUID{hn.ID}) ||
		got.Fingerprint() != set.Fingerprint() {
		t.Errorf("stored preferences = %+v (%v), returned %+v", got, err, set)
	}

	// Replacing is all or nothing: an unknown source changes nothing.
	if _, err := st.SetPreferences(ctx, domain.Preferences{Topics: []string{"x"}, SourceIDs: []uuid.UUID{uuid.New()}}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown source: %v, want not found", err)
	}
	if _, err := st.SetPreferences(ctx, domain.Preferences{Topics: make([]string, 0, 1)}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := st.GetPreferences(ctx); len(got.Topics)+len(got.SourceIDs) != 0 {
		t.Errorf("after clearing = %+v", got)
	}
	if _, err := st.SetPreferences(ctx, domain.Preferences{SourceIDs: make([]uuid.UUID, domain.MaxPreferredSources+1)}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("too many sources: %v, want invalid", err)
	}
	// The database keeps topics normalized too.
	_, err = st.pool.Exec(ctx, `INSERT INTO preferred_topics (topic) VALUES ('Agents')`)
	if !errors.Is(mapErr(err), domain.ErrInvalid) {
		t.Errorf("raw uppercase topic: %v, want invalid", err)
	}
}

func TestListItemsPersonalized(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	hn, ax, _ := rankFixture(t, st)

	// No preferences: the same order as plain ranking.
	plain, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked})
	if err != nil {
		t.Fatal(err)
	}
	none, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Personalize: &domain.Preferences{}})
	if err != nil || !slices.Equal(feedIDs(none.Items), feedIDs(plain.Items)) {
		t.Errorf("empty preferences changed the order (%v)", err)
	}

	// Topic "CS.LG" matches the two arXiv papers' tags (case-insensitively);
	// "ai" matches usable enrichment topics only (not the fake or stale
	// enrichment); arxiv is a preferred source.
	prefs := domain.Preferences{Topics: []string{"CS.LG", "ai"}, SourceIDs: []uuid.UUID{ax.ID}}
	prefs.Normalize()
	page, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Personalize: &prefs})
	if err != nil {
		t.Fatal(err)
	}
	matches := map[string]int{"old-important": 2, "stale": 2, "mid-minor": 1, "neutral": 1, "tie-a": 1, "tie-b": 1, "tie-d": 1}
	imp := map[string]int{"old-important": 5, "mid-minor": 1, "neutral": 3, "tie-a": 3, "tie-d": 4, "tie-b": 4}
	for _, it := range page.Items {
		var p *int
		if v, ok := imp[it.ExternalID]; ok {
			p = &v
		}
		if want := domain.RankAt(it.FeedAt, p, matches[it.ExternalID]); !it.RankAt.Equal(want) {
			t.Errorf("%s: rank_at %v, want %v", it.ExternalID, it.RankAt, want)
		}
	}
	// old-important: -30h + 24h + 24h = +18h; stale: -4h + 24h = +20h.
	if got := externalIDs(page.Items)[:3]; !slices.Equal(got, []string{"stale", "old-important", "neutral"}) {
		t.Errorf("personalized order starts %v", got)
	}

	// Pages are complete and stable.
	for limit := 1; limit <= 3; limit++ {
		var paged []domain.FeedItem
		var after *domain.ItemCursor
		for n := 0; ; n++ {
			if n > 12 {
				t.Fatal("pagination did not terminate")
			}
			p, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Personalize: &prefs, Limit: limit, After: after})
			if err != nil {
				t.Fatal(err)
			}
			paged = append(paged, p.Items...)
			if p.Next == nil {
				break
			}
			after = p.Next
		}
		if !slices.Equal(feedIDs(paged), feedIDs(page.Items)) {
			t.Errorf("limit %d: paged %v != full %v", limit, externalIDs(paged), externalIDs(page.Items))
		}
	}

	// Filters still apply; personalization needs the ranked sort.
	only, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Personalize: &prefs, SourceIDs: []uuid.UUID{hn.ID}})
	if err != nil || len(only.Items) != 8 {
		t.Errorf("hn only: %d items (%v), want 8", len(only.Items), err)
	}
	if _, err := st.ListItems(ctx, domain.ItemFilter{Personalize: &prefs}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("personalized newest: %v, want invalid", err)
	}
}
