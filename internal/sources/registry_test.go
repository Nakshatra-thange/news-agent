package sources_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/sources/sourcestest"
)

// fakeStore is an in-memory sources.Store mirroring the PostgreSQL store's
// semantics closely enough for registry tests.
type fakeStore struct {
	byID    map[uuid.UUID]domain.Source
	failGet error
}

func newFakeStore() *fakeStore { return &fakeStore{byID: map[uuid.UUID]domain.Source{}} }

func (f *fakeStore) CreateSource(_ context.Context, n domain.NewSource) (domain.Source, error) {
	n.Normalize()
	if err := n.Validate(); err != nil {
		return domain.Source{}, err
	}
	for _, s := range f.byID {
		if s.Slug == n.Slug {
			return domain.Source{}, fmt.Errorf("%w: slug taken", domain.ErrConflict)
		}
	}
	now := time.Now().UTC()
	s := domain.Source{
		ID: uuid.New(), Slug: n.Slug, Name: n.Name, Type: n.Type, URL: n.URL, Status: n.Status,
		Priority: n.Priority, Config: n.Config, State: json.RawMessage(`{}`),
		MinFetchInterval: *n.MinFetchInterval, CreatedAt: now, UpdatedAt: now,
	}
	if s.Status == domain.SourceStatusRetired {
		s.RetiredAt = &now
	}
	f.byID[s.ID] = s
	return s, nil
}

