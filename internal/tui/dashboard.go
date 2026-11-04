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
	// dashSummaryRows fits the token breakdown plus one extra line: the
	// subagent undercount warning, or the telemetry background-call line.
	// dashDetailMaxRows fits that line and one more (the enable hint, or
	// the transcripts figure) under the session metadata without clipping
	// the first process line.
	dashSummaryRows    = 8
	dashSummaryMinRows = 18
	dashDetailMinRows  = 16
	dashDetailMaxRows  = 12
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
		// Grow past the usual height when the selected session needs it,
		// such as for wrapped background calls, while leaving the sessions
		// panel its 8 rows.
		need := len(m.dashSelectedDetail(m.width-4)) + 2
		layout.detail = min(max(dashDetailMaxRows, need), rest-8)
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
	if len(m.processGroups) == 0 {
		return m.viewIdleProcesses(contentRows)
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

// idleBody is the centered message block of the idle Processes view, without
// the recent-sessions list under it.
func (m model) idleBody() []string {
	message := "No agent sessions running"
	if m.refreshedAt.IsZero() {
		message = "Scanning for agent sessions…"
	}
	color := usageBrightColor
	if m.processErr != "" {
		message = "process scan failed: " + sanitizeProcessCommand(m.processErr)
		color = dashRed
	}
	body := []string{
		dashPaint(message, color, false),
		dashPaint("Start codex, copilot or opencode in a terminal.", usageMutedColor, false),
		dashPaint("Supported agents appear here automatically.", usageMutedColor, false),
		dashPaint("Press Tab for Overview / Usage history.", usageMutedColor, false),
	}
	if m.processMetadataWarning != "" {
		body = append(body, dashPaint("session metadata unavailable: "+sanitizeProcessCommand(m.processMetadataWarning), dashYellow, false))
	}
	return body
}

// idleGeometry places the idle view's blocks in a content area of rows rows:
// the first row of the message block and the number of recent-list rows
// (heading included) under it.
func (m model) idleGeometry(rows int) (start, recentRows int) {
	body := m.idleBody()
	recentRows = len(m.recentLines(min(rows-1-len(body)-2, recentSessionLimit+1)))
	if recentRows > 0 {
		recentRows++ // blank separator
	}
	return max(1, (rows-len(body)-recentRows)/2), recentRows
}

// viewIdleProcesses keeps empty discovery useful without empty panel frames.
func (m model) viewIdleProcesses(rows int) string {
	status := "Idle · Scanning every 2s"
	if m.refreshedAt.IsZero() {
		status = "Scanning every 2s · first scan pending"
	} else {
		status += " · last scan " + m.refreshedAt.Format("15:04:05")
	}
	if m.processErr != "" {
		status = strings.Replace(status, "Idle · ", "", 1)
	}
	body := m.idleBody()
	lines := make([]string, rows)
	lines[0] = dashFit(dashPaint(status, usageMutedColor, false), m.width, usagePanelColor)
	// Recent sessions take the space under the hint, which moves up to make room.
	recent := m.recentLines(min(rows-1-len(body)-2, recentSessionLimit+1))
	start, _ := m.idleGeometry(rows)
	if len(recent) > 0 {
		body = append(body, "")
		body = append(body, recent...)
	}
	for i := 1; i < rows; i++ {
		line := ""
		if index := i - start; index >= 0 && index < len(body) {
			line = body[index]
			if index < len(body)-len(recent) || len(recent) == 0 {
				line = dashPaint(strings.Repeat(" ", max(0, (m.width-ansi.StringWidth(line))/2)), usageTextColor, false) + line
			}
		}
		lines[i] = dashFit(line, m.width, usagePanelColor)
	}
	return strings.Join(lines, "\n")
}

func (m model) dashSessionsPanel(layout dashLayout) []string {
	inner := m.dashInnerWidth()
	columns := m.dashFitColumns(inner)
	body := []string{dashHeaderRow(columns, inner)}
	// Reuse the header row for feedback without changing the table's scroll budget.
	switch {
	case m.processErr != "":
		body[0] = dashPaint("process scan failed: "+sanitizeProcessCommand(m.processErr), dashRed, false)
	case m.processMetadataWarning != "":
		body[0] = dashPaint("session metadata unavailable: "+sanitizeProcessCommand(m.processMetadataWarning), dashYellow, false)
	}
	if len(m.processGroups) == 0 && m.processErr == "" {
		message := "No agent sessions running"
		if m.refreshedAt.IsZero() {
			message = "Scanning for agent sessions…"
		}
		body = append(body, dashPaint(message, usageMutedColor, false))
	} else if len(m.processGroups) > 0 {
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
	{key: "project", title: "Project", width: 14, drop: 3},
	{key: "title", title: "Title", width: 16, drop: 8},
	{key: "session", title: "Session", width: 8, drop: 6},
	{key: "directory", title: "Directory", drop: 0},
	{key: "status", title: "Status", width: 13, drop: 2},
	{key: "model", title: "Model", width: 16, drop: 7},
	{key: "tokens", title: "Tokens", width: 7, drop: 3, right: true},
	{key: "up", title: "Up", width: 11, drop: 9},
}

const (
	dashMarkerWidth       = 2
	dashDirectoryMinWidth = 28
	// dashGrowMaxWidth caps how far the Project and Model columns grow.
	dashGrowMaxWidth = 32
)

// dashFitColumns drops the lowest-priority columns until the table fits
// width, then gives the remaining space to the Directory column.
func (m model) dashFitColumns(width int) []dashColumn {
	columns := append([]dashColumn(nil), dashColumns...)
	for index := range columns {
		if columns[index].key == "ai" {
			columns[index].width += toolIconWidth()
		}
		if columns[index].key == "title" {
			for _, group := range m.processGroups {
				for _, session := range group.sessions {
					if session.subagent {
						columns[index].width = max(columns[index].width, ansi.StringWidth(subagentShare(session, groupTokenTotal(group))))
					}
				}
			}
		}
		if columns[index].key == "session" {
			for _, group := range m.processGroups {
				if len(group.sessions) > 1 {
					label := sanitizeProcessCommand(dashSessionLabel(group))
					columns[index].width = max(columns[index].width, ansi.StringWidth(label))
				}
			}
			break
		}
	}
	for {
		fixed := dashMarkerWidth
		for _, column := range columns {
			fixed += column.width + 1
		}
		if fixed+dashDirectoryMinWidth <= width || len(columns) <= 1 {
			// Long model and project names take spare width before Directory does.
			slack := width - fixed - dashDirectoryMinWidth
			for _, key := range []string{"model", "project", "title"} {
				for index := range columns {
					if columns[index].key != key || slack <= 0 {
						continue
					}
					grow := min(max(m.longestCell(key)-columns[index].width, 0), slack, dashGrowMaxWidth-columns[index].width)
					grow = max(grow, 0)
					columns[index].width += grow
					fixed += grow
					slack -= grow
				}
			}
			for index := range columns {
				if columns[index].key == "directory" {
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

// longestCell is the widest project or model label across all sessions.
func (m model) longestCell(key string) int {
	longest := 0
	for _, group := range m.processGroups {
		for _, session := range group.sessions {
			value := session.model
			switch key {
			case "project":
				value = dashProjectName(session)
			case "title":
				value = session.name
			}
			longest = max(longest, ansi.StringWidth(sanitizeProcessCommand(value)))
		}
	}
	return longest
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

// processBodyLines returns the sessions table body and each runtime's first
// line index. Inline details are used only when the separate detail panel is hidden.
func (m model) processBodyLines() ([]string, []int) {
	width := m.dashInnerWidth()
	columns := m.dashFitColumns(width)
	inlineDetails := m.dashboardLayout().detail == 0

	var lines []string
	offsets := make([]int, len(m.processGroups))
	for index, group := range m.processGroups {
		offsets[index] = len(lines)
		selected := index == m.processCursor
		expanded := inlineDetails && m.expandedGroups[group.root.pid]
		background := usagePanelColor
		if selected {
			background = usageHighlightColor
		}
		lines = append(lines, dashGroupRow(group, columns, width, selected, expanded, background))
		for _, sub := range group.sessions {
			if sub.subagent {
				lines = append(lines, dashSubagentRow(sub, groupTokenTotal(group), columns, width, selected))
			}
		}
		if !expanded {
			continue
		}
		for _, line := range dashGroupDetail(group, width-4) {
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
			value, color, bold = toolLabel(group.tool), dashToolColor(group.tool), true
		case "project":
			value, color, bold = dashProjectName(session), usageBrightColor, true
			if !hasSession {
				value, color, bold = "—", usageMutedColor, false
			}
		case "title":
			value, color = emptyFallback(session.name, "—"), usageTextColor
			if !hasSession {
				color = usageMutedColor
			}
		case "session":
			value, color = dashSessionLabel(group), dashOrange
		case "directory":
			value, color = "session metadata unavailable", usageMutedColor
			if hasSession {
				value = emptyFallback(dashShortPath(session.cwd), "—")
			}
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
			if total := groupTokenTotal(group); total > 0 {
				value, color = formatTokenCount(total), usageBrightColor
			}
		case "up":
			value, color = group.root.elapsed, usageMutedColor
		}
		row.WriteString(usagePaint(dashCell(sanitizeProcessCommand(value), column.width, column.right)+" ", color, bold, background))
	}
	return dashFit(row.String(), width, background)
}

// dashSubagentRow is the line under a runtime for a subagent it spawned. It
// sits in the same columns, indented with a tree branch, and belongs to the
// runtime above it: selecting either selects that runtime.
func dashSubagentRow(sub sessionInfo, groupTotal int64, columns []dashColumn, width int, selected bool) string {
	background := usagePanelColor
	if selected {
		background = usageHighlightColor
	}
	var row strings.Builder
	row.WriteString(usagePaint("  ", dashOrange, true, background))
	for _, column := range columns {
		value, color, bold := "", usageMutedColor, false
		switch column.key {
		case "ai":
			value = "└─ sub"
		case "project":
			value = subagentLabel(sub)
		case "session":
			// Spawned threads share the parent's time-ordered id prefix, so the
			// tail is what tells them apart.
			value, color = dashIDTail(sub.id), dashOrange
		case "title":
			value = subagentShare(sub, groupTotal)
		case "status":
			value, color = dashStateIcon(sub.state)+" "+sub.state.String(), dashStateColor(sub.state)
		case "model":
			value = emptyFallback(sub.model, "—")
		case "tokens":
			value = "—"
			if total := sessionTokenTotal(sub); total > 0 {
				value, color = formatTokenCount(total), usageTextColor
			}
		}
		row.WriteString(usagePaint(dashCell(sanitizeProcessCommand(value), column.width, column.right)+" ", color, bold, background))
	}
	return dashFit(row.String(), width, background)
}

// subagentShare is the indented note under a runtime's title saying how much
// of its tokens a subagent spent; the indent marks it as the subagent's.
func subagentShare(sub sessionInfo, groupTotal int64) string {
	total := sessionTokenTotal(sub)
	if total <= 0 || groupTotal <= 0 {
		return ""
	}
	return fmt.Sprintf("  (%d%% of total tokens)", (total*100+groupTotal/2)/groupTotal)
}

// dashSubagentRollup adds what the subagents spent to the primary session's
// own token line, so the row total can be read as own plus subagents.
func dashSubagentRollup(total int64, count int) string {
	if count == 0 {
		return ""
	}
	noun := "subagent"
	if count > 1 {
		noun += "s"
	}
	return dashPaint(fmt.Sprintf("  + %s in %d %s", formatTokenCount(total), count, noun), usageMutedColor, false)
}

// groupTokenTotal is everything a runtime has spent: its sessions and the
// subagents they spawned.
func groupTokenTotal(group processGroup) int64 {
	var total int64
	for _, session := range group.sessions {
		total += sessionTokenTotal(session)
	}
	return total
}

// subagentTokenTotal is the part of groupTokenTotal spent by subagents.
func subagentTokenTotal(group processGroup) (total int64, count int) {
	for _, session := range group.sessions {
		if session.subagent {
			total += sessionTokenTotal(session)
			count++
		}
	}
	return total, count
}

// subagentLabel names a subagent: its nickname, else its title when it has one.
func subagentLabel(sub sessionInfo) string {
	if sub.agentName != "" {
		return sub.agentName
	}
	if sub.name != "" && sub.name != sub.id {
		return sub.name
	}
	return "subagent"
}

func dashIDTail(id string) string {
	if id == "" {
		return "—"
	}
	runes := []rune(id)
	return string(runes[max(len(runes)-8, 0):])
}

// dashGroupDetail describes every session and process of one runtime. It backs
// both the detail panel and inline expansion when that panel is hidden. The
// background calls wrap onto as many lines of width as they need.
func dashGroupDetail(group processGroup, width int) []string {
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
		case "Claude":
			message = "no Claude Code transcript found"
		case "OpenCode":
			message = "no OpenCode session found"
		}
		lines = append(lines,
			dashPaint("RUNTIME ", usageBrightColor, true)+dashPaint(toolLabel(group.tool), dashToolColor(group.tool), true)+
				dashPaint(" · "+message, usageMutedColor, false),
		)
	}
	subTotal, subCount := subagentTokenTotal(group)
	for _, session := range group.sessions {
		if session.subagent {
			share := ""
			if total := groupTokenTotal(group); total > 0 && sessionTokenTotal(session) > 0 {
				share = fmt.Sprintf(" · %d%% of runtime", (sessionTokenTotal(session)*100+total/2)/total)
			}
			lines = append(lines,
				dashPaint("└─ SUBAGENT ", usageBrightColor, true)+dashPaint(subagentLabel(session)+" ", usageBrightColor, true)+
					dashPaint(emptyFallback(session.id, "unknown"), dashOrange, false)+
					dashPaint(" of "+emptyFallback(dashShortID(session.parentID), "this runtime"), usageMutedColor, false),
				dashPaint("   ", usageMutedColor, false)+
					dashPaint(dashStateIcon(session.state)+" "+session.state.String()+"   ", dashStateColor(session.state), true)+
					label("model", emptyFallback(session.model, "unknown"))+
					dashPaint("tokens ", usageMutedColor, false)+
					dashPaint(formatTokenCount(sessionTokenTotal(session))+" ", usageTextColor, false)+
					dashTokenBreakdownText(session.tokens)+dashPaint(share, usageMutedColor, false),
			)
			continue
		}
		originLabel, origin := "source", session.source
		if session.repository != "" {
			originLabel, origin = "repo", session.repository
		}
		heading := "SESSION "
		lines = append(lines,
			dashPaint(heading, usageBrightColor, true)+dashPaint(emptyFallback(session.id, "unknown"), dashOrange, true)+
				dashPaint(" · "+sanitizeProcessCommand(emptyFallback(session.cwd, "unknown cwd")), usageTextColor, false),
			dashPaint("title ", usageMutedColor, false)+dashPaint(sanitizeProcessCommand(emptyFallback(session.name, "—")), usageBrightColor, true),
			dashPaint("state ", usageMutedColor, false)+
				dashPaint(dashStateIcon(session.state)+" "+session.state.String()+"   ", dashStateColor(session.state), true)+
				label("model", emptyFallback(session.model, "unknown")),
			label(originLabel, emptyFallback(origin, "unknown"))+
				label("branch", emptyFallback(session.gitBranch, "none")),
			dashPaint("tokens ", usageMutedColor, false)+
				dashPaint(formatTokenCount(sessionTokenTotal(session))+" ", usageTextColor, false)+
				dashTokenBreakdownText(session.tokens)+dashSubagentRollup(subTotal, subCount),
		)
		subTotal, subCount = 0, 0
		if session.telemetryLine != "" {
			lines = append(lines, dashPaint("background calls ", usageMutedColor, false)+
				dashPaint(formatTokenCount(session.backgroundTokens)+" ", dashYellow, false)+
				dashPaint("(not in transcripts)", usageMutedColor, false))
			for _, line := range wrapItems(session.backgroundCalls, " · ", max(width-2, 1)) {
				lines = append(lines, dashPaint("  "+line, dashYellow, false))
			}
			lines = append(lines, dashPaint("transcripts ", usageMutedColor, false)+dashPaint(formatTokenCount(session.transcriptTokens), usageTextColor, false))
		} else if session.undercountWarning != "" {
			lines = append(lines, dashPaint(session.undercountWarning, dashYellow, false))
		}
		if session.telemetryHint {
			lines = append(lines, dashPaint("firekeeper telemetry enable gets exact totals", usageMutedColor, false))
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
	if len(m.processGroups) == 0 {
		return []string{dashPaint("No sessions", usageMutedColor, false)}
	}
	if len(m.processGroups) == 0 || m.processCursor >= len(m.processGroups) {
		return []string{dashPaint("Select a session to see its details.", usageMutedColor, false)}
	}
	return dashGroupDetail(m.processGroups[m.processCursor], width)
}

// dashSummary aggregates the visible runtimes for the summary panels.
type dashSummary struct {
	sessions  int
	states    map[sessionState]int
	tokens    int64
	breakdown tokenBreakdown
	tools     []dashToolSummary
	projects  []dashProjectSummary
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
			// Subagent tokens are spent by their runtime, so they count toward
			// the totals. A subagent is not a session of its own: it is left out
			// of the session, state and project counts.
			summary.tokens += sessionTokenTotal(session)
			summary.breakdown.Input += session.tokens.Input
			summary.breakdown.Output += session.tokens.Output
			summary.breakdown.CacheRead += session.tokens.CacheRead
			summary.breakdown.CacheWrite += session.tokens.CacheWrite
			tool.tokens += sessionTokenTotal(session)
			if session.subagent {
				continue
			}
			summary.sessions++
			summary.states[session.state]++
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
		{"summary", dashBlue, summary.summaryLines},
		{m.dashTokensTitle(), dashYellow, m.dashTokenLines},
		{"providers", dashPurple, summary.providerLines},
		{"projects", dashGreen, summary.projectLines},
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

func (s dashSummary) summaryLines(width int) []string {
	state := func(value sessionState, label string) string {
		count := s.states[value]
		if count == 0 {
			return dashPaint(dashStateIcon(value)+" ", usageMutedColor, false) +
				dashPaint(fmt.Sprintf("%d %-9s", count, label), usageMutedColor, false)
		}
		number := dashPaint(fmt.Sprintf("%d", count), dashStateColor(value), true)
		if value == sessionStateNeedsInput {
			// The state that needs the user stands out as a highlighted block.
			number = usagePaint(fmt.Sprintf(" %d ", count), usagePanelColor, true, dashRed)
		}
		pad := strings.Repeat(" ", max(9-len(label), 0))
		return dashPaint(dashStateIcon(value)+" ", dashStateColor(value), true) +
			number + dashPaint(" "+label+pad, dashStateColor(value), false)
	}
	return []string{
		dashPaint("Sessions ", usageMutedColor, false) + dashPaint(fmt.Sprint(s.sessions), usageBrightColor, true),
		state(sessionStateActive, "active"),
		state(sessionStateWaiting, "waiting"),
		state(sessionStateNeedsInput, "input"),
		dashPaint("Tokens ", usageMutedColor, false) + dashPaint(formatTokenCount(s.tokens), dashYellow, true) +
			dashPaint(" total", usageMutedColor, false),
		s.splitLine(),
	}
}

// splitLine shows how the running sessions' tokens divide into input, output
// and cache, for sessions that report a breakdown.
func (s dashSummary) splitLine() string {
	total := s.breakdown.Total()
	if total == 0 {
		return dashPaint("no token split reported", usageMutedColor, false)
	}
	part := func(label string, value int64, color string) string {
		return dashPaint(label+" ", usageMutedColor, false) + dashPaint(dashPercent(value, total), color, false)
	}
	dot := dashPaint(" · ", usageMutedColor, false)
	return part("in", s.breakdown.Input, dashBlue) + dot +
		part("out", s.breakdown.Output, dashGreen) + dot +
		part("cache", s.breakdown.Cache(), dashPurple)
}

// dashPercent formats value as a share of total, showing <1% rather than 0%
// for a part that exists.
func dashPercent(value, total int64) string {
	if value > 0 && value*200 < total {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", float64(value)*100/float64(total))
}

func (s dashSummary) providerLines(width int) []string {
	if len(s.tools) == 0 {
		return []string{dashPaint("No agent runtimes running.", usageMutedColor, false)}
	}
	if len(s.tools) == 1 {
		tool := s.tools[0]
		return []string{usageSplit(
			dashPaint(dashCell(toolLabel(tool.tool), 8+toolIconWidth(), false), dashToolColor(tool.tool), true)+
				dashPaint(fmt.Sprintf("%3d ", tool.runtimes), usageBrightColor, false),
			dashPaint(formatTokenCount(tool.tokens), usageTextColor, false),
		)}
	}
	var maximum int64
	for _, tool := range s.tools {
		maximum = max(maximum, tool.tokens)
	}
	barWidth := max(width-8-toolIconWidth()-4-8, 1)
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
			dashPaint(dashCell(toolLabel(tool.tool), 8+toolIconWidth(), false), dashToolColor(tool.tool), true)+
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

// dashHeaderLine is the top row shared by all tabs, with live state counts on
// the right.
func (m model) dashHeaderLine() string {
	left := dashPaint(" ", usageTextColor, false)
	for _, item := range []struct {
		tab   tab
		label string
	}{{processesTab, "Processes"}, {overviewTab, "Overview"}, {usageTab, "Usage"}} {
		if item.tab == m.activeTab {
			left += usagePaint("[ "+item.label+" ]", dashOrange, true, usageHighlightColor) + dashPaint(" ", usageTextColor, false)
		} else {
			left += dashPaint("  "+item.label+"   ", usageMutedColor, false)
		}
	}
	right := ""
	if m.notice != "" {
		right = dashPaint(sanitizeProcessCommand(m.notice)+" ", dashGreen, true)
	} else if !m.refreshedAt.IsZero() {
		summary := m.dashSummarize()
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

func footerKeysWidth(keys []footerKey, spacing int) int {
	width := 1
	for _, key := range keys {
		width += ansi.StringWidth(key.key) + 1 + ansi.StringWidth(key.action) + spacing
	}
	return width
}

func footerVersion(width int) (string, int) {
	label := ansi.Truncate(version.Version, max(width/2, 0), "…")
	available := max(width, 0)
	if label != "" {
		available = max(available-ansi.StringWidth(label)-1, 0)
	}
	return label, available
}

// renderFooterKeys paints the shortcuts and right-aligned version.
func renderFooterKeys(keys []footerKey, width int) string {
	label, available := footerVersion(width)
	spacing := "  "
	if footerKeysWidth(keys, len(spacing)) > available {
		spacing = " "
	}
	help := dashPaint(" ", usageTextColor, false)
	for _, key := range keys {
		help += dashPaint(key.key+" ", dashBlue, true) + dashPaint(key.action+spacing, usageTextColor, false)
	}
	help = dashFit(help, available, usagePanelColor)
	if label == "" {
		return help
	}
	return help + dashPaint(label, dashGreen, true) + dashPaint(" ", usageTextColor, false)
}

// statusFooter replaces the shortcuts with a centered, temporary status.
func statusFooter(status string, width int) string {
	text := ansi.Truncate(sanitizeProcessCommand(status), max(width-2, 0), "…")
	left := max((width-ansi.StringWidth(text))/2, 0)
	return dashFit(dashPaint(strings.Repeat(" ", left)+text, dashYellow, true), width, usagePanelColor)
}

func (m model) processFooter() string {
	if m.terminalStatus != "" {
		return statusFooter(m.terminalStatus, m.width)
	}
	if len(m.processGroups) == 0 {
		if len(m.recentSessions) > 0 {
			return renderFooterKeys([]footerKey{{"↑↓", "select"}, {"enter", "chat"}, {"R", "resume"}, {"r", "refresh"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"}}, m.width)
		}
		return renderFooterKeys([]footerKey{{"r", "refresh"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"}}, m.width)
	}
	keys := []footerKey{{"↑↓", "select"}}
	if m.dashboardLayout().detail == 0 {
		keys = append(keys, footerKey{"enter", "expand"})
	}
	keys = append(keys, []footerKey{
		{"s", "switch terminal"}, {"c", "chat"}, {"r", "refresh"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"},
	}...)
	_, available := footerVersion(m.width)
	for _, compact := range []footerKey{{"s", "terminal"}, {"tab", "next"}, {"r", "reload"}} {
		if footerKeysWidth(keys, 1) <= available {
			break
		}
		for index := range keys {
			if keys[index].key == compact.key {
				keys[index].action = compact.action
				break
			}
		}
	}
	return renderFooterKeys(keys, m.width)
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
			dashPaint("│ ", dashBorderColor, false)+dashFit(usageFitStat(line, inner), inner, usagePanelColor)+dashPaint(" │", dashBorderColor, false),
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

func dashSessionLabel(group processGroup) string {
	session, _ := firstSession(group)
	label := dashShortID(session.id)
	primaries := 0
	for _, session := range group.sessions {
		if !session.subagent {
			primaries++
		}
	}
	if extra := primaries - 1; extra > 0 {
		label += fmt.Sprintf(" +%d", extra)
	}
	return label
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

// sessionTokenTotal prefers an exact telemetry total when this session has
// one, then the provider's token breakdown, then the aggregate the provider
// reports. Measured transcript totals are not replaced unless telemetry
// has been recorded.
func sessionTokenTotal(session sessionInfo) int64 {
	if session.exactTokens > 0 {
		return session.exactTokens
	}
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
		return "tokens"
	}
	return "tokens (" + sanitizeProcessCommand(dashProjectName(session)) + "/" + dashShortID(session.id) + ")"
}

// dashTokenLines breaks down the selected session's tokens into input,
// output, cache read, and cache write bars, each scaled to the session total.
func (m model) dashTokenLines(width int) []string {
	if len(m.processGroups) == 0 {
		return []string{dashPaint("No sessions", usageMutedColor, false)}
	}
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
	if session.telemetryLine != "" {
		lines = append(lines, dashPaint(marquee(session.telemetryLine, width, m.marqueeFrame), dashYellow, false))
	} else if session.undercountWarning != "" {
		lines = append(lines, dashPaint(session.undercountWarning, dashYellow, false))
	}
	return lines
}
