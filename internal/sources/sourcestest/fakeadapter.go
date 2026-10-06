package sourcestest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// FakeAdapter is a deterministic, network-free sources.Adapter for tests.
// Each Fetch call consumes the next scripted Step; once the script is
// exhausted the last step repeats.
type FakeAdapter struct {
	SourceType domain.SourceType
	Steps      []Step

	mu    sync.Mutex
	calls int
	seen  []domain.Source
}

// Step scripts one Fetch call.
type Step struct {
	Result sources.FetchResult
	Err    error
	// Panic makes Fetch panic with this value.
	Panic any
	// Delay makes Fetch wait before returning, honoring ctx.
	Delay time.Duration
	// Block makes Fetch wait until the channel is closed or ctx ends.
	Block <-chan struct{}
	// Started, if set, is closed when Fetch begins.
	Started chan<- struct{}
}

var _ sources.Adapter = (*FakeAdapter)(nil)

// Type implements sources.Adapter.
func (f *FakeAdapter) Type() domain.SourceType { return f.SourceType }

// Fetch implements sources.Adapter.
func (f *FakeAdapter) Fetch(ctx context.Context, src domain.Source) (sources.FetchResult, error) {
	f.mu.Lock()
	step := Step{}
	if len(f.Steps) > 0 {
		step = f.Steps[min(f.calls, len(f.Steps)-1)]
	}
	f.calls++
	f.seen = append(f.seen, src)
	f.mu.Unlock()

	if step.Started != nil {
		close(step.Started)
	}
	if step.Panic != nil {
		panic(step.Panic)
	}
	if step.Block != nil {
		select {
		case <-step.Block:
		case <-ctx.Done():
			return step.Result, fmt.Errorf("fake fetch interrupted: %w", ctx.Err())
		}
	}
	if step.Delay > 0 {
		select {
		case <-time.After(step.Delay):
		case <-ctx.Done():
			return step.Result, fmt.Errorf("fake fetch interrupted: %w", ctx.Err())
		}
	}
	return step.Result, step.Err
}

// Calls reports how many times Fetch was called.
func (f *FakeAdapter) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Seen returns the sources passed to Fetch, in call order.
func (f *FakeAdapter) Seen() []domain.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Source(nil), f.seen...)
}

// LoadCandidates reads a JSON fixture of candidates (see
// internal/ingest/testdata). Timestamps use RFC 3339.
func LoadCandidates(t testing.TB, path string) []domain.Candidate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var raw []struct {
		ExternalID    string          `json:"external_id"`
		Kind          string          `json:"kind"`
		Title         string          `json:"title"`
		Description   string          `json:"description"`
		URL           string          `json:"url"`
		DiscussionURL string          `json:"discussion_url"`
		Authors       []string        `json:"authors"`
		Tags          []string        `json:"tags"`
		Metadata      json.RawMessage `json:"metadata"`
		PublishedAt   *time.Time      `json:"published_at"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	out := make([]domain.Candidate, len(raw))
	for i, r := range raw {
		out[i] = domain.Candidate{
			ExternalID: r.ExternalID, Kind: domain.ItemKind(r.Kind), Title: r.Title, Description: r.Description,
			URL: r.URL, DiscussionURL: r.DiscussionURL, Authors: r.Authors, Tags: r.Tags,
			Metadata: r.Metadata, PublishedAt: r.PublishedAt,
		}
	}
	return out
}
