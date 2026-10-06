// Package httpx is the polite HTTP client source adapters use to call
// upstream APIs. It adds what net/http lacks for that job:
//
//   - per-upstream rate limiting (a shared token bucket, see Limiters)
//   - bounded retries of transient failures only, with exponential backoff,
//     jitter and Retry-After support, all cancellable through the context
//   - per-attempt timeouts, a response size cap and a fixed User-Agent
//   - errors that carry the status code and retry hints but never request
//     headers (where credentials live) or query strings
package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// Defaults.
const (
	DefaultTimeout       = 30 * time.Second
	DefaultMaxBodyBytes  = 10 << 20
	DefaultMaxAttempts   = 3
	DefaultBaseDelay     = time.Second
	DefaultMaxDelay      = 30 * time.Second
	DefaultMaxRetryAfter = time.Minute
	DefaultUserAgent     = "Synergy/0.1 (personal AI research aggregator)"
)

// ErrBodyTooLarge reports a response larger than Options.MaxBodyBytes.
var ErrBodyTooLarge = errors.New("response body too large")

// RetryPolicy bounds retries.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts int
	// BaseDelay and MaxDelay bound exponential backoff between attempts.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// MaxRetryAfter is the longest server-requested wait honored. A longer
	// Retry-After fails fast with a *StatusError carrying RetryAfter rather
	// than stalling a fetch.
	MaxRetryAfter time.Duration
	// RateLimited recognizes rate-limit responses and how long to wait. The
	// default treats 429 as rate limited and reads Retry-After. Adapters for
	// APIs with other conventions (e.g. GitHub's 403 with
	// X-RateLimit-Remaining: 0) supply their own.
	RateLimited func(*http.Response) (wait time.Duration, limited bool)
}

// Options configures a Client. Zero values use the defaults.
type Options struct {
	// Name identifies the upstream in logs and errors, e.g. "arxiv".
	Name         string
	UserAgent    string
	Timeout      time.Duration // per attempt, including reading the body
	MaxBodyBytes int64
	// Limiter paces every attempt, retries included. Share one limiter per
	// upstream (see Limiters). Nil disables pacing.
	Limiter *rate.Limiter
	Retry   RetryPolicy
	Logger  *slog.Logger
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
}

// Client is safe for concurrent use.
type Client struct {
	name      string
	userAgent string
	maxBody   int64
	limiter   *rate.Limiter
	retry     RetryPolicy
	logger    *slog.Logger
	http      *http.Client

	// Test hooks.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func(d time.Duration) time.Duration
	now    func() time.Time
}

// New builds a Client.
func New(o Options) *Client {
	if o.UserAgent == "" {
		o.UserAgent = DefaultUserAgent
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	r := o.Retry
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = DefaultMaxAttempts
	}
	if r.BaseDelay <= 0 {
		r.BaseDelay = DefaultBaseDelay
	}
	if r.MaxDelay <= 0 {
		r.MaxDelay = DefaultMaxDelay
	}
	if r.MaxRetryAfter <= 0 {
		r.MaxRetryAfter = DefaultMaxRetryAfter
	}
	if r.RateLimited == nil {
		r.RateLimited = TooManyRequests
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Client{
		name: o.Name, userAgent: o.UserAgent, maxBody: o.MaxBodyBytes, limiter: o.Limiter, retry: r,
		logger: o.Logger,
		http:   &http.Client{Timeout: o.Timeout, Transport: o.Transport},
		sleep:  sleepCtx,
		jitter: equalJitter,
		now:    time.Now,
	}
}

// Response is a fully read, successful (2xx) response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	// Attempts is how many requests were made, retries included.
	Attempts int
}

