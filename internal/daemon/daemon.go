// Package daemon runs the reporter's upload pass on a fixed interval until
// it is told to stop.
//
// Each cycle runs reporter.RunOnce and then sends a heartbeat, so the
// dashboard sees the machine online even when there is nothing new. A
// failed cycle backs off exponentially. Only one daemon runs per home
// directory, enforced by a lock on ~/.firekeeper/daemon.lock, and it logs
// to ~/.firekeeper/daemon.log. Logs carry counts and sanitized errors,
// never transcript text.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
)

const (
	// DefaultInterval is the time between passes when Config.Interval is
	// zero.
	DefaultInterval = 15 * time.Second
	// MinInterval is the shortest interval Run accepts.
	MinInterval = 5 * time.Second

	minBackoff = 2 * time.Second
	maxBackoff = 5 * time.Minute

	logMaxBytes = 10 << 20
	logBackups  = 3
)

// ErrLocked means another daemon holds the lock for the same home.
var ErrLocked = errors.New("another firekeeper daemon is already running")

// ErrNoProviders means the reporter config would upload nothing.
var ErrNoProviders = errors.New("no provider enabled; the daemon uploads nothing without one")

// Clock schedules the wait between passes. Tests replace it so backoff can
// be checked without sleeping.
type Clock interface {
	After(time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Config configures Run.
type Config struct {
	// Reporter configures each pass. Its Providers allowlist is the
	// daemon's; Run refuses to start without one. Out and Stop are set by
	// Run.
	Reporter reporter.Config
	// Interval is the time between successful passes. Zero means
	// DefaultInterval.
	Interval time.Duration
	// Dir holds daemon.lock and daemon.log. Empty means
	// ~/.firekeeper, using Reporter.Home when set.
	Dir string
	// Out, when set, also receives every log line.
	Out io.Writer

	// RunOnce, Heartbeat, Clock, and Rand replace the real implementations
	// in tests. Nil means reporter.RunOnce, reporter.Heartbeat, the system
	// clock, and math/rand/v2.Float64.
	RunOnce   func(context.Context, reporter.Config) (reporter.Summary, error)
	Heartbeat func(context.Context, reporter.Config, reporter.Summary) error
	Clock     Clock
	Rand      func() float64
}

// Run reports every Interval until ctx is cancelled. Cancelling ctx is a
// graceful stop: the batch in flight finishes and its offset is saved, no
// new batch starts, and Run returns nil. Run returns an error only when it
// cannot start.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Interval < MinInterval {
		return fmt.Errorf("interval must be at least %s", MinInterval)
	}
	if !cfg.Reporter.Uploading() {
		return ErrNoProviders
	}
	if cfg.RunOnce == nil {
		cfg.RunOnce = reporter.RunOnce
	}
	if cfg.Heartbeat == nil {
		cfg.Heartbeat = reporter.Heartbeat
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.Dir == "" {
		home := cfg.Reporter.Home
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return errors.New("find home directory")
			}
		}
		cfg.Dir = filepath.Join(home, ".firekeeper")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return fmt.Errorf("create daemon directory: %w", cause(err))
	}

	lock, err := acquireLock(filepath.Join(cfg.Dir, "daemon.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()

	logFile, err := openLog(filepath.Join(cfg.Dir, "daemon.log"), logMaxBytes, logBackups)
	if err != nil {
		return err
	}
	defer logFile.Close()
	var w io.Writer = logFile
	if cfg.Out != nil {
		w = io.MultiWriter(logFile, cfg.Out)
	}

	d := &daemon{cfg: cfg, log: log.New(w, "", log.LstdFlags)}
	d.loop(ctx)
	return nil
}

type daemon struct {
	cfg         Config
	log         *log.Logger
	lastWarning string
}

func (d *daemon) loop(ctx context.Context) {
	providers := make([]string, len(d.cfg.Reporter.Providers))
	for i, p := range d.cfg.Reporter.Providers {
		providers[i] = string(p)
	}
	d.log.Printf("started: providers %s, interval %s", strings.Join(providers, ","), d.cfg.Interval)
	failures := 0
	for {
		err := d.cycle(ctx)
		if ctx.Err() != nil {
			d.log.Printf("stopped")
			return
		}
		wait := d.cfg.Interval
		if err != nil {
			failures++
			wait = backoff(failures, d.cfg.Rand)
			d.log.Printf("pass failed (%d in a row), retrying in %s: %v", failures, wait.Round(time.Millisecond), err)
		} else {
			if failures > 0 {
				d.log.Printf("recovered after %d failed %s", failures, plural(failures, "pass", "passes"))
			}
			failures = 0
		}
		select {
		case <-ctx.Done():
			d.log.Printf("stopped")
			return
		case <-d.cfg.Clock.After(wait):
		}
	}
}

// cycle runs one pass and one heartbeat. The pass runs on a context that
// ctx does not cancel, with ctx.Done as its Stop channel, so a stop request
// lets the batch in flight finish and saves its offset.
func (d *daemon) cycle(ctx context.Context) error {
	cfg := d.cfg.Reporter
	cfg.Out = nil
	cfg.Stop = ctx.Done()
	summary, err := d.cfg.RunOnce(context.WithoutCancel(ctx), cfg)
	if err != nil {
		return fmt.Errorf("report pass: %w", err)
	}
	warning := ""
	if summary.Warning != nil {
		warning = summary.Warning.Error()
	}
	if warning != d.lastWarning {
		if warning != "" {
			d.log.Printf("discovery warning: %s", warning)
		}
		d.lastWarning = warning
	}

	failed, events, batches, dups := 0, 0, 0, 0
	for _, s := range summary.Sessions {
		if s.Err != nil {
			failed++
			d.log.Printf("%s %s: %v", s.Provider, s.SessionID, s.Err)
		}
		events += s.Events
		batches += s.Batches
		dups += s.Duplicates
	}
	if events > 0 {
		d.log.Printf("uploaded %d events in %d %s (%d duplicates)", events, batches, plural(batches, "batch", "batches"), dups)
	}
	if summary.Stopped || ctx.Err() != nil {
		return nil
	}
	if err := d.cfg.Heartbeat(ctx, cfg, summary); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d %s failed", failed, plural(failed, "session", "sessions"))
	}
	return nil
}

// backoff is the wait after the nth consecutive failure: 2s doubling to
// 5m, shortened by up to a quarter at random so restarted daemons spread
// out, and never under 2s.
func backoff(n int, random func() float64) time.Duration {
	d := maxBackoff
	if n < 20 {
		d = min(minBackoff<<(n-1), maxBackoff)
	}
	d -= time.Duration(random() * float64(d) / 4)
	return max(d, minBackoff)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// cause drops the path from filesystem errors so messages stay free of
// machine-specific locations.
func cause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}
