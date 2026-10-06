package sources

import (
	"context"
	"encoding/json"

	"synergy/internal/domain"
)

// Adapter fetches content from one kind of upstream source. It is the only
// thing a new source type has to implement to take part in ingestion; the
// pipeline (internal/ingest) does everything else: normalization, URL
// canonicalization, hashing, deduplication, persistence, fetch-run tracking
// and source health.
//
// Contract:
//
//   - Fetch must honor ctx: stop promptly and return ctx.Err() (possibly
//     wrapped) when it is cancelled or its deadline passes.
//   - Fetch reads its settings from src.Config (decode with the type's
//     ParseConfig) and its cursor from src.State.
//   - Candidates should be unique by ExternalID; the pipeline folds repeats.
//   - Fetch does no database access and no deduplication.
//   - On partial failure (some requests succeeded), return the candidates
//     gathered so far together with a non-nil error: they are stored, and
//     the run is still recorded as failed. Result.State is ignored then.
//   - Errors must not contain credentials.
type Adapter interface {
	Type() domain.SourceType
	Fetch(ctx context.Context, src domain.Source) (FetchResult, error)
}

// FetchResult is what one Fetch call produced.
type FetchResult struct {
	Items []domain.Candidate
	// State is the adapter's new cursor/watermark, persisted to the source
	// only when the whole run succeeds. Nil keeps the previous state.
	State json.RawMessage
	// Requests is the number of upstream HTTP requests made (for logs and
	// politeness accounting).
	Requests int
}
