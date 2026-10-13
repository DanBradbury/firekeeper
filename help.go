package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// help.go renders the ? overlay listing every hotkey. While it is open it
// takes over the content area and swallows keys other than close and quit.

type helpKey struct{ key, action string }

type helpSection struct {
	title string
	keys  []helpKey
}

var helpSections = []helpSection{
	{"Global", []helpKey{
		{"Tab / Shift+Tab", "Switch between Processes and Usage"},
		{"?", "Show or hide this help"},
		{"q / Ctrl+C", "Quit"},
	}},
	{"Processes", []helpKey{
		{"↑ ↓ / j k", "Select runtime"},
		{"PgUp / PgDn", "Move half a page"},
		{"Home / End", "Jump to first or last runtime"},
		{"Enter", "Expand session details and process tree"},
		{"s", "Switch to the runtime's terminal"},
		{"r", "Refresh now"},
	}},
	{"Usage", []helpKey{
		{"← → / h l", "Switch provider"},
		{"Enter", "Open or close session history"},
		{"↑ ↓ / j k", "Select a history session"},
		{"Esc / Backspace", "Close session history"},
		{"r", "Refresh selected provider"},
	}},
}

func (m model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?", "esc", "backspace", "enter":
		m.helpOpen = false
	}
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

func (m model) helpFooter() (string, string) {
	help := dashPaint(" ", usageTextColor, false) +
		dashPaint("? / esc ", dashBlue, true) + dashPaint("close  ", usageTextColor, false) +
		dashPaint("q ", dashBlue, true) + dashPaint("quit", usageTextColor, false)
	return dashFit(help, m.width, usagePanelColor), dashFit(dashPaint("  keyboard shortcuts", usageMutedColor, false), m.width, usagePanelColor)
}
