package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// Store is the persistence the registry needs.
type Store interface {
	CreateSource(ctx context.Context, n domain.NewSource) (domain.Source, error)
	GetSource(ctx context.Context, id uuid.UUID) (domain.Source, error)
	GetSourceBySlug(ctx context.Context, slug string) (domain.Source, error)
	ListSources(ctx context.Context, statuses ...domain.SourceStatus) ([]domain.Source, error)
	UpdateSource(ctx context.Context, id uuid.UUID, p domain.SourcePatch) (domain.Source, error)
}

// Registry manages the set of configured sources. It owns the business
// rules: which types exist, how their configuration is validated, how often
// they may be fetched, and which status transitions are allowed.
type Registry struct {
	store  Store
	logger *slog.Logger
	specs  map[domain.SourceType]TypeSpec
	order  []domain.SourceType
}

// NewRegistry builds a registry for the given source types.
func NewRegistry(store Store, logger *slog.Logger, specs ...TypeSpec) (*Registry, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	r := &Registry{store: store, logger: logger, specs: make(map[domain.SourceType]TypeSpec, len(specs))}
	for _, s := range specs {
		t := s.Type()
		if _, dup := r.specs[t]; dup {
			return nil, fmt.Errorf("source type %q registered twice", t)
		}
		if p := s.FetchPolicy(); p.MinInterval <= 0 || p.DefaultInterval < p.MinInterval {
			return nil, fmt.Errorf("source type %q: invalid fetch policy %+v", t, p)
		}
		r.specs[t] = s
		r.order = append(r.order, t)
	}
	return r, nil
}

// Types describes every registered source type, in registration order.
func (r *Registry) Types() []TypeInfo {
	out := make([]TypeInfo, 0, len(r.order))
	for _, t := range r.order {
		s := r.specs[t]
		def, err := s.NormalizeConfig(nil)
		if err != nil {
			// Defaults that fail their own validation are a programming error.
			panic(fmt.Sprintf("source type %q: default config invalid: %v", t, err))
		}
		out = append(out, TypeInfo{Type: t, Description: s.Description(), DefaultConfig: def, FetchPolicy: s.FetchPolicy()})
	}
	return out
}

// ListFilter selects sources. Empty fields mean "any".
type ListFilter struct {
	Statuses []domain.SourceStatus
	Type     domain.SourceType
}

// List returns sources ordered by priority (highest first), then name.
func (r *Registry) List(ctx context.Context, f ListFilter) ([]domain.Source, error) {
	all, err := r.store.ListSources(ctx, f.Statuses...)
	if err != nil {
		return nil, err
	}
	if f.Type == "" {
		return all, nil
	}
	return slices.DeleteFunc(all, func(s domain.Source) bool { return s.Type != f.Type }), nil
}

// Get resolves a source by UUID or slug.
func (r *Registry) Get(ctx context.Context, ref string) (domain.Source, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return r.store.GetSource(ctx, id)
	}
	return r.store.GetSourceBySlug(ctx, ref)
}

// Register validates and stores a new source. Its configuration is
// normalized with the type's defaults, and its fetch interval defaults to
// (and may not undercut) the type's fetch policy.
func (r *Registry) Register(ctx context.Context, n domain.NewSource) (domain.Source, error) {
	spec, ok := r.specs[n.Type]
	if !ok {
		return domain.Source{}, r.unknownType(n.Type)
	}
	if n.MinFetchInterval == nil {
		d := spec.FetchPolicy().DefaultInterval
		n.MinFetchInterval = &d
	}
	n.Normalize()

	// Report base-field and config problems together.
	var fields []domain.FieldError
	fields = appendFieldErrors(fields, n.Validate())
	cfg, err := spec.NormalizeConfig(n.Config)
	fields = appendFieldErrors(fields, err)
	fields = appendFieldErrors(fields, checkInterval(spec, *n.MinFetchInterval))
	if len(fields) > 0 {
		return domain.Source{}, &domain.ValidationError{Fields: fields}
	}
	n.Config = cfg

	src, err := r.store.CreateSource(ctx, n)
	if errors.Is(err, domain.ErrConflict) {
		return domain.Source{}, fmt.Errorf("%w: a source with slug %q already exists", domain.ErrConflict, n.Slug)
	}
	if err != nil {
		return domain.Source{}, err
	}
	r.logger.InfoContext(ctx, "source registered",
		"source_id", src.ID, "slug", src.Slug, "type", src.Type, "status", src.Status, "priority", src.Priority)
	return src, nil
}

