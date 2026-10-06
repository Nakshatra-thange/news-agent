package hackernews

import (
	"encoding/json"
	"slices"
	"strings"
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
	if len(c.Queries) == 0 || c.MinPoints != 30 || c.Lookback.D() != 48*time.Hour || c.MaxResultsPerQuery != 50 {
		t.Errorf("defaults = %+v", c)
	}
}

func TestParseConfigOverrides(t *testing.T) {
	c, err := ParseConfig(json.RawMessage(`{
		"queries": [" LLM ", "", "LLM", "Mistral"],
		"min_points": 0,
		"lookback": "7d",
		"max_results_per_query": 200
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !slices.Equal(c.Queries, []string{"LLM", "Mistral"}) {
		t.Errorf("queries = %q, want trimmed and deduplicated", c.Queries)
	}
	if c.MinPoints != 0 || c.Lookback.D() != 7*24*time.Hour || c.MaxResultsPerQuery != 200 {
		t.Errorf("overrides = %+v", c)
	}
}

func TestParseConfigPartialKeepsOtherDefaults(t *testing.T) {
	c, err := ParseConfig(json.RawMessage(`{"min_points": 100}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if c.MinPoints != 100 || !slices.Equal(c.Queries, DefaultConfig().Queries) {
		t.Errorf("partial config = %+v", c)
	}
}

func TestParseConfigInvalid(t *testing.T) {
	tests := []struct {
		raw, field string
	}{
		{`{"queries": []}`, "config.queries"},
		{`{"queries": ["  "]}`, "config.queries"},
		{`{"queries": ["a","b","c","d","e","f","g","h","i","j","k"]}`, "config.queries"},
		{`{"queries": ["` + strings.Repeat("x", 101) + `"]}`, "config.queries"},
		{`{"min_points": -1}`, "config.min_points"},
		{`{"lookback": "30m"}`, "config.lookback"},
		{`{"lookback": "31d"}`, "config.lookback"},
		{`{"lookback": "soon"}`, "config"},
		{`{"lookback": 48}`, "config"},
		{`{"max_results_per_query": 0}`, "config.max_results_per_query"},
		{`{"max_results_per_query": 201}`, "config.max_results_per_query"},
		{`{"min_points": "30"}`, "config"},
		{`{"querys": ["typo"]}`, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := ParseConfig(json.RawMessage(tt.raw))
			sourcestest.AssertInvalidField(t, err, tt.field)
		})
	}
}
