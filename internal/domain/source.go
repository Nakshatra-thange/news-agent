package domain

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SourceType identifies which adapter fetches a source.
type SourceType string

const (
	SourceTypeGitHub     SourceType = "github"
	SourceTypeHackerNews SourceType = "hackernews"
	SourceTypeArxiv      SourceType = "arxiv"
)

// SourceTypes lists the adapter types known in Phase 1.
var SourceTypes = []SourceType{SourceTypeGitHub, SourceTypeHackerNews, SourceTypeArxiv}

// Valid reports whether t is a known source type.
func (t SourceType) Valid() bool {
	for _, known := range SourceTypes {
		if t == known {
			return true
		}
	}
	return false
}

// SourceStatus is the lifecycle state of a source.
type SourceStatus string

const (
	SourceStatusActive  SourceStatus = "active"  // fetched normally
	SourceStatusPaused  SourceStatus = "paused"  // kept, but not fetched
	SourceStatusRetired SourceStatus = "retired" // permanently out of use; items kept for provenance
)

// Valid reports whether s is a known status.
func (s SourceStatus) Valid() bool {
	switch s {
	case SourceStatusActive, SourceStatusPaused, SourceStatusRetired:
		return true
	}
	return false
}

// Health summarizes recent fetch outcomes for a source.
type Health string

const (
	HealthUnknown  Health = "unknown"  // never fetched
	HealthHealthy  Health = "healthy"  // last fetch succeeded
	HealthDegraded Health = "degraded" // recent failures, below the failing threshold
	HealthFailing  Health = "failing"  // FailingThreshold or more consecutive failures
)

// FailingThreshold is the number of consecutive failures after which a source
// is considered failing rather than degraded.
const FailingThreshold = 3

// Source is a configured instance of an adapter type. There can be several
// sources of the same type with different configuration.
type Source struct {
	ID                  uuid.UUID
	Slug                string
	Name                string
	Type                SourceType
	URL                 string
	Status              SourceStatus
	Priority            int
	Config              json.RawMessage
	State               json.RawMessage
	MinFetchInterval    time.Duration
	LastFetchAt         *time.Time
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	LastError           string
	ConsecutiveFailures int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	RetiredAt           *time.Time
}

// Health derives the source's health from its fetch history.
func (s Source) Health() Health {
	switch {
	case s.ConsecutiveFailures >= FailingThreshold:
		return HealthFailing
	case s.ConsecutiveFailures > 0:
		return HealthDegraded
	case s.LastSuccessAt != nil:
		return HealthHealthy
	default:
		return HealthUnknown
	}
}

// DefaultMinFetchInterval is used when a new source does not specify one.
const DefaultMinFetchInterval = 10 * time.Minute

// maxFetchInterval bounds the interval so it fits the database column.
const maxFetchInterval = time.Duration(math.MaxInt32) * time.Second

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// NewSource is the input for registering a source.
type NewSource struct {
	Slug             string
	Name             string
	Type             SourceType
	URL              string
	Status           SourceStatus // defaults to active
	Priority         int
	Config           json.RawMessage // defaults to {}
	MinFetchInterval *time.Duration  // defaults to DefaultMinFetchInterval
}

// Normalize trims text fields and applies defaults. Call before Validate.
func (n *NewSource) Normalize() {
	n.Slug = strings.TrimSpace(n.Slug)
	n.Name = strings.TrimSpace(n.Name)
	n.URL = strings.TrimSpace(n.URL)
	if n.Status == "" {
		n.Status = SourceStatusActive
	}
	if len(n.Config) == 0 {
		n.Config = json.RawMessage(`{}`)
	}
	if n.MinFetchInterval == nil {
		d := DefaultMinFetchInterval
		n.MinFetchInterval = &d
	}
}

// Validate checks every field and reports all problems together.
func (n NewSource) Validate() error {
	var v validator
	v.check(slugPattern.MatchString(n.Slug), "slug", "must be 1-63 lowercase letters, digits or hyphens, starting with a letter or digit")
	v.check(n.Name != "", "name", "is required")
	v.check(n.Type.Valid(), "type", "must be one of "+joinTypes(SourceTypes))
	v.check(n.URL == "" || isHTTPURL(n.URL), "url", "must be an absolute http(s) URL")
	v.check(n.Status.Valid(), "status", "must be active, paused or retired")
	v.check(isJSONObject(n.Config), "config", "must be a JSON object")
	if n.MinFetchInterval != nil {
		v.check(validInterval(*n.MinFetchInterval), "min_fetch_interval", "must be between 0 and ~68 years")
	}
	return v.err()
}

// SourcePatch is a partial update. Nil fields are left unchanged. Slug and
// type are immutable: they identify the source and its adapter.
type SourcePatch struct {
	Name             *string
	URL              *string
	Status           *SourceStatus
	Priority         *int
	Config           *json.RawMessage
	MinFetchInterval *time.Duration
}

// Normalize trims text fields.
func (p *SourcePatch) Normalize() {
	if p.Name != nil {
		s := strings.TrimSpace(*p.Name)
		p.Name = &s
	}
	if p.URL != nil {
		s := strings.TrimSpace(*p.URL)
		p.URL = &s
	}
}

// Validate checks the fields that are set.
func (p SourcePatch) Validate() error {
	var v validator
	if p.Name != nil {
		v.check(*p.Name != "", "name", "must not be empty")
	}
	if p.URL != nil {
		v.check(*p.URL == "" || isHTTPURL(*p.URL), "url", "must be an absolute http(s) URL")
	}
	if p.Status != nil {
		v.check(p.Status.Valid(), "status", "must be active, paused or retired")
	}
	if p.Config != nil {
		v.check(isJSONObject(*p.Config), "config", "must be a JSON object")
	}
	if p.MinFetchInterval != nil {
		v.check(validInterval(*p.MinFetchInterval), "min_fetch_interval", "must be between 0 and ~68 years")
	}
	return v.err()
}

// IsEmpty reports whether the patch changes nothing.
func (p SourcePatch) IsEmpty() bool {
	return p == SourcePatch{}
}

func validInterval(d time.Duration) bool {
	return d >= 0 && d <= maxFetchInterval
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

func joinTypes(types []SourceType) string {
	s := make([]string, len(types))
	for i, t := range types {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}
