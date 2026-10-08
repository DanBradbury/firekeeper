package auth

import (
	"sync"
	"time"
)

// limiter counts events per key in a sliding window. It backs both the
// bearer-token failure limit and the login and signup limits.
type limiter struct {
	max    int
	window time.Duration

	mu     sync.Mutex
	events map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, events: map[string][]time.Time{}}
}

// prune drops expired events for key. The caller holds l.mu.
func (l *limiter) prune(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.events[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.events, key)
	} else {
		l.events[key] = keep
	}
	return keep
}

// limited reports whether key has used up its allowance.
func (l *limiter) limited(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, now)) >= l.max
}

// record counts one event for key.
func (l *limiter) record(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Keys can be attacker-chosen (emails), so bound the map.
	if len(l.events) > 10000 {
		for k := range l.events {
			l.prune(k, now)
		}
	}
	l.events[key] = append(l.prune(key, now), now)
}
