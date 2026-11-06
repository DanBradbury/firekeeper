package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestUsageFitStatKeepsNameAndFloatsValueRight(t *testing.T) {
	line := usageStat("Cache write", "2.2M", "")
	for _, inner := range []int{30, 18} {
		got := ansi.Strip(usageFitStat(line, inner))
		if !strings.HasPrefix(got, "Cache write") {
			t.Fatalf("inner %d: name truncated: %q", inner, got)
		}
		if ansi.StringWidth(got) != inner {
			t.Fatalf("inner %d: width = %d, want %d: %q", inner, ansi.StringWidth(got), inner, got)
		}
		if !strings.HasSuffix(got, "2.2M") {
			t.Fatalf("inner %d: value not flush right: %q", inner, got)
		}
	}
}

func TestUsageFitStatTruncatesNameBeforeValue(t *testing.T) {
	got := ansi.Strip(usageFitStat(usageStat("Cache write", "2.2M", ""), 12))
	if ansi.StringWidth(got) != 12 || !strings.HasSuffix(got, " 2.2M") || !strings.Contains(got, "…") {
		t.Fatalf("narrow row = %q, want truncated name and whole value", got)
	}
}

func TestUsageCardsFloatValuesRight(t *testing.T) {
	const inner = 36
	rows := map[string][]string{
		"models": usageModelCardLines([]modelUsage{{Model: "claude-sonnet-5-5", Tokens: 59900000}}, 5, ""),
		"projects": usageProjectLines([]usageSession{
			{WorkDir: "/w/game-sync", Tokens: 673800000},
			{WorkDir: "/w/other", Tokens: 1500},
		}, 5),
		"copilot plan": copilotPlanLines(copilotUsageSnapshot{Plan: copilotPlanDetails{Available: true, Name: "pro", UsedCredits: 481, TotalCredits: 7000}}),
	}
	for name, lines := range rows {
		for _, line := range lines {
			got := ansi.Strip(usageFitStat(line, inner))
			if strings.Contains(line, usageStatGap) && ansi.StringWidth(got) != inner {
				t.Errorf("%s: width = %d, want %d: %q", name, ansi.StringWidth(got), inner, got)
			}
		}
	}
}

func TestUsageFooterMatchesProcessesFooterStyle(t *testing.T) {
	m := model{width: 100, height: 30, activeTab: usageTab}
	usageHelp := m.usageFooter()
	processHelp := m.processFooter()
	if got := ansi.StringWidth(usageHelp); got != m.width {
		t.Errorf("usage help width = %d, want %d", got, m.width)
	}
	if !strings.Contains(usageHelp, dashPaint("tab ", dashBlue, true)) {
		t.Errorf("usage help keys are not painted like the Processes footer: %q", usageHelp)
	}
	if len(usageHelp) == 0 || len(processHelp) == 0 {
		t.Fatal("empty footer")
	}
}

func TestUsageShowsRefreshErrorsWithCachedData(t *testing.T) {
	for _, provider := range []usageProvider{codexProvider, copilotProvider, kimiProvider, claudeProvider} {
		m := NewModel()
		m.width, m.height = 120, 30
		m.activeTab, m.usageProvider = usageTab, provider
		m.codexUsage.Daily = []codexDailyUsage{{StartDate: "2026-10-31", Tokens: 100}}
		m.copilotUsage.Summary.Sessions = 1
		m.kimiUsage.Summary.Sessions = 1
		m.claudeUsage.Summary.Sessions = 1
		m.codexUsageErr, m.copilotUsageErr = "temporary failure", "temporary failure"
		m.kimiUsageErr, m.claudeUsageErr = "temporary failure", "temporary failure"

		view := ansi.Strip(m.View())
		if !strings.Contains(view, "Usage refresh failed: temporary failure") {
			t.Errorf("provider %d: refresh error not visible", provider)
		}
		if !strings.Contains(view, "╭─lifetime") {
			t.Errorf("provider %d: cached usage not visible", provider)
		}
	}
}

