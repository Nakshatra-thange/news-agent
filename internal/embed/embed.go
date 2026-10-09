// Package embed turns stored items into embedding vectors and stores them
// beside the items:
//
//	select items without a current embedding -> Input -> Provider
//	  -> validate the vector -> store (item_embeddings)
//
// Provider output is untrusted: a vector of the wrong length, with
// non-finite values or all zeros is reported and nothing is stored. Items
// are only read, never modified.
package embed

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"synergy/internal/domain"
)

// Provider is the minimal embedding interface: text in, one vector out.
// Implementations own their transport, credentials and model choice.
type Provider interface {
	// Name identifies the provider, e.g. "fake". Stored as provenance.
	Name() string
	// Model identifies the model. Stored as provenance; each model has its
	// own embeddings.
	Model() string
	// Dimensions is the vector length the provider returns.
	Dimensions() int
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Store is the persistence the service needs.
type Store interface {
	ItemsToEmbed(ctx context.Context, model string, dimensions, limit int) ([]domain.Item, error)
	UpsertEmbedding(ctx context.Context, e domain.Embedding) (domain.Embedding, error)
}

// Bounds for one explicit embedding run.
const (
	DefaultLimit = 10
	MaxLimit     = 50
	// itemTimeout bounds one provider call plus storing its result.
	itemTimeout = 30 * time.Second
	// maxInputRunes bounds the text sent to the provider.
	maxInputRunes = 8000
)

// Input is the text embedded for an item: its title and description, the
// same fields its content hash covers, so an item is re-embedded exactly
// when that text changes. The description is truncated to keep requests
// bounded.
func Input(item domain.Item) string {
	s := strings.TrimSpace(item.Title)
	if desc := strings.TrimSpace(item.Description); desc != "" {
		s += "\n\n" + desc
	}
	if r := []rune(s); len(r) > maxInputRunes {
		s = string(r[:maxInputRunes])
	}
	return s
}

// Service embeds items. It is safe for sequential use from one caller.
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
	Embedded int
	Failures []Failure
}

// Failure is one item that could not be embedded.
type Failure struct {
	Item domain.Item
	Err  error
}

// Run embeds up to limit items that lack a current embedding for the
// provider's model, newest first, one at a time. Items already embedded
// from the same content are not selected, so repeating a run does no work.
// A failure (provider error, invalid vector) is recorded for that item
// only: its item and any existing embedding stay untouched. There are no
// retries; a failed item is selected again by the next run.
func (s *Service) Run(ctx context.Context, limit int) (Result, error) {
	if limit < 1 || limit > MaxLimit {
		return Result{}, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrInvalid, MaxLimit)
	}
	items, err := s.store.ItemsToEmbed(ctx, s.provider.Model(), s.provider.Dimensions(), limit)
	if err != nil {
		return Result{}, err
	}
	res := Result{Selected: len(items)}
	for _, item := range items {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if err := s.embed(ctx, item); err != nil {
			res.Failures = append(res.Failures, Failure{Item: item, Err: err})
			s.logger.Warn("embedding failed", "item_id", item.ID, "err", err)
			continue
		}
		res.Embedded++
	}
	return res, nil
}

func (s *Service) embed(ctx context.Context, item domain.Item) error {
	ctx, cancel := context.WithTimeout(ctx, itemTimeout)
	defer cancel()

	vec, err := s.provider.Embed(ctx, Input(item))
	if err != nil {
		return fmt.Errorf("provider %s: %w", s.provider.Name(), err)
	}
	e := domain.Embedding{
		ItemID: item.ID, Provider: s.provider.Name(), Model: s.provider.Model(),
		Dimensions: s.provider.Dimensions(), Vector: vec, ContentHash: item.ContentHash,
	}
	if err := e.Validate(); err != nil {
		return fmt.Errorf("provider %s returned an invalid vector: %w", s.provider.Name(), err)
	}
	if _, err := s.store.UpsertEmbedding(ctx, e); err != nil {
		return fmt.Errorf("store embedding: %w", err)
	}
	return nil
}
