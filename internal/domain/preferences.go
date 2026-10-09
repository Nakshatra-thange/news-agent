package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Preferences are the user's explicit interests: topics and sources. In the
// personalized ranked feed, an item matching a topic (one of its tags or
// enrichment topics) or from a preferred source ranks higher (see RankAt).
// There is a single, local user for now.
type Preferences struct {
	// Topics are lowercase and matched case-insensitively.
	Topics    []string
	SourceIDs []uuid.UUID
}

// Preference limits. They keep the ranking query small.
const (
	MaxPreferredTopics  = 50
	MaxPreferredSources = 20
	MaxPreferredTopic   = 100
)

// Normalize trims and lowercases topics, drops empty and repeated entries,
// and sorts both lists, so equal preferences have one representation.
func (p *Preferences) Normalize() {
	var topics []string
	for _, t := range p.Topics {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			topics = append(topics, t)
		}
	}
	slices.Sort(topics)
	p.Topics = slices.Compact(topics)
	ids := slices.Clone(p.SourceIDs)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	p.SourceIDs = slices.Compact(ids)
}

// Validate checks the limits. Call Normalize first.
func (p Preferences) Validate() error {
	var v validator
	v.check(len(p.Topics) <= MaxPreferredTopics, "topics", fmt.Sprintf("at most %d topics", MaxPreferredTopics))
	for _, t := range p.Topics {
		if utf8.RuneCountInString(t) > MaxPreferredTopic {
			v.check(false, "topics", fmt.Sprintf("each topic must be at most %d characters", MaxPreferredTopic))
			break
		}
	}
	v.check(len(p.SourceIDs) <= MaxPreferredSources, "sources", fmt.Sprintf("at most %d sources", MaxPreferredSources))
	v.check(!slices.Contains(p.SourceIDs, uuid.Nil), "sources", "must be valid source IDs")
	return v.err()
}

// Fingerprint identifies a normalized preference set. A personalized cursor
// carries it, so a page issued before the preferences changed is rejected
// instead of skipping or repeating items.
func (p Preferences) Fingerprint() string {
	parts := slices.Clone(p.Topics)
	for _, id := range p.SourceIDs {
		parts = append(parts, "source:"+id.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}
