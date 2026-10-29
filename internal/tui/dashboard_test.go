package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func dashboardTestModel() model {
	m := NewModel()
	m.refreshedAt = time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	m.processGroups = []processGroup{
		{
			tool:      "Codex",
			root:      processInfo{pid: 100, tty: "ttys001", elapsed: "01:23"},
			processes: []processInfo{{pid: 100, ppid: 1, tty: "ttys001", elapsed: "01:23", command: "codex"}},
			sessions: []sessionInfo{{
				id: "019fb33c-c5f4-75f3-b987-228eb484c6ec", name: "Dashboard first pass",
				state: sessionStateActive, cwd: "/workspace/firekeeper", model: "gpt-5.6",
				gitBranch: "main", tokensUsed: 1_250_000,
				tokens: tokenBreakdown{Input: 50_000, Output: 200_000, CacheRead: 900_000, CacheWrite: 100_000},
			}},
		},
		{
			tool:      "Copilot",
			root:      processInfo{pid: 200, tty: "ttys002", elapsed: "00:42"},
			processes: []processInfo{{pid: 200, ppid: 1, tty: "ttys002", elapsed: "00:42", command: "copilot"}},
			sessions: []sessionInfo{{
				id: testCopilotSessionID, name: "Copilot metadata support", state: sessionStateNeedsInput,
				cwd: "/workspace/api", repository: "DanBradbury/api", gitBranch: "feature/x", tokensUsed: 321,
			}},
		},
		{
			tool:      "OpenCode",
			root:      processInfo{pid: 300, tty: "ttys003", elapsed: "00:05"},
			processes: []processInfo{{pid: 300, ppid: 1, tty: "ttys003", elapsed: "00:05", command: "opencode"}},
		},
	}
	return m
}

func TestDashboardFitsTerminalAtEverySize(t *testing.T) {
	for _, size := range [][2]int{{20, 6}, {40, 12}, {80, 24}, {120, 40}, {200, 60}} {
		m := dashboardTestModel()
		m.width, m.height = size[0], size[1]
		m.expandedGroups[100] = true
		m.clampProcessSelection()
		lines := strings.Split(m.View(), "\n")
		if len(lines) != m.height {
			t.Fatalf("%v: rows = %d, want %d", size, len(lines), m.height)
		}
		for index, line := range lines {
			if width := ansi.StringWidth(line); width != m.width {
				t.Fatalf("%v: line %d width = %d, want %d", size, index, width, m.width)
			}
		}
	}
}

