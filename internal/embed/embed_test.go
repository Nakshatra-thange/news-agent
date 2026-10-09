package embed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func testItem(title, desc string) domain.Item {
	return domain.Item{ID: uuid.New(), Title: title, Description: desc, ContentHash: make([]byte, domain.HashSize)}
}

func TestInput(t *testing.T) {
	if got := Input(testItem("  Agents at Scale ", " About agents. ")); got != "Agents at Scale\n\nAbout agents." {
		t.Errorf("Input = %q", got)
	}
	if got := Input(testItem("Title only", "  ")); got != "Title only" {
		t.Errorf("Input without description = %q", got)
	}
	long := Input(testItem("T", strings.Repeat("é", 2*maxInputRunes)))
	if n := len([]rune(long)); n != maxInputRunes {
		t.Errorf("long input has %d runes, want %d", n, maxInputRunes)
	}
}

func TestFakeProvider(t *testing.T) {
	ctx := context.Background()
	p := FakeProvider{}
	a, err := p.Embed(ctx, "OpenAI releases GPT model for code")
	if err != nil || len(a) != p.Dimensions() {
		t.Fatalf("Embed = %d values, %v", len(a), err)
	}
	again, _ := p.Embed(ctx, "OpenAI releases GPT model for code")
	if !slices.Equal(a, again) {
		t.Error("fake embeddings are not deterministic")
	}
	similar, _ := p.Embed(ctx, "OpenAI releases a new GPT model for code generation")
	other, _ := p.Embed(ctx, "Protein folding benchmark from a biology lab")
	simSim, _ := domain.CosineSimilarity(a, similar)
	simOther, _ := domain.CosineSimilarity(a, other)
	if simSim <= simOther || simSim < 0.7 {
		t.Errorf("similarity: related %.3f, unrelated %.3f; want related clearly higher", simSim, simOther)
	}
	if self, _ := domain.CosineSimilarity(a, a); self < 0.9999 {
		t.Errorf("self-similarity = %v, want 1", self)
	}
	if _, err := p.Embed(ctx, " -- "); err == nil {
		t.Error("text without words embedded, want an error")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Embed(cancelled, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Embed error = %v", err)
	}
}

// memStore is an in-memory Store.
type memStore struct {
	items  []domain.Item
	stored []domain.Embedding
	limit  int
}

func (m *memStore) ItemsToEmbed(_ context.Context, _ string, _, limit int) ([]domain.Item, error) {
	m.limit = limit
	return m.items[:min(limit, len(m.items))], nil
}

func (m *memStore) UpsertEmbedding(_ context.Context, e domain.Embedding) (domain.Embedding, error) {
	m.stored = append(m.stored, e)
	return e, nil
}

// stubProvider returns a fixed vector or error.
type stubProvider struct {
	vec  []float32
	err  error
	dims int
}

func (stubProvider) Name() string      { return "stub" }
func (stubProvider) Model() string     { return "stub-1" }
func (p stubProvider) Dimensions() int { return p.dims }
func (p stubProvider) Embed(context.Context, string) ([]float32, error) {
	return p.vec, p.err
}

func TestServiceRun(t *testing.T) {
	ctx := context.Background()
	items := []domain.Item{testItem("A", ""), testItem("B", "")}
	tests := []struct {
		name     string
		provider Provider
		embedded int
	}{
		{"valid", stubProvider{vec: []float32{1, 0}, dims: 2}, 2},
		{"provider error", stubProvider{err: errors.New("boom"), dims: 2}, 0},
		{"wrong dimensions", stubProvider{vec: []float32{1, 0, 0}, dims: 2}, 0},
		{"zero vector", stubProvider{vec: []float32{0, 0}, dims: 2}, 0},
	}
	for _, tt := range tests {
		st := &memStore{items: items}
		res, err := New(st, tt.provider, nil).Run(ctx, 10)
		if err != nil || res.Selected != 2 || res.Embedded != tt.embedded || len(res.Failures) != 2-tt.embedded {
			t.Errorf("%s: Run = %+v, %v", tt.name, res, err)
		}
		if len(st.stored) != tt.embedded {
			t.Errorf("%s: stored %d embeddings, want %d", tt.name, len(st.stored), tt.embedded)
		}
	}

	st := &memStore{items: items}
	if _, err := New(st, FakeProvider{}, nil).Run(ctx, 1); err != nil || st.limit != 1 || len(st.stored) != 1 {
		t.Errorf("limit 1: stored %d (limit passed %d, %v)", len(st.stored), st.limit, err)
	}
	if st.stored[0].Provider != "fake" || st.stored[0].Model != "fake-embed-1" || st.stored[0].Dimensions != FakeDimensions {
		t.Errorf("provenance = %+v", st.stored[0])
	}
	for _, limit := range []int{0, MaxLimit + 1} {
		if _, err := New(st, FakeProvider{}, nil).Run(ctx, limit); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Run(limit %d) error = %v, want ErrInvalid", limit, err)
		}
	}
}
