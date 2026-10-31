package claudecloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// Poller limits. A pass downloads at most DefaultMaxFetch sessions; when it
// leaves work behind, the next pass comes after MoreWorkDelay instead of the
// full interval. Errors back off from the interval, doubling to MaxBackoff.
const (
	DefaultPollInterval = 15 * time.Minute
	MinPollInterval     = 10 * time.Minute
	MaxBackoff          = 6 * time.Hour
	DefaultMaxFetch     = 25
	MoreWorkDelay       = time.Minute
	pollScanBudget      = 30 * time.Second
)

// ErrClaudeNotAllowed means [claude_cloud] is on but claude is not in the
// upload allowlist, which always wins.
var ErrClaudeNotAllowed = errors.New("claude cloud sync is enabled but claude is not in providers; add it to providers to upload cloud sessions")

// PollerConfig configures a Poller.
type PollerConfig struct {
	// Reporter is the daemon's reporter config. The poller copies it for
	// its own passes, limited to provider claude and the cloud cache.
	Reporter reporter.Config
	// Interval is the time between successful passes. Zero means
	// DefaultPollInterval; less than MinPollInterval is an error.
	Interval time.Duration
	// Lookback opens the window on the first pass: sessions last updated
	// before it are left to `backfill --claude-cloud`. Zero means
	// reporter.DefaultScanLookback.
	Lookback time.Duration
	// Dir is the cache directory. Empty means ~/.firekeeper/cloud/claude.
	Dir string
	// StatusPath receives the outcome of each pass for the dashboard.
	// Empty means ~/.firekeeper/claude-cloud-status.json.
	StatusPath string
	// MaxFetch caps downloads per pass. Zero means DefaultMaxFetch.
	MaxFetch int
	// Logf receives log lines. Nil discards them.
	Logf func(format string, args ...any)

	// Credentials, BaseURL, HTTP, RunOnce, and Now replace the real
	// implementations in tests. Nil or empty means LoadCredentials on the
	// home directory, DefaultBaseURL, a client that never follows
	// redirects, reporter.RunOnce, and time.Now.
	Credentials func(context.Context) (Credentials, error)
	BaseURL     string
	HTTP        *http.Client
	RunOnce     func(context.Context, reporter.Config) (reporter.Summary, error)
	Now         func() time.Time
}

// Poller imports new and updated cloud sessions on a schedule, through the
// cache, then the normal redact, batch and ingest path. Already-uploaded
// events are skipped by the reporter's saved offsets, and a session is
// downloaded again only when the API reports a newer update time. It is not
// safe for concurrent use; the daemon calls it from its own loop.
type Poller struct {
	cfg      PollerConfig
	after    time.Time
	next     time.Time
	failures int
	lastLog  string
}

// PollResult is what one Poll did. It holds counts and sanitized messages
// only.
type PollResult struct {
	// Ran is false when the pass was not due.
	Ran  bool
	Sync SyncResult
	// Events, Sessions count what was uploaded.
	Events, Sessions int
	// Err is the pass's failure, if any. Warning is a problem that did not
	// fail the pass, such as one session that could not be downloaded.
	Err     error
	Warning string
	// Next is when the next pass is due.
	Next time.Time
}

// Status is the file the poller writes after each pass so the dashboard
// can show a sync problem. It never holds a token or transcript data.
type Status struct {
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success,omitzero"`
	NextAttempt time.Time `json:"next_attempt"`
	// Warning is the last problem, or empty when the last pass was clean.
	Warning string `json:"warning,omitempty"`
}

// StatusFile is the default status file name inside ~/.firekeeper.
const StatusFile = "claude-cloud-status.json"

// NewPoller checks cfg and returns a poller whose first pass is due at once.
func NewPoller(cfg PollerConfig) (*Poller, error) {
	if cfg.Interval == 0 {
		cfg.Interval = DefaultPollInterval
	}
	if cfg.Interval < MinPollInterval {
		return nil, fmt.Errorf("claude_cloud.interval must be at least %s", MinPollInterval)
	}
	if !slices.Contains(cfg.Reporter.Providers, transcript.ProviderClaude) {
		return nil, ErrClaudeNotAllowed
	}
	if cfg.Lookback <= 0 {
		cfg.Lookback = reporter.DefaultScanLookback
	}
	if cfg.MaxFetch <= 0 {
		cfg.MaxFetch = DefaultMaxFetch
	}
	if cfg.Dir == "" || cfg.StatusPath == "" || cfg.Credentials == nil {
		home := cfg.Reporter.Home
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return nil, errors.New("find home directory")
			}
			home = h
		}
		if cfg.Dir == "" {
			cfg.Dir = filepath.Join(home, ".firekeeper", "cloud", "claude")
		}
		if cfg.StatusPath == "" {
			cfg.StatusPath = filepath.Join(home, ".firekeeper", StatusFile)
		}
		if cfg.Credentials == nil {
			cfg.Credentials = func(ctx context.Context) (Credentials, error) { return LoadCredentials(ctx, home) }
		}
	}
	if cfg.RunOnce == nil {
		cfg.RunOnce = reporter.RunOnce
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Poller{cfg: cfg}, nil
}

// Interval is the configured time between successful passes.
func (p *Poller) Interval() time.Duration { return p.cfg.Interval }

