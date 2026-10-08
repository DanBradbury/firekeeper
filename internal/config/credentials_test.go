package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveCredentialsCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".firekeeper", "config.toml")
	if err := SaveCredentials(path, "https://fk.example.com", "fk_secret_token_value"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %o, want 600", info.Mode().Perm())
		}
		dir, _ := os.Stat(filepath.Dir(path))
		if dir.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode %o, want 700", dir.Mode().Perm())
		}
	}
	c, err := Load(Options{Home: t.TempDir(), Getenv: func(k string) string {
		if k == EnvConfig {
			return path
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "https://fk.example.com" || c.Token != "fk_secret_token_value" || len(c.Providers) != 0 {
		t.Fatalf("loaded %+v", c)
	}
}

func TestSaveCredentialsKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := `# my settings
providers = ["claude"]
server = "http://old.example"
exclude = ["~/clients"]  # keep private

[redact]
paths = ["~/private"]
token = "not the top-level token"
`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(path, "https://new.example", "fk_abc"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := `token = "fk_abc"
# my settings
providers = ["claude"]
server = "https://new.example"
exclude = ["~/clients"]  # keep private

[redact]
paths = ["~/private"]
token = "not the top-level token"
`
	if string(got) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("a world-readable config kept mode %o after storing a token", info.Mode().Perm())
		}
	}

	// Replacing is idempotent in place.
	if err := SaveCredentials(path, "https://new.example", "fk_def"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if strings.Count(string(got), "token = \"fk_") != 1 || !strings.Contains(string(got), `token = "fk_def"`) {
		t.Fatalf("file:\n%s", got)
	}
}

func TestClearToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if removed, err := ClearToken(path); err != nil || removed {
		t.Fatalf("missing file: %v, %v", removed, err)
	}
	os.WriteFile(path, []byte("server = \"https://x.example\"\ntoken = \"fk_abc\"\n[redact]\ntoken = \"keep\"\n"), 0o600)
	removed, err := ClearToken(path)
	if err != nil || !removed {
		t.Fatalf("clear = %v, %v", removed, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "server = \"https://x.example\"\n[redact]\ntoken = \"keep\"\n" {
		t.Fatalf("file:\n%s", got)
	}
	if removed, err := ClearToken(path); err != nil || removed {
		t.Fatalf("second clear = %v, %v", removed, err)
	}
}

func TestSaveCredentialsRefusesWhatItCannotEditSafely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	broken := "server = \n"
	os.WriteFile(path, []byte(broken), 0o600)
	if err := SaveCredentials(path, "https://x.example", "fk_abc"); err == nil {
		t.Fatal("edited a file that is not valid TOML")
	} else if strings.Contains(err.Error(), "fk_abc") {
		t.Fatalf("error repeats the token: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != broken {
		t.Fatalf("file changed: %q", got)
	}
	for _, bad := range []string{"", "has space", "quo\"te", "back\\slash", "new\nline"} {
		if err := SaveCredentials(filepath.Join(dir, "other.toml"), "https://x.example", bad); err == nil {
			t.Errorf("accepted token %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.toml")); err == nil {
		t.Fatal("wrote a file for a rejected value")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestResolvePath(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == EnvConfig {
				return v
			}
			return ""
		}
	}
	if p, _ := ResolvePath("/home/u", env("")); p != "/home/u/.firekeeper/config.toml" {
		t.Fatalf("default = %q", p)
	}
	if p, _ := ResolvePath("/home/u", env("~/alt.toml")); p != "/home/u/alt.toml" {
		t.Fatalf("env = %q", p)
	}
}
