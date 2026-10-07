package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// fakeItems records the filter it was asked for and returns canned results.
type fakeItems struct {
	page      domain.ItemPage
	item      domain.FeedItem
	err       error
	gotFilter domain.ItemFilter
	calls     int
}

func (f *fakeItems) ListItems(_ context.Context, filter domain.ItemFilter) (domain.ItemPage, error) {
	f.calls++
	f.gotFilter = filter
	return f.page, f.err
}

func (f *fakeItems) GetFeedItem(_ context.Context, id uuid.UUID) (domain.FeedItem, error) {
	f.calls++
	if f.err != nil {
		return domain.FeedItem{}, f.err
	}
	if id != f.item.ID {
		return domain.FeedItem{}, domain.ErrNotFound
	}
	return f.item, nil
}

// slugRegistry resolves sources by slug or UUID from a fixed set.
type slugRegistry struct {
	fakeRegistry
	bySlug map[string]domain.Source
}

func (r *slugRegistry) Get(_ context.Context, ref string) (domain.Source, error) {
	for _, s := range r.bySlug {
		if s.Slug == ref || s.ID.String() == ref {
			return s, nil
		}
	}
	return domain.Source{}, domain.ErrNotFound
}

var (
	hnID    = uuid.MustParse("0199a000-0000-7000-8000-0000000000a1")
	arxivID = uuid.MustParse("0199a000-0000-7000-8000-0000000000a2")
	itemID  = uuid.MustParse("0199a000-0000-7000-8000-0000000000b1")
	dupID   = uuid.MustParse("0199a000-0000-7000-8000-0000000000b2")
)

func sampleFeedItem() domain.FeedItem {
	pub := time.Date(2026, 10, 6, 9, 30, 0, 0, time.UTC)
	disc := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	return domain.FeedItem{
		Item: domain.Item{
			ID: itemID, SourceID: arxivID, ExternalID: "2610.08789", Kind: domain.ItemKindPaper,
			Title: "QF3: Fast Flow RL", Description: "We study...", URL: "https://arxiv.org/abs/2610.08789",
			CanonicalURL: "https://arxiv.org/abs/2610.08789", URLHash: make([]byte, 32), ContentHash: make([]byte, 32),
			Authors: []string{"Ada"}, Tags: []string{"cs.LG"}, Metadata: json.RawMessage(`{"version":"v1"}`),
			PublishedAt: &pub, DiscoveredAt: disc, LastSeenAt: disc, UpdatedAt: disc, FeedAt: pub,
		},
		Source: domain.SourceRef{ID: arxivID, Slug: "arxiv-ai", Name: "arXiv: AI", Type: domain.SourceTypeArxiv},
		AlsoSeenOn: []domain.Sighting{{
			ItemID: dupID, Source: domain.SourceRef{ID: hnID, Slug: "hn-ai", Name: "Hacker News: AI", Type: domain.SourceTypeHackerNews},
			URL: "https://arxiv.org/abs/2610.08789", DiscussionURL: "https://news.ycombinator.com/item?id=1", DiscoveredAt: disc,
		}},
	}
}

func newItemServer(t *testing.T) (*Server, *fakeItems) {
	t.Helper()
	items := &fakeItems{page: domain.ItemPage{Items: []domain.FeedItem{sampleFeedItem()}}, item: sampleFeedItem()}
	reg := &slugRegistry{bySlug: map[string]domain.Source{
		"hn-ai":    {ID: hnID, Slug: "hn-ai", Type: domain.SourceTypeHackerNews},
		"arxiv-ai": {ID: arxivID, Slug: "arxiv-ai", Type: domain.SourceTypeArxiv},
	}}
	return New(Options{Sources: reg, Items: items}), items
}

