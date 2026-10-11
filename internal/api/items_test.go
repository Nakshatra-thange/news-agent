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
		"external_id", "feed_at", "feedback", "id", "kind", "last_seen_at", "metadata", "published_at", "source", "summary",
		"tags", "title", "updated_at", "url",
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
	if string(items[0]["summary"]) != "null" {
		t.Errorf("summary = %s, want null when the item has none", items[0]["summary"])
	}
}

func TestItemSummaryJSON(t *testing.T) {
	srv, items := newItemServer(t)
	at := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	items.page.Items[0].Summary = &domain.Summary{
		Text: "A lab released an open model. The authors report faster inference.", Provider: "anthropic", Model: "claude-opus-5-5", UpdatedAt: at,
	}
	rec := do(t, srv, http.MethodGet, "/api/v1/items", nil)
	var body struct {
		Items []struct {
			Summary map[string]json.RawMessage `json:"summary"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	got := body.Items[0].Summary
	if keys := sortedKeys(got); !slices.Equal(keys, []string{"generated_at", "model", "provider", "text"}) {
		t.Errorf("summary keys = %v", keys)
	}
	if string(got["text"]) != `"A lab released an open model. The authors report faster inference."` ||
		string(got["provider"]) != `"anthropic"` || string(got["generated_at"]) != `"2026-10-11T09:30:00Z"` {
		t.Errorf("summary = %v", got)
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
	next := domain.ItemCursor{Sort: domain.SortNewest, FeedAt: time.Date(2026, 10, 6, 9, 30, 0, 123456000, time.UTC), ID: itemID}
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
	next := domain.ItemCursor{Sort: domain.SortNewest, FeedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), ID: itemID}
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

func TestListItemsSort(t *testing.T) {
	srv, items := newItemServer(t)
	for query, want := range map[string]domain.ItemSort{"": domain.SortNewest, "sort=newest": domain.SortNewest, "sort=ranked": domain.SortRanked} {
		if rec := do(t, srv, http.MethodGet, "/api/v1/items?"+query, nil); rec.Code != http.StatusOK || items.gotFilter.Sort != want {
			t.Errorf("%q: status %d, sort %q; want %q", query, rec.Code, items.gotFilter.Sort, want)
		}
	}
	for _, query := range []string{"sort=score", "sort=RANKED", "sort=ranked&sort=newest"} {
		rec := do(t, srv, http.MethodGet, "/api/v1/items?"+query, nil)
		if body := decode[errorBody](t, rec); rec.Code != http.StatusBadRequest || len(body.Error.Details) != 1 || body.Error.Details[0].Field != "sort" {
			t.Errorf("%q: status %d, error %+v", query, rec.Code, body.Error)
		}
	}
}

func TestListItemsRankedCursors(t *testing.T) {
	srv, items := newItemServer(t)
	next := domain.ItemCursor{Sort: domain.SortRanked, RankAt: time.Date(2026, 10, 5, 21, 30, 0, 123456000, time.UTC),
		DiscoveredAt: time.Date(2026, 10, 6, 9, 30, 0, 0, time.UTC), ID: itemID}
	items.page.Next = &next
	ranked := *decode[itemListJSON](t, do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked&kind=paper", nil)).NextCursor
	newestNext := domain.ItemCursor{Sort: domain.SortNewest, FeedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), ID: itemID}
	items.page.Next = &newestNext
	newest := *decode[itemListJSON](t, do(t, srv, http.MethodGet, "/api/v1/items?kind=paper", nil)).NextCursor
	items.page.Next = nil

	// A ranked cursor round-trips with the same filters and sort.
	rec := do(t, srv, http.MethodGet, "/api/v1/items?kind=paper&sort=ranked&cursor="+ranked, nil)
	if rec.Code != http.StatusOK || items.gotFilter.After == nil || *items.gotFilter.After != next {
		t.Fatalf("status %d, after = %+v, want %+v", rec.Code, items.gotFilter.After, next)
	}

	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	id := itemID.String()
	tests := []struct{ name, query, msg string }{
		{"ranked cursor, default sort", "kind=paper&cursor=" + ranked, "different filters, sort, preferences or feedback"},
		{"newest cursor, ranked sort", "kind=paper&sort=ranked&cursor=" + newest, "different filters, sort, preferences or feedback"},
		{"ranked cursor, other filters", "kind=repository&sort=ranked&cursor=" + ranked, "different filters, sort, preferences or feedback"},
		{"unknown cursor sort", "cursor=" + enc(`{"v":1,"t":1,"id":"`+id+`","f":"","s":"top"}`), "not a valid cursor"},
		{"ranked without discovery time", "sort=ranked&cursor=" + enc(`{"v":1,"id":"`+id+`","f":"","s":"ranked","r":1}`), "not a valid cursor"},
		{"ranked with feed time", "sort=ranked&cursor=" + enc(`{"v":1,"t":1,"id":"`+id+`","f":"","s":"ranked","r":1,"d":1}`), "not a valid cursor"},
		{"newest with rank time", "cursor=" + enc(`{"v":1,"t":1,"id":"`+id+`","f":"","r":1}`), "not a valid cursor"},
	}
	for _, tt := range tests {
		rec := do(t, srv, http.MethodGet, "/api/v1/items?"+tt.query, nil)
		body := decode[errorBody](t, rec)
		if rec.Code != http.StatusBadRequest || len(body.Error.Details) != 1 || body.Error.Details[0].Field != "cursor" ||
			!strings.Contains(body.Error.Details[0].Message, tt.msg) {
			t.Errorf("%s: status %d, error %+v", tt.name, rec.Code, body.Error)
		}
	}
}

// fakePrefs is an in-memory PreferenceStore.
type fakePrefs struct{ p domain.Preferences }

func (f *fakePrefs) GetPreferences(context.Context) (domain.Preferences, error) { return f.p, nil }
func (f *fakePrefs) SetPreferences(_ context.Context, p domain.Preferences) (domain.Preferences, error) {
	p.Normalize()
	f.p = p
	return p, nil
}

func TestListItemsPersonalized(t *testing.T) {
	_, items := newItemServer(t)
	prefs := &fakePrefs{p: domain.Preferences{Topics: []string{"agents"}, SourceIDs: []uuid.UUID{hnID}}}
	reg := &slugRegistry{bySlug: map[string]domain.Source{"hn-ai": {ID: hnID, Slug: "hn-ai", Type: domain.SourceTypeHackerNews}}}
	srv := New(Options{Sources: reg, Items: items, Preferences: prefs})

	// Not requested: no personalization, whatever the sort.
	for _, q := range []string{"", "sort=ranked", "sort=ranked&personalized=false"} {
		if rec := do(t, srv, http.MethodGet, "/api/v1/items?"+q, nil); rec.Code != http.StatusOK || items.gotFilter.Personalize != nil {
			t.Errorf("%q: status %d, personalize %+v", q, rec.Code, items.gotFilter.Personalize)
		}
	}
	rec := do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked&personalized=true", nil)
	if p := items.gotFilter.Personalize; rec.Code != http.StatusOK || p == nil || !slices.Equal(p.Topics, []string{"agents"}) {
		t.Fatalf("status %d, personalize %+v", rec.Code, p)
	}

	for _, tt := range []struct{ query, msg string }{
		{"personalized=true", "requires sort=ranked"},
		{"sort=newest&personalized=true", "requires sort=ranked"},
		{"sort=ranked&personalized=maybe", "must be true or false"},
	} {
		rec := do(t, srv, http.MethodGet, "/api/v1/items?"+tt.query, nil)
		body := decode[errorBody](t, rec)
		if rec.Code != http.StatusBadRequest || len(body.Error.Details) != 1 || body.Error.Details[0].Field != "personalized" ||
			!strings.Contains(body.Error.Details[0].Message, tt.msg) {
			t.Errorf("%q: status %d, error %+v", tt.query, rec.Code, body.Error)
		}
	}
	// Without a preference store, personalization is unavailable.
	plain, _ := newItemServer(t)
	if rec := do(t, plain, http.MethodGet, "/api/v1/items?sort=ranked&personalized=true", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no preference store: status %d, want 400", rec.Code)
	}

	// A personalized cursor is bound to the preferences it was issued with,
	// and does not work unpersonalized.
	next := domain.ItemCursor{Sort: domain.SortRanked, RankAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC),
		DiscoveredAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), ID: itemID}
	items.page.Next = &next
	cursor := *decode[itemListJSON](t, do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked&personalized=true", nil)).NextCursor
	items.page.Next = nil
	if rec := do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked&personalized=true&cursor="+cursor, nil); rec.Code != http.StatusOK {
		t.Errorf("same preferences: status %d", rec.Code)
	}
	for name, query := range map[string]string{
		"unpersonalized": "sort=ranked&cursor=" + cursor,
		"changed prefs":  "sort=ranked&personalized=true&cursor=" + cursor,
	} {
		if name == "changed prefs" {
			prefs.p.Topics = []string{"robotics"}
		}
		rec := do(t, srv, http.MethodGet, "/api/v1/items?"+query, nil)
		if body := decode[errorBody](t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body.Error.Details[0].Message, "preferences") {
			t.Errorf("%s: status %d, error %+v", name, rec.Code, body.Error)
		}
	}
}

// fakeFeedback is an in-memory FeedbackStore.
type fakeFeedback struct {
	byItem map[uuid.UUID]domain.FeedbackValue
}

func (f *fakeFeedback) SetFeedback(_ context.Context, id uuid.UUID, v domain.FeedbackValue) (domain.Feedback, error) {
	if id != itemID {
		return domain.Feedback{}, domain.ErrNotFound
	}
	f.byItem[id] = v
	return domain.Feedback{ItemID: id, Value: v}, nil
}

func (f *fakeFeedback) DeleteFeedback(_ context.Context, id uuid.UUID) error {
	delete(f.byItem, id)
	return nil
}

func (f *fakeFeedback) ListFeedback(context.Context, int) ([]domain.Feedback, error) {
	var out []domain.Feedback
	for id, v := range f.byItem {
		out = append(out, domain.Feedback{ItemID: id, Value: v})
	}
	return out, nil
}

func (f *fakeFeedback) FeedbackSignals(context.Context) (domain.FeedbackSignals, error) {
	var entries []domain.FeedbackEntry
	for id, v := range f.byItem {
		entries = append(entries, domain.FeedbackEntry{ItemID: id, Value: v})
	}
	return domain.NewFeedbackSignals(entries), nil
}

func TestFeedbackEndpoints(t *testing.T) {
	_, items := newItemServer(t)
	fb := &fakeFeedback{byItem: map[uuid.UUID]domain.FeedbackValue{}}
	reg := &slugRegistry{bySlug: map[string]domain.Source{}}
	srv := New(Options{Sources: reg, Items: items, Preferences: &fakePrefs{}, Feedback: fb})
	path := "/api/v1/items/" + itemID.String() + "/feedback"

	rec := send(t, srv, http.MethodPut, path, `{"value":"more"}`, nil)
	if rec.Code != http.StatusOK || fb.byItem[itemID] != domain.FeedbackMore || !strings.Contains(rec.Body.String(), `"value":"more"`) {
		t.Fatalf("PUT more: %d %s", rec.Code, rec.Body)
	}
	for _, tt := range []struct {
		path, body string
		code       int
	}{
		{path, `{"value":"meh"}`, http.StatusUnprocessableEntity},
		{path, `{"value":"more","x":1}`, http.StatusBadRequest},
		{"/api/v1/items/not-a-uuid/feedback", `{"value":"more"}`, http.StatusBadRequest},
		{"/api/v1/items/" + uuid.New().String() + "/feedback", `{"value":"less"}`, http.StatusNotFound},
	} {
		if rec := send(t, srv, http.MethodPut, tt.path, tt.body, nil); rec.Code != tt.code {
			t.Errorf("PUT %s %s: status %d, want %d", tt.path, tt.body, rec.Code, tt.code)
		}
	}
	if fb.byItem[itemID] != domain.FeedbackMore {
		t.Error("a rejected PUT changed the feedback")
	}
	if rec := do(t, srv, http.MethodGet, "/api/v1/feedback", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), itemID.String()) {
		t.Errorf("GET feedback: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodGet, "/api/v1/feedback?limit=0", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("GET feedback limit=0: %d", rec.Code)
	}

	// The personalized feed gets the signals; a cursor is bound to them.
	next := domain.ItemCursor{Sort: domain.SortRanked, RankAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC),
		DiscoveredAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), ID: itemID}
	items.page.Next = &next
	rec = do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked&personalized=true", nil)
	if f := items.gotFilter.Feedback; rec.Code != http.StatusOK || f == nil || !slices.Equal(f.More, []uuid.UUID{itemID}) {
		t.Fatalf("status %d, feedback %+v", rec.Code, f)
	}
	cursor := *decode[itemListJSON](t, rec).NextCursor
	items.page.Next = nil
	if rec := do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked", nil); items.gotFilter.Feedback != nil || rec.Code != http.StatusOK {
		t.Error("feedback applied without personalized=true")
	}

	if rec := do(t, srv, http.MethodDelete, path, nil); rec.Code != http.StatusNoContent || len(fb.byItem) != 0 {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodDelete, path, nil); rec.Code != http.StatusNoContent {
		t.Errorf("repeated DELETE: %d", rec.Code)
	}
	rec = do(t, srv, http.MethodGet, "/api/v1/items?sort=ranked&personalized=true&cursor="+cursor, nil)
	if body := decode[errorBody](t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body.Error.Details[0].Message, "feedback") {
		t.Errorf("cursor after feedback changed: %d %+v", rec.Code, body.Error)
	}
}
