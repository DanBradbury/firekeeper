package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func stubReport(t *testing.T, summary reporter.Summary, err error) *reporter.Config {
	t.Helper()
	var got reporter.Config
	previous := reportOnce
	reportOnce = func(_ context.Context, cfg reporter.Config) (reporter.Summary, error) {
		got = cfg
		return summary, err
	}
	t.Cleanup(func() { reportOnce = previous })
	return &got
}

func TestReportFlags(t *testing.T) {
	cfg := stubReport(t, reporter.Summary{}, nil)
	var stdout, stderr bytes.Buffer
	code := runReport([]string{"--server", "http://h:1", "--provider", "codex", "--provider", "Copilot", "--since", "2h", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	want := []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot}
	if cfg.Server != "http://h:1" || !cfg.DryRun || cfg.Since != 2*time.Hour || len(cfg.Providers) != 2 || cfg.Providers[0] != want[0] || cfg.Providers[1] != want[1] {
		t.Fatalf("config = %+v", *cfg)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestReportDefaultsUploadNothing(t *testing.T) {
	cfg := stubReport(t, reporter.Summary{}, nil)
	var stdout, stderr bytes.Buffer
	if code := runReport(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if cfg.Server != reporter.DefaultServer || cfg.Uploading() {
		t.Fatalf("config = %+v", *cfg)
	}
	if !strings.Contains(stderr.String(), "no --provider given; uploading nothing") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestReportExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		summary reporter.Summary
		err     error
		code    int
		stderr  string
	}{
		{name: "unknown provider", args: []string{"--provider", "emacs"}, code: 2, stderr: "unknown provider"},
		{name: "negative since", args: []string{"--since", "-1h"}, code: 2, stderr: "must not be negative"},
		{name: "extra argument", args: []string{"now"}, code: 2, stderr: "unexpected argument"},
		{name: "fatal", err: errors.New("run ps: failed"), code: 1, stderr: "firekeeper report: run ps: failed"},
		{name: "session failed", summary: reporter.Summary{Sessions: []reporter.SessionResult{{Err: errors.New("x")}}}, code: 1},
		{name: "warning", summary: reporter.Summary{Warning: &session.Warning{Message: "Copilot: unavailable"}}, code: 0, stderr: "warning: Copilot: unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubReport(t, tt.summary, tt.err)
			var stdout, stderr bytes.Buffer
			if code := runReport(tt.args, &stdout, &stderr); code != tt.code {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.stderr)
			}
		})
	}
}

func TestReportToken(t *testing.T) {
	cfg := stubReport(t, reporter.Summary{}, nil)
	var stdout, stderr bytes.Buffer
	t.Setenv(config.EnvToken, "fk_env")
	if code := runReport(nil, &stdout, &stderr); code != 0 || cfg.Token != "fk_env" {
		t.Fatalf("env: code=%d token=%q", code, cfg.Token)
	}
	if code := runReport([]string{"--token", "fk_flag"}, &stdout, &stderr); code != 0 || cfg.Token != "fk_flag" {
		t.Fatalf("flag: code=%d token=%q", code, cfg.Token)
	}
}
