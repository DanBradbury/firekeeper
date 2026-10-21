package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/version"
)

func TestRunSubcommandDispatch(t *testing.T) {
	stubDiscover(t, nil, nil)
	tests := []struct {
		args       []string
		handled    bool
		code       int
		wantStderr string
	}{
		{args: nil, handled: false},
		{args: []string{"--unknown"}, handled: false},
		{args: []string{"--unknown", "snapshot"}, handled: false},
		{args: []string{"unknown"}, handled: false},
		{args: []string{"export"}, handled: true, code: 2, wantStderr: "firekeeper export: not implemented"},
		{args: []string{"report", "--bogus"}, handled: true, code: 2},
		{args: []string{"serve"}, handled: true, code: 2, wantStderr: "firekeeper-server"},
		{args: []string{"serve", "--listen", "127.0.0.1:0"}, handled: true, code: 2, wantStderr: "separate program"},
		{args: []string{"daemon"}, handled: true, code: 2, wantStderr: "firekeeper daemon: no --provider given"},
		{args: []string{"snapshot"}, handled: true, code: 2, wantStderr: "only --json"},
		{args: []string{"snapshot", "--bogus"}, handled: true, code: 2},
		{args: []string{"snapshot", "--json", "extra"}, handled: true, code: 2, wantStderr: "unexpected argument"},
		{args: []string{"snapshot", "--json"}, handled: true, code: 0},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code, handled := RunSubcommand(tt.args, &stdout, &stderr)
			if handled != tt.handled || code != tt.code {
				t.Fatalf("RunSubcommand = (%d, %v), want (%d, %v)", code, handled, tt.code, tt.handled)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
			if !handled && (stdout.Len() > 0 || stderr.Len() > 0) {
				t.Fatalf("unhandled args wrote output")
			}
		})
	}
}

func TestSnapshotJSON(t *testing.T) {
	stubDiscover(t, []session.Meta{{ID: "s1", Provider: "codex", Project: "demo", State: session.SessionStateNeedsInput}}, &session.Warning{Message: "Copilot: unavailable"})
	var stdout, stderr bytes.Buffer
	if code := runSnapshot([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(got) != 1 || got[0]["session_id"] != "s1" || got[0]["state"] != "NEEDS_INPUT" || got[0]["provider"] != "codex" {
		t.Fatalf("output = %v", got)
	}
	if !strings.Contains(stderr.String(), "warning: Copilot: unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSnapshotJSONEmptyAndFatal(t *testing.T) {
	stubDiscover(t, nil, nil)
	var stdout, stderr bytes.Buffer
	if code := runSnapshot([]string{"--json"}, &stdout, &stderr); code != 0 || strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("empty snapshot = %d %q", code, stdout.String())
	}

	stubDiscover(t, nil, errors.New("run ps: failed"))
	stdout.Reset()
	stderr.Reset()
	if code := runSnapshot([]string{"--json"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("fatal snapshot = %d %q", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "run ps: failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUsageListsSubcommands(t *testing.T) {
	fs := flag.NewFlagSet("firekeeper", flag.ContinueOnError)
	var out bytes.Buffer
	fs.SetOutput(&out)
	PrintUsage(fs)
	for _, want := range []string{"snapshot", "export", "report", "backfill", "serve", "daemon"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("usage missing %q:\n%s", want, out.String())
		}
	}
}

func stubDiscover(t *testing.T, metas []session.Meta, err error) {
	t.Helper()
	previous := discoverSessions
	discoverSessions = func(context.Context, session.Options) ([]session.Meta, error) { return metas, err }
	t.Cleanup(func() { discoverSessions = previous })
}

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out, errb bytes.Buffer
		code, ok := RunSubcommand([]string{arg}, &out, &errb)
		if !ok || code != 0 {
			t.Fatalf("%s: ok=%v code=%d stderr=%q", arg, ok, code, errb.String())
		}
		if got := out.String(); !strings.HasPrefix(got, "firekeeper dev (commit dev, built dev") {
			t.Errorf("%s: output %q", arg, got)
		}
	}
	var out, errb bytes.Buffer
	if code, _ := RunSubcommand([]string{"version", "x"}, &out, &errb); code != 2 {
		t.Errorf("extra arg code = %d, want 2", code)
	}
}

func TestApplyReporterConfigSendsVersion(t *testing.T) {
	old := version.Version
	version.Version = "v1.2.3"
	defer func() { version.Version = old }()
	var cfg reporter.Config
	applyReporterConfig(&cfg, config.Config{})
	if cfg.Version != "v1.2.3" {
		t.Errorf("Version = %q", cfg.Version)
	}
}
