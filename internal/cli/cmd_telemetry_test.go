package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryEnableDisableIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("FIREKEEPER_CONFIG", filepath.Join(home, "config.toml"))
	if err := os.MkdirAll(filepath.Join(home, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"theme\": \"dark\",\n  \"env\": {\"PATH\": \"/usr/bin\"}\n}\n")
	settings := filepath.Join(home, "claude", "settings.json")
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := runTelemetry([]string{"enable"}, &stdout, &stderr); code != 0 {
		t.Fatalf("enable = %d %s", code, stderr.String())
	}
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"theme"`) || !strings.Contains(text, "dark") || !strings.Contains(text, `"PATH"`) || !strings.Contains(text, "/usr/bin") {
		t.Fatalf("settings lost user keys:\n%s", text)
	}
	if !strings.Contains(text, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`) || !strings.Contains(text, "127.0.0.1:4318") {
		t.Fatalf("settings missing telemetry env:\n%s", text)
	}
	if strings.Contains(text, "OTEL_LOG_USER_PROMPTS") || strings.Contains(text, "SECRET") {
		t.Fatalf("settings contain prompt logging or a secret:\n%s", text)
	}
	backup, err := os.ReadFile(settings + ".firekeeper-backup")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup = %q err %v", backup, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runTelemetry([]string{"enable"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "already set") {
		t.Fatalf("second enable = %d %s %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runTelemetry([]string{"status"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "exporting to http://127.0.0.1:4318") || !strings.Contains(stdout.String(), "listener enabled") {
		t.Fatalf("status = %d\n%s", code, stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runTelemetry([]string{"disable"}, &stdout, &stderr); code != 0 {
		t.Fatalf("disable = %d %s", code, stderr.String())
	}
	body, err = os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	text = string(body)
	if strings.Contains(text, "CLAUDE_CODE_ENABLE_TELEMETRY") || strings.Contains(text, "OTEL_") {
		t.Fatalf("disable left telemetry env:\n%s", text)
	}
	if !strings.Contains(text, `"theme"`) || !strings.Contains(text, "dark") || !strings.Contains(text, `"PATH"`) || !strings.Contains(text, "/usr/bin") {
		t.Fatalf("disable lost user keys:\n%s", text)
	}
	stdout.Reset()
	if code := runTelemetry([]string{"status"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "not set") || !strings.Contains(stdout.String(), "listener off") {
		t.Fatalf("status after disable:\n%s", stdout.String())
	}
}
