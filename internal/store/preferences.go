package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

// GetPreferences returns the user's preferences, normalized. Without any,
// both lists are empty.
func (s *Store) GetPreferences(ctx context.Context) (domain.Preferences, error) {
	var p domain.Preferences
	err := s.pool.QueryRow(ctx, `SELECT
		ARRAY(SELECT topic FROM preferred_topics ORDER BY topic),
		ARRAY(SELECT source_id FROM preferred_sources ORDER BY source_id::text)`).Scan(&p.Topics, &p.SourceIDs)
	if err != nil {
		return domain.Preferences{}, fmt.Errorf("get preferences: %w", mapErr(err))
	}
	p.Normalize()
	return p, nil
}

// SetPreferences replaces the user's preferences in one transaction and
// returns them normalized. It returns a *domain.ValidationError for invalid
// input and domain.ErrNotFound if a source does not exist.
func (s *Store) SetPreferences(ctx context.Context, p domain.Preferences) (domain.Preferences, error) {
	p.Normalize()
	if err := p.Validate(); err != nil {
		return domain.Preferences{}, err
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM preferred_topics`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM preferred_sources`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO preferred_topics (topic) SELECT unnest($1::text[])`, nonNil(p.Topics)); err != nil {
			return err
		}
		ids := make([]string, len(p.SourceIDs))
		for i, id := range p.SourceIDs {
			ids[i] = id.String()
		}
		_, err := tx.Exec(ctx, `INSERT INTO preferred_sources (source_id) SELECT unnest($1::uuid[])`, ids)
		return err
	})
	if err != nil {
		return domain.Preferences{}, fmt.Errorf("set preferences: %w", mapErr(err))
	}
	return p, nil
}
