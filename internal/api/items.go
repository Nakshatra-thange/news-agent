package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// ItemReader is what the feed endpoints need from storage.
type ItemReader interface {
	ListItems(ctx context.Context, f domain.ItemFilter) (domain.ItemPage, error)
	GetFeedItem(ctx context.Context, id uuid.UUID) (domain.FeedItem, error)
}

// ---- Response representations ----

type itemJSON struct {
	ID            uuid.UUID       `json:"id"`
	Source        itemSourceJSON  `json:"source"`
	ExternalID    string          `json:"external_id"`
	Kind          string          `json:"kind"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	URL           string          `json:"url"`
	CanonicalURL  string          `json:"canonical_url"`
	DiscussionURL string          `json:"discussion_url"`
	Authors       []string        `json:"authors"`
	Tags          []string        `json:"tags"`
	Metadata      json.RawMessage `json:"metadata"`
	PublishedAt   *time.Time      `json:"published_at"`
	DiscoveredAt  time.Time       `json:"discovered_at"`
	// FeedAt is the feed ordering time: published_at, else discovered_at.
	FeedAt      time.Time      `json:"feed_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	LastSeenAt  time.Time      `json:"last_seen_at"`
	DuplicateOf *uuid.UUID     `json:"duplicate_of"`
	AlsoSeenOn  []sightingJSON `json:"also_seen_on"`
}

type itemSourceJSON struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
	Type string    `json:"type"`
}

type sightingJSON struct {
	ItemID        uuid.UUID      `json:"item_id"`
	Source        itemSourceJSON `json:"source"`
	URL           string         `json:"url"`
	DiscussionURL string         `json:"discussion_url"`
	DiscoveredAt  time.Time      `json:"discovered_at"`
}

type itemListJSON struct {
	Items      []itemJSON `json:"items"`
	Count      int        `json:"count"`
	HasMore    bool       `json:"has_more"`
	NextCursor *string    `json:"next_cursor"`
}

func toSourceRefJSON(s domain.SourceRef) itemSourceJSON {
	return itemSourceJSON{ID: s.ID, Slug: s.Slug, Name: s.Name, Type: string(s.Type)}
}

func toItemJSON(f domain.FeedItem) itemJSON {
	meta := f.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	out := itemJSON{
		ID: f.ID, Source: toSourceRefJSON(f.Source), ExternalID: f.ExternalID, Kind: string(f.Kind),
		Title: f.Title, Description: f.Description, URL: f.URL, CanonicalURL: f.CanonicalURL,
		DiscussionURL: f.DiscussionURL, Authors: orEmpty(f.Authors), Tags: orEmpty(f.Tags), Metadata: meta,
		PublishedAt: f.PublishedAt, DiscoveredAt: f.DiscoveredAt, FeedAt: f.FeedAt,
		UpdatedAt: f.UpdatedAt, LastSeenAt: f.LastSeenAt, DuplicateOf: f.DuplicateOf,
		AlsoSeenOn: make([]sightingJSON, len(f.AlsoSeenOn)),
	}
	for i, s := range f.AlsoSeenOn {
		out.AlsoSeenOn[i] = sightingJSON{
			ItemID: s.ItemID, Source: toSourceRefJSON(s.Source), URL: s.URL,
			DiscussionURL: s.DiscussionURL, DiscoveredAt: s.DiscoveredAt,
		}
	}
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- Handlers ----

// handleListItems serves the feed: items newest first by feed time
// (published, else discovered), then by ID, with keyset pagination.
//
// Query parameters (list parameters are repeatable or comma-separated):
//
//	limit               1-200, default 50
//	cursor              next_cursor from the previous page (same filters)
//	source              source slugs or UUIDs
//	source_type         hackernews, arxiv, github
//	source_status       active, paused, retired or all; default active
//	kind                paper, repository, discussion, release
//	tag                 every given tag must be present
//	since, until        feed time range [since, until); RFC 3339 or YYYY-MM-DD
//	discovered_since,
//	discovered_until    discovery time range, same format
//	q                   full-text search over title and description
//	include_duplicates  true to include cross-source duplicates; default false
func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	// r.URL.Query() silently drops malformed pairs; a dropped filter would
	// widen the feed, so parse strictly.
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeQueryError(w, "query", "is not a valid URL query string (check percent-encoding)")
		return
	}
	fq, fields := parseFeedQuery(values)
	if len(fields) == 0 {
		fields = s.resolveSources(r.Context(), &fq)
	}
	if len(fields) == 0 && fq.cursor != "" {
		after, err := decodeCursor(fq.cursor, fq.fingerprint())
		if err != nil {
			fields = append(fields, domain.FieldError{Field: "cursor", Message: err.Error()})
		}
		fq.filter.After = after
	}
	if len(fields) > 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
			Code: codeInvalidQuery, Message: "invalid query parameters", Details: fields,
		}})
		return
	}

	page, err := s.items.ListItems(r.Context(), fq.filter)
	if err != nil {
		s.writeServiceError(w, r, "item", err)
		return
	}
	out := itemListJSON{Items: make([]itemJSON, len(page.Items)), Count: len(page.Items), HasMore: page.Next != nil}
	for i, it := range page.Items {
		out.Items[i] = toItemJSON(it)
	}
	if page.Next != nil {
		c := encodeCursor(*page.Next, fq.fingerprint())
		out.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetItem returns one item, whatever its source's status and whether
// or not it is a duplicate.
func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidID, "item id must be a UUID")
		return
	}
	it, err := s.items.GetFeedItem(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, r, "item", err)
		return
	}
	writeJSON(w, http.StatusOK, toItemJSON(it))
}

