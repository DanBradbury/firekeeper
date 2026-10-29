package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/version"
	"github.com/charmbracelet/x/ansi"
)

// dashboard.go renders the Processes tab as a panel dashboard: summary panels
// across the top, a sessions table, and a detail panel for the selected
// runtime. Panels drop out as the terminal shrinks; the sessions table stays.

const (
	dashBorderColor = "69;74;98"
	dashBlue        = "137;180;250"
	dashOrange      = "250;179;135"
	dashGreen       = "166;227;161"
	dashRed         = "243;139;168"
	dashPurple      = "203;166;247"
	dashYellow      = "249;226;175"
	dashTeal        = "148;226;213"

	// dashSummaryMinRows is the content height needed before the summary
	// panels appear; dashDetailMinRows is what must remain below them before
	// the detail panel appears.
	// dashSummaryRows fits the token breakdown plus the one-line subagent
	// undercount warning. dashDetailMaxRows fits that warning under the
	// session metadata without clipping the first process line.
	dashSummaryRows    = 8
	dashSummaryMinRows = 18
	dashDetailMinRows  = 16
	dashDetailMaxRows  = 11
)

// dashLayout holds the row budget for each panel of the Processes tab.
type dashLayout struct {
	summary, sessions, detail int
	boxed                     bool
}

func (m model) dashboardLayout() dashLayout {
	contentRows := max(m.height-chromeRows, 0)
	layout := dashLayout{}
	if contentRows >= dashSummaryMinRows && m.width >= 40 {
		layout.summary = dashSummaryRows
	}
	rest := contentRows - layout.summary
	if rest >= dashDetailMinRows && m.width >= 40 {
		layout.detail = min(dashDetailMaxRows, rest-8)
	}
	layout.sessions = contentRows - layout.summary - layout.detail
	layout.boxed = layout.sessions >= 4 && m.width >= 24
	return layout
}

// processPageSize is the number of table body rows visible in the sessions
// panel, excluding its border and column header.
func (m model) processPageSize() int {
	layout := m.dashboardLayout()
	if layout.boxed {
		return max(layout.sessions-3, 0)
	}
	return max(layout.sessions-1, 0)
}

// dashInnerWidth is the usable width inside a full-width panel.
func (m model) dashInnerWidth() int {
	if m.dashboardLayout().boxed {
		return max(m.width-4, 1)
	}
	return max(m.width, 1)
}

