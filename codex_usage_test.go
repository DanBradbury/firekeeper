package main

import (
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
	models := []modelUsage{
		{Model: "gpt-5.6-sol", Tokens: 100},
		{Model: "gpt-5.6-codex", Tokens: 80},
	}
	colored := colorModelUsage(models)
	if colored[0].Color == "" || colored[1].Color == "" {
		t.Fatalf("model colors = %#v, want colors", colored)
	}
	if colored[0].Color == colored[1].Color {
		t.Fatalf("model colors = %#v, want distinct colors", colored)
	}
	if again := colorModelUsage([]modelUsage{{Model: "gpt-5.6-codex"}, {Model: "gpt-5.6-sol"}}); again[0].Color != colored[1].Color || again[1].Color != colored[0].Color {
		t.Fatalf("model colors changed between calls: %#v then %#v", colored, again)
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
	for _, color := range []string{modelUsageColor("gpt-5.6-sol"), modelUsageColor("gpt-5.6-codex")} {
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
