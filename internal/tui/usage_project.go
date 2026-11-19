package tui

import (
	"fmt"
	"sort"
	"strings"
)

// usage_project.go narrows the Usage tab's session list to projects, the
// working directory names the projects panel groups by, with the / filter's
// project: qualifier. p writes the selected session's project into the
// filter and takes it out again when it is the only one shown; a click on a
// row of the projects panel does the same for that project.

// filterUsageProjects returns the sessions in any of projects, compared
// ignoring case, in list order. No projects returns sessions unchanged.
func filterUsageProjects(sessions []usageSession, projects []string) []usageSession {
	if len(projects) == 0 {
		return sessions
	}
	var matched []usageSession
	for _, session := range sessions {
		if usageProjectIn(usageProjectName(session), projects) {
			matched = append(matched, session)
		}
	}
	return matched
}

func usageProjectIn(name string, projects []string) bool {
	for _, project := range projects {
		if strings.EqualFold(name, project) {
			return true
		}
	}
	return false
}

// toggleUsageProject shows only project, or every project when it is
// already the only one shown. An empty project shows every project.
func (m *model) toggleUsageProject(project string) {
	m.keepUsageSelection(func() { m.usageFilter = withUsageProject(m.usageFilter, project) })
}

// toggleSelectedUsageProject is the p key: the selected session's project.
func (m *model) toggleSelectedUsageProject() {
	page := m.usagePage()
	if len(page.Sessions) == 0 {
		m.toggleUsageProject("")
		return
	}
	m.toggleUsageProject(usageProjectName(page.Sessions[page.Cursor]))
}

// keepUsageSelection applies change to the list's order or filters and
// keeps the selected session selected when it is still listed. Every list
// otherwise starts again at the top.
func (m *model) keepUsageSelection(change func()) {
	selected := ""
	if page := m.usagePage(); len(page.Sessions) > 0 {
		selected = page.Sessions[page.Cursor].ID
	}
	change()
	m.codexHistoryCursor, m.copilotHistoryCursor, m.kimiHistoryCursor, m.claudeHistoryCursor = 0, 0, 0, 0
	for index, session := range m.usagePage().Sessions {
		if session.ID == selected {
			*m.usageCursorRef() = index
			break
		}
	}
}

// usageProjectTotal is one row of the projects panel.
type usageProjectTotal struct {
	name     string
	tokens   int64
	sessions int
}

// usageProjectTotals rolls the history up by working directory name, largest
// token total first.
func usageProjectTotals(history []usageSession) []usageProjectTotal {
	index := map[string]int{}
	var totals []usageProjectTotal
	for _, session := range history {
		name := usageProjectName(session)
		at, ok := index[name]
		if !ok {
			at = len(totals)
			index[name] = at
			totals = append(totals, usageProjectTotal{name: name})
		}
		totals[at].tokens += session.Tokens
		totals[at].sessions++
	}
	sort.SliceStable(totals, func(i, j int) bool {
		if totals[i].tokens != totals[j].tokens {
			return totals[i].tokens > totals[j].tokens
		}
		return totals[i].name < totals[j].name
	})
	return totals
}

// usageProjectRows is how many projects the panel lists in rows lines; the
// last line says how many more there are when they do not all fit.
func usageProjectRows(totals, rows int) int {
	if totals > rows {
		return max(rows-1, 0)
	}
	return totals
}

// usageProjectsShown returns the indexes into totals that the panel lists in
// rows lines, largest first. The first shown project left out takes the last
// row, so a project the filter shows stays visible and clickable.
func usageProjectsShown(totals []usageProjectTotal, rows int, active []string) []int {
	shown := make([]int, usageProjectRows(len(totals), rows))
	for index := range shown {
		shown[index] = index
	}
	if len(shown) == 0 || len(active) == 0 {
		return shown
	}
	for _, index := range shown {
		if usageProjectIn(totals[index].name, active) {
			return shown
		}
	}
	for index := len(shown); index < len(totals); index++ {
		if usageProjectIn(totals[index].name, active) {
			shown[len(shown)-1] = index
			break
		}
	}
	return shown
}

// usageProjectLines draws the projects panel. When a project filter is
// active, every row gets a marker column and the shown projects are marked.
func usageProjectLines(history []usageSession, rows int, active []string) []string {
	totals := usageProjectTotals(history)
	if len(totals) == 0 {
		return []string{dashPaint("No project data.", usageMutedColor, false)}
	}
	countWidth := 0
	for _, entry := range totals {
		countWidth = max(countWidth, len(fmt.Sprintf("×%d", entry.sessions)))
	}
	shown := usageProjectsShown(totals, rows, active)
	lines := make([]string, 0, len(shown)+1)
	for _, index := range shown {
		entry := totals[index]
		name := dashPaint(sanitizeProcessCommand(entry.name), usageBrightColor, true)
		switch {
		case len(active) == 0:
		case usageProjectIn(entry.name, active):
			name = dashPaint("▸ ", dashOrange, true) + dashPaint(sanitizeProcessCommand(entry.name), dashOrange, true)
		default:
			name = dashPaint("  ", usageMutedColor, false) + name
		}
		lines = append(lines,
			usageSplit(
				name,
				dashPaint(dashCell(formatTokenCount(entry.tokens), 7, true), dashYellow, true)+
					dashPaint("  "+dashCell(fmt.Sprintf("×%d", entry.sessions), countWidth, true), dashOrange, false),
			),
		)
	}
	if len(shown) < len(totals) {
		lines = append(lines, dashPaint(fmt.Sprintf("+%d more", len(totals)-len(shown)), usageMutedColor, false))
	}
	return lines
}

// usageRightPanels splits the right column of a sessions row of height
// rows: the session detail panel's height, then the projects panel's top
// and height (zero when it is not drawn).
func usageRightPanels(height int) (detail, projectsTop, projectsHeight int) {
	if height < usageDetailMinRows {
		return 0, 0, height
	}
	detail = min(usageDetailRows, height)
	if height-detail < 4 {
		detail = height // too little left for a projects panel
	}
	if rest := height - detail; rest >= 4 {
		return detail, detail, rest
	}
	return detail, 0, 0
}

// usageProjectAt maps a cell of the Usage page to the project row of the
// projects panel drawn there, as an index into usageProjectTotals.
func (m model) usageProjectAt(x, pageRow, pageRows int) (int, bool) {
	page := m.usagePage()
	if (page.Loading && !page.HasData) || (page.Err != "" && !page.HasData) || m.width < 80 {
		return 0, false
	}
	geometry := usageGeometryFor(page, pageRows)
	if geometry.sessions == 0 {
		return 0, false
	}
	leftWidth := m.width - usageRightWidth(m.width)
	if x <= leftWidth || x >= m.width-1 {
		return 0, false
	}
	_, top, height := usageRightPanels(geometry.sessions)
	if height == 0 {
		return 0, false
	}
	shown := usageProjectsShown(usageProjectTotals(page.ProjectSessions), height-2, page.Query.Projects)
	offset := pageRow - (geometry.usageSessionsTop() + top + 1)
	if offset < 0 || offset >= len(shown) {
		return 0, false
	}
	return shown[offset], true
}