func (m model) viewProcesses(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	layout := m.dashboardLayout()
	var lines []string
	if layout.summary > 0 {
		lines = append(lines, m.dashSummaryPanels(layout.summary)...)
	}
	lines = append(lines, m.dashSessionsPanel(layout)...)
	if layout.detail > 0 {
		lines = append(lines, dashBox("session", m.dashSelectedDetail(m.width-4), m.width, layout.detail, dashBlue)...)
	}
	for len(lines) < contentRows {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	return strings.Join(lines[:contentRows], "\n")
}

func (m model) dashSessionsPanel(layout dashLayout) []string {
	inner := m.dashInnerWidth()
	columns := dashFitColumns(inner)
	body := []string{dashHeaderRow(columns, inner)}
	if len(m.processGroups) == 0 {
		message := "No running Codex, Copilot, or OpenCode processes found."
		if m.refreshedAt.IsZero() {
			message = "Scanning for Codex, Copilot, and OpenCode processes…"
		}
		body = append(body, dashPaint(message, usageMutedColor, false))
	} else {
		lines, _ := m.processBodyLines()
		end := min(m.processScroll+m.processPageSize(), len(lines))
		body = append(body, lines[min(m.processScroll, end):end]...)
	}
	title := fmt.Sprintf("sessions (%d)", len(m.processGroups))
	if !layout.boxed {
		for index := range body {
			body[index] = dashFit(body[index], m.width, usagePanelColor)
		}
		return body
	}
	return dashBox(title, body, m.width, layout.sessions, dashOrange)
}

// dashColumn is one column of the sessions table. Columns with a higher drop
// rank are hidden first when the terminal is narrow.
type dashColumn struct {
	key   string
	title string
	width int
	drop  int
	right bool
}

var dashColumns = []dashColumn{
	{key: "ai", title: "AI", width: 8, drop: 1},
	{key: "pid", title: "PID", width: 7, drop: 5},
	{key: "project", title: "Project", width: 14, drop: 3},
	{key: "session", title: "Session", width: 8, drop: 6},
	{key: "summary", title: "Summary", drop: 0},
	{key: "status", title: "Status", width: 13, drop: 2},
	{key: "model", title: "Model", width: 16, drop: 4},
	{key: "tokens", title: "Tokens", width: 7, drop: 3, right: true},
	{key: "up", title: "Up", width: 11, drop: 7},
}

const (
	dashMarkerWidth     = 2
	dashSummaryMinWidth = 16
)

// dashFitColumns drops the lowest-priority columns until the table fits
// width, then gives the remaining space to the Summary column.
func dashFitColumns(width int) []dashColumn {
	columns := append([]dashColumn(nil), dashColumns...)
	for {
		fixed := dashMarkerWidth
		for _, column := range columns {
			fixed += column.width + 1
		}
		if fixed+dashSummaryMinWidth <= width || len(columns) <= 1 {
			for index := range columns {
				if columns[index].key == "summary" {
					columns[index].width = max(width-fixed, 1)
				}
			}
			return columns
		}
		worst := -1
		for index, column := range columns {
			if column.drop > 0 && (worst < 0 || column.drop > columns[worst].drop) {
				worst = index
			}
		}
		if worst < 0 {
			columns[0].width = max(width-dashMarkerWidth-1, 1)
			return columns[:1]
		}
		columns = append(columns[:worst], columns[worst+1:]...)
	}
}

func dashHeaderRow(columns []dashColumn, width int) string {
	var row strings.Builder
	row.WriteString(dashPaint(strings.Repeat(" ", dashMarkerWidth), usageTextColor, false))
	for _, column := range columns {
		row.WriteString(dashPaint(dashCell(column.title, column.width, column.right)+" ", usageBrightColor, true))
	}
	return dashFit(row.String(), width, usagePanelColor)
}

// dashCell truncates or pads value to exactly width cells.
func dashCell(value string, width int, right bool) string {
	value = ansi.Truncate(value, width, "…")
	space := strings.Repeat(" ", max(width-ansi.StringWidth(value), 0))
	if right {
		return space + value
	}
	return value + space
}

// processBodyLines returns the sessions table body (two rows per runtime,
// plus details when expanded) and the first line index of each runtime.
func (m model) processBodyLines() ([]string, []int) {
	width := m.dashInnerWidth()
	columns := dashFitColumns(width)
	summaryOffset := dashMarkerWidth
	for _, column := range columns {
		if column.key == "summary" {
			break
		}
		summaryOffset += column.width + 1
	}
	if summaryOffset >= width-8 {
		summaryOffset = dashMarkerWidth
	}

	var lines []string
	offsets := make([]int, len(m.processGroups))
	for index, group := range m.processGroups {
		offsets[index] = len(lines)
		selected := index == m.processCursor
		background := usagePanelColor
		if selected {
			background = usageHighlightColor
		}
		lines = append(lines, dashGroupRow(group, columns, width, selected, m.expandedGroups[group.root.pid], background))
		lines = append(lines, dashFit(
			dashPaint(strings.Repeat(" ", summaryOffset), usageMutedColor, false)+dashGroupSubline(group),
			width,
			usagePanelColor,
		))
		if !m.expandedGroups[group.root.pid] {
			continue
		}
		for _, line := range dashGroupDetail(group) {
			lines = append(lines, dashFit(dashPaint("    ", usageMutedColor, false)+line, width, usagePanelColor))
		}
	}
	return lines, offsets
}

func dashGroupRow(group processGroup, columns []dashColumn, width int, selected, expanded bool, background string) string {
	session, hasSession := firstSession(group)
	marker := "  "
	if selected {
		marker = "▶ "
	}
	if expanded {
		marker = string([]rune(marker)[0]) + "▾"
	}

	var row strings.Builder
	row.WriteString(usagePaint(marker, dashOrange, true, background))
	for _, column := range columns {
		value, color, bold := "", usageTextColor, false
		switch column.key {
		case "ai":
			value, color, bold = group.tool, dashToolColor(group.tool), true
		case "pid":
			value, color = fmt.Sprint(group.root.pid), usageMutedColor
		case "project":
			value, color, bold = dashProjectName(session), usageBrightColor, true
			if !hasSession {
				value, color, bold = "—", usageMutedColor, false
			}
		case "session":
			value, color = dashShortID(session.id), dashOrange
		case "summary":
			value = session.name
			if !hasSession {
				value, color = "session metadata unavailable", usageMutedColor
			} else if value == "" {
				value, color = filepath.Base(session.cwd), usageMutedColor
			}
			bold = selected
		case "status":
			state := sessionStateUnknown
			if hasSession {
				state = session.state
			}
			value, color = dashStateIcon(state)+" "+state.String(), dashStateColor(state)
		case "model":
			value, color = emptyFallback(session.model, "—"), usageMutedColor
		case "tokens":
			value, color = "—", usageMutedColor
			if total := sessionTokenTotal(session); total > 0 {
				value, color = formatTokenCount(total), usageBrightColor
			}
		case "up":
			value, color = group.root.elapsed, usageMutedColor
		}
		row.WriteString(usagePaint(dashCell(sanitizeProcessCommand(value), column.width, column.right)+" ", color, bold, background))
	}
	return dashFit(row.String(), width, background)
}

func dashGroupSubline(group processGroup) string {
	session, ok := firstSession(group)
	if !ok {
		procs := "procs"
		if len(group.processes) == 1 {
			procs = "proc"
		}
		return dashPaint(fmt.Sprintf("└─ %s · %d %s", displayTTY(group.root.tty), len(group.processes), procs), usageMutedColor, false)
	}
	parts := []string{}
	if session.gitBranch != "" {
		parts = append(parts, session.gitBranch)
	}
	if session.cwd != "" {
		parts = append(parts, dashShortPath(session.cwd))
	}
	if extra := len(group.sessions) - 1; extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d sessions", extra))
	}
	if len(parts) == 0 {
		parts = append(parts, displayTTY(group.root.tty))
	}
	return dashPaint("└─ ", usageMutedColor, false) + dashPaint(strings.Join(parts, " · "), usageMutedColor, false)
}

