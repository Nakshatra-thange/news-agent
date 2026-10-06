// Package arxiv defines the arXiv source type. Papers are discovered through
// the official arXiv Atom API, newest submissions first.
package arxiv

import (
	"encoding/json"
	"regexp"
	"time"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// Config configures an arXiv source.
type Config struct {
	// Categories are arXiv subject classes such as "cs.LG" or "stat.ML";
	// papers in any of them are included.
	Categories []string `json:"categories"`
	// MaxResults caps the papers requested per fetch.
	MaxResults int `json:"max_results"`
	// Lookback limits results to papers submitted within this window. It
	// defaults to 3 days so weekend gaps in arXiv announcements are covered.
	Lookback sources.Duration `json:"lookback"`
}

// Limits.
const (
	maxCategories = 20
	maxResults    = 500 // arXiv allows more per call, but asks clients to be modest
	minLookback   = time.Hour
	maxLookback   = 30 * 24 * time.Hour
)

// categoryPattern matches arXiv classes: "cs.AI", "stat.ML", "q-bio.NC",
// "physics.comp-ph", or archive-only classes such as "hep-th".
var categoryPattern = regexp.MustCompile(`^[a-z][a-z-]*(\.[A-Za-z][A-Za-z-]*)?$`)

// DefaultConfig covers AI, machine learning and NLP.
func DefaultConfig() Config {
	return Config{
		Categories: []string{"cs.AI", "cs.LG", "cs.CL"},
		MaxResults: 100,
		Lookback:   sources.Duration(72 * time.Hour),
	}
}

// ParseConfig decodes raw on top of the defaults and validates the result.
func ParseConfig(raw json.RawMessage) (Config, error) {
	c := DefaultConfig()
	if err := sources.DecodeConfig(raw, &c); err != nil {
		return Config{}, err
	}
	c.Categories = sources.CleanStrings(c.Categories)

	var v sources.Checks
	v.Check(len(c.Categories) >= 1 && len(c.Categories) <= maxCategories, "categories", "must contain 1 to %d categories", maxCategories)
	for _, cat := range c.Categories {
		v.Check(categoryPattern.MatchString(cat), "categories", "%q is not an arXiv category such as cs.LG", cat)
	}
	v.Check(c.MaxResults >= 1 && c.MaxResults <= maxResults, "max_results", "must be between 1 and %d", maxResults)
	v.Check(c.Lookback.D() >= minLookback && c.Lookback.D() <= maxLookback, "lookback", "must be between 1h and 30d")
	if err := v.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Spec describes the arXiv source type.
type Spec struct{}

var _ sources.TypeSpec = Spec{}

func (Spec) Type() domain.SourceType { return domain.SourceTypeArxiv }

func (Spec) Description() string {
	return "arXiv papers in selected subject categories (arXiv Atom API)"
}

func (Spec) NormalizeConfig(raw json.RawMessage) (json.RawMessage, error) {
	c, err := ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return sources.EncodeConfig(c)
}

// FetchPolicy: arXiv announces new papers once per weekday and asks API
// clients to space requests at least 3 seconds apart, so frequent fetches
// gain nothing.
func (Spec) FetchPolicy() sources.FetchPolicy {
	return sources.FetchPolicy{DefaultInterval: 3 * time.Hour, MinInterval: 30 * time.Minute}
}

func (Spec) Seeds() []domain.NewSource {
	interval := 3 * time.Hour
	return []domain.NewSource{{
		Slug:             "arxiv-ai",
		Name:             "arXiv: AI, ML & NLP",
		Type:             domain.SourceTypeArxiv,
		URL:              "https://arxiv.org",
		Priority:         40,
		MinFetchInterval: &interval,
	}}
}
