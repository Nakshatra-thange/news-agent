package httpx

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// StatusError is a non-2xx response that was not (or no longer) retried.
type StatusError struct {
	Upstream   string
	URL        string // scheme, host and path only
	StatusCode int
	Status     string
	// RateLimited is set when the upstream signalled a rate limit.
	RateLimited bool
	// RetryAfter is the server-requested wait, if any.
	RetryAfter time.Duration
	// Body is a short, sanitized excerpt of the response body.
	Body     string
	Attempts int
}

func (e *StatusError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: GET %s: HTTP %d %s", e.Upstream, e.URL, e.StatusCode, e.Status)
	if e.RateLimited {
		b.WriteString(" (rate limited")
		if e.RetryAfter > 0 {
			fmt.Fprintf(&b, ", retry after %s", e.RetryAfter.Round(time.Second))
		}
		b.WriteString(")")
	}
	if e.Attempts > 1 {
		fmt.Fprintf(&b, " after %d attempts", e.Attempts)
	}
	if e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	return b.String()
}

// AsStatusError returns the *StatusError in err's chain, if any.
func AsStatusError(err error) (*StatusError, bool) {
	var se *StatusError
	ok := errors.As(err, &se)
	return se, ok
}

// redact keeps scheme, host and path: query strings can carry API keys.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// scrub removes the full URL (with its query) that *url.Error embeds in
// transport error messages, keeping the underlying cause.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

const maxSnippet = 300

func snippet(body []byte) string {
	s := strings.ToValidUTF8(string(body[:min(len(body), maxSnippet*2)]), "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxSnippet {
		s = s[:maxSnippet]
		for len(s) > 0 && s[len(s)-1]&0xC0 == 0x80 {
			s = s[:len(s)-1]
		}
		s += "..."
	}
	return s
}