// dashGroupDetail describes every session and process of one runtime. It backs
// both the detail panel and inline expansion with Enter.
func dashGroupDetail(group processGroup) []string {
	var lines []string
	label := func(name, value string) string {
		return dashPaint(name+" ", usageMutedColor, false) + dashPaint(sanitizeProcessCommand(value)+"   ", usageTextColor, false)
	}
	if len(group.sessions) == 0 {
		message := "no session metadata adapter"
		switch group.tool {
		case "Codex":
			message = "no Codex rollout metadata found"
		case "Copilot":
			message = "no Copilot session metadata found"
		case "Kimi":
			message = "no Kimi session metadata found"
		case "Gemini":
			message = "no Gemini CLI session log found"
		case "Cursor":
			message = "no Cursor CLI transcript found"
		}
		lines = append(lines,
			dashPaint("RUNTIME ", usageBrightColor, true)+dashPaint(group.tool, dashToolColor(group.tool), true)+
				dashPaint(" · "+message, usageMutedColor, false),
		)
	}
	for _, session := range group.sessions {
		originLabel, origin := "source", session.source
		if session.repository != "" {
			originLabel, origin = "repo", session.repository
		}
		lines = append(lines,
			dashPaint("SESSION ", usageBrightColor, true)+dashPaint(emptyFallback(session.id, "unknown"), dashOrange, true)+
				dashPaint(" · "+sanitizeProcessCommand(emptyFallback(session.cwd, "unknown cwd")), usageTextColor, false),
			dashPaint("title ", usageMutedColor, false)+dashPaint(sanitizeProcessCommand(emptyFallback(session.name, "—")), usageBrightColor, true),
			dashPaint("state ", usageMutedColor, false)+
				dashPaint(dashStateIcon(session.state)+" "+session.state.String()+"   ", dashStateColor(session.state), true)+
				label("model", emptyFallback(session.model, "unknown")),
			label(originLabel, emptyFallback(origin, "unknown"))+
				label("branch", emptyFallback(session.gitBranch, "none")),
			dashPaint("tokens ", usageMutedColor, false)+
				dashPaint(formatTokenCount(sessionTokenTotal(session))+" ", usageTextColor, false)+
				dashTokenBreakdownText(session.tokens),
		)
		if session.undercountWarning != "" {
			lines = append(lines, dashPaint(session.undercountWarning, dashYellow, false))
		}
		lines = append(lines,
			label("updated", formatSessionTime(session.updatedAt))+
				label("tty", displayTTY(group.root.tty))+
				label("up", emptyFallback(group.root.elapsed, "—")),
		)
	}
	lines = append(lines, dashPaint(fmt.Sprintf("PROCESSES (%d)", len(group.processes)), usageBrightColor, true))
	for index, process := range group.processes {
		branch := "├─"
		if index == len(group.processes)-1 {
			branch = "└─"
		}
		lines = append(lines,
			dashPaint(" "+branch+" ", usageMutedColor, false)+
				dashPaint(fmt.Sprintf("PID %-7d", process.pid), usageTextColor, false)+
				dashPaint(fmt.Sprintf(" PPID %-7d TTY %-9s %-11s ", process.ppid, displayTTY(process.tty), process.elapsed), usageMutedColor, false)+
				dashPaint(process.command, usageTextColor, false),
		)
	}
	return lines
}

