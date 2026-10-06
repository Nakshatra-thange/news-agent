package store

import (
	"context"
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

// ItemPage is one page of a keyset-paginated item listing.
type ItemPage struct {
	Items []domain.Item
	// Next is the cursor for the following page, or nil on the last page.
	Next *domain.ItemCursor
}

// ListItems returns items newest first (by FeedAt, then ID) matching f.
func (s *Store) ListItems(ctx context.Context, f domain.ItemFilter) (ItemPage, error) {
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
		where = append(where, "duplicate_of IS NULL")
	}
	if len(f.SourceIDs) > 0 {
		ids := make([]string, len(f.SourceIDs))
		for i, id := range f.SourceIDs {
			ids[i] = id.String()
		}
		where = append(where, "source_id = ANY("+arg(ids)+"::uuid[])")
	}
	if f.Kind != "" {
		where = append(where, "kind = "+arg(string(f.Kind)))
	}
	if f.Tag != "" {
		where = append(where, "tags @> ARRAY["+arg(f.Tag)+"::text]")
	}
	if f.Since != nil {
		where = append(where, "feed_at >= "+arg(pgTime(*f.Since)))
	}
	if f.Until != nil {
		where = append(where, "feed_at < "+arg(pgTime(*f.Until)))
	}
	if f.After != nil {
		where = append(where, "(feed_at, id) < ("+arg(pgTime(f.After.FeedAt))+", "+arg(f.After.ID)+")")
	}

	q := `SELECT ` + itemColumns + ` FROM items`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	// Fetch one extra row to learn whether another page exists.
	q += ` ORDER BY feed_at DESC, id DESC LIMIT ` + arg(limit+1)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return ItemPage{}, fmt.Errorf("list items: %w", mapErr(err))
	}
	items, err := collect(rows, scanItem)
	if err != nil {
		return ItemPage{}, fmt.Errorf("list items: %w", err)
	}

	page := ItemPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		next := page.Items[limit-1].CursorAfter()
		page.Next = &next
	}
	return page, nil
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
