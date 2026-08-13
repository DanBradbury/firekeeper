package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestReadCodexUsageResponses(t *testing.T) {
	responses := strings.Join([]string{
		`{"id":0,"result":{"userAgent":"firekeeper/0.1.0"}}`,
		`{"method":"account/rateLimits/updated","params":{}}`,
		`{"id":1,"result":{"rateLimits":{"limitId":"codex"},"rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":25,"windowDurationMins":10080,"resetsAt":1730947200},"planType":"plus"}},"rateLimitResetCredits":{"availableCount":2}}}`,
		`{"id":2,"result":{"summary":{"lifetimeTokens":1234567,"peakDailyTokens":45678,"longestRunningTurnSec":540,"currentStreakDays":8,"longestStreakDays":14},"dailyUsageBuckets":[{"startDate":"2026-06-18","tokens":12345}]}}`,
	}, "\n")

	snapshot, err := readCodexUsageResponses(strings.NewReader(responses))
	if err != nil {
		t.Fatal(err)
	}
	limit := snapshot.RateLimits["codex"]
	if limit.Primary == nil || limit.Primary.UsedPercent != 25 || limit.Primary.WindowDurationMin != 10080 {
		t.Fatalf("rate limit = %#v", limit)
	}
	if snapshot.ResetCredits != 2 {
		t.Fatalf("reset credits = %d, want 2", snapshot.ResetCredits)
	}
	if snapshot.Summary.LifetimeTokens == nil || *snapshot.Summary.LifetimeTokens != 1234567 {
		t.Fatalf("lifetime tokens = %#v", snapshot.Summary.LifetimeTokens)
	}
	if len(snapshot.Daily) != 1 || snapshot.Daily[0].Tokens != 12345 {
		t.Fatalf("daily usage = %#v", snapshot.Daily)
	}
}

func TestCodexUsageViewShowsQuotaAndTokenSummary(t *testing.T) {
	lifetime := int64(248676162)
	peak := int64(25915360)
	longest := int64(31788)
	currentStreak := int64(6)
	longestStreak := int64(9)
	m := testModel()
	m.activeTab = usageTab
	m.width = 100
	m.height = 24
	now := time.Now()
	m.codexUsage = codexUsageSnapshot{
		RateLimits: map[string]codexRateLimit{
			"codex": {
				LimitID:  "codex",
				PlanType: "plus",
				Primary: &codexQuotaWindow{
					UsedPercent:       32,
					WindowDurationMin: 10080,
					ResetsAt:          now.Add(4*24*time.Hour + 17*time.Hour).Unix(),
				},
			},
		},
		ResetCredits: 1,
		Summary: codexUsageSummary{
			LifetimeTokens:        &lifetime,
			PeakDailyTokens:       &peak,
			LongestRunningTurnSec: &longest,
			CurrentStreakDays:     &currentStreak,
			LongestStreakDays:     &longestStreak,
		},
		Daily: []codexDailyUsage{{StartDate: now.Format("2006-01-02"), Tokens: 14556919}},
	}
	m.processGroups = []processGroup{{
		tool: "Codex",
		sessions: []sessionInfo{
			{id: "session-1", model: "gpt-5.6-sol", tokensUsed: 9812345},
			{id: "session-2", model: "gpt-5.6-codex", tokensUsed: 4100000},
		},
	}}
	m.codexUsageRefreshedAt = time.Now()

	view := m.View()
	for _, expected := range []string{
		"[ Usage ]",
		"[ CODEX ]",
		"╭────╮  Codex",
		"PLUS  •  248.7M TOKENS  •  1 RESET",
		"LIMITS",
		"Weekly",
		"32%",
		"Resets in 4d 17h",
		"TOKENS BY DAY",
		"Today",
		"14.6M",
		"ACTIVE TOKENS BY MODEL",
		"GPT 5.6 SOL",
		"9.8M",
		"GPT 5.6 CODEX",
		"4.1M",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Codex usage view missing %q", expected)
		}
	}
}

func TestColorModelUsageAssignsStableDistinctColors(t *testing.T) {
	models := make([]modelUsage, 10)
	for index := range models {
		models[index] = modelUsage{Model: fmt.Sprintf("model-%d", index), Tokens: int64(100 - index)}
	}
	colored := colorModelUsage(models)
	used := make(map[string]bool, len(colored))
	for _, model := range colored {
		if model.Color == "" {
			t.Fatalf("model colors = %#v, want colors", colored)
		}
		if used[model.Color] {
			t.Fatalf("model color %q repeated: %#v", model.Color, colored)
		}
		used[model.Color] = true
	}
	if len(used) != 10 || len(modelUsageColors) != 10 {
		t.Fatalf("unique colors/palette = %d/%d, want 10/10", len(used), len(modelUsageColors))
	}
	reversed := append([]modelUsage(nil), models...)
	slices.Reverse(reversed)
	again := colorModelUsage(reversed)
	byModel := make(map[string]string, len(colored))
	for _, model := range colored {
		byModel[model.Model] = model.Color
	}
	for _, model := range again {
		if model.Color != byModel[model.Model] {
			t.Fatalf("color for %q changed from %q to %q after reorder", model.Model, byModel[model.Model], model.Color)
		}
	}
}

