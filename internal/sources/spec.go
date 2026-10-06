// Package sources is Synergy's source registry: the business rules for
// registering, configuring and managing information sources.
//
// Each source type (hackernews, arxiv, github) lives in its own subpackage
// and describes itself with a TypeSpec: how its configuration is validated
// and defaulted, how often it may be fetched, and which source instances are
// recommended as seeds. Later stages add the fetch adapters alongside.
package sources

import (
	"encoding/json"
	"time"

	"synergy/internal/domain"
)

// TypeSpec describes one source type.
type TypeSpec interface {
	Type() domain.SourceType
	// Description is a one-line, human-readable summary of the type.
	Description() string
	// NormalizeConfig validates raw configuration and returns it in canonical
	// form with every default filled in. Unknown fields are rejected so typos
	// surface immediately. Problems are reported as *domain.ValidationError
	// with fields named "config.<field>".
	NormalizeConfig(raw json.RawMessage) (json.RawMessage, error)
	// FetchPolicy bounds how often sources of this type may be fetched.
	FetchPolicy() FetchPolicy
	// Seeds returns the recommended default sources of this type.
	Seeds() []domain.NewSource
}

// FetchPolicy is a source type's politeness policy toward its upstream API.
type FetchPolicy struct {
	// DefaultInterval is used when a new source does not specify one.
	DefaultInterval time.Duration
	// MinInterval is the shortest min_fetch_interval a source may be given,
	// so a misconfiguration cannot make Synergy hammer an external API.
	MinInterval time.Duration
}

// TypeInfo is the public description of a source type.
type TypeInfo struct {
	Type          domain.SourceType
	Description   string
	DefaultConfig json.RawMessage
	FetchPolicy   FetchPolicy
}
