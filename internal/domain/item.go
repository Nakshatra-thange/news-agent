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

// ItemFilter selects items for listing. Zero values mean "no filter".
type ItemFilter struct {
	SourceIDs         []uuid.UUID
	Kind              ItemKind
	Tag               string
	Since             *time.Time // FeedAt >= Since
	Until             *time.Time // FeedAt < Until
	IncludeDuplicates bool
	Limit             int
	After             *ItemCursor
}

// ItemCursor is a keyset pagination position: items strictly after it in
// (FeedAt DESC, ID DESC) order are returned.
type ItemCursor struct {
	FeedAt time.Time
	ID     uuid.UUID
}

// CursorAfter returns the cursor positioned after it.
func (it Item) CursorAfter() ItemCursor {
	return ItemCursor{FeedAt: it.FeedAt, ID: it.ID}
}

// Item listing limits.
const (
	DefaultItemLimit = 50
	MaxItemLimit     = 200
)
