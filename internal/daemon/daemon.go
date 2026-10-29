// Package daemon runs the reporter's upload pass on a fixed interval until
// it is told to stop.
//
// Each cycle runs reporter.RunOnce and then sends a heartbeat, so the
// dashboard sees the machine online even when there is nothing new. With
// Reporter.ScanEnded set, each pass also reads the transcripts that
// changed since the previous pass, so sessions that start and end between
// passes are not missed. The scan window is held in memory: the first pass
// looks back Reporter.ScanLookback, and each later pass starts twice the
// interval before the previous complete pass began. A
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

	"github.com/DanBradbury/firekeeper/internal/otelrecv"
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

	// Telemetry, when enabled, also serves the loopback OTLP listener.
	// It is independent of uploading: with no providers the daemon only
	// listens, and it still uploads nothing.
	Telemetry TelemetryConfig

	// RunOnce, Heartbeat, Clock, Rand, and Listen replace the real
	// implementations in tests. Nil means reporter.RunOnce,
	// reporter.Heartbeat, the system clock, math/rand/v2.Float64, and
	// otelrecv.Listen.
	RunOnce   func(context.Context, reporter.Config) (reporter.Summary, error)
	Heartbeat func(context.Context, reporter.Config, reporter.Summary) error
	Clock     Clock
	Rand      func() float64
	Listen    func(context.Context, otelrecv.Options) error
}

// TelemetryConfig is the opt-in loopback receiver. Enabled is false unless
// the config file sets it.
type TelemetryConfig struct {
	Enabled bool
	Port    int
	Path    string
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
	if !cfg.Reporter.Uploading() && !cfg.Telemetry.Enabled {
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
	listenDone := d.startTelemetry(ctx, w)
	if cfg.Reporter.Uploading() {
		d.loop(ctx)
	} else {
		d.log.Printf("started: telemetry listener only, no upload providers")
		<-ctx.Done()
		d.log.Printf("stopped")
	}
	if listenDone != nil {
		<-listenDone
	}
	return nil
}

func (d *daemon) startTelemetry(ctx context.Context, logOut io.Writer) <-chan struct{} {
	if !d.cfg.Telemetry.Enabled {
		return nil
	}
	listen := d.cfg.Listen
	if listen == nil {
		listen = otelrecv.Listen
	}
	port := d.cfg.Telemetry.Port
	if port == 0 {
		port = 4318
	}
	path := d.cfg.Telemetry.Path
	if path == "" {
		var err error
		path, err = otelrecv.DefaultPath()
		if err != nil {
			d.log.Printf("telemetry listener: %v", err)
			return nil
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := listen(ctx, otelrecv.Options{
			Port: port, Path: path, Stdout: io.Discard, Stderr: logOut, Quiet: true,
		})
		if err != nil && ctx.Err() == nil {
			d.log.Printf("telemetry listener: %v", err)
		}
	}()
	return done
}

type daemon struct {
	cfg         Config
	log         *log.Logger
	lastWarning string
	// scanAfter opens the next pass's scan window. Zero until the first
	// pass sets it from Reporter.ScanLookback.
	scanAfter time.Time
}

func (d *daemon) now() time.Time {
	if d.cfg.Reporter.Now != nil {
		return d.cfg.Reporter.Now()
	}
	return time.Now()
}

func (d *daemon) loop(ctx context.Context) {
	providers := make([]string, len(d.cfg.Reporter.Providers))
	for i, p := range d.cfg.Reporter.Providers {
		providers[i] = string(p)
	}
	scan := "off (live sessions only)"
	if d.cfg.Reporter.ScanEnded {
		lookback := d.cfg.Reporter.ScanLookback
		if lookback <= 0 {
			lookback = reporter.DefaultScanLookback
		}
		scan = "lookback " + lookback.String()
	}
	d.log.Printf("started: providers %s, interval %s, transcript scan %s", strings.Join(providers, ","), d.cfg.Interval, scan)
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
//
// The scan window moves forward only after a pass whose scan was complete
// and in which no session failed, so sessions left over by the scan's
// limit or budget, or that failed to upload, are found again next pass.
// The margin of twice the interval covers a transcript written while the
// previous pass was enumerating.
func (d *daemon) cycle(ctx context.Context) error {
	cfg := d.cfg.Reporter
	cfg.Out = nil
	cfg.Stop = ctx.Done()
	start := d.now()
	if cfg.ScanEnded {
		if d.scanAfter.IsZero() {
			lookback := cfg.ScanLookback
			if lookback <= 0 {
				lookback = reporter.DefaultScanLookback
			}
			d.scanAfter = start.Add(-lookback)
		}
		cfg.ScanAfter = d.scanAfter
	}
	summary, err := d.cfg.RunOnce(context.WithoutCancel(ctx), cfg)
	if err != nil {
		return fmt.Errorf("report pass: %w", err)
	}
	if cfg.ScanEnded && summary.Scan.Complete && !summary.Failed() {
		if next := start.Add(-2 * d.cfg.Interval); next.After(d.scanAfter) {
			d.scanAfter = next
		}
	}
	if s := summary.Scan; s.Found > 0 || s.Excluded > 0 {
		d.log.Printf("transcript scan: %d changed %s not running, %d read, %d left for the next pass, %d excluded",
			s.Found, plural(s.Found, "session", "sessions"), s.Read, s.Deferred, s.Excluded)
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
