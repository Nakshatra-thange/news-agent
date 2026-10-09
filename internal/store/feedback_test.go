package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func TestFeedbackStorage(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	mustUpsert(t, st, testNow, newItem(src.ID, "1", "https://x.test/1"))
	item := itemsToEmbed(t, st, "m", 3, 1)[0]

	first, err := st.SetFeedback(ctx, item.ID, domain.FeedbackMore)
	if err != nil || first.Value != domain.FeedbackMore {
		t.Fatalf("SetFeedback = %+v, %v", first, err)
	}
	// Repeating it changes nothing; changing it updates the one row.
	same, err := st.SetFeedback(ctx, item.ID, domain.FeedbackMore)
	if err != nil || !same.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("repeat = %+v, %v; want the row untouched", same, err)
	}
	changed, err := st.SetFeedback(ctx, item.ID, domain.FeedbackLess)
	if err != nil || changed.Value != domain.FeedbackLess || !changed.CreatedAt.Equal(first.CreatedAt) || !changed.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("change = %+v, %v", changed, err)
	}
	list, err := st.ListFeedback(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Value != domain.FeedbackLess {
		t.Errorf("ListFeedback = %+v, %v; want one entry", list, err)
	}
	page, _ := st.ListItems(ctx, domain.ItemFilter{})
	if fb := page.Items[0].Feedback; fb == nil || *fb != domain.FeedbackLess {
		t.Errorf("feed item feedback = %v", fb)
	}

	if err := st.DeleteFeedback(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFeedback(ctx, item.ID); err != nil {
		t.Errorf("deleting absent feedback: %v", err)
	}
	if page, _ := st.ListItems(ctx, domain.ItemFilter{}); page.Items[0].Feedback != nil {
		t.Error("feedback still shown after removal")
	}
	if _, err := st.SetFeedback(ctx, uuid.New(), domain.FeedbackMore); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown item: %v, want not found", err)
	}
	if _, err := st.SetFeedback(ctx, item.ID, "meh"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("bad value: %v, want invalid", err)
	}
	_, err = st.pool.Exec(ctx, `INSERT INTO item_feedback (item_id, value) VALUES ($1, 'meh')`, item.ID)
	if !errors.Is(mapErr(err), domain.ErrInvalid) {
		t.Errorf("raw bad value: %v, want invalid", err)
	}
}

func TestListItemsWithFeedback(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	_, ax, byExt := rankFixture(t, st)

	signals := func() *domain.FeedbackSignals {
		s, err := st.FeedbackSignals(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return &s
	}
	plain, _ := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked})
	// No feedback: no change, and no feedback is not negative.
	if none, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Feedback: signals()}); err != nil ||
		!slices.Equal(feedIDs(none.Items), feedIDs(plain.Items)) {
		t.Errorf("empty feedback changed the order (%v)", err)
	}

	// "more" on a paper (tag cs.LG, enrichment topic ai), "less" on an HN
	// item (enrichment topic ai, no tags). "ai" conflicts and is dropped;
	// cs.lg stays a "more" topic.
	for ext, v := range map[string]domain.FeedbackValue{"old-important": domain.FeedbackMore, "neutral": domain.FeedbackLess} {
		if _, err := st.SetFeedback(ctx, byExt[ext].ID, v); err != nil {
			t.Fatal(err)
		}
	}
	sig := signals()
	if !slices.Equal(sig.MoreTopics, []string{"cs.lg"}) || len(sig.LessTopics) != 0 {
		t.Fatalf("signals = %+v", sig)
	}
	steps := map[string]int{"old-important": 2, "stale": 1, "neutral": -2} // clamp(2+1), shares cs.lg, own "less"
	imp := map[string]int{"old-important": 5, "mid-minor": 1, "neutral": 3, "tie-a": 3, "tie-d": 4, "tie-b": 4}
	check := func(prefs *domain.Preferences, matches map[string]int) []domain.FeedItem {
		t.Helper()
		page, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Personalize: prefs, Feedback: sig})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			var p *int
			if v, ok := imp[it.ExternalID]; ok {
				p = &v
			}
			if want := domain.RankAt(it.FeedAt, p, matches[it.ExternalID], steps[it.ExternalID]); !it.RankAt.Equal(want) {
				t.Errorf("%s: rank_at %v, want %v", it.ExternalID, it.RankAt, want)
			}
		}
		return page.Items
	}
	items := check(nil, nil)
	// old-important -6h + 24h = +18h first; neutral -5h - 24h = -29h last.
	if got := externalIDs(items); got[0] != "old-important" || got[len(got)-1] != "neutral" {
		t.Errorf("order with feedback = %v", got)
	}
	// Every item is still listed: feedback never hides anything.
	if len(items) != len(plain.Items) {
		t.Errorf("%d items with feedback, %d without", len(items), len(plain.Items))
	}

	// Combined with preferences, both adjustments add up.
	prefs := domain.Preferences{SourceIDs: []uuid.UUID{ax.ID}}
	check(&prefs, map[string]int{"old-important": 1, "stale": 1})

	// Pages are complete with feedback applied.
	for limit := 1; limit <= 3; limit++ {
		var paged []domain.FeedItem
		var after *domain.ItemCursor
		for n := 0; ; n++ {
			if n > 12 {
				t.Fatal("pagination did not terminate")
			}
			p, err := st.ListItems(ctx, domain.ItemFilter{Sort: domain.SortRanked, Feedback: sig, Limit: limit, After: after})
			if err != nil {
				t.Fatal(err)
			}
			paged = append(paged, p.Items...)
			if p.Next == nil {
				break
			}
			after = p.Next
		}
		if !slices.Equal(feedIDs(paged), feedIDs(items)) {
			t.Errorf("limit %d: paged %v != full %v", limit, externalIDs(paged), externalIDs(items))
		}
	}

	// Newest first ignores feedback, and rejects feedback signals.
	newest, _ := st.ListItems(ctx, domain.ItemFilter{})
	if got := externalIDs(newest.Items)[:3]; !slices.Equal(got, []string{"new", "mid-minor", "fake"}) {
		t.Errorf("newest = %v", got)
	}
	if _, err := st.ListItems(ctx, domain.ItemFilter{Feedback: sig}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("feedback with newest: %v, want invalid", err)
	}
}
