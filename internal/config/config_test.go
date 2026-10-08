package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := DefaultPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var defaults = Config{Server: "http://default", Interval: 15 * time.Second}

func ptr[T any](v T) *T { return &v }

func TestPrecedence(t *testing.T) {
	const fileBody = `
server = "http://file"
token = "file-token"
providers = ["codex"]
interval = "1m"
`
	codex, claude, copilot := transcript.ProviderCodex, transcript.ProviderClaude, transcript.ProviderCopilot
	tests := []struct {
		name      string
		file      string
		env       map[string]string
		flags     Overrides
		server    string
		token     string
		providers []transcript.Provider
		interval  time.Duration
		sources   map[string]string
	}{
		{name: "defaults only", server: "http://default", interval: 15 * time.Second, providers: []transcript.Provider{},
			sources: map[string]string{"server": "default", "token": "default", "providers": "default", "interval": "default"}},
		{name: "file over defaults", file: fileBody, server: "http://file", token: "file-token",
			providers: []transcript.Provider{codex}, interval: time.Minute,
			sources: map[string]string{"server": "file", "token": "file", "providers": "file", "interval": "file"}},
		{name: "env over file", file: fileBody,
			env:    map[string]string{EnvServer: "http://env", EnvToken: "env-token", EnvProviders: "claude, copilot", EnvInterval: "2m"},
			server: "http://env", token: "env-token", providers: []transcript.Provider{claude, copilot}, interval: 2 * time.Minute,
			sources: map[string]string{"server": "env", "token": "env", "providers": "env", "interval": "env"}},
		{name: "flags over env", file: fileBody,
			env:    map[string]string{EnvServer: "http://env", EnvToken: "env-token", EnvProviders: "claude", EnvInterval: "2m"},
			flags:  Overrides{Server: ptr("http://flag"), Token: ptr("flag-token"), Providers: []transcript.Provider{copilot}, Interval: ptr(3 * time.Minute)},
			server: "http://flag", token: "flag-token", providers: []transcript.Provider{copilot}, interval: 3 * time.Minute,
			sources: map[string]string{"server": "flag", "token": "flag", "providers": "flag", "interval": "flag"}},
		{name: "mixed layers", file: fileBody,
			env:    map[string]string{EnvToken: "env-token"},
			flags:  Overrides{Interval: ptr(5 * time.Second)},
			server: "http://file", token: "env-token", providers: []transcript.Provider{codex}, interval: 5 * time.Second,
			sources: map[string]string{"server": "file", "token": "env", "providers": "file", "interval": "flag"}},
		{name: "empty env and flags do not override", file: fileBody,
			env:    map[string]string{EnvServer: "  ", EnvProviders: ""},
			flags:  Overrides{Providers: nil},
			server: "http://file", token: "file-token", providers: []transcript.Provider{codex}, interval: time.Minute,
			sources: map[string]string{"server": "file", "token": "file", "providers": "file", "interval": "file"}},
		{name: "file can list no providers", file: `providers = []`, server: "http://default", interval: 15 * time.Second,
			providers: []transcript.Provider{},
			sources:   map[string]string{"server": "default", "token": "default", "providers": "file", "interval": "default"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.file != "" {
				writeConfig(t, home, tt.file)
			}
			c, err := Load(Options{Home: home, Getenv: env(tt.env), Defaults: defaults})
			if err != nil {
				t.Fatal(err)
			}
			c = c.Apply(tt.flags)
			if c.Server != tt.server || c.Token != tt.token || c.Interval != tt.interval {
				t.Fatalf("got server=%q token=%q interval=%s", c.Server, c.Token, c.Interval)
			}
			if c.Providers == nil {
				c.Providers = []transcript.Provider{}
			}
			if !reflect.DeepEqual(c.Providers, tt.providers) {
				t.Fatalf("providers = %v, want %v", c.Providers, tt.providers)
			}
			if !reflect.DeepEqual(c.Sources, tt.sources) {
				t.Fatalf("sources = %v, want %v", c.Sources, tt.sources)
			}
		})
	}
}

