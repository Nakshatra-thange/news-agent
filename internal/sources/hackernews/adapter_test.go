package hackernews

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/sources"
)

// fixtureNow matches the timestamps in testdata.
var fixtureNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeHN serves testdata as the Firebase API. Paths can be overridden with a
// sequence of canned responses, and every request is counted.
type fakeHN struct {
	t        *testing.T
	mu       sync.Mutex
	hits     map[string]int
	override map[string][]canned
	delay    time.Duration
}

type canned struct {
	status int
	body   string
	header http.Header
}

func newFakeHN(t *testing.T) (*fakeHN, string) {
	f := &fakeHN{t: t, hits: map[string]int{}, override: map[string][]canned{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL + "/v0"
}

func (f *fakeHN) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v0/")
	f.mu.Lock()
	f.hits[path]++
	n := f.hits[path]
	seq := f.override[path]
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	if len(seq) > 0 {
		c := seq[min(n, len(seq))-1]
		for k, vs := range c.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(c.status)
		_, _ = w.Write([]byte(c.body))
		return
	}
	// API item/<id>.json is stored as testdata/items/<id>.json.
	file := filepath.Join("testdata", filepath.FromSlash(strings.Replace(path, "item/", "items/", 1)))
	body, err := os.ReadFile(file)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (f *fakeHN) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeHN) set(path string, c ...canned) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override[path] = c
}

func newTestAdapter(base string) *Adapter {
	return NewAdapter(sources.ClientOptions{
		BaseURL:         base,
		RequestInterval: time.Millisecond,
		Retry:           httpx.RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
		Now:             func() time.Time { return fixtureNow },
	})
}

func source(config, state string) domain.Source {
	if config == "" {
		config = "{}"
	}
	if state == "" {
		state = "{}"
	}
	return domain.Source{Slug: "hn-ai", Type: domain.SourceTypeHackerNews, Config: json.RawMessage(config), State: json.RawMessage(state)}
}

func ids(items []domain.Candidate) []string {
	out := make([]string, len(items))
	for i, c := range items {
		out[i] = c.ExternalID
	}
	return out
}

func TestFetchFiltersAndMaps(t *testing.T) {
	fake, base := newFakeHN(t)
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Kept: LLM router (1006), Ask HN about Claude agents (1002), machine
	// learning (1001). Dropped: non-matching, low points, too old, job,
	// comment, deleted, dead, purged (null).
	if got := ids(res.Items); !slices.Equal(got, []string{"1006", "1002", "1001"}) {
		t.Fatalf("candidates = %v", got)
	}
	if res.Requests != 14 { // 2 lists + 12 distinct items
		t.Errorf("requests = %d, want 14", res.Requests)
	}
	if string(res.State) != `{"old_id_floor":950}` {
		t.Errorf("state = %s, want floor at the newest too-old item", res.State)
	}
	if fake.count("item/950.json") != 1 || fake.count("item/1006.json") != 1 {
		t.Error("items must be fetched exactly once even when in several lists")
	}

	link := res.Items[0]
	if link.Kind != domain.ItemKindDiscussion || link.Title != "Show HN: An open-source LLM router" ||
		link.URL != "https://github.com/Acme/llm-router?utm_source=hn" ||
		link.DiscussionURL != "https://news.ycombinator.com/item?id=1006" ||
		!slices.Equal(link.Authors, []string{"router_dev"}) || !slices.Equal(link.Tags, []string{"show_hn"}) {
		t.Errorf("link story = %+v", link)
	}
	if link.PublishedAt == nil || !link.PublishedAt.Equal(fixtureNow.Add(-2*time.Hour)) {
		t.Errorf("published = %v", link.PublishedAt)
	}
	var meta map[string]any
	_ = json.Unmarshal(link.Metadata, &meta)
	if meta["points"] != float64(120) || meta["num_comments"] != float64(34) || len(meta["hn_lists"].([]any)) != 2 {
		t.Errorf("metadata = %v", meta)
	}

	ask := res.Items[1]
	if ask.URL != "https://news.ycombinator.com/item?id=1002" || !slices.Equal(ask.Tags, []string{"ask_hn"}) {
		t.Errorf("text post must link to its discussion: %+v", ask)
	}
	if ask.Description != "We run many evals.\nWhat works for you & your team? See this." {
		t.Errorf("text = %q", ask.Description)
	}
}

func TestFetchCursorSkipsKnownOldItems(t *testing.T) {
	fake, base := newFakeHN(t)
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", `{"old_id_floor":950}`))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Equal(ids(res.Items), []string{"1006", "1002", "1001"}) {
		t.Errorf("candidates = %v", ids(res.Items))
	}
	if res.Requests != 12 || fake.count("item/900.json") != 0 || fake.count("item/950.json") != 0 {
		t.Errorf("requests=%d; items at or below the floor must not be requested", res.Requests)
	}
	if string(res.State) != `{"old_id_floor":950}` {
		t.Errorf("state = %s", res.State)
	}
}

func TestFetchUnreadableStateStartsFresh(t *testing.T) {
	_, base := newFakeHN(t)
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", `{"old_id_floor":"x"}`))
	if err != nil || res.Requests != 14 {
		t.Errorf("requests=%d err=%v", res.Requests, err)
	}
}

func TestConfigControlsFetch(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		want     []string
		requests int
	}{
		{"keywords and points", `{"queries":["Rust","GPT"],"min_points":0}`, []string{"1007", "1008"}, 14},
		{"top list only", `{"lists":["top"]}`, []string{"1006", "1002", "1001"}, 12},
		{"best list only", `{"lists":["best"]}`, []string{"1006", "1001"}, 5},
		{"longer lookback", `{"lookback":"5d","queries":["OpenAI","Gemini"]}`, []string{"950", "900"}, 14},
		{"max items caps requests", `{"max_items":3}`, []string{"1006", "1002"}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, base := newFakeHN(t)
			res, err := newTestAdapter(base).Fetch(context.Background(), source(tt.config, ""))
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got := ids(res.Items); !slices.Equal(got, tt.want) || res.Requests != tt.requests {
				t.Errorf("candidates=%v requests=%d, want %v and %d", got, res.Requests, tt.want, tt.requests)
			}
		})
	}
}

