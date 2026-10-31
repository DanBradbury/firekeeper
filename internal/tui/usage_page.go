package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// usage_page.go renders every provider's Usage tab with the same panels as
// the Processes tab: three cards across the top, a tokens by day chart, and
// at the bottom a selectable session list beside the selected session's
// details and a per-project token rollup. Each provider supplies a usagePage;
// Enter on the selected session opens its chat.

const (
	usageCardRows = 7

	// usageDetailRows is the height of the selected-session panel; below
	// usageDetailMinRows the right column shows projects only.
	usageDetailRows    = 12
	usageDetailMinRows = 8

	// usageDaysRows is the height of the tokens by day panel when there is
	// room for the session panels below it, which need usageSessionMinRows.
	usageDaysRows       = 9
	usageDaysMinRows    = 4
	usageSessionMinRows = 6

	usageSessionPage = 10
)

// usageField is one labelled line of the selected-session panel.
type usageField struct {
	Name, Value, Color string
}

// usageSession is a provider's historical session reduced to what the list,
// detail and project panels show.
type usageSession struct {
	ID, Title, WorkDir, Branch, Model string
	Tokens                            int64
	UpdatedAt                         time.Time
	Details                           []usageField
}

// usageCard is one box in the card row. Lines receives the body rows
// available and returns styled lines.
type usageCard struct {
	Title  string
	Accent string
	Lines  func(rows int) []string
}

// usagePage is everything the shared Usage layout needs from a provider.
type usagePage struct {
	// Label is the display name, also used as the chat view's tool name.
	Label string
	// Provider is the transcript provider used to open a session's chat.
	Provider transcript.Provider

	Loading     bool
	LoadingText string
	HasData     bool
	Err         string
	HistoryErr  string

	Cards []usageCard
	// Days returns the chart title suffix (for example " · model") and one
	// styled line per day, newest last.
	Days func(count, inner int) (suffix string, lines []string)

	Sessions []usageSession
	Cursor   int
}

func (m model) usagePage() usagePage {
	switch m.usageProvider {
	case copilotProvider:
		return m.copilotPage()
	case kimiProvider:
		return m.kimiPage()
	case claudeProvider:
		return m.claudePage()
	default:
		return m.codexPage()
	}
}

// usageCursorRef points at the selected-session cursor of the active provider.
func (m *model) usageCursorRef() *int {
	switch m.usageProvider {
	case copilotProvider:
		return &m.copilotHistoryCursor
	case kimiProvider:
		return &m.kimiHistoryCursor
	case claudeProvider:
		return &m.claudeHistoryCursor
	default:
		return &m.codexHistoryCursor
	}
}

func (m model) usageSessionCount() int {
	switch m.usageProvider {
	case copilotProvider:
		return len(m.copilotUsage.History)
	case kimiProvider:
		return len(m.kimiUsage.History)
	case claudeProvider:
		return len(m.claudeUsage.History)
	default:
		return len(m.codexUsage.History)
	}
}

// moveUsageCursor moves the session selection by delta, clamped to the list.
func (m *model) moveUsageCursor(delta int) {
	cursor := m.usageCursorRef()
	*cursor = min(max(*cursor+delta, 0), max(m.usageSessionCount()-1, 0))
}

func (m *model) setUsageCursorEnd() {
	*m.usageCursorRef() = max(m.usageSessionCount()-1, 0)
}

// clampUsageCursors keeps every provider's cursor inside its list after a refresh.
func (m *model) clampUsageCursors() {
	m.codexHistoryCursor = min(m.codexHistoryCursor, max(len(m.codexUsage.History)-1, 0))
	m.copilotHistoryCursor = min(m.copilotHistoryCursor, max(len(m.copilotUsage.History)-1, 0))
	m.kimiHistoryCursor = min(m.kimiHistoryCursor, max(len(m.kimiUsage.History)-1, 0))
	m.claudeHistoryCursor = min(m.claudeHistoryCursor, max(len(m.claudeUsage.History)-1, 0))
}

