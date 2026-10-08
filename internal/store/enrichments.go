package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

const enrichmentColumns = `item_id, topics, entities, importance, category,
	provider, model, prompt_version, content_hash, created_at, updated_at`

func scanEnrichment(row pgx.Row) (domain.Enrichment, error) {
	var (
		e        domain.Enrichment
		entities []byte
	)
	err := row.Scan(&e.ItemID, &e.Topics, &entities, &e.Importance, &e.Category,
		&e.Provider, &e.Model, &e.PromptVersion, &e.ContentHash, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return domain.Enrichment{}, mapErr(err)
	}
	if err := json.Unmarshal(entities, &e.Entities); err != nil {
		return domain.Enrichment{}, fmt.Errorf("decode entities of item %s: %w", e.ItemID, err)
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return e, nil
}

// UpsertEnrichment stores an item's enrichment, replacing any previous one
// (there is at most one per item). A row whose stored values are identical
// is left untouched, so repeating an enrichment is a no-op. It returns the
// stored row, domain.ErrNotFound if the item does not exist, and a
// *domain.ValidationError for invalid input. The item itself is never
// modified.
func (s *Store) UpsertEnrichment(ctx context.Context, e domain.Enrichment) (domain.Enrichment, error) {
	e.Normalize()
	if err := e.Validate(); err != nil {
		return domain.Enrichment{}, err
	}
	entities, err := json.Marshal(e.Entities)
	if err != nil {
		return domain.Enrichment{}, fmt.Errorf("encode entities: %w", err)
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO item_enrichments AS ie (item_id, topics, entities, importance, category,
			provider, model, prompt_version, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (item_id) DO UPDATE SET
			topics = EXCLUDED.topics, entities = EXCLUDED.entities, importance = EXCLUDED.importance,
			category = EXCLUDED.category, provider = EXCLUDED.provider, model = EXCLUDED.model,
			prompt_version = EXCLUDED.prompt_version, content_hash = EXCLUDED.content_hash,
			updated_at = now()
		WHERE (ie.topics, ie.entities, ie.importance, ie.category, ie.provider, ie.model,
		       ie.prompt_version, ie.content_hash)
		   IS DISTINCT FROM
		      (EXCLUDED.topics, EXCLUDED.entities, EXCLUDED.importance, EXCLUDED.category,
		       EXCLUDED.provider, EXCLUDED.model, EXCLUDED.prompt_version, EXCLUDED.content_hash)
		RETURNING `+enrichmentColumns,
		e.ItemID, e.Topics, entities, e.Importance, string(e.Category),
		e.Provider, e.Model, e.PromptVersion, e.ContentHash)
	stored, err := scanEnrichment(row)
	if err == domain.ErrNotFound {
		// ON CONFLICT ... WHERE matched nothing: identical row already stored.
		return s.GetEnrichment(ctx, e.ItemID)
	}
	return stored, err
}

// GetEnrichment returns an item's enrichment or domain.ErrNotFound.
func (s *Store) GetEnrichment(ctx context.Context, itemID uuid.UUID) (domain.Enrichment, error) {
	return scanEnrichment(s.pool.QueryRow(ctx,
		`SELECT `+enrichmentColumns+` FROM item_enrichments WHERE item_id = $1`, itemID))
}

// ItemsToEnrich returns up to limit items, newest first, that have no
// current enrichment: none at all, or one made from different item content
// or by a different model or prompt version. Cross-source duplicates are
// skipped; their primary item carries the enrichment.
func (s *Store) ItemsToEnrich(ctx context.Context, model string, promptVersion, limit int) ([]domain.Item, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+itemColumns+` FROM items i
		WHERE i.duplicate_of IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM item_enrichments e
			WHERE e.item_id = i.id AND e.content_hash = i.content_hash
			  AND e.model = $1 AND e.prompt_version = $2)
		ORDER BY i.feed_at DESC, i.id DESC
		LIMIT $3`, model, promptVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("items to enrich: %w", mapErr(err))
	}
	return collect(rows, scanItem)
}
