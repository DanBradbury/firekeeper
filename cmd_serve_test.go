package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/api"
)

func TestParseByteSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"1048576", 1 << 20, true},
		{"500MB", 500_000_000, true},
		{"500 mb", 500_000_000, true},
		{"2GiB", 2 << 30, true},
		{"10kib", 10 << 10, true},
		{"1TB", 1_000_000_000_000, true},
		{"7b", 7, true},
		{"", 0, false},
		{"-1", 0, false},
		{"1.5GB", 0, false},
		{"ten", 0, false},
		{"MB", 0, false},
		{"9999999999TiB", 0, false},
	} {
		got, err := parseByteSize(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestServeLimitAndBannerFlags(t *testing.T) {
	var got server.Config
	previous := serveRun
	serveRun = func(_ context.Context, cfg server.Config) error { got = cfg; return nil }
	t.Cleanup(func() { serveRun = previous })
	withConfigFile(t, "")

	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		return runServe(args, &out, &errb), errb.String()
	}
	if code, _ := run(); code != 0 || got.Limits != api.DefaultLimits() || got.Banner != "" {
		t.Fatalf("defaults: %+v", got)
	}
	if d := api.DefaultLimits(); d.MaxBytes <= 0 || d.MaxSessions <= 0 || d.IngestPerMinute <= 0 {
		t.Fatalf("a default limit is off: %+v", d)
	}
	code, stderr := run("--max-bytes", "50MB", "--max-sessions", "12", "--max-ingest-per-minute", "7", "--banner", "Test bed: data may be wiped")
	want := api.Limits{MaxBytes: 50_000_000, MaxSessions: 12, IngestPerMinute: 7}
	if code != 0 || got.Limits != want || got.Banner != "Test bed: data may be wiped" {
		t.Fatalf("flags: code %d stderr %q cfg %+v", code, stderr, got)
	}
	// Zero turns a limit off.
	if code, _ := run("--max-bytes", "0", "--max-sessions", "0", "--max-ingest-per-minute", "0"); code != 0 || got.Limits != (api.Limits{}) {
		t.Fatalf("zero limits: %+v", got.Limits)
	}
	for _, args := range [][]string{
		{"--max-bytes", "lots"},
		{"--max-bytes", "-5"},
		{"--max-sessions", "-1"},
		{"--max-ingest-per-minute", "-1"},
		{"--banner", "two\nlines"},
		{"--banner", strings.Repeat("x", server.MaxBannerLen+1)},
	} {
		if code, _ := run(args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
}

func TestValidateBanner(t *testing.T) {
	for _, ok := range []string{"", "Test bed", "Dæta may be wiped — ☃", strings.Repeat("x", server.MaxBannerLen)} {
		if err := server.ValidateBanner(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a\nb", "tab\there", "bell\x07", "del\x7f", strings.Repeat("x", server.MaxBannerLen+1)} {
		if err := server.ValidateBanner(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestServeTrustedProxyFlag(t *testing.T) {
	var got server.Config
	previous := serveRun
	serveRun = func(_ context.Context, cfg server.Config) error { got = cfg; return nil }
	t.Cleanup(func() { serveRun = previous })
	withConfigFile(t, "")

	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		return runServe(args, &out, &errb), errb.String()
	}
	if code, _ := run(); code != 0 || len(got.TrustedProxies) != 0 {
		t.Fatalf("default must trust no proxy: %+v", got.TrustedProxies)
	}
	if code, stderr := run("--trusted-proxy", "10.0.0.5", "--trusted-proxy", "172.29.77.0/24"); code != 0 || len(got.TrustedProxies) != 2 {
		t.Fatalf("flags: code %d stderr %q proxies %v", code, stderr, got.TrustedProxies)
	}
	for _, args := range [][]string{
		{"--trusted-proxy", "caddy"},
		{"--trusted-proxy", "0.0.0.0/0"},
		{"--trusted-proxy", "::/0"},
		{"--trusted-proxy", ""},
	} {
		if code, stderr := run(args...); code != 2 || !strings.Contains(stderr, "trusted proxy") {
			t.Errorf("%v: code %d stderr %q, want 2", args, code, stderr)
		}
	}

	// The config file supplies a default; the flag replaces it.
	withConfigFile(t, `trusted_proxies = ["10.1.0.0/16", " "]`)
	if code, stderr := run(); code != 0 || len(got.TrustedProxies) != 1 || got.TrustedProxies[0].String() != "10.1.0.0/16" {
		t.Fatalf("config file: code %d stderr %q proxies %v", code, stderr, got.TrustedProxies)
	}
	if code, _ := run("--trusted-proxy", "10.2.0.1"); code != 0 || len(got.TrustedProxies) != 1 || got.TrustedProxies[0].String() != "10.2.0.1/32" {
		t.Fatalf("flag over config: %v", got.TrustedProxies)
	}
	withConfigFile(t, `trusted_proxies = ["0.0.0.0/0"]`)
	if code, _ := run(); code != 2 {
		t.Fatalf("config file trusting everything: code %d", code)
	}
}