func (m model) dashSelectedDetail(width int) []string {
	if len(m.processGroups) == 0 || m.processCursor >= len(m.processGroups) {
		return []string{dashPaint("Select a runtime to see its session details.", usageMutedColor, false)}
	}
	return dashGroupDetail(m.processGroups[m.processCursor])
}

// dashSummary aggregates the visible runtimes for the summary panels.
type dashSummary struct {
	sessions, processes int
	states              map[sessionState]int
	tokens              int64
	tools               []dashToolSummary
	projects            []dashProjectSummary
}

type dashToolSummary struct {
	tool     string
	runtimes int
	tokens   int64
}

type dashProjectSummary struct {
	name     string
	branches []string
	sessions int
}

func (m model) dashSummarize() dashSummary {
	summary := dashSummary{states: make(map[sessionState]int)}
	tools := map[string]*dashToolSummary{}
	projects := map[string]*dashProjectSummary{}
	var projectOrder []string
	for _, group := range m.processGroups {
		summary.processes += len(group.processes)
		tool := tools[group.tool]
		if tool == nil {
			tool = &dashToolSummary{tool: group.tool}
			tools[group.tool] = tool
		}
		tool.runtimes++
		if len(group.sessions) == 0 {
			summary.states[sessionStateUnknown]++
		}
		for _, session := range group.sessions {
			summary.sessions++
			summary.states[session.state]++
			summary.tokens += sessionTokenTotal(session)
			tool.tokens += sessionTokenTotal(session)
			name := dashProjectName(session)
			project := projects[name]
			if project == nil {
				project = &dashProjectSummary{name: name}
				projects[name] = project
				projectOrder = append(projectOrder, name)
			}
			project.sessions++
			if session.gitBranch != "" && !containsString(project.branches, session.gitBranch) {
				project.branches = append(project.branches, session.gitBranch)
			}
		}
	}
	for _, tool := range tools {
		summary.tools = append(summary.tools, *tool)
	}
	sort.Slice(summary.tools, func(i, j int) bool {
		if summary.tools[i].runtimes != summary.tools[j].runtimes {
			return summary.tools[i].runtimes > summary.tools[j].runtimes
		}
		return summary.tools[i].tool < summary.tools[j].tool
	})
	for _, name := range projectOrder {
		summary.projects = append(summary.projects, *projects[name])
	}
	return summary
}

func (m model) dashSummaryPanels(height int) []string {
	summary := m.dashSummarize()
	panels := []struct {
		title  string
		accent string
		body   func(width int) []string
	}{
		{"¹overview", dashBlue, summary.overviewLines},
		{m.dashTokensTitle(), dashYellow, m.dashTokenLines},
		{"³providers", dashPurple, summary.providerLines},
		{"⁴projects", dashGreen, summary.projectLines},
	}
	count := 1
	switch {
	case m.width >= 128:
		count = 4
	case m.width >= 96:
		count = 3
	case m.width >= 64:
		count = 2
	}
	panels = panels[:count]

	rows := make([]string, height)
	remaining := m.width
	for index, panel := range panels {
		width := remaining / (count - index)
		remaining -= width
		box := dashBox(panel.title, panel.body(width-4), width, height, panel.accent)
		for row := range rows {
			rows[row] += box[row]
		}
	}
	return rows
}

