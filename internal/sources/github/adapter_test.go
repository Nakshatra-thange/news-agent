package github

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
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

// canary builds a fake credential at runtime; tests assert it never leaks.
func canary(t *testing.T) string {
	return strings.Join([]string{"canary", t.Name(), strconv.Itoa(os.Getpid())}, "-")
}

// fakeGitHub answers /search/repositories by the query's first term (e.g.
// "topic:llm"): a scripted sequence of responses (the last repeats) or, by
// default, testdata/<file> from the routes map (empty.json if unrouted). Every request is recorded.
type fakeGitHub struct {
	mu       sync.Mutex
	routes   map[string]string
	scripts  map[string][]canned
	requests []*http.Request
	hits     map[string]int
	delay    time.Duration
}

type canned struct {
	status int
	file   string
	body   string
	header http.Header
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, string) {
	f := &fakeGitHub{
		routes: map[string]string{
			"topic:llm":       "search_llm.json",
			"topic:ai-agents": "search_agents.json",
			"topic:empty":     "empty.json",
			"topic:broken":    "malformed.json",
			"topic:sparse":    "missing_fields.json",
		},
		scripts: map[string][]canned{},
		hits:    map[string]int{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	term, _, _ := strings.Cut(r.URL.Query().Get("q"), " ")
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	f.hits[term]++
	n := f.hits[term]
	script := f.scripts[term]
	file, ok := f.routes[term]
	if !ok {
		file = "empty.json"
	}
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	if r.URL.Path != "/search/repositories" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	c := canned{status: http.StatusOK, file: file}
	if len(script) > 0 {
		c = script[min(n, len(script))-1]
	}
	for k, vs := range c.header {
		w.Header()[k] = vs
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.status)
	body := []byte(c.body)
	if c.file != "" {
		body, _ = os.ReadFile("testdata/" + c.file)
	}
	_, _ = w.Write(body)
}

func (f *fakeGitHub) script(term string, c ...canned) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[term] = c
}

func (f *fakeGitHub) count(term string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[term]
}

func (f *fakeGitHub) received() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

func newTestAdapter(base, credential string) *Adapter {
	return newTestAdapterWithLogger(base, credential, nil)
}

func newTestAdapterWithLogger(base, credential string, logger *slog.Logger) *Adapter {
	return NewAdapter(sources.ClientOptions{
		UserAgent:       "Synergy-test",
		BaseURL:         base,
		RequestInterval: time.Millisecond,
		Retry:           httpx.RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
		Now:             func() time.Time { return fixtureNow },
		Logger:          logger,
	}, credential)
}

func source(config string) domain.Source {
	if config == "" {
		config = "{}"
	}
	return domain.Source{Slug: "github-ai-repos", Type: domain.SourceTypeGitHub, Config: json.RawMessage(config), State: json.RawMessage("{}")}
}

func ids(items []domain.Candidate) []string {
	out := make([]string, len(items))
	for i, c := range items {
		out[i] = c.ExternalID
	}
	return out
}

const twoQueries = `{"queries":["topic:llm","topic:ai-agents"]}`

func TestFetchMapsAndMergesQueries(t *testing.T) {
	_, base := newFakeGitHub(t)
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(twoQueries))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// acme/agent-kit is in both result sets and is returned once, first.
	if got := ids(res.Items); !slices.Equal(got, []string{"998877", "556677", "112233"}) {
		t.Fatalf("candidates = %v", got)
	}
	if res.Requests != 2 || res.State != nil {
		t.Errorf("requests=%d state=%s; want 2 and no cursor", res.Requests, res.State)
	}

	kit := res.Items[0]
	if kit.Kind != domain.ItemKindRepository || kit.Title != "acme/agent-kit" || kit.URL != "https://github.com/acme/agent-kit" ||
		kit.Description != "A toolkit for building LLM agents." || !slices.Equal(kit.Authors, []string{"acme"}) ||
		!slices.Equal(kit.Tags, []string{"llm", "ai-agents", "python"}) {
		t.Errorf("repo = %+v", kit)
	}
	if kit.PublishedAt == nil || !kit.PublishedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("published = %v", kit.PublishedAt)
	}
	var meta map[string]any
	_ = json.Unmarshal(kit.Metadata, &meta)
	want := map[string]any{
		"stars": float64(1200), "forks": float64(85), "open_issues": float64(14), "language": "Python",
		"license": "Apache-2.0", "homepage": "https://agent-kit.dev", "owner_type": "Organization",
		"archived": false, "updated_at": "2026-10-06T08:00:00Z", "pushed_at": "2026-10-06T07:30:00Z",
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("metadata[%s] = %v, want %v", k, meta[k], v)
		}
	}
	if m, _ := meta["matched_queries"].([]any); len(m) != 2 || m[0] != "topic:llm" || m[1] != "topic:ai-agents" {
		t.Errorf("matched_queries = %v", meta["matched_queries"])
	}

	// NOASSERTION licenses and empty homepages are omitted; null
	// description and language are tolerated.
	meta = nil
	_ = json.Unmarshal(res.Items[1].Metadata, &meta)
	if _, ok := meta["license"]; ok {
		t.Errorf("NOASSERTION license must be omitted: %v", meta)
	}
	if _, ok := meta["homepage"]; ok {
		t.Errorf("empty homepage must be omitted: %v", meta)
	}
	if bench := res.Items[2]; bench.Description != "" || len(bench.Tags) != 0 {
		t.Errorf("sparse repo = %+v", bench)
	}
}

