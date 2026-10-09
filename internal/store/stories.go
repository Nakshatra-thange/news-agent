package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

// ItemsToCluster returns the embeddings of up to limit items, oldest first,
// that have no story for the model yet and a current embedding for it: made
// from the item's present content, with the given dimensions. Items without
// one (never embedded, or changed since) wait for `synergy embed`.
// Cross-source duplicates are skipped.
func (s *Store) ItemsToCluster(ctx context.Context, model string, dimensions, limit int) ([]domain.Embedding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.item_id, e.provider, e.model, e.dimensions, e.embedding, e.content_hash, e.created_at, e.updated_at
		FROM item_embeddings e
		JOIN items i ON i.id = e.item_id
		WHERE e.model = $1 AND e.dimensions = $2
		  AND e.content_hash = i.content_hash
		  AND i.duplicate_of IS NULL
		  AND NOT EXISTS (SELECT 1 FROM story_items si WHERE si.item_id = i.id AND si.model = $1)
		ORDER BY i.feed_at, i.id
		LIMIT $3`, model, dimensions, limit)
	if err != nil {
		return nil, fmt.Errorf("items to cluster: %w", mapErr(err))
	}
	return collect(rows, scanEmbedding)
}

// StorySeeds returns up to limit of the model's newest stories with the
// vector of their seed item. Stories whose seed has no embedding with the
// given dimensions are left out.
func (s *Store) StorySeeds(ctx context.Context, model string, dimensions, limit int) ([]domain.StorySeed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT st.id, e.embedding FROM stories st
		JOIN item_embeddings e ON e.item_id = st.seed_item_id AND e.model = st.model
		WHERE st.model = $1 AND e.dimensions = $2
		ORDER BY st.id DESC
		LIMIT $3`, model, dimensions, limit)
	if err != nil {
		return nil, fmt.Errorf("story seeds: %w", mapErr(err))
	}
	return collect(rows, func(row pgx.Row) (domain.StorySeed, error) {
		var seed domain.StorySeed
		return seed, mapErr(row.Scan(&seed.StoryID, &seed.Vector))
	})
}

// CreateStory starts a story for the model with the item as its seed and
// first member. It returns domain.ErrConflict if the item already belongs
// to a story for the model.
func (s *Store) CreateStory(ctx context.Context, model string, seedItemID uuid.UUID) (uuid.UUID, error) {
	id := newID()
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO stories (id, model, seed_item_id) VALUES ($1, $2, $3)`,
			id, model, seedItemID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO story_items (story_id, item_id, model, similarity) VALUES ($1, $2, $3, 1)`,
			id, seedItemID, model)
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create story: %w", mapErr(err))
	}
	return id, nil
}

// AddStoryItem adds an item to a story with its similarity to the story's
// seed. It returns domain.ErrConflict if the item already belongs to a
// story for the model, and domain.ErrNotFound if the story (for that model)
// or the item does not exist.
func (s *Store) AddStoryItem(ctx context.Context, storyID, itemID uuid.UUID, model string, similarity float64) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO story_items (story_id, item_id, model, similarity) VALUES ($1, $2, $3, $4)`,
		storyID, itemID, model, similarity)
	if err != nil {
		return fmt.Errorf("add story item: %w", mapErr(err))
	}
	return nil
}

// ItemStory returns the story the item belongs to for the model, or
// domain.ErrNotFound.
func (s *Store) ItemStory(ctx context.Context, itemID uuid.UUID, model string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT story_id FROM story_items WHERE item_id = $1 AND model = $2`, itemID, model).Scan(&id)
	return id, mapErr(err)
}
