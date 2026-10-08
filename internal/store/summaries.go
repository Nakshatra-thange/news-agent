package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

const summaryColumns = `item_id, summary, provider, model, prompt_version, content_hash, created_at, updated_at`

func scanSummary(row pgx.Row) (domain.Summary, error) {
	var s domain.Summary
	err := row.Scan(&s.ItemID, &s.Text, &s.Provider, &s.Model, &s.PromptVersion, &s.ContentHash, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return domain.Summary{}, mapErr(err)
	}
	s.CreatedAt, s.UpdatedAt = s.CreatedAt.UTC(), s.UpdatedAt.UTC()
	return s, nil
}

// UpsertSummary stores an item's summary, replacing any previous one (there
// is at most one per item). An identical row is left untouched. It returns
// the stored row, domain.ErrNotFound if the item does not exist, and a
// *domain.ValidationError for invalid input. The item is never modified.
func (s *Store) UpsertSummary(ctx context.Context, sum domain.Summary) (domain.Summary, error) {
	sum.Normalize()
	if err := sum.Validate(); err != nil {
		return domain.Summary{}, err
	}
	stored, err := scanSummary(s.pool.QueryRow(ctx, `
		INSERT INTO item_summaries AS isum (item_id, summary, provider, model, prompt_version, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (item_id) DO UPDATE SET
			summary = EXCLUDED.summary, provider = EXCLUDED.provider, model = EXCLUDED.model,
			prompt_version = EXCLUDED.prompt_version, content_hash = EXCLUDED.content_hash,
			updated_at = now()
		WHERE (isum.summary, isum.provider, isum.model, isum.prompt_version, isum.content_hash)
		   IS DISTINCT FROM
		      (EXCLUDED.summary, EXCLUDED.provider, EXCLUDED.model, EXCLUDED.prompt_version, EXCLUDED.content_hash)
		RETURNING `+summaryColumns,
		sum.ItemID, sum.Text, sum.Provider, sum.Model, sum.PromptVersion, sum.ContentHash))
	if err == domain.ErrNotFound {
		// ON CONFLICT ... WHERE matched nothing: identical row already stored.
		return s.GetSummary(ctx, sum.ItemID)
	}
	return stored, err
}

// GetSummary returns an item's summary or domain.ErrNotFound.
func (s *Store) GetSummary(ctx context.Context, itemID uuid.UUID) (domain.Summary, error) {
	return scanSummary(s.pool.QueryRow(ctx, `SELECT `+summaryColumns+` FROM item_summaries WHERE item_id = $1`, itemID))
}

// ItemsToSummarize returns up to limit items, newest first, without a
// current summary: none at all, or one written from different item content
// or by a different model or prompt version. Cross-source duplicates are
// skipped; their primary item carries the summary.
func (s *Store) ItemsToSummarize(ctx context.Context, model string, promptVersion, limit int) ([]domain.Item, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+itemColumns+` FROM items i
		WHERE i.duplicate_of IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM item_summaries s
			WHERE s.item_id = i.id AND s.content_hash = i.content_hash
			  AND s.model = $1 AND s.prompt_version = $2)
		ORDER BY i.feed_at DESC, i.id DESC
		LIMIT $3`, model, promptVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("items to summarize: %w", mapErr(err))
	}
	return collect(rows, scanItem)
}