func (f *fakeStore) GetSource(_ context.Context, id uuid.UUID) (domain.Source, error) {
	if f.failGet != nil {
		return domain.Source{}, f.failGet
	}
	s, ok := f.byID[id]
	if !ok {
		return domain.Source{}, domain.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) GetSourceBySlug(_ context.Context, slug string) (domain.Source, error) {
	if f.failGet != nil {
		return domain.Source{}, f.failGet
	}
	for _, s := range f.byID {
		if s.Slug == slug {
			return s, nil
		}
	}
	return domain.Source{}, domain.ErrNotFound
}

func (f *fakeStore) ListSources(_ context.Context, statuses ...domain.SourceStatus) ([]domain.Source, error) {
	out := []domain.Source{}
	for _, s := range f.byID {
		if len(statuses) == 0 || slices.Contains(statuses, s.Status) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b domain.Source) int {
		return cmp.Or(cmp.Compare(b.Priority, a.Priority), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func (f *fakeStore) UpdateSource(_ context.Context, id uuid.UUID, p domain.SourcePatch) (domain.Source, error) {
	s, ok := f.byID[id]
	if !ok {
		return domain.Source{}, domain.ErrNotFound
	}
	if p.Name != nil {
		s.Name = *p.Name
	}
	if p.URL != nil {
		s.URL = *p.URL
	}
	if p.Status != nil {
		s.Status = *p.Status
	}
	if p.Priority != nil {
		s.Priority = *p.Priority
	}
	if p.Config != nil {
		s.Config = *p.Config
	}
	if p.MinFetchInterval != nil {
		s.MinFetchInterval = *p.MinFetchInterval
	}
	if s.Status == domain.SourceStatusRetired && s.RetiredAt == nil {
		now := time.Now().UTC()
		s.RetiredAt = &now
	} else if s.Status != domain.SourceStatusRetired {
		s.RetiredAt = nil
	}
	f.byID[id] = s
	return s, nil
}

func ptr[T any](v T) *T { return &v }

func newRegistry(t *testing.T, st sources.Store) *sources.Registry {
	t.Helper()
	r, err := sources.NewRegistry(st, nil, hackernews.Spec{}, arxiv.Spec{}, github.Spec{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestNewRegistryRejectsDuplicateTypes(t *testing.T) {
	if _, err := sources.NewRegistry(newFakeStore(), nil, arxiv.Spec{}, arxiv.Spec{}); err == nil {
		t.Error("expected error for duplicate source type")
	}
}

func TestTypes(t *testing.T) {
	types := newRegistry(t, newFakeStore()).Types()
	got := make([]domain.SourceType, len(types))
	for i, ti := range types {
		got[i] = ti.Type
		if ti.Description == "" || len(ti.DefaultConfig) == 0 || ti.FetchPolicy.MinInterval <= 0 {
			t.Errorf("incomplete type info: %+v", ti)
		}
	}
	want := []domain.SourceType{domain.SourceTypeHackerNews, domain.SourceTypeArxiv, domain.SourceTypeGitHub}
	if !slices.Equal(got, want) {
		t.Errorf("types = %v, want %v", got, want)
	}
}

func TestRegisterAppliesTypeDefaults(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(t, newFakeStore())

	src, err := r.Register(ctx, domain.NewSource{Slug: "papers", Name: "Papers", Type: domain.SourceTypeArxiv})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if src.MinFetchInterval != (arxiv.Spec{}).FetchPolicy().DefaultInterval {
		t.Errorf("MinFetchInterval = %v, want arXiv default", src.MinFetchInterval)
	}
	cfg, err := arxiv.ParseConfig(src.Config)
	if err != nil {
		t.Fatalf("stored config does not parse: %v", err)
	}
	if !slices.Equal(cfg.Categories, arxiv.DefaultConfig().Categories) {
		t.Errorf("stored config = %s, want defaults filled in", src.Config)
	}
}

func TestRegisterNormalizesPartialConfig(t *testing.T) {
	r := newRegistry(t, newFakeStore())
	src, err := r.Register(context.Background(), domain.NewSource{
		Slug: "hn-rust", Name: "HN Rust", Type: domain.SourceTypeHackerNews,
		Config: json.RawMessage(`{"queries": ["Rust ML", " Rust ML "]}`),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cfg, _ := hackernews.ParseConfig(src.Config)
	if !slices.Equal(cfg.Queries, []string{"Rust ML"}) || cfg.MinPoints != hackernews.DefaultConfig().MinPoints {
		t.Errorf("config = %s", src.Config)
	}
}

func TestRegisterValidation(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(t, newFakeStore())

	t.Run("unknown type", func(t *testing.T) {
		_, err := r.Register(ctx, domain.NewSource{Slug: "x", Name: "X", Type: "reddit"})
		sourcestest.AssertInvalidField(t, err, "type")
	})
	t.Run("all problems reported together", func(t *testing.T) {
		_, err := r.Register(ctx, domain.NewSource{
			Slug: "Bad Slug", Name: "", Type: domain.SourceTypeGitHub,
			Config: json.RawMessage(`{"sort": "forks"}`), MinFetchInterval: ptr(time.Minute),
		})
		for _, f := range []string{"slug", "name", "config.sort", "min_fetch_interval"} {
			sourcestest.AssertInvalidField(t, err, f)
		}
	})
	t.Run("interval below type minimum", func(t *testing.T) {
		_, err := r.Register(ctx, domain.NewSource{Slug: "fast", Name: "Fast", Type: domain.SourceTypeArxiv, MinFetchInterval: ptr(time.Minute)})
		sourcestest.AssertInvalidField(t, err, "min_fetch_interval")
	})
	t.Run("duplicate slug", func(t *testing.T) {
		if _, err := r.Register(ctx, domain.NewSource{Slug: "dup", Name: "A", Type: domain.SourceTypeArxiv}); err != nil {
			t.Fatal(err)
		}
		_, err := r.Register(ctx, domain.NewSource{Slug: "dup", Name: "B", Type: domain.SourceTypeArxiv})
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("error = %v, want ErrConflict", err)
		}
	})
}

func TestGetByIDOrSlug(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(t, newFakeStore())
	src, _ := r.Register(ctx, domain.NewSource{Slug: "hn", Name: "HN", Type: domain.SourceTypeHackerNews})

	for _, ref := range []string{src.ID.String(), "hn"} {
		got, err := r.Get(ctx, ref)
		if err != nil || got.ID != src.ID {
			t.Errorf("Get(%q) = %v, %v", ref, got.ID, err)
		}
	}
	if _, err := r.Get(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := r.Get(ctx, uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(unknown uuid) error = %v, want ErrNotFound", err)
	}
}

func TestListFilters(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(t, newFakeStore())
	for _, n := range []domain.NewSource{
		{Slug: "hn", Name: "HN", Type: domain.SourceTypeHackerNews, Priority: 5},
		{Slug: "ax", Name: "AX", Type: domain.SourceTypeArxiv, Priority: 9},
		{Slug: "gh", Name: "GH", Type: domain.SourceTypeGitHub, Status: domain.SourceStatusPaused},
	} {
		if _, err := r.Register(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		f    sources.ListFilter
		want []string
	}{
		{"all by priority", sources.ListFilter{}, []string{"ax", "hn", "gh"}},
		{"active", sources.ListFilter{Statuses: []domain.SourceStatus{domain.SourceStatusActive}}, []string{"ax", "hn"}},
		{"by type", sources.ListFilter{Type: domain.SourceTypeGitHub}, []string{"gh"}},
		{"type and status", sources.ListFilter{Type: domain.SourceTypeGitHub, Statuses: []domain.SourceStatus{domain.SourceStatusActive}}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.List(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			slugs := []string{}
			for _, s := range got {
				slugs = append(slugs, s.Slug)
			}
			if !slices.Equal(slugs, tt.want) {
				t.Errorf("got %v, want %v", slugs, tt.want)
			}
		})
	}
}

func TestUpdateConfigIsValidatedAgainstSourceType(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(t, newFakeStore())
	src, _ := r.Register(ctx, domain.NewSource{Slug: "gh", Name: "GH", Type: domain.SourceTypeGitHub})

	// A valid replacement is normalized with defaults.
	upd, err := r.Update(ctx, "gh", domain.SourcePatch{Config: ptr(json.RawMessage(`{"min_stars": 500}`))})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg, _ := github.ParseConfig(upd.Config)
	if cfg.MinStars != 500 || cfg.Sort != "stars" {
		t.Errorf("config = %s", upd.Config)
	}

	// Another type's config shape is rejected (unknown field).
	_, err = r.Update(ctx, src.ID.String(), domain.SourcePatch{Config: ptr(json.RawMessage(`{"categories": ["cs.AI"]}`))})
	sourcestest.AssertInvalidField(t, err, "config")

	_, err = r.Update(ctx, "gh", domain.SourcePatch{MinFetchInterval: ptr(time.Minute)})
	sourcestest.AssertInvalidField(t, err, "min_fetch_interval")

	if _, err := r.Update(ctx, "nope", domain.SourcePatch{Priority: ptr(1)}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown ref error = %v, want ErrNotFound", err)
	}
}

func TestStatusTransitions(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(t, newFakeStore())
	if _, err := r.Register(ctx, domain.NewSource{Slug: "hn", Name: "HN", Type: domain.SourceTypeHackerNews}); err != nil {
		t.Fatal(err)
	}
	status := func(s domain.SourceStatus) domain.SourcePatch { return domain.SourcePatch{Status: &s} }

	steps := []struct {
		name    string
		patch   domain.SourcePatch
		want    domain.SourceStatus
		wantErr error
	}{
		{"pause", status(domain.SourceStatusPaused), domain.SourceStatusPaused, nil},
		{"resume", status(domain.SourceStatusActive), domain.SourceStatusActive, nil},
		{"pause again", status(domain.SourceStatusPaused), domain.SourceStatusPaused, nil},
		{"retire from paused", status(domain.SourceStatusRetired), domain.SourceStatusRetired, nil},
		{"retired cannot be paused", status(domain.SourceStatusPaused), "", domain.ErrConflict},
		{"retired cannot be edited", domain.SourcePatch{Priority: ptr(3)}, "", domain.ErrConflict},
		{"restore with other changes rejected", domain.SourcePatch{Status: ptr(domain.SourceStatusActive), Priority: ptr(3)}, "", domain.ErrConflict},
		{"retire again is a no-op", status(domain.SourceStatusRetired), domain.SourceStatusRetired, nil},
		{"restore", status(domain.SourceStatusActive), domain.SourceStatusActive, nil},
		{"edit after restore", domain.SourcePatch{Priority: ptr(3)}, domain.SourceStatusActive, nil},
		{"unknown status", status("deleted"), "", domain.ErrInvalid},
	}
	for _, st := range steps {
		got, err := r.Update(ctx, "hn", st.patch)
		if st.wantErr != nil {
			if !errors.Is(err, st.wantErr) {
				t.Fatalf("%s: error = %v, want %v", st.name, err, st.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if got.Status != st.want {
			t.Fatalf("%s: status = %s, want %s", st.name, got.Status, st.want)
		}
		if (got.Status == domain.SourceStatusRetired) != (got.RetiredAt != nil) {
			t.Fatalf("%s: retired_at = %v inconsistent with status %s", st.name, got.RetiredAt, got.Status)
		}
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	r := newRegistry(t, st)

	res, err := r.Seed(ctx)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	created := []string{}
	for _, s := range res.Created {
		created = append(created, s.Slug)
	}
	if !slices.Equal(created, []string{"hn-ai", "arxiv-ai", "github-ai-repos"}) || len(res.Skipped) != 0 {
		t.Fatalf("first seed: created=%v skipped=%v", created, res.Skipped)
	}

	// User edits survive a re-seed.
	if _, err := r.Update(ctx, "hn-ai", domain.SourcePatch{Priority: ptr(99)}); err != nil {
		t.Fatal(err)
	}
	res, err = r.Seed(ctx)
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(res.Created) != 0 || len(res.Skipped) != 3 {
		t.Errorf("second seed: created=%d skipped=%v", len(res.Created), res.Skipped)
	}
	hn, _ := r.Get(ctx, "hn-ai")
	if hn.Priority != 99 {
		t.Errorf("re-seed overwrote user edit: priority = %d", hn.Priority)
	}
}

func TestSeedPropagatesStoreErrors(t *testing.T) {
	st := newFakeStore()
	st.failGet = errors.New("connection reset")
	if _, err := newRegistry(t, st).Seed(context.Background()); err == nil {
		t.Error("Seed succeeded despite store failure")
	}
}
