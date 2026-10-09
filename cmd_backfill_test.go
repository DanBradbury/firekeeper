package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// stubBackfill records the config and answers as a pass that planned one
// session, asking Confirm when the config uploads.
func stubBackfill(t *testing.T, input string, interactive bool) (*reporter.BackfillConfig, *bool) {
	t.Helper()
	var got reporter.BackfillConfig
	var confirmed bool
	previousRun, previousInput, previousInteractive := backfillRun, backfillInput, backfillInteractive
	backfillRun = func(_ context.Context, cfg reporter.BackfillConfig) (reporter.BackfillResult, error) {
		got = cfg
		plan := reporter.Plan{Uploading: cfg.Uploading(), Providers: []reporter.ProviderPlan{{Provider: transcript.ProviderCodex, Sessions: 1, Events: 4, Requests: 1}}}
		result := reporter.BackfillResult{Plan: plan}
		if !plan.Uploading {
			return result, nil
		}
		if cfg.Confirm != nil && !cfg.Confirm(plan) {
			result.Declined = true
			return result, nil
		}
		confirmed = true
		result.Sessions = []reporter.SessionResult{{Provider: "codex", Events: 4}}
		return result, nil
	}
	backfillInput = strings.NewReader(input)
	backfillInteractive = func() bool { return interactive }
	t.Cleanup(func() {
		backfillRun, backfillInput, backfillInteractive = previousRun, previousInput, previousInteractive
	})
	return &got, &confirmed
}

func TestBackfillFlags(t *testing.T) {
	cfg, _ := stubBackfill(t, "", false)
	var stdout, stderr bytes.Buffer
	code := runBackfill([]string{"--server", "http://h:1", "--provider", "codex", "--provider", "copilot", "--after", "2026-03-04", "--limit", "10", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if cfg.Server != "http://h:1" || !cfg.DryRun || cfg.Limit != 10 || len(cfg.Providers) != 2 || cfg.Since != 0 {
		t.Fatalf("config = %+v", *cfg)
	}
	if want := time.Date(2026, 3, 4, 0, 0, 0, 0, time.Local); !cfg.After.Equal(want) {
		t.Fatalf("after = %v, want %v", cfg.After, want)
	}
	if cfg.Progress != &stderr || cfg.Out != &stdout {
		t.Fatal("plan and progress are not wired to stdout and stderr")
	}
}

func TestBackfillRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--since", "24h", "--after", "2026-01-01"},
		{"--after", "01/02/2026"},
		{"--since", "-1h"},
		{"--limit", "-1"},
		{"--provider", "nope"},
		{"--all", "--provider", "codex"},
		{"extra"},
	} {
		stubBackfill(t, "", false)
		var stdout, stderr bytes.Buffer
		if code := runBackfill(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: code = %d, want 2", args, code)
		}
	}
}

func TestBackfillAll(t *testing.T) {
	for _, tt := range []struct {
		name        string
		args        []string
		input       string
		interactive bool
		wantUpload  bool
		wantCode    int
	}{
		{"confirmed", []string{"--all"}, "y\n", true, true, 0},
		{"preview", []string{"--all", "--dry-run"}, "", false, false, 0},
		{"automatic", []string{"--all", "--yes"}, "", false, true, 0},
		{"declined", []string{"--all"}, "n\n", true, false, 1},
		{"non-interactive", []string{"--all"}, "", false, false, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfigFile(t, "server = \"https://saved.example\"\ntoken = \"fk_test\"\nproviders = [\"copilot\"]\n")
			cfg, confirmed := stubBackfill(t, tt.input, tt.interactive)
			previous := detectBackfillProviders
			want := []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot, transcript.ProviderClaude}
			detectBackfillProviders = func() ([]transcript.Provider, error) { return want, nil }
			t.Cleanup(func() { detectBackfillProviders = previous })
			var stdout, stderr bytes.Buffer
			if code := runBackfill(tt.args, &stdout, &stderr); code != tt.wantCode || *confirmed != tt.wantUpload {
				t.Fatalf("code = %d, confirmed = %v", code, *confirmed)
			}
			if !slices.Equal(cfg.Providers, want) || cfg.Server != "https://saved.example" || cfg.Token != "fk_test" {
				t.Fatal("detected providers or saved credentials not applied")
			}
			if !strings.Contains(stdout.String(), "Detected providers: codex, copilot, claude") {
				t.Fatal("detected providers not shown")
			}
		})
	}
}

func TestBackfillAllDetectionStops(t *testing.T) {
	for _, detectionErr := range []error{nil, errors.New("detection failed")} {
		stubBackfill(t, "", false)
		previous := detectBackfillProviders
		detectBackfillProviders = func() ([]transcript.Provider, error) { return nil, detectionErr }
		previousRun := backfillRun
		called := false
		backfillRun = func(context.Context, reporter.BackfillConfig) (reporter.BackfillResult, error) {
			called = true
			return reporter.BackfillResult{}, nil
		}
		var stdout, stderr bytes.Buffer
		code := runBackfill([]string{"--all", "--yes"}, &stdout, &stderr)
		detectBackfillProviders, backfillRun = previous, previousRun
		wantCode := 0
		if detectionErr != nil {
			wantCode = 1
		}
		if code != wantCode || called {
			t.Fatalf("code = %d, backfill called = %v", code, called)
		}
		if detectionErr == nil && !strings.Contains(stdout.String(), "no supported providers found locally") {
			t.Fatal("missing empty discovery message")
		}
	}
}

func TestBackfillWithoutProviderOnlyPlans(t *testing.T) {
	_, confirmed := stubBackfill(t, "y\n", true)
	var stdout, stderr bytes.Buffer
	if code := runBackfill(nil, &stdout, &stderr); code != 0 || *confirmed {
		t.Fatalf("code = %d, confirmed = %v", code, *confirmed)
	}
	if !strings.Contains(stderr.String(), "no --provider given; uploading nothing") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestBackfillConfirmation(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		input       string
		interactive bool
		wantCode    int
		wantUpload  bool
		wantStderr  string
	}{
		{"yes typed", []string{"--provider", "codex"}, "y\n", true, 0, true, "Upload 1 session (~4 events, ~1 request) to http://127.0.0.1:7777? [y/N]"},
		{"enter declines", []string{"--provider", "codex"}, "\n", true, 1, false, "nothing uploaded"},
		{"non-interactive refused", []string{"--provider", "codex"}, "y\n", false, 1, false, "pass --yes to upload"},
		{"--yes skips the prompt", []string{"--provider", "codex", "--yes"}, "", false, 0, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, confirmed := stubBackfill(t, tt.input, tt.interactive)
			var stdout, stderr bytes.Buffer
			code := runBackfill(tt.args, &stdout, &stderr)
			if code != tt.wantCode || *confirmed != tt.wantUpload {
				t.Fatalf("code = %d, uploaded = %v, stderr = %q", code, *confirmed, stderr.String())
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
			if tt.wantStderr == "" && stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
			if tt.wantUpload && !strings.Contains(stdout.String(), "backfill: uploaded 4 events from 1 session") {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}