func (s dashSummary) overviewLines(width int) []string {
	stat := func(name string, value int) string {
		return dashPaint(name+" ", usageMutedColor, false) + dashPaint(fmt.Sprintf("%-4d", value), usageBrightColor, true)
	}
	state := func(value sessionState, label string) string {
		return dashPaint(dashStateIcon(value)+" ", dashStateColor(value), true) +
			dashPaint(fmt.Sprintf("%d %-9s", s.states[value], label), usageTextColor, false)
	}
	return []string{
		stat("Sessions", s.sessions) + stat("Procs", s.processes),
		state(sessionStateActive, "active") + state(sessionStateWaiting, "waiting"),
		state(sessionStateNeedsInput, "input") + state(sessionStateUnknown, "unknown"),
		dashPaint("Tokens ", usageMutedColor, false) + dashPaint(formatTokenCount(s.tokens), dashYellow, true) +
			dashPaint(" live", usageMutedColor, false),
	}
}

func (s dashSummary) providerLines(width int) []string {
	if len(s.tools) == 0 {
		return []string{dashPaint("No agent runtimes running.", usageMutedColor, false)}
	}
	var maximum int64
	for _, tool := range s.tools {
		maximum = max(maximum, tool.tokens)
	}
	barWidth := max(width-8-4-8, 1)
	var lines []string
	for _, tool := range s.tools {
		filled := 0
		if maximum > 0 {
			filled = int(float64(barWidth)*float64(tool.tokens)/float64(maximum) + 0.5)
			if tool.tokens > 0 {
				filled = max(filled, 1)
			}
		}
		lines = append(lines,
			dashPaint(dashCell(tool.tool, 8, false), dashToolColor(tool.tool), true)+
				dashPaint(fmt.Sprintf("%3d ", tool.runtimes), usageBrightColor, false)+
				dashPaint(strings.Repeat("━", filled), dashToolColor(tool.tool), true)+
				dashPaint(strings.Repeat("━", barWidth-filled), usageTrackColor, false)+
				dashPaint(fmt.Sprintf(" %7s", formatTokenCount(tool.tokens)), usageTextColor, false),
		)
	}
	return lines
}

func (s dashSummary) projectLines(width int) []string {
	if len(s.projects) == 0 {
		return []string{dashPaint("No session metadata yet.", usageMutedColor, false)}
	}
	var lines []string
	for index, project := range s.projects {
		if index == dashSummaryRows-3 && len(s.projects) > dashSummaryRows-2 {
			lines = append(lines, dashPaint(fmt.Sprintf("+%d more", len(s.projects)-index), usageMutedColor, false))
			break
		}
		line := dashPaint(sanitizeProcessCommand(project.name), usageBrightColor, true)
		if len(project.branches) > 0 {
			line += dashPaint("  "+sanitizeProcessCommand(strings.Join(project.branches, ", ")), dashBlue, false)
		}
		if project.sessions > 1 {
			line += dashPaint(fmt.Sprintf("  ×%d", project.sessions), dashOrange, false)
		}
		lines = append(lines, line)
	}
	return lines
}

// dashHeaderLine is the top row shared by both tabs: product name, tabs, and
// on the right the last refresh time with live state counts.
func (m model) dashHeaderLine() string {
	left := dashPaint(" firekeeper ", usageBrightColor, true) +
		dashPaint(version.Version+" · agent monitor  ", usageMutedColor, false)
	for _, item := range []struct {
		tab   tab
		label string
	}{{processesTab, "Processes"}, {usageTab, "Usage"}} {
		if item.tab == m.activeTab {
			left += usagePaint("[ "+item.label+" ]", dashOrange, true, usageHighlightColor) + dashPaint(" ", usageTextColor, false)
		} else {
			left += dashPaint("  "+item.label+"   ", usageMutedColor, false)
		}
	}
	left += dashPaint(" tab switch  ? help", usageMutedColor, false)

	right := ""
	if !m.refreshedAt.IsZero() {
		summary := m.dashSummarize()
		right = dashPaint(m.refreshedAt.Format("15:04:05")+"  ", usageMutedColor, false)
		for _, state := range []sessionState{sessionStateActive, sessionStateWaiting, sessionStateNeedsInput} {
			if count := summary.states[state]; count > 0 {
				right += dashPaint(fmt.Sprintf("%d%s ", count, dashStateIcon(state)), dashStateColor(state), true)
			}
		}
	}
	space := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if space < 1 {
		return dashFit(left, m.width, usagePanelColor)
	}
	return left + dashPaint(strings.Repeat(" ", space), usageTextColor, false) + right
}

// footerKey is one key and its action in a footer help line.
type footerKey struct{ key, action string }