// Get fetches rawURL, retrying transient failures. Any non-2xx outcome is
// returned as a *StatusError; transport failures and cancellation as other
// errors. Every attempt waits for the rate limiter first.
func (c *Client) Get(ctx context.Context, rawURL string, header http.Header) (*Response, error) {
	for attempt := 1; ; attempt++ {
		if c.limiter != nil {
			if err := c.limiter.Wait(ctx); err != nil {
				return nil, fmt.Errorf("%s: waiting for rate limiter: %w", c.name, ctxErr(ctx, err))
			}
		}

		resp, err := c.attempt(ctx, rawURL, header)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Attempts = attempt
			return resp, nil
		}

		wait, retryable, final := c.classify(ctx, rawURL, resp, err, attempt)
		if !retryable || attempt >= c.retry.MaxAttempts {
			return nil, final
		}
		c.logger.DebugContext(ctx, "http retry", "upstream", c.name, "attempt", attempt,
			"wait", wait.String(), "reason", final.Error())
		if err := c.sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("%s: retry wait: %w (after %v)", c.name, err, final)
		}
	}
}

func (c *Client) attempt(ctx context.Context, rawURL string, header http.Header) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("User-Agent", c.userAgent)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, c.maxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > c.maxBody {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrBodyTooLarge, c.maxBody)
	}
	return &Response{StatusCode: res.StatusCode, Header: res.Header, Body: body}, nil
}

// classify decides whether a failed attempt is worth retrying and how long
// to wait, and builds the error to return if it is not retried.
func (c *Client) classify(ctx context.Context, rawURL string, resp *Response, err error, attempt int) (time.Duration, bool, error) {
	backoff := c.backoff(attempt)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, fmt.Errorf("%s: GET %s: %w", c.name, redact(rawURL), ctx.Err())
		}
		wrapped := fmt.Errorf("%s: GET %s: %w", c.name, redact(rawURL), scrub(err))
		return backoff, transientNetErr(err), wrapped
	}

	se := &StatusError{
		Upstream: c.name, URL: redact(rawURL), StatusCode: resp.StatusCode,
		Status: http.StatusText(resp.StatusCode), Body: snippet(resp.Body), Attempts: attempt,
	}
	httpResp := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header}
	if wait, limited := c.retry.RateLimited(httpResp); limited {
		se.RateLimited, se.RetryAfter = true, wait
		if wait > c.retry.MaxRetryAfter {
			return 0, false, se // too long to wait inside a fetch: fail fast
		}
		return max(wait, backoff), true, se
	}
	switch resp.StatusCode {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		if ra := RetryAfter(resp.Header, c.now()); ra > 0 {
			se.RetryAfter = ra
			if ra > c.retry.MaxRetryAfter {
				return 0, false, se
			}
			return max(ra, backoff), true, se
		}
		return backoff, true, se
	}
	return 0, false, se
}

// backoff returns the jittered exponential delay before attempt+1.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.retry.BaseDelay << (attempt - 1)
	if d <= 0 || d > c.retry.MaxDelay { // <= 0 guards shift overflow
		d = c.retry.MaxDelay
	}
	return c.jitter(d)
}

// equalJitter picks a delay in [d/2, d]: spreads retries out while keeping a
// guaranteed minimum spacing.
func equalJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half+1)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// transientNetErr reports whether a transport error may succeed on retry.
// Certificate problems, unknown hosts and malformed URLs will not.
func transientNetErr(err error) bool {
	var (
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
		unkAuth x509.UnknownAuthorityError
		hostErr x509.HostnameError
	)
	switch {
	case errors.Is(err, ErrBodyTooLarge):
		return false
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return false
	case errors.As(err, &certErr), errors.As(err, &unkAuth), errors.As(err, &hostErr):
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Op == "parse" {
		return false
	}
	return true
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// TooManyRequests is the default RateLimited detector: HTTP 429, waiting as
// long as Retry-After asks (0 means "use backoff").
func TooManyRequests(r *http.Response) (time.Duration, bool) {
	if r.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	return RetryAfter(r.Header, time.Now()), true
}

// RetryAfter parses a Retry-After header given in seconds or as an HTTP
// date. It returns 0 when the header is absent, invalid or in the past.
func RetryAfter(h http.Header, now time.Time) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return max(time.Duration(secs)*time.Second, 0)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}
