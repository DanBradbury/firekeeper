package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func overviewTestModel(now time.Time) model {
	m := NewModel()
	m.width, m.height = 100, 40
	m.activeTab = overviewTab
	today := now.Format("2006-01-02")
	lifetime := int64(5_000_000)
	m.codexUsage = codexUsageSnapshot{
		RateLimits: map[string]codexRateLimit{"codex": {
			LimitID:   "codex",
			Primary:   &codexQuotaWindow{UsedPercent: 42, WindowDurationMin: 300, ResetsAt: now.Add(90 * time.Minute).Unix()},
			Secondary: &codexQuotaWindow{UsedPercent: 85, WindowDurationMin: 10080, ResetsAt: now.Add(50 * time.Hour).Unix()},
		}},
		Daily:   []codexDailyUsage{{StartDate: today, Tokens: 1200}, {StartDate: now.AddDate(0, 0, -3).Format("2006-01-02"), Tokens: 800}, {StartDate: now.AddDate(0, 0, -20).Format("2006-01-02"), Tokens: 9999}},
		Summary: codexUsageSummary{LifetimeTokens: &lifetime},
	}
	m.copilotUsage = copilotUsageSnapshot{
		Plan:    copilotPlanDetails{Available: true, UsedCredits: 300, TotalCredits: 1500, ResetAt: now.Add(10 * 24 * time.Hour)},
		Summary: copilotUsageSummary{Sessions: 3, LifetimeTokens: 70000},
	}
	m.claudeUsage = claudeUsageSnapshot{
		Summary: claudeUsageSummary{Sessions: 2, Messages: 4, LifetimeTokens: 1200},
		Daily:   []claudeDailyUsage{{StartDate: today, Tokens: 1200}},
	}
	return m
}

func TestOverviewShowsQuotaMetersResetsAndTokenTotals(t *testing.T) {
	now := time.Now()
	m := overviewTestModel(now)
	view := ansi.Strip(m.viewOverview(30))
	for _, expected := range []string{
		"╭─codex", "5-hour", "42%", "↻ 1h", "Weekly", "85%", "↻ 2d",
		"today 1.2K", "7d 2.0K", "lifetime 5.0M",
		"╭─copilot", "AI credits", "20%", "300 / 1,500",
		"╭─claude", "Plan limits: Claude Code /usage", "today 1.2K",
		"1 provider(s) with no local usage data are not shown",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("overview missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "╭─kimi") {
		t.Fatalf("provider without data got a panel:\n%s", view)
	}
}

func TestOverviewRecentTokensWindow(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.Local)
	dates := []string{"2026-10-30", "2026-10-24", "2026-10-23", "2026-11-02"}
	today, week := overviewRecentTokens(dates, []int64{10, 20, 40, 80}, now)
	if today != 10 || week != 30 {
		t.Fatalf("today = %d week = %d, want 10 and 30", today, week)
	}
}

func TestOverviewMeterLineIsWidthSafeAndDropsBarWhenNarrow(t *testing.T) {
	now := time.Now()
	meter := overviewMeter{Label: "Weekly", Percent: 85, Detail: "300 / 1,500", ResetAt: now.Add(50 * time.Hour)}
	for _, inner := range []int{16, 30, 50, 96} {
		line := overviewMeterLine(meter, inner, now)
		if ansi.StringWidth(ansi.Truncate(line, inner, "")) > inner {
			t.Fatalf("inner %d overflowed", inner)
		}
		hasBar := strings.Contains(ansi.Strip(line), "█")
		if inner >= 50 && !hasBar {
			t.Fatalf("inner %d: bar missing: %q", inner, ansi.Strip(line))
		}
		if inner <= 30 && hasBar && ansi.StringWidth(line) > inner {
			t.Fatalf("inner %d: bar overflowed", inner)
		}
	}
	if got := ansi.Strip(overviewMeterLine(overviewMeter{Label: "x"}, 60, now)); !strings.Contains(got, "reset unknown") {
		t.Fatalf("zero reset not reported: %q", got)
	}
}

func TestOverviewViewFitsTerminalAtAnySize(t *testing.T) {
	now := time.Now()
	for _, size := range [][2]int{{20, 6}, {40, 10}, {80, 24}, {120, 50}} {
		m := overviewTestModel(now)
		m.width, m.height = size[0], size[1]
		lines := strings.Split(m.View(), "\n")
		if len(lines) != m.height {
			t.Fatalf("%v: rows = %d, want %d", size, len(lines), m.height)
		}
		for _, line := range lines {
			if got := ansi.StringWidth(line); got > m.width {
				t.Fatalf("%v: line width = %d", size, got)
			}
		}
	}
}

func TestOverviewEmptyLoadingAndErrorStates(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.activeTab = overviewTab
	if view := ansi.Strip(m.viewOverview(20)); !strings.Contains(view, "No provider usage found yet.") {
		t.Fatalf("empty state:\n%s", view)
	}
	m.codexUsageLoading = true
	m.claudeUsageErr = "boom"
	view := ansi.Strip(m.viewOverview(20))
	for _, expected := range []string{"╭─codex", "Loading…", "╭─claude", "Unavailable: boom"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("missing %q:\n%s", expected, view)
		}
	}
}

func TestOverviewRefreshSkipsProvidersAlreadyLoading(t *testing.T) {
	m := NewModel()
	m.activeTab = overviewTab
	m.copilotUsageLoading = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	if cmd == nil || !m.codexUsageLoading || !m.claudeUsageLoading || !m.kimiUsageLoading {
		t.Fatalf("r did not refresh the other providers: %+v", m)
	}
	if m.usageProvider != codexProvider {
		t.Fatal("overview refresh must not change the Usage tab's provider")
	}
}

func TestOverviewFooterMatchesFooterWidth(t *testing.T) {
	m := model{width: 100, height: 30, activeTab: overviewTab}
	help, status := m.overviewFooter()
	for name, line := range map[string]string{"help": help, "status": status} {
		if got := ansi.StringWidth(line); got != m.width {
			t.Fatalf("%s width = %d, want %d", name, got, m.width)
		}
	}
}
