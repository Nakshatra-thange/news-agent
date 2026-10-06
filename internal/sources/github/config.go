// Package github defines the GitHub source type. New and rising AI
// repositories are discovered through the GitHub repository search API.
package github

import (
	"encoding/json"
	"strings"
	"time"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// Config configures a GitHub repository-discovery source.
type Config struct {
	// Queries are GitHub search expressions such as "topic:llm" or
	// "agent framework language:python"; each runs as a separate request.
	// Recency and star filters are added from CreatedWithin and MinStars.
	Queries []string `json:"queries"`
	// CreatedWithin limits results to repositories created in this window.
	CreatedWithin sources.Duration `json:"created_within"`
	// MinStars drops repositories with fewer stars.
	MinStars int `json:"min_stars"`
	// Sort orders search results: "stars" or "updated".
	Sort string `json:"sort"`
	// MaxResultsPerQuery caps the repositories requested per query (one page).
	MaxResultsPerQuery int `json:"max_results_per_query"`
}

// Limits.
const (
	maxQueries         = 10
	maxQueryLen        = 256
	minCreatedWithin   = 24 * time.Hour
	maxCreatedWithin   = 365 * 24 * time.Hour
	maxMinStars        = 1_000_000
	maxResultsPerQuery = 100 // GitHub's per_page maximum
)

// reservedQualifiers are set from dedicated config fields; allowing them in
// queries too would produce contradictory searches.
var reservedQualifiers = []string{"created:", "stars:", "sort:"}

// DefaultConfig discovers recently created AI repositories gaining stars.
func DefaultConfig() Config {
	return Config{
		Queries:            []string{"topic:llm", "topic:large-language-models", "topic:ai-agents", "topic:generative-ai"},
		CreatedWithin:      sources.Duration(7 * 24 * time.Hour),
		MinStars:           50,
		Sort:               "stars",
		MaxResultsPerQuery: 30,
	}
}

// ParseConfig decodes raw on top of the defaults and validates the result.
func ParseConfig(raw json.RawMessage) (Config, error) {
	c := DefaultConfig()
	if err := sources.DecodeConfig(raw, &c); err != nil {
		return Config{}, err
	}
	c.Queries = sources.CleanStrings(c.Queries)
	c.Sort = strings.ToLower(strings.TrimSpace(c.Sort))

	var v sources.Checks
	v.Check(len(c.Queries) >= 1 && len(c.Queries) <= maxQueries, "queries", "must contain 1 to %d non-empty queries", maxQueries)
	for _, q := range c.Queries {
		v.Check(len(q) <= maxQueryLen, "queries", "each query must be at most %d characters", maxQueryLen)
		for _, r := range reservedQualifiers {
			v.Check(!strings.Contains(strings.ToLower(q), r), "queries",
				"%q must not use the %s qualifier; use created_within, min_stars or sort instead", q, r)
		}
	}
	v.Check(c.CreatedWithin.D() >= minCreatedWithin && c.CreatedWithin.D() <= maxCreatedWithin, "created_within", "must be between 1d and 365d")
	v.Check(c.MinStars >= 0 && c.MinStars <= maxMinStars, "min_stars", "must be between 0 and %d", maxMinStars)
	v.Check(c.Sort == "stars" || c.Sort == "updated", "sort", `must be "stars" or "updated"`)
	v.Check(c.MaxResultsPerQuery >= 1 && c.MaxResultsPerQuery <= maxResultsPerQuery, "max_results_per_query", "must be between 1 and %d", maxResultsPerQuery)
	if err := v.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Spec describes the GitHub source type.
type Spec struct{}

var _ sources.TypeSpec = Spec{}

func (Spec) Type() domain.SourceType { return domain.SourceTypeGitHub }

func (Spec) Description() string {
	return "Newly created GitHub repositories matching search queries (GitHub search API)"
}

func (Spec) NormalizeConfig(raw json.RawMessage) (json.RawMessage, error) {
	c, err := ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return sources.EncodeConfig(c)
}

// FetchPolicy: unauthenticated search allows 10 requests/minute (30 with a
// token); hourly fetches of a few queries stay well inside that.
func (Spec) FetchPolicy() sources.FetchPolicy {
	return sources.FetchPolicy{DefaultInterval: time.Hour, MinInterval: 10 * time.Minute}
}

func (Spec) Seeds() []domain.NewSource {
	interval := time.Hour
	return []domain.NewSource{{
		Slug:             "github-ai-repos",
		Name:             "GitHub: new AI repositories",
		Type:             domain.SourceTypeGitHub,
		URL:              "https://github.com",
		Priority:         30,
		MinFetchInterval: &interval,
	}}
}
