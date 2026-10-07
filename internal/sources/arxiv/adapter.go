package arxiv

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/sources"
)

// API constants. arXiv asks clients to wait 3 seconds between requests and
// to use a single connection (https://info.arxiv.org/help/api/tou.html).
const (
	DefaultBaseURL  = "https://export.arxiv.org/api/query"
	requestInterval = 3 * time.Second
	defaultPageSize = 100
)

// Adapter fetches recent arXiv papers.
type Adapter struct {
	client *httpx.Client
	base   string
	now    func() time.Time
	// PageSize is the number of results requested per call.
	PageSize int
}

var _ sources.Adapter = (*Adapter)(nil)

// NewAdapter builds the adapter.
func NewAdapter(o sources.ClientOptions) *Adapter {
	base := o.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Adapter{
		client:   o.NewClient("arxiv", requestInterval, 1, httpx.RetryPolicy{BaseDelay: requestInterval}),
		base:     base,
		now:      o.Clock(),
		PageSize: defaultPageSize,
	}
}

// Type implements sources.Adapter.
func (a *Adapter) Type() domain.SourceType { return domain.SourceTypeArxiv }

// Fetch returns papers in the configured categories submitted within the
// lookback window, newest first, up to MaxResults.
//
// No cursor is kept: arXiv's submission dates are not announcement order
// (papers submitted earlier can be announced later), so a date cursor would
// silently miss papers. The lookback window plus deduplication is safe.
func (a *Adapter) Fetch(ctx context.Context, src domain.Source) (sources.FetchResult, error) {
	var res sources.FetchResult
	cfg, err := ParseConfig(src.Config)
	if err != nil {
		return res, fmt.Errorf("invalid arxiv config: %w", err)
	}
	now := a.now().UTC()
	cutoff := now.Add(-cfg.Lookback.D())
	query := buildQuery(cfg.Categories, cutoff, now)
	seen := map[string]bool{}

	for start := 0; len(res.Items) < cfg.MaxResults; {
		size := min(a.PageSize, cfg.MaxResults-len(res.Items))
		f, err := a.page(ctx, query, start, size, &res)
		if err != nil {
			return res, fmt.Errorf("page at offset %d: %w", start, err)
		}
		stop := false
		for _, e := range f.Entries {
			c, published := toCandidate(e)
			if published != nil && published.Before(cutoff) {
				stop = true // results are sorted newest first
				break
			}
			if c.ExternalID != "" && seen[c.ExternalID] {
				continue // pages can overlap when new papers arrive mid-fetch
			}
			seen[c.ExternalID] = true
			res.Items = append(res.Items, c)
			if len(res.Items) == cfg.MaxResults {
				break
			}
		}
		start += len(f.Entries)
		if stop || len(f.Entries) < size || start >= f.TotalResults {
			break
		}
	}
	return res, nil
}

// page fetches one page. arXiv occasionally answers with an empty page while
// reporting more results; that is retried once before being treated as an
// error.
func (a *Adapter) page(ctx context.Context, query string, start, size int, res *sources.FetchResult) (*feed, error) {
	q := url.Values{
		"search_query": {query},
		"start":        {strconv.Itoa(start)},
		"max_results":  {strconv.Itoa(size)},
		"sortBy":       {"submittedDate"},
		"sortOrder":    {"descending"},
	}
	u := a.base + "?" + q.Encode()
	for attempt := 1; ; attempt++ {
		resp, err := a.client.Get(ctx, u, nil)
		if err != nil {
			res.Requests += httpx.Attempts(err)
			return nil, err
		}
		res.Requests += resp.Attempts
		f, err := parseFeed(resp.Body)
		if err != nil {
			return nil, err
		}
		if len(f.Entries) > 0 || start >= f.TotalResults {
			return f, nil
		}
		if attempt == 2 {
			return nil, fmt.Errorf("arXiv returned an empty page although it reports %d results (transient API issue)", f.TotalResults)
		}
	}
}

// buildQuery restricts results to the categories and to submissions within
// [from, to], so the server does the date filtering.
func buildQuery(categories []string, from, to time.Time) string {
	cats := make([]string, len(categories))
	for i, c := range categories {
		cats[i] = "cat:" + c
	}
	const layout = "200601021504"
	return fmt.Sprintf("(%s) AND submittedDate:[%s TO %s]",
		strings.Join(cats, " OR "), from.UTC().Format(layout), to.UTC().Format(layout))
}
