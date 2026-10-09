package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FeedbackValue is the user's explicit reaction to an item.
type FeedbackValue string

const (
	FeedbackMore FeedbackValue = "more" // "More like this"
	FeedbackLess FeedbackValue = "less" // "Less like this"
)

// Valid reports whether v is a known value.
func (v FeedbackValue) Valid() bool { return v == FeedbackMore || v == FeedbackLess }

// Feedback is the user's current feedback on one item. There is at most
// one per item; giving feedback again replaces it.
type Feedback struct {
	ItemID    uuid.UUID
	Value     FeedbackValue
	CreatedAt time.Time
	UpdatedAt time.Time
}

// FeedbackEntry is one item's feedback with the item's topics: its tags
// and usable enrichment topics, lowercase.
type FeedbackEntry struct {
	ItemID uuid.UUID
	Value  FeedbackValue
	Topics []string
}

// MaxFeedbackSignals bounds how many of the most recent feedback entries
// shape the ranking, which keeps the ranking query small.
const MaxFeedbackSignals = 500

// FeedbackSignals is what the personalized ranked feed uses from feedback:
// the items marked "more" or "less", and the topics of each group. A topic
// found in both groups is conflicting and dropped from both, so it neither
// raises nor lowers anything. All lists are sorted and free of repeats.
type FeedbackSignals struct {
	More, Less             []uuid.UUID
	MoreTopics, LessTopics []string
}

// NewFeedbackSignals derives signals from feedback entries.
func NewFeedbackSignals(entries []FeedbackEntry) FeedbackSignals {
	var s FeedbackSignals
	more, less := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		topics := more
		if e.Value == FeedbackMore {
			s.More = append(s.More, e.ItemID)
		} else {
			s.Less = append(s.Less, e.ItemID)
			topics = less
		}
		for _, t := range e.Topics {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				topics[t] = true
			}
		}
	}
	for t := range more {
		if !less[t] {
			s.MoreTopics = append(s.MoreTopics, t)
		}
	}
	for t := range less {
		if !more[t] {
			s.LessTopics = append(s.LessTopics, t)
		}
	}
	byString := func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }
	slices.SortFunc(s.More, byString)
	slices.SortFunc(s.Less, byString)
	slices.Sort(s.MoreTopics)
	slices.Sort(s.LessTopics)
	return s
}

// Fingerprint identifies the signals. A personalized cursor carries it, so
// a page issued before the feedback changed is rejected instead of
// skipping or repeating items.
func (s FeedbackSignals) Fingerprint() string {
	var b strings.Builder
	for _, id := range s.More {
		b.WriteString("+" + id.String() + "\x00")
	}
	for _, id := range s.Less {
		b.WriteString("-" + id.String() + "\x00")
	}
	b.WriteString(strings.Join(s.MoreTopics, "\x00") + "\x01" + strings.Join(s.LessTopics, "\x00"))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// Feedback ranking bounds. An item's feedback adjustment, in steps of
// RankFeedbackStep, is
//
//	clamp(2 × direct + related, -MaxFeedbackSteps, MaxFeedbackSteps)
//
// where direct is +1 or -1 for the item's own "more" or "less" (0 without)
// and related is +1 if it shares a "more" topic, -1 if it shares a "less"
// topic (both or neither: 0). Each part counts once however many items or
// topics match, so feedback cannot pile up. The adjustment is at most ±24
// hours: it never hides an item and never outweighs more than a day of
// recency.
const (
	RankFeedbackStep = 12 * time.Hour
	MaxFeedbackSteps = 2
)

// FeedbackSteps computes the clamped adjustment described above.
func FeedbackSteps(direct int, sharesMore, sharesLess bool) int {
	steps := 2 * direct
	if sharesMore {
		steps++
	}
	if sharesLess {
		steps--
	}
	return max(-MaxFeedbackSteps, min(MaxFeedbackSteps, steps))
}