// openUsageSessionChat opens the chat view for the selected session. The
// session may no longer be running, so it is located by its id alone.
func (m *model) openUsageSessionChat() tea.Cmd {
	page := m.usagePage()
	if len(page.Sessions) == 0 {
		return nil
	}
	selected := page.Sessions[min(max(page.Cursor, 0), len(page.Sessions)-1)]
	m.chat = chatState{
		open: true,
		tool: page.Label,
		meta: session.Meta{
			ID:       selected.ID,
			Provider: string(page.Provider),
			CWD:      selected.WorkDir,
		},
		title:   usageProjectName(selected) + "/" + dashShortID(selected.ID),
		loading: true,
		follow:  true,
	}
	return loadChat(0, m.chat.meta, nil, nil)
}

func (m model) viewUsagePage(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	page := m.usagePage()
	if page.Loading && !page.HasData {
		return m.usageMessageBox("loading", page.LoadingText, contentRows)
	}
	if page.Err != "" && !page.HasData {
		return m.usageMessageBox("unavailable", page.Err, contentRows)
	}

	now := time.Now()
	cardHeight := min(usageCardRows, contentRows)
	lines := m.usageCardRow(page, cardHeight)
	if rest := contentRows - cardHeight; rest >= 4 {
		// The session list is interactive, so it keeps its minimum height
		// and the chart gives up rows first.
		days := rest
		if rest >= usageDaysMinRows+usageSessionMinRows {
			days = min(usageDaysRows, rest-usageSessionMinRows)
		}
		lines = append(lines, m.usageDaysPanel(page, days)...)
		if below := rest - days; below >= usageSessionMinRows {
			lines = append(lines, m.usageSessionRow(page, below, now)...)
		}
	}
	for len(lines) < contentRows {
		lines = append(lines, dashFit("", m.width, usagePanelColor))
	}
	return strings.Join(lines[:contentRows], "\n")
}

func (m model) usageMessageBox(title, message string, height int) string {
	body := []string{dashPaint(message, usageMutedColor, false)}
	return strings.Join(dashBox(title, body, m.width, height, dashBlue), "\n")
}

func (m model) usageCardRow(page usagePage, height int) []string {
	cards := page.Cards
	count := 1
	switch {
	case m.width >= 96:
		count = 3
	case m.width >= 64:
		count = 2
	}
	if len(cards) > 3 && m.width >= 128 {
		count = 4 // a provider with a limits card keeps its models card
	}
	cards = cards[:min(count, len(cards))]
	count = len(cards)

	rows := make([]string, height)
	remaining := m.width
	for index, card := range cards {
		width := remaining / (count - index)
		remaining -= width
		box := dashBox(card.Title, card.Lines(height-2), width, height, card.Accent)
		for row := range rows {
			rows[row] += box[row]
		}
	}
	return rows
}

// usageDaysPanel is the token history panel: one bar per day, stacked by
// model when the provider has a model breakdown.
func (m model) usageDaysPanel(page usagePage, height int) []string {
	dayCount := min(7, max(height-2, 1))
	suffix, body := page.Days(dayCount, max(m.width-4, 1))
	return dashBox("tokens by day"+suffix, body, m.width, height, dashGreen)
}

