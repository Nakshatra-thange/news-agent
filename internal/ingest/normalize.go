package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"synergy/internal/canon"
	"synergy/internal/domain"
)

// Size limits applied during normalization. Text is truncated to fit; lists
// are capped; anything that cannot be repaired rejects the candidate.
const (
	maxExternalIDLen  = 512
	maxTitleLen       = 1000
	maxDescriptionLen = 20_000
	maxURLLen         = 4096
	maxNameLen        = 200
	maxAuthors        = 100
	maxTags           = 50
	maxMetadataBytes  = 64 << 10
)

// Plausible publication dates. Anything outside is treated as unknown rather
// than trusted: feeds sometimes carry clock skew or placeholder dates.
var (
	earliestPublished = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	futureTolerance   = 24 * time.Hour
)

// Rejection explains why a candidate was not stored.
type Rejection struct {
	ExternalID string
	Reason     string
}

// Normalize turns an adapter's candidate into a validated NewItem: text is
// cleaned and bounded, the URL canonicalized and hashed, the content hashed,
// lists deduplicated, metadata sanitized for PostgreSQL. It returns an error
// describing why the candidate cannot be stored.
func Normalize(sourceID uuid.UUID, c domain.Candidate, now time.Time) (domain.NewItem, error) {
	n := domain.NewItem{
		SourceID:    sourceID,
		ExternalID:  canon.Text(c.ExternalID),
		Kind:        c.Kind,
		Title:       canon.Truncate(canon.Text(c.Title), maxTitleLen),
		Description: canon.Truncate(canon.Text(c.Description), maxDescriptionLen),
		URL:         strings.TrimSpace(c.URL),
		Authors:     cleanList(c.Authors, maxAuthors),
		Tags:        cleanList(c.Tags, maxTags),
		PublishedAt: plausibleTime(c.PublishedAt, now),
	}

	switch {
	case n.ExternalID == "":
		return n, errors.New("missing external_id")
	case len(n.ExternalID) > maxExternalIDLen:
		return n, fmt.Errorf("external_id longer than %d bytes", maxExternalIDLen)
	case !n.Kind.Valid():
		return n, fmt.Errorf("unknown kind %q", n.Kind)
	case n.Title == "":
		return n, errors.New("missing title")
	case len(n.URL) > maxURLLen:
		return n, fmt.Errorf("url longer than %d bytes", maxURLLen)
	}

	canonical, err := canon.Canonicalize(n.URL)
	if err != nil {
		return n, err
	}
	n.CanonicalURL = canonical
	n.URLHash = canon.URLHash(canonical)
	n.ContentHash = canon.ContentHash(n.Title, n.Description)

	// An unusable discussion link is dropped, not fatal.
	if d := strings.TrimSpace(c.DiscussionURL); d != "" && len(d) <= maxURLLen {
		if _, err := canon.Canonicalize(d); err == nil {
			n.DiscussionURL = d
		}
	}

	meta, err := sanitizeMetadata(c.Metadata)
	if err != nil {
		return n, err
	}
	n.Metadata = meta

	if err := n.Validate(); err != nil {
		return n, err
	}
	return n, nil
}

// normalizeBatch normalizes every candidate. Repeats of an external ID
// (e.g. the same story matched by two queries) are folded into the first
// occurrence. It returns the storable items, the rejections, and how many
// repeats were folded.
func normalizeBatch(sourceID uuid.UUID, cands []domain.Candidate, now time.Time) ([]domain.NewItem, []Rejection, int) {
	items := make([]domain.NewItem, 0, len(cands))
	var rejected []Rejection
	seen := make(map[string]bool, len(cands))
	folded := 0
	for _, c := range cands {
		id := canon.Text(c.ExternalID)
		if id != "" && seen[id] {
			folded++
			continue
		}
		seen[id] = true
		n, err := Normalize(sourceID, c, now)
		if err != nil {
			rejected = append(rejected, Rejection{ExternalID: id, Reason: err.Error()})
			continue
		}
		items = append(items, n)
	}
	return items, rejected, folded
}

// cleanList normalizes, deduplicates and caps a list of short strings.
func cleanList(in []string, limit int) []string {
	out := make([]string, 0, min(len(in), limit))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = canon.Truncate(canon.Text(s), maxNameLen)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == limit {
			break
		}
	}
	return out
}

func plausibleTime(t *time.Time, now time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	if u.Before(earliestPublished) || u.After(now.Add(futureTolerance)) {
		return nil
	}
	return &u
}

// sanitizeMetadata requires a JSON object and strips what PostgreSQL's jsonb
// rejects (NUL characters, invalid UTF-8) from every string in it. Numbers
// keep their exact textual form.
func sanitizeMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(raw) > maxMetadataBytes {
		return nil, fmt.Errorf("metadata larger than %d bytes", maxMetadataBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("metadata must be a JSON object")
	}
	out, err := json.Marshal(cleanJSON(obj))
	if err != nil {
		return nil, fmt.Errorf("re-encode metadata: %w", err)
	}
	return out, nil
}

func cleanJSON(v any) any {
	switch x := v.(type) {
	case string:
		return cleanString(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[cleanString(k)] = cleanJSON(val)
		}
		return out
	case []any:
		for i := range x {
			x[i] = cleanJSON(x[i])
		}
		return x
	default:
		return v
	}
}

func cleanString(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
}
