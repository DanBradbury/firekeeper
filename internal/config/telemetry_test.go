package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTelemetryConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("server = \"http://127.0.0.1:7777\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetTelemetry(path, true, 4318); err != nil {
		t.Fatal(err)
	}
	if err := SetTelemetry(path, true, 4318); err != nil {
		t.Fatal(err)
	}
	got, err := loadFile(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TelemetryEnabled || got.TelemetryPort != 4318 || got.Server != "http://127.0.0.1:7777" {
		t.Fatalf("config = %+v", got)
	}
	if err := SetTelemetry(path, false, 4318); err != nil {
		t.Fatal(err)
	}
	got, err = loadFile(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if got.TelemetryEnabled || got.Server != "http://127.0.0.1:7777" {
		t.Fatalf("after disable = %+v", got)
	}
}

func loadFile(t *testing.T, path string) (Config, error) {
	t.Helper()
	return Load(Options{Getenv: func(key string) string {
		if key == EnvConfig {
			return path
		}
		return ""
	}})
}