// usageSessionRow is the bottom of the Usage view: the selectable session
// list on the left, and on the right the selected session's details above a
// per-project token rollup. Narrow terminals show the list alone.
func (m model) usageSessionRow(page usagePage, height int, now time.Time) []string {
	history := page.Sessions
	body := height - 2
	title := usageSessionsTitle(page)
	if m.width < 80 {
		return dashBox(title, usageSessionLines(page, m.width-4, body, now), m.width, height, dashBlue)
	}
	rightWidth := m.width / 3
	if m.width >= 120 {
		rightWidth = m.width * 2 / 5
	}
	leftWidth := m.width - rightWidth
	left := dashBox(title, usageSessionLines(page, leftWidth-4, body, now), leftWidth, height, dashBlue)

	var right []string
	projects := fmt.Sprintf("projects · last %d", len(history))
	switch {
	case height < usageDetailMinRows:
		right = dashBox(projects, usageProjectLines(history, body), rightWidth, height, dashTeal)
	default:
		detail := min(usageDetailRows, height)
		if height-detail < 4 {
			detail = height // too little left for a projects panel
		}
		right = dashBox("session", usageSessionDetailLines(page), rightWidth, detail, dashTeal)
		if rest := height - detail; rest >= 4 {
			right = append(right, dashBox(projects, usageProjectLines(history, rest-2), rightWidth, rest, dashTeal)...)
		}
	}
	rows := make([]string, height)
	for row := range rows {
		rows[row] = left[row] + right[row]
	}
	return rows
}

func usageSessionsTitle(page usagePage) string {
	if len(page.Sessions) == 0 {
		return "sessions"
	}
	return fmt.Sprintf("sessions · %d/%d", min(page.Cursor+1, len(page.Sessions)), len(page.Sessions))
}

// usageSessionLines is the session table with the selected row highlighted
// and kept in view.
func usageSessionLines(page usagePage, width, rows int, now time.Time) []string {
	if len(page.Sessions) == 0 {
		message := "No historical " + page.Label + " sessions."
		switch {
		case page.HistoryErr != "":
			message = "Session history unavailable: " + page.HistoryErr
		case page.Loading:
			message = "Loading historical " + page.Label + " sessions…"
		}
		return []string{dashPaint(message, usageMutedColor, false)}
	}
	columns := usageSessionColumns(width, page.Sessions)
	var lines []string
	capacity := rows
	if rows >= 4 {
		lines = append(lines, columns.header())
		capacity--
	}
	start, end := usageSessionWindow(len(page.Sessions), page.Cursor, capacity)
	for index := start; index < end; index++ {
		lines = append(lines, columns.row(page.Sessions[index], index == page.Cursor, now))
	}
	return lines
}

func usageProjectName(session usageSession) string {
	if session.WorkDir == "" {
		return "unknown"
	}
	return filepath.Base(session.WorkDir)
}

// usageProjectLines rolls the history up by working directory name, largest
// token total first.
func usageProjectLines(history []usageSession, rows int) []string {
	type total struct {
		name     string
		tokens   int64
		sessions int
	}
	byName := map[string]*total{}
	var totals []*total
	for _, session := range history {
		name := usageProjectName(session)
		entry := byName[name]
		if entry == nil {
			entry = &total{name: name}
			byName[name] = entry
			totals = append(totals, entry)
		}
		entry.tokens += session.Tokens
		entry.sessions++
	}
	if len(totals) == 0 {
		return []string{dashPaint("No project data.", usageMutedColor, false)}
	}
	sort.SliceStable(totals, func(i, j int) bool {
		if totals[i].tokens != totals[j].tokens {
			return totals[i].tokens > totals[j].tokens
		}
		return totals[i].name < totals[j].name
	})
	countWidth := 0
	for _, entry := range totals {
		countWidth = max(countWidth, len(fmt.Sprintf("×%d", entry.sessions)))
	}
	var lines []string
	for index, entry := range totals {
		if index == rows-1 && len(totals) > rows {
			lines = append(lines, dashPaint(fmt.Sprintf("+%d more", len(totals)-index), usageMutedColor, false))
			break
		}
		lines = append(lines,
			usageSplit(
				dashPaint(entry.name, usageBrightColor, true),
				dashPaint(dashCell(formatTokenCount(entry.tokens), 7, true), dashYellow, true)+
					dashPaint("  "+dashCell(fmt.Sprintf("×%d", entry.sessions), countWidth, true), dashOrange, false),
			),
		)
	}
	return lines
}

