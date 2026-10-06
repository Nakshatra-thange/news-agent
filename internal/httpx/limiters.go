package httpx

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiters hands out one shared rate limiter per upstream key. Upstream APIs
// limit clients (per IP or token), not per Synergy source, so every source of
// a type shares its type's budget: two arXiv sources together still send at
// most one request per 3 seconds. (Per-source fetch frequency is governed
// separately by each source's min_fetch_interval.)
type Limiters struct {
	mu sync.Mutex
	m  map[string]*rate.Limiter
}

// NewLimiters returns an empty set.
func NewLimiters() *Limiters {
	return &Limiters{m: make(map[string]*rate.Limiter)}
}

// Get returns the limiter for key, creating it with one request per
// interval and the given burst on first use. Later calls for the same key
// return the same limiter regardless of their arguments.
func (l *Limiters) Get(key string, interval time.Duration, burst int) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lim, ok := l.m[key]; ok {
		return lim
	}
	lim := rate.NewLimiter(rate.Every(interval), max(burst, 1))
	l.m[key] = lim
	return lim
}
