package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeConfig(t *testing.T, body string) (string, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".firekeeper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func TestLoadPrecedence(t *testing.T) {
	file := `
server = "http://file:1"
token = "filetoken12345"
providers = ["codex"]
interval = "1m"
exclude = ["/a"]
repo_url_template = "https://x/{path}"
[redact]
paths = ["/secret"]
[prices."m1"]
input = 3
output = 15
`
	tests := []struct {
		name string
		file string
		env  map[string]string
		want func(Config) bool
	}{
		{"defaults", "", nil, func(c Config) bool {
			return c.Server == DefaultServer && time.Duration(c.Interval) == DefaultInterval && c.Token == "" && len(c.Providers) == 0
		}},
		{"file over defaults", file, nil, func(c Config) bool {
			return c.Server == "http://file:1" && c.Token == "filetoken12345" && time.Duration(c.Interval) == time.Minute &&
				c.Providers[0] == "codex" && c.Exclude[0] == "/a" && c.Redact.Paths[0] == "/secret" &&
				c.Prices["m1"].Output == 15 && c.RepoURLTemplate == "https://x/{path}"
		}},
		{"env over file", file, map[string]string{"FIREKEEPER_SERVER": "http://env:2", "FIREKEEPER_PROVIDERS": "codex, copilot", "FIREKEEPER_INTERVAL": "5s"}, func(c Config) bool {
			return c.Server == "http://env:2" && len(c.Providers) == 2 && time.Duration(c.Interval) == 5*time.Second && c.Token == "filetoken12345"
		}},
		{"env over defaults", "", map[string]string{"FIREKEEPER_TOKEN": "t"}, func(c Config) bool {
			return c.Token == "t" && c.Server == DefaultServer
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.file != "" {
				home, _ = writeConfig(t, tt.file)
			}
			cfg, err := Load(env(tt.env), home)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.want(cfg) {
				t.Fatalf("config = %+v", cfg)
			}
		})
	}
}

func TestLoadEnvPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(`server = "http://override:9"`), 0o600)
	cfg, err := Load(env(map[string]string{EnvPath: path}), t.TempDir())
	if err != nil || cfg.Server != "http://override:9" {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
}

func TestLoadErrorsDoNotLeakValues(t *testing.T) {
	home, _ := writeConfig(t, "token = \"supersecretvalue\nserver = ")
	_, err := Load(env(nil), home)
	if err == nil || strings.Contains(err.Error(), "supersecretvalue") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(env(map[string]string{"FIREKEEPER_INTERVAL": "bogus"}), t.TempDir()); err == nil {
		t.Fatal("want interval error")
	}
}

func TestShowMasksToken(t *testing.T) {
	cfg := Defaults()
	cfg.Token = "abcdefghijkl"
	cfg.Exclude = []string{"/x"}
	var buf bytes.Buffer
	if err := cfg.Show(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "abcdefghijkl") || !strings.Contains(out, `token = "****ijkl"`) || !strings.Contains(out, `exclude = ["/x"]`) {
		t.Fatalf("out = %s", out)
	}
	if MaskToken("short") != "****" || MaskToken("") != "" {
		t.Fatal("mask")
	}
}

func TestExcluder(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work", "acme", "app")
	sub := filepath.Join(repo, "pkg")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n\turl = nope\n[remote \"origin\"]\n\turl = git@github.com:Acme/App.git\n"), 0o644)
	other := filepath.Join(root, "play")
	os.MkdirAll(other, 0o755)

	tests := []struct {
		name     string
		patterns []string
		cwd      string
		want     bool
	}{
		{"none", nil, sub, false},
		{"exact dir", []string{repo}, repo, true},
		{"ancestor dir", []string{filepath.Join(root, "work")}, sub, true},
		{"glob dir", []string{filepath.Join(root, "*", "acme")}, sub, true},
		{"unrelated dir", []string{filepath.Join(root, "work", "other")}, sub, false},
		{"other cwd", []string{repo}, other, false},
		{"scp remote", []string{"git@github.com:acme/*"}, sub, true},
		{"normalized remote", []string{"github.com/acme/*"}, sub, true},
		{"remote exact", []string{"github.com/acme/app"}, sub, true},
		{"remote other org", []string{"github.com/other/*"}, sub, false},
		{"remote no git", []string{"github.com/acme/*"}, other, false},
		{"empty cwd", []string{repo}, "", false},
		{"blank pattern", []string{"  "}, sub, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := NewExcluder(tt.patterns, root).Excluded(tt.cwd)
			if got != tt.want {
				t.Fatalf("Excluded = %v, want %v", got, tt.want)
			}
		})
	}
	if _, ok := NewExcluder([]string{"~/proj"}, "/h").Excluded("/h/proj/x"); !ok {
		t.Fatal("tilde expansion")
	}
	var nilEx *Excluder
	if _, ok := nilEx.Excluded(repo); ok {
		t.Fatal("nil excluder")
	}
}
