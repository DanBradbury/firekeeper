package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/version"
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

func TestDashboardHeaderShowsCountsWithoutRefreshTime(t *testing.T) {
	m := dashboardTestModel()
	m.width = 120

	header := ansi.Strip(m.dashHeaderLine())
	if strings.Contains(header, "09:30:00") {
		t.Fatalf("header shows refresh time: %q", header)
	}
	for _, status := range []string{"1●", "1▲"} {
		if !strings.Contains(header, status) {
			t.Errorf("header missing status count %q: %q", status, header)
		}
	}
}

func TestVersionInBottomRight(t *testing.T) {
	originalVersion := version.Version
	t.Cleanup(func() { version.Version = originalVersion })

	views := []struct {
		name string
		tab  tab
		help bool
		chat bool
		key  string
	}{
		{"processes", processesTab, false, false, "↑↓"},
		{"overview", overviewTab, false, false, "r refresh"},
		{"usage", usageTab, false, false, "↑↓"},
		{"chat", processesTab, false, true, "↑↓"},
		{"help", processesTab, true, false, "any key"},
	}
	for _, currentVersion := range []string{"dev", "0.8.5", "0.10.1"} {
		version.Version = currentVersion
		for _, view := range views {
			for _, width := range []int{20, 40, 80, 140} {
				t.Run(fmt.Sprintf("%s/%s/%d", currentVersion, view.name, width), func(t *testing.T) {
					m := dashboardTestModel()
					m.width, m.height = width, 40
					m.activeTab, m.helpOpen, m.chat.open = view.tab, view.help, view.chat
					m.codexUsage.History = []codexHistoricalSession{{ID: "test-session", WorkDir: "/workspace/project"}}

					lines := strings.Split(m.View(), "\n")
					header := ansi.Strip(lines[0])
					for _, text := range []string{"firekeeper", "agent monitor", " " + currentVersion + " "} {
						if strings.Contains(header, text) {
							t.Errorf("header contains %q: %q", text, header)
						}
					}
					footer := lines[len(lines)-1]
					if got := ansi.StringWidth(footer); got != width {
						t.Errorf("footer width = %d, want %d", got, width)
					}
					if !strings.HasSuffix(footer, dashPaint(currentVersion, dashGreen, true)+dashPaint(" ", usageTextColor, false)) {
						t.Errorf("version missing right padding or green styling: %q", footer)
					}
					if plain := ansi.Strip(footer); !strings.Contains(plain, view.key) {
						t.Errorf("footer missing %q: %q", view.key, plain)
					}
					if view.help {
						plain := ansi.Strip(footer)
						if !strings.Contains(plain, "any key close") && !strings.Contains(plain, "any key back") {
							t.Errorf("footer missing help-close hint: %q", plain)
						}
					}
					if width >= 80 && view.name == "processes" {
						plain := ansi.Strip(footer)
						for _, hint := range []string{"↑↓ select", "c chat", "? help", "q quit"} {
							if !strings.Contains(plain, hint) {
								t.Errorf("footer missing %q: %q", hint, plain)
							}
						}
						if !strings.Contains(plain, "tab switch") && !strings.Contains(plain, "tab next") {
							t.Errorf("footer missing tab hint: %q", plain)
						}
						if !strings.Contains(plain, "r refresh") && !strings.Contains(plain, "r reload") {
							t.Errorf("footer missing refresh hint: %q", plain)
						}
					}
				})
			}
		}
	}
}

func TestLongVersionKeepsRoomForShortcuts(t *testing.T) {
	originalVersion := version.Version
	t.Cleanup(func() { version.Version = originalVersion })
	version.Version = "0.8.5-development-build"

	m := dashboardTestModel()
	m.width, m.height = 20, 6
	lines := strings.Split(m.View(), "\n")
	footer := lines[len(lines)-1]
	label := ansi.Truncate(version.Version, m.width/2, "…")
	if got := ansi.StringWidth(footer); got != m.width {
		t.Errorf("footer width = %d, want %d", got, m.width)
	}
	if !strings.HasSuffix(footer, dashPaint(label, dashGreen, true)+dashPaint(" ", usageTextColor, false)) {
		t.Errorf("truncated version missing right padding or green styling: %q", footer)
	}
	if !strings.Contains(ansi.Strip(footer), "↑↓") {
		t.Errorf("long version displaced shortcuts: %q", ansi.Strip(footer))
	}
}

