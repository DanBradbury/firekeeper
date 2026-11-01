package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
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
		"╭─claude", "Limits: firekeeper claude-limits enable", "today 1.2K",
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
	help := m.overviewFooter()
	if got := ansi.StringWidth(help); got != m.width {
		t.Fatalf("help width = %d, want %d", got, m.width)
	}
}

func TestOverviewShowsClaudeLimitsWhenEnabled(t *testing.T) {
	now := time.Now()
	m := overviewTestModel(now)
	m.claudeUsage.LimitsOn = true
	m.claudeUsage.Limits = claudelimits.Limits{
		FiveHour: &claudelimits.Window{UsedPercent: 64, ResetsAt: now.Add(2*time.Hour + 30*time.Minute)},
		SevenDay: &claudelimits.Window{UsedPercent: 12, ResetsAt: now.Add(4 * 24 * time.Hour)},
	}
	view := ansi.Strip(m.viewOverview(30))
	for _, expected := range []string{"╭─claude", "5-hour", "64%", "↻ 2h", "Weekly", "12%", "↻ 4d"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "claude-limits enable") {
		t.Fatalf("enable hint shown while enabled:\n%s", view)
	}
}

func TestOverviewClaudeLimitsHintAndError(t *testing.T) {
	now := time.Now()
	m := overviewTestModel(now)
	if view := ansi.Strip(m.viewOverview(30)); !strings.Contains(view, "Limits: firekeeper claude-limits enable") {
		t.Fatalf("missing enable hint:\n%s", view)
	}
	m.claudeUsage.LimitsOn = true
	m.claudeUsage.LimitsErr = claudelimits.ErrTokenExpired.Error()
	view := ansi.Strip(m.viewOverview(30))
	if !strings.Contains(view, "Limits unavailable: Claude Code sign-in has expired") || strings.Contains(view, "claude-limits enable") {
		t.Fatalf("error not shown:\n%s", view)
	}
}

func TestClaudeUsageUsageTabLimitsCard(t *testing.T) {
	now := time.Now()
	m := overviewTestModel(now)
	m.activeTab = usageTab
	m.usageProvider = claudeProvider
	m.width, m.height = 140, 40
	m.claudeUsage.LimitsOn = true
	m.claudeUsage.Limits = claudelimits.Limits{SevenDay: &claudelimits.Window{UsedPercent: 88, ResetsAt: now.Add(26 * time.Hour)}}
	view := ansi.Strip(m.viewUsagePage(30))
	for _, expected := range []string{"╭─limits", "Weekly", "88%", "↻ 1d", "╭─lifetime", "╭─models"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("missing %q:\n%s", expected, view)
		}
	}
}

func TestRefreshClaudeUsageAttachesLimitsWithoutFailingOnLimitsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := claudeLimitsSource
	defer func() { claudeLimitsSource = original }()

	claudeLimitsSource = func() (claudelimits.Limits, bool, error) {
		return claudelimits.Limits{FiveHour: &claudelimits.Window{UsedPercent: 5}}, true, nil
	}
	msg := refreshClaudeUsage()().(claudeUsageResultMsg)
	if msg.err != nil || !msg.snapshot.LimitsOn || msg.snapshot.Limits.FiveHour == nil {
		t.Fatalf("limits not attached: %+v err %v", msg.snapshot, msg.err)
	}
	claudeLimitsSource = func() (claudelimits.Limits, bool, error) {
		return claudelimits.Limits{}, true, claudelimits.ErrTokenExpired
	}
	msg = refreshClaudeUsage()().(claudeUsageResultMsg)
	if msg.err != nil || msg.snapshot.LimitsErr == "" || msg.snapshot.Limits.Any() {
		t.Fatalf("limits error must not fail the refresh: %+v err %v", msg.snapshot, msg.err)
	}
	// No Claude Code sign-in is not an error: nothing is shown for it.
	claudeLimitsSource = func() (claudelimits.Limits, bool, error) {
		return claudelimits.Limits{}, true, claudelimits.ErrNoToken
	}
	msg = refreshClaudeUsage()().(claudeUsageResultMsg)
	if msg.err != nil || !msg.snapshot.LimitsOn || msg.snapshot.LimitsErr != "" {
		t.Fatalf("missing sign-in must be silent: %+v", msg.snapshot)
	}
	m := overviewTestModel(time.Now())
	m.claudeUsage.LimitsOn, m.claudeUsage.LimitsErr = msg.snapshot.LimitsOn, msg.snapshot.LimitsErr
	view := ansi.Strip(m.viewOverview(30))
	if strings.Contains(view, "claude-limits enable") || strings.Contains(view, "Limits unavailable") {
		t.Fatalf("overview nags on a machine without a sign-in:\n%s", view)
	}
	claudeLimitsSource = func() (claudelimits.Limits, bool, error) { return claudelimits.Limits{}, false, nil }
	msg = refreshClaudeUsage()().(claudeUsageResultMsg)
	if msg.snapshot.LimitsOn {
		t.Fatal("limits reported on while disabled")
	}
}

func TestOverviewShowsClaudeCloudSyncWarning(t *testing.T) {
	m := overviewTestModel(time.Now())
	if view := ansi.Strip(m.viewOverview(30)); strings.Contains(view, "Cloud sync") {
		t.Fatalf("cloud note shown without a warning:\n%s", view)
	}
	m.claudeUsage.CloudSyncWarn = "claudecloud: the Claude Code login has expired"
	if view := ansi.Strip(m.viewOverview(30)); !strings.Contains(view, "Cloud sync: claudecloud: the Claude Code login") {
		t.Fatalf("cloud warning not shown:\n%s", view)
	}
	original := claudeCloudStatusSource
	defer func() { claudeCloudStatusSource = original }()
	claudeCloudStatusSource = func() string { return "boom" }
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := refreshClaudeUsage()().(claudeUsageResultMsg); msg.snapshot.CloudSyncWarn != "boom" {
		t.Fatalf("snapshot = %+v", msg.snapshot)
	}
}