func TestUsageShowsHistoryErrorsWithAndWithoutCachedSessions(t *testing.T) {
	for _, provider := range []usageProvider{codexProvider, copilotProvider} {
		for _, height := range []int{12, 19, 20, 40} {
			for _, cached := range []bool{false, true} {
				m := NewModel()
				m.width, m.height = 120, height
				m.activeTab, m.usageProvider = usageTab, provider
				m.codexUsage.Daily = []codexDailyUsage{{StartDate: "2026-10-31", Tokens: 100}}
				m.copilotUsage.Summary.Sessions = 1
				m.codexHistoryErr, m.copilotHistoryErr = "database unavailable", "database unavailable"
				if cached {
					m.codexUsage.History = []codexHistoricalSession{{ID: "test-session", Title: "cached session", WorkDir: "/workspace/project"}}
					m.copilotUsage.History = []copilotHistoricalSession{{ID: "test-session", Title: "cached session", WorkDir: "/workspace/project"}}
				}
				view := ansi.Strip(m.View())
				message := "Session history unavailable: database unavailable"
				if cached {
					message = "Session history refresh failed: database unavailable"
				}
				if count := strings.Count(view, message); count != 1 {
					t.Errorf("provider %d height %d cached %v: history error shown %d times", provider, height, cached, count)
				}
				if cached && height == 40 && !strings.Contains(view, "cached session") {
					t.Errorf("provider %d: cached session not visible", provider)
				}
			}
		}
	}
}

func TestUsageErrorsFitAndSanitize(t *testing.T) {
	tests := []struct {
		name string
		set  func(*model)
		want string
	}{
		{"initial failure", func(m *model) { m.codexUsageErr = "failed\nagain\x1b[31m" }, "No providers"},
		{"cached usage", func(m *model) {
			m.codexUsage.Daily = []codexDailyUsage{{StartDate: "2026-10-31", Tokens: 100}}
			m.codexUsageErr = "failed\nagain\x1b[31m"
		}, "Usage refresh"},
		{"empty history", func(m *model) {
			withUsageData(m, codexProvider)
			m.codexHistoryErr = "failed\nagain\x1b[31m"
		}, "Session history"},
		{"cached history", func(m *model) {
			withUsageData(m, codexProvider)
			m.codexUsage.History = []codexHistoricalSession{{ID: "test-session"}}
			m.codexHistoryErr = "failed\nagain\x1b[31m"
		}, "Session history"},
	}
	for _, tt := range tests {
		for _, size := range [][2]int{{20, 6}, {40, 12}, {80, 24}} {
			m := NewModel()
			m.width, m.height = size[0], size[1]
			m.activeTab = usageTab
			tt.set(&m)
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != m.height {
				t.Fatalf("%s %v: rows = %d, want %d", tt.name, size, len(lines), m.height)
			}
			for i, line := range lines {
				if width := ansi.StringWidth(line); width != m.width {
					t.Fatalf("%s %v: line %d width = %d, want %d", tt.name, size, i, width, m.width)
				}
			}
			if !strings.Contains(ansi.Strip(view), tt.want) || strings.Contains(view, "\x1b[31m") {
				t.Errorf("%s %v: error was hidden or contained an unsafe escape", tt.name, size)
			}
		}
	}
}

func TestUsageFitStatPaintsGapWithPanelBackground(t *testing.T) {
	got := usageFitStat(usageStat("Output", "374.9K", ""), 30)
	gap := dashPaint(strings.Repeat(" ", 30-len("Output")-len("374.9K")), usageTextColor, false)
	if !strings.Contains(got, gap) {
		t.Fatalf("gap between name and value is not painted: %q", got)
	}
}

// withUsageData gives each provider the least usage that makes the Usage tab
// list it.
func withUsageData(m *model, providers ...usageProvider) {
	if len(providers) == 0 {
		providers = []usageProvider{codexProvider, copilotProvider, kimiProvider, claudeProvider}
	}
	for _, provider := range providers {
		switch provider {
		case codexProvider:
			lifetime := int64(1)
			m.codexUsage.Summary.LifetimeTokens = &lifetime
		case copilotProvider:
			m.copilotUsage.Summary.Sessions = 1
		case kimiProvider:
			m.kimiUsage.Summary.Sessions = 1
		case claudeProvider:
			m.claudeUsage.Summary.Sessions = 1
		}
	}
}

func TestUsageTabWithoutProvidersSaysSo(t *testing.T) {
	withIconMode(t, iconsOff)
	m := NewModel()
	m.width, m.height = 100, 20
	m.activeTab = usageTab
	m.codexUsageErr = "Codex executable not found"
	m.claudeUsageErr = "find Claude Code session history: stat projects: no such file"
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "No providers found.") {
		t.Fatalf("empty state missing:\n%s", view)
	}
	for _, unwanted := range []string{"CODEX", "COPILOT", "KIMI", "CLAUDE", "executable", "unavailable", "no such file"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("view shows %q for a missing provider:\n%s", unwanted, view)
		}
	}
	m.kimiUsageLoading = true
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Looking for providers…") {
		t.Fatalf("loading state missing:\n%s", view)
	}
	// Arrows do nothing with no provider to move to.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if cmd != nil || updated.(model).usageProvider != m.usageProvider {
		t.Fatal("arrow moved to a provider that is not listed")
	}
}

