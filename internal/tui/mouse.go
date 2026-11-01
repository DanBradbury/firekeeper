package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// mouse.go makes the dashboard mouse-native: clicking tabs, provider chips and
// session rows, double-clicking a row for its main action, the wheel for
// scrolling, and right-click context menus. Text selection and copying live in
// mouse_select.go. Every mouse action maps onto an existing keyboard action,
// so the keyboard controls are unchanged.

const (
	doubleClickWindow = 400 * time.Millisecond
	wheelChatLines    = 3
)

// mouseNow is the clock for double-click detection; tests replace it.
var mouseNow = time.Now

type cell struct{ x, y int }

type hitKind int

const (
	hitNone hitKind = iota
	hitTab
	hitProcess
	hitRecent
	hitUsageProvider
	hitUsageSession
)

// hit is the interactive element under a screen cell.
type hit struct {
	kind  hitKind
	index int
}

type mouseState struct {
	lastPress time.Time
	lastCell  cell
	lastHit   hit

	dragging bool
	wordMode bool
	anchor   cell
	cursor   cell
	// anchorWord is the [start, end) columns of the word under a double click.
	anchorWord [2]int
}

type menuItem struct {
	label string
	key   tea.KeyMsg
}

type contextMenu struct {
	x, y   int
	cursor int
	items  []menuItem
}

