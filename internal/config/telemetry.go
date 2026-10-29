package config

import (
	"fmt"
	"strconv"
	"strings"
)

// SetTelemetry sets the [telemetry] table in the config file at path,
// creating the file when needed. Other keys are left as they are.
func SetTelemetry(path string, enabled bool, port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("config: telemetry port %d is outside 0-65535", port)
	}
	return edit(path, true, func(lines []string) []string {
		return setTelemetry(lines, enabled, port)
	}, func(m map[string]any) bool {
		table, _ := m["telemetry"].(map[string]any)
		if table == nil {
			return false
		}
		on, _ := table["enabled"].(bool)
		got, ok := table["port"].(int64)
		return on == enabled && ok && int(got) == port
	})
}

func setTelemetry(lines []string, enabled bool, port int) []string {
	start, end := telemetrySpan(lines)
	block := []string{
		"[telemetry]",
		"enabled = " + strconv.FormatBool(enabled),
		"port = " + strconv.Itoa(port),
	}
	if start < 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		return append(lines, block...)
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, block...)
	return append(out, lines[end:]...)
}

func telemetrySpan(lines []string) (start, end int) {
	start = -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "[telemetry]" {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(strings.TrimSpace(line), "[") {
			return start, i
		}
	}
	if start >= 0 {
		return start, len(lines)
	}
	return -1, -1
}
