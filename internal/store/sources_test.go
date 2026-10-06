package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func TestCreateAndGetSource(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	created, err := st.CreateSource(ctx, domain.NewSource{
		Slug:     " arxiv-ml ",
		Name:     "arXiv ML",
		Type:     domain.SourceTypeArxiv,
		URL:      "https://arxiv.org",
		Priority: 5,
		Config:   json.RawMessage(`{"categories": ["cs.LG"]}`),
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if created.ID.Version() != 7 {
		t.Errorf("ID version = %d, want UUIDv7", created.ID.Version())
	}
	if created.Slug != "arxiv-ml" || created.Status != domain.SourceStatusActive || created.Priority != 5 {
		t.Errorf("created = %+v", created)
	}
	if created.MinFetchInterval != domain.DefaultMinFetchInterval {
		t.Errorf("MinFetchInterval = %v, want default", created.MinFetchInterval)
	}
	if string(created.State) != "{}" || created.Health() != domain.HealthUnknown {
		t.Errorf("state=%s health=%s, want {} and unknown", created.State, created.Health())
	}
	var cfg map[string][]string
	if err := json.Unmarshal(created.Config, &cfg); err != nil || cfg["categories"][0] != "cs.LG" {
		t.Errorf("config round-trip = %s (%v)", created.Config, err)
	}
	if created.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", created.CreatedAt.Location())
	}

	byID, err := st.GetSource(ctx, created.ID)
	if err != nil || byID.Slug != "arxiv-ml" {
		t.Errorf("GetSource = %+v, %v", byID, err)
	}
	bySlug, err := st.GetSourceBySlug(ctx, "arxiv-ml")
	if err != nil || bySlug.ID != created.ID {
		t.Errorf("GetSourceBySlug = %+v, %v", bySlug, err)
	}
}

func TestCreateSourceErrors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	_, err := st.CreateSource(ctx, domain.NewSource{Slug: "hn", Name: "again", Type: domain.SourceTypeHackerNews})
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate slug error = %v, want ErrConflict", err)
	}

	_, err = st.CreateSource(ctx, domain.NewSource{Slug: "x", Name: "x", Type: "myspace"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown type error = %v, want ErrInvalid", err)
	}
}

func TestCreateRetiredSourceStampsRetiredAt(t *testing.T) {
	st := newTestStore(t)
	src, err := st.CreateSource(context.Background(), domain.NewSource{
		Slug: "old", Name: "Old", Type: domain.SourceTypeGitHub, Status: domain.SourceStatusRetired,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if src.RetiredAt == nil {
		t.Error("RetiredAt not set for a source created as retired")
	}
}

func TestGetSourceNotFound(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if _, err := st.GetSource(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSource error = %v, want ErrNotFound", err)
	}
	if _, err := st.GetSourceBySlug(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSourceBySlug error = %v, want ErrNotFound", err)
	}
}

func TestListSources(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for _, n := range []domain.NewSource{
		{Slug: "b", Name: "B", Type: domain.SourceTypeArxiv, Priority: 1},
		{Slug: "a", Name: "A", Type: domain.SourceTypeArxiv, Priority: 1},
		{Slug: "top", Name: "Top", Type: domain.SourceTypeGitHub, Priority: 10},
		{Slug: "paused", Name: "Paused", Type: domain.SourceTypeHackerNews, Status: domain.SourceStatusPaused},
	} {
		if _, err := st.CreateSource(ctx, n); err != nil {
			t.Fatalf("CreateSource %s: %v", n.Slug, err)
		}
	}

	all, err := st.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if got := slugs(all); !slices.Equal(got, []string{"top", "a", "b", "paused"}) {
		t.Errorf("order = %v, want priority desc then name", got)
	}

	active, err := st.ListSources(ctx, domain.SourceStatusActive)
	if err != nil {
		t.Fatalf("ListSources(active): %v", err)
	}
	if got := slugs(active); !slices.Equal(got, []string{"top", "a", "b"}) {
		t.Errorf("active = %v", got)
	}

	empty := newTestStore(t)
	none, err := empty.ListSources(ctx)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("empty ListSources = %v, %v; want empty non-nil slice", none, err)
	}
}

func TestUpdateSource(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src, err := st.CreateSource(ctx, domain.NewSource{
		Slug: "gh", Name: "GitHub", Type: domain.SourceTypeGitHub, URL: "https://github.com",
		Priority: 1, Config: json.RawMessage(`{"q":"llm"}`),
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	// Partial update: only the given fields change.
	updated, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{
		Name:             ptr(" GitHub AI "),
		Priority:         ptr(7),
		MinFetchInterval: ptr(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if updated.Name != "GitHub AI" || updated.Priority != 7 || updated.MinFetchInterval != 30*time.Minute {
		t.Errorf("updated = %+v", updated)
	}
	if updated.URL != "https://github.com" || string(updated.Config) != `{"q": "llm"}` {
		t.Errorf("untouched fields changed: url=%q config=%s", updated.URL, updated.Config)
	}
	if !updated.UpdatedAt.After(src.UpdatedAt) && !updated.UpdatedAt.Equal(src.UpdatedAt) {
		t.Errorf("UpdatedAt went backwards")
	}

	// Config replacement and clearing the URL.
	updated, err = st.UpdateSource(ctx, src.ID, domain.SourcePatch{
		Config: ptr(json.RawMessage(`{"q":"agents"}`)),
		URL:    ptr(""),
	})
	if err != nil {
		t.Fatalf("UpdateSource config: %v", err)
	}
	if string(updated.Config) != `{"q": "agents"}` || updated.URL != "" {
		t.Errorf("config=%s url=%q", updated.Config, updated.URL)
	}

	// Retire stamps retired_at; reactivating clears it.
	retired, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{Status: ptr(domain.SourceStatusRetired)})
	if err != nil || retired.Status != domain.SourceStatusRetired || retired.RetiredAt == nil {
		t.Fatalf("retire: %+v, %v", retired, err)
	}
	again, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{Priority: ptr(2)})
	if err != nil || again.RetiredAt == nil || !again.RetiredAt.Equal(*retired.RetiredAt) {
		t.Errorf("unrelated update changed retired_at: %v -> %v (%v)", retired.RetiredAt, again.RetiredAt, err)
	}
	active, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{Status: ptr(domain.SourceStatusActive)})
	if err != nil || active.RetiredAt != nil {
		t.Errorf("reactivate: retired_at=%v err=%v, want nil", active.RetiredAt, err)
	}

	// Empty patch is a read.
	same, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{})
	if err != nil || same.ID != src.ID {
		t.Errorf("empty patch = %+v, %v", same, err)
	}
}

func TestUpdateSourceErrors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	if _, err := st.UpdateSource(ctx, uuid.New(), domain.SourcePatch{Priority: ptr(1)}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing source error = %v, want ErrNotFound", err)
	}
	if _, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{Name: ptr("  ")}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank name error = %v, want ErrInvalid", err)
	}
	if _, err := st.UpdateSource(ctx, src.ID, domain.SourcePatch{Config: ptr(json.RawMessage(`[1]`))}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("array config error = %v, want ErrInvalid", err)
	}
}

func slugs(srcs []domain.Source) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = s.Slug
	}
	return out
}