func TestLoadFileSettings(t *testing.T) {
	home := t.TempDir()
	path := writeConfig(t, home, `
exclude = ["~/clients/*", "/srv/secret", "github.com/acme", "  "]
repo_url_template = "https://github.com/me/{project}/blob/{ref}/{path}"

[redact]
paths = ["~/clients", "/opt/private/"]

[prices."gpt-5"]
input = 1.25
output = 10
cache = 0.125
`)
	c, err := Load(Options{Home: home, Getenv: env(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != path {
		t.Fatalf("path = %q", c.Path)
	}
	wantExclude := []string{filepath.Join(home, "clients", "*"), "/srv/secret", "github.com/acme"}
	if !reflect.DeepEqual(c.Exclude, wantExclude) {
		t.Fatalf("exclude = %q, want %q", c.Exclude, wantExclude)
	}
	wantRedact := []string{filepath.Join(home, "clients"), "/opt/private"}
	if !reflect.DeepEqual(c.RedactPaths, wantRedact) {
		t.Fatalf("redact paths = %q", c.RedactPaths)
	}
	if p := c.Prices["gpt-5"]; p != (Price{Input: 1.25, Output: 10, Cache: 0.125}) {
		t.Fatalf("price = %+v", p)
	}
	if c.RepoURLTemplate == "" {
		t.Fatal("repo_url_template not read")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"bad toml", "server = \"x\nsecret-value", "invalid TOML"},
		{"unknown key", "servr = \"x\"\n[redact]\npath = []", "unknown keys: redact.path, servr"},
		{"bad provider", `providers = ["codex", "gpt"]`, `unknown provider "gpt"`},
		{"bad interval", `interval = "soon"`, "not a positive duration"},
		{"relative redact path", "[redact]\npaths = [\"src\"]", "must be absolute"},
		{"bad glob", `exclude = ["/a/[b"]`, "malformed"},
		{"negative price", "[prices.m]\ninput = -1", "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			writeConfig(t, home, tt.body)
			_, err := Load(Options{Home: home, Getenv: env(nil)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error echoes file contents: %v", err)
			}
		})
	}
}

func TestConfigPathOverride(t *testing.T) {
	home := t.TempDir()
	other := filepath.Join(t.TempDir(), "fk.toml")
	if err := os.WriteFile(other, []byte(`server = "http://other"`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, `server = "http://default-file"`)
	c, err := Load(Options{Home: home, Getenv: env(map[string]string{EnvConfig: other})})
	if err != nil || c.Server != "http://other" || c.Path != other {
		t.Fatalf("override: %+v %v", c, err)
	}
	// A missing default file is fine; a missing named file is not.
	if _, err := Load(Options{Home: t.TempDir(), Getenv: env(nil)}); err != nil {
		t.Fatalf("missing default file: %v", err)
	}
	if _, err := Load(Options{Home: home, Getenv: env(map[string]string{EnvConfig: filepath.Join(home, "nope.toml")})}); err == nil {
		t.Fatal("missing FIREKEEPER_CONFIG file: no error")
	}
	if _, err := Load(Options{Home: home, Getenv: env(map[string]string{EnvProviders: "codex,nope"})}); err == nil {
		t.Fatal("bad FIREKEEPER_PROVIDERS: no error")
	}
}

func TestExcluded(t *testing.T) {
	c := Config{Exclude: []string{
		"/home/u/clients/*",
		"/srv/secret",
		"github.com/acme",
		"gitlab.example.com/team/*-private",
	}}
	tests := []struct {
		name    string
		dirs    []string
		remotes []string
		want    string
	}{
		{"glob matches child", []string{"/home/u/clients/acme"}, nil, "/home/u/clients/*"},
		{"glob matches below child", []string{"/home/u/clients/acme/sub/dir"}, nil, "/home/u/clients/*"},
		{"glob does not match parent", []string{"/home/u/clients"}, nil, ""},
		{"plain dir matches itself", []string{"/srv/secret"}, nil, "/srv/secret"},
		{"plain dir matches below", []string{"/srv/secret/x"}, nil, "/srv/secret"},
		{"no partial name match", []string{"/srv/secret-two"}, nil, ""},
		{"git root matches when cwd does not", []string{"/elsewhere/sub", "/srv/secret"}, nil, "/srv/secret"},
		{"ssh scp remote", nil, []string{"git@github.com:acme/api.git"}, "github.com/acme"},
		{"https remote", nil, []string{"https://user@github.com/acme/api"}, "github.com/acme"},
		{"ssh url remote with port", nil, []string{"ssh://git@github.com:22/acme/api.git"}, "github.com/acme"},
		{"remote case-insensitive", nil, []string{"https://GitHub.com/Acme/API.git"}, "github.com/acme"},
		{"other owner", nil, []string{"git@github.com:acme-labs/api.git"}, ""},
		{"remote glob", nil, []string{"https://gitlab.example.com/team/payments-private.git"}, "gitlab.example.com/team/*-private"},
		{"remote glob no match", nil, []string{"https://gitlab.example.com/team/payments"}, ""},
		{"remote pattern is not a dir", []string{"/github.com/acme"}, nil, ""},
		{"nothing", []string{"/home/u/src/app"}, []string{"git@github.com:me/app.git"}, ""},
		{"empty inputs", []string{""}, []string{""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := c.Excluded(tt.dirs, tt.remotes)
			if got != tt.want || ok != (tt.want != "") {
				t.Fatalf("Excluded = (%q, %v), want %q", got, ok, tt.want)
			}
		})
	}
}

func TestMaskToken(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"short":                   "********",
		"fk_0123456789abcdefWXYZ": "********WXYZ",
	} {
		if got := MaskToken(in); got != want {
			t.Errorf("MaskToken(%q) = %q, want %q", in, got, want)
		}
	}
}
