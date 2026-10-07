package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

const itemColumns = `id, source_id, external_id, kind, title, description, url, canonical_url,
	url_hash, discussion_url, authors, tags, content_hash, metadata, duplicate_of,
	published_at, discovered_at, last_seen_at, updated_at, feed_at`

func scanItem(row pgx.Row) (domain.Item, error) {
	var it domain.Item
	err := row.Scan(&it.ID, &it.SourceID, &it.ExternalID, &it.Kind, &it.Title, &it.Description, &it.URL, &it.CanonicalURL,
		&it.URLHash, &it.DiscussionURL, &it.Authors, &it.Tags, &it.ContentHash, &it.Metadata, &it.DuplicateOf,
		&it.PublishedAt, &it.DiscoveredAt, &it.LastSeenAt, &it.UpdatedAt, &it.FeedAt)
	if err != nil {
		return domain.Item{}, mapErr(err)
	}
	it.PublishedAt = utc(it.PublishedAt)
	it.DiscoveredAt, it.LastSeenAt, it.UpdatedAt, it.FeedAt = it.DiscoveredAt.UTC(), it.LastSeenAt.UTC(), it.UpdatedAt.UTC(), it.FeedAt.UTC()
	return it, nil
}

// upsertItemSQL implements deterministic dedup in one atomic statement:
//
//   - Same source: (source_id, external_id) is unique. A re-fetched item
//     updates the stored row only if a stored field actually changed (the
//     WHERE clause); otherwise no row is returned and the caller just bumps
//     last_seen_at.
//   - Cross source: a new item whose url_hash matches a primary (non-duplicate)
//     item from another source is linked via duplicate_of to the earliest such
//     item. Duplicates are stored, not dropped. duplicate_of is decided once,
//     at insert time.
//
// (xmax = 0) is true only for freshly inserted rows, distinguishing inserts
// from ON CONFLICT updates.
const upsertItemSQL = `
INSERT INTO items AS i (
	id, source_id, external_id, kind, title, description, url, canonical_url, url_hash,
	discussion_url, authors, tags, content_hash, metadata, duplicate_of,
	published_at, discovered_at, last_seen_at, updated_at)
VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
	(SELECT d.id FROM items d
	 WHERE d.url_hash = $9 AND d.source_id <> $2 AND d.duplicate_of IS NULL
	 ORDER BY d.discovered_at, d.id
	 LIMIT 1),
	$15, $16, $16, $16)
ON CONFLICT (source_id, external_id) DO UPDATE SET
	kind = EXCLUDED.kind,
	title = EXCLUDED.title,
	description = EXCLUDED.description,
	url = EXCLUDED.url,
	canonical_url = EXCLUDED.canonical_url,
	url_hash = EXCLUDED.url_hash,
	discussion_url = EXCLUDED.discussion_url,
	authors = EXCLUDED.authors,
	tags = EXCLUDED.tags,
	content_hash = EXCLUDED.content_hash,
	metadata = EXCLUDED.metadata,
	published_at = EXCLUDED.published_at,
	last_seen_at = GREATEST(i.last_seen_at, EXCLUDED.last_seen_at),
	updated_at = EXCLUDED.updated_at
WHERE (i.kind, i.title, i.description, i.url, i.canonical_url, i.url_hash, i.discussion_url,
       i.authors, i.tags, i.content_hash, i.metadata, i.published_at)
   IS DISTINCT FROM
      (EXCLUDED.kind, EXCLUDED.title, EXCLUDED.description, EXCLUDED.url, EXCLUDED.canonical_url,
       EXCLUDED.url_hash, EXCLUDED.discussion_url, EXCLUDED.authors, EXCLUDED.tags,
       EXCLUDED.content_hash, EXCLUDED.metadata, EXCLUDED.published_at)
RETURNING i.id, (i.xmax = 0) AS inserted, i.duplicate_of`

const touchItemSQL = `
UPDATE items SET last_seen_at = GREATEST(last_seen_at, $3)
WHERE source_id = $1 AND external_id = $2
RETURNING id`

