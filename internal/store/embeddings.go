package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

const embeddingColumns = `item_id, provider, model, dimensions, embedding, content_hash, created_at, updated_at`

func scanEmbedding(row pgx.Row) (domain.Embedding, error) {
	var e domain.Embedding
	err := row.Scan(&e.ItemID, &e.Provider, &e.Model, &e.Dimensions, &e.Vector, &e.ContentHash, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return domain.Embedding{}, mapErr(err)
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return e, nil
}

// UpsertEmbedding stores an item's embedding for its model, replacing any
// previous one for the same item and model. An identical row is left
// untouched. It returns the stored row, domain.ErrNotFound if the item does
// not exist, and a *domain.ValidationError for invalid input. The item is
// never modified.
func (s *Store) UpsertEmbedding(ctx context.Context, e domain.Embedding) (domain.Embedding, error) {
	if err := e.Validate(); err != nil {
		return domain.Embedding{}, err
	}
	stored, err := scanEmbedding(s.pool.QueryRow(ctx, `
		INSERT INTO item_embeddings AS ie (item_id, provider, model, dimensions, embedding, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (item_id, model) DO UPDATE SET
			provider = EXCLUDED.provider, dimensions = EXCLUDED.dimensions,
			embedding = EXCLUDED.embedding, content_hash = EXCLUDED.content_hash,
			updated_at = now()
		WHERE (ie.provider, ie.dimensions, ie.embedding, ie.content_hash)
		   IS DISTINCT FROM
		      (EXCLUDED.provider, EXCLUDED.dimensions, EXCLUDED.embedding, EXCLUDED.content_hash)
		RETURNING `+embeddingColumns,
		e.ItemID, e.Provider, e.Model, e.Dimensions, e.Vector, e.ContentHash))
	if err == domain.ErrNotFound {
		// ON CONFLICT ... WHERE matched nothing: identical row already stored.
		return s.GetEmbedding(ctx, e.ItemID, e.Model)
	}
	return stored, err
}

// GetEmbedding returns an item's embedding for a model or domain.ErrNotFound.
func (s *Store) GetEmbedding(ctx context.Context, itemID uuid.UUID, model string) (domain.Embedding, error) {
	return scanEmbedding(s.pool.QueryRow(ctx,
		`SELECT `+embeddingColumns+` FROM item_embeddings WHERE item_id = $1 AND model = $2`, itemID, model))
}

// ItemsToEmbed returns up to limit items, newest first, without a current
// embedding for the model: none at all, or one made from different item
// content or with different dimensions. Cross-source duplicates are
// skipped; their primary item carries the embedding.
func (s *Store) ItemsToEmbed(ctx context.Context, model string, dimensions, limit int) ([]domain.Item, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+itemColumns+` FROM items i
		WHERE i.duplicate_of IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM item_embeddings e
			WHERE e.item_id = i.id AND e.model = $1
			  AND e.content_hash = i.content_hash AND e.dimensions = $2)
		ORDER BY i.feed_at DESC, i.id DESC
		LIMIT $3`, model, dimensions, limit)
	if err != nil {
		return nil, fmt.Errorf("items to embed: %w", mapErr(err))
	}
	return collect(rows, scanItem)
}
