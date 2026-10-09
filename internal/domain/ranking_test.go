package domain

import (
	"testing"
	"time"
)

func TestRankAt(t *testing.T) {
	feed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	imp := func(v int) *int { return &v }
	tests := []struct {
		name       string
		importance *int
		want       time.Time
	}{
		{"no enrichment: recency only", nil, feed},
		{"neutral importance", imp(3), feed},
		{"major", imp(5), feed.Add(24 * time.Hour)},
		{"minor", imp(1), feed.Add(-24 * time.Hour)},
		{"one step", imp(4), feed.Add(12 * time.Hour)},
	}
	for _, tt := range tests {
		if got := RankAt(feed, tt.importance); !got.Equal(tt.want) {
			t.Errorf("%s: RankAt = %v, want %v", tt.name, got, tt.want)
		}
	}
	// A newer item wins at equal importance; importance can outweigh up to
	// 48 hours of age.
	older := feed.Add(-47 * time.Hour)
	if !RankAt(older, imp(5)).After(RankAt(feed, imp(1))) {
		t.Error("a major item 47h older should outrank a minor new one")
	}
	if !RankAt(feed, imp(3)).After(RankAt(feed.Add(-time.Hour), imp(3))) {
		t.Error("at equal importance the newer item should rank higher")
	}
	for s, want := range map[ItemSort]bool{SortNewest: true, SortRanked: true, "": false, "top": false} {
		if s.Valid() != want {
			t.Errorf("ItemSort(%q).Valid() = %v", s, !want)
		}
	}
}
