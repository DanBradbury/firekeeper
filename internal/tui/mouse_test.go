package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func press(m model, x, y int) model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return next.(model)
}

func pressCmd(m model, x, y int) (model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return next.(model), cmd
}

func release(m model, x, y int) (model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	return next.(model), cmd
}

func motion(m model, x, y int) model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	return next.(model)
}

func rightClick(m model, x, y int) model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress})
	return next.(model)
}

func wheelMsg(m model, button tea.MouseButton) model {
	next, _ := m.Update(tea.MouseMsg{Button: button, Action: tea.MouseActionPress})
	return next.(model)
}

// rowOf returns the screen row whose plain text contains needle.
func rowOf(t *testing.T, m model, needle string) int {
	t.Helper()
	for y, row := range strings.Split(ansi.Strip(m.screen()), "\n") {
		if strings.Contains(row, needle) {
			return y
		}
	}
	t.Fatalf("no row contains %q:\n%s", needle, ansi.Strip(m.screen()))
	return -1
}

// fakeClock makes double-click timing deterministic.
func fakeClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	previous := mouseNow
	mouseNow = func() time.Time { return now }
	t.Cleanup(func() { mouseNow = previous })
	return &now
}

func TestClickTabsSwitchViews(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 120, 40
	x := 1 + len("Processes") + 5 + 2 // inside the Overview chip
	next, _ := pressCmd(m, x, 0)
	if next.activeTab != overviewTab {
		t.Fatalf("active tab = %v after clicking Overview", next.activeTab)
	}
	next, _ = pressCmd(next, 1+len("Processes")+5+len("Overview")+5+2, 0)
	if next.activeTab != usageTab {
		t.Fatalf("active tab = %v after clicking Usage", next.activeTab)
	}
	next, _ = pressCmd(next, 3, 0)
	if next.activeTab != processesTab {
		t.Fatalf("active tab = %v after clicking Processes", next.activeTab)
	}
}

