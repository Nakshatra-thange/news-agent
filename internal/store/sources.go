package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

const sourceColumns = `id, slug, name, type, url, status, priority, config, state,
	min_fetch_interval_seconds, last_fetch_at, last_success_at, last_failure_at,
	last_error, consecutive_failures, created_at, updated_at, retired_at`

func scanSource(row pgx.Row) (domain.Source, error) {
	var (
		s        domain.Source
		interval int32
	)
	err := row.Scan(&s.ID, &s.Slug, &s.Name, &s.Type, &s.URL, &s.Status, &s.Priority, &s.Config, &s.State,
		&interval, &s.LastFetchAt, &s.LastSuccessAt, &s.LastFailureAt,
		&s.LastError, &s.ConsecutiveFailures, &s.CreatedAt, &s.UpdatedAt, &s.RetiredAt)
	if err != nil {
		return domain.Source{}, mapErr(err)
	}
	s.MinFetchInterval = time.Duration(interval) * time.Second
	s.LastFetchAt, s.LastSuccessAt, s.LastFailureAt, s.RetiredAt = utc(s.LastFetchAt), utc(s.LastSuccessAt), utc(s.LastFailureAt), utc(s.RetiredAt)
	s.CreatedAt, s.UpdatedAt = s.CreatedAt.UTC(), s.UpdatedAt.UTC()
	return s, nil
}

// CreateSource registers a new source. It returns domain.ErrConflict if the
// slug is taken and a *domain.ValidationError for invalid input.
func (s *Store) CreateSource(ctx context.Context, n domain.NewSource) (domain.Source, error) {
	n.Normalize()
	if err := n.Validate(); err != nil {
		return domain.Source{}, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO sources (id, slug, name, type, url, status, priority, config, min_fetch_interval_seconds, retired_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, CASE WHEN $6 = 'retired' THEN now() END)
		RETURNING `+sourceColumns,
		newID(), n.Slug, n.Name, string(n.Type), n.URL, string(n.Status), n.Priority, []byte(n.Config),
		int32(*n.MinFetchInterval/time.Second))
	return scanSource(row)
}

// GetSource returns the source with the given ID or domain.ErrNotFound.
func (s *Store) GetSource(ctx context.Context, id uuid.UUID) (domain.Source, error) {
	return scanSource(s.pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM sources WHERE id = $1`, id))
}

// GetSourceBySlug returns the source with the given slug or domain.ErrNotFound.
func (s *Store) GetSourceBySlug(ctx context.Context, slug string) (domain.Source, error) {
	return scanSource(s.pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM sources WHERE slug = $1`, slug))
}

// ListSources returns sources ordered by priority (highest first), then name.
// If statuses is non-empty, only sources in those statuses are returned.
func (s *Store) ListSources(ctx context.Context, statuses ...domain.SourceStatus) ([]domain.Source, error) {
	filter := make([]string, len(statuses))
	for i, st := range statuses {
		filter[i] = string(st)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+sourceColumns+` FROM sources
		WHERE cardinality($1::text[]) = 0 OR status = ANY($1::text[])
		ORDER BY priority DESC, name, id`, filter)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", mapErr(err))
	}
	return collect(rows, scanSource)
}

// UpdateSource applies a partial update. Moving to status "retired" stamps
// retired_at; moving out of it clears retired_at.
func (s *Store) UpdateSource(ctx context.Context, id uuid.UUID, p domain.SourcePatch) (domain.Source, error) {
	p.Normalize()
	if err := p.Validate(); err != nil {
		return domain.Source{}, err
	}
	if p.IsEmpty() {
		return s.GetSource(ctx, id)
	}

	var (
		status   *string
		config   []byte // nil encodes as NULL, leaving the column unchanged
		interval *int32
	)
	if p.Status != nil {
		v := string(*p.Status)
		status = &v
	}
	if p.Config != nil {
		config = []byte(*p.Config)
	}
	if p.MinFetchInterval != nil {
		v := int32(*p.MinFetchInterval / time.Second)
		interval = &v
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE sources SET
			name = COALESCE($2, name),
			url = COALESCE($3, url),
			status = COALESCE($4, status),
			priority = COALESCE($5, priority),
			config = COALESCE($6::jsonb, config),
			min_fetch_interval_seconds = COALESCE($7, min_fetch_interval_seconds),
			retired_at = CASE WHEN COALESCE($4, status) = 'retired' THEN COALESCE(retired_at, now()) END,
			updated_at = now()
		WHERE id = $1
		RETURNING `+sourceColumns,
		id, p.Name, p.URL, status, p.Priority, config, interval)
	return scanSource(row)
}

// collect scans every row with scan, closing rows.
func collect[T any](rows pgx.Rows, scan func(pgx.Row) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// jsonOrEmpty returns raw, or {} if raw is empty, for NOT NULL jsonb columns.
func jsonOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}