// usageAgo is a compact relative time such as 5m, 3h or 2d.
func usageAgo(value, now time.Time) string {
	if value.IsZero() || value.UnixMilli() == 0 {
		return "—"
	}
	elapsed := now.Sub(value)
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed/time.Minute))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(elapsed/(24*time.Hour)))
	}
}

// usageSessionWindow returns the slice of sessions to draw so the cursor
// stays visible.
func usageSessionWindow(total, cursor, capacity int) (int, int) {
	if total <= 0 || capacity <= 0 {
		return 0, 0
	}
	capacity = min(capacity, total)
	cursor = min(max(cursor, 0), total-1)
	start := min(max(cursor-capacity+1, 0), total-capacity)
	return start, start + capacity
}

type usageSessionCols struct {
	title, project, branch, model, tokens, updated int
}

// usageSessionColumns picks the columns that fit width. The branch column is
// left out when no session has a branch.
func usageSessionColumns(width int, sessions []usageSession) usageSessionCols {
	cols := usageSessionCols{tokens: 7, updated: 5}
	used := 2 + cols.tokens + cols.updated + 2 // cursor, spacing
	hasBranch := false
	for _, session := range sessions {
		if session.Branch != "" {
			hasBranch = true
			break
		}
	}
	if width >= 60 {
		cols.project = 14
		used += cols.project + 1
	}
	if width >= 84 && hasBranch {
		cols.branch = 16
		used += cols.branch + 1
	}
	if width >= 104 {
		cols.model = 16
		used += cols.model + 1
	}
	cols.title = max(width-used, 8)
	return cols
}

func (c usageSessionCols) header() string {
	line := dashPaint("  "+dashCell("TITLE", c.title, false)+" ", usageMutedColor, true)
	if c.project > 0 {
		line += dashPaint(dashCell("PROJECT", c.project, false)+" ", usageMutedColor, true)
	}
	if c.branch > 0 {
		line += dashPaint(dashCell("BRANCH", c.branch, false)+" ", usageMutedColor, true)
	}
	if c.model > 0 {
		line += dashPaint(dashCell("MODEL", c.model, false)+" ", usageMutedColor, true)
	}
	return line + dashPaint(dashCell("TOKENS", c.tokens, true)+" "+dashCell("AGO", c.updated, true), usageMutedColor, true)
}

func (c usageSessionCols) row(session usageSession, selected bool, now time.Time) string {
	background := usagePanelColor
	cursor := "  "
	if selected {
		background = usageHighlightColor
		cursor = "▸ "
	}
	paint := func(value, color string, bold bool) string { return usagePaint(value, color, bold, background) }
	line := paint(cursor, dashOrange, true) + paint(dashCell(session.Title, c.title, false)+" ", usageBrightColor, selected)
	if c.project > 0 {
		line += paint(dashCell(usageProjectName(session), c.project, false)+" ", dashTeal, false)
	}
	if c.branch > 0 {
		line += paint(dashCell(session.Branch, c.branch, false)+" ", dashBlue, false)
	}
	if c.model > 0 {
		line += paint(dashCell(session.Model, c.model, false)+" ", usageTextColor, false)
	}
	return line + paint(dashCell(formatTokenCount(session.Tokens), c.tokens, true)+" ", dashYellow, true) +
		paint(dashCell(usageAgo(session.UpdatedAt, now), c.updated, true), usageMutedColor, false)
}

func usageSessionDetailLines(page usagePage) []string {
	if len(page.Sessions) == 0 {
		return []string{dashPaint("No session selected.", usageMutedColor, false)}
	}
	session := page.Sessions[min(max(page.Cursor, 0), len(page.Sessions)-1)]
	lines := []string{
		dashPaint(session.Title, usageBrightColor, true),
		usageFieldLine("ID", session.ID, usageTextColor),
		usageFieldLine("Path", dashShortPath(session.WorkDir), dashTeal),
	}
	if session.Branch != "" {
		lines = append(lines, usageFieldLine("Branch", session.Branch, dashBlue))
	}
	lines = append(lines,
		usageFieldLine("Model", session.Model, usageTextColor),
		usageFieldLine("Tokens", formatTokenCount(session.Tokens), dashYellow),
		usageFieldLine("Updated", formatSessionTime(session.UpdatedAt), usageTextColor),
	)
	for _, field := range session.Details {
		color := field.Color
		if color == "" {
			color = usageTextColor
		}
		lines = append(lines, usageFieldLine(field.Name, field.Value, color))
	}
	return lines
}