func TestModelColorAssignmentsStayConsistentAcrossOverviewSections(t *testing.T) {
	assignments := modelUsageColorAssignments([]string{"gpt-5", "claude-opus", "gpt-5", "claude-sonnet"})
	legend := colorModelUsageWithAssignments([]modelUsage{{Model: "gpt-5"}, {Model: "claude-opus"}}, assignments)
	daily := colorModelUsageWithAssignments([]modelUsage{{Model: "claude-opus"}, {Model: "gpt-5"}}, assignments)
	if legend[0].Color != daily[1].Color || legend[1].Color != daily[0].Color {
		t.Fatalf("overview colors changed between sections: legend=%#v daily=%#v", legend, daily)
	}
	if legend[0].Color == legend[1].Color {
		t.Fatalf("overview models share color: %#v", legend)
	}
}

func TestScanCodexRolloutModelUsageTracksTimestampedTokenDeltas(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	rollout := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol"}}}`,
		`{"timestamp":"2026-08-08T10:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":100}}}}`,
		`{"timestamp":"2026-08-08T10:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":140}}}}`,
		`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-codex"}}}`,
		`{"timestamp":"2026-08-09T10:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":200}}}}`,
	}, "\n")
	usage, err := scanCodexRolloutModelUsage(strings.NewReader(rollout), now, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int64)
	for _, entry := range usage {
		got[entry.StartDate+"/"+entry.Model] = entry.Tokens
	}
	if got["2026-08-08/gpt-5.6-sol"] != 140 {
		t.Fatalf("day-one model usage = %#v, want 140", got)
	}
	if got["2026-08-09/gpt-5.6-codex"] != 60 {
		t.Fatalf("day-two model usage = %#v, want 60", got)
	}
}

func TestCodexUsageViewUsesModelSplitDailyBars(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.width = 100
	m.height = 24
	today := time.Now().Format("2006-01-02")
	m.codexUsage = codexUsageSnapshot{DailyByModel: []codexDailyModelUsage{
		{StartDate: today, Model: "gpt-5.6-sol", Tokens: 900},
		{StartDate: today, Model: "gpt-5.6-codex", Tokens: 100},
	}}
	view := m.View()
	if !strings.Contains(view, "TOKENS BY DAY · MODEL") {
		t.Fatalf("model daily heading missing:\n%s", view)
	}
	colors := modelUsageColorAssignments([]string{"gpt-5.6-sol", "gpt-5.6-codex"})
	for _, color := range colors {
		if !strings.Contains(view, "38;2;"+color) {
			t.Fatalf("model color %s missing:\n%s", color, view)
		}
	}
}

func TestRecentCodexDailyUsageFillsMissingDays(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	days := recentCodexDailyUsage([]codexDailyUsage{
		{StartDate: "2026-07-27", Tokens: 2100000},
		{StartDate: "2026-07-31", Tokens: 23800000},
	}, now, 7)
	if len(days) != 7 || days[0].StartDate != "2026-07-25" || days[6].StartDate != "2026-07-31" {
		t.Fatalf("daily range = %#v", days)
	}
	if days[2].Tokens != 2100000 || days[5].Tokens != 0 || days[6].Tokens != 23800000 {
		t.Fatalf("daily values = %#v", days)
	}
}

func TestReadCodexHistoricalSessionsFromLocalState(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 unavailable")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	statePath := filepath.Join(home, "state_5.sqlite")
	schema := `
		CREATE TABLE threads (
			id TEXT PRIMARY KEY,
			title TEXT,
			cwd TEXT,
			model TEXT,
			source TEXT,
			git_branch TEXT,
			updated_at INTEGER,
			tokens_used INTEGER,
			has_user_event INTEGER,
			archived INTEGER
		);
		INSERT INTO threads VALUES ('older', 'Older session', '/tmp/older', 'gpt-old', 'cli', 'main', 100, 20, 0, 1);
		INSERT INTO threads VALUES ('newer', 'Newer session', '/tmp/newer', 'gpt-new', 'app', 'feature', 200, 40, 0, 0);
		INSERT INTO threads VALUES ('empty', 'No user event', '/tmp/empty', '', '', '', 300, 0, 0, 0);
	`
	if output, err := exec.Command(sqlite, statePath, schema).CombinedOutput(); err != nil {
		t.Fatalf("create Codex state fixture: %v: %s", err, output)
	}

	sessions, err := readCodexHistoricalSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || sessions[0].ID != "empty" || sessions[1].ID != "newer" || sessions[2].ID != "older" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if sessions[1].Title != "Newer session" || sessions[1].GitBranch != "feature" || sessions[1].Tokens != 40 {
		t.Fatalf("newer session = %#v", sessions[1])
	}
	if sessions[2].UpdatedAt.Unix() != 100 || sessions[2].Archived != 1 {
		t.Fatalf("older session metadata = %#v", sessions[2])
	}
}

