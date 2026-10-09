package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

const feedbackColumns = `item_id, value, created_at, updated_at`

func scanFeedback(row pgx.Row) (domain.Feedback, error) {
	var f domain.Feedback
	if err := row.Scan(&f.ItemID, &f.Value, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return domain.Feedback{}, mapErr(err)
	}
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()
	return f, nil
}

// SetFeedback records the user's feedback on an item, replacing any earlier
// feedback on it. Repeating the same feedback leaves the row untouched. It
// returns domain.ErrNotFound if the item does not exist.
func (s *Store) SetFeedback(ctx context.Context, itemID uuid.UUID, v domain.FeedbackValue) (domain.Feedback, error) {
	if !v.Valid() {
		return domain.Feedback{}, fmt.Errorf("%w: feedback must be %q or %q", domain.ErrInvalid, domain.FeedbackMore, domain.FeedbackLess)
	}
	f, err := scanFeedback(s.pool.QueryRow(ctx, `
		INSERT INTO item_feedback AS f (item_id, value) VALUES ($1, $2)
		ON CONFLICT (item_id) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
		WHERE f.value <> EXCLUDED.value
		RETURNING `+feedbackColumns, itemID, v))
	if err == domain.ErrNotFound {
		// ON CONFLICT ... WHERE matched nothing: the same feedback exists.
		return scanFeedback(s.pool.QueryRow(ctx, `SELECT `+feedbackColumns+` FROM item_feedback WHERE item_id = $1`, itemID))
	}
	if err != nil {
		return domain.Feedback{}, fmt.Errorf("set feedback: %w", err)
	}
	return f, nil
}

// DeleteFeedback removes the user's feedback on an item. Removing feedback
// that does not exist is not an error.
func (s *Store) DeleteFeedback(ctx context.Context, itemID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM item_feedback WHERE item_id = $1`, itemID); err != nil {
		return fmt.Errorf("delete feedback: %w", mapErr(err))
	}
	return nil
}

// ListFeedback returns up to limit feedback entries, most recently changed
// first.
func (s *Store) ListFeedback(ctx context.Context, limit int) ([]domain.Feedback, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+feedbackColumns+` FROM item_feedback
		ORDER BY updated_at DESC, item_id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", mapErr(err))
	}
	return collect(rows, scanFeedback)
}

// FeedbackSignals derives the ranking signals from the most recent
// domain.MaxFeedbackSignals feedback entries, with each item's tags and
// usable enrichment topics (real model, current content).
func (s *Store) FeedbackSignals(ctx context.Context) (domain.FeedbackSignals, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.item_id, f.value, ARRAY(SELECT DISTINCT lower(t) FROM unnest(i.tags || COALESCE(e.topics, '{}'::text[])) AS t)
		FROM item_feedback f
		JOIN items i ON i.id = f.item_id
		LEFT JOIN item_enrichments e ON e.item_id = i.id AND e.provider <> 'fake' AND e.content_hash = i.content_hash
		ORDER BY f.updated_at DESC, f.item_id
		LIMIT $1`, domain.MaxFeedbackSignals)
	if err != nil {
		return domain.FeedbackSignals{}, fmt.Errorf("feedback signals: %w", mapErr(err))
	}
	entries, err := collect(rows, func(row pgx.Row) (domain.FeedbackEntry, error) {
		var e domain.FeedbackEntry
		return e, mapErr(row.Scan(&e.ItemID, &e.Value, &e.Topics))
	})
	if err != nil {
		return domain.FeedbackSignals{}, fmt.Errorf("feedback signals: %w", err)
	}
	return domain.NewFeedbackSignals(entries), nil
}
