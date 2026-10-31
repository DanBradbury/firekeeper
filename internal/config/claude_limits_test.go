package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeLimitsConfigRoundTripKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "server = \"http://127.0.0.1:7777\"\n\n[telemetry]\nenabled = true\nport = 4318\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadFile(t, path)
	if err != nil || !got.ClaudeLimits {
		t.Fatalf("default = %+v err %v, want on by default", got, err)
	}
	for _, enabled := range []bool{false, false, true, false} {
		if err := SetClaudeLimits(path, enabled); err != nil {
			t.Fatal(err)
		}
		got, err = loadFile(t, path)
		if err != nil || got.ClaudeLimits != enabled {
			t.Fatalf("after set %v: %+v err %v", enabled, got, err)
		}
		if got.Server != "http://127.0.0.1:7777" || !got.TelemetryEnabled {
			t.Fatalf("other keys lost: %+v", got)
		}
	}
	// A file without the table still means on.
	if err := os.WriteFile(path, []byte("server = \"http://127.0.0.1:7777\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err = loadFile(t, path); err != nil || !got.ClaudeLimits {
		t.Fatalf("file without table = %+v err %v, want on", got, err)
	}
	if err := SetClaudeLimits(path, false); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Count(string(body), "[claude_limits]") != 1 {
		t.Fatalf("table duplicated:\n%s", body)
	}
}
