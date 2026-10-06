package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"synergy/internal/api"
	"synergy/internal/sources"
	"synergy/internal/sources/arxiv"
	"synergy/internal/sources/github"
	"synergy/internal/sources/hackernews"
	"synergy/internal/store/storetest"
)

// End-to-end tests: HTTP -> api -> sources.Registry -> store -> PostgreSQL.

func newIntegrationServer(t *testing.T) (*httptest.Server, *sources.Registry) {
	t.Helper()
	st := storetest.New(t)
	reg, err := sources.NewRegistry(st, nil, hackernews.Spec{}, arxiv.Spec{}, github.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Options{Version: "test", DB: st, Sources: reg}))
	t.Cleanup(srv.Close)
	return srv, reg
}

type apiResp struct {
	code int
	body map[string]any
}

func call(t *testing.T, srv *httptest.Server, method, path, body string) apiResp {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	out := apiResp{code: res.StatusCode}
	if err := json.NewDecoder(res.Body).Decode(&out.body); err != nil {
		t.Fatalf("%s %s: decode: %v", method, path, err)
	}
	return out
}

func (r apiResp) str(key string) string {
	v, _ := r.body[key].(string)
	return v
}

func (r apiResp) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func slugsOf(t *testing.T, r apiResp) []string {
	t.Helper()
	list, _ := r.body["sources"].([]any)
	out := []string{}
	for _, s := range list {
		out = append(out, s.(map[string]any)["slug"].(string))
	}
	return out
}

func TestIntegrationSourceLifecycle(t *testing.T) {
	srv, _ := newIntegrationServer(t)

	// Register with a partial config: the rest is filled from type defaults.
	created := call(t, srv, "POST", "/api/v1/sources",
		`{"slug":"hn-rust","name":"HN Rust","type":"hackernews","priority":5,"config":{"queries":["Rust"]}}`)
	if created.code != http.StatusCreated {
		t.Fatalf("create: %d %v", created.code, created.body)
	}
	cfg := created.body["config"].(map[string]any)
	if cfg["min_points"] != float64(30) || cfg["lookback"] != "2d" {
		t.Errorf("config not normalized with defaults: %v", cfg)
	}
	if created.body["min_fetch_interval_seconds"] != float64(1800) {
		t.Errorf("interval = %v, want HN default 1800", created.body["min_fetch_interval_seconds"])
	}
	if h := created.body["health"].(map[string]any); h["status"] != "unknown" {
		t.Errorf("new source health = %v, want unknown", h)
	}
	id := created.str("id")

	// Readable by UUID and by slug.
	for _, ref := range []string{id, "hn-rust"} {
		if got := call(t, srv, "GET", "/api/v1/sources/"+ref, ""); got.code != 200 || got.str("id") != id {
			t.Errorf("GET %s: %d %v", ref, got.code, got.body)
		}
	}

	// Duplicate slug.
	if dup := call(t, srv, "POST", "/api/v1/sources", `{"slug":"hn-rust","name":"Again","type":"hackernews"}`); dup.code != http.StatusConflict {
		t.Errorf("duplicate: %d %v", dup.code, dup.body)
	}

	// Config replacement is validated against the source's own type.
	if bad := call(t, srv, "PATCH", "/api/v1/sources/hn-rust", `{"config":{"categories":["cs.AI"]}}`); bad.code != http.StatusUnprocessableEntity {
		t.Errorf("foreign config: %d %v", bad.code, bad.body)
	}
	// Fetch interval below the type's politeness floor.
	if bad := call(t, srv, "PATCH", "/api/v1/sources/hn-rust", `{"min_fetch_interval_seconds":10}`); bad.code != http.StatusUnprocessableEntity {
		t.Errorf("too-frequent interval: %d %v", bad.code, bad.body)
	}

	// Pause -> resume -> retire -> (frozen) -> restore.
	steps := []struct {
		body       string
		wantCode   int
		wantStatus string
	}{
		{`{"status":"paused"}`, 200, "paused"},
		{`{"status":"active","priority":9}`, 200, "active"},
		{`{"status":"retired"}`, 200, "retired"},
		{`{"priority":1}`, 409, ""},
		{`{"status":"paused"}`, 409, ""},
		{`{"status":"active"}`, 200, "active"},
	}
	for _, st := range steps {
		got := call(t, srv, "PATCH", "/api/v1/sources/"+id, st.body)
		if got.code != st.wantCode {
			t.Fatalf("PATCH %s: %d %v", st.body, got.code, got.body)
		}
		if st.wantStatus != "" && got.str("status") != st.wantStatus {
			t.Fatalf("PATCH %s: status %q, want %q", st.body, got.str("status"), st.wantStatus)
		}
		if st.wantStatus == "retired" && got.body["retired_at"] == nil {
			t.Error("retired source has no retired_at")
		}
		if st.wantStatus == "active" && got.body["retired_at"] != nil {
			t.Error("active source still has retired_at")
		}
	}
	final := call(t, srv, "GET", "/api/v1/sources/"+id, "")
	if final.body["priority"] != float64(9) {
		t.Errorf("priority = %v, want 9", final.body["priority"])
	}
}