// ---- Query parsing ----

// Feed query limits.
const (
	maxListValues = 20
	maxTagLen     = 100
	maxSearchLen  = 200
)

// feedQuery is a parsed feed request. sourceRefs are resolved to
// filter.SourceIDs by resolveSources.
type feedQuery struct {
	filter     domain.ItemFilter
	sourceRefs []string
	cursor     string
}

var feedParams = []string{
	"limit", "cursor", "source", "source_type", "source_status", "kind", "tag",
	"since", "until", "discovered_since", "discovered_until", "q", "include_duplicates",
}

var listParams = []string{"source", "source_type", "source_status", "kind", "tag"}

func parseFeedQuery(q url.Values) (feedQuery, []domain.FieldError) {
	var (
		fq     feedQuery
		fields []domain.FieldError
	)
	bad := func(field, format string, args ...any) {
		fields = append(fields, domain.FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	// Reject unknown and repeated scalar parameters so typos never silently
	// widen the feed.
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		switch {
		case !slices.Contains(feedParams, name):
			bad(name, "unsupported query parameter; supported: %s", strings.Join(feedParams, ", "))
		case len(q[name]) > 1 && !slices.Contains(listParams, name):
			bad(name, "must be given at most once")
		}
	}

	f := &fq.filter
	f.Limit = domain.DefaultItemLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > domain.MaxItemLimit {
			bad("limit", "must be an integer between 1 and %d", domain.MaxItemLimit)
		} else {
			f.Limit = n
		}
	}
	fq.cursor = q.Get("cursor")

	fq.sourceRefs = listValues(q, "source", bad)
	for _, v := range listValues(q, "source_type", bad) {
		if !domain.SourceType(v).Valid() {
			bad("source_type", "unknown source type %s", quote(v))
		}
		f.SourceTypes = append(f.SourceTypes, domain.SourceType(v))
	}
	all := false
	for _, v := range listValues(q, "source_status", bad) {
		switch {
		case v == "all":
			all = true
		case domain.SourceStatus(v).Valid():
			f.SourceStatuses = append(f.SourceStatuses, domain.SourceStatus(v))
		default:
			bad("source_status", "must be active, paused, retired or all; got %s", quote(v))
		}
	}
	switch {
	case all:
		f.SourceStatuses = nil
	case len(f.SourceStatuses) == 0:
		f.SourceStatuses = []domain.SourceStatus{domain.SourceStatusActive}
	}
	for _, v := range listValues(q, "kind", bad) {
		if !domain.ItemKind(v).Valid() {
			bad("kind", "unknown kind %s", quote(v))
		}
		f.Kinds = append(f.Kinds, domain.ItemKind(v))
	}
	for _, v := range listValues(q, "tag", bad) {
		if len(v) > maxTagLen {
			bad("tag", "each tag must be at most %d characters", maxTagLen)
		}
		f.Tags = append(f.Tags, v)
	}

	f.Since = parseTimeParam(q, "since", bad)
	f.Until = parseTimeParam(q, "until", bad)
	f.DiscoveredSince = parseTimeParam(q, "discovered_since", bad)
	f.DiscoveredUntil = parseTimeParam(q, "discovered_until", bad)
	if f.Since != nil && f.Until != nil && !f.Since.Before(*f.Until) {
		bad("until", "must be after since")
	}
	if f.DiscoveredSince != nil && f.DiscoveredUntil != nil && !f.DiscoveredSince.Before(*f.DiscoveredUntil) {
		bad("discovered_until", "must be after discovered_since")
	}

	f.Query = strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(f.Query) > maxSearchLen {
		bad("q", "must be at most %d characters", maxSearchLen)
	}
	if v := q.Get("include_duplicates"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			bad("include_duplicates", "must be true or false")
		}
		f.IncludeDuplicates = b
	}
	return fq, fields
}