func TestFetchInvalidConfig(t *testing.T) {
	_, base := newFakeHN(t)
	_, err := newTestAdapter(base).Fetch(context.Background(), source(`{"lists":["front"]}`, ""))
	if err == nil || !strings.Contains(err.Error(), "invalid hackernews config") {
		t.Errorf("error = %v", err)
	}
}

func TestFetchEmptyList(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("topstories.json", canned{status: 200, body: `[]`})
	fake.set("beststories.json", canned{status: 200, body: `[]`})
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err != nil || len(res.Items) != 0 || string(res.State) != `{"old_id_floor":0}` {
		t.Errorf("items=%d state=%s err=%v", len(res.Items), res.State, err)
	}
}

func TestFetchMalformedList(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("topstories.json", canned{status: 200, body: `{"not":"a list"}`})
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err == nil || !strings.Contains(err.Error(), "list top: malformed story list") || len(res.Items) != 0 {
		t.Errorf("items=%d err=%v", len(res.Items), err)
	}
}

func TestFetchMalformedItemIsPartialFailure(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("item/1002.json", canned{status: 200, body: `{"id":1002,"title":`})
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err == nil || !strings.Contains(err.Error(), "1 of 12 items") || !strings.Contains(err.Error(), "item 1002: malformed item") {
		t.Fatalf("error = %v", err)
	}
	if !slices.Equal(ids(res.Items), []string{"1006", "1001"}) {
		t.Errorf("partial candidates = %v, want the items that did parse", ids(res.Items))
	}
	if res.State != nil {
		t.Error("state must not be produced on partial failure")
	}
}

func TestFetchItemForWrongID(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("item/1006.json", canned{status: 200, body: `{"id":42,"type":"story","title":"LLM","time":1791280000,"score":99}`})
	_, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err == nil || !strings.Contains(err.Error(), "different item") {
		t.Errorf("error = %v", err)
	}
}

func TestFetchRetriesTransientItemErrors(t *testing.T) {
	fake, base := newFakeHN(t)
	body, _ := os.ReadFile("testdata/items/1006.json")
	fake.set("item/1006.json", canned{status: 503}, canned{status: 200, body: string(body)})
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fake.count("item/1006.json") != 2 || res.Requests != 15 || !slices.Contains(ids(res.Items), "1006") {
		t.Errorf("hits=%d requests=%d items=%v", fake.count("item/1006.json"), res.Requests, ids(res.Items))
	}
}

func TestFetchNonRetryableItemError(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("item/1002.json", canned{status: 404, body: `not found`})
	res, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || fake.count("item/1002.json") != 1 {
		t.Errorf("err=%v hits=%d (404 must not be retried)", err, fake.count("item/1002.json"))
	}
	if !slices.Equal(ids(res.Items), []string{"1006", "1001"}) {
		t.Errorf("candidates = %v", ids(res.Items))
	}
}

func TestFetchRateLimitedListFailsFast(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("topstories.json", canned{status: 429, header: http.Header{"Retry-After": {"3600"}}})
	start := time.Now()
	_, err := newTestAdapter(base).Fetch(context.Background(), source("", ""))
	se, ok := httpx.AsStatusError(err)
	if !ok || !se.RateLimited || time.Since(start) > 2*time.Second {
		t.Errorf("err=%v after %v, want a fast rate-limit failure", err, time.Since(start))
	}
}

func TestFetchContextCancellation(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.delay = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newTestAdapter(base).Fetch(ctx, source("", ""))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("err=%v after %v, want prompt DeadlineExceeded", err, time.Since(start))
	}
}

func TestFetchCancelledDuringItems(t *testing.T) {
	fake, base := newFakeHN(t)
	fake.set("item/1001.json", canned{status: 200, body: `{}`})
	ctx, cancel := context.WithCancel(context.Background())
	a := newTestAdapter(base)
	// Cancel as soon as the lists are read: items are interrupted, not failed.
	go func() {
		for fake.count("beststories.json") == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	fake.delay = 200 * time.Millisecond
	_, err := a.Fetch(ctx, source("", ""))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestMatchesAny(t *testing.T) {
	kws := []string{"LLM", "GPT", "machine learning", "Claude"}
	tests := []struct {
		title string
		want  bool
	}{
		{"An LLM router", true},
		{"LLMs are everywhere", true},
		{"llm-based agents", true},
		{"(LLM) benchmarks", true},
		{"GPT-5 released", true},
		{"ChatGPT outage", false}, // keyword must start a word
		{"SMALLMOUTH bass", false},
		{"Practical Machine Learning", true},
		{"Claude's new model", true},
		{"Rust 2.0 released", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := matchesAny(tt.title, kws); got != tt.want {
			t.Errorf("matchesAny(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"a<p>b", "a\nb"},
		{"<i>x</i> &amp; <code>y</code>", "x & y"},
		{`see <a href="https://x">link</a>`, "see link"},
		{"&#x27;quoted&#x27; &gt; &lt;", "'quoted' > <"},
	}
	for _, tt := range tests {
		if got := htmlToText(tt.in); got != tt.want {
			t.Errorf("htmlToText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseItemNull(t *testing.T) {
	it, err := parseItem([]byte("null"))
	if err != nil || it != nil {
		t.Errorf("parseItem(null) = %v, %v", it, err)
	}
}
