package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"synergy/internal/domain"
)

// maxErrorLen bounds stored error messages so a pathological upstream
// response cannot bloat the database.
const maxErrorLen = 2000

const fetchRunColumns = `id, source_id, trigger, status, started_at, finished_at,
	items_fetched, items_inserted, items_updated, items_unchanged, items_duplicate, items_rejected, error`

func scanFetchRun(row pgx.Row) (domain.FetchRun, error) {
	var r domain.FetchRun
	err := row.Scan(&r.ID, &r.SourceID, &r.Trigger, &r.Status, &r.StartedAt, &r.FinishedAt,
		&r.Stats.Fetched, &r.Stats.Inserted, &r.Stats.Updated, &r.Stats.Unchanged, &r.Stats.Duplicate, &r.Stats.Rejected, &r.Error)
	if err != nil {
		return domain.FetchRun{}, mapErr(err)
	}
	r.StartedAt, r.FinishedAt = r.StartedAt.UTC(), utc(r.FinishedAt)
	return r, nil
}

// StartFetchRun records a new running fetch for a source and stamps the
// source's last_fetch_at. It returns domain.ErrFetchInProgress if the source
// already has a running fetch (enforced by a partial unique index), and
// domain.ErrNotFound if the source does not exist.
func (s *Store) StartFetchRun(ctx context.Context, sourceID uuid.UUID, trigger domain.RunTrigger) (domain.FetchRun, error) {
	if !trigger.Valid() {
		return domain.FetchRun{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "trigger", Message: "must be api, cli or scheduler"}}}
	}
	var run domain.FetchRun
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		run, err = scanFetchRun(tx.QueryRow(ctx, `
			INSERT INTO fetch_runs (id, source_id, trigger) VALUES ($1, $2, $3)
			RETURNING `+fetchRunColumns, newID(), sourceID, string(trigger)))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sources SET last_fetch_at = $2 WHERE id = $1`, sourceID, run.StartedAt)
		return mapErr(err)
	})
	if err != nil {
		return domain.FetchRun{}, err
	}
	return run, nil
}

// FinishFetchRun completes a running fetch and, in the same transaction,
// updates the source's health: success resets the failure streak and
// persists the adapter state; failure records the error and extends the
// streak. Finishing a run that is not running returns domain.ErrConflict.
func (s *Store) FinishFetchRun(ctx context.Context, runID uuid.UUID, c domain.RunCompletion) (domain.FetchRun, error) {
	if c.State != nil && !json.Valid(c.State) {
		return domain.FetchRun{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "state", Message: "must be valid JSON"}}}
	}
	status, errMsg := domain.RunSucceeded, ""
	if !c.Succeeded() {
		status, errMsg = domain.RunFailed, truncate(c.Err, maxErrorLen)
	}

	var run domain.FetchRun
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		run, err = scanFetchRun(tx.QueryRow(ctx, `
			UPDATE fetch_runs SET
				status = $2, finished_at = now(), error = $3,
				items_fetched = $4, items_inserted = $5, items_updated = $6,
				items_unchanged = $7, items_duplicate = $8, items_rejected = $9
			WHERE id = $1 AND status = 'running'
			RETURNING `+fetchRunColumns,
			runID, string(status), errMsg,
			c.Stats.Fetched, c.Stats.Inserted, c.Stats.Updated, c.Stats.Unchanged, c.Stats.Duplicate, c.Stats.Rejected))
		if errors.Is(err, domain.ErrNotFound) {
			return s.explainUnfinishable(ctx, tx, runID)
		}
		if err != nil {
			return err
		}

		if status == domain.RunSucceeded {
			var state []byte // nil keeps the existing state
			if c.State != nil {
				state = c.State
			}
			_, err = tx.Exec(ctx, `
				UPDATE sources SET
					last_success_at = $2, consecutive_failures = 0, last_error = '',
					state = COALESCE($3::jsonb, state), updated_at = now()
				WHERE id = $1`, run.SourceID, *run.FinishedAt, state)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE sources SET
					last_failure_at = $2, consecutive_failures = consecutive_failures + 1,
					last_error = $3, updated_at = now()
				WHERE id = $1`, run.SourceID, *run.FinishedAt, errMsg)
		}
		return mapErr(err)
	})
	if err != nil {
		return domain.FetchRun{}, err
	}
	return run, nil
}

// explainUnfinishable distinguishes a missing run from one already finished.
func (s *Store) explainUnfinishable(ctx context.Context, tx pgx.Tx, runID uuid.UUID) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM fetch_runs WHERE id = $1`, runID).Scan(&status)
	if err != nil {
		return mapErr(err)
	}
	return fmt.Errorf("%w: fetch run %s is already %s", domain.ErrConflict, runID, status)
}

// GetFetchRun returns the run with the given ID or domain.ErrNotFound.
func (s *Store) GetFetchRun(ctx context.Context, id uuid.UUID) (domain.FetchRun, error) {
	return scanFetchRun(s.pool.QueryRow(ctx, `SELECT `+fetchRunColumns+` FROM fetch_runs WHERE id = $1`, id))
}

// ListFetchRuns returns a source's most recent runs, newest first.
func (s *Store) ListFetchRuns(ctx context.Context, sourceID uuid.UUID, limit int) ([]domain.FetchRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+fetchRunColumns+` FROM fetch_runs
		WHERE source_id = $1
		ORDER BY started_at DESC, id DESC
		LIMIT $2`, sourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list fetch runs: %w", mapErr(err))
	}
	return collect(rows, scanFetchRun)
}

// FailAbandonedRuns marks runs still "running" that started before cutoff as
// failed. It is meant for process startup, when no fetch can legitimately be
// in flight, so a crash never blocks a source forever. It returns the number
// of runs marked.
func (s *Store) FailAbandonedRuns(ctx context.Context, cutoff time.Time, reason string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE fetch_runs SET status = 'failed', finished_at = now(), error = $2
		WHERE status = 'running' AND started_at < $1`, pgTime(cutoff), truncate(reason, maxErrorLen))
	if err != nil {
		return 0, fmt.Errorf("fail abandoned runs: %w", mapErr(err))
	}
	return tag.RowsAffected(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Back off to a rune boundary so the result stays valid UTF-8.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
