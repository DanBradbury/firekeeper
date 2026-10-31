package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/DanBradbury/firekeeper/internal/otelrecv"
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

func TestShortcutHintsStayInFooter(t *testing.T) {
	tests := []struct {
		name        string
		tab         tab
		provider    usageProvider
		withHistory bool
		header      string
		footerHints []string
	}{
		{"processes", processesTab, codexProvider, false, "[ Processes ]", []string{"↑↓ select", "enter expand", "c chat", "r refresh"}},
		{"overview", overviewTab, codexProvider, false, "[ Overview ]", []string{"r refresh"}},
		{"usage codex", usageTab, codexProvider, false, "[ Usage ]", []string{"←/→ provider", "r refresh"}},
		{"usage copilot", usageTab, copilotProvider, false, "[ Usage ]", []string{"←/→ provider", "r refresh"}},
		{"usage kimi", usageTab, kimiProvider, false, "[ Usage ]", []string{"←/→ provider", "r refresh"}},
		{"usage claude", usageTab, claudeProvider, false, "[ Usage ]", []string{"←/→ provider", "r refresh"}},
		{"usage with history", usageTab, codexProvider, true, "[ Usage ]", []string{"↑↓ session", "enter chat", "←/→ provider", "r refresh"}},
	}
	for _, tt := range tests {
		for _, width := range []int{80, 140} {
			t.Run(fmt.Sprintf("%s/%d", tt.name, width), func(t *testing.T) {
				m := dashboardTestModel()
				m.width, m.height = width, 40
				m.activeTab = tt.tab
				m.usageProvider = tt.provider
				if tt.withHistory {
					m.codexUsage.History = []codexHistoricalSession{{ID: "test-session", WorkDir: "/workspace/project"}}
				}
				lines := strings.Split(ansi.Strip(m.View()), "\n")
				if len(lines) != m.height {
					t.Fatalf("rows = %d, want %d", len(lines), m.height)
				}
				if !strings.Contains(lines[0], tt.header) {
					t.Errorf("header missing active tab %q: %q", tt.header, lines[0])
				}
				if tt.tab == usageTab {
					selected := "[ " + strings.ToUpper(m.usageProviderName()) + " ]"
					if !strings.Contains(lines[1], selected) {
						t.Errorf("provider selector missing %q: %q", selected, lines[1])
					}
				}
				body := strings.Join(lines[:len(lines)-2], "\n")
				for _, hint := range []string{"tab switch", "? help", "←/→ provider"} {
					if strings.Contains(body, hint) {
						t.Errorf("shortcut %q appears above the footer", hint)
					}
				}
				footer := lines[len(lines)-2]
				for _, hint := range append([]string{"tab switch", "? help", "q quit"}, tt.footerHints...) {
					if !strings.Contains(footer, hint) {
						t.Errorf("footer missing %q: %q", hint, footer)
					}
				}
				if tt.tab == processesTab {
					terminalHint := "s terminal"
					if width == 140 {
						terminalHint = "s switch terminal"
					}
					if !strings.Contains(footer, terminalHint) {
						t.Errorf("footer missing %q: %q", terminalHint, footer)
					}
				}
			})
		}
	}
}

func TestOverlayHeadersDoNotShowShortcutHints(t *testing.T) {
	for _, tt := range []struct {
		name   string
		help   bool
		chat   bool
		footer string
	}{
		{"help", true, false, "any key close"},
		{"chat", false, true, "esc back"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := dashboardTestModel()
			m.width, m.height = 100, 40
			m.helpOpen, m.chat.open = tt.help, tt.chat
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			for _, hint := range []string{"tab switch", "? help"} {
				if strings.Contains(lines[0], hint) {
					t.Errorf("header contains %q: %q", hint, lines[0])
				}
			}
			if footer := lines[len(lines)-2]; !strings.Contains(footer, tt.footer) {
				t.Errorf("footer missing %q: %q", tt.footer, footer)
			}
		})
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

func TestDashboardShowsSubagentUndercountWarning(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 160, 44
	m.processGroups[0].sessions[0].undercountWarning = session.ClaudeUndercountWarning
	view := ansi.Strip(m.View())
	for _, expected := range []string{"1.2M", "900.0K", session.ClaudeUndercountWarning} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "est.") || strings.Contains(view, "hidden summary calls, est") {
		t.Fatalf("dashboard still shows an estimate:\n%s", view)
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
	for _, active := range []tab{processesTab, overviewTab, usageTab} {
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
	if got := sessionTokenTotal(sessionInfo{exactTokens: 371, tokens: tokenBreakdown{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}}); got != 371 {
		t.Fatalf("exact total = %d", got)
	}
}

func TestDashboardShowsTelemetryAndDropsUndercountWarning(t *testing.T) {
	dir := t.TempDir()
	id := "33333333-3333-4333-8333-333333333333"
	path := filepath.Join(dir, id+".jsonl")
	if err := os.MkdirAll(filepath.Join(dir, id, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id, "subagents", "agent-1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	group := func() []processGroup {
		return []processGroup{{
			tool:      "Claude",
			root:      processInfo{pid: 9, tty: "ttys009", elapsed: "00:10"},
			processes: []processInfo{{pid: 9, ppid: 1, tty: "ttys009", elapsed: "00:10", command: "claude"}},
			sessions: []sessionInfo{{
				id: id, name: "Haiku sleeps", state: sessionStateActive, cwd: "/work",
				rolloutPath: path, tokensUsed: 300,
				tokens: tokenBreakdown{Input: 230, Output: 50, CacheRead: 20},
			}},
		}}
	}
	warned := annotateClaudeTelemetry(group(), nil)
	if warned[0].sessions[0].undercountWarning == "" || !warned[0].sessions[0].telemetryHint {
		t.Fatalf("without telemetry: %+v", warned[0].sessions[0])
	}
	sup := map[string]otelrecv.Supplement{id: {
		Covered: true, Transcript: 300, Added: 71, Exact: 371,
		BySource: map[string]int64{"agent_summary": 45, "prompt_suggestion": 24, "generate_session_title": 2},
	}}
	exact := annotateClaudeTelemetry(group(), sup)
	session := exact[0].sessions[0]
	if session.undercountWarning != "" || session.telemetryHint || session.exactTokens != 371 {
		t.Fatalf("with telemetry: %+v", session)
	}
	if !strings.Contains(session.telemetryLine, "Background calls (not in transcripts):") || !strings.Contains(session.telemetryLine, "summaries") || !strings.Contains(session.telemetryLine, "prompt suggestions") || !strings.Contains(session.telemetryLine, "titles") {
		t.Fatalf("line = %q", session.telemetryLine)
	}
	m := NewModel()
	m.width, m.height = 160, 44
	m.processGroups = exact
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Background calls (not in transcripts):") || strings.Contains(view, "Has subagents:") {
		t.Fatalf("detail view:\n%s", view)
	}
	if !strings.Contains(view, "transcripts") || !strings.Contains(view, "371") {
		t.Fatalf("exact total missing:\n%s", view)
	}
}
