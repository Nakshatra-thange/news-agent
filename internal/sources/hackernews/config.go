// Package hackernews defines the Hacker News source type. Stories are
// discovered through the Algolia HN Search API, one request per query.
package hackernews

import (
	"encoding/json"
	"time"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// Config configures a Hacker News source.
type Config struct {
	// Queries are keyword searches; each runs as a separate request and the
	// results are merged. Example: ["LLM", "Anthropic"].
	Queries []string `json:"queries"`
	// MinPoints drops stories with fewer points.
	MinPoints int `json:"min_points"`
	// Lookback limits results to stories created within this window.
	Lookback sources.Duration `json:"lookback"`
	// MaxResultsPerQuery caps the stories requested per query.
	MaxResultsPerQuery int `json:"max_results_per_query"`
}

// Limits.
const (
	maxQueries         = 10
	maxQueryLen        = 100
	maxMinPoints       = 10_000
	minLookback        = time.Hour
	maxLookback        = 30 * 24 * time.Hour
	maxResultsPerQuery = 200
)

// DefaultConfig targets AI/ML discussion on Hacker News.
func DefaultConfig() Config {
	return Config{
		Queries: []string{
			"LLM", "GPT", "Claude", "OpenAI", "Anthropic",
			"machine learning", "AI agents", "Gemini",
		},
		MinPoints:          30,
		Lookback:           sources.Duration(48 * time.Hour),
		MaxResultsPerQuery: 50,
	}
}

// ParseConfig decodes raw on top of the defaults and validates the result.
func ParseConfig(raw json.RawMessage) (Config, error) {
	c := DefaultConfig()
	if err := sources.DecodeConfig(raw, &c); err != nil {
		return Config{}, err
	}
	c.Queries = sources.CleanStrings(c.Queries)

	var v sources.Checks
	v.Check(len(c.Queries) >= 1 && len(c.Queries) <= maxQueries, "queries", "must contain 1 to %d non-empty queries", maxQueries)
	for _, q := range c.Queries {
		v.Check(len(q) <= maxQueryLen, "queries", "each query must be at most %d characters", maxQueryLen)
	}
	v.Check(c.MinPoints >= 0 && c.MinPoints <= maxMinPoints, "min_points", "must be between 0 and %d", maxMinPoints)
	v.Check(c.Lookback.D() >= minLookback && c.Lookback.D() <= maxLookback, "lookback", "must be between 1h and 30d")
	v.Check(c.MaxResultsPerQuery >= 1 && c.MaxResultsPerQuery <= maxResultsPerQuery, "max_results_per_query", "must be between 1 and %d", maxResultsPerQuery)
	if err := v.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Spec describes the Hacker News source type.
type Spec struct{}

var _ sources.TypeSpec = Spec{}

func (Spec) Type() domain.SourceType { return domain.SourceTypeHackerNews }

func (Spec) Description() string {
	return "Hacker News stories matching keyword queries (Algolia HN Search API)"
}

func (Spec) NormalizeConfig(raw json.RawMessage) (json.RawMessage, error) {
	c, err := ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return sources.EncodeConfig(c)
}

// FetchPolicy: Algolia allows ~10k requests/hour per IP; a few queries every
// few minutes is far below that.
func (Spec) FetchPolicy() sources.FetchPolicy {
	return sources.FetchPolicy{DefaultInterval: 30 * time.Minute, MinInterval: 5 * time.Minute}
}

func (Spec) Seeds() []domain.NewSource {
	interval := 30 * time.Minute
	return []domain.NewSource{{
		Slug:             "hn-ai",
		Name:             "Hacker News: AI",
		Type:             domain.SourceTypeHackerNews,
		URL:              "https://news.ycombinator.com",
		Priority:         50,
		MinFetchInterval: &interval,
	}}
}
