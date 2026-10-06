package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// script serves the given status codes in order (repeating the last), with
// optional headers per response.
type script struct {
	mu      sync.Mutex
	codes   []int
	headers []http.Header
	body    string
	hits    atomic.Int32
	lastReq *http.Request
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(s.hits.Add(1)) - 1
	s.mu.Lock()
	s.lastReq = r.Clone(context.Background())
	s.mu.Unlock()
	i := min(n, len(s.codes)-1)
	if i < len(s.headers) {
		for k, vs := range s.headers[i] {
			w.Header()[k] = vs
		}
	}
	w.WriteHeader(s.codes[i])
	body := s.body
	if body == "" {
		body = http.StatusText(s.codes[i])
	}
	_, _ = w.Write([]byte(body))
}

// newTestClient returns a client whose sleeps are recorded instead of slept.
func newTestClient(o Options) (*Client, *[]time.Duration) {
	c := New(o)
	var slept []time.Duration
	var mu sync.Mutex
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return ctx.Err()
	}
	c.jitter = func(d time.Duration) time.Duration { return d } // deterministic
	return c, &slept
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGetSuccess(t *testing.T) {
	s := &script{codes: []int{200}, body: `{"ok":true}`}
	url := serve(t, s)
	c, slept := newTestClient(Options{Name: "test", UserAgent: "SynergyTest/1"})

	resp, err := c.Get(context.Background(), url+"/x?q=1", http.Header{"Accept": {"application/json"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(resp.Body) != `{"ok":true}` || resp.Attempts != 1 || resp.StatusCode != 200 {
		t.Errorf("resp = %+v", resp)
	}
	if len(*slept) != 0 {
		t.Errorf("slept %v on success", *slept)
	}
	if ua := s.lastReq.Header.Get("User-Agent"); ua != "SynergyTest/1" {
		t.Errorf("User-Agent = %q", ua)
	}
	if a := s.lastReq.Header.Get("Accept"); a != "application/json" {
		t.Errorf("Accept = %q", a)
	}
}

func TestRetriesTransientStatusThenSucceeds(t *testing.T) {
	for _, code := range []int{408, 502, 503, 504} {
		s := &script{codes: []int{code, 200}}
		c, slept := newTestClient(Options{Name: "test", Retry: RetryPolicy{BaseDelay: 100 * time.Millisecond}})
		resp, err := c.Get(context.Background(), serve(t, s), nil)
		if err != nil {
			t.Fatalf("%d: Get: %v", code, err)
		}
		if resp.Attempts != 2 || s.hits.Load() != 2 {
			t.Errorf("%d: attempts=%d hits=%d, want 2", code, resp.Attempts, s.hits.Load())
		}
		if len(*slept) != 1 || (*slept)[0] != 100*time.Millisecond {
			t.Errorf("%d: slept %v, want [100ms]", code, *slept)
		}
	}
}

func TestRetriesAreBoundedWithExponentialBackoff(t *testing.T) {
	s := &script{codes: []int{503}}
	c, slept := newTestClient(Options{Name: "up", Retry: RetryPolicy{
		MaxAttempts: 4, BaseDelay: 100 * time.Millisecond, MaxDelay: 300 * time.Millisecond,
	}})
	_, err := c.Get(context.Background(), serve(t, s), nil)

	se, ok := AsStatusError(err)
	if !ok || se.StatusCode != 503 || se.Attempts != 4 {
		t.Fatalf("error = %v, want StatusError 503 after 4 attempts", err)
	}
	if s.hits.Load() != 4 {
		t.Errorf("hits = %d, want 4", s.hits.Load())
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond} // capped
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, want %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Errorf("sleep %d = %v, want %v", i, (*slept)[i], want[i])
		}
	}
}

func TestNonTransientStatusNotRetried(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 410, 422, 500, 501} {
		s := &script{codes: []int{code}}
		c, slept := newTestClient(Options{Name: "test"})
		_, err := c.Get(context.Background(), serve(t, s), nil)
		se, ok := AsStatusError(err)
		if !ok || se.StatusCode != code {
			t.Errorf("%d: error = %v", code, err)
		}
		if s.hits.Load() != 1 || len(*slept) != 0 {
			t.Errorf("%d: hits=%d slept=%v, want no retry", code, s.hits.Load(), *slept)
		}
	}
}