func TestDashboardShowsOperationalMessagesInSessionsPanel(t *testing.T) {
	tests := []struct {
		name     string
		set      func(*model)
		message  string
		noGroups bool
	}{
		{"scan failure", func(m *model) { m.processErr = "ps unavailable" }, "process scan failed: ps unavailable", false},
		{"scan failure without results", func(m *model) {
			m.processErr = "ps unavailable"
			m.processGroups = nil
		}, "process scan failed: ps unavailable", true},
		{"terminal switch", func(m *model) { m.terminalStatus = "terminal switch failed: permission denied" }, "terminal switch failed: permission denied", false},
		{"metadata warning", func(m *model) { m.processMetadataWarning = "timed out" }, "session metadata unavailable: timed out", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := dashboardTestModel()
			m.width, m.height = 120, 40
			tt.set(&m)
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			body := strings.Join(lines[:len(lines)-1], "\n")
			if !strings.Contains(body, tt.message) {
				t.Errorf("missing operational message %q", tt.message)
			}
			if tt.noGroups && strings.Contains(body, "No running Codex") {
				t.Error("scan failure was presented as an empty process list")
			}
			if !tt.noGroups && !strings.Contains(body, "Dashboard first pass") {
				t.Error("operational message hid the existing sessions")
			}
			if !strings.Contains(lines[len(lines)-1], "? help") {
				t.Error("operational message replaced the shortcuts")
			}
		})
	}
}

