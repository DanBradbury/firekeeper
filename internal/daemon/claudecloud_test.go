package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/claudecloud"
)

type countingPoller struct{ polls int }

func (p *countingPoller) Poll(context.Context) claudecloud.PollResult {
	p.polls++
	return claudecloud.PollResult{Ran: true}
}

func runCycles(t *testing.T, cfg Config, cycles int) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	cfg.Dir = dir
	cfg.Clock = &fakeClock{limit: cycles, cancel: cancel}
	cfg.Rand = func() float64 { return 0 }
	cfg.RunOnce = func(context.Context, reporter.Config) (reporter.Summary, error) { return reporter.Summary{}, nil }
	cfg.Heartbeat = func(context.Context, reporter.Config, reporter.Summary) error { return nil }
	if err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "daemon.log"))
	return string(log)
}

func TestClaudeCloudIsOffByDefault(t *testing.T) {
	poller := &countingPoller{}
	cfg := Config{Reporter: uploading()}
	cfg.ClaudeCloud.Poller = poller // not Enabled: must never be used
	log := runCycles(t, cfg, 3)
	if poller.polls != 0 || strings.Contains(log, "claude cloud") {
		t.Fatalf("polls = %d, log = %q", poller.polls, log)
	}
}

func TestClaudeCloudPollsEachCycleWhenEnabled(t *testing.T) {
	poller := &countingPoller{}
	cfg := Config{Reporter: uploading(), ClaudeCloud: ClaudeCloudConfig{Enabled: true, Poller: poller}}
	runCycles(t, cfg, 3)
	if poller.polls != 3 {
		t.Fatalf("polls = %d, want one due-check per cycle", poller.polls)
	}
}

func TestClaudeCloudNeedsClaudeInProviders(t *testing.T) {
	cfg := Config{Reporter: uploading(), ClaudeCloud: ClaudeCloudConfig{Enabled: true}}
	cfg.Reporter.Home = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	cfg.Dir = dir
	cfg.Clock = &fakeClock{limit: 2, cancel: cancel}
	cfg.RunOnce = func(context.Context, reporter.Config) (reporter.Summary, error) { return reporter.Summary{}, nil }
	cfg.Heartbeat = func(context.Context, reporter.Config, reporter.Summary) error { return nil }
	if err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "daemon.log"))
	if !strings.Contains(string(log), "claude cloud sync is off") {
		t.Fatalf("log = %q", log)
	}
	status, ok := claudecloud.ReadStatus(filepath.Join(dir, claudecloud.StatusFile))
	if !ok || !strings.Contains(status.Warning, "not in providers") {
		t.Fatalf("status = %+v", status)
	}
}

func TestClaudeCloudRealPollerDoesNothingWithoutLogin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("the real poller would read the macOS keychain")
	}
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	cfg := Config{Reporter: reporter.Config{Server: "http://127.0.0.1:1", Home: home,
		Providers: []transcript.Provider{transcript.ProviderClaude}},
		ClaudeCloud: ClaudeCloudConfig{Enabled: true, Interval: 10 * time.Minute}}
	log := runCycles(t, cfg, 2)
	if !strings.Contains(log, "claude cloud sync: on, every 10m0s") || !strings.Contains(log, "no Claude Code login found") {
		t.Fatalf("log = %q", log)
	}
	if strings.Count(log, "claude cloud sync failed") != 1 {
		t.Fatalf("the failure must be tried once per interval, not every cycle: %q", log)
	}
}

func TestClaudeCloudIntervalMinimum(t *testing.T) {
	err := Run(context.Background(), Config{Reporter: uploading(), Dir: t.TempDir(),
		ClaudeCloud: ClaudeCloudConfig{Enabled: true, Interval: time.Minute}})
	if err == nil || !strings.Contains(err.Error(), "claude_cloud.interval") {
		t.Fatalf("err = %v", err)
	}
}
