package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// help.go renders the ? overlay listing every hotkey. While it is open it
// takes over the content area, and any key closes it without running that
// key's action. Ctrl+C still quits so the app is never stuck.

type helpKey struct{ key, action string }

type helpSection struct {
	title string
	keys  []helpKey
}

var helpSections = []helpSection{
	{"Global", []helpKey{
		{"Tab / Shift+Tab", "Switch between Processes, Overview and Usage"},
		{"?", "Show or hide this help"},
		{"q / Ctrl+C", "Quit"},
	}},
	{"Processes", []helpKey{
		{"↑ ↓ / j k", "Select runtime"},
		{"PgUp / PgDn", "Move half a page"},
		{"Home / End", "Jump to first or last runtime"},
		{"Enter", "Expand details inline when the bottom session panel is hidden"},
		{"s", "Switch to the runtime's terminal"},
		{"c", "Open the session's chat history"},
		{"Enter", "With nothing running, open the selected recent session's chat"},
		{"R", "With nothing running, resume the selected recent session in a new tab (Ghostty, iTerm2, Terminal.app)"},
		{"r", "Refresh now"},
	}},
	{"Chat", []helpKey{
		{"↑ ↓ / j k", "Scroll a line"},
		{"PgUp / PgDn", "Scroll a page"},
		{"g / G", "Jump to the first or latest message"},
		{"r", "Reload the transcript"},
		{"Esc / c / q", "Back"},
	}},
	{"Overview", []helpKey{
		{"r", "Refresh every provider"},
	}},
	{"Usage", []helpKey{
		{"← → / h l", "Switch provider"},
		{"Enter", "Open the selected session's chat"},
		{"↑ ↓ / j k", "Select a session (PgUp/PgDn/Home/End also work)"},
		{"r", "Refresh selected provider"},
	}},
}

func (m model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.helpOpen = false
	return m, nil
}

func helpLines() []string {
	keyWidth := 0
	for _, section := range helpSections {
		for _, key := range section.keys {
			keyWidth = max(keyWidth, ansi.StringWidth(key.key))
		}
	}
	var lines []string
	for index, section := range helpSections {
		if index > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, dashPaint(section.title, dashOrange, true))
		for _, key := range section.keys {
			lines = append(lines,
				dashPaint("  "+dashCell(key.key, keyWidth, false)+"  ", dashBlue, true)+
					dashPaint(key.action, usageTextColor, false),
			)
		}
	}
	return lines
}

func (m model) viewHelp(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	body := helpLines()
	width := min(m.width, 64)
	height := min(contentRows, len(body)+2)
	box := dashBox("keys", body, width, height, dashBlue)
	left := (m.width - width) / 2
	top := (contentRows - height) / 2
	lines := make([]string, 0, contentRows)
	for len(lines) < top {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	for _, row := range box {
		lines = append(lines, dashFit(dashPaint(strings.Repeat(" ", left), usageTextColor, false)+row, m.width, usagePanelColor))
	}
	for len(lines) < contentRows {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	return strings.Join(lines[:contentRows], "\n")
}

func (m model) helpFooter() string {
	keys := []footerKey{{"any key", "close"}}
	_, available := footerVersion(m.width)
	if footerKeysWidth(keys, 1) > available {
		keys[0].action = "back"
	}
	return renderFooterKeys(keys, m.width)
}