func TestFetchBuildsSearchFromConfig(t *testing.T) {
	tests := []struct {
		name, config string
		wantQ        []string
		sort, per    string
	}{
		{"defaults", `{"queries":["topic:llm"]}`, []string{"topic:llm created:>=2026-09-29 stars:>=50"}, "stars", "30"},
		{"overrides", `{"queries":["agent framework language:python","topic:llm"],"created_within":"30d","min_stars":0,"sort":"updated","max_results_per_query":100}`,
			[]string{"agent framework language:python created:>=2026-09-06 stars:>=0", "topic:llm created:>=2026-09-06 stars:>=0"}, "updated", "100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, base := newFakeGitHub(t)
			if _, err := newTestAdapter(base, "").Fetch(context.Background(), source(tt.config)); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			reqs := fake.received()
			if len(reqs) != len(tt.wantQ) {
				t.Fatalf("requests = %d, want one per query", len(reqs))
			}
			for i, r := range reqs {
				q := r.URL.Query()
				if q.Get("q") != tt.wantQ[i] || q.Get("sort") != tt.sort || q.Get("order") != "desc" || q.Get("per_page") != tt.per {
					t.Errorf("request %d query = %v", i, q)
				}
			}
		})
	}
}

func TestFetchSendsAPIHeaders(t *testing.T) {
	fake, base := newFakeGitHub(t)
	if _, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:llm"]}`)); err != nil {
		t.Fatal(err)
	}
	h := fake.received()[0].Header
	if h.Get("Accept") != "application/vnd.github+json" || h.Get("X-GitHub-Api-Version") != apiVersion || h.Get("User-Agent") != "Synergy-test" {
		t.Errorf("headers = %v", h)
	}
	if _, ok := h["Authorization"]; ok {
		t.Error("no Authorization header may be sent without a token")
	}
}

func TestFetchEmptyResults(t *testing.T) {
	_, base := newFakeGitHub(t)
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:empty"]}`))
	if err != nil || len(res.Items) != 0 || res.Requests != 1 {
		t.Errorf("items=%d requests=%d err=%v", len(res.Items), res.Requests, err)
	}
}

func TestFetchMalformedResponseIsPartialFailure(t *testing.T) {
	fake, base := newFakeGitHub(t)
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:broken","topic:llm"]}`))
	if err == nil || !strings.Contains(err.Error(), "1 of 2 queries failed") || !strings.Contains(err.Error(), "malformed search response") {
		t.Fatalf("error = %v", err)
	}
	if !slices.Equal(ids(res.Items), []string{"998877", "556677"}) || fake.count("topic:llm") != 1 {
		t.Errorf("the healthy query must still run and its items be returned: %v", ids(res.Items))
	}
	if res.State != nil {
		t.Error("no state on failure")
	}
}

