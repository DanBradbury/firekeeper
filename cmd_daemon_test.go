package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func stubDaemon(t *testing.T, err error) *daemon.Config {
	t.Helper()
	var got daemon.Config
	previous := daemonRun
	daemonRun = func(_ context.Context, cfg daemon.Config) error {
		got = cfg
		return err
	}
	t.Cleanup(func() { daemonRun = previous })
	return &got
}

func TestDaemonFlags(t *testing.T) {
	cfg := stubDaemon(t, nil)
	var stdout, stderr bytes.Buffer
	code := runDaemon([]string{"--server", "http://h:1", "--provider", "codex", "--interval", "30s"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	r := cfg.Reporter
	if r.Server != "http://h:1" || cfg.Interval != 30*time.Second || len(r.Providers) != 1 || r.Providers[0] != transcript.ProviderCodex || r.DryRun {
		t.Fatalf("config = %+v", *cfg)
	}
}

func TestDaemonDefaults(t *testing.T) {
	cfg := stubDaemon(t, nil)
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"--provider", "copilot"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if cfg.Reporter.Server != reporter.DefaultServer || cfg.Interval != daemon.DefaultInterval {
		t.Fatalf("config = %+v", *cfg)
	}
}

func TestDaemonRejectsBadFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, "no --provider given"},
		{[]string{"--provider", "codex", "--interval", "4s"}, "--interval must be at least 5s"},
		{[]string{"--provider", "nope"}, "unknown provider"},
		{[]string{"--provider", "codex", "extra"}, "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			called := false
			previous := daemonRun
			daemonRun = func(context.Context, daemon.Config) error { called = true; return nil }
			t.Cleanup(func() { daemonRun = previous })
			var stdout, stderr bytes.Buffer
			if code := runDaemon(tt.args, &stdout, &stderr); code != 2 {
				t.Fatalf("code = %d", code)
			}
			if called || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("called = %v, stderr = %q", called, stderr.String())
			}
		})
	}
}

func TestDaemonErrorExitsOne(t *testing.T) {
	stubDaemon(t, daemon.ErrLocked)
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"--provider", "codex"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), daemon.ErrLocked.Error()) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