func usageFieldLine(name, value, color string) string {
	return dashPaint(dashCell(name, 9, false), usageMutedColor, false) + dashPaint(emptyFallback(value, "—"), color, false)
}

// usageStat is a label and value line for the summary cards.
func usageStat(name, value, color string) string {
	if color == "" {
		color = usageBrightColor
	}
	return usageSplit(dashPaint(name, usageMutedColor, false), dashPaint(value, color, true))
}

// usageSplit joins a painted name and value so usageFitStat floats the value
// to the right edge of whatever box the line lands in.
func usageSplit(name, value string) string { return name + usageStatGap + value }

// usageStatGap separates a stat's name from its value until the card width is
// known; usageFitStat then floats the value to the right edge.
const usageStatGap = "\x1f"

// usageFitStat expands the gap in a usageSplit line so the value sits flush
// right. When the row is too narrow the name is truncated, never the value.
func usageFitStat(line string, inner int) string {
	name, value, ok := strings.Cut(line, usageStatGap)
	if !ok {
		return line
	}
	valueWidth := ansi.StringWidth(value)
	if ansi.StringWidth(name)+1+valueWidth > inner {
		name = ansi.Truncate(name, max(inner-valueWidth-1, 0), "…")
	}
	gap := max(inner-ansi.StringWidth(name)-valueWidth, 1)
	return name + dashPaint(strings.Repeat(" ", gap), usageTextColor, false) + value
}

// usageNote is a muted explanatory line, such as where to find quota.
func usageNote(text string) string { return dashPaint(text, usageMutedColor, false) }

// usageModelCardLines lists models as coloured diamonds with token counts.
func usageModelCardLines(models []modelUsage, rows int, empty string) []string {
	if len(models) == 0 {
		return []string{usageNote(empty)}
	}
	var lines []string
	for index, model := range models {
		if rows > 0 && index == rows-1 && len(models) > rows {
			lines = append(lines, usageNote(fmt.Sprintf("+%d more", len(models)-index)))
			break
		}
		name := strings.ReplaceAll(strings.ToUpper(model.Model), "-", " ")
		lines = append(lines,
			usageSplit(
				dashPaint("◆ ", model.Color, true)+dashPaint(name, usageTextColor, false),
				dashPaint(formatTokenCount(model.Tokens), usageBrightColor, true),
			),
		)
	}
	return lines
}

// usageModelDays draws the tokens by day chart stacked by model.
func usageModelDays(days []codexDailyModelUsageBucket, colors map[string]string, inner int) []string {
	for index := range days {
		days[index].Models = colorModelUsageWithAssignments(days[index].Models, colors)
	}
	peak := int64(0)
	for _, day := range days {
		total := int64(0)
		for _, model := range day.Models {
			total += model.Tokens
		}
		peak = max(peak, total)
	}
	lines := make([]string, 0, len(days))
	for index, day := range days {
		label := dayLabel(day.StartDate, index == len(days)-1)
		lines = append(lines, usageModelStackBarLine(label, day.Models, peak, inner, index == len(days)-1))
	}
	return lines
}

// usageTotalDays draws the tokens by day chart as one bar per day.
func usageTotalDays(dates []string, tokens []int64, inner int) []string {
	peak := int64(0)
	for _, value := range tokens {
		peak = max(peak, value)
	}
	lines := make([]string, 0, len(dates))
	for index, date := range dates {
		last := index == len(dates)-1
		lines = append(lines, usageMetricBarLine(dayLabel(date, last), tokens[index], peak, inner, last))
	}
	return lines
}