func TestRateLimitHonorsRetryAfter(t *testing.T) {
	s := &script{codes: []int{429, 200}, headers: []http.Header{{"Retry-After": {"2"}}}}
	c, slept := newTestClient(Options{Name: "test", Retry: RetryPolicy{BaseDelay: 100 * time.Millisecond}})
	resp, err := c.Get(context.Background(), serve(t, s), nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Attempts != 2 || len(*slept) != 1 || (*slept)[0] != 2*time.Second {
		t.Errorf("attempts=%d slept=%v, want one 2s wait", resp.Attempts, *slept)
	}
}

func TestRateLimitWithoutRetryAfterUsesBackoff(t *testing.T) {
	s := &script{codes: []int{429, 200}}
	c, slept := newTestClient(Options{Name: "test", Retry: RetryPolicy{BaseDelay: 250 * time.Millisecond}})
	if _, err := c.Get(context.Background(), serve(t, s), nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(*slept) != 1 || (*slept)[0] != 250*time.Millisecond {
		t.Errorf("slept %v, want [250ms]", *slept)
	}
}

func TestLongRetryAfterFailsFast(t *testing.T) {
	s := &script{codes: []int{429}, headers: []http.Header{{"Retry-After": {"3600"}}}}
	c, slept := newTestClient(Options{Name: "test", Retry: RetryPolicy{MaxRetryAfter: time.Minute}})
	_, err := c.Get(context.Background(), serve(t, s), nil)

	se, ok := AsStatusError(err)
	if !ok || !se.RateLimited || se.RetryAfter != time.Hour {
		t.Fatalf("error = %v, want rate-limited StatusError with 1h RetryAfter", err)
	}
	if s.hits.Load() != 1 || len(*slept) != 0 {
		t.Errorf("hits=%d slept=%v, want an immediate failure", s.hits.Load(), *slept)
	}
	if !strings.Contains(err.Error(), "rate limited, retry after 1h0m0s") {
		t.Errorf("message = %q", err)
	}
}

func TestCustomRateLimitDetector(t *testing.T) {
	// GitHub-style: 403 with X-RateLimit-Remaining: 0.
	s := &script{codes: []int{403, 200}, headers: []http.Header{{"X-Ratelimit-Remaining": {"0"}}}}
	c, slept := newTestClient(Options{Name: "github", Retry: RetryPolicy{
		BaseDelay: 10 * time.Millisecond,
		RateLimited: func(r *http.Response) (time.Duration, bool) {
			return 5 * time.Second, r.StatusCode == 403 && r.Header.Get("X-RateLimit-Remaining") == "0"
		},
	}})
	if _, err := c.Get(context.Background(), serve(t, s), nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(*slept) != 1 || (*slept)[0] != 5*time.Second {
		t.Errorf("slept %v, want [5s]", *slept)
	}
}

func TestNetworkErrorsRetried(t *testing.T) {
	// A closed listener refuses connections.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c, slept := newTestClient(Options{Name: "test", Retry: RetryPolicy{MaxAttempts: 3}})
	_, err = c.Get(context.Background(), "http://"+addr+"/x", nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v, want connection refused", err)
	}
	if len(*slept) != 2 {
		t.Errorf("slept %d times, want 2 (3 attempts)", len(*slept))
	}
}

func TestPerAttemptTimeoutIsRetried(t *testing.T) {
	var hits atomic.Int32
	url := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		w.WriteHeader(200)
	}))
	c, _ := newTestClient(Options{Name: "test", Timeout: 100 * time.Millisecond})
	resp, err := c.Get(context.Background(), url, nil)
	if err != nil || resp.Attempts != 2 {
		t.Fatalf("resp=%+v err=%v, want success on attempt 2", resp, err)
	}
}

func TestNonTransientNetErrors(t *testing.T) {
	if transientNetErr(&net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}) {
		t.Error("unknown host should not be retried")
	}
	if !transientNetErr(&net.DNSError{Err: "timeout", Name: "x", IsTimeout: true}) {
		t.Error("DNS timeout should be retried")
	}
	if transientNetErr(ErrBodyTooLarge) {
		t.Error("oversized body should not be retried")
	}
}

func TestContextCancelledDuringRetryWait(t *testing.T) {
	s := &script{codes: []int{503}}
	url := serve(t, s)
	c := New(Options{Name: "test", Retry: RetryPolicy{BaseDelay: 10 * time.Second}}) // real sleep

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.Get(ctx, url, nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("cancellation took %v; backoff sleep must be interruptible", time.Since(start))
	}
	if s.hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", s.hits.Load())
	}
}

