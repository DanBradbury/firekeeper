package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// usage_filter.go narrows the Usage tab's session list with a query (see
// usage_query.go for its syntax). "/" starts typing it; sessions whose title
// contains its text come first, then sessions whose project or branch
// contains it. Enter keeps the query, Esc clears it. One query applies to
// every provider's list, and the list's title and a filter line above the
// rows always say it is applied.

// usageFilterMaxRunes caps the query so a large paste cannot grow it unbounded.
const usageFilterMaxRunes = 64

// filterUsageSessions returns the sessions matching the raw query.
func filterUsageSessions(sessions []usageSession, raw string) []usageSession {
	query := parseUsageQuery(raw)
	return filterUsageText(filterUsageProjects(sessions, query.Projects), query.Text)
}

// filterUsageText returns the sessions containing text, case-insensitively:
// title matches first, then project or branch matches, each in list order.
// An empty text returns sessions unchanged.
func filterUsageText(sessions []usageSession, text string) []usageSession {
	query := strings.ToLower(strings.TrimSpace(text))
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

// startUsageFilter begins typing a query when there is a session list to
// filter. Typing goes on as a new word after a kept query, such as the
// project: qualifier p writes.
func (m *model) startUsageFilter() {
	if m.usagePage().Total == 0 {
		return
	}
	m.usageFilterEditing = true
	if m.usageFilter != "" && !strings.HasSuffix(m.usageFilter, " ") {
		m.usageFilter += " "
	}
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

// clearUsageFilter is Esc: it stops typing and clears the query, keeping
// the selected session selected.
func (m *model) clearUsageFilter() {
	m.keepUsageSelection(func() {
		m.usageFilterEditing = false
		m.usageFilter = ""
	})
}

// usageNoMatchText says why a narrowed list is empty.
func usageNoMatchText(page usagePage) string {
	text := "No " + page.Label + " sessions"
	if len(page.Query.Projects) > 0 {
		text += " in project " + usageProjectList(page.Query.Projects)
	}
	if page.Query.Text != "" {
		text += fmt.Sprintf(" match %q", page.Query.Text)
	}
	return text + "."
}

// usageProjectList names the projects a query shows, for titles and messages.
func usageProjectList(projects []string) string {
	names := make([]string, len(projects))
	for index, project := range projects {
		names[index] = sanitizeProcessCommand(project)
	}
	return strings.Join(names, " or ")
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
	// appendUsageFilter keeps the query to printable runes.
	line := dashPaint("filter ", usageMutedColor, false) + paintUsageQuery(page.Filter)
	hint := "/ edit · esc clear"
	if page.FilterEditing {
		line += dashPaint("▏", dashOrange, true)
		hint = "enter keep · esc clear"
	}
	used := ansi.StringWidth(line)
	if used+2+ansi.StringWidth(hint) > width {
		return ansi.Truncate(line, width, "…")
	}
	return line + dashPaint(strings.Repeat(" ", width-used-ansi.StringWidth(hint))+hint, usageMutedColor, false)
}