// listValues collects a list parameter's values from repeats and commas,
// trimmed, without empties or repeats.
func listValues(q url.Values, name string, bad func(string, string, ...any)) []string {
	var out []string
	for _, raw := range q[name] {
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	if len(out) > maxListValues {
		bad(name, "at most %d values", maxListValues)
		out = out[:maxListValues]
	}
	return out
}

// parseTimeParam accepts RFC 3339 timestamps or dates (midnight UTC).
func parseTimeParam(q url.Values, name string, bad func(string, string, ...any)) *time.Time {
	v := q.Get(name)
	if v == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			t = t.UTC()
			return &t
		}
	}
	bad(name, "must be an RFC 3339 timestamp (2026-10-07T12:00:00Z) or a date (2026-10-07)")
	return nil
}

// resolveSources turns source slugs/UUIDs into IDs, reporting unknown ones.
// The number of refs is bounded by maxListValues.
func (s *Server) resolveSources(ctx context.Context, fq *feedQuery) []domain.FieldError {
	var fields []domain.FieldError
	for _, ref := range fq.sourceRefs {
		src, err := s.sources.Get(ctx, ref)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			fields = append(fields, domain.FieldError{Field: "source", Message: "unknown source " + quote(ref)})
		case err != nil:
			fields = append(fields, domain.FieldError{Field: "source", Message: "could not look up source " + quote(ref)})
		case !slices.Contains(fq.filter.SourceIDs, src.ID):
			fq.filter.SourceIDs = append(fq.filter.SourceIDs, src.ID)
		}
	}
	return fields
}

// ---- Cursors ----

// A cursor is opaque to clients: base64url JSON holding the last item's
// position and a fingerprint of the filters it was issued for. Reusing it
// with different filters is rejected rather than returning a confusing page.
type cursorJSON struct {
	V      int       `json:"v"`
	FeedAt int64     `json:"t"` // Unix microseconds, PostgreSQL's precision
	ID     uuid.UUID `json:"id"`
	Filter string    `json:"f"`
}

const cursorVersion = 1

func encodeCursor(c domain.ItemCursor, fingerprint string) string {
	b, _ := json.Marshal(cursorJSON{V: cursorVersion, FeedAt: c.FeedAt.UnixMicro(), ID: c.ID, Filter: fingerprint})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s, fingerprint string) (*domain.ItemCursor, error) {
	invalid := errors.New("is not a valid cursor; use next_cursor from a previous response")
	if len(s) > 512 {
		return nil, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, invalid
	}
	var c cursorJSON
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || c.V != cursorVersion || c.ID == uuid.Nil || c.FeedAt <= 0 {
		return nil, invalid
	}
	if c.Filter != fingerprint {
		return nil, errors.New("was issued for different filters; restart from the first page")
	}
	return &domain.ItemCursor{FeedAt: time.UnixMicro(c.FeedAt).UTC(), ID: c.ID}, nil
}

// fingerprint identifies the result set a cursor belongs to: every filter
// except the page size and position, in canonical form.
func (fq feedQuery) fingerprint() string {
	f := fq.filter
	sorted := func(in []string) string {
		s := slices.Clone(in)
		slices.Sort(s)
		return strings.Join(s, ",")
	}
	ids := make([]string, len(f.SourceIDs))
	for i, id := range f.SourceIDs {
		ids[i] = id.String()
	}
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return strconv.FormatInt(t.UnixMicro(), 10)
	}
	parts := []string{
		sorted(ids), sorted(strs(f.SourceTypes)), sorted(strs(f.SourceStatuses)), sorted(strs(f.Kinds)), sorted(f.Tags),
		ts(f.Since), ts(f.Until), ts(f.DiscoveredSince), ts(f.DiscoveredUntil), f.Query, strconv.FormatBool(f.IncludeDuplicates),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}
