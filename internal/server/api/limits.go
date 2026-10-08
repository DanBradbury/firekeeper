package api

import (
	"strconv"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// Limits caps what one account may do. Zero means no limit. They protect a
// shared server from one account filling the disk or flooding ingest. The
// built-in default account, which owns single-user data, is exempt.
type Limits struct {
	// MaxBytes caps stored event text plus raw payloads.
	MaxBytes int64 `json:"max_bytes"`
	// MaxSessions caps stored sessions.
	MaxSessions int64 `json:"max_sessions"`
	// IngestPerMinute caps POST /v1/ingest requests per rolling minute.
	IngestPerMinute int `json:"ingest_per_minute"`
}

// DefaultLimits are the limits `firekeeper serve` starts with.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 1 << 30, MaxSessions: 5000, IngestPerMinute: 120}
}

// store returns the limits the store enforces.
func (l Limits) store() store.Limits {
	return store.Limits{MaxBytes: l.MaxBytes, MaxSessions: l.MaxSessions}
}

// rateLimiter counts requests per key in a rolling window.
type rateLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

// allow counts a request for key. When the key has used its allowance it
// counts nothing and returns how long until a slot frees.
func (l *rateLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[key] = keep
		return false, keep[0].Add(l.window).Sub(now)
	}
	l.hits[key] = append(keep, now)
	return true, 0
}

// retryAfterSeconds renders a Retry-After header value, at least 1.
func retryAfterSeconds(d time.Duration) string {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.Itoa(s)
}
