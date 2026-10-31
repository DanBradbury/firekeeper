package config

import "strconv"

// SetClaudeLimits sets the [claude_limits] table in the config file at path,
// creating the file when needed. Other keys are left as they are.
func SetClaudeLimits(path string, enabled bool) error {
	return edit(path, true, func(lines []string) []string {
		return setClaudeLimits(lines, enabled)
	}, func(m map[string]any) bool {
		table, _ := m["claude_limits"].(map[string]any)
		on, _ := table["enabled"].(bool)
		return table != nil && on == enabled
	})
}

func setClaudeLimits(lines []string, enabled bool) []string {
	start, end := tableSpan(lines, "[claude_limits]")
	block := []string{"[claude_limits]", "enabled = " + strconv.FormatBool(enabled)}
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
