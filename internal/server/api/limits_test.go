package api

import (
	"testing"
	"time"
)

func TestRateLimiterRollingWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d refused", i)
		}
		now = now.Add(10 * time.Second)
	}
	// 20s in: both slots are used, the first frees 40s from now.
	ok, wait := l.allow("a")
	if ok || wait != 40*time.Second {
		t.Fatalf("third request = %v, %v; want refused, 40s", ok, wait)
	}
	// A refused request does not extend the penalty.
	now = now.Add(40 * time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("first slot did not free after the window")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("second slot freed early")
	}
	// Keys are independent.
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("another key is limited by a")
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{{0, "1"}, {time.Millisecond, "1"}, {time.Second, "1"}, {1500 * time.Millisecond, "2"}, {40 * time.Second, "40"}} {
		if got := retryAfterSeconds(tc.d); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.d, got, tc.want)
		}
	}
}