func TestDashboardShowsSessionsSummaryAndDetail(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 140, 40
	m.processCursor = 1
	view := ansi.Strip(m.View())
	for _, expected := range []string{
		"[ Processes ]", "¹overview", "³providers", "⁴projects", "sessions (3)",
		"Codex", "Copilot", "OpenCode", "Dashboard first pass", "▲ NEEDS INPUT", "● ACTIVE",
		"1.2M", "019fb33c", "└─ main · /workspace/firekeeper", "session metadata unavailable",
		"SESSION " + testCopilotSessionID, "repo DanBradbury/api", "feature/x",
		"Sessions 2", "1 active", "1 input",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
}

func TestDashboardShowsSummaryEstimateSeparate(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 160, 44
	m.processGroups[0].sessions[0].agentSummary = &session.AgentSummaryEstimate{
		Calls: 5, Tokens: 31000, CacheRead: 31000, IntervalSeconds: 30,
		Source: session.AgentSummarySourceTranscript,
		Label:  "~5 hidden summary calls, est. 31000 tokens",
	}
	view := ansi.Strip(m.View())
	for _, expected := range []string{
		"1.2M", "900.0K",
		"~5 hidden summary calls, est. 31.0K tokens",
		"estimate only, not in the measured total",
		"server may set 30-240s",
		"~5 hidden, est. 31.0K",
		"est. only · 30s (30-240s)",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
}

func TestDashboardDropsColumnsAndPanelsWhenNarrow(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 60, 20
	view := ansi.Strip(m.View())
	if strings.Contains(view, "³providers") || strings.Contains(view, "Up ") {
		t.Fatalf("narrow dashboard kept wide-only content:\n%s", view)
	}
	if !strings.Contains(view, "Dashboard first") {
		t.Fatalf("narrow dashboard lost the session summary:\n%s", view)
	}
}

func TestDashboardSelectionScrollsWithinSessionsPanel(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 80, 12
	for range 2 {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	if m.processCursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.processCursor)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "▶ OpenCode") {
		t.Fatalf("selected runtime not visible:\n%s", view)
	}
}

func TestHelpOverlayTogglesAndListsHotkeys(t *testing.T) {
	for _, active := range []tab{processesTab, usageTab} {
		m := dashboardTestModel()
		m.width, m.height = 100, 40
		m.activeTab = active
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
		m = updated.(model)
		if !m.helpOpen {
			t.Fatal("? did not open help")
		}
		view := ansi.Strip(m.View())
		for _, expected := range []string{"Global", "Processes", "Usage", "Switch to the runtime's terminal", "Switch provider", "Quit"} {
			if !strings.Contains(view, expected) {
				t.Fatalf("help missing %q:\n%s", expected, view)
			}
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
		if m.helpOpen || m.processCursor != 0 {
			t.Fatal("a key must close help without running its action")
		}
	}
}

func TestHelpOverlayClosesOnAnyKeyWithoutActing(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyRunes, Runes: []rune{'?'}},
		{Type: tea.KeyRunes, Runes: []rune{'r'}},
		{Type: tea.KeyRunes, Runes: []rune{'s'}},
		{Type: tea.KeyTab},
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
	}
	for _, key := range keys {
		m := dashboardTestModel()
		m.helpOpen = true
		updated, cmd := m.Update(key)
		m = updated.(model)
		if m.helpOpen {
			t.Fatalf("%q did not close help", key.String())
		}
		if cmd != nil || m.activeTab != processesTab || m.expandedGroups[100] || m.terminalStatus != "" {
			t.Fatalf("%q ran its action behind the help overlay", key.String())
		}
	}
	m := dashboardTestModel()
	m.helpOpen = true
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("ctrl+c must still quit from help")
	}
}

func TestHelpOverlayFitsSmallTerminals(t *testing.T) {
	for _, size := range [][2]int{{20, 6}, {40, 12}, {80, 24}} {
		m := dashboardTestModel()
		m.width, m.height = size[0], size[1]
		m.helpOpen = true
		lines := strings.Split(m.View(), "\n")
		if len(lines) != m.height {
			t.Fatalf("%v: rows = %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != m.width {
				t.Fatalf("%v: line width = %d", size, ansi.StringWidth(line))
			}
		}
	}
}

func TestDashboardShowsSelectedSessionTokenBreakdown(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 140, 40
	view := ansi.Strip(m.View())
	for _, expected := range []string{
		"²tokens (firekeeper/019fb33c)", "Total  1.2M", "Input", "50.0K", "Output", "200.0K",
		"Cache read", "900.0K", "Cache write", "100.0K",
		"tokens 1.2M (in 50.0K · out 200.0K · cache read 900.0K · cache write 100.0K)",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}

	m.processCursor = 1
	view = ansi.Strip(m.View())
	for _, expected := range []string{"²tokens (api/dc315f47)", "Total  321", "No input/output/cache breakdown"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
}

func TestSessionTokenTotalPrefersBreakdown(t *testing.T) {
	if got := sessionTokenTotal(sessionInfo{tokensUsed: 99}); got != 99 {
		t.Fatalf("fallback total = %d", got)
	}
	if got := sessionTokenTotal(sessionInfo{tokensUsed: 99, tokens: tokenBreakdown{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}}); got != 10 {
		t.Fatalf("breakdown total = %d", got)
	}
}