func TestClickProcessRowSelectsAndDoubleClickSwitchesTerminal(t *testing.T) {
	now := fakeClock(t)
	m := dashboardTestModel()
	m.width, m.height = 140, 40
	row := -1
	for i, line := range strings.Split(ansi.Strip(m.screen()), "\n") {
		if strings.Contains(line, "Copilot ") && strings.Contains(line, "/workspace/api") {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("copilot row not found:\n%s", ansi.Strip(m.screen()))
	}
	m = press(m, 30, row)
	if m.processCursor != 1 {
		t.Fatalf("cursor = %d after clicking the second row", m.processCursor)
	}
	*now = now.Add(100 * time.Millisecond)
	next, cmd := pressCmd(m, 30, row)
	if cmd == nil || !strings.HasPrefix(next.terminalStatus, "switching to") {
		t.Fatalf("double click did not switch terminal: %q", next.terminalStatus)
	}
}

func TestSlowSecondClickIsNotDouble(t *testing.T) {
	now := fakeClock(t)
	m := dashboardTestModel()
	m.width, m.height = 140, 40
	row := rowOf(t, m, "019fb33c")
	m = press(m, 30, row)
	*now = now.Add(2 * time.Second)
	next, _ := pressCmd(m, 30, row)
	if next.terminalStatus != "" {
		t.Fatalf("slow click acted as a double click: %q", next.terminalStatus)
	}
}

func TestIdleRecentRowsClickAndDoubleClick(t *testing.T) {
	now := fakeClock(t)
	m := idleRecentModel(100, 30)
	row := rowOf(t, m, "Gemini")
	m = press(m, 20, row)
	if m.recentCursor != 3 {
		t.Fatalf("cursor = %d after clicking Gemini", m.recentCursor)
	}
	*now = now.Add(50 * time.Millisecond)
	next, cmd := pressCmd(m, 20, row)
	if cmd == nil || !next.chat.open || next.chat.tool != "Gemini" {
		t.Fatalf("double click did not open the chat: %+v", next.chat)
	}
	if got := m.hitTest(20, 1); got.kind != hitNone {
		t.Fatalf("status strip hit %+v", got)
	}
}

func TestUsageChipsAndSessionRows(t *testing.T) {
	now := fakeClock(t)
	m := NewModel()
	m.width, m.height = 140, 40
	m.activeTab = usageTab
	m.codexUsageRefreshedAt = time.Now()
	m.copilotUsageRefreshedAt = time.Now()
	for i := range 6 {
		m.codexUsage.History = append(m.codexUsage.History, codexHistorySessionForTest(i))
	}
	// Copilot chip: after CODEX (5+5) at column 1.
	next, _ := pressCmd(m, 1+len("CODEX")+5+3, 1)
	if next.usageProvider != copilotProvider {
		t.Fatalf("provider = %v after clicking COPILOT", next.usageProvider)
	}
	back, _ := pressCmd(next, 3, 1)
	if back.usageProvider != codexProvider {
		t.Fatalf("provider = %v after clicking CODEX", back.usageProvider)
	}
	// Wheel moves the session cursor.
	scrolled := wheelMsg(back, tea.MouseButtonWheelDown)
	if scrolled.codexHistoryCursor != 1 {
		t.Fatalf("wheel cursor = %d", scrolled.codexHistoryCursor)
	}
	// Find a session row by probing for the one hitTest reports at index 3.
	found := -1
	for y := 2; y < m.height; y++ {
		if h := m.hitTest(5, y); h.kind == hitUsageSession && h.index == 3 {
			found = y
		}
	}
	if found < 0 {
		t.Fatal("no usage session row at index 3")
	}
	clicked := press(m, 5, found)
	if clicked.codexHistoryCursor != 3 {
		t.Fatalf("cursor = %d after clicking row", clicked.codexHistoryCursor)
	}
	*now = now.Add(50 * time.Millisecond)
	opened, cmd := pressCmd(clicked, 5, found)
	if cmd == nil || !opened.chat.open {
		t.Fatal("double click did not open the session chat")
	}
}

func TestWheelScrollsLists(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 140, 40
	m = wheelMsg(m, tea.MouseButtonWheelDown)
	if m.processCursor != 1 {
		t.Fatalf("process cursor = %d", m.processCursor)
	}
	m = wheelMsg(m, tea.MouseButtonWheelUp)
	m = wheelMsg(m, tea.MouseButtonWheelUp)
	if m.processCursor != 0 {
		t.Fatalf("process cursor = %d after scrolling up past the top", m.processCursor)
	}
	idle := idleRecentModel(100, 30)
	idle = wheelMsg(idle, tea.MouseButtonWheelDown)
	if idle.recentCursor != 1 {
		t.Fatalf("recent cursor = %d", idle.recentCursor)
	}
	chat := dashboardTestModel()
	chat.width, chat.height = 60, 12
	chat.chat = chatState{open: true, follow: false}
	for range 40 {
		chat.chat.messages = append(chat.chat.messages, chatMessage{text: "line"})
	}
	chat = wheelMsg(chat, tea.MouseButtonWheelDown)
	if chat.chat.scroll != wheelChatLines {
		t.Fatalf("chat scroll = %d", chat.chat.scroll)
	}
}

func TestRightClickMenuRunsShortcut(t *testing.T) {
	m := idleRecentModel(100, 30)
	row := rowOf(t, m, "Codex")
	m = rightClick(m, 20, row)
	if m.menu == nil || len(m.menu.items) != 3 || m.recentCursor != 1 {
		t.Fatalf("recent menu = %+v cursor %d", m.menu, m.recentCursor)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Open chat", "Resume in new tab", "Refresh"} {
		if !strings.Contains(view, want) {
			t.Errorf("menu missing %q", want)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) != 100 {
			t.Fatalf("menu overlay changed row width to %d", ansi.StringWidth(line))
		}
	}
	// Click "Open chat" (first entry, one row below the top border).
	origin := m.menu.origin(m.width, m.height)
	next, cmd := pressCmd(m, origin.x+3, origin.y+1)
	if next.menu != nil || cmd == nil || !next.chat.open {
		t.Fatalf("menu entry did not run: menu=%v chat=%v", next.menu, next.chat.open)
	}
	// Clicking elsewhere closes the menu without acting.
	closed, _ := pressCmd(m, 0, m.height-1)
	if closed.menu != nil || closed.chat.open {
		t.Fatal("outside click did not just close the menu")
	}
}

func TestMenuKeyboardAndEdges(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 100, 30
	m = rightClick(m, 99, 29) // bottom-right corner must stay on screen
	origin := m.menu.origin(m.width, m.height)
	if origin.x+m.menu.width() > m.width || origin.y+len(m.menu.items)+2 > m.height {
		t.Fatalf("menu off screen at %+v", origin)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) != 100 {
			t.Fatal("menu overlay changed a row width")
		}
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if next.(model).menu.cursor != 1 {
		t.Fatal("down did not move the menu cursor")
	}
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(model).menu != nil {
		t.Fatal("esc did not close the menu")
	}
}

func TestDragSelectionCopiesAndTrimsFrames(t *testing.T) {
	var copied string
	previous := copyToClipboard
	copyToClipboard = func(text string) error { copied = text; return nil }
	t.Cleanup(func() { copyToClipboard = previous })

	m := dashboardTestModel()
	m.width, m.height = 140, 40
	m.processCursor = 1
	row := rowOf(t, m, "SESSION "+testCopilotSessionID)
	plain := strings.Split(ansi.Strip(m.screen()), "\n")[row]
	start := strings.Index(plain, "SESSION")
	startCol := ansi.StringWidth(plain[:start])

	m = press(m, startCol, row)
	m = motion(m, startCol+len("SESSION "+testCopilotSessionID)-1, row)
	if view := m.View(); !strings.Contains(view, "\x1b[0;7m") {
		t.Fatal("selection not highlighted")
	}
	next, cmd := release(m, startCol+len("SESSION "+testCopilotSessionID)-1, row)
	if cmd == nil {
		t.Fatal("release did not copy")
	}
	result := cmd().(copyResultMsg)
	done, _ := next.Update(result)
	if copied != "SESSION "+testCopilotSessionID {
		t.Fatalf("copied %q", copied)
	}
	if !strings.Contains(done.(model).notice, "copied") {
		t.Fatalf("notice = %q", done.(model).notice)
	}
	// Any key clears the selection and the notice.
	cleared, _ := done.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cleared.(model).notice != "" || cleared.(model).hasSelection() {
		t.Fatal("key press kept the selection or notice")
	}
}

func TestClickWithoutDragCopiesNothing(t *testing.T) {
	fakeClock(t)
	m := dashboardTestModel()
	m.width, m.height = 140, 40
	m = press(m, 70, 30)
	next, cmd := release(m, 70, 30)
	if cmd != nil || next.hasSelection() {
		t.Fatal("plain click selected or copied text")
	}
}

func TestDoubleClickSelectsTokenAndExtendsByWords(t *testing.T) {
	now := fakeClock(t)
	var copied string
	previous := copyToClipboard
	copyToClipboard = func(text string) error { copied = text; return nil }
	t.Cleanup(func() { copyToClipboard = previous })

	m := dashboardTestModel()
	m.width, m.height = 140, 40
	m.processCursor = 1
	row := rowOf(t, m, "title Copilot metadata support")
	plain := strings.Split(ansi.Strip(m.screen()), "\n")[row]
	at := func(word string) int { return ansi.StringWidth(plain[:strings.Index(plain, word)]) }

	m = press(m, at("Copilot")+2, row)
	*now = now.Add(80 * time.Millisecond)
	m = press(m, at("Copilot")+2, row)
	next, cmd := release(m, at("Copilot")+2, row)
	if cmd == nil {
		t.Fatal("double click did not copy on release")
	}
	cmd()
	if copied != "Copilot" {
		t.Fatalf("double click copied %q, want the token", copied)
	}

	// Hold the second press and drag across a later word: whole words only.
	m = press(next, at("metadata")+1, row)
	*now = now.Add(80 * time.Millisecond)
	m = press(m, at("metadata")+1, row)
	m = motion(m, at("support")+2, row)
	_, cmd = release(m, at("support")+2, row)
	cmd()
	if copied != "metadata support" {
		t.Fatalf("word drag copied %q", copied)
	}
}

func TestSelectionTextAndWordAt(t *testing.T) {
	rows := []string{
		"╭─panel──────╮",
		"│ first line │",
		"│ second     │",
		"╰────────────╯",
	}
	if got := selectionText(rows, cell{0, 0}, cell{14, 3}); got != "panel\nfirst line\nsecond" {
		t.Fatalf("selectionText = %q", got)
	}
	if got := selectionText(rows, cell{2, 1}, cell{7, 1}); got != "first" {
		t.Fatalf("partial selection = %q", got)
	}
	if span, ok := wordAt("│ foo-bar baz │", 3); !ok || span.start != 2 || span.end != 9 {
		t.Fatalf("wordAt = %+v %v", span, ok)
	}
	if _, ok := wordAt("│ foo │", 0); ok {
		t.Fatal("border selected as a token")
	}
	if span, ok := wordAt("日本語 x", 1); !ok || span.start != 0 || span.end != 6 {
		t.Fatalf("wide token = %+v %v", span, ok)
	}
}

func TestHelpClosesOnClickAndTooSmallIgnoresMouse(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 100, 30
	m.helpOpen = true
	if next := press(m, 5, 5); next.helpOpen {
		t.Fatal("click did not close help")
	}
	m.helpOpen = false
	m.width, m.height = 10, 4
	if next := press(m, 1, 1); next.mouse.dragging {
		t.Fatal("mouse acted on a too-small terminal")
	}
}

func codexHistorySessionForTest(i int) codexHistoricalSession {
	return codexHistoricalSession{
		ID: "codex-session-" + string(rune('a'+i)), Title: "title", WorkDir: "/workspace/proj",
		Tokens: 1000, UpdatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
	}
}
