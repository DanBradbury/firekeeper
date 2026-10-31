package tui

import (
	"strings"
	"testing"

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
		{"initial failure", func(m *model) { m.codexUsageErr = "failed\nagain\x1b[31m" }, "unavailable"},
		{"cached usage", func(m *model) {
			m.codexUsage.Daily = []codexDailyUsage{{StartDate: "2026-10-31", Tokens: 100}}
			m.codexUsageErr = "failed\nagain\x1b[31m"
		}, "Usage refresh"},
		{"empty history", func(m *model) {
			m.codexHistoryErr = "failed\nagain\x1b[31m"
		}, "Session history"},
		{"cached history", func(m *model) {
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
