package github

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
	if len(c.Queries) == 0 || c.CreatedWithin.D() != 7*24*time.Hour || c.MinStars != 50 || c.Sort != "stars" || c.MaxResultsPerQuery != 30 {
		t.Errorf("defaults = %+v", c)
	}
}

func TestParseConfigOverrides(t *testing.T) {
	c, err := ParseConfig(json.RawMessage(`{
		"queries": ["agent framework language:python"],
		"created_within": "30d",
		"min_stars": 0,
		"sort": " Updated ",
		"max_results_per_query": 100
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !slices.Equal(c.Queries, []string{"agent framework language:python"}) || c.CreatedWithin.D() != 30*24*time.Hour ||
		c.MinStars != 0 || c.Sort != "updated" || c.MaxResultsPerQuery != 100 {
		t.Errorf("overrides = %+v", c)
	}
}

func TestParseConfigInvalid(t *testing.T) {
	tests := []struct {
		raw, field string
	}{
		{`{"queries": []}`, "config.queries"},
		{`{"queries": ["topic:llm created:>2026-01-01"]}`, "config.queries"},
		{`{"queries": ["topic:llm STARS:>10"]}`, "config.queries"},
		{`{"queries": ["topic:llm sort:stars"]}`, "config.queries"},
		{`{"created_within": "12h"}`, "config.created_within"},
		{`{"created_within": "400d"}`, "config.created_within"},
		{`{"min_stars": -5}`, "config.min_stars"},
		{`{"sort": "forks"}`, "config.sort"},
		{`{"max_results_per_query": 101}`, "config.max_results_per_query"},
		{`{"token": "placeholder"}`, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := ParseConfig(json.RawMessage(tt.raw))
			sourcestest.AssertInvalidField(t, err, tt.field)
		})
	}
}