// Update applies a partial update to the source identified by ref (UUID or
// slug). A new config replaces the old one entirely and is normalized with
// the type's defaults. Status changes must follow the allowed transitions; a
// retired source only accepts being restored to active.
func (r *Registry) Update(ctx context.Context, ref string, p domain.SourcePatch) (domain.Source, error) {
	cur, err := r.Get(ctx, ref)
	if err != nil {
		return domain.Source{}, err
	}
	spec, ok := r.specs[cur.Type]
	if !ok {
		return domain.Source{}, r.unknownType(cur.Type)
	}

	p.Normalize()
	if err := r.checkStatusChange(cur, p); err != nil {
		return domain.Source{}, err
	}

	var fields []domain.FieldError
	fields = appendFieldErrors(fields, p.Validate())
	if p.Config != nil {
		cfg, err := spec.NormalizeConfig(*p.Config)
		fields = appendFieldErrors(fields, err)
		p.Config = &cfg
	}
	if p.MinFetchInterval != nil {
		fields = appendFieldErrors(fields, checkInterval(spec, *p.MinFetchInterval))
	}
	if len(fields) > 0 {
		return domain.Source{}, &domain.ValidationError{Fields: fields}
	}

	updated, err := r.store.UpdateSource(ctx, cur.ID, p)
	if err != nil {
		return domain.Source{}, err
	}
	attrs := []any{"source_id", updated.ID, "slug", updated.Slug, "changed", changedFields(p)}
	if updated.Status != cur.Status {
		attrs = append(attrs, "status_from", cur.Status, "status_to", updated.Status)
	}
	r.logger.InfoContext(ctx, "source updated", attrs...)
	return updated, nil
}

func (r *Registry) checkStatusChange(cur domain.Source, p domain.SourcePatch) error {
	next := cur.Status
	if p.Status != nil {
		next = *p.Status
	}
	if !next.Valid() {
		return nil // reported by field validation
	}
	if !cur.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: a %s source cannot become %s; restore it to active first", domain.ErrConflict, cur.Status, next)
	}
	// A retired source is frozen: the only accepted change is a bare restore.
	if cur.Status == domain.SourceStatusRetired {
		restoreOnly := domain.SourcePatch{Status: p.Status}
		if p != restoreOnly {
			return fmt.Errorf("%w: source %q is retired; restore it with {\"status\":\"active\"} before changing anything else", domain.ErrConflict, cur.Slug)
		}
	}
	return nil
}

// SeedResult reports what Seed did.
type SeedResult struct {
	Created []domain.Source
	Skipped []string // slugs that already existed
}

// Seed registers each type's recommended default sources. It is idempotent:
// a seed whose slug already exists is left untouched, so user edits survive.
func (r *Registry) Seed(ctx context.Context) (SeedResult, error) {
	var res SeedResult
	for _, t := range r.order {
		for _, n := range r.specs[t].Seeds() {
			_, err := r.store.GetSourceBySlug(ctx, n.Slug)
			switch {
			case err == nil:
				res.Skipped = append(res.Skipped, n.Slug)
				continue
			case !errors.Is(err, domain.ErrNotFound):
				return res, fmt.Errorf("seed %s: %w", n.Slug, err)
			}
			src, err := r.Register(ctx, n)
			if errors.Is(err, domain.ErrConflict) {
				// Created concurrently by another process.
				res.Skipped = append(res.Skipped, n.Slug)
				continue
			}
			if err != nil {
				return res, fmt.Errorf("seed %s: %w", n.Slug, err)
			}
			res.Created = append(res.Created, src)
		}
	}
	r.logger.InfoContext(ctx, "source seeding complete", "created", len(res.Created), "skipped", len(res.Skipped))
	return res, nil
}

func (r *Registry) unknownType(t domain.SourceType) error {
	known := make([]string, len(r.order))
	for i, k := range r.order {
		known[i] = string(k)
	}
	return &domain.ValidationError{Fields: []domain.FieldError{{
		Field: "type", Message: fmt.Sprintf("unknown source type %q (known: %v)", t, known),
	}}}
}

func checkInterval(spec TypeSpec, d time.Duration) error {
	if floor := spec.FetchPolicy().MinInterval; d < floor {
		return &domain.ValidationError{Fields: []domain.FieldError{{
			Field:   "min_fetch_interval",
			Message: fmt.Sprintf("must be at least %s (%d seconds) for %s sources", formatDuration(floor), int64(floor/time.Second), spec.Type()),
		}}}
	}
	return nil
}

// appendFieldErrors merges a validation error's fields into dst. A
// non-validation error is a bug in a TypeSpec, so it is surfaced as a field
// error rather than hidden.
func appendFieldErrors(dst []domain.FieldError, err error) []domain.FieldError {
	if err == nil {
		return dst
	}
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return append(dst, ve.Fields...)
	}
	return append(dst, domain.FieldError{Field: "config", Message: err.Error()})
}

func changedFields(p domain.SourcePatch) []string {
	var out []string
	if p.Name != nil {
		out = append(out, "name")
	}
	if p.URL != nil {
		out = append(out, "url")
	}
	if p.Status != nil {
		out = append(out, "status")
	}
	if p.Priority != nil {
		out = append(out, "priority")
	}
	if p.Config != nil {
		out = append(out, "config")
	}
	if p.MinFetchInterval != nil {
		out = append(out, "min_fetch_interval")
	}
	return out
}
