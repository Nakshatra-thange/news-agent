// Package sourcestest provides a conformance suite every sources.TypeSpec
// must pass. Each source type package calls Conformance from its tests.
package sourcestest

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// Conformance checks the TypeSpec contract: valid defaults, idempotent
// normalization, strict decoding, a sane fetch policy, and valid seeds.
func Conformance(t *testing.T, spec sources.TypeSpec) {
	t.Helper()

	t.Run("type and description", func(t *testing.T) {
		if !spec.Type().Valid() {
			t.Errorf("Type() = %q is not a known domain.SourceType", spec.Type())
		}
		if strings.TrimSpace(spec.Description()) == "" {
			t.Error("Description() is empty")
		}
	})

	t.Run("defaults are valid and complete", func(t *testing.T) {
		for _, raw := range []string{"", "{}", "  {}  "} {
			cfg, err := spec.NormalizeConfig(json.RawMessage(raw))
			if err != nil {
				t.Fatalf("NormalizeConfig(%q): %v", raw, err)
			}
			var m map[string]any
			if err := json.Unmarshal(cfg, &m); err != nil || len(m) == 0 {
				t.Errorf("NormalizeConfig(%q) = %s, want a non-empty object", raw, cfg)
			}
		}
	})

	t.Run("normalization is idempotent", func(t *testing.T) {
		once, err := spec.NormalizeConfig(nil)
		if err != nil {
			t.Fatal(err)
		}
		twice, err := spec.NormalizeConfig(once)
		if err != nil {
			t.Fatalf("re-normalizing defaults: %v", err)
		}
		if !bytes.Equal(once, twice) {
			t.Errorf("not idempotent:\n once: %s\ntwice: %s", once, twice)
		}
	})

	t.Run("rejects malformed config", func(t *testing.T) {
		for _, raw := range []string{`[]`, `"x"`, `null`, `{`, `{"unknown_field_xyz": 1}`, `{} {}`} {
			_, err := spec.NormalizeConfig(json.RawMessage(raw))
			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("NormalizeConfig(%s) error = %v, want a ValidationError", raw, err)
				continue
			}
			for _, f := range ve.Fields {
				if !strings.HasPrefix(f.Field, "config") {
					t.Errorf("NormalizeConfig(%s): field %q should be config-scoped", raw, f.Field)
				}
			}
		}
	})

	t.Run("fetch policy", func(t *testing.T) {
		p := spec.FetchPolicy()
		if p.MinInterval <= 0 || p.DefaultInterval < p.MinInterval {
			t.Errorf("FetchPolicy = %+v, want 0 < MinInterval <= DefaultInterval", p)
		}
	})

	t.Run("seeds", func(t *testing.T) {
		seeds := spec.Seeds()
		if len(seeds) == 0 {
			t.Fatal("no seed sources")
		}
		for _, n := range seeds {
			if n.Type != spec.Type() {
				t.Errorf("seed %q has type %q, want %q", n.Slug, n.Type, spec.Type())
			}
			if _, err := spec.NormalizeConfig(n.Config); err != nil {
				t.Errorf("seed %q config invalid: %v", n.Slug, err)
			}
			if n.MinFetchInterval != nil && *n.MinFetchInterval < spec.FetchPolicy().MinInterval {
				t.Errorf("seed %q interval %v below the type minimum", n.Slug, *n.MinFetchInterval)
			}
			n.Normalize()
			if err := n.Validate(); err != nil {
				t.Errorf("seed %q invalid: %v", n.Slug, err)
			}
		}
	})
}

// AssertInvalidField fails unless err is a ValidationError mentioning field.
func AssertInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want ValidationError for %q", err, field)
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return
		}
	}
	t.Errorf("error %v does not mention %q", err, field)
}
