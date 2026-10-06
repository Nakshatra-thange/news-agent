// Package hackernews implements the Hacker News source type using the
// official Hacker News Firebase API (https://github.com/HackerNews/API).
//
// The API has no search: it exposes ranked story-ID lists (top, best, ...)
// and one endpoint per item. The adapter reads the configured lists, fetches
// up to MaxItems stories, and keeps those whose title matches a keyword,
// that have enough points and that are recent enough.
package hackernews

import (
	"encoding/json"
	"time"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// Config configures a Hacker News source.
type Config struct {
	// Queries are keywords matched case-insensitively against story titles,
	// at the start of a word: "LLM" matches "LLMs" and "LLM-based" but not
	// "ALLMEN". A story matching any keyword is kept.
	Queries []string `json:"queries"`
	// MinPoints drops stories with fewer points.
	MinPoints int `json:"min_points"`
	// Lookback drops stories submitted longer ago than this.
	Lookback sources.Duration `json:"lookback"`
	// Lists are the ranked story lists to scan: top, best, new, ask, show.
	Lists []string `json:"lists"`
	// MaxItems caps the stories fetched per run (one request each), which
	// bounds the load a fetch puts on the API.
	MaxItems int `json:"max_items"`
}

// Limits.
const (
	maxQueries  = 20
	maxQueryLen = 100
	maxMinPts   = 10_000
	minLookback = time.Hour
	maxLookback = 30 * 24 * time.Hour
	maxMaxItems = 1000
)

// listEndpoints maps list names to Firebase endpoints.
var listEndpoints = map[string]string{
	"top":  "topstories",
	"best": "beststories",
	"new":  "newstories",
	"ask":  "askstories",
	"show": "showstories",
}

// DefaultConfig targets AI/ML discussion on Hacker News.
func DefaultConfig() Config {
	return Config{
		Queries: []string{
			"LLM", "GPT", "Claude", "OpenAI", "Anthropic",
			"machine learning", "AI agents", "Gemini",
		},
		MinPoints: 30,
		Lookback:  sources.Duration(48 * time.Hour),
		Lists:     []string{"top", "best"},
		MaxItems:  300,
	}
}

// ParseConfig decodes raw on top of the defaults and validates the result.
func ParseConfig(raw json.RawMessage) (Config, error) {
	c := DefaultConfig()
	if err := sources.DecodeConfig(raw, &c); err != nil {
		return Config{}, err
	}
	c.Queries = sources.CleanStrings(c.Queries)
	c.Lists = sources.CleanStrings(c.Lists)

	var v sources.Checks
	v.Check(len(c.Queries) >= 1 && len(c.Queries) <= maxQueries, "queries", "must contain 1 to %d non-empty keywords", maxQueries)
	for _, q := range c.Queries {
		v.Check(len(q) <= maxQueryLen, "queries", "each keyword must be at most %d characters", maxQueryLen)
	}
	v.Check(c.MinPoints >= 0 && c.MinPoints <= maxMinPts, "min_points", "must be between 0 and %d", maxMinPts)
	v.Check(c.Lookback.D() >= minLookback && c.Lookback.D() <= maxLookback, "lookback", "must be between 1h and 30d")
	v.Check(len(c.Lists) >= 1, "lists", "must name at least one list (top, best, new, ask, show)")
	for _, l := range c.Lists {
		_, ok := listEndpoints[l]
		v.Check(ok, "lists", "%q is not a list; use top, best, new, ask or show", l)
	}
	v.Check(c.MaxItems >= 1 && c.MaxItems <= maxMaxItems, "max_items", "must be between 1 and %d", maxMaxItems)
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
	return "Hacker News stories from the official Firebase API, filtered by title keywords, points and age"
}

func (Spec) NormalizeConfig(raw json.RawMessage) (json.RawMessage, error) {
	c, err := ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return sources.EncodeConfig(c)
}

// FetchPolicy: each fetch makes up to max_items+len(lists) small requests,
// so fetches are kept at least 5 minutes apart.
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