func TestUsageTabListsAndCyclesOnlyProvidersWithData(t *testing.T) {
	withIconMode(t, iconsOff)
	m := NewModel()
	m.width, m.height = 120, 30
	m.activeTab = usageTab
	m.usageProvider = codexProvider
	m.codexUsageErr = "Codex executable not found"
	withUsageData(&m, copilotProvider, claudeProvider)
	m.copilotUsageRefreshedAt, m.claudeUsageRefreshedAt = time.Now(), time.Now()

	view := ansi.Strip(m.View())
	selector := strings.Split(view, "\n")[1]
	if !strings.Contains(selector, "[ COPILOT ]") || !strings.Contains(selector, "CLAUDE") ||
		strings.Contains(selector, "CODEX") || strings.Contains(selector, "KIMI") {
		t.Fatalf("selector = %q", selector)
	}
	if strings.Contains(view, "executable") {
		t.Fatalf("failed provider's error shown:\n%s", view)
	}

	// A result moves the selection off a provider with nothing to show.
	updated, _ := m.Update(kimiUsageResultMsg{refreshed: time.Now(), err: errors.New("no Kimi")})
	m = updated.(model)
	if m.usageProvider != copilotProvider {
		t.Fatalf("selection = %d, want Copilot", m.usageProvider)
	}
	for _, want := range []usageProvider{claudeProvider, copilotProvider} {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = updated.(model)
		if m.usageProvider != want {
			t.Fatalf("Right selected %d, want %d", m.usageProvider, want)
		}
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m = updated.(model); m.usageProvider != claudeProvider {
		t.Fatalf("Left selected %d, want Claude", m.usageProvider)
	}

	// The second chip is Claude, not Copilot's neighbour in the full list.
	next, _ := pressCmd(m, 1+len("COPILOT")+5+3, 1)
	if next.usageProvider != claudeProvider {
		t.Fatalf("second chip selected %d, want Claude", next.usageProvider)
	}
	back, _ := pressCmd(next, 3, 1)
	if back.usageProvider != copilotProvider {
		t.Fatalf("first chip selected %d, want Copilot", back.usageProvider)
	}
}

func TestEnteringUsageTabRefreshesEveryProvider(t *testing.T) {
	m := NewModel()
	m.activeTab = processesTab
	cmd := m.switchTab(usageTab)
	if cmd == nil || !m.codexUsageLoading || !m.copilotUsageLoading || !m.kimiUsageLoading || !m.claudeUsageLoading {
		t.Fatalf("entering Usage did not refresh every provider: %+v", m)
	}
	// r with nothing listed looks for every provider again.
	m = NewModel()
	m.activeTab = usageTab
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	if cmd == nil || !m.codexUsageLoading || !m.copilotUsageLoading || !m.kimiUsageLoading || !m.claudeUsageLoading {
		t.Fatal("r did not refresh every provider when none is listed")
	}
	// r with a listed provider refreshes only that one.
	m = NewModel()
	m.activeTab = usageTab
	withUsageData(&m, claudeProvider)
	m.usageProvider = claudeProvider
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	if !m.claudeUsageLoading || m.codexUsageLoading || m.copilotUsageLoading || m.kimiUsageLoading {
		t.Fatal("r refreshed providers other than the selected one")
	}
}

// With no session on screen, a metadata warning is about a hidden runtime
// (such as lsof finding nothing for an idle Codex server) and is not shown.
func TestIdleProcessesHideMetadataWarning(t *testing.T) {
	m := dashboardTestModel()
	m.processGroups = nil
	m.width, m.height = 120, 24
	m.processMetadataWarning = "Codex: run lsof: exit status 1"
	view := ansi.Strip(m.View())
	if strings.Contains(view, "session metadata unavailable") || strings.Contains(view, "lsof") {
		t.Fatalf("idle view shows a metadata warning:\n%s", view)
	}
	if !strings.Contains(view, "No agent sessions running") {
		t.Fatalf("idle message missing:\n%s", view)
	}
	// A scan failure is still reported.
	m.processErr = "ps unavailable"
	if view := ansi.Strip(m.View()); !strings.Contains(view, "process scan failed: ps unavailable") {
		t.Fatalf("scan failure hidden:\n%s", view)
	}
}
