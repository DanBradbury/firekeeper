package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// fakeClock records each wait and fires at once until limit waits, then
// cancels the daemon and never fires, so the loop's exit is deterministic.
type fakeClock struct {
	mu     sync.Mutex
	waits  []time.Duration
	limit  int
	cancel context.CancelFunc
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	if len(c.waits) >= c.limit {
		c.cancel()
		return nil
	}
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func uploading() reporter.Config {
	return reporter.Config{Server: "http://127.0.0.1:1", Providers: []transcript.Provider{transcript.ProviderCodex}}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		n      int
		random float64
		want   time.Duration
	}{
		{1, 0, 2 * time.Second},
		{2, 0, 4 * time.Second},
		{3, 0, 8 * time.Second},
		{8, 0, 256 * time.Second},
		{9, 0, 5 * time.Minute},
		{1_000_000, 0, 5 * time.Minute},
		// Jitter shortens by up to a quarter, never below 2s or above 5m.
		{1, 0.99, 2 * time.Second},
		{2, 0.5, 3500 * time.Millisecond},
		{3, 1, 6 * time.Second},
		{1_000_000, 1, 225 * time.Second},
	}
	for _, tt := range tests {
		if got := backoff(tt.n, func() float64 { return tt.random }); got != tt.want {
			t.Errorf("backoff(%d, %v) = %s, want %s", tt.n, tt.random, got, tt.want)
		}
	}
}

