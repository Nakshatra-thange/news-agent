package enrich

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// memStore returns fixed items and records stored enrichments.
type memStore struct {
	mu        sync.Mutex
	items     []domain.Item
	stored    map[uuid.UUID]domain.Enrichment
	gotModel  string
	gotPrompt int
	gotLimit  int
}

func (m *memStore) ItemsToEnrich(_ context.Context, model string, promptVersion, limit int) ([]domain.Item, error) {
	m.gotModel, m.gotPrompt, m.gotLimit = model, promptVersion, limit
	return m.items[:min(limit, len(m.items))], nil
}

func (m *memStore) UpsertEnrichment(_ context.Context, e domain.Enrichment) (domain.Enrichment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := e.Validate(); err != nil {
		return e, err
	}
	if m.stored == nil {
		m.stored = map[uuid.UUID]domain.Enrichment{}
	}
	m.stored[e.ItemID] = e
	return e, nil
}

// scripted answers per item title, falling back to the fake provider.
type scripted struct {
	replies map[string]string
	errs    map[string]error
}

func (scripted) Name() string  { return "scripted" }
func (scripted) Model() string { return "scripted-1" }
func (s scripted) Complete(ctx context.Context, p Prompt) (string, error) {
	for title, err := range s.errs {
		if containsLine(p.User, "Title: "+title) {
			return "", err
		}
	}
	for title, reply := range s.replies {
		if containsLine(p.User, "Title: "+title) {
			return reply, nil
		}
	}
	return FakeProvider{}.Complete(ctx, p)
}

func containsLine(s, line string) bool {
	for l := range strings.Lines(s) {
		if strings.TrimSuffix(l, "\n") == line {
			return true
		}
	}
	return false
}

func TestRunRejectsBadLimits(t *testing.T) {
	svc := New(&memStore{}, FakeProvider{}, nil)
	for _, n := range []int{0, -1, MaxLimit + 1} {
		if _, err := svc.Run(context.Background(), n); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Run(limit=%d) error = %v, want ErrInvalid", n, err)
		}
	}
}

func TestRunStoresValidatedEnrichmentsWithProvenance(t *testing.T) {
	a := testItem(domain.ItemKindPaper, "Agents at Scale", "cs.AI")
	b := testItem(domain.ItemKindRepository, "OrgoAI/bops", "ai-agents")
	st := &memStore{items: []domain.Item{a, b}}
	res, err := New(st, FakeProvider{}, nil).Run(context.Background(), 5)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Selected != 2 || res.Enriched != 2 || len(res.Failures) != 0 {
		t.Fatalf("result = %+v, want 2 selected and enriched", res)
	}
	if st.gotModel != "fake-1" || st.gotPrompt != PromptVersion || st.gotLimit != 5 {
		t.Errorf("selection used model=%q prompt=%d limit=%d", st.gotModel, st.gotPrompt, st.gotLimit)
	}
	e := st.stored[a.ID]
	if e.Provider != "fake" || e.Model != "fake-1" || e.PromptVersion != PromptVersion || string(e.ContentHash) != string(a.ContentHash) {
		t.Errorf("provenance = %s/%s/v%d", e.Provider, e.Model, e.PromptVersion)
	}
	if e.Category != "research" || st.stored[b.ID].Category != "tool" {
		t.Errorf("categories = %q, %q", e.Category, st.stored[b.ID].Category)
	}
}

func TestRunIsolatesFailures(t *testing.T) {
	good := testItem(domain.ItemKindPaper, "Good Paper", "cs.AI")
	garbage := testItem(domain.ItemKindPaper, "Garbage Reply")
	outOfRange := testItem(domain.ItemKindPaper, "Out Of Range")
	providerDown := testItem(domain.ItemKindPaper, "Provider Down")
	st := &memStore{items: []domain.Item{garbage, outOfRange, providerDown, good}}
	p := scripted{
		replies: map[string]string{
			"Garbage Reply": "I could not decide.",
			"Out Of Range":  `{"topics":["a"],"entities":[],"importance":9,"category":"research"}`,
		},
		errs: map[string]error{"Provider Down": errors.New("503 from provider")},
	}
	res, err := New(st, p, nil).Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Selected != 4 || res.Enriched != 1 || len(res.Failures) != 3 {
		t.Fatalf("result = %+v, want 1 enriched and 3 failures", res)
	}
	if _, ok := st.stored[good.ID]; !ok || len(st.stored) != 1 {
		t.Errorf("stored %d enrichments, want only the good item", len(st.stored))
	}
	for _, f := range res.Failures[:2] {
		if !errors.Is(f.Err, ErrInvalidOutput) {
			t.Errorf("%s: error %v, want ErrInvalidOutput", f.Item.Title, f.Err)
		}
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	st := &memStore{items: []domain.Item{testItem(domain.ItemKindPaper, "A"), testItem(domain.ItemKindPaper, "B")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := New(st, FakeProvider{}, nil).Run(ctx, 10)
	if !errors.Is(err, context.Canceled) || res.Enriched != 0 {
		t.Errorf("Run on cancelled ctx = %+v, %v", res, err)
	}
}
