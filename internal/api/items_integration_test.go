package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/api"
	"synergy/internal/domain"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/store"
	"synergy/internal/store/storetest"
)

// Feed tests against real PostgreSQL: HTTP -> api -> store -> SQL.
//
// Fixture (feed time = published, else discovered):
//
//	g1  github  repository  no published date, discovered 10-07 11:00   tags llm
//	h1  hn      discussion  10-07 08:00, same URL as a1 -> duplicate of a1
//	h2  hn      discussion  10-06 10:00 (ties with a1; inserted later, so larger id)
//	a1  arxiv   paper       10-06 10:00   tags cs.LG, cs.AI
//	a2  arxiv   paper       10-05 10:00   tags cs.CL
//	g2  github  repository  10-01 00:00   tags llm, agents
//
// Default feed: g1 h2 a1 a2 g2 (h1 hidden as a duplicate).

type feedEnv struct {
	t          *testing.T
	st         *store.Store
	reg        *sources.Registry
	srv        *httptest.Server
	ids        map[string]uuid.UUID // fixture name -> item ID
	names      map[uuid.UUID]string
	hn, ax, gh domain.Source
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

func newFeedEnv(t *testing.T, seed bool) *feedEnv {
	t.Helper()
	st := storetest.New(t)
	reg, err := sources.NewRegistry(st, nil, hackernews.Spec{}, arxiv.Spec{}, github.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	e := &feedEnv{t: t, st: st, reg: reg, ids: map[string]uuid.UUID{}, names: map[uuid.UUID]string{}}
	e.srv = httptest.NewServer(api.New(api.Options{Sources: reg, Items: st}))
	t.Cleanup(e.srv.Close)

	mk := func(slug, name string, typ domain.SourceType) domain.Source {
		src, err := st.CreateSource(context.Background(), domain.NewSource{Slug: slug, Name: name, Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	e.hn = mk("hn-ai", "Hacker News: AI", domain.SourceTypeHackerNews)
	e.ax = mk("arxiv-ai", "arXiv: AI", domain.SourceTypeArxiv)
	e.gh = mk("github-ai-repos", "GitHub: AI repos", domain.SourceTypeGitHub)
	if !seed {
		return e
	}

	e.add(at("2026-10-07T09:00:00Z"),
		e.item("a1", e.ax, domain.ItemKindPaper, "Flow matching for robots", "https://arxiv.org/abs/2610.00001", ptr(at("2026-10-06T10:00:00Z")), "cs.LG", "cs.AI"),
		e.item("a2", e.ax, domain.ItemKindPaper, "Language models count tokens", "https://arxiv.org/abs/2610.00002", ptr(at("2026-10-05T10:00:00Z")), "cs.CL"))
	e.add(at("2026-10-07T10:00:00Z"),
		e.item("h1", e.hn, domain.ItemKindDiscussion, "Flow matching robots paper", "https://arxiv.org/abs/2610.00001", ptr(at("2026-10-07T08:00:00Z"))),
		e.item("h2", e.hn, domain.ItemKindDiscussion, "Ask HN: LLM evals that work?", "https://news.ycombinator.com/item?id=2", ptr(at("2026-10-06T10:00:00Z")), "ask_hn"))
	e.add(at("2026-10-07T11:00:00Z"),
		e.item("g1", e.gh, domain.ItemKindRepository, "acme/agent-kit", "https://github.com/acme/agent-kit", nil, "llm"),
		e.item("g2", e.gh, domain.ItemKindRepository, "lab/agents", "https://github.com/lab/agents", ptr(at("2026-10-01T00:00:00Z")), "llm", "agents"))
	return e
}

type namedItem struct {
	name string
	item domain.NewItem
}

func (e *feedEnv) item(name string, src domain.Source, kind domain.ItemKind, title, link string, published *time.Time, tags ...string) namedItem {
	h := func(s string) []byte { b := sha256.Sum256([]byte(s)); return b[:] }
	return namedItem{name, domain.NewItem{
		SourceID: src.ID, ExternalID: name, Kind: kind, Title: title, URL: link, CanonicalURL: link,
		URLHash: h(link), ContentHash: h(title), Tags: tags, PublishedAt: published,
		Metadata: json.RawMessage(`{"fixture":"` + name + `"}`),
	}}
}

func (e *feedEnv) add(seenAt time.Time, items ...namedItem) {
	e.t.Helper()
	batch := make([]domain.NewItem, len(items))
	for i, n := range items {
		batch[i] = n.item
	}
	res, err := e.st.UpsertItems(context.Background(), batch, seenAt)
	if err != nil {
		e.t.Fatal(err)
	}
	for i, r := range res {
		e.ids[items[i].name], e.names[r.ID] = r.ID, items[i].name
	}
}

type feedPage struct {
	Items []struct {
		ID          uuid.UUID  `json:"id"`
		ExternalID  string     `json:"external_id"`
		FeedAt      time.Time  `json:"feed_at"`
		DuplicateOf *uuid.UUID `json:"duplicate_of"`
		Source      struct {
			Slug string `json:"slug"`
			Type string `json:"type"`
		} `json:"source"`
		AlsoSeenOn []struct {
			ItemID uuid.UUID `json:"item_id"`
			Source struct {
				Slug string `json:"slug"`
			} `json:"source"`
		} `json:"also_seen_on"`
	} `json:"items"`
	Count      int     `json:"count"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
	Error      *struct {
		Code    string `json:"code"`
		Details []struct {
			Field string `json:"field"`
		} `json:"details"`
	} `json:"error"`
}

func (e *feedEnv) get(path string) (int, feedPage) {
	e.t.Helper()
	res, err := e.srv.Client().Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var p feedPage
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		e.t.Fatalf("GET %s: decode: %v", path, err)
	}
	return res.StatusCode, p
}

func (e *feedEnv) feed(query string) []string {
	e.t.Helper()
	code, p := e.get("/api/v1/items?" + query)
	if code != http.StatusOK {
		e.t.Fatalf("GET ?%s: status %d (%+v)", query, code, p.Error)
	}
	return e.namesOf(p)
}

func (e *feedEnv) namesOf(p feedPage) []string {
	out := make([]string, len(p.Items))
	for i, it := range p.Items {
		out[i] = it.ExternalID
	}
	return out
}

// walk pages through the feed and returns every item name in order.
func (e *feedEnv) walk(query string, limit int) (names []string, pages int) {
	e.t.Helper()
	cursor := ""
	for {
		q := query + "&limit=" + strconv.Itoa(limit)
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		code, p := e.get("/api/v1/items?" + q)
		if code != http.StatusOK {
			e.t.Fatalf("page %d: status %d (%+v)", pages, code, p.Error)
		}
		pages++
		names = append(names, e.namesOf(p)...)
		if p.HasMore != (p.NextCursor != nil) || p.Count != len(p.Items) {
			e.t.Fatalf("inconsistent page %+v", p)
		}
		if !p.HasMore {
			return names, pages
		}
		if pages > 50 {
			e.t.Fatal("pagination did not terminate")
		}
		cursor = *p.NextCursor
	}
}

var defaultFeed = []string{"g1", "h2", "a1", "a2", "g2"}

func TestFeedEmpty(t *testing.T) {
	e := newFeedEnv(t, false)
	code, p := e.get("/api/v1/items")
	if code != http.StatusOK || len(p.Items) != 0 || p.Count != 0 || p.HasMore || p.NextCursor != nil {
		t.Errorf("empty feed = %d %+v", code, p)
	}
}

func TestFeedDefaultOrderingAndDuplicates(t *testing.T) {
	e := newFeedEnv(t, true)
	_, p := e.get("/api/v1/items")
	if got := e.namesOf(p); !slices.Equal(got, defaultFeed) {
		t.Fatalf("default feed = %v, want %v", got, defaultFeed)
	}
	// g1 has no published date: it is placed by its discovery time.
	if !p.Items[0].FeedAt.Equal(at("2026-10-07T11:00:00Z")) {
		t.Errorf("g1 feed_at = %v", p.Items[0].FeedAt)
	}
	// a1 carries its HN sighting; the HN duplicate itself is hidden.
	a1 := p.Items[2]
	if len(a1.AlsoSeenOn) != 1 || a1.AlsoSeenOn[0].ItemID != e.ids["h1"] || a1.AlsoSeenOn[0].Source.Slug != "hn-ai" {
		t.Errorf("a1 also_seen_on = %+v", a1.AlsoSeenOn)
	}
	for _, it := range p.Items {
		if it.Source.Slug == "" || it.Source.Type == "" {
			t.Errorf("item %s lacks source info", it.ExternalID)
		}
		if it.ExternalID != "a1" && len(it.AlsoSeenOn) != 0 {
			t.Errorf("item %s has unexpected sightings", it.ExternalID)
		}
	}

	_, p = e.get("/api/v1/items?include_duplicates=true")
	if got := e.namesOf(p); !slices.Equal(got, []string{"g1", "h1", "h2", "a1", "a2", "g2"}) {
		t.Fatalf("with duplicates = %v", got)
	}
	if h1 := p.Items[1]; h1.DuplicateOf == nil || *h1.DuplicateOf != e.ids["a1"] {
		t.Errorf("h1 duplicate_of = %v, want a1", h1.DuplicateOf)
	}
}

func TestFeedPaginationIsStableAndComplete(t *testing.T) {
	e := newFeedEnv(t, true)
	for limit := 1; limit <= 6; limit++ {
		names, pages := e.walk("", limit)
		if !slices.Equal(names, defaultFeed) {
			t.Errorf("limit %d: paged feed = %v, want %v", limit, names, defaultFeed)
		}
		if want := (len(defaultFeed) + limit - 1) / limit; pages != want {
			t.Errorf("limit %d: %d pages, want %d", limit, pages, want)
		}
	}
	// Paging a filtered feed (with a feed-time tie between h2 and a1).
	if names, _ := e.walk("source_type=hackernews,arxiv&include_duplicates=true", 1); !slices.Equal(names, []string{"h1", "h2", "a1", "a2"}) {
		t.Errorf("filtered paging = %v", names)
	}
	// Repeated requests give identical pages.
	first, second := e.feed("limit=3"), e.feed("limit=3")
	if !slices.Equal(first, second) {
		t.Errorf("non-deterministic: %v vs %v", first, second)
	}
}

func TestFeedNewItemsBetweenPages(t *testing.T) {
	e := newFeedEnv(t, true)
	_, p1 := e.get("/api/v1/items?limit=2")
	if got := e.namesOf(p1); !slices.Equal(got, []string{"g1", "h2"}) {
		t.Fatalf("page 1 = %v", got)
	}

	// While the client reads page 1, a newer item and a back-dated one arrive.
	e.add(at("2026-10-07T12:00:00Z"),
		e.item("new", e.ax, domain.ItemKindPaper, "Brand new paper", "https://arxiv.org/abs/2610.00009", ptr(at("2026-10-07T11:30:00Z"))),
		e.item("old", e.ax, domain.ItemKindPaper, "Late-indexed old paper", "https://arxiv.org/abs/2609.00001", ptr(at("2026-09-01T00:00:00Z"))))

	rest := []string{}
	cursor := *p1.NextCursor
	for {
		code, p := e.get("/api/v1/items?limit=2&cursor=" + url.QueryEscape(cursor))
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		rest = append(rest, e.namesOf(p)...)
		if !p.HasMore {
			break
		}
		cursor = *p.NextCursor
	}
	// Nothing repeated, nothing from the original feed skipped; the newer
	// item belongs before the cursor (seen on the next fresh read), the
	// back-dated one after it.
	if want := []string{"a1", "a2", "g2", "old"}; !slices.Equal(rest, want) {
		t.Errorf("remaining pages = %v, want %v", rest, want)
	}
	if fresh := e.feed("limit=1"); !slices.Equal(fresh, []string{"new"}) {
		t.Errorf("fresh first page = %v, want the new item", fresh)
	}
}

func TestFeedFilters(t *testing.T) {
	e := newFeedEnv(t, true)
	tests := []struct {
		query string
		want  []string
	}{
		{"source=arxiv-ai", []string{"a1", "a2"}},
		{"source=hn-ai,github-ai-repos", []string{"g1", "h2", "g2"}},
		{"source=" + e.ax.ID.String(), []string{"a1", "a2"}},
		{"source=hn-ai&include_duplicates=true", []string{"h1", "h2"}},
		{"source_type=github", []string{"g1", "g2"}},
		{"source_type=arxiv&source_type=hackernews", []string{"h2", "a1", "a2"}},
		{"kind=paper", []string{"a1", "a2"}},
		{"kind=discussion,repository", []string{"g1", "h2", "g2"}},
		{"kind=release", []string{}},
		{"tag=llm", []string{"g1", "g2"}},
		{"tag=llm&tag=agents", []string{"g2"}},
		{"tag=cs.LG", []string{"a1"}},
		{"tag=nonexistent", []string{}},
		{"since=2026-10-06", []string{"g1", "h2", "a1"}},
		{"since=2026-10-06T10:00:00Z", []string{"g1", "h2", "a1"}}, // inclusive
		{"until=2026-10-06T10:00:00Z", []string{"a2", "g2"}},       // exclusive
		{"since=2026-10-02&until=2026-10-06", []string{"a2"}},
		{"discovered_since=2026-10-07T10:30:00Z", []string{"g1", "g2"}},
		{"discovered_until=2026-10-07T10:30:00Z", []string{"h2", "a1", "a2"}},
		{"q=robot", []string{"a1"}}, // stemmed: matches "robots"
		{"q=" + url.QueryEscape(`"language models"`), []string{"a2"}},
		{"q=llm", []string{"h2"}},
		{"q=agents%20-lab", []string{}},
		{"q=matching&include_duplicates=true", []string{"h1", "a1"}},
		{"source_type=arxiv&tag=cs.LG&since=2026-10-06", []string{"a1"}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := e.feed(tt.query); !slices.Equal(got, tt.want) {
				t.Errorf("?%s = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestFeedSourceStatus(t *testing.T) {
	e := newFeedEnv(t, true)
	paused := domain.SourceStatusPaused
	if _, err := e.reg.Update(context.Background(), "hn-ai", domain.SourcePatch{Status: &paused}); err != nil {
		t.Fatal(err)
	}
	if got := e.feed(""); !slices.Equal(got, []string{"g1", "a1", "a2", "g2"}) {
		t.Errorf("default feed with HN paused = %v; only active sources are shown", got)
	}
	if got := e.feed("source_status=paused"); !slices.Equal(got, []string{"h2"}) {
		t.Errorf("paused only = %v", got)
	}
	if got := e.feed("source_status=all"); !slices.Equal(got, defaultFeed) {
		t.Errorf("all statuses = %v", got)
	}
	// An explicitly requested paused source still needs its status allowed.
	if got := e.feed("source=hn-ai"); len(got) != 0 {
		t.Errorf("source=hn-ai (paused) = %v, want empty unless source_status allows it", got)
	}
	// Direct links keep working.
	if code, _ := e.get("/api/v1/items/" + e.ids["h2"].String()); code != http.StatusOK {
		t.Errorf("item of a paused source = %d, want 200", code)
	}
}

func TestFeedSingleItem(t *testing.T) {
	e := newFeedEnv(t, true)
	get := func(id string) (int, map[string]any) {
		res, err := e.srv.Client().Get(e.srv.URL + "/api/v1/items/" + id)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}

	code, a1 := get(e.ids["a1"].String())
	if code != http.StatusOK || a1["title"] != "Flow matching for robots" || a1["kind"] != "paper" ||
		a1["source"].(map[string]any)["slug"] != "arxiv-ai" || a1["metadata"].(map[string]any)["fixture"] != "a1" {
		t.Errorf("a1 = %d %v", code, a1)
	}
	if seen := a1["also_seen_on"].([]any); len(seen) != 1 {
		t.Errorf("a1 also_seen_on = %v", seen)
	}
	if _, h1 := get(e.ids["h1"].String()); h1["duplicate_of"] != e.ids["a1"].String() {
		t.Errorf("h1 duplicate_of = %v, want a1", h1["duplicate_of"])
	}
	if _, g1 := get(e.ids["g1"].String()); g1["published_at"] != nil || g1["feed_at"] != g1["discovered_at"] {
		t.Errorf("g1 without a published date = %v", g1)
	}
	if code, body := get(uuid.NewString()); code != http.StatusNotFound || body["error"].(map[string]any)["code"] != "not_found" {
		t.Errorf("missing item = %d %v", code, body)
	}
	if code, _ := get("nope"); code != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", code)
	}
}

func TestFeedRejectsInvalidQueries(t *testing.T) {
	e := newFeedEnv(t, true)
	_, p := e.get("/api/v1/items?limit=1&kind=paper")
	for _, q := range []string{
		"limit=1000",
		"source=does-not-exist",
		"cursor=" + url.QueryEscape(*p.NextCursor), // issued for kind=paper
		"offset=10",
	} {
		code, body := e.get("/api/v1/items?" + q)
		if code != http.StatusBadRequest || body.Error == nil || body.Error.Code != "invalid_query" {
			t.Errorf("?%s = %d %+v, want 400 invalid_query", q, code, body.Error)
		}
	}
}