func TestCodexHistoryQuerySupportsPartialSchema(t *testing.T) {
	query, err := codexHistoryQuery(map[string]bool{
		"id":         true,
		"title":      true,
		"updated_at": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"NULLIF(title, '')", "NULLIF(updated_at, 0) * 1000", "0 AS tokens_used"} {
		if !strings.Contains(query, expected) {
			t.Fatalf("partial-schema query missing %q:\n%s", expected, query)
		}
	}
	for _, absent := range []string{"NULLIF(thread_source", "NULLIF(git_branch", "WHERE COALESCE(has_user_event"} {
		if strings.Contains(query, absent) {
			t.Fatalf("partial-schema query referenced missing %q:\n%s", absent, query)
		}
	}
	if _, err := codexHistoryQuery(map[string]bool{"title": true}); err == nil {
		t.Fatal("schema without id column was accepted")
	}
}

func TestDecodeCodexHistoricalSessionsSanitizesPartialRows(t *testing.T) {
	sessions, err := decodeCodexHistoricalSessions([]byte(`[{"id":"session-1","display_name":"Build\u001b[31m feature","updated_at_ms":1000}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || strings.Contains(sessions[0].Title, "\x1b") || sessions[0].UpdatedAt.UnixMilli() != 1000 {
		t.Fatalf("sessions = %#v", sessions)
	}
	if _, err := decodeCodexHistoricalSessions([]byte(`{malformed`)); err == nil {
		t.Fatal("malformed history JSON was accepted")
	}
}

func TestCodexUsageEnterBrowsesHistoricalSessions(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.usageProvider = codexProvider
	m.width = 100
	m.codexUsage.History = []codexHistoricalSession{
		{ID: "one", Title: "First session", Model: "gpt-5.6-sol", WorkDir: "/work/one"},
		{ID: "two", Title: "Second session", Model: "gpt-5.6-codex", WorkDir: "/work/two"},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.codexHistoryOpen {
		t.Fatal("Enter did not open Codex history")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.codexHistoryCursor != 1 || !strings.Contains(m.viewCodexHistory(7), "Second session") {
		t.Fatalf("history cursor/view = %d\n%s", m.codexHistoryCursor, m.viewCodexHistory(7))
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.codexHistoryOpen {
		t.Fatal("Esc did not close Codex history")
	}
}

func TestCodexUsageEnterShowsEmptyHistoryState(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	m.usageProvider = codexProvider
	m.width = 100
	m.codexHistoryErr = "state database unavailable"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.codexHistoryOpen {
		t.Fatal("Enter did not open empty Codex history")
	}
	if view := m.viewCodexHistory(8); !strings.Contains(view, "Session history unavailable") {
		t.Fatalf("empty history did not explain failure:\n%s", view)
	}
}

func TestCodexHistoryRefreshFailureRetainsLastGoodSessions(t *testing.T) {
	m := testModel()
	m.codexUsage.History = []codexHistoricalSession{{ID: "known"}}
	updated, _ := m.Update(codexUsageResultMsg{
		snapshot:   codexUsageSnapshot{Daily: []codexDailyUsage{{StartDate: "2026-08-12", Tokens: 1}}},
		refreshed:  time.Now(),
		historyErr: errTestCodexUsage,
	})
	m = updated.(model)
	if len(m.codexUsage.History) != 1 || m.codexUsage.History[0].ID != "known" {
		t.Fatal("failed history refresh discarded last good sessions")
	}
	if m.codexHistoryErr == "" {
		t.Fatal("failed history refresh did not expose error")
	}
}

func TestRefreshKeyRequestsCodexUsage(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab

	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	if command == nil || !m.codexUsageLoading {
		t.Fatal("R did not start Codex usage refresh")
	}
}

func TestCodexUsageErrorRetainsLastGoodSnapshot(t *testing.T) {
	m := testModel()
	m.codexUsage = codexUsageSnapshot{Daily: []codexDailyUsage{{StartDate: "2026-07-31", Tokens: 10}}}

	updated, _ := m.Update(codexUsageResultMsg{err: errTestCodexUsage, refreshed: time.Now()})
	m = updated.(model)
	if len(m.codexUsage.Daily) != 1 || m.codexUsage.Daily[0].Tokens != 10 {
		t.Fatal("failed refresh discarded last good Codex usage snapshot")
	}
	if m.codexUsageErr == "" {
		t.Fatal("failed refresh did not expose error")
	}
}

var errTestCodexUsage = &codexUsageTestError{}

type codexUsageTestError struct{}

func (*codexUsageTestError) Error() string { return "temporary failure" }
