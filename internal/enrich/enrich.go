// Package enrich extracts structured intelligence (topics, entities,
// importance, category) from stored items with an LLM and stores it beside
// them:
//
//	select items without a current enrichment -> prompt -> Provider
//	  -> parse + validate the output -> store (item_enrichments)
//
// The model's output is untrusted. It is parsed strictly and validated
// against domain bounds; anything malformed is reported and nothing is
// stored. Items are only read, never modified.
package enrich

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"synergy/internal/domain"
)

// Provider is the minimal LLM interface: send a prompt, get the model's text
// back. Implementations own their transport, credentials and model choice.
type Provider interface {
	// Name identifies the provider, e.g. "fake". Stored as provenance.
	Name() string
	// Model identifies the model, e.g. "fake-1". Stored as provenance; a
	// different model makes existing enrichments stale.
	Model() string
	Complete(ctx context.Context, p Prompt) (string, error)
}

// Store is the persistence the service needs.
type Store interface {
	ItemsToEnrich(ctx context.Context, model string, promptVersion, limit int) ([]domain.Item, error)
	UpsertEnrichment(ctx context.Context, e domain.Enrichment) (domain.Enrichment, error)
}

// Bounds for one explicit enrichment run.
const (
	DefaultLimit = 10
	MaxLimit     = 100
	// itemTimeout bounds one provider call plus storing its result.
	itemTimeout = time.Minute
)

// Service enriches items. It is safe for sequential use from one caller.
type Service struct {
	store    Store
	provider Provider
	logger   *slog.Logger
}

// New builds a Service.
func New(st Store, p Provider, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, provider: p, logger: logger}
}

// Result summarizes one run.
type Result struct {
	Selected int
	Enriched int
	Failures []Failure
}

// Failure is one item that could not be enriched.
type Failure struct {
	Item domain.Item
	Err  error
}

// Run enriches up to limit items that lack a current enrichment, newest
// first, one at a time. Items already enriched from the same content, model
// and prompt version are not selected, so repeating a run does no work. One
// item failing never stops the others; there are no retries.
func (s *Service) Run(ctx context.Context, limit int) (Result, error) {
	if limit < 1 || limit > MaxLimit {
		return Result{}, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrInvalid, MaxLimit)
	}
	items, err := s.store.ItemsToEnrich(ctx, s.provider.Model(), PromptVersion, limit)
	if err != nil {
		return Result{}, err
	}
	res := Result{Selected: len(items)}
	for _, item := range items {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if err := s.enrichItem(ctx, item); err != nil {
			res.Failures = append(res.Failures, Failure{Item: item, Err: err})
			s.logger.Warn("enrichment failed", "item_id", item.ID, "err", err)
			continue
		}
		res.Enriched++
	}
	return res, nil
}

func (s *Service) enrichItem(ctx context.Context, item domain.Item) error {
	ctx, cancel := context.WithTimeout(ctx, itemTimeout)
	defer cancel()

	out, err := s.provider.Complete(ctx, BuildPrompt(item))
	if err != nil {
		return fmt.Errorf("provider %s: %w", s.provider.Name(), err)
	}
	e, err := ParseOutput(out)
	if err != nil {
		return err
	}
	e.ItemID = item.ID
	e.Provider, e.Model, e.PromptVersion = s.provider.Name(), s.provider.Model(), PromptVersion
	e.ContentHash = item.ContentHash
	e.Normalize()
	if err := e.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	if _, err := s.store.UpsertEnrichment(ctx, e); err != nil {
		return fmt.Errorf("store enrichment: %w", err)
	}
	return nil
}

// ErrInvalidOutput marks model output that is malformed or out of bounds.
var ErrInvalidOutput = errors.New("invalid model output")