func TestRunBacksOffOnFailureAndResetsOnSuccess(t *testing.T) {
	// fail, fail, fail, ok, fail (heartbeat), ok, then stop.
	outcomes := []error{errors.New("down"), errors.New("down"), errors.New("down"), nil, nil, nil}
	heartbeats := []error{nil, errors.New("heartbeat: server returned 500"), nil}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeClock{limit: 6, cancel: cancel}
	passes, beats := 0, 0
	err := Run(ctx, Config{
		Reporter: uploading(),
		Dir:      t.TempDir(),
		Clock:    clock,
		Rand:     func() float64 { return 0 },
		RunOnce: func(context.Context, reporter.Config) (reporter.Summary, error) {
			err := outcomes[passes]
			passes++
			return reporter.Summary{}, err
		},
		Heartbeat: func(context.Context, reporter.Config, reporter.Summary) error {
			err := heartbeats[beats]
			beats++
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, DefaultInterval, 2 * time.Second, DefaultInterval}
	if fmt.Sprint(clock.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clock.waits, want)
	}
	if passes != 6 || beats != 3 {
		t.Fatalf("passes = %d, heartbeats = %d", passes, beats)
	}
}

func TestSessionErrorsCountAsFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeClock{limit: 2, cancel: cancel}
	err := Run(ctx, Config{
		Reporter: uploading(),
		Dir:      t.TempDir(),
		Interval: 7 * time.Second,
		Clock:    clock,
		Rand:     func() float64 { return 0 },
		RunOnce: func(context.Context, reporter.Config) (reporter.Summary, error) {
			return reporter.Summary{Sessions: []reporter.SessionResult{{Provider: "codex", SessionID: "s", Err: errors.New("upload: connection refused")}}}, nil
		},
		Heartbeat: func(context.Context, reporter.Config, reporter.Summary) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{2 * time.Second, 4 * time.Second}; fmt.Sprint(clock.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clock.waits, want)
	}
}

func TestHeartbeatEveryCycleWithoutEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeClock{limit: 3, cancel: cancel}
	beats := 0
	err := Run(ctx, Config{
		Reporter: uploading(),
		Dir:      t.TempDir(),
		Clock:    clock,
		RunOnce: func(context.Context, reporter.Config) (reporter.Summary, error) {
			return reporter.Summary{}, nil
		},
		Heartbeat: func(context.Context, reporter.Config, reporter.Summary) error {
			beats++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if beats != 3 {
		t.Fatalf("heartbeats = %d, want one per cycle", beats)
	}
}

func TestPassUsesAllowlistAndStopChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got reporter.Config
	var passCtxDone <-chan struct{}
	err := Run(ctx, Config{
		Reporter: uploading(),
		Dir:      t.TempDir(),
		Clock:    &fakeClock{limit: 1, cancel: cancel},
		RunOnce: func(passCtx context.Context, cfg reporter.Config) (reporter.Summary, error) {
			got = cfg
			passCtxDone = passCtx.Done()
			return reporter.Summary{}, nil
		},
		Heartbeat: func(context.Context, reporter.Config, reporter.Summary) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 || got.Providers[0] != transcript.ProviderCodex || got.DryRun || got.Out != nil {
		t.Fatalf("pass config = %+v", got)
	}
	if got.Stop == nil {
		t.Fatal("pass has no Stop channel")
	}
	select {
	case <-got.Stop:
	default:
		t.Fatal("Stop is not closed after the daemon was cancelled")
	}
	if passCtxDone != nil {
		t.Fatal("the pass context is cancellable, so a stop would abort the batch in flight")
	}
}

func TestRunRefusesToStart(t *testing.T) {
	noProviders := uploading()
	noProviders.Providers = nil
	dryRun := uploading()
	dryRun.DryRun = true
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no providers", Config{Reporter: noProviders}, ErrNoProviders.Error()},
		{"dry run", Config{Reporter: dryRun}, ErrNoProviders.Error()},
		{"interval", Config{Reporter: uploading(), Interval: 4 * time.Second}, "interval must be at least 5s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.Dir = t.TempDir()
			tt.cfg.RunOnce = func(context.Context, reporter.Config) (reporter.Summary, error) {
				t.Fatal("pass ran")
				return reporter.Summary{}, nil
			}
			err := Run(context.Background(), tt.cfg)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLockPreventsSecondInstance(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Reporter: uploading(),
			Dir:      dir,
			RunOnce: func(context.Context, reporter.Config) (reporter.Summary, error) {
				select {
				case <-started:
				default:
					close(started)
				}
				return reporter.Summary{}, nil
			},
			Heartbeat: func(context.Context, reporter.Config, reporter.Summary) error { return nil },
		})
	}()
	<-started

	second := Config{Reporter: uploading(), Dir: dir, RunOnce: func(context.Context, reporter.Config) (reporter.Summary, error) {
		t.Error("second daemon ran a pass")
		return reporter.Summary{}, nil
	}}
	if err := Run(context.Background(), second); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Run = %v, want ErrLocked", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The lock is released on exit.
	f, err := acquireLock(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		t.Fatalf("lock after first daemon stopped: %v", err)
	}
	f.Close()
}

func TestLogRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openLog(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// Six 60-byte lines: one per file, so each write after the first rotates.
	for i := range 6 {
		line := fmt.Sprintf("%d%s\n", i, strings.Repeat("x", 58))
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"": "5", ".1": "4", ".2": "3", ".3": "2"}
	for suffix, first := range want {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatalf("daemon.log%s: %v", suffix, err)
		}
		if len(data) != 60 || string(data[:1]) != first {
			t.Errorf("daemon.log%s starts with %q (%d bytes), want line %s", suffix, data[:1], len(data), first)
		}
	}
	if _, err := os.Stat(path + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("daemon.log.4 exists; only 3 backups should be kept")
	}
}

func TestLogAppendsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	for range 2 {
		w, err := openLog(path, 100, 3)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(strings.Repeat("y", 40) + "\n"))
		w.Close()
	}
	// 82 bytes fit in one file; the third line does not.
	w, _ := openLog(path, 100, 3)
	w.Write([]byte(strings.Repeat("z", 40) + "\n"))
	w.Close()
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != 82 {
		t.Fatalf("daemon.log.1 = %v, %v; want the two earlier lines", info, err)
	}
}