// Poll runs one pass when one is due and returns what it did. It never
// panics on API errors: every failure is logged once per distinct message,
// written to the status file, and pushes the next pass back.
func (p *Poller) Poll(ctx context.Context) PollResult {
	now := p.cfg.Now()
	if now.Before(p.next) {
		return PollResult{Next: p.next}
	}
	if p.after.IsZero() {
		p.after = now.Add(-p.cfg.Lookback)
	}
	res := p.pass(ctx)
	res.Ran = true
	if ctx.Err() != nil {
		return res
	}
	status := Status{LastAttempt: now}
	if prev, ok := ReadStatus(p.cfg.StatusPath); ok {
		status.LastSuccess = prev.LastSuccess
	}
	wait := p.cfg.Interval
	switch {
	case res.Err != nil:
		p.failures++
		wait = pollBackoff(p.failures, p.cfg.Interval)
		status.Warning = res.Err.Error()
		msg := res.Err.Error()
		if msg != p.lastLog {
			p.cfg.Logf("claude cloud sync failed, retrying in %s: %s", wait, msg)
			p.lastLog = msg
		} else {
			p.cfg.Logf("claude cloud sync failed again (%d in a row), retrying in %s", p.failures, wait)
		}
	default:
		if p.failures > 0 {
			p.cfg.Logf("claude cloud sync recovered after %d failed %s", p.failures, plural(p.failures, "pass", "passes"))
		}
		p.failures, p.lastLog = 0, ""
		status.LastSuccess = now
		status.Warning = res.Warning
		if res.Sync.Remaining > 0 {
			wait = MoreWorkDelay
		}
	}
	p.next = now.Add(wait)
	res.Next = p.next
	status.NextAttempt = p.next
	if err := WriteStatus(p.cfg.StatusPath, status); err != nil {
		p.cfg.Logf("claude cloud sync: %v", err)
	}
	return res
}

func (p *Poller) pass(ctx context.Context) PollResult {
	var res PollResult
	creds, err := p.cfg.Credentials(ctx)
	if err != nil {
		res.Err = err
		return res
	}
	client := NewClient(creds)
	client.BaseURL, client.HTTP = p.cfg.BaseURL, p.cfg.HTTP
	res.Sync, err = Sync(ctx, client, SyncOptions{Dir: p.cfg.Dir, After: p.after, MaxFetch: p.cfg.MaxFetch})
	if err != nil {
		res.Err = err
		return res
	}
	if n := len(res.Sync.Failed); n > 0 {
		res.Warning = fmt.Sprintf("%d cloud %s could not be downloaded and will be retried: %s",
			n, plural(n, "session", "sessions"), res.Sync.Failed[0])
		p.cfg.Logf("claude cloud sync: %s", res.Warning)
	}
	if res.Sync.Fetched > 0 || res.Sync.Remaining > 0 {
		p.cfg.Logf("claude cloud sync: %d listed, %d downloaded, %d unchanged, %d left for the next pass",
			res.Sync.Listed, res.Sync.Fetched, res.Sync.Current, res.Sync.Remaining)
	}

	source := &Source{Dir: p.cfg.Dir}
	only := func(provider transcript.Provider) bool { return provider == transcript.ProviderClaude }
	cfg := p.cfg.Reporter
	cfg.Out = nil
	cfg.Stop = ctx.Done()
	cfg.Since = 0
	cfg.Providers = []transcript.Provider{transcript.ProviderClaude}
	cfg.Discover = func(context.Context, session.Options) ([]session.Meta, error) { return nil, nil }
	cfg.Sources = func(provider transcript.Provider) (reader.Source, bool) { return source, only(provider) }
	cfg.ScanEnumerators = func(provider transcript.Provider) (reader.Enumerator, bool) { return source, only(provider) }
	cfg.ScanEnded = true
	cfg.ScanAfter = p.after
	cfg.ScanBudget = pollScanBudget
	summary, err := p.cfg.RunOnce(context.WithoutCancel(ctx), cfg)
	if err != nil {
		res.Err = fmt.Errorf("upload cloud sessions: %w", err)
		return res
	}
	failed := 0
	for _, s := range summary.Sessions {
		if s.Err != nil {
			failed++
			p.cfg.Logf("claude cloud %s: %v", s.SessionID, s.Err)
		}
		if s.Events > 0 {
			res.Sessions++
		}
		res.Events += s.Events
	}
	if res.Events > 0 {
		p.cfg.Logf("claude cloud sync: uploaded %d events from %d %s", res.Events, res.Sessions, plural(res.Sessions, "session", "sessions"))
	}
	if summary.Scan.Deferred > 0 {
		res.Sync.Remaining += summary.Scan.Deferred
	}
	if failed > 0 {
		res.Err = fmt.Errorf("%d cloud %s failed to upload", failed, plural(failed, "session", "sessions"))
	}
	return res
}

// pollBackoff is the wait after the nth consecutive failure: the interval
// doubling up to MaxBackoff.
func pollBackoff(n int, interval time.Duration) time.Duration {
	d := interval
	for i := 1; i < n && d < MaxBackoff; i++ {
		d *= 2
	}
	return min(d, MaxBackoff)
}

// ReadStatus reads a status file the poller wrote.
func ReadStatus(path string) (Status, bool) {
	data, err := readCapped(path, 64<<10)
	if err != nil {
		return Status{}, false
	}
	var s Status
	if json.Unmarshal(data, &s) != nil {
		return Status{}, false
	}
	s.Warning = strings.TrimSpace(s.Warning)
	return s, true
}

// WriteStatus replaces the status file at path with s, mode 0600.
func WriteStatus(path string, s Status) error {
	data, err := json.Marshal(s)
	if err != nil {
		return errors.New("encode status")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("write status file")
	}
	if err := writeAtomic(path, data, time.Time{}); err != nil {
		return errors.New("write status file")
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
