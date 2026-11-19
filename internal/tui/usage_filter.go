package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// usage_filter.go narrows the Usage tab's session list with a substring
// search. "/" starts typing a query; sessions whose title contains it come
// first, then sessions whose project or branch contains it. Enter keeps the
// query, Esc clears it. One query applies to every provider's list, and the
// list's title and a filter line above the rows always say it is applied.

// usageFilterMaxRunes caps the query so a large paste cannot grow it unbounded.
const usageFilterMaxRunes = 64

// filterUsageSessions returns the sessions matching query, case-insensitively:
// title matches first, then project or branch matches, each in list order.
// An empty query returns sessions unchanged.
func filterUsageSessions(sessions []usageSession, query string) []usageSession {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return sessions
	}
	var titled, other []usageSession
	for _, session := range sessions {
		switch {
		case strings.Contains(strings.ToLower(session.Title), query):
			titled = append(titled, session)
		case strings.Contains(strings.ToLower(usageProjectName(session)), query),
			strings.Contains(strings.ToLower(session.Branch), query):
			other = append(other, session)
		}
	}
	return append(titled, other...)
}

// startUsageFilter begins typing a query when there is a session list to filter.
func (m *model) startUsageFilter() {
	if m.usagePage().Total == 0 {
		return
	}
	m.usageFilterEditing = true
}

// setUsageFilter replaces the query and selects the first match of every
// provider's list.
func (m *model) setUsageFilter(query string) {
	if query == m.usageFilter {
		return
	}
	m.usageFilter = query
	m.codexHistoryCursor, m.copilotHistoryCursor, m.kimiHistoryCursor, m.claudeHistoryCursor = 0, 0, 0, 0
}

func (m *model) clearUsageFilter() {
	m.usageFilterEditing = false
	m.setUsageFilter("")
}

// clearUsageFilters is Esc outside typing: the query and the project.
func (m *model) clearUsageFilters() {
	m.clearUsageFilter()
	if m.usageProject != "" {
		m.keepUsageSelection(func() { m.usageProject = "" })
	}
}

// usageNoMatchText says why a narrowed list is empty.
func usageNoMatchText(page usagePage) string {
	text := "No " + page.Label + " sessions"
	if page.Project != "" {
		text += " in project " + sanitizeProcessCommand(page.Project)
	}
	if filter := strings.TrimSpace(page.Filter); filter != "" {
		text += fmt.Sprintf(" match %q", filter)
	}
	return text + "."
}

// updateUsageFilterKey handles keys while the query is being typed: text
// edits it, arrows still move the selection, Enter keeps it and Esc clears it.
func (m model) updateUsageFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.clearUsageFilter()
	case tea.KeyEnter:
		m.usageFilterEditing = false
		m.setUsageFilter(strings.TrimSpace(m.usageFilter))
	case tea.KeyBackspace:
		if runes := []rune(m.usageFilter); len(runes) > 0 {
			m.setUsageFilter(string(runes[:len(runes)-1]))
		}
	case tea.KeyCtrlU:
		m.setUsageFilter("")
	case tea.KeyUp:
		m.moveUsageCursor(-1)
	case tea.KeyDown:
		m.moveUsageCursor(1)
	case tea.KeyPgUp:
		m.moveUsageCursor(-usageSessionPage)
	case tea.KeyPgDown:
		m.moveUsageCursor(usageSessionPage)
	case tea.KeySpace:
		m.appendUsageFilter([]rune{' '})
	case tea.KeyRunes:
		m.appendUsageFilter(msg.Runes)
	}
	return m, nil
}

// appendUsageFilter adds typed or pasted text, dropping control characters
// and anything past usageFilterMaxRunes.
func (m *model) appendUsageFilter(runes []rune) {
	query := []rune(m.usageFilter)
	for _, r := range runes {
		if len(query) >= usageFilterMaxRunes {
			break
		}
		if unicode.IsPrint(r) {
			query = append(query, r)
		}
	}
	m.setUsageFilter(string(query))
}

// usageFilterLine is the row above the session list that shows the project
// and the query, a caret while it is typed, and how to change them.
func usageFilterLine(page usagePage, width int) string {
	line := ""
	if page.Project != "" {
		line += dashPaint("project ", usageMutedColor, false) + dashPaint(sanitizeProcessCommand(page.Project), dashTeal, true)
	}
	query := page.Filter // appendUsageFilter keeps printable runes only
	if page.FilterEditing {
		query += "▏"
	}
	if query != "" {
		if line != "" {
			line += dashPaint("  ", usageMutedColor, false)
		}
		line += dashPaint("filter ", usageMutedColor, false) + dashPaint(query, dashOrange, true)
	}
	hint := "/ search · esc clear"
	switch {
	case page.FilterEditing:
		hint = "enter keep · esc clear"
	case strings.TrimSpace(page.Filter) != "":
		hint = "/ edit · esc clear"
	}
	used := ansi.StringWidth(line)
	if used+2+ansi.StringWidth(hint) > width {
		return ansi.Truncate(line, width, "…")
	}
	return line + dashPaint(strings.Repeat(" ", width-used-ansi.StringWidth(hint))+hint, usageMutedColor, false)
}