func TestFetchMissingFields(t *testing.T) {
	_, base := newFakeGitHub(t)
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:sparse"]}`))
	if err != nil {
		t.Fatalf("incomplete_results is a warning, not an error: %v", err)
	}
	// Both ID-less repos are kept separately (the pipeline rejects them);
	// the repeated 445566 is folded.
	if got := ids(res.Items); !slices.Equal(got, []string{"", "", "445566"}) {
		t.Fatalf("candidates = %q", got)
	}
	if res.Items[0].Title != "ghost/no-id" || res.Items[1].Title != "ghost/zero-id" {
		t.Errorf("ID-less repos must not collapse into one: %+v", res.Items[:2])
	}
	bare := res.Items[2]
	if bare.Description != "" || bare.Authors != nil || bare.Tags != nil || bare.PublishedAt != nil || bare.URL != "https://github.com/bare/minimal" {
		t.Errorf("sparse repo = %+v", bare)
	}
}

func TestFetchRetriesTransientErrors(t *testing.T) {
	fake, base := newFakeGitHub(t)
	fake.script("topic:llm", canned{status: http.StatusBadGateway}, canned{status: http.StatusOK, file: "search_llm.json"})
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:llm"]}`))
	if err != nil || len(res.Items) != 2 || res.Requests != 2 || fake.count("topic:llm") != 2 {
		t.Errorf("items=%d requests=%d hits=%d err=%v", len(res.Items), res.Requests, fake.count("topic:llm"), err)
	}
}

func TestFetchNonRetryableErrorContinues(t *testing.T) {
	fake, base := newFakeGitHub(t)
	fake.script("topic:llm", canned{status: http.StatusUnprocessableEntity, body: `{"message":"Validation Failed"}`})
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(twoQueries))
	if err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "1 of 2 queries failed") {
		t.Fatalf("error = %v", err)
	}
	if fake.count("topic:llm") != 1 || fake.count("topic:ai-agents") != 1 {
		t.Error("a 422 must not be retried, and other queries must still run")
	}
	if !slices.Equal(ids(res.Items), []string{"998877", "112233"}) || res.Requests != 2 {
		t.Errorf("items=%v requests=%d", ids(res.Items), res.Requests)
	}
}

func TestFetchShortRateLimitIsRetried(t *testing.T) {
	fake, base := newFakeGitHub(t)
	// A secondary limit with no wait hint: retried after backoff.
	fake.script("topic:llm", canned{status: http.StatusTooManyRequests}, canned{status: http.StatusOK, file: "search_llm.json"})
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:llm"]}`))
	if err != nil || len(res.Items) != 2 || fake.count("topic:llm") != 2 {
		t.Errorf("items=%d hits=%d err=%v", len(res.Items), fake.count("topic:llm"), err)
	}
}

