// Package cluster groups items about the same underlying story, using their
// stored embeddings and cosine similarity:
//
//	select embedded items without a story -> compare with story seeds
//	  -> join the most similar story at or above the threshold,
//	     or start a new story -> store (stories, story_items)
//
// It is "leader" clustering: each story is represented by the vector of
// its first item (the seed), and an item joins at most one story. Items
// are taken oldest first, so the result is deterministic for the same
// data, threshold and limit. No LLM is involved, and items are never
// modified.
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// Store is the persistence the service needs.
type Store interface {
	ItemsToCluster(ctx context.Context, model string, dimensions, limit int) ([]domain.Embedding, error)
	StorySeeds(ctx context.Context, model string, dimensions, limit int) ([]domain.StorySeed, error)
	CreateStory(ctx context.Context, model string, seedItemID uuid.UUID) (uuid.UUID, error)
	AddStoryItem(ctx context.Context, storyID, itemID uuid.UUID, model string, similarity float64) error
}

// Bounds for one explicit clustering run, and the default threshold.
const (
	DefaultLimit = 20
	MaxLimit     = 200
	// MaxSeeds bounds how many of the newest stories an item is compared
	// with. Older stories no longer gain items.
	MaxSeeds = 1000
	// DefaultThreshold is the minimum cosine similarity to a story's seed
	// for an item to join it. It is an initial, unvalidated choice: high
	// enough that topically related but distinct items usually stay apart,
	// at the cost of missing some rewordings of the same story. Tune it
	// with real data.
	DefaultThreshold = 0.80
)

// Options selects the embeddings to cluster and how strictly.
type Options struct {
	// Model and Dimensions select which embeddings are compared. Vectors
	// from different models or of different lengths are never mixed.
	Model      string
	Dimensions int
	// Threshold is the minimum cosine similarity, in (0, 1].
	Threshold float64
}

// Service clusters items. It is safe for sequential use from one caller.
type Service struct {
	store  Store
	opts   Options
	logger *slog.Logger
}

// New builds a Service.
func New(st Store, opts Options, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, opts: opts, logger: logger}
}

// Result summarizes one run.
type Result struct {
	Selected int
	// Joined counts items added to an existing story, Created the stories
	// started by this run (each with its seed item).
	Joined   int
	Created  int
	Failures []Failure
}

// Failure is one item that could not be clustered.
type Failure struct {
	ItemID uuid.UUID
	Err    error
}

// Run assigns up to limit items that have a current embedding for the
// model and no story yet, oldest first. Each item joins the story whose
// seed is most similar, if that similarity reaches the threshold (ties go
// to the newest story), and otherwise starts a new story that later items
// in the same run can join. Assigned items are not selected again, so
// repeating a run does no work. Items without a current embedding are not
// selected; a failure affects that item only.
func (s *Service) Run(ctx context.Context, limit int) (Result, error) {
	if limit < 1 || limit > MaxLimit {
		return Result{}, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrInvalid, MaxLimit)
	}
	if s.opts.Threshold <= 0 || s.opts.Threshold > 1 || s.opts.Model == "" || s.opts.Dimensions < 1 {
		return Result{}, fmt.Errorf("%w: invalid cluster options %+v", domain.ErrInvalid, s.opts)
	}
	items, err := s.store.ItemsToCluster(ctx, s.opts.Model, s.opts.Dimensions, limit)
	if err != nil {
		return Result{}, err
	}
	seeds, err := s.store.StorySeeds(ctx, s.opts.Model, s.opts.Dimensions, MaxSeeds)
	if err != nil {
		return Result{}, err
	}
	res := Result{Selected: len(items)}
	for _, e := range items {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		seed, joined, err := s.assign(ctx, e, seeds)
		if err != nil {
			res.Failures = append(res.Failures, Failure{ItemID: e.ItemID, Err: err})
			s.logger.Warn("clustering failed", "item_id", e.ItemID, "err", err)
			continue
		}
		if joined {
			res.Joined++
		} else {
			res.Created++
			// Newest first, like StorySeeds.
			seeds = append([]domain.StorySeed{seed}, seeds...)
		}
	}
	return res, nil
}

// assign adds the item to the best matching story or starts a new one. It
// returns the new story's seed when it started one.
func (s *Service) assign(ctx context.Context, e domain.Embedding, seeds []domain.StorySeed) (domain.StorySeed, bool, error) {
	if e.Model != s.opts.Model || e.Dimensions != s.opts.Dimensions {
		return domain.StorySeed{}, false, fmt.Errorf("%w: embedding is %s/%d, want %s/%d",
			domain.ErrInvalid, e.Model, e.Dimensions, s.opts.Model, s.opts.Dimensions)
	}
	if err := e.Validate(); err != nil {
		return domain.StorySeed{}, false, err
	}
	best, bestSim := uuid.Nil, math.Inf(-1)
	for _, seed := range seeds {
		sim, err := domain.CosineSimilarity(e.Vector, seed.Vector)
		if err != nil {
			s.logger.Warn("skipping incomparable story seed", "story_id", seed.StoryID, "err", err)
			continue
		}
		if sim > bestSim {
			best, bestSim = seed.StoryID, sim
		}
	}
	if best != uuid.Nil && bestSim >= s.opts.Threshold {
		// Rounding can push a float32 cosine just past 1.
		return domain.StorySeed{}, true, s.store.AddStoryItem(ctx, best, e.ItemID, s.opts.Model, min(bestSim, 1))
	}
	id, err := s.store.CreateStory(ctx, s.opts.Model, e.ItemID)
	if err != nil {
		return domain.StorySeed{}, false, err
	}
	return domain.StorySeed{StoryID: id, Vector: e.Vector}, false, nil
}