func TestListItemsResponseShape(t *testing.T) {
	srv, _ := newItemServer(t)
	rec := do(t, srv, http.MethodGet, "/api/v1/items", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if keys := sortedKeys(raw); !slices.Equal(keys, []string{"count", "has_more", "items", "next_cursor"}) {
		t.Errorf("top-level keys = %v", keys)
	}
	if string(raw["next_cursor"]) != "null" || string(raw["has_more"]) != "false" || string(raw["count"]) != "1" {
		t.Errorf("pagination = %s %s %s", raw["next_cursor"], raw["has_more"], raw["count"])
	}

	var items []map[string]json.RawMessage
	_ = json.Unmarshal(raw["items"], &items)
	want := []string{
		"also_seen_on", "authors", "canonical_url", "description", "discovered_at", "discussion_url", "duplicate_of",
		"external_id", "feed_at", "id", "kind", "last_seen_at", "metadata", "published_at", "source", "tags",
		"title", "updated_at", "url",
	}
	if keys := sortedKeys(items[0]); !slices.Equal(keys, want) {
		t.Errorf("item keys = %v\nwant       %v", keys, want)
	}
	for _, internal := range []string{"url_hash", "content_hash", "source_id"} {
		if _, ok := items[0][internal]; ok {
			t.Errorf("internal field %s exposed", internal)
		}
	}

	type out struct {
		Items []itemJSON `json:"items"`
	}
	it := decode[out](t, rec).Items[0]
	if it.Source.Slug != "arxiv-ai" || it.Source.Type != "arxiv" || it.Source.Name != "arXiv: AI" || it.Source.ID != arxivID {
		t.Errorf("source = %+v", it.Source)
	}
	if len(it.AlsoSeenOn) != 1 || it.AlsoSeenOn[0].Source.Slug != "hn-ai" || it.AlsoSeenOn[0].ItemID != dupID {
		t.Errorf("also_seen_on = %+v", it.AlsoSeenOn)
	}
	if it.PublishedAt == nil || !it.FeedAt.Equal(*it.PublishedAt) || it.DuplicateOf != nil {
		t.Errorf("times/duplicate = %+v", it)
	}
}

func TestListItemsEmptyFeed(t *testing.T) {
	srv, items := newItemServer(t)
	items.page = domain.ItemPage{Items: []domain.FeedItem{}}
	rec := do(t, srv, http.MethodGet, "/api/v1/items", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"items":[],"count":0,"has_more":false,"next_cursor":null}` {
		t.Errorf("empty feed = %d %s", rec.Code, rec.Body)
	}
}

func TestItemJSONNormalizesNilCollections(t *testing.T) {
	f := sampleFeedItem()
	f.Authors, f.Tags, f.Metadata, f.AlsoSeenOn, f.PublishedAt = nil, nil, nil, nil, nil
	b, _ := json.Marshal(toItemJSON(f))
	for _, want := range []string{`"authors":[]`, `"tags":[]`, `"metadata":{}`, `"also_seen_on":[]`, `"published_at":null`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing from %s", want, b)
		}
	}
}

func TestListItemsDefaultFilter(t *testing.T) {
	srv, items := newItemServer(t)
	do(t, srv, http.MethodGet, "/api/v1/items", nil)
	f := items.gotFilter
	if f.Limit != domain.DefaultItemLimit || f.IncludeDuplicates || f.After != nil ||
		!slices.Equal(f.SourceStatuses, []domain.SourceStatus{domain.SourceStatusActive}) {
		t.Errorf("default filter = %+v; want active sources, no duplicates, default limit", f)
	}
}

func TestListItemsFilterMapping(t *testing.T) {
	day := func(d int) *time.Time { t := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC); return &t }
	tests := []struct {
		name  string
		query string
		check func(domain.ItemFilter) bool
	}{
		{"limit", "limit=7", func(f domain.ItemFilter) bool { return f.Limit == 7 }},
		{"max limit", "limit=200", func(f domain.ItemFilter) bool { return f.Limit == 200 }},
		{"sources by slug and UUID, deduplicated", "source=hn-ai,arxiv-ai&source=" + hnID.String(),
			func(f domain.ItemFilter) bool { return slices.Equal(f.SourceIDs, []uuid.UUID{hnID, arxivID}) }},
		{"source types", "source_type=arxiv&source_type=github",
			func(f domain.ItemFilter) bool {
				return slices.Equal(f.SourceTypes, []domain.SourceType{domain.SourceTypeArxiv, domain.SourceTypeGitHub})
			}},
		{"statuses", "source_status=active,paused",
			func(f domain.ItemFilter) bool {
				return slices.Equal(f.SourceStatuses, []domain.SourceStatus{domain.SourceStatusActive, domain.SourceStatusPaused})
			}},
		{"all statuses", "source_status=all", func(f domain.ItemFilter) bool { return f.SourceStatuses == nil }},
		{"kinds", "kind=paper,repository",
			func(f domain.ItemFilter) bool {
				return slices.Equal(f.Kinds, []domain.ItemKind{domain.ItemKindPaper, domain.ItemKindRepository})
			}},
		{"tags", "tag=cs.LG&tag=cs.AI", func(f domain.ItemFilter) bool { return slices.Equal(f.Tags, []string{"cs.LG", "cs.AI"}) }},
		{"feed time range with dates", "since=2026-10-01&until=2026-10-07",
			func(f domain.ItemFilter) bool { return f.Since.Equal(*day(1)) && f.Until.Equal(*day(7)) }},
		{"RFC 3339 with offset is normalized to UTC", "since=2026-10-07T05:30:00%2B05:30",
			func(f domain.ItemFilter) bool { return f.Since.Equal(*day(7)) && f.Since.Location() == time.UTC }},
		{"discovered range", "discovered_since=2026-10-02&discovered_until=2026-10-03",
			func(f domain.ItemFilter) bool {
				return f.DiscoveredSince.Equal(*day(2)) && f.DiscoveredUntil.Equal(*day(3))
			}},
		{"search", "q=%20flow%20matching%20", func(f domain.ItemFilter) bool { return f.Query == "flow matching" }},
		{"duplicates", "include_duplicates=true", func(f domain.ItemFilter) bool { return f.IncludeDuplicates }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, items := newItemServer(t)
			rec := do(t, srv, http.MethodGet, "/api/v1/items?"+tt.query, nil)
			if rec.Code != http.StatusOK || !tt.check(items.gotFilter) {
				t.Errorf("status %d, filter %+v (%s)", rec.Code, items.gotFilter, rec.Body)
			}
		})
	}
}

func TestListItemsInvalidQuery(t *testing.T) {
	many := strings.TrimSuffix(strings.Repeat("x,", 21), ",")
	for i := range 21 {
		many = strings.Replace(many, "x", string(rune('a'+i)), 1)
	}
	tests := []struct{ query, field string }{
		{"limit=0", "limit"},
		{"limit=201", "limit"},
		{"limit=-1", "limit"},
		{"limit=ten", "limit"},
		{"limit=5&limit=6", "limit"},
		{"page=2", "page"},
		{"offset=50", "offset"},
		{"sort=stars", "sort"},
		{"source=nope", "source"},
		{"source_type=reddit", "source_type"},
		{"source_status=deleted", "source_status"},
		{"kind=video", "kind"},
		{"tag=" + strings.Repeat("t", 101), "tag"},
		{"tag=" + many, "tag"},
		{"since=yesterday", "since"},
		{"until=2026-13-01", "until"},
		{"since=2026-10-07&until=2026-10-07", "until"},
		{"discovered_since=2026-10-07&discovered_until=2026-10-01", "discovered_until"},
		{"q=" + strings.Repeat("q", 201), "q"},
		{"include_duplicates=maybe", "include_duplicates"},
		{"cursor=%%%", "query"},
		{"kind=paper&source=%zz", "query"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			srv, items := newItemServer(t)
			rec := do(t, srv, http.MethodGet, "/api/v1/items?"+tt.query, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Code != codeInvalidQuery || len(body.Error.Details) == 0 || body.Error.Details[0].Field != tt.field {
				t.Errorf("error = %+v, want invalid_query on %s", body.Error, tt.field)
			}
			if items.calls != 0 {
				t.Error("the store must not be queried for an invalid request")
			}
		})
	}
}

func TestListItemsReportsAllProblems(t *testing.T) {
	srv, _ := newItemServer(t)
	rec := do(t, srv, http.MethodGet, "/api/v1/items?limit=0&kind=video&bogus=1", nil)
	if body := decode[errorBody](t, rec); len(body.Error.Details) != 3 {
		t.Errorf("details = %+v, want all three problems", body.Error.Details)
	}
}

func TestListItemsCursorRoundTrip(t *testing.T) {
	srv, items := newItemServer(t)
	next := domain.ItemCursor{FeedAt: time.Date(2026, 10, 6, 9, 30, 0, 123456000, time.UTC), ID: itemID}
	items.page.Next = &next

	const query = "/api/v1/items?source=arxiv-ai&kind=paper&limit=1"
	rec := do(t, srv, http.MethodGet, query, nil)
	page := decode[itemListJSON](t, rec)
	if !page.HasMore || page.NextCursor == nil {
		t.Fatalf("page = %+v, want has_more and a cursor", page)
	}
	cursor := *page.NextCursor
	if strings.Contains(cursor, itemID.String()) || strings.ContainsAny(cursor, "+/=") {
		t.Errorf("cursor %q should be opaque and URL-safe", cursor)
	}

	// Same filters (written differently) and a different limit: accepted.
	items.page.Next = nil
	rec = do(t, srv, http.MethodGet, "/api/v1/items?kind=paper&source="+arxivID.String()+"&limit=5&cursor="+cursor, nil)
	if rec.Code != http.StatusOK || items.gotFilter.After == nil || *items.gotFilter.After != next {
		t.Fatalf("status %d, after = %+v, want %+v", rec.Code, items.gotFilter.After, next)
	}
	if page := decode[itemListJSON](t, rec); page.HasMore || page.NextCursor != nil {
		t.Errorf("last page = %+v", page)
	}
}

func TestListItemsRejectsBadCursors(t *testing.T) {
	srv, items := newItemServer(t)
	next := domain.ItemCursor{FeedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), ID: itemID}
	items.page.Next = &next
	cursor := *decode[itemListJSON](t, do(t, srv, http.MethodGet, "/api/v1/items?kind=paper", nil)).NextCursor

	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct{ name, query, msg string }{
		{"garbage", "kind=paper&cursor=not-a-cursor", "not a valid cursor"},
		{"not JSON", "kind=paper&cursor=" + enc("hello"), "not a valid cursor"},
		{"unknown version", "kind=paper&cursor=" + enc(`{"v":2,"t":1,"id":"`+itemID.String()+`","f":""}`), "not a valid cursor"},
		{"nil id", "kind=paper&cursor=" + enc(`{"v":1,"t":1,"id":"`+uuid.Nil.String()+`","f":""}`), "not a valid cursor"},
		{"extra fields", "kind=paper&cursor=" + enc(`{"v":1,"t":1,"id":"`+itemID.String()+`","f":"","x":1}`), "not a valid cursor"},
		{"too long", "kind=paper&cursor=" + strings.Repeat("A", 600), "not a valid cursor"},
		{"different filters", "kind=repository&cursor=" + cursor, "different filters"},
		{"filters dropped", "cursor=" + cursor, "different filters"},
		{"duplicates toggled", "kind=paper&include_duplicates=true&cursor=" + cursor, "different filters"},
		{"tampered fingerprint", "kind=paper&cursor=" + tamper(t, cursor), "different filters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := items.calls
			rec := do(t, srv, http.MethodGet, "/api/v1/items?"+tt.query, nil)
			body := decode[errorBody](t, rec)
			if rec.Code != http.StatusBadRequest || len(body.Error.Details) != 1 || body.Error.Details[0].Field != "cursor" ||
				!strings.Contains(body.Error.Details[0].Message, tt.msg) {
				t.Errorf("status %d, error %+v", rec.Code, body.Error)
			}
			if items.calls != before {
				t.Error("store queried with a bad cursor")
			}
		})
	}
}

func tamper(t *testing.T, cursor string) string {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var c cursorJSON
	_ = json.Unmarshal(b, &c)
	c.Filter = "0000000000000000"
	b, _ = json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestFingerprintIsCanonical(t *testing.T) {
	fp := func(q string) string {
		v, _ := url.ParseQuery(q)
		fq, fields := parseFeedQuery(v)
		if len(fields) > 0 {
			t.Fatalf("%s: %v", q, fields)
		}
		return fq.fingerprint()
	}
	same := [][2]string{
		{"kind=paper,repository", "kind=repository&kind=paper"},
		{"tag=a,b", "tag=b&tag=a"},
		{"", "source_status=active"},
		{"limit=5", "limit=50"},
		{"since=2026-10-07", "since=2026-10-07T05:30:00%2B05:30"},
	}
	for _, p := range same {
		if fp(p[0]) != fp(p[1]) {
			t.Errorf("%q and %q should share a fingerprint", p[0], p[1])
		}
	}
	differ := [][2]string{
		{"", "kind=paper"},
		{"q=llm", "q=llms"},
		{"", "source_status=all"},
		{"since=2026-10-07", "discovered_since=2026-10-07"},
	}
	for _, p := range differ {
		if fp(p[0]) == fp(p[1]) {
			t.Errorf("%q and %q must not share a fingerprint", p[0], p[1])
		}
	}
}

func TestListItemsStoreError(t *testing.T) {
	srv, items := newItemServer(t)
	items.err = errors.New("connection reset by peer")
	rec := do(t, srv, http.MethodGet, "/api/v1/items", nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("status %d body %s; internals must not leak", rec.Code, rec.Body)
	}
}

func TestGetItem(t *testing.T) {
	srv, _ := newItemServer(t)
	tests := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/items/" + itemID.String(), http.StatusOK, ""},
		{"/api/v1/items/" + uuid.NewString(), http.StatusNotFound, codeNotFound},
		{"/api/v1/items/not-a-uuid", http.StatusBadRequest, codeInvalidID},
		{"/api/v1/items/2610.08789", http.StatusBadRequest, codeInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, tt.path, nil)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.code != "" {
				if body := decode[errorBody](t, rec); body.Error.Code != tt.code {
					t.Errorf("code = %s, want %s", body.Error.Code, tt.code)
				}
				return
			}
			it := decode[itemJSON](t, rec)
			if it.ID != itemID || it.Source.Slug != "arxiv-ai" || len(it.AlsoSeenOn) != 1 {
				t.Errorf("item = %+v", it)
			}
		})
	}
}

func TestItemRoutes(t *testing.T) {
	srv, _ := newItemServer(t)
	if rec := do(t, srv, http.MethodPost, "/api/v1/items", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /items = %d, want 405 (read-only API)", rec.Code)
	}
	if rec := do(t, srv, http.MethodDelete, "/api/v1/items/"+itemID.String(), nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /items/{id} = %d, want 405", rec.Code)
	}
	if rec := do(t, New(Options{}), http.MethodGet, "/api/v1/items", nil); rec.Code != http.StatusNotFound {
		t.Errorf("feed without an item reader = %d, want 404", rec.Code)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
