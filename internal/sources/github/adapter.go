package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"synergy/internal/domain"
	"synergy/internal/httpx"
	"synergy/internal/sources"
)

// API constants. The search API allows 10 requests/minute without a token
// and 30 with one (https://docs.github.com/en/rest/search/search).
const (
	DefaultBaseURL  = "https://api.github.com"
	apiVersion      = "2022-11-28"
	intervalAnon    = 6 * time.Second
	intervalToken   = 2 * time.Second
	maxRateLimitGap = 65 * time.Second // search limits reset each minute
)

// Adapter discovers repositories through GitHub's search API.
type Adapter struct {
	client *httpx.Client
	base   string
	header http.Header
	now    func() time.Time
	logger *slog.Logger
	// authenticated is true when a token is sent.
	authenticated bool
}

var _ sources.Adapter = (*Adapter)(nil)

// NewAdapter builds the adapter. token is optional; it is sent only in the
// Authorization header, which httpx never includes in errors or logs.
func NewAdapter(o sources.ClientOptions, token string) *Adapter {
	base := o.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	now := o.Clock()
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	interval := intervalAnon
	h := http.Header{
		"Accept":               {"application/vnd.github+json"},
		"X-Github-Api-Version": {apiVersion},
	}
	if token = strings.TrimSpace(token); token != "" {
		interval = intervalToken
		h.Set("Authorization", "Bearer "+token)
	}
	return &Adapter{
		client: o.NewClient("github", interval, 1, httpx.RetryPolicy{
			MaxRetryAfter: maxRateLimitGap,
			RateLimited:   RateLimited(now),
		}),
		base:          strings.TrimSuffix(base, "/"),
		header:        h,
		now:           now,
		logger:        logger,
		authenticated: h.Get("Authorization") != "",
	}
}

// RateLimited recognizes GitHub's rate-limit responses: 403 or 429 with
// Retry-After (secondary limits) or with X-RateLimit-Remaining: 0 (primary
// limit, waiting until X-RateLimit-Reset). Other 403s (e.g. a token without
// access) are not rate limits and are not retried.
func RateLimited(now func() time.Time) func(*http.Response) (time.Duration, bool) {
	return func(r *http.Response) (time.Duration, bool) {
		if r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusTooManyRequests {
			return 0, false
		}
		if wait := httpx.RetryAfter(r.Header, now()); wait > 0 {
			return wait, true
		}
		if r.Header.Get("X-RateLimit-Remaining") == "0" {
			if reset, err := strconv.ParseInt(r.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				return max(time.Unix(reset, 0).Sub(now())+time.Second, 0), true
			}
			return 0, true
		}
		return 0, r.StatusCode == http.StatusTooManyRequests
	}
}

// Type implements sources.Adapter.
func (a *Adapter) Type() domain.SourceType { return domain.SourceTypeGitHub }

// Fetch runs each configured query (one request each), restricted to
// repositories created within CreatedWithin with at least MinStars stars.
// A repository matched by several queries is returned once, listing all of
// them. No cursor is kept: star counts change, so results are re-read.
func (a *Adapter) Fetch(ctx context.Context, src domain.Source) (sources.FetchResult, error) {
	var res sources.FetchResult
	cfg, err := ParseConfig(src.Config)
	if err != nil {
		return res, fmt.Errorf("invalid github config: %w", err)
	}
	since := a.now().UTC().Add(-cfg.CreatedWithin.D()).Format("2006-01-02")

	type found struct {
		repo    repo
		matched []string
	}
	var (
		order    []*found
		byID     = map[int64]*found{}
		failures []error
		skipped  int
	)
	for i, q := range cfg.Queries {
		params := url.Values{
			"q":        {fmt.Sprintf("%s created:>=%s stars:>=%d", q, since, cfg.MinStars)},
			"sort":     {cfg.Sort},
			"order":    {"desc"},
			"per_page": {strconv.Itoa(cfg.MaxResultsPerQuery)},
		}
		resp, err := a.client.Get(ctx, a.base+"/search/repositories?"+params.Encode(), a.header)
		if err != nil {
			res.Requests += httpx.Attempts(err)
			failures = append(failures, fmt.Errorf("query %q: %w", q, a.explain(err)))
			if ctx.Err() != nil || stopsFetch(err) {
				// Further queries would fail the same way; don't spend them.
				skipped = len(cfg.Queries) - i - 1
				break
			}
			continue
		}
		res.Requests += resp.Attempts
		a.logger.DebugContext(ctx, "github search", "query", q,
			"rate_remaining", resp.Header.Get("X-RateLimit-Remaining"), "rate_limit", resp.Header.Get("X-RateLimit-Limit"))
		sr, err := parseSearch(resp.Body)
		if err != nil {
			failures = append(failures, fmt.Errorf("query %q: %w", q, err))
			continue
		}
		if sr.IncompleteResults {
			// GitHub's search timed out server-side; the page is still valid.
			a.logger.WarnContext(ctx, "github search returned incomplete results", "query", q, "returned", len(sr.Items))
		}
		for _, r := range sr.Items {
			if r.ID <= 0 {
				// Cannot be deduplicated by ID; the pipeline rejects and
				// counts it, so malformed upstream data stays visible.
				order = append(order, &found{repo: r, matched: []string{q}})
				continue
			}
			if f, ok := byID[r.ID]; ok {
				f.matched = append(f.matched, q)
				continue
			}
			f := &found{repo: r, matched: []string{q}}
			byID[r.ID] = f
			order = append(order, f)
		}
	}
	for _, f := range order {
		res.Items = append(res.Items, toCandidate(f.repo, f.matched))
	}

	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("github fetch interrupted: %w", err)
	}
	if len(failures) > 0 {
		msg := fmt.Sprintf("%d of %d queries failed", len(failures), len(cfg.Queries))
		if skipped > 0 {
			msg += fmt.Sprintf(" (%d not attempted)", skipped)
		}
		return res, fmt.Errorf("%s; first: %w", msg, failures[0])
	}
	return res, nil
}

// stopsFetch reports whether an error would repeat for every remaining
// query: a rate limit, or a rejected token.
func stopsFetch(err error) bool {
	se, ok := httpx.AsStatusError(err)
	return ok && (se.RateLimited || se.StatusCode == http.StatusUnauthorized)
}

// explain adds a hint to errors caused by the token. The token itself never
// appears: httpx errors carry no request headers.
func (a *Adapter) explain(err error) error {
	se, ok := httpx.AsStatusError(err)
	if !ok {
		return err
	}
	switch {
	case se.StatusCode == http.StatusUnauthorized && a.authenticated:
		return fmt.Errorf("%w (GITHUB_TOKEN was rejected; check or unset it)", err)
	case se.RateLimited && !a.authenticated:
		return fmt.Errorf("%w (unauthenticated limit; setting GITHUB_TOKEN raises it)", err)
	}
	return err
}
