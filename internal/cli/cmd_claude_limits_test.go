package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeLimitsEnableDisableAndStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("FIREKEEPER_CONFIG", filepath.Join(home, "config.toml"))
	// A file named by FIREKEEPER_CONFIG must exist for Load.
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(arg string) (int, string, string) {
		var stdout, stderr strings.Builder
		code := runClaudeLimits([]string{arg}, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, out, _ := run("status"); code != 0 || !strings.Contains(out, "off") {
		t.Fatalf("default status = %d %q", code, out)
	}
	if code, out, errOut := run("enable"); code != 0 || !strings.Contains(out, "undocumented") {
		t.Fatalf("enable = %d %q %q", code, out, errOut)
	}
	// No sign-in on this machine: status says so and does not fail.
	if code, out, _ := run("status"); code != 0 || !strings.Contains(out, "Claude limits: on") || !strings.Contains(out, "no Claude Code sign-in") {
		t.Fatalf("status without sign-in = %d %q", code, out)
	}
	if code, out, _ := run("disable"); code != 0 || !strings.Contains(out, "off") {
		t.Fatalf("disable = %d %q", code, out)
	}
	if code, _, errOut := run("bogus"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("bogus = %d %q", code, errOut)
	}
	var stdout, stderr strings.Builder
	if code := runClaudeLimits(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no args = %d", code)
	}
	body, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || !strings.Contains(string(body), "enabled = false") {
		t.Fatalf("config = %q err %v", body, err)
	}
}

func TestClaudeLimitsStatusReportsWindowsWithoutLeakingToken(t *testing.T) {
	// The endpoint URL is fixed in production, so this only checks the CLI
	// path up to the network: a token on disk is "found", never printed.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("FIREKEEPER_CONFIG", filepath.Join(home, "config.toml"))
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	// Expired on purpose: status must not make a request or print the token.
	creds := `{"claudeAiOauth":{"accessToken":"sk-ant-synthetic-secret","expiresAt":1}}`
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if code := runClaudeLimits([]string{"enable"}, &stdout, &stderr); code != 0 {
		t.Fatalf("enable = %d %s", code, stderr.String())
	}
	stdout.Reset()
	if code := runClaudeLimits([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Sign-in: found") || !strings.Contains(out, "expired") || strings.Contains(out, "synthetic-secret") {
		t.Fatalf("status = %q", out)
	}
}
