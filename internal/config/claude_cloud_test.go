package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeCloudIsOffByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("providers = [\"claude\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadFile(t, path)
	if err != nil || got.ClaudeCloud || got.ClaudeCloudInterval != 0 {
		t.Fatalf("got %+v err %v; cloud sync must be opt-in", got, err)
	}
}

func TestClaudeCloudTable(t *testing.T) {
	for name, tc := range map[string]struct {
		body     string
		on       bool
		interval time.Duration
		err      string
	}{
		"enabled":          {"[claude_cloud]\nenabled = true\n", true, 0, ""},
		"enabled interval": {"[claude_cloud]\nenabled = true\ninterval = \"30m\"\n", true, 30 * time.Minute, ""},
		"disabled":         {"[claude_cloud]\nenabled = false\n", false, 0, ""},
		"bad interval":     {"[claude_cloud]\nenabled = true\ninterval = \"soon\"\n", false, 0, "claude_cloud.interval"},
		"negative":         {"[claude_cloud]\ninterval = \"-1m\"\n", false, 0, "claude_cloud.interval"},
		"unknown key":      {"[claude_cloud]\nenable = true\n", false, 0, "unknown keys: claude_cloud.enable"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := loadFile(t, path)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got.ClaudeCloud != tc.on || got.ClaudeCloudInterval != tc.interval || got.Sources["claude_cloud"] != "file" {
				t.Fatalf("got %+v err %v", got, err)
			}
		})
	}
}
