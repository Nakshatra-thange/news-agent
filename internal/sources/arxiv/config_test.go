package arxiv

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"synergy/internal/sources/sourcestest"
)

func TestConformance(t *testing.T) {
	sourcestest.Conformance(t, Spec{})
}

func TestParseConfigDefaults(t *testing.T) {
	c, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !slices.Equal(c.Categories, []string{"cs.AI", "cs.LG", "cs.CL"}) || c.MaxResults != 100 || c.Lookback.D() != 72*time.Hour {
		t.Errorf("defaults = %+v", c)
	}
}

func TestCategoryFormats(t *testing.T) {
	valid := []string{"cs.AI", "cs.LG", "stat.ML", "q-bio.NC", "physics.comp-ph", "hep-th", "math.OC"}
	raw, _ := json.Marshal(map[string]any{"categories": valid})
	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig(%v): %v", valid, err)
	}
	if !slices.Equal(c.Categories, valid) {
		t.Errorf("categories = %v", c.Categories)
	}

	for _, bad := range []string{"CS.AI", "cs.", ".AI", "cs AI", "cs.AI OR cs.LG", "cs.AI;"} {
		raw, _ := json.Marshal(map[string]any{"categories": []string{bad}})
		_, err := ParseConfig(raw)
		sourcestest.AssertInvalidField(t, err, "config.categories")
	}
}

func TestParseConfigInvalid(t *testing.T) {
	tests := []struct {
		raw, field string
	}{
		{`{"categories": []}`, "config.categories"},
		{`{"max_results": 0}`, "config.max_results"},
		{`{"max_results": 501}`, "config.max_results"},
		{`{"lookback": "10m"}`, "config.lookback"},
		{`{"category": ["cs.AI"]}`, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := ParseConfig(json.RawMessage(tt.raw))
			sourcestest.AssertInvalidField(t, err, tt.field)
		})
	}
}
