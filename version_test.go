package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/reporter"
)

func stubBuild(t *testing.T, v, c, d string, info *debug.BuildInfo) {
	t.Helper()
	pv, pc, pd, pr := version, commit, date, readBuildInfo
	version, commit, date = v, c, d
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, info != nil }
	t.Cleanup(func() { version, commit, date, readBuildInfo = pv, pc, pd, pr })
}

func TestVersionOutput(t *testing.T) {
	stubBuild(t, "0.1.0", "abc1234", "2026-10-08T00:00:00Z", nil)
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code, handled := runSubcommand(args, &stdout, &stderr)
			if !handled || code != 0 {
				t.Fatalf("runSubcommand = (%d, %v), stderr = %q", code, handled, stderr.String())
			}
			want := "firekeeper 0.1.0 (commit abc1234, built 2026-10-08T00:00:00Z)\n"
			if stdout.String() != want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), want)
			}
		})
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	stubBuild(t, "dev", "dev", "dev", nil)
	var stdout, stderr bytes.Buffer
	if code := runVersion([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), "unexpected argument") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestBuildVersion(t *testing.T) {
	vcs := []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}, {Key: "vcs.time", Value: "2026-01-02T03:04:05Z"}}
	tests := []struct {
		name         string
		v, c, d      string
		info         *debug.BuildInfo
		wantV, wantC string
		wantD        string
	}{
		{name: "no build info", v: "dev", c: "dev", d: "dev", wantV: "dev", wantC: "dev", wantD: "dev"},
		{name: "devel build", v: "dev", c: "dev", d: "dev", info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, wantV: "dev", wantC: "dev", wantD: "dev"},
		{name: "go install", v: "dev", c: "dev", d: "dev", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}, Settings: vcs}, wantV: "v0.2.0", wantC: "deadbeef", wantD: "2026-01-02T03:04:05Z"},
		{name: "ldflags win", v: "0.3.0", c: "abc", d: "today", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}, Settings: vcs}, wantV: "0.3.0", wantC: "abc", wantD: "today"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubBuild(t, tt.v, tt.c, tt.d, tt.info)
			v, c, d := buildVersion()
			if v != tt.wantV || c != tt.wantC || d != tt.wantD {
				t.Fatalf("buildVersion = (%q, %q, %q), want (%q, %q, %q)", v, c, d, tt.wantV, tt.wantC, tt.wantD)
			}
		})
	}
}

func TestReporterCommandsSendVersion(t *testing.T) {
	stubBuild(t, "1.2.3", "abc", "today", nil)
	var stdout, stderr bytes.Buffer

	report := stubReport(t, reporter.Summary{}, nil)
	if code := runReport([]string{"--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("report code = %d, stderr = %q", code, stderr.String())
	}
	if report.Version != "1.2.3" {
		t.Fatalf("report Version = %q", report.Version)
	}

	backfill, _ := stubBackfill(t, "", false)
	if code := runBackfill([]string{"--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("backfill code = %d, stderr = %q", code, stderr.String())
	}
	if backfill.Version != "1.2.3" {
		t.Fatalf("backfill Version = %q", backfill.Version)
	}

	daemonCfg := stubDaemon(t, nil)
	if code := runDaemon([]string{"--provider", "codex"}, &stdout, &stderr); code != 0 {
		t.Fatalf("daemon code = %d, stderr = %q", code, stderr.String())
	}
	if daemonCfg.Reporter.Version != "1.2.3" {
		t.Fatalf("daemon Version = %q", daemonCfg.Reporter.Version)
	}
}
