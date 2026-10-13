package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func dashboardTestModel() model {
	m := newModel()
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
		"[ Processes ]", "¹overview", "²providers", "³projects", "sessions (3)",
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

func TestDashboardDropsColumnsAndPanelsWhenNarrow(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 60, 20
	view := ansi.Strip(m.View())
	if strings.Contains(view, "²providers") || strings.Contains(view, "Up ") {
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