func keyRunes(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// chipSpans returns the [start, end) columns of each chip in a tab-bar style
// row. Every chip is its label plus five cells, active or not.
func chipSpans(labels []string) [][2]int {
	spans := make([][2]int, len(labels))
	x := 1
	for index, label := range labels {
		width := ansi.StringWidth(label) + 5
		spans[index] = [2]int{x, x + width}
		x += width
	}
	return spans
}

var (
	tabLabels           = []string{"Processes", "Overview", "Usage"}
	usageProviderLabels = []string{"CODEX", "COPILOT", "KIMI", "CLAUDE"}
)

func chipAt(labels []string, x int) (int, bool) {
	for index, span := range chipSpans(labels) {
		if x >= span[0] && x < span[1] {
			return index, true
		}
	}
	return 0, false
}

// hitTest finds the interactive element at screen cell (x, y). Overlays and
// the chat view have none: they take wheel, selection and right-click only.
func (m model) hitTest(x, y int) hit {
	if m.helpOpen || m.chat.open || m.width < 20 || m.height < 6 || x < 0 || x >= m.width {
		return hit{}
	}
	if y == 0 {
		if index, ok := chipAt(tabLabels, x); ok {
			return hit{hitTab, index}
		}
		return hit{}
	}
	rows := m.height - chromeRows
	row := y - 1
	if row < 0 || row >= rows {
		return hit{}
	}
	switch m.activeTab {
	case processesTab:
		if len(m.processGroups) == 0 {
			if index, ok := m.recentIndexAt(row, rows); ok {
				return hit{hitRecent, index}
			}
			return hit{}
		}
		if index, ok := m.processIndexAt(x, row); ok {
			return hit{hitProcess, index}
		}
	case usageTab:
		if row == 0 {
			if index, ok := chipAt(usageProviderLabels, x); ok {
				return hit{hitUsageProvider, index}
			}
			return hit{}
		}
		if index, ok := m.usageSessionIndexAt(x, row-1, rows-1); ok {
			return hit{hitUsageSession, index}
		}
	}
	return hit{}
}

// processIndexAt maps a content row to the runtime whose row (or inline
// detail) is drawn there.
func (m model) processIndexAt(x, row int) (int, bool) {
	layout := m.dashboardLayout()
	bodyStart := layout.summary + 1
	if layout.boxed {
		bodyStart = layout.summary + 2
		if x < 1 || x >= m.width-1 {
			return 0, false
		}
	}
	visible := row - bodyStart
	if visible < 0 || visible >= m.processPageSize() {
		return 0, false
	}
	lines, offsets := m.processBodyLines()
	line := m.processScroll + visible
	if line >= len(lines) {
		return 0, false
	}
	index := 0
	for group, offset := range offsets {
		if offset <= line {
			index = group
		}
	}
	return index, true
}

// recentIndexAt maps a content row of the idle view to a recent session.
func (m model) recentIndexAt(row, rows int) (int, bool) {
	body := m.idleBody()
	capacity := min(rows-1-len(body)-2, recentSessionLimit+1)
	if len(m.recentSessions) == 0 || capacity < 2 {
		return 0, false
	}
	start, _ := m.idleGeometry(rows)
	first := start + len(body) + 2 // blank separator and heading
	windowStart, windowEnd := usageSessionWindow(len(m.recentSessions), m.recentCursor, capacity-1)
	if offset := row - first; offset >= 0 && offset < windowEnd-windowStart {
		return windowStart + offset, true
	}
	return 0, false
}

// usageSessionIndexAt maps a row of the Usage page (below the provider
// selector) to a session in the history list.
func (m model) usageSessionIndexAt(x, pageRow, pageRows int) (int, bool) {
	page := m.usagePage()
	if (page.Loading && !page.HasData) || (page.Err != "" && !page.HasData) {
		return 0, false
	}
	geometry := usageGeometryFor(page, pageRows)
	if geometry.sessions == 0 || len(page.Sessions) == 0 {
		return 0, false
	}
	listWidth := m.width
	if m.width >= 80 {
		right := m.width / 3
		if m.width >= 120 {
			right = m.width * 2 / 5
		}
		listWidth = m.width - right
	}
	if x < 1 || x >= listWidth-1 {
		return 0, false
	}
	body := geometry.sessions - 2
	first := geometry.usageSessionsTop() + 1
	capacity := body
	if body >= 4 {
		first++
		capacity--
	}
	start, end := usageSessionWindow(len(page.Sessions), page.Cursor, capacity)
	if offset := pageRow - first; offset >= 0 && offset < end-start {
		return start + offset, true
	}
	return 0, false
}

func (m model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.width < 20 || m.height < 6 {
		return m, nil
	}
	x, y := msg.X, msg.Y
	switch {
	case msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		m.menu = nil
		return m.wheel(delta), nil
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		return m.leftPress(cell{x, y})
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight:
		m.notice = ""
		m.clearSelection()
		return m.openMenu(cell{x, y}), nil
	case msg.Action == tea.MouseActionMotion && m.mouse.dragging:
		m.dragTo(cell{x, y})
		return m, nil
	case msg.Action == tea.MouseActionRelease && m.mouse.dragging:
		return m.endDrag()
	}
	return m, nil
}

// wheel scrolls whichever list or view is under focus.
func (m model) wheel(delta int) model {
	switch {
	case m.helpOpen:
	case m.chat.open:
		m.scrollChat(delta * wheelChatLines)
	case m.activeTab == processesTab && len(m.processGroups) > 0:
		m.processCursor += delta
		m.clampProcessSelection()
	case m.idleListActive():
		m.moveRecentCursor(delta)
	case m.activeTab == usageTab:
		m.moveUsageCursor(delta)
	}
	return m
}

func (m model) leftPress(at cell) (tea.Model, tea.Cmd) {
	m.notice = ""
	if m.helpOpen {
		m.helpOpen = false
		return m, nil
	}
	if m.menu != nil {
		index, ok := m.menuItemAt(at)
		item := menuItem{}
		if ok {
			item = m.menu.items[index]
		}
		m.menu = nil
		if ok {
			return m.Update(item.key)
		}
		return m, nil
	}
	m.clearSelection()
	now := mouseNow()
	target := m.hitTest(at.x, at.y)
	repeat := !m.mouse.lastPress.IsZero() && now.Sub(m.mouse.lastPress) <= doubleClickWindow
	double := repeat && (target.kind != hitNone && target == m.mouse.lastHit || target.kind == hitNone && at == m.mouse.lastCell && m.mouse.lastHit.kind == hitNone)
	m.mouse.lastPress, m.mouse.lastCell, m.mouse.lastHit = now, at, target
	if double {
		// A second click on the same target must not pair with a third.
		m.mouse.lastPress = time.Time{}
	}

	var cmd tea.Cmd
	switch target.kind {
	case hitTab:
		if t := tab(target.index); t != m.activeTab {
			return m, m.switchTab(t)
		}
		return m, nil
	case hitUsageProvider:
		if provider := usageProvider(target.index); provider != m.usageProvider {
			cmd = m.cycleUsageProvider(int(provider - m.usageProvider))
		}
		return m, cmd
	case hitProcess:
		m.processCursor = target.index
		m.clampProcessSelection()
		if double {
			return m.Update(keyRunes('s'))
		}
	case hitRecent:
		m.recentCursor = target.index
		if double {
			cmd = m.openRecentChat()
			return m, cmd
		}
	case hitUsageSession:
		*m.usageCursorRef() = target.index
		if double {
			cmd = m.openUsageSessionChat()
			return m, cmd
		}
	default:
		if double {
			m.startWordSelection(at)
			return m, nil
		}
	}
	m.mouse.dragging, m.mouse.wordMode = true, false
	m.mouse.anchor, m.mouse.cursor = at, at
	return m, nil
}

// openMenu selects the element under the pointer and lists what can be done
// with it. Each entry replays a keyboard shortcut.
func (m model) openMenu(at cell) model {
	target := m.hitTest(at.x, at.y)
	refresh := menuItem{"Refresh", keyRunes('r')}
	help := menuItem{"Keyboard help", keyRunes('?')}
	var items []menuItem
	switch {
	case m.helpOpen:
		return m
	case m.chat.open:
		items = []menuItem{{"Back", tea.KeyMsg{Type: tea.KeyEsc}}, {"Reload transcript", keyRunes('r')}, {"Jump to latest", keyRunes('G')}}
	case target.kind == hitProcess:
		m.processCursor = target.index
		m.clampProcessSelection()
		items = []menuItem{{"Open chat", keyRunes('c')}, {"Switch to terminal", keyRunes('s')}, refresh}
	case target.kind == hitRecent:
		m.recentCursor = target.index
		items = []menuItem{{"Open chat", tea.KeyMsg{Type: tea.KeyEnter}}, {"Resume in new tab", keyRunes('R')}, refresh}
	case target.kind == hitUsageSession:
		*m.usageCursorRef() = target.index
		items = []menuItem{{"Open chat", tea.KeyMsg{Type: tea.KeyEnter}}, refresh}
	default:
		items = []menuItem{refresh, help}
	}
	m.menu = &contextMenu{x: at.x, y: at.y, items: items}
	return m
}

func (c *contextMenu) width() int {
	width := 0
	for _, item := range c.items {
		width = max(width, ansi.StringWidth(item.label))
	}
	return width + 4
}

// origin is the menu's top-left cell, moved to keep it on a screen of the
// given size.
func (c *contextMenu) origin(screenWidth, screenHeight int) cell {
	return cell{
		x: max(min(c.x, screenWidth-c.width()), 0),
		y: max(min(c.y, screenHeight-len(c.items)-2), 0),
	}
}

// menuItemAt resolves a click inside the open menu to an entry.
func (m model) menuItemAt(at cell) (int, bool) {
	if m.menu == nil {
		return 0, false
	}
	origin := m.menu.origin(m.width, m.height)
	row := at.y - origin.y - 1
	if at.x >= origin.x && at.x < origin.x+m.menu.width() && row >= 0 && row < len(m.menu.items) {
		return row, true
	}
	return 0, false
}

// menuLines draws the menu box.
func (m model) menuLines() []string {
	width := m.menu.width()
	lines := []string{dashPaint("╭"+strings.Repeat("─", width-2)+"╮", dashBorderColor, false)}
	for index, item := range m.menu.items {
		background := usagePanelColor
		if index == m.menu.cursor {
			background = usageHighlightColor
		}
		label := ansi.Truncate(item.label, width-4, "…")
		lines = append(lines,
			dashPaint("│", dashBorderColor, false)+
				usagePaint(" "+label+strings.Repeat(" ", width-3-ansi.StringWidth(label)), usageBrightColor, index == m.menu.cursor, background)+
				dashPaint("│", dashBorderColor, false))
	}
	return append(lines, dashPaint("╰"+strings.Repeat("─", width-2)+"╯", dashBorderColor, false))
}

// overlayMenu draws the menu over the screen rows.
func (m model) overlayMenu(lines []string) []string {
	origin := m.menu.origin(m.width, m.height)
	for offset, box := range m.menuLines() {
		row := origin.y + offset
		if row >= len(lines) {
			break
		}
		lines[row] = ansi.Cut(lines[row], 0, origin.x) + box + ansi.Cut(lines[row], origin.x+m.menu.width(), m.width)
	}
	return lines
}

// updateMenuKey handles keys while a context menu is open.
func (m model) updateMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.menu.cursor = max(m.menu.cursor-1, 0)
	case "down", "j":
		m.menu.cursor = min(m.menu.cursor+1, len(m.menu.items)-1)
	case "enter":
		item := m.menu.items[m.menu.cursor]
		m.menu = nil
		return m.Update(item.key)
	default:
		m.menu = nil
	}
	return m, nil
}
