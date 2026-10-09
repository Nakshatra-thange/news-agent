package domain

import (
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ItemKind is the structural type of an item. Topic categories ("LLMs",
// "robotics") are a later-phase classification and are not stored here.
type ItemKind string

const (
	ItemKindPaper      ItemKind = "paper"
	ItemKindRepository ItemKind = "repository"
	ItemKindDiscussion ItemKind = "discussion"
	ItemKindRelease    ItemKind = "release"
)

// ItemKinds lists the kinds known in Phase 1.
var ItemKinds = []ItemKind{ItemKindPaper, ItemKindRepository, ItemKindDiscussion, ItemKindRelease}

// Valid reports whether k is a known kind.
func (k ItemKind) Valid() bool {
	for _, known := range ItemKinds {
		if k == known {
			return true
		}
	}
	return false
}

// HashSize is the length of URLHash and ContentHash (SHA-256).
const HashSize = sha256.Size

// Item is one normalized sighting of a piece of content on one source.
type Item struct {
	ID            uuid.UUID
	SourceID      uuid.UUID
	ExternalID    string
	Kind          ItemKind
	Title         string
	Description   string
	URL           string
	CanonicalURL  string
	URLHash       []byte
	DiscussionURL string
	Authors       []string
	Tags          []string
	ContentHash   []byte
	Metadata      json.RawMessage
	// DuplicateOf links to an item from another source with the same
	// canonical URL that was stored first.
	DuplicateOf  *uuid.UUID
	PublishedAt  *time.Time
	DiscoveredAt time.Time
	LastSeenAt   time.Time
	UpdatedAt    time.Time
	// FeedAt is PublishedAt, or DiscoveredAt when the source gives no date.
	FeedAt time.Time
}

// Candidate is an item as a source adapter observed it, before the ingestion
// pipeline normalizes it. The pipeline derives a NewItem from it by cleaning
// text, canonicalizing the URL, computing hashes and attaching the source.
type Candidate struct {
	// ExternalID must be stable for the same content across fetches
	// (HN objectID, arXiv id without version, GitHub repository id).
	ExternalID    string
	Kind          ItemKind
	Title         string
	Description   string
	URL           string // the content's link; canonicalized for dedup
	DiscussionURL string // optional, e.g. the HN comments page
	Authors       []string
	Tags          []string
	// Metadata holds source-specific data as a JSON object.
	Metadata    json.RawMessage
	PublishedAt *time.Time
}

// NewItem is a fully normalized item ready to be upserted. Canonicalization
// and hashing happen before this point (in the ingestion pipeline).
type NewItem struct {
	SourceID      uuid.UUID
	ExternalID    string
	Kind          ItemKind
	Title         string
	Description   string
	URL           string
	CanonicalURL  string
	URLHash       []byte
	DiscussionURL string
	Authors       []string
	Tags          []string
	ContentHash   []byte
	Metadata      json.RawMessage
	PublishedAt   *time.Time
}

var kindFormat = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Validate checks the invariants the database also enforces, so bad data is
// rejected with a clear message before reaching SQL.
func (n NewItem) Validate() error {
	var v validator
	v.check(n.SourceID != uuid.Nil, "source_id", "is required")
	v.check(strings.TrimSpace(n.ExternalID) != "", "external_id", "is required")
	v.check(kindFormat.MatchString(string(n.Kind)), "kind", "must be a lowercase identifier")
	v.check(strings.TrimSpace(n.Title) != "", "title", "is required")
	v.check(strings.TrimSpace(n.URL) != "", "url", "is required")
	v.check(strings.TrimSpace(n.CanonicalURL) != "", "canonical_url", "is required")
	v.check(len(n.URLHash) == HashSize, "url_hash", "must be a SHA-256 digest")
	v.check(len(n.ContentHash) == HashSize, "content_hash", "must be a SHA-256 digest")
	v.check(len(n.Metadata) == 0 || isJSONObject(n.Metadata), "metadata", "must be a JSON object")
	return v.err()
}

// UpsertOutcome describes what an item upsert did.
type UpsertOutcome string

const (
	UpsertInserted  UpsertOutcome = "inserted"  // new for this source
	UpsertUpdated   UpsertOutcome = "updated"   // already known; stored fields changed
	UpsertUnchanged UpsertOutcome = "unchanged" // already known; only last_seen_at moved
)

// UpsertResult is the result of upserting one item.
type UpsertResult struct {
	ID          uuid.UUID
	Outcome     UpsertOutcome
	DuplicateOf *uuid.UUID // set when a newly inserted item was linked to another source's item
}

// ItemFilter selects items for listing. Zero values mean "no filter"; values
// within one slice are alternatives (OR), except Tags, which must all match.
type ItemFilter struct {
	SourceIDs      []uuid.UUID
	SourceTypes    []SourceType
	SourceStatuses []SourceStatus
	Kinds          []ItemKind
	Tags           []string
	// Since and Until bound FeedAt (published, else discovered): Since <= FeedAt < Until.
	Since *time.Time
	Until *time.Time
	// DiscoveredSince and DiscoveredUntil bound DiscoveredAt the same way.
	DiscoveredSince *time.Time
	DiscoveredUntil *time.Time
	// Query is a full-text search over title and description (web search
	// syntax: words, "quoted phrases", -excluded, or).
	Query             string
	IncludeDuplicates bool
	// Sort is the order; the zero value is SortNewest.
	Sort ItemSort
	// Personalize, with SortRanked only, boosts items matching these
	// preferences (see RankAt). Nil means no personalization.
	Personalize *Preferences
	Limit       int
	After       *ItemCursor
}

// ItemSort is a feed order.
type ItemSort string

const (
	// SortNewest orders by FeedAt, then ID, newest first.
	SortNewest ItemSort = "newest"
	// SortRanked orders by RankAt (see RankAt), then DiscoveredAt, then
	// ID, highest first.
	SortRanked ItemSort = "ranked"
)

// Valid reports whether s is a known sort.
func (s ItemSort) Valid() bool { return s == SortNewest || s == SortRanked }

// Ranking weights. The ranked feed orders items by a point in time:
//
//	rank_at = feed_at + (importance - RankNeutralImportance) * RankImportanceStep
//
// so each importance point above or below neutral moves an item by
// RankImportanceStep. The personalized ranked feed adds
// RankPreferenceBoost for each kind of preference the item matches: a
// preferred topic among its tags or enrichment topics, and a preferred
// source (at most two boosts). Importance comes from the item's enrichment, used
// only when it was made by a real model (not the fake provider) from the
// item's current content; without one the item is ranked by recency alone.
// The score does not depend on the current time, so pages stay stable.
const (
	RankNeutralImportance = 3
	RankImportanceStep    = 12 * time.Hour
	RankPreferenceBoost   = 12 * time.Hour
)

// RankAt is the ranked-feed position of an item with the given feed time,
// enrichment importance (nil when there is no usable enrichment) and number
// of matched preference kinds (0 to 2; always 0 without personalization).
// The store computes the same value in SQL.
func RankAt(feedAt time.Time, importance *int, preferenceMatches int) time.Time {
	t := feedAt.Add(time.Duration(preferenceMatches) * RankPreferenceBoost)
	if importance == nil {
		return t
	}
	return t.Add(time.Duration(*importance-RankNeutralImportance) * RankImportanceStep)
}

// SourceRef identifies an item's source for display.
type SourceRef struct {
	ID   uuid.UUID
	Slug string
	Name string
	Type SourceType
}

// Sighting is the same content seen on another source: an item stored as a
// cross-source duplicate of a primary item.
type Sighting struct {
	ItemID        uuid.UUID
	Source        SourceRef
	URL           string
	DiscussionURL string
	DiscoveredAt  time.Time
}

// FeedItem is an item with its source and its other sightings.
type FeedItem struct {
	Item
	Source SourceRef
	// AlsoSeenOn lists duplicates of this item from other sources, oldest
	// first. Empty for items that are themselves duplicates.
	AlsoSeenOn []Sighting
	// RankAt is the item's position in the ranked feed (see RankAt). It
	// is internal: the API does not expose it.
	RankAt time.Time
}

// ItemPage is one page of a keyset-paginated item listing.
type ItemPage struct {
	Items []FeedItem
	// Next is the cursor for the following page, or nil on the last page.
	Next *ItemCursor
}

// ItemCursor is a keyset pagination position: items strictly after it in
// the sort's order are returned. SortNewest uses (FeedAt, ID); SortRanked
// uses (RankAt, DiscoveredAt, ID).
type ItemCursor struct {
	Sort         ItemSort
	FeedAt       time.Time
	RankAt       time.Time
	DiscoveredAt time.Time
	ID           uuid.UUID
}

// CursorAfter returns the cursor positioned after it in the given order.
func (f FeedItem) CursorAfter(sort ItemSort) ItemCursor {
	if sort == SortRanked {
		return ItemCursor{Sort: SortRanked, RankAt: f.RankAt, DiscoveredAt: f.DiscoveredAt, ID: f.ID}
	}
	return ItemCursor{Sort: SortNewest, FeedAt: f.FeedAt, ID: f.ID}
}

// Item listing limits.
const (
	DefaultItemLimit = 50
	MaxItemLimit     = 200
)
