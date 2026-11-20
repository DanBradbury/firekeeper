// Package parallel runs independent calls on goroutines and waits for all of
// them. It is the one implementation of Firekeeper's fan-out rules (see
// "Concurrency" in AGENTS.md): Each for per-provider work, one goroutine per
// item, and Bounded for file and subprocess work, at most
// runtime.GOMAXPROCS(0) goroutines at once.
//
// Callers keep output deterministic by having fn(i) write only to index i of
// a slice they own, and merge the results on their own goroutine afterwards.
package parallel

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
)

// Panic is a panic from one call, carried from its goroutine to the caller of
// Each or Bounded with the stack of the goroutine that panicked. Index is the
// call's index, so a caller can name the item that failed.
type Panic struct {
	Index int
	Value any
	Stack []byte
}

func (p *Panic) Error() string {
	return fmt.Sprintf("panic in parallel call %d: %v\n\n%s", p.Index, p.Value, p.Stack)
}

// Unwrap returns the original panic value when it was an error.
func (p *Panic) Unwrap() error {
	err, _ := p.Value.(error)
	return err
}

// Each calls fn(0) through fn(n-1), each on its own goroutine, and returns
// once every call has returned. Use it for per-provider work, where n is
// small and fixed.
func Each(n int, fn func(i int)) {
	run(n, n, fn)
}

// Bounded calls fn(0) through fn(n-1) on at most runtime.GOMAXPROCS(0)
// goroutines at once, and returns once every call has returned. Use it for
// per-file and per-command work, where n depends on the data.
func Bounded(n int, fn func(i int)) {
	run(n, runtime.GOMAXPROCS(0), fn)
}

// run calls fn for every index on at most limit goroutines at once. With n
// of 1 it calls fn on the caller's goroutine, as a loop would. A panic in a
// call is raised again on the caller's goroutine, always as a *Panic, after
// every call has returned; with several, the first by index. That keeps a
// panic recoverable by the caller: Bubble Tea, for one, recovers a panic in
// a command and restores the terminal, which it cannot do for a panic on a
// goroutine it did not start. Wrapping the single inline call too means a
// *Panic's Index always names a call of this run, even when fn itself runs
// a nested Each or Bounded that panicked.
func run(n, limit int, fn func(i int)) {
	if n <= 0 {
		return
	}
	panics := make([]*Panic, n)
	if n == 1 {
		call(0, fn, panics)
	} else {
		var wg sync.WaitGroup
		slots := make(chan struct{}, max(limit, 1))
		for i := range n {
			slots <- struct{}{}
			wg.Go(func() {
				defer func() { <-slots }()
				call(i, fn, panics)
			})
		}
		wg.Wait()
	}
	for _, caught := range panics {
		if caught != nil {
			panic(caught)
		}
	}
}

// call runs fn(i), recording a panic at panics[i] with the stack of the
// goroutine that panicked.
func call(i int, fn func(i int), panics []*Panic) {
	defer func() {
		if value := recover(); value != nil {
			panics[i] = &Panic{Index: i, Value: value, Stack: debug.Stack()}
		}
	}()
	fn(i)
}
