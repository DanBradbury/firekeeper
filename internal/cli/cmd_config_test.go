package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// testConfigHome is the home every main test reads its config from, so no
// test ever sees the developer's ~/.firekeeper/config.toml.
var testConfigHome string

func TestMain(m *testing.M) {
	runHelperProcess() // exits when this process is a handoff test helper
	dir, err := os.MkdirTemp("", "firekeeper-config-home")
	if err != nil {
		panic(err)
	}
	testConfigHome = dir
	configHome = func() (string, error) { return testConfigHome, nil }
	isolateServerLookup()
	for _, k := range []string{config.EnvConfig, config.EnvServer, config.EnvToken, config.EnvProviders, config.EnvInterval} {
		os.Unsetenv(k)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// withConfigFile writes body as the config file for one test.
func withConfigFile(t *testing.T, body string) {
	t.Helper()
	path := config.DefaultPath(testConfigHome)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
}

func TestReportUsesConfigFile(t *testing.T) {
	withConfigFile(t, `
server = "http://file:1"
token = "fk_file_token_0123456789"
providers = ["claude"]
exclude = ["~/clients", "github.com/acme"]
[redact]
paths = ["~/private"]
`)
	tests := []struct {
		name      string
		env       map[string]string
		args      []string
		server    string
		token     string
		providers []transcript.Provider
	}{
		{"file", nil, nil, "http://file:1", "fk_file_token_0123456789", []transcript.Provider{transcript.ProviderClaude}},
		{"env over file", map[string]string{config.EnvServer: "http://env:2", config.EnvProviders: "codex"}, nil,
			"http://env:2", "fk_file_token_0123456789", []transcript.Provider{transcript.ProviderCodex}},
		{"flags over env", map[string]string{config.EnvServer: "http://env:2", config.EnvToken: "fk_env"},
			[]string{"--server", "http://flag:3", "--token", "fk_flag", "--provider", "copilot"},
			"http://flag:3", "fk_flag", []transcript.Provider{transcript.ProviderCopilot}},
		{"default flag value given explicitly still wins", map[string]string{config.EnvServer: "http://env:2"},
			[]string{"--server", reporter.DefaultServer}, reporter.DefaultServer, "fk_file_token_0123456789",
			[]transcript.Provider{transcript.ProviderClaude}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg := stubReport(t, reporter.Summary{}, nil)
			var stdout, stderr bytes.Buffer
			if code := runReport(append([]string{"--dry-run"}, tt.args...), &stdout, &stderr); code != 0 {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			if cfg.Server != tt.server || cfg.Token != tt.token || !slices.Equal(cfg.Providers, tt.providers) {
				t.Fatalf("config = server %q token %q providers %v", cfg.Server, cfg.Token, cfg.Providers)
			}
			wantExclude := []string{filepath.Join(testConfigHome, "clients"), "github.com/acme"}
			if !slices.Equal(cfg.Exclude, wantExclude) || !slices.Equal(cfg.RedactPaths, []string{filepath.Join(testConfigHome, "private")}) {
				t.Fatalf("exclude %q redact %q", cfg.Exclude, cfg.RedactPaths)
			}
		})
	}
}

func TestReportBadConfig(t *testing.T) {
	withConfigFile(t, `providers = "codex"`)
	stubReport(t, reporter.Summary{}, nil)
	var stdout, stderr bytes.Buffer
	if code := runReport(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "config") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestConfigShowMasksToken(t *testing.T) {
	withConfigFile(t, `
token = "fk_supersecret_token_ABCD"
providers = ["codex"]
scan_lookback = "6h"
`)
	t.Setenv(config.EnvInterval, "45s")
	var stdout, stderr bytes.Buffer
	if code := runConfig([]string{"show"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "supersecret") {
		t.Fatalf("token not masked:\n%s", out)
	}
	for _, want := range []string{
		`token = "********ABCD"  # file`,
		`providers = ["codex"]  # file`,
		`interval = "45s"  # env`,
		`server = "` + reporter.DefaultServer + `"  # default`,
		`scan_ended = true  # default`,
		`scan_lookback = "6h0m0s"  # file`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code := runConfig(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("bare config: code %d", code)
	}
}

func TestDaemonUsesConfigFile(t *testing.T) {
	withConfigFile(t, `
providers = ["codex"]
interval = "40s"
token = "fk_daemon_token_0123456789"
exclude = ["/srv/secret"]
`)
	cfg := stubDaemon(t, nil)
	var stdout, stderr bytes.Buffer
	if code := runDaemon([]string{"--quiet"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	r := cfg.Reporter
	if cfg.Interval != 40*time.Second || r.Token != "fk_daemon_token_0123456789" || !slices.Equal(r.Providers, []transcript.Provider{transcript.ProviderCodex}) || !slices.Equal(r.Exclude, []string{filepath.Clean("/srv/secret")}) {
		t.Fatalf("config = %+v", *cfg)
	}

	// install records only the flags given, so later config edits apply.
	args, _, _ := stubService(t, "darwin")
	if code := runDaemon([]string{"install", "--interval", "1m"}, &stdout, &stderr); code != 0 {
		t.Fatalf("install code = %d, stderr = %q", code, stderr.String())
	}
	if want := []string{"--quiet", "--interval", "1m0s"}; !slices.Equal(*args, want) {
		t.Fatalf("args = %q, want %q", *args, want)
	}
}