func TestDashboardOperationalMessagesFitAndSanitize(t *testing.T) {
	for _, size := range [][2]int{{20, 6}, {40, 12}, {80, 24}} {
		m := dashboardTestModel()
		m.width, m.height = size[0], size[1]
		m.processErr = "failed\nagain\x1b[31m"
		m.terminalStatus = "switch status"
		m.processMetadataWarning = "metadata warning"
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) != m.height {
			t.Fatalf("%v: rows = %d, want %d", size, len(lines), m.height)
		}
		for i, line := range lines {
			if width := ansi.StringWidth(line); width != m.width {
				t.Fatalf("%v: line %d width = %d, want %d", size, i, width, m.width)
			}
		}
		if !strings.Contains(ansi.Strip(view), "process scan") || strings.Contains(view, "\x1b[31m") {
			t.Errorf("%v: scan failure was hidden or contained an unsafe escape", size)
		}
		if strings.Contains(ansi.Strip(view), "switch status") || strings.Contains(ansi.Strip(view), "metadata warning") {
			t.Errorf("%v: lower-priority feedback replaced the scan failure", size)
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
		{"processes", processesTab, codexProvider, false, "[ Processes ]", []string{"↑↓ select", "c chat", "r refresh"}},
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
				body := strings.Join(lines[1:len(lines)-1], "\n")
				for _, hint := range []string{"tab switch", "tab next", "? help", "←/→ provider", "runtimes /", "refreshed "} {
					if strings.Contains(body, hint) {
						t.Errorf("footer-only text %q appears above the footer", hint)
					}
				}
				footer := lines[len(lines)-1]
				for _, hint := range append([]string{"? help", "q quit"}, tt.footerHints...) {
					if !strings.Contains(footer, hint) {
						t.Errorf("footer missing %q: %q", hint, footer)
					}
				}
				if !strings.Contains(footer, "tab switch") && !(tt.tab == processesTab && strings.Contains(footer, "tab next")) {
					t.Errorf("footer missing tab hint: %q", footer)
				}
				if tt.tab == processesTab {
					if !strings.Contains(footer, "s switch terminal") && !(width == 80 && strings.Contains(footer, "s terminal")) {
						t.Errorf("footer missing terminal hint: %q", footer)
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
			if footer := lines[len(lines)-1]; !strings.Contains(footer, tt.footer) {
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
		"[ Processes ]", "summary", "providers", "projects", "sessions (3)",
		"Codex", "Copilot", "OpenCode", "Copilot metadata support", "▲ NEEDS INPUT", "● ACTIVE",
		"1.2M", "019fb33c", "/workspace/firekeeper", "session metadata unavailable",
		"SESSION " + testCopilotSessionID, "repo DanBradbury/api", "feature/x",
		"Sessions 2", "1 active", "1 input",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "overview") {
		t.Fatal("Processes panel still collides with the Overview tab name")
	}
}

func TestDashboardProcessRowsKeepDirectoryOnOneLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := dashboardTestModel()
	m.width, m.height = 200, 40
	m.processGroups[0].sessions = append(m.processGroups[0].sessions, sessionInfo{id: "another-session"})
	m.processGroups[1].sessions[0].name = ""
	m.processGroups[1].sessions[0].cwd = ""

	header := ansi.Strip(dashHeaderRow(m.dashFitColumns(m.dashInnerWidth()), m.dashInnerWidth()))
	for _, column := range []string{"Project", "Session", "Directory"} {
		if !strings.Contains(header, column) {
			t.Errorf("missing column %q in %q", column, header)
		}
	}
	for _, column := range []string{"Summary", "Title", "Branch"} {
		if strings.Contains(header, column) {
			t.Errorf("unexpected column %q in %q", column, header)
		}
	}

	lines, offsets := m.processBodyLines()
	if len(lines) != len(m.processGroups) {
		t.Fatalf("collapsed runtimes use %d lines, want %d", len(lines), len(m.processGroups))
	}
	for index, line := range lines {
		if offsets[index] != index {
			t.Errorf("runtime %d starts at line %d, want %d", index, offsets[index], index)
		}
		if strings.Contains(line, "\n") || ansi.StringWidth(line) != m.dashInnerWidth() {
			t.Errorf("runtime %d does not fit on one line", index)
		}
	}
	for _, expected := range []string{"Codex", "019fb33c +1", "/workspace/firekeeper"} {
		if !strings.Contains(ansi.Strip(lines[0]), expected) {
			t.Errorf("first runtime row missing %q: %q", expected, ansi.Strip(lines[0]))
		}
	}
	for _, unwanted := range []string{"Dashboard first pass", "main", "+1 sessions"} {
		if strings.Contains(ansi.Strip(lines[0]), unwanted) {
			t.Errorf("first runtime row contains %q: %q", unwanted, ansi.Strip(lines[0]))
		}
	}
	if row := ansi.Strip(lines[1]); !strings.Contains(row, "—") || strings.Contains(row, "feature/x") {
		t.Errorf("missing directory is not shown as unavailable: %q", row)
	}
	if row := ansi.Strip(lines[2]); !strings.Contains(row, "session metadata unavailable") || strings.Contains(row, "ttys003") || strings.Contains(row, "1 proc") {
		t.Errorf("unexpected missing-metadata fallback: %q", row)
	}
	directoryColumn := []dashColumn{{key: "directory", width: 48}}
	for index, want := range []string{"/workspace/firekeeper", "—", "session metadata unavailable"} {
		row := dashGroupRow(m.processGroups[index], directoryColumn, 51, false, false, usagePanelColor)
		if got := strings.TrimSpace(ansi.Strip(row)); got != want {
			t.Errorf("runtime %d directory = %q, want %q", index, got, want)
		}
	}
}

func TestDashboardDirectoryFitsLongAndUnsafeValues(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 80, 24
	m.processGroups[0].sessions[0].cwd = "/workspace/a/very/long/directory/for/the/project\nunexpected\x1b[31m"
	m.processGroups[0].sessions[0].gitBranch = "feature\x1b[31m"
	lines, _ := m.processBodyLines()
	row := lines[0]
	if strings.Contains(row, "\n") || strings.Contains(row, "\x1b[31m") {
		t.Fatal("directory data escaped the runtime row")
	}
	if plain := ansi.Strip(row); strings.Contains(plain, "feature") || !strings.Contains(plain, "/workspace/") || !strings.Contains(plain, "…") {
		t.Errorf("long directory included a branch or was not truncated: %q", plain)
	}
}

func TestDashboardKeepsRuntimeIdentifiersBesideDirectory(t *testing.T) {
	for _, width := range []int{100, 120, 140} {
		m := dashboardTestModel()
		m.width, m.height = width, 40
		header := ansi.Strip(dashHeaderRow(m.dashFitColumns(m.dashInnerWidth()), m.dashInnerWidth()))
		for _, column := range []string{"Project", "Session", "Directory"} {
			if !strings.Contains(header, column) {
				t.Errorf("width %d: missing column %q in %q", width, column, header)
			}
		}
		if strings.Contains(header, "PID") {
			t.Errorf("width %d: PID column present in %q", width, header)
		}
		lines, _ := m.processBodyLines()
		row := ansi.Strip(lines[0])
		for _, value := range []string{"firekeeper", "019fb33c", "/workspace/firekeeper"} {
			if !strings.Contains(row, value) {
				t.Errorf("width %d: missing %q in %q", width, value, row)
			}
		}
	}
}

func TestDashboardSessionCountFitsBesideIdentifier(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 100, 40
	m.processGroups[0].sessions = append(m.processGroups[0].sessions, make([]sessionInfo, 12)...)
	lines, _ := m.processBodyLines()
	row := ansi.Strip(lines[0])
	for _, value := range []string{"019fb33c +12", "/workspace/firekeeper"} {
		if !strings.Contains(row, value) {
			t.Errorf("missing %q in %q", value, row)
		}
	}
}

func TestDashboardDetailsUseSingleLocationAcrossResize(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 140, 24
	id := m.processGroups[0].sessions[0].id
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.expandedGroups[100] || !strings.Contains(ansi.Strip(m.processFooter()), "enter expand") {
		t.Fatal("small terminals lost inline expansion")
	}
	lines, _ := m.processBodyLines()
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "SESSION "+id) {
		t.Fatal("inline details missing when the bottom panel is hidden")
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = updated.(model)
	lines, _ = m.processBodyLines()
	table := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(table, "SESSION ") || strings.Contains(table, "PROCESSES (") || strings.Contains(table, "▾") {
		t.Fatal("middle panel duplicates the bottom panel's details")
	}
	if count := strings.Count(ansi.Strip(m.View()), "SESSION "+id); count != 1 {
		t.Fatalf("session details shown %d times, want once", count)
	}
	if strings.Contains(ansi.Strip(m.processFooter()), "enter expand") {
		t.Fatal("footer advertises inline expansion while the bottom panel is visible")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.expandedGroups[100] {
		t.Fatal("Enter changed inline expansion while the bottom panel is visible")
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	m = updated.(model)
	lines, _ = m.processBodyLines()
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "SESSION "+id) {
		t.Fatal("shrinking the terminal did not restore inline details")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	lines, _ = m.processBodyLines()
	if strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "SESSION "+id) {
		t.Fatal("Enter did not collapse inline details on the smaller terminal")
	}
}

func TestProviderBarsRequireMultipleProviders(t *testing.T) {
	tests := []struct {
		name  string
		tools []dashToolSummary
		bar   bool
	}{
		{"none", nil, false},
		{"single", []dashToolSummary{{tool: "Codex", runtimes: 2, tokens: 1200}}, false},
		{"single without tokens", []dashToolSummary{{tool: "Codex", runtimes: 2}}, false},
		{"multiple", []dashToolSummary{{tool: "Codex", runtimes: 2, tokens: 1200}, {tool: "Copilot", runtimes: 1, tokens: 600}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, width := range []int{24, 40, 80} {
				summary := dashSummary{tools: tt.tools}
				lines := dashBox("providers", summary.providerLines(width-4), width, 5, dashPurple)
				view := ansi.Strip(strings.Join(lines, "\n"))
				if strings.Contains(view, "━") != tt.bar {
					t.Fatalf("width %d: unexpected provider bar visibility", width)
				}
				for _, line := range lines {
					if got := ansi.StringWidth(line); got != width {
						t.Fatalf("provider panel width = %d, want %d", got, width)
					}
				}
				if len(tt.tools) == 1 {
					for _, text := range []string{"Codex", "  2", formatTokenCount(tt.tools[0].tokens)} {
						if !strings.Contains(view, text) {
							t.Errorf("single provider lost %q", text)
						}
					}
				}
			}
		})
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
	if strings.Contains(view, "providers") || strings.Contains(view, "Up ") {
		t.Fatalf("narrow dashboard kept wide-only content:\n%s", view)
	}
	if !strings.Contains(view, "Directory") || !strings.Contains(view, "/workspace/firekeeper") {
		t.Fatalf("narrow dashboard lost the session directory:\n%s", view)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Dashboard first pass") {
		t.Fatalf("inline details lost the session title:\n%s", view)
	}
}

func TestDashboardSelectionScrollsWithinSessionsPanel(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 80, 6
	for range 2 {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	if m.processCursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.processCursor)
	}
	if m.processScroll != 2 {
		t.Fatalf("scroll = %d, want 2", m.processScroll)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "▶ OpenCode") {
		t.Fatalf("selected runtime not visible:\n%s", view)
	}
}

func TestDashboardSelectionScrollsPastExpandedRuntime(t *testing.T) {
	m := dashboardTestModel()
	m.width, m.height = 100, 9
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	_, offsets := m.processBodyLines()
	if offsets[1] <= 1 {
		t.Fatal("expanded details did not move the next runtime")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "▶ Copilot") {
		t.Fatalf("selected runtime after expanded details is not visible:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.processScroll != 0 {
		t.Fatalf("returning to expanded runtime left scroll at %d", m.processScroll)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	_, offsets = m.processBodyLines()
	if offsets[1] != 1 {
		t.Fatalf("collapsed next runtime starts at line %d, want 1", offsets[1])
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
		"tokens (firekeeper/019fb33c)", "Total  1.2M", "Input", "50.0K", "Output", "200.0K",
		"Cache read", "900.0K", "Cache write", "100.0K",
		"tokens 1.2M (in 50.0K · out 200.0K · cache read 900.0K · cache write 100.0K)",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}

	m.processCursor = 1
	view = ansi.Strip(m.View())
	for _, expected := range []string{"tokens (api/dc315f47)", "Total  321", "No input/output/cache breakdown"} {
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

func TestIdleDashboard(t *testing.T) {
	for _, width := range []int{20, 80, 140} {
		m := dashboardTestModel()
		m.processGroups = nil
		m.width, m.height = width, 24
		view := ansi.Strip(m.View())
		for _, unwanted := range []string{"╭", "Select a", "s switch", "c chat", "PID", "0 active"} {
			if strings.Contains(view, unwanted) {
				t.Errorf("empty dashboard contains %q", unwanted)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatal("empty dashboard exceeds width")
			}
		}
		if width >= 80 {
			for _, want := range []string{"No agent sessions running", "Scanning every 2s", "last scan", "Start codex", "Overview / Usage"} {
				if !strings.Contains(view, want) {
					t.Errorf("missing %q", want)
				}
			}
		}
	}
}

func TestIdleDashboardScanStates(t *testing.T) {
	m := dashboardTestModel()
	m.processGroups = nil
	m.width, m.height = 100, 24
	m.refreshedAt = time.Time{}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "first scan pending") || strings.Contains(view, "No agent sessions running") {
		t.Fatal("initial scan presented as completed empty discovery")
	}
	m.processErr = "ps unavailable"
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "process scan failed: ps unavailable") || strings.Contains(view, "No agent sessions running") {
		t.Fatal("failed scan presented as empty discovery")
	}
}

func TestIdleDashboardPaintsCenteredPadding(t *testing.T) {
	for _, state := range []string{"idle", "pending", "error", "metadata warning", "terminal status"} {
		t.Run(state, func(t *testing.T) {
			m := dashboardTestModel()
			m.processGroups = nil
			m.width, m.height = 140, 24
			switch state {
			case "pending":
				m.refreshedAt = time.Time{}
			case "error":
				m.processErr = "ps unavailable"
			case "metadata warning":
				m.processMetadataWarning = "metadata unavailable"
			case "terminal status":
				m.terminalStatus = "terminal unavailable"
			}
			for _, line := range strings.Split(m.viewIdleProcesses(m.height-chromeRows), "\n") {
				plain := ansi.Strip(line)
				if strings.TrimSpace(plain) == "" {
					continue
				}
				padding := len(plain) - len(strings.TrimLeft(plain, " "))
				if padding == 0 {
					continue
				}
				want := dashPaint(strings.Repeat(" ", padding), usageTextColor, false)
				if !strings.HasPrefix(line, want) {
					t.Fatalf("centered padding does not use the dashboard background: %q", line)
				}
			}
		})
	}
}

func idleRecentModel(width, height int) model {
	m := dashboardTestModel()
	m.processGroups = nil
	m.width, m.height = width, height
	now := time.Now()
	for i, tool := range []string{"Copilot", "Codex", "Claude", "Gemini", "Kimi", "Cursor", "OpenCode"} {
		m.recentSessions = append(m.recentSessions, recentSession{
			tool: tool, id: fmt.Sprintf("session-%d-abcdef", i), project: "proj" + tool,
			title: "title " + tool, last: now.Add(-time.Duration(i+1) * time.Hour),
		})
	}
	return m
}

func TestIdleDashboardRecentSessions(t *testing.T) {
	for _, size := range [][2]int{{20, 8}, {80, 24}, {140, 40}, {80, 10}} {
		m := idleRecentModel(size[0], size[1])
		view := ansi.Strip(m.View())
		lines := strings.Split(view, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%v: rows = %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("%v: line exceeds width: %q", size, line)
			}
		}
		if size[0] >= 80 && size[1] >= 24 {
			for _, want := range []string{"Recent sessions", "▸ Copilot", "Codex", "1h", "enter chat"} {
				if !strings.Contains(view, want) {
					t.Errorf("%v: missing %q:\n%s", size, want, view)
				}
			}
		}
	}
}

func TestIdleRecentSelectionScrollsAndOpensChat(t *testing.T) {
	m := idleRecentModel(80, 14)
	for range 6 {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(model)
	}
	if m.recentCursor != 6 || !strings.Contains(ansi.Strip(m.View()), "▸ OpenCode") {
		t.Fatalf("cursor %d not visible:\n%s", m.recentCursor, ansi.Strip(m.View()))
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if next.(model).recentCursor != 6 {
		t.Fatal("cursor moved past the last session")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	opened := next.(model)
	if cmd == nil || !opened.chat.open || opened.chat.tool != "OpenCode" || opened.chat.meta.ID != "session-6-abcdef" {
		t.Fatalf("enter did not open the selected session: %+v", opened.chat)
	}
}

func TestRecentSessionsLoadWhileIdleOnly(t *testing.T) {
	m := NewModel()
	if cmd := m.maybeLoadRecent(); cmd == nil || !m.recentLoading {
		t.Fatal("idle model did not start a load")
	}
	if m.maybeLoadRecent() != nil {
		t.Fatal("second load started while one is running")
	}
	m.applyRecentSessions(recentSessionsMsg{loadedAt: time.Now()})
	if m.maybeLoadRecent() != nil {
		t.Fatal("reload started inside the refresh interval")
	}
	m.recentLoadedAt = time.Now().Add(-time.Hour)
	m.processGroups = dashboardTestModel().processGroups
	if m.maybeLoadRecent() != nil {
		t.Fatal("load started while sessions are running")
	}
}

func TestSortRecentSessionsNewestFirst(t *testing.T) {
	now := time.Now()
	list := []recentSession{{id: "a", last: now.Add(-2 * time.Hour)}, {id: "b", last: now}, {id: "c", last: now.Add(-time.Hour)}}
	sortRecentSessions(list)
	if list[0].id != "b" || list[1].id != "c" || list[2].id != "a" {
		t.Fatalf("order = %v", list)
	}
}

func TestProjectColumnGrowsForLongNames(t *testing.T) {
	m := dashboardTestModel()
	m.processGroups = m.processGroups[:1]
	m.processGroups[0].sessions[0].cwd = "/workspace/firekeeper-server-extended"
	m.width, m.height = 160, 30
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "firekeeper-server-extended") {
		t.Fatalf("project name truncated at width 160:\n%s", view)
	}
	m.width = 80
	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
}

func TestModelColumnFitsLongModelNames(t *testing.T) {
	m := dashboardTestModel()
	m.processGroups = m.processGroups[:1]
	m.processGroups[0].sessions[0].model = "claude-sonnet-5-5"
	for _, width := range []int{120, 140} {
		m.width, m.height = width, 30
		if view := ansi.Strip(m.View()); !strings.Contains(view, "claude-sonnet-5-5") {
			t.Errorf("width %d: model truncated:\n%s", width, view)
		}
	}
}
