package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
		if got := RankAt(feed, tt.importance, 0); !got.Equal(tt.want) {
			t.Errorf("%s: RankAt = %v, want %v", tt.name, got, tt.want)
		}
	}
	// A newer item wins at equal importance; importance can outweigh up to
	// 48 hours of age.
	older := feed.Add(-47 * time.Hour)
	if !RankAt(older, imp(5), 0).After(RankAt(feed, imp(1), 0)) {
		t.Error("a major item 47h older should outrank a minor new one")
	}
	if !RankAt(feed, imp(3), 0).After(RankAt(feed.Add(-time.Hour), imp(3), 0)) {
		t.Error("at equal importance the newer item should rank higher")
	}
	for s, want := range map[ItemSort]bool{SortNewest: true, SortRanked: true, "": false, "top": false} {
		if s.Valid() != want {
			t.Errorf("ItemSort(%q).Valid() = %v", s, !want)
		}
	}
}

func TestRankAtPreferences(t *testing.T) {
	feed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	five := 5
	if got := RankAt(feed, nil, 1); !got.Equal(feed.Add(12 * time.Hour)) {
		t.Errorf("one match = %v", got)
	}
	if got := RankAt(feed, &five, 2); !got.Equal(feed.Add(48 * time.Hour)) {
		t.Errorf("importance 5 and both matches = %v, want +48h", got)
	}
}

func TestPreferencesNormalize(t *testing.T) {
	a, b := uuid.MustParse("0199a000-0000-7000-8000-000000000002"), uuid.MustParse("0199a000-0000-7000-8000-000000000001")
	p := Preferences{Topics: []string{" Agents", "cs.LG", "agents", "", "  "}, SourceIDs: []uuid.UUID{a, b, a}}
	p.Normalize()
	if !slices.Equal(p.Topics, []string{"agents", "cs.lg"}) || !slices.Equal(p.SourceIDs, []uuid.UUID{b, a}) {
		t.Errorf("normalized = %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	same := Preferences{Topics: []string{"CS.LG", "agents"}, SourceIDs: []uuid.UUID{b, a}}
	same.Normalize()
	if p.Fingerprint() != same.Fingerprint() || p.Fingerprint() == (Preferences{}).Fingerprint() {
		t.Error("fingerprint does not identify the preference set")
	}
	for name, bad := range map[string]Preferences{
		"too many topics":  {Topics: make([]string, MaxPreferredTopics+1)},
		"long topic":       {Topics: []string{strings.Repeat("x", MaxPreferredTopic+1)}},
		"too many sources": {SourceIDs: make([]uuid.UUID, MaxPreferredSources+1)},
		"nil source":       {SourceIDs: []uuid.UUID{uuid.Nil}},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
}