func TestContextDeadlineDuringRequestNotRetried(t *testing.T) {
	var hits atomic.Int32
	url := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done()
	}))
	c, slept := newTestClient(Options{Name: "test"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.Get(ctx, url, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	if hits.Load() != 1 || len(*slept) != 0 {
		t.Errorf("hits=%d slept=%v, want no retry after the caller's deadline", hits.Load(), *slept)
	}
}

func TestBodyTooLarge(t *testing.T) {
	s := &script{codes: []int{200}, body: strings.Repeat("x", 2048)}
	c, slept := newTestClient(Options{Name: "test", MaxBodyBytes: 1024})
	_, err := c.Get(context.Background(), serve(t, s), nil)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrBodyTooLarge", err)
	}
	if s.hits.Load() != 1 || len(*slept) != 0 {
		t.Errorf("hits=%d slept=%v, want no retry", s.hits.Load(), *slept)
	}
}

func TestErrorsNeverLeakSecrets(t *testing.T) {
	s := &script{codes: []int{401}, body: "bad credentials"}
	url := serve(t, s)
	c, _ := newTestClient(Options{Name: "test"})
	_, err := c.Get(context.Background(), url+"/search?api_key=SECRET123&q=llm",
		http.Header{"Authorization": {"Bearer TOKEN456"}})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET123") || strings.Contains(msg, "TOKEN456") {
		t.Errorf("error leaks credentials: %q", msg)
	}
	if !strings.Contains(msg, "/search") || !strings.Contains(msg, "401") || !strings.Contains(msg, "bad credentials") {
		t.Errorf("error lacks useful context: %q", msg)
	}

	// Transport errors embed the full URL; it must be scrubbed too.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c2, _ := newTestClient(Options{Name: "test", Retry: RetryPolicy{MaxAttempts: 1}})
	_, err = c2.Get(context.Background(), "http://"+addr+"/x?token=SECRET789", nil)
	if err == nil || strings.Contains(err.Error(), "SECRET789") {
		t.Errorf("transport error leaks query: %v", err)
	}
}

func TestLimiterPacesEveryAttempt(t *testing.T) {
	s := &script{codes: []int{503, 503, 200}}
	url := serve(t, s)
	lim := rate.NewLimiter(rate.Every(100*time.Millisecond), 1)
	c, _ := newTestClient(Options{Name: "test", Limiter: lim})

	start := time.Now()
	if _, err := c.Get(context.Background(), url, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// 3 attempts at 1 per 100ms with burst 1: at least ~200ms even though the
	// recorded backoff sleeps are instant.
	if d := time.Since(start); d < 180*time.Millisecond {
		t.Errorf("3 attempts took %v, want >= ~200ms of limiter pacing", d)
	}
}

func TestLimiterWaitHonorsContext(t *testing.T) {
	lim := rate.NewLimiter(rate.Every(time.Hour), 1)
	lim.Allow() // drain the only token
	c, _ := newTestClient(Options{Name: "test", Limiter: lim})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Get(ctx, "http://127.0.0.1:1/never", nil)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v, want a prompt limiter failure", err, time.Since(start))
	}
}

func TestLimitersShareByKey(t *testing.T) {
	l := NewLimiters()
	a1 := l.Get("arxiv", 3*time.Second, 1)
	a2 := l.Get("arxiv", time.Millisecond, 99) // later args ignored
	g := l.Get("github", 6*time.Second, 1)
	if a1 != a2 {
		t.Error("same key must return the same limiter")
	}
	if a1 == g {
		t.Error("different keys must not share a limiter")
	}
	if a1.Limit() != rate.Every(3*time.Second) || a1.Burst() != 1 {
		t.Errorf("limit=%v burst=%d", a1.Limit(), a1.Burst())
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		v    string
		want time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"garbage", 0},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
	}
	for _, tt := range tests {
		h := http.Header{}
		if tt.v != "" {
			h.Set("Retry-After", tt.v)
		}
		if got := RetryAfter(h, now); got != tt.want {
			t.Errorf("RetryAfter(%q) = %v, want %v", tt.v, got, tt.want)
		}
	}
}

func TestEqualJitterRange(t *testing.T) {
	for range 1000 {
		d := equalJitter(time.Second)
		if d < 500*time.Millisecond || d > time.Second {
			t.Fatalf("jitter %v outside [500ms, 1s]", d)
		}
	}
	if equalJitter(1) != 1 {
		t.Error("tiny durations should pass through")
	}
}

func TestBackoffOverflowIsCapped(t *testing.T) {
	c, _ := newTestClient(Options{Retry: RetryPolicy{BaseDelay: time.Second, MaxDelay: 30 * time.Second}})
	if d := c.backoff(80); d != 30*time.Second {
		t.Errorf("backoff(80) = %v, want cap 30s", d)
	}
}