// UpsertItems stores a batch of normalized items from one fetch in a single
// transaction. seenAt is recorded as discovered_at for new items and as
// last_seen_at for all of them. Every item is validated first; one invalid
// item rejects the whole batch (the ingestion pipeline filters bad items).
func (s *Store) UpsertItems(ctx context.Context, items []domain.NewItem, seenAt time.Time) ([]domain.UpsertResult, error) {
	for i, n := range items {
		if err := n.Validate(); err != nil {
			return nil, fmt.Errorf("item %d (external_id %q): %w", i, n.ExternalID, err)
		}
	}
	seenAt = pgTime(seenAt)

	results := make([]domain.UpsertResult, 0, len(items))
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		for _, n := range items {
			r, err := upsertItem(ctx, tx, n, seenAt)
			if err != nil {
				return fmt.Errorf("upsert item %q: %w", n.ExternalID, err)
			}
			results = append(results, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func upsertItem(ctx context.Context, tx pgx.Tx, n domain.NewItem, seenAt time.Time) (domain.UpsertResult, error) {
	var (
		r        domain.UpsertResult
		inserted bool
	)
	var published *time.Time
	if n.PublishedAt != nil {
		t := pgTime(*n.PublishedAt)
		published = &t
	}
	err := tx.QueryRow(ctx, upsertItemSQL,
		newID(), n.SourceID, n.ExternalID, string(n.Kind), n.Title, n.Description, n.URL, n.CanonicalURL, n.URLHash,
		n.DiscussionURL, nonNil(n.Authors), nonNil(n.Tags), n.ContentHash, jsonOrEmpty(n.Metadata),
		published, seenAt,
	).Scan(&r.ID, &inserted, &r.DuplicateOf)

	switch {
	case err == nil && inserted:
		r.Outcome = domain.UpsertInserted
		return r, nil
	case err == nil:
		r.Outcome = domain.UpsertUpdated
		r.DuplicateOf = nil // only reported for new links
		return r, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return r, mapErr(err)
	}

	// Conflict with no changed fields: the item is already stored as-is.
	if err := tx.QueryRow(ctx, touchItemSQL, n.SourceID, n.ExternalID, seenAt).Scan(&r.ID); err != nil {
		return r, mapErr(err)
	}
	r.Outcome = domain.UpsertUnchanged
	return r, nil
}

// GetItem returns the item with the given ID or domain.ErrNotFound.
func (s *Store) GetItem(ctx context.Context, id uuid.UUID) (domain.Item, error) {
	return scanItem(s.pool.QueryRow(ctx, `SELECT `+itemColumns+` FROM items WHERE id = $1`, id))
}

// feedSQL selects a page of items with their source, then attaches each
// item's sightings on other sources. The page is limited first (in the CTE),
// so the sightings subquery runs only for the rows returned: one statement,
// no N+1. %s is the WHERE clause, %s the LIMIT placeholder.
const feedSQL = `
WITH page AS (
	SELECT i.*, s.slug AS source_slug, s.name AS source_name, s.type AS source_type
	FROM items i JOIN sources s ON s.id = i.source_id
	%s
	ORDER BY i.feed_at DESC, i.id DESC
	LIMIT %s
)
SELECT ` + itemColumns + `, source_slug, source_name, source_type, (
	SELECT COALESCE(jsonb_agg(jsonb_build_object(
		'item_id', d.id, 'source_id', ds.id, 'source_slug', ds.slug, 'source_name', ds.name,
		'source_type', ds.type, 'url', d.url, 'discussion_url', d.discussion_url,
		'discovered_at', d.discovered_at) ORDER BY d.discovered_at, d.id), '[]')
	FROM items d JOIN sources ds ON ds.id = d.source_id
	WHERE d.duplicate_of = page.id
) AS sightings
FROM page
ORDER BY feed_at DESC, id DESC`

type sightingRow struct {
	ItemID        uuid.UUID `json:"item_id"`
	SourceID      uuid.UUID `json:"source_id"`
	SourceSlug    string    `json:"source_slug"`
	SourceName    string    `json:"source_name"`
	SourceType    string    `json:"source_type"`
	URL           string    `json:"url"`
	DiscussionURL string    `json:"discussion_url"`
	DiscoveredAt  time.Time `json:"discovered_at"`
}

func scanFeedItem(row pgx.Row) (domain.FeedItem, error) {
	var (
		f         domain.FeedItem
		srcType   string
		sightings []byte
	)
	it := &f.Item
	err := row.Scan(&it.ID, &it.SourceID, &it.ExternalID, &it.Kind, &it.Title, &it.Description, &it.URL, &it.CanonicalURL,
		&it.URLHash, &it.DiscussionURL, &it.Authors, &it.Tags, &it.ContentHash, &it.Metadata, &it.DuplicateOf,
		&it.PublishedAt, &it.DiscoveredAt, &it.LastSeenAt, &it.UpdatedAt, &it.FeedAt,
		&f.Source.Slug, &f.Source.Name, &srcType, &sightings)
	if err != nil {
		return domain.FeedItem{}, mapErr(err)
	}
	it.PublishedAt = utc(it.PublishedAt)
	it.DiscoveredAt, it.LastSeenAt, it.UpdatedAt, it.FeedAt = it.DiscoveredAt.UTC(), it.LastSeenAt.UTC(), it.UpdatedAt.UTC(), it.FeedAt.UTC()
	f.Source.ID, f.Source.Type = it.SourceID, domain.SourceType(srcType)

	var rows []sightingRow
	if err := json.Unmarshal(sightings, &rows); err != nil {
		return domain.FeedItem{}, fmt.Errorf("decode sightings of item %s: %w", it.ID, err)
	}
	f.AlsoSeenOn = make([]domain.Sighting, len(rows))
	for i, r := range rows {
		f.AlsoSeenOn[i] = domain.Sighting{
			ItemID:        r.ItemID,
			Source:        domain.SourceRef{ID: r.SourceID, Slug: r.SourceSlug, Name: r.SourceName, Type: domain.SourceType(r.SourceType)},
			URL:           r.URL,
			DiscussionURL: r.DiscussionURL,
			DiscoveredAt:  r.DiscoveredAt.UTC(),
		}
	}
	return f, nil
}

// GetFeedItem returns one item with its source and sightings, regardless of
// the source's status or whether the item is a duplicate.
func (s *Store) GetFeedItem(ctx context.Context, id uuid.UUID) (domain.FeedItem, error) {
	q := fmt.Sprintf(feedSQL, "WHERE i.id = $1", "1")
	return scanFeedItem(s.pool.QueryRow(ctx, q, id))
}

// ListItems returns items newest first (by FeedAt, then ID) matching f, with
// their source and sightings, using keyset pagination.
func (s *Store) ListItems(ctx context.Context, f domain.ItemFilter) (domain.ItemPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = domain.DefaultItemLimit
	}
	limit = min(limit, domain.MaxItemLimit)

	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !f.IncludeDuplicates {
		where = append(where, "i.duplicate_of IS NULL")
	}
	if len(f.SourceIDs) > 0 {
		ids := make([]string, len(f.SourceIDs))
		for i, id := range f.SourceIDs {
			ids[i] = id.String()
		}
		where = append(where, "i.source_id = ANY("+arg(ids)+"::uuid[])")
	}
	if len(f.SourceTypes) > 0 {
		where = append(where, "s.type = ANY("+arg(strs(f.SourceTypes))+"::text[])")
	}
	if len(f.SourceStatuses) > 0 {
		where = append(where, "s.status = ANY("+arg(strs(f.SourceStatuses))+"::text[])")
	}
	if len(f.Kinds) > 0 {
		where = append(where, "i.kind = ANY("+arg(strs(f.Kinds))+"::text[])")
	}
	if len(f.Tags) > 0 {
		where = append(where, "i.tags @> "+arg(f.Tags)+"::text[]")
	}
	for _, b := range []struct {
		col, op string
		t       *time.Time
	}{
		{"i.feed_at", ">=", f.Since}, {"i.feed_at", "<", f.Until},
		{"i.discovered_at", ">=", f.DiscoveredSince}, {"i.discovered_at", "<", f.DiscoveredUntil},
	} {
		if b.t != nil {
			where = append(where, b.col+" "+b.op+" "+arg(pgTime(*b.t)))
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// Must match the items_search_idx expression to use the index.
		where = append(where, "to_tsvector('english'::regconfig, i.title || ' ' || i.description) @@ websearch_to_tsquery('english'::regconfig, "+arg(q)+")")
	}
	if f.After != nil {
		where = append(where, "(i.feed_at, i.id) < ("+arg(pgTime(f.After.FeedAt))+", "+arg(f.After.ID)+")")
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	// Fetch one extra row to learn whether another page exists.
	rows, err := s.pool.Query(ctx, fmt.Sprintf(feedSQL, clause, arg(limit+1)), args...)
	if err != nil {
		return domain.ItemPage{}, fmt.Errorf("list items: %w", mapErr(err))
	}
	items, err := collect(rows, scanFeedItem)
	if err != nil {
		return domain.ItemPage{}, fmt.Errorf("list items: %w", err)
	}

	page := domain.ItemPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		next := page.Items[limit-1].CursorAfter()
		page.Next = &next
	}
	return page, nil
}

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

// ListDuplicates returns the items linked to primaryID as cross-source
// duplicates, oldest first.
func (s *Store) ListDuplicates(ctx context.Context, primaryID uuid.UUID) ([]domain.Item, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+itemColumns+` FROM items
		WHERE duplicate_of = $1
		ORDER BY discovered_at, id`, primaryID)
	if err != nil {
		return nil, fmt.Errorf("list duplicates: %w", mapErr(err))
	}
	return collect(rows, scanItem)
}

// nonNil turns a nil slice into an empty one for NOT NULL array columns.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
