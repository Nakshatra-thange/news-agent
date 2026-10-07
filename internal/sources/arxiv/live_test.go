//go:build live

package arxiv

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// TestLiveSmoke calls the real upstream with a deliberately tiny
// configuration. It runs only with: go test -tags live ./internal/sources/...
func TestLiveSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	src := domain.Source{Slug: "live-smoke", Config: json.RawMessage(`{"max_results":5}`), State: json.RawMessage("{}")}
	res, err := NewAdapter(sources.ClientOptions{}).Fetch(ctx, src)
	if err != nil {
		t.Fatalf("Fetch: %v (requests %d)", err, res.Requests)
	}
	if len(res.Items) == 0 {
		t.Fatalf("no items from the live API (requests %d)", res.Requests)
	}
	for _, c := range res.Items {
		if c.ExternalID == "" || c.Title == "" || c.URL == "" {
			t.Errorf("incomplete candidate: %+v", c)
		}
	}
	t.Logf("%d items in %d requests; first: %s", len(res.Items), res.Requests, res.Items[0].Title)
}
