package sources

import (
	"log/slog"
	"net/http"
	"time"

	"synergy/internal/httpx"
)

// ClientOptions are the HTTP dependencies every adapter takes. Production
// code sets UserAgent, Limiters and Logger; the remaining fields exist so
// tests can point an adapter at a local fixture server and run fast.
type ClientOptions struct {
	UserAgent string
	// Limiters is shared process-wide so all sources of a type share their
	// upstream's request budget. Nil gives the adapter a private limiter.
	Limiters *httpx.Limiters
	Logger   *slog.Logger

	// BaseURL overrides the upstream API root.
	BaseURL string
	// Transport overrides the HTTP transport.
	Transport http.RoundTripper
	// Retry overrides retry timing; zero fields use the adapter's defaults.
	Retry httpx.RetryPolicy
	// RequestInterval overrides the adapter's politeness spacing.
	RequestInterval time.Duration
	// Now overrides the clock.
	Now func() time.Time
}

// Clock returns the configured clock or time.Now.
func (o ClientOptions) Clock() func() time.Time {
	if o.Now != nil {
		return o.Now
	}
	return time.Now
}

// NewClient builds an httpx client for an upstream. key names the upstream
// (limiter key and error prefix); interval and burst define its default
// politeness; retry fields left zero by the caller's options take the given
// defaults.
func (o ClientOptions) NewClient(key string, interval time.Duration, burst int, retry httpx.RetryPolicy) *httpx.Client {
	if o.RequestInterval > 0 {
		interval = o.RequestInterval
	}
	lims := o.Limiters
	if lims == nil {
		lims = httpx.NewLimiters()
	}
	r := o.Retry
	if r.MaxAttempts == 0 {
		r.MaxAttempts = retry.MaxAttempts
	}
	if r.BaseDelay == 0 {
		r.BaseDelay = retry.BaseDelay
	}
	if r.MaxDelay == 0 {
		r.MaxDelay = retry.MaxDelay
	}
	if r.MaxRetryAfter == 0 {
		r.MaxRetryAfter = retry.MaxRetryAfter
	}
	if r.RateLimited == nil {
		r.RateLimited = retry.RateLimited
	}
	return httpx.New(httpx.Options{
		Name:      key,
		UserAgent: o.UserAgent,
		Limiter:   lims.Get(key, interval, burst),
		Retry:     r,
		Logger:    o.Logger,
		Transport: o.Transport,
	})
}
