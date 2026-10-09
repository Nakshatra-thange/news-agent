package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// memStore is an in-memory Store. Items are returned in the given order.
type memStore struct {
	items   []domain.Embedding
	seeds   []domain.StorySeed
	members map[uuid.UUID]uuid.UUID // item -> story
	addErr  error
}

func (m *memStore) ItemsToCluster(_ context.Context, _ string, _, limit int) ([]domain.Embedding, error) {
	var out []domain.Embedding
	for _, e := range m.items {
		if _, ok := m.members[e.ItemID]; !ok && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) StorySeeds(context.Context, string, int, int) ([]domain.StorySeed, error) {
	return m.seeds, nil
}

func (m *memStore) CreateStory(_ context.Context, _ string, seed uuid.UUID) (uuid.UUID, error) {
	if _, ok := m.members[seed]; ok {
		return uuid.Nil, domain.ErrConflict
	}
	id := uuid.New()
	m.members[seed] = id
	return id, nil
}

func (m *memStore) AddStoryItem(_ context.Context, story, item uuid.UUID, _ string, sim float64) error {
	if m.addErr != nil {
		return m.addErr
	}
	if _, ok := m.members[item]; ok {
		return domain.ErrConflict
	}
	if sim < -1 || sim > 1 {
		return domain.ErrInvalid
	}
	m.members[item] = story
	return nil
}

var opts = Options{Model: "m", Dimensions: 3, Threshold: 0.8}

func emb(v ...float32) domain.Embedding {
	return domain.Embedding{
		ItemID: uuid.New(), Provider: "fake", Model: "m", Dimensions: len(v), Vector: v,
		ContentHash: make([]byte, domain.HashSize),
	}
}

func TestRunGroupsBySimilarity(t *testing.T) {
	ctx := context.Background()
	a1, a2 := emb(1, 0, 0), emb(0.95, 0.1, 0) // cosine ≈ 0.99
	b1 := emb(0, 1, 0)                        // orthogonal to a1
	a3 := emb(0.7, 0.7, 0)                    // ≈ 0.71 to both seeds: below threshold
	st := &memStore{items: []domain.Embedding{a1, a2, b1, a3}, members: map[uuid.UUID]uuid.UUID{}}
	svc := New(st, opts, nil)

	res, err := svc.Run(ctx, 10)
	if err != nil || res.Selected != 4 || res.Joined != 1 || res.Created != 3 || len(res.Failures) != 0 {
		t.Fatalf("Run = %+v, %v; want 1 joined, 3 new stories", res, err)
	}
	if st.members[a1.ItemID] != st.members[a2.ItemID] {
		t.Error("similar items are in different stories")
	}
	if st.members[a1.ItemID] == st.members[b1.ItemID] || st.members[a1.ItemID] == st.members[a3.ItemID] {
		t.Error("dissimilar items share a story")
	}
	// Repeating the run selects nothing and changes nothing.
	before := len(st.members)
	if res, err := svc.Run(ctx, 10); err != nil || res.Selected != 0 || len(st.members) != before {
		t.Errorf("second run = %+v, %v; want no work", res, err)
	}
}

func TestRunJoinsExistingStoriesAndBreaksTies(t *testing.T) {
	older, newer := uuid.New(), uuid.New()
	item := emb(1, 0, 0)
	st := &memStore{
		items: []domain.Embedding{item},
		// Newest first, as the store returns them; both seeds are identical.
		seeds:   []domain.StorySeed{{StoryID: newer, Vector: []float32{1, 0, 0}}, {StoryID: older, Vector: []float32{1, 0, 0}}},
		members: map[uuid.UUID]uuid.UUID{},
	}
	if res, err := New(st, opts, nil).Run(context.Background(), 10); err != nil || res.Joined != 1 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if st.members[item.ItemID] != newer {
		t.Error("a tie did not go to the newest story")
	}
}

func TestRunHandlesBadInput(t *testing.T) {
	ctx := context.Background()
	wrongDims := emb(1, 0)
	wrongModel := emb(1, 0, 0)
	wrongModel.Model = "other"
	zero := emb(0, 0, 0)
	ok := emb(1, 0, 0)
	st := &memStore{
		items: []domain.Embedding{wrongDims, wrongModel, zero, ok},
		// A seed of another length is skipped, not compared.
		seeds:   []domain.StorySeed{{StoryID: uuid.New(), Vector: []float32{1, 0}}},
		members: map[uuid.UUID]uuid.UUID{},
	}
	res, err := New(st, opts, nil).Run(ctx, 10)
	if err != nil || res.Selected != 4 || res.Created != 1 || len(res.Failures) != 3 {
		t.Fatalf("Run = %+v, %v; want 3 failures and 1 new story", res, err)
	}
	for _, f := range res.Failures[:2] {
		if !errors.Is(f.Err, domain.ErrInvalid) {
			t.Errorf("failure %v, want invalid", f.Err)
		}
	}
	if _, ok := st.members[wrongDims.ItemID]; ok {
		t.Error("an incompatible embedding was clustered")
	}

	// A store error (e.g. a concurrent run took the item) fails that item.
	twin := emb(1, 0, 0)
	st = &memStore{items: []domain.Embedding{twin}, members: map[uuid.UUID]uuid.UUID{},
		seeds: []domain.StorySeed{{StoryID: uuid.New(), Vector: []float32{1, 0, 0}}}, addErr: domain.ErrConflict}
	if res, err := New(st, opts, nil).Run(ctx, 10); err != nil || len(res.Failures) != 1 || !errors.Is(res.Failures[0].Err, domain.ErrConflict) {
		t.Errorf("conflict run = %+v, %v", res, err)
	}
}

func TestRunLimits(t *testing.T) {
	ctx := context.Background()
	st := &memStore{items: []domain.Embedding{emb(1, 0, 0), emb(0, 1, 0), emb(0, 0, 1)}, members: map[uuid.UUID]uuid.UUID{}}
	if res, err := New(st, opts, nil).Run(ctx, 2); err != nil || res.Selected != 2 {
		t.Errorf("limit 2: %+v, %v", res, err)
	}
	for _, limit := range []int{0, MaxLimit + 1} {
		if _, err := New(st, opts, nil).Run(ctx, limit); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("limit %d: %v, want invalid", limit, err)
		}
	}
	for _, o := range []Options{
		{Model: "m", Dimensions: 3, Threshold: 0},
		{Model: "m", Dimensions: 3, Threshold: 1.1},
		{Model: "", Dimensions: 3, Threshold: 0.8},
		{Model: "m", Dimensions: 0, Threshold: 0.8},
	} {
		if _, err := New(st, o, nil).Run(ctx, 1); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("options %+v: %v, want invalid", o, err)
		}
	}
}
