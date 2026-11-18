package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/claudecloud"
)

func TestBackfillCloudClaudeRunsCloudBackfill(t *testing.T) {
	cfg, _ := stubBackfill(t, "", false)
	call := stubClaudeCloud(t, claudecloud.SyncResult{Listed: 2, Fetched: 2}, nil)
	var stdout, stderr bytes.Buffer
	if code := runBackfillCloud([]string{"claude", "--after", "2026-03-04", "--yes"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !call.called {
		t.Fatal("cloud sessions were not fetched")
	}
	if !slices.Equal(cfg.Providers, []transcript.Provider{transcript.ProviderClaude}) {
		t.Fatalf("providers = %v", cfg.Providers)
	}
}

func TestBackfillCloudRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no provider", nil, 2, "Usage:"},
		{"unknown provider", []string{"codex"}, 2, `unknown cloud provider "codex"`},
		{"look-alike", []string{"claude-cloud"}, 2, "unknown cloud provider"},
		{"flag first", []string{"--yes"}, 2, "unknown cloud provider"},
		{"help", []string{"--help"}, 0, "Providers: claude"},
		{"local flag conflicts", []string{"claude", "--all"}, 2, "--claude-cloud imports only cloud sessions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubBackfill(t, "", false)
			call := stubClaudeCloud(t, claudecloud.SyncResult{}, nil)
			var stdout, stderr bytes.Buffer
			if code := runBackfillCloud(tt.args, &stdout, &stderr); code != tt.code {
				t.Fatalf("code = %d, want %d", code, tt.code)
			}
			if out := stdout.String() + stderr.String(); !strings.Contains(out, tt.want) {
				t.Fatalf("output = %q, want %q", out, tt.want)
			}
			if call.called {
				t.Fatal("fetched cloud sessions on rejected input")
			}
		})
	}
}

func TestBackfillCloudIsRegistered(t *testing.T) {
	code, ok := RunSubcommand([]string{"backfill-cloud"}, &bytes.Buffer{}, &bytes.Buffer{})
	if !ok || code != 2 {
		t.Fatalf("RunSubcommand = %d, %v", code, ok)
	}
}
