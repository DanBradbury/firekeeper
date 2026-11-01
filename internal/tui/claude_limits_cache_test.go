package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

func TestClaudeLimitsCacheBacksOffOnRateLimit(t *testing.T) {
	cache := &claudeLimitsCache{}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	good := claudelimits.Limits{SubscriptionTag: "max"}
	calls := 0
	fetch := func(at time.Time, result claudelimits.Limits, err error) (claudelimits.Limits, error) {
		return cache.fetch(at, func() (claudelimits.Limits, error) { calls++; return result, err })
	}

	if _, err := fetch(now, good, nil); err != nil || calls != 1 {
		t.Fatalf("first fetch: err=%v calls=%d", err, calls)
	}
	// Within the minimum interval the endpoint is not asked again.
	if got, err := fetch(now.Add(10*time.Second), claudelimits.Limits{}, nil); err != nil || calls != 1 || got.SubscriptionTag != "max" {
		t.Fatalf("throttled fetch: got=%+v err=%v calls=%d", got, err, calls)
	}
	// A 429 keeps the last good limits and shows no error.
	limited := &claudelimits.RateLimitedError{RetryAfter: 10 * time.Minute}
	if got, err := fetch(now.Add(2*time.Minute), claudelimits.Limits{}, limited); err != nil || calls != 2 || got.SubscriptionTag != "max" {
		t.Fatalf("429 fetch: got=%+v err=%v calls=%d", got, err, calls)
	}
	if _, err := fetch(now.Add(5*time.Minute), good, nil); err != nil || calls != 2 {
		t.Fatalf("fetch during backoff: err=%v calls=%d", err, calls)
	}
	if _, err := fetch(now.Add(13*time.Minute), good, nil); err != nil || calls != 3 {
		t.Fatalf("fetch after backoff: err=%v calls=%d", err, calls)
	}
}

func TestClaudeLimitsCacheRateLimitedWithoutHistoryReturnsError(t *testing.T) {
	cache := &claudeLimitsCache{}
	now := time.Now()
	_, err := cache.fetch(now, func() (claudelimits.Limits, error) {
		return claudelimits.Limits{}, &claudelimits.RateLimitedError{}
	})
	var limited *claudelimits.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("err = %v, want RateLimitedError", err)
	}
	_, err = cache.fetch(now.Add(time.Minute), func() (claudelimits.Limits, error) {
		t.Fatal("endpoint called during backoff")
		return claudelimits.Limits{}, nil
	})
	if !errors.As(err, &limited) {
		t.Fatalf("err during backoff = %v, want RateLimitedError", err)
	}
}