func TestFetchLongRateLimitStopsFetch(t *testing.T) {
	fake, base := newFakeGitHub(t)
	reset := strconv.FormatInt(fixtureNow.Add(time.Hour).Unix(), 10)
	fake.script("topic:llm", canned{
		status: http.StatusForbidden,
		body:   `{"message":"API rate limit exceeded"}`,
		header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset}},
	})
	start := time.Now()
	res, err := newTestAdapter(base, "").Fetch(context.Background(), source(twoQueries))
	se, ok := httpx.AsStatusError(err)
	if !ok || !se.RateLimited || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v, want a fast rate-limit failure", err, time.Since(start))
	}
	if fake.count("topic:ai-agents") != 0 || len(res.Items) != 0 {
		t.Error("queries after a long rate limit must not be attempted")
	}
	for _, want := range []string{"1 of 2 queries failed (1 not attempted)", "setting GITHUB_TOKEN raises it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestRateLimited(t *testing.T) {
	now := func() time.Time { return fixtureNow }
	reset := func(d time.Duration) string { return strconv.FormatInt(fixtureNow.Add(d).Unix(), 10) }
	tests := []struct {
		name    string
		status  int
		header  http.Header
		wait    time.Duration
		limited bool
	}{
		{"ok", 200, nil, 0, false},
		{"plain 403 is a permission problem", 403, nil, 0, false},
		{"403 with quota left", 403, http.Header{"X-Ratelimit-Remaining": {"12"}}, 0, false},
		{"primary limit waits for reset", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset(30 * time.Second)}}, 31 * time.Second, true},
		{"primary limit, reset passed", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset(-time.Minute)}}, 0, true},
		{"primary limit, no reset", 429, http.Header{"X-Ratelimit-Remaining": {"0"}}, 0, true},
		{"secondary limit with Retry-After", 403, http.Header{"Retry-After": {"20"}}, 20 * time.Second, true},
		{"429 without hints", 429, nil, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wait, limited := RateLimited(now)(&http.Response{StatusCode: tt.status, Header: tt.header})
			if wait != tt.wait || limited != tt.limited {
				t.Errorf("RateLimited = %v, %v; want %v, %v", wait, limited, tt.wait, tt.limited)
			}
		})
	}
}

func TestFetchWithToken(t *testing.T) {
	cred := canary(t)
	fake, base := newFakeGitHub(t)
	if _, err := newTestAdapter(base, "  "+cred+"\n").Fetch(context.Background(), source(`{"queries":["topic:llm"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := fake.received()[0].Header.Get("Authorization"); got != "Bearer "+cred {
		t.Errorf("Authorization = %q, want the trimmed token as a bearer credential", got)
	}
}

func TestFetchRejectedTokenNeverLeaks(t *testing.T) {
	cred := canary(t)
	fake, base := newFakeGitHub(t)
	fake.script("topic:llm", canned{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`})
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := newTestAdapterWithLogger(base, cred, logger).Fetch(context.Background(), source(twoQueries))
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "GITHUB_TOKEN was rejected") {
		t.Fatalf("error = %v", err)
	}
	if fake.count("topic:llm") != 1 || fake.count("topic:ai-agents") != 0 {
		t.Error("a rejected token must not be retried or reused for later queries")
	}
	for name, s := range map[string]string{"error": err.Error(), "logs": logs.String()} {
		if strings.Contains(s, cred) || strings.Contains(s, "Bearer") {
			t.Errorf("%s leaked the credential: %s", name, s)
		}
	}
}

func TestFetchContextCancellation(t *testing.T) {
	fake, base := newFakeGitHub(t)
	fake.delay = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newTestAdapter(base, "").Fetch(ctx, source(twoQueries))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("err=%v after %v, want prompt DeadlineExceeded", err, time.Since(start))
	}
	if fake.count("topic:ai-agents") != 0 {
		t.Error("no further queries after cancellation")
	}
}

func TestFetchInvalidConfig(t *testing.T) {
	fake, base := newFakeGitHub(t)
	_, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"sort":"forks"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid github config") || len(fake.received()) != 0 {
		t.Errorf("error = %v", err)
	}
}

func TestErrorsOmitQueryString(t *testing.T) {
	fake, base := newFakeGitHub(t)
	fake.script("topic:llm", canned{status: http.StatusNotFound})
	_, err := newTestAdapter(base, "").Fetch(context.Background(), source(`{"queries":["topic:llm"]}`))
	if err == nil || strings.Contains(err.Error(), "per_page") || strings.Contains(err.Error(), url.QueryEscape("created:")) {
		t.Errorf("error should name the endpoint only: %v", err)
	}
}

func TestDefaultsArePolite(t *testing.T) {
	c := DefaultConfig()
	if len(c.Queries)*c.MaxResultsPerQuery > 200 {
		t.Errorf("default config requests up to %d repos per fetch", len(c.Queries)*c.MaxResultsPerQuery)
	}
	if intervalAnon < 6*time.Second || intervalToken < 2*time.Second {
		t.Error("request spacing must respect the 10/min (anonymous) and 30/min (token) search limits")
	}
}