// renderFooterKeys paints a footer help line shared by the Processes and Usage
// tabs, filled to width with the panel background.
func renderFooterKeys(keys []footerKey, width int) string {
	help := dashPaint(" ", usageTextColor, false)
	for _, key := range keys {
		help += dashPaint(key.key+" ", dashBlue, true) + dashPaint(key.action+"  ", usageTextColor, false)
	}
	return dashFit(help, width, usagePanelColor)
}

func (m model) processFooter() (string, string) {
	help := renderFooterKeys([]footerKey{
		{"↑↓", "select"}, {"enter", "expand"}, {"s", "switch terminal"}, {"c", "chat"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"},
	}, m.width)
	status := dashFit(dashPaint("  "+m.processStatusText(), usageMutedColor, false), m.width, usagePanelColor)
	return help, status
}

func (m model) processStatusText() string {
	if m.processErr != "" {
		return "process scan failed: " + m.processErr
	}
	if m.terminalStatus != "" {
		return m.terminalStatus
	}
	if m.refreshedAt.IsZero() {
		return "scanning processes…"
	}
	processCount := 0
	for _, group := range m.processGroups {
		processCount += len(group.processes)
	}
	if m.processMetadataWarning != "" {
		return fmt.Sprintf(
			"%d runtimes / %d processes  •  session metadata unavailable: %s",
			len(m.processGroups), processCount, m.processMetadataWarning,
		)
	}
	return fmt.Sprintf(
		"%d runtimes / %d processes  •  selected %d/%d  •  refreshed %s  •  2s auto",
		len(m.processGroups),
		processCount,
		min(m.processCursor+1, len(m.processGroups)),
		len(m.processGroups),
		m.refreshedAt.Format("15:04:05"),
	)
}

// dashBox draws a rounded panel of exactly width columns and height rows with
// title set into the top border. Body lines are truncated and padded.
func dashBox(title string, body []string, width, height int, accent string) []string {
	if height < 1 || width < 4 {
		return make([]string, max(height, 0))
	}
	inner := width - 4
	titleText := ansi.Truncate(title, max(width-5, 0), "…")
	top := dashPaint("╭─", dashBorderColor, false) + dashPaint(titleText, accent, true) +
		dashPaint(strings.Repeat("─", max(width-3-ansi.StringWidth(titleText), 0))+"╮", dashBorderColor, false)
	lines := []string{top}
	for row := 0; row < height-2; row++ {
		line := ""
		if row < len(body) {
			line = body[row]
		}
		lines = append(lines,
			dashPaint("│ ", dashBorderColor, false)+dashFit(line, inner, usagePanelColor)+dashPaint(" │", dashBorderColor, false),
		)
	}
	if height >= 2 {
		lines = append(lines, dashPaint("╰"+strings.Repeat("─", width-2)+"╯", dashBorderColor, false))
	}
	return lines
}

// dashFit truncates a painted string to width cells and pads it with painted
// spaces so every row fills the panel background.
func dashFit(value string, width int, background string) string {
	if width < 1 {
		return ""
	}
	value = ansi.Truncate(value, width, "")
	if pad := width - ansi.StringWidth(value); pad > 0 {
		value += usagePaint(strings.Repeat(" ", pad), usageTextColor, false, background)
	}
	return value
}

func dashPaint(value, foreground string, bold bool) string {
	return usagePaint(value, foreground, bold, usagePanelColor)
}

func firstSession(group processGroup) (sessionInfo, bool) {
	if len(group.sessions) == 0 {
		return sessionInfo{}, false
	}
	return group.sessions[0], true
}

func dashProjectName(session sessionInfo) string {
	if session.repository != "" {
		return filepath.Base(session.repository)
	}
	if session.cwd != "" {
		return filepath.Base(session.cwd)
	}
	return "—"
}

func dashShortID(id string) string {
	if id == "" {
		return "—"
	}
	return string([]rune(id)[:min(8, len([]rune(id)))])
}

// dashShortPath replaces the home directory prefix with ~.
func dashShortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home || strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func dashStateIcon(state sessionState) string {
	switch state {
	case sessionStateActive:
		return "●"
	case sessionStateWaiting:
		return "◌"
	case sessionStateNeedsInput:
		return "▲"
	case session.SessionStateEnded:
		return "✕"
	default:
		return "○"
	}
}

func dashStateColor(state sessionState) string {
	switch state {
	case sessionStateActive:
		return dashGreen
	case sessionStateWaiting:
		return dashYellow
	case sessionStateNeedsInput:
		return dashRed
	default:
		return usageMutedColor
	}
}

func dashToolColor(tool string) string {
	switch tool {
	case "Codex":
		return dashTeal
	case "Copilot":
		return dashPurple
	case "Claude":
		return dashOrange
	case "OpenCode":
		return dashBlue
	case "Kimi":
		return dashYellow
	case "Gemini":
		return dashGreen
	case "Cursor":
		// Cursor's mark is monochrome; red stays the NEEDS INPUT color.
		return usageBrightColor
	default:
		return usageTextColor
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// sessionTokenTotal prefers the provider's token breakdown and falls back to
// the aggregate the provider reports when no breakdown is available. The
// Claude agent_summary estimate is not included.
func sessionTokenTotal(session sessionInfo) int64 {
	if total := session.tokens.Total(); total > 0 {
		return total
	}
	return session.tokensUsed
}

func dashTokenBreakdownText(tokens tokenBreakdown) string {
	if tokens.Total() == 0 {
		return ""
	}
	return dashPaint("(", usageMutedColor, false) +
		dashPaint("in ", usageMutedColor, false) + dashPaint(formatTokenCount(tokens.Input), dashBlue, false) +
		dashPaint(" · out ", usageMutedColor, false) + dashPaint(formatTokenCount(tokens.Output), dashOrange, false) +
		dashPaint(" · cache read ", usageMutedColor, false) + dashPaint(formatTokenCount(tokens.CacheRead), dashPurple, false) +
		dashPaint(" · cache write ", usageMutedColor, false) + dashPaint(formatTokenCount(tokens.CacheWrite), dashTeal, false) +
		dashPaint(")   ", usageMutedColor, false)
}

func (m model) selectedSession() (processGroup, sessionInfo, bool) {
	if len(m.processGroups) == 0 || m.processCursor >= len(m.processGroups) {
		return processGroup{}, sessionInfo{}, false
	}
	group := m.processGroups[m.processCursor]
	session, ok := firstSession(group)
	return group, session, ok
}

func (m model) dashTokensTitle() string {
	_, session, ok := m.selectedSession()
	if !ok {
		return "²tokens"
	}
	return "²tokens (" + sanitizeProcessCommand(dashProjectName(session)) + "/" + dashShortID(session.id) + ")"
}

// dashTokenLines breaks down the selected session's tokens into input,
// output, cache read, and cache write bars, each scaled to the session total.
func (m model) dashTokenLines(width int) []string {
	_, session, ok := m.selectedSession()
	if !ok {
		return []string{dashPaint("Select a session to see its tokens.", usageMutedColor, false)}
	}
	total := sessionTokenTotal(session)
	lines := []string{
		dashPaint("Total  ", usageMutedColor, false) + dashPaint(formatTokenCount(total), usageBrightColor, true),
	}
	tokens := session.tokens
	if tokens.Total() == 0 {
		lines = append(lines, dashPaint("No input/output/cache breakdown", usageMutedColor, false),
			dashPaint("for this session.", usageMutedColor, false))
	} else {
		const labelWidth = 12
		barWidth := max(width-labelWidth-8, 1)
		for _, row := range []struct {
			label string
			value int64
			color string
		}{
			{"Input", tokens.Input, dashBlue},
			{"Output", tokens.Output, dashOrange},
			{"Cache read", tokens.CacheRead, dashPurple},
			{"Cache write", tokens.CacheWrite, dashTeal},
		} {
			filled := int(float64(barWidth)*float64(row.value)/float64(tokens.Total()) + 0.5)
			if row.value > 0 {
				filled = max(filled, 1)
			}
			filled = min(filled, barWidth)
			lines = append(lines,
				dashPaint(dashCell(row.label, labelWidth, false), usageMutedColor, false)+
					dashPaint(strings.Repeat("━", filled), row.color, true)+
					dashPaint(strings.Repeat("━", barWidth-filled), usageTrackColor, false)+
					dashPaint(fmt.Sprintf(" %7s", formatTokenCount(row.value)), row.color, true),
			)
		}
	}
	if session.undercountWarning != "" {
		lines = append(lines, dashPaint(session.undercountWarning, dashYellow, false))
	}
	return lines
}
