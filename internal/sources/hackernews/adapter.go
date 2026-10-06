package hackernews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/sources"
)

// API constants. Firebase publishes no rate limit; Synergy stays polite at
// about 10 requests per second with a few concurrent requests.
const (
	DefaultBaseURL  = "https://hacker-news.firebaseio.com/v0"
	requestInterval = 100 * time.Millisecond
	requestBurst    = 4
	workers         = 4
)

// State is the adapter's cursor. Item IDs are assigned in submission order,
// so once an item is known to be older than the lookback window, every
// smaller ID is too: OldIDFloor lets later fetches skip them without a
// request. (Stories re-upped through HN's second-chance pool can carry a
// newer time on an old ID; they may be skipped, which is acceptable.)
type State struct {
	OldIDFloor int64 `json:"old_id_floor"`
}

// Adapter fetches Hacker News stories.
type Adapter struct {
	client *httpx.Client
	base   string
	now    func() time.Time
}

var _ sources.Adapter = (*Adapter)(nil)

// NewAdapter builds the adapter.
func NewAdapter(o sources.ClientOptions) *Adapter {
	base := o.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Adapter{
		client: o.NewClient("hackernews", requestInterval, requestBurst, httpx.RetryPolicy{}),
		base:   strings.TrimSuffix(base, "/"),
		now:    o.Clock(),
	}
}

// Type implements sources.Adapter.
func (a *Adapter) Type() domain.SourceType { return domain.SourceTypeHackerNews }

type fetched struct {
	item     *item
	err      error
	attempts int
	done     bool
}

// Fetch reads the configured lists, fetches up to MaxItems stories not
// already known to be too old, and returns those passing the filters.
func (a *Adapter) Fetch(ctx context.Context, src domain.Source) (sources.FetchResult, error) {
	var res sources.FetchResult
	cfg, err := ParseConfig(src.Config)
	if err != nil {
		return res, fmt.Errorf("invalid hackernews config: %w", err)
	}
	var st State
	if len(src.State) > 0 {
		_ = json.Unmarshal(src.State, &st) // unreadable state: start fresh
	}
	cutoff := a.now().Add(-cfg.Lookback.D())

	// 1. Ranked lists, merged in configured order without repeats.
	var ranked []int64
	lists := map[int64][]string{}
	for _, name := range cfg.Lists {
		resp, err := a.client.Get(ctx, a.base+"/"+listEndpoints[name]+".json", nil)
		if err != nil {
			res.Requests++
			return res, fmt.Errorf("list %s: %w", name, err)
		}
		res.Requests += resp.Attempts
		ids, err := parseList(resp.Body)
		if err != nil {
			return res, fmt.Errorf("list %s: %w", name, err)
		}
		for _, id := range ids {
			if _, seen := lists[id]; !seen {
				ranked = append(ranked, id)
			}
			lists[id] = append(lists[id], name)
		}
	}

	// 2. Skip IDs known to be too old; cap the request count.
	var ids []int64
	for _, id := range ranked {
		if id > st.OldIDFloor {
			ids = append(ids, id)
		}
	}
	ids = ids[:min(len(ids), cfg.MaxItems)]

	// 3. Fetch items with a few workers; the shared limiter paces them.
	results := a.fetchItems(ctx, ids)

	// 4. Filter in rank order.
	floor := st.OldIDFloor
	var failures []error
	for i, r := range results {
		res.Requests += r.attempts
		switch {
		case !r.done:
			continue // not attempted: the context ended
		case r.err != nil:
			failures = append(failures, fmt.Errorf("item %d: %w", ids[i], r.err))
			continue
		}
		it := r.item
		if it != nil && it.Time > 0 && time.Unix(it.Time, 0).Before(cutoff) {
			floor = max(floor, it.ID)
		}
		if skipReason(it, cfg, cutoff) != "" {
			continue
		}
		res.Items = append(res.Items, toCandidate(it, lists[it.ID]))
	}

	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("hackernews fetch interrupted: %w", err)
	}
	if len(failures) > 0 {
		return res, fmt.Errorf("%d of %d items could not be fetched; first: %w", len(failures), len(ids), failures[0])
	}
	res.State, _ = json.Marshal(State{OldIDFloor: floor})
	return res, nil
}

func (a *Adapter) fetchItems(ctx context.Context, ids []int64) []fetched {
	results := make([]fetched, len(ids))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = a.fetchItem(ctx, ids[i])
			}
		}()
	}
dispatch:
	for i := range ids {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func (a *Adapter) fetchItem(ctx context.Context, id int64) fetched {
	resp, err := a.client.Get(ctx, a.base+"/item/"+strconv.FormatInt(id, 10)+".json", nil)
	if err != nil {
		if ctx.Err() != nil {
			return fetched{attempts: 1} // interrupted, not a failure of this item
		}
		attempts := 1
		if se, ok := httpx.AsStatusError(err); ok {
			attempts = se.Attempts
		}
		return fetched{err: err, attempts: attempts, done: true}
	}
	it, err := parseItem(resp.Body)
	if err == nil && it != nil && it.ID != id {
		err = errors.New("response is for a different item")
	}
	return fetched{item: it, err: err, attempts: resp.Attempts, done: true}
}
