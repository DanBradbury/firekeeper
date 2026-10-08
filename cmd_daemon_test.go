package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	if cfg.Out == nil {
		t.Fatal("daemon without --quiet does not log to stderr")
	}
}

func TestDaemonQuiet(t *testing.T) {
	cfg := stubDaemon(t, nil)
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"--provider", "codex", "--quiet"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if cfg.Out != nil {
		t.Fatal("--quiet still logs to stderr")
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

// serviceRunner is a fake daemon.Runner. It fails on anything but
// launchctl and systemctl, so a sudo call would fail the test.
type serviceRunner struct {
	t     *testing.T
	calls []string
	reply func(line string) (string, error)
}

func (r *serviceRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "launchctl" && name != "systemctl" {
		r.t.Fatalf("ran %q", name)
	}
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)
	if r.reply == nil {
		return nil, nil
	}
	out, err := r.reply(line)
	return []byte(out), err
}

// stubService makes newService build a Service for goos in a temporary
// home, and returns the args it was built with and its runner.
func stubService(t *testing.T, goos string) (*[]string, *serviceRunner, string) {
	t.Helper()
	home := t.TempDir()
	runner := &serviceRunner{t: t}
	var gotArgs []string
	previous := newService
	newService = func(args []string, out io.Writer) (*daemon.Service, error) {
		gotArgs = args
		return &daemon.Service{
			GOOS: goos, Home: home, UID: 501,
			Executable: "/usr/local/bin/firekeeper",
			Args:       args,
			Runner:     runner,
			Out:        out,
			Sleep:      func(time.Duration) {},
		}, nil
	}
	t.Cleanup(func() { newService = previous })
	return &gotArgs, runner, home
}

func TestDaemonInstallCapturesFlags(t *testing.T) {
	args, runner, home := stubService(t, "linux")
	var stdout, stderr bytes.Buffer
	code := runDaemon([]string{"install", "--server", "http://h:1", "--provider", "codex", "--provider", "Copilot", "--interval", "1m"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	want := []string{"--quiet", "--server", "http://h:1", "--provider", "codex", "--provider", "copilot", "--interval", "1m0s"}
	if !slices.Equal(*args, want) {
		t.Fatalf("args = %q, want %q", *args, want)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %q", runner.calls)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "firekeeper.service"))
	if err != nil || !strings.Contains(string(unit), `"--provider" "copilot"`) {
		t.Fatalf("unit = %s, %v", unit, err)
	}
}

func TestDaemonInstallDefaults(t *testing.T) {
	args, _, _ := stubService(t, "darwin")
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"install", "--provider", "codex"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	want := []string{"--quiet", "--server", reporter.DefaultServer, "--provider", "codex", "--interval", "15s"}
	if !slices.Equal(*args, want) {
		t.Fatalf("args = %q, want %q", *args, want)
	}
}

func TestDaemonInstallDryRun(t *testing.T) {
	_, runner, home := stubService(t, "darwin")
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"install", "--provider", "codex", "--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"would write", "<string>dev.firekeeper.daemon</string>", "launchctl bootstrap gui/501 "} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
	if len(runner.calls) > 0 {
		t.Fatalf("dry run ran %q", runner.calls)
	}
	if entries, _ := os.ReadDir(home); len(entries) > 0 {
		t.Fatalf("dry run wrote %v", entries)
	}
}

func TestDaemonInstallRejectsBadFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"install"}, "no --provider given"},
		{[]string{"install", "--provider", "codex", "--interval", "1s"}, "--interval must be at least"},
		{[]string{"install", "--provider", "codex", "extra"}, "unexpected argument"},
		{[]string{"install", "--provider", "codex", "--quiet"}, "not defined"},
		{[]string{"uninstall", "extra"}, "unexpected argument"},
		{[]string{"status", "--bogus"}, "not defined"},
		{[]string{"logs", "-n", "-1"}, "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			_, runner, _ := stubService(t, "darwin")
			var stdout, stderr bytes.Buffer
			if code := runDaemon(tt.args, &stdout, &stderr); code != 2 {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.want) || len(runner.calls) > 0 {
				t.Fatalf("stderr = %q, calls = %q", stderr.String(), runner.calls)
			}
		})
	}
}

func TestDaemonInstallFailureExitsOne(t *testing.T) {
	_, runner, _ := stubService(t, "linux")
	runner.reply = func(string) (string, error) {
		return "", &daemon.CommandError{Name: "systemctl", Code: 1, Output: "Failed to connect to bus"}
	}
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"install", "--provider", "codex"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), "Failed to connect to bus") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDaemonUninstall(t *testing.T) {
	_, runner, home := stubService(t, "linux")
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"install", "--provider", "codex"}, &stdout, &stderr); code != 0 {
		t.Fatalf("install code = %d, stderr = %q", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(home, ".firekeeper", "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	if code := runDaemon([]string{"uninstall"}, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall code = %d, stderr = %q", code, stderr.String())
	}
	if !slices.Contains(runner.calls, "systemctl --user disable --now firekeeper.service") {
		t.Fatalf("calls = %q", runner.calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".firekeeper", "state.json")); err != nil {
		t.Fatal("uninstall without --purge removed data")
	}

	stdout.Reset()
	if code := runDaemon([]string{"uninstall", "--purge"}, &stdout, &stderr); code != 0 {
		t.Fatalf("purge code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not installed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".firekeeper")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--purge left data: %v", err)
	}
}

func TestDaemonStatusExitCodes(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		code  int
		want  string
	}{
		{"running", "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=7\nExecMainStatus=0\n", 0, "running, pid 7"},
		{"failed", "LoadState=loaded\nActiveState=failed\nSubState=failed\nMainPID=0\nExecMainStatus=1\n", 3, "failed\nlast exit:    1"},
		{"missing", "LoadState=not-found\nActiveState=inactive\nSubState=dead\n", 3, "not loaded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, runner, _ := stubService(t, "linux")
			runner.reply = func(string) (string, error) { return tt.reply, nil }
			var stdout, stderr bytes.Buffer
			if code := runDaemon([]string{"status"}, &stdout, &stderr); code != tt.code {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.want)
			}
		})
	}
}

func TestDaemonLogs(t *testing.T) {
	_, _, home := stubService(t, "darwin")
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"logs"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "no daemon log yet") {
		t.Fatalf("missing log: code = %d, stderr = %q", code, stderr.String())
	}
	dir := filepath.Join(home, ".firekeeper")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.log"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.stderr.log"), []byte("locked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runDaemon([]string{"logs", "-n", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "b\nc\n" || !strings.Contains(stderr.String(), "daemon.stderr.log") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}
