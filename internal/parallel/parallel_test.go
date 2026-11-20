package parallel

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// peakConcurrency runs call over n indexes and reports how many calls ran
// at once at most, failing unless every index ran exactly once.
func peakConcurrency(t *testing.T, call func(int, func(int)), n int, during func()) int {
	t.Helper()
	calls := make([]int, n)
	var mu sync.Mutex
	running, peak := 0, 0
	call(n, func(i int) {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		during()
		calls[i]++
		mu.Lock()
		running--
		mu.Unlock()
	})
	for i, c := range calls {
		if c != 1 {
			t.Fatalf("index %d called %d times", i, c)
		}
	}
	return peak
}

func TestBoundedCallsEveryIndexOnceWithinBound(t *testing.T) {
	limit := runtime.GOMAXPROCS(0)
	for _, n := range []int{0, 1, 2, limit, 3*limit + 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			peak := peakConcurrency(t, Bounded, n, func() { time.Sleep(time.Millisecond) })
			if peak > limit {
				t.Fatalf("peak concurrency %d exceeds GOMAXPROCS %d", peak, limit)
			}
		})
	}
}

// Each runs every call at once, even past GOMAXPROCS: each call waits until
// all n have started, which only finishes when none is held back.
func TestEachRunsEveryIndexAtOnce(t *testing.T) {
	limit := runtime.GOMAXPROCS(0)
	for _, n := range []int{0, 1, 2, 2*limit + 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var started sync.WaitGroup
			if n > 1 {
				started.Add(n)
			}
			all := make(chan struct{})
			go func() { started.Wait(); close(all) }()
			peak := peakConcurrency(t, Each, n, func() {
				if n <= 1 {
					return // on the caller's goroutine; nothing to wait for
				}
				started.Done()
				select {
				case <-all:
				case <-time.After(10 * time.Second):
					t.Error("not every call started: Each held some back")
				}
			})
			if n > 1 && peak != n {
				t.Fatalf("peak concurrency %d, want all %d", peak, n)
			}
		})
	}
}

func TestNegativeCountCallsNothing(t *testing.T) {
	for name, call := range map[string]func(int, func(int)){"Each": Each, "Bounded": Bounded} {
		t.Run(name, func(t *testing.T) {
			call(-1, func(int) { t.Fatal("fn called for negative n") })
		})
	}
}

// A single call runs on the caller's goroutine, as a loop would, and its
// panic is still a *Panic, so callers handle one shape.
func TestSingleCallPanicIsWrapped(t *testing.T) {
	for name, call := range map[string]func(int, func(int)){"Each": Each, "Bounded": Bounded} {
		t.Run(name, func(t *testing.T) {
			var caught any
			func() {
				defer func() { caught = recover() }()
				call(1, func(int) { panic("fixture panic") })
			}()
			p, ok := caught.(*Panic)
			if !ok || p.Index != 0 || p.Value != "fixture panic" {
				t.Fatalf("recovered %#v, want *Panic{Index: 0, Value: \"fixture panic\"}", caught)
			}
		})
	}
}

// A nested run's panic is carried as the outer call's Value, so the outer
// Index names the outer item, not the inner one.
func TestNestedPanicKeepsOuterIndex(t *testing.T) {
	for _, n := range []int{1, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var caught any
			func() {
				defer func() { caught = recover() }()
				Each(n, func(outer int) {
					if outer == n-1 {
						Bounded(6, func(inner int) {
							if inner == 5 {
								panic("fixture panic")
							}
						})
					}
				})
			}()
			p, ok := caught.(*Panic)
			if !ok || p.Index != n-1 {
				t.Fatalf("recovered %#v, want the outer call %d", caught, n-1)
			}
			inner, ok := p.Value.(*Panic)
			if !ok || inner.Index != 5 || inner.Value != "fixture panic" {
				t.Fatalf("Value = %#v, want the inner *Panic at index 5", p.Value)
			}
		})
	}
}

// A panic in one call reaches the caller, on the caller's goroutine, after
// every other call has returned. It keeps the original value, the index and
// the stack of the goroutine that panicked, and with several panics it is
// the first by index.
func TestPanicReachesCaller(t *testing.T) {
	cause := errors.New("fixture panic")
	for _, call := range []struct {
		name string
		run  func(int, func(int))
	}{{"Each", Each}, {"Bounded", Bounded}} {
		for _, tc := range []struct {
			name      string
			panics    map[int]any
			index     int
			want      string
			unwrapped error
		}{
			{"error value", map[int]any{3: cause}, 3, "fixture panic", cause},
			{"string value", map[int]any{3: "fixture panic"}, 3, "fixture panic", nil},
			{"two calls", map[int]any{5: "later", 1: "first"}, 1, "first", nil},
		} {
			t.Run(call.name+"/"+tc.name, func(t *testing.T) {
				const n = 8
				var finished atomic.Int32
				var caught any
				func() {
					defer func() { caught = recover() }()
					call.run(n, func(i int) {
						if value, ok := tc.panics[i]; ok {
							panic(value)
						}
						finished.Add(1)
					})
				}()
				p, ok := caught.(*Panic)
				if !ok {
					t.Fatalf("recovered %v, want a *Panic", caught)
				}
				if p.Index != tc.index {
					t.Errorf("Index = %d, want %d", p.Index, tc.index)
				}
				message := p.Error()
				if want := fmt.Sprintf("panic in parallel call %d: %s", tc.index, tc.want); !strings.HasPrefix(message, want) {
					t.Errorf("panic = %.80q, want it to start with %q", message, want)
				}
				if !strings.Contains(message, "TestPanicReachesCaller") {
					t.Error("panic message lost the stack of the goroutine that panicked")
				}
				if got := errors.Unwrap(p); got != tc.unwrapped {
					t.Errorf("Unwrap = %v, want %v", got, tc.unwrapped)
				}
				if got, want := int(finished.Load()), n-len(tc.panics); got != want {
					t.Errorf("%d other calls finished before the panic was raised, want %d", got, want)
				}
			})
		}
	}
}
