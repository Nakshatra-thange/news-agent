package domain

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFeedbackSteps(t *testing.T) {
	tests := []struct {
		direct     int
		more, less bool
		want       int
	}{
		{0, false, false, 0},   // no feedback is not negative
		{1, false, false, 2},   // "more" on the item itself
		{-1, false, false, -2}, // "less" on the item itself
		{0, true, false, 1},    // shares a "more" topic
		{0, false, true, -1},   // shares a "less" topic
		{0, true, true, 0},     // conflicting related signals cancel
		{1, true, false, 2},    // capped
		{1, false, true, 1},    // own feedback outweighs related
		{-1, true, false, -1},
		{-1, false, true, -2}, // capped
	}
	for _, tt := range tests {
		if got := FeedbackSteps(tt.direct, tt.more, tt.less); got != tt.want {
			t.Errorf("FeedbackSteps(%d, %v, %v) = %d, want %d", tt.direct, tt.more, tt.less, got, tt.want)
		}
	}
	feed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if got := RankAt(feed, nil, 0, -MaxFeedbackSteps); !got.Equal(feed.Add(-24 * time.Hour)) {
		t.Errorf("lowest feedback = %v, want -24h", got)
	}
}

func TestNewFeedbackSignals(t *testing.T) {
	a, b, c := uuid.MustParse("0199a000-0000-7000-8000-000000000003"), uuid.MustParse("0199a000-0000-7000-8000-000000000001"),
		uuid.MustParse("0199a000-0000-7000-8000-000000000002")
	s := NewFeedbackSignals([]FeedbackEntry{
		{ItemID: a, Value: FeedbackMore, Topics: []string{"Agents", "cs.ai"}},
		{ItemID: b, Value: FeedbackMore, Topics: []string{"agents", "llm"}},
		{ItemID: c, Value: FeedbackLess, Topics: []string{"cs.AI", "crypto", " "}},
	})
	if !slices.Equal(s.More, []uuid.UUID{b, a}) || !slices.Equal(s.Less, []uuid.UUID{c}) {
		t.Errorf("items = %v / %v", s.More, s.Less)
	}
	// cs.ai is in both groups: conflicting, dropped from both.
	if !slices.Equal(s.MoreTopics, []string{"agents", "llm"}) || !slices.Equal(s.LessTopics, []string{"crypto"}) {
		t.Errorf("topics = %v / %v", s.MoreTopics, s.LessTopics)
	}
	again := NewFeedbackSignals([]FeedbackEntry{
		{ItemID: c, Value: FeedbackLess, Topics: []string{"crypto", "cs.ai"}},
		{ItemID: b, Value: FeedbackMore, Topics: []string{"llm", "agents"}},
		{ItemID: a, Value: FeedbackMore, Topics: []string{"cs.AI", "agents"}},
	})
	if again.Fingerprint() != s.Fingerprint() || s.Fingerprint() == NewFeedbackSignals(nil).Fingerprint() {
		t.Error("fingerprint does not identify the signals independent of order")
	}
	if !FeedbackMore.Valid() || !FeedbackLess.Valid() || FeedbackValue("meh").Valid() {
		t.Error("Valid")
	}
}