func TestIntegrationSeedAndListing(t *testing.T) {
	srv, reg := newIntegrationServer(t)
	ctx := context.Background()

	if _, err := reg.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if res, err := reg.Seed(ctx); err != nil || len(res.Created) != 0 || len(res.Skipped) != 3 {
		t.Fatalf("re-seed: %+v %v", res, err)
	}

	list := call(t, srv, "GET", "/api/v1/sources", "")
	if got := slugsOf(t, list); !slices.Equal(got, []string{"hn-ai", "arxiv-ai", "github-ai-repos"}) {
		t.Errorf("default listing = %v, want seeds by priority", got)
	}

	// Retired sources are hidden by default but listable explicitly.
	call(t, srv, "PATCH", "/api/v1/sources/github-ai-repos", `{"status":"retired"}`)
	if got := slugsOf(t, call(t, srv, "GET", "/api/v1/sources", "")); !slices.Equal(got, []string{"hn-ai", "arxiv-ai"}) {
		t.Errorf("after retire, default listing = %v", got)
	}
	if got := slugsOf(t, call(t, srv, "GET", "/api/v1/sources?status=retired", "")); !slices.Equal(got, []string{"github-ai-repos"}) {
		t.Errorf("retired listing = %v", got)
	}
	if got := slugsOf(t, call(t, srv, "GET", "/api/v1/sources?status=all&type=arxiv", "")); !slices.Equal(got, []string{"arxiv-ai"}) {
		t.Errorf("type filter = %v", got)
	}

	// Priority changes reorder the listing.
	call(t, srv, "PATCH", "/api/v1/sources/arxiv-ai", `{"priority":100}`)
	if got := slugsOf(t, call(t, srv, "GET", "/api/v1/sources", "")); !slices.Equal(got, []string{"arxiv-ai", "hn-ai"}) {
		t.Errorf("after priority change = %v", got)
	}

	types := call(t, srv, "GET", "/api/v1/source-types", "")
	if n := len(types.body["source_types"].([]any)); n != 3 {
		t.Errorf("source types = %d, want 3", n)
	}
}

func TestIntegrationNotFound(t *testing.T) {
	srv, _ := newIntegrationServer(t)
	for _, ref := range []string{"nope", "0199a000-0000-7000-8000-000000000000"} {
		if got := call(t, srv, "GET", "/api/v1/sources/"+ref, ""); got.code != 404 || got.errCode() != "not_found" {
			t.Errorf("GET %s: %d %v", ref, got.code, got.body)
		}
		if got := call(t, srv, "PATCH", "/api/v1/sources/"+ref, `{"priority":1}`); got.code != 404 {
			t.Errorf("PATCH %s: %d", ref, got.code)
		}
	}
}
