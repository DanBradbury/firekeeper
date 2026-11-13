//go:build js

package tui

import (
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

// seedDemoUsage provides only invented in-memory snapshots. Every renderer
// continues to use the same provider adapters as the native dashboard.
func seedDemoUsage(m *model, now time.Time) {
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	threeDaysAgo := now.AddDate(0, 0, -3).Format("2006-01-02")
	codexLifetime, codexPeak, codexStreak, codexBest := int64(2_480_000), int64(91_000), int64(5), int64(14)
	m.codexUsage = codexUsageSnapshot{
		RateLimits: map[string]codexRateLimit{"codex": {
			LimitID: "codex", LimitName: "Codex", PlanType: "demo",
			Primary:   &codexQuotaWindow{UsedPercent: 42, WindowDurationMin: 300, ResetsAt: now.Add(2 * time.Hour).Unix()},
			Secondary: &codexQuotaWindow{UsedPercent: 68, WindowDurationMin: 10080, ResetsAt: now.Add(3 * 24 * time.Hour).Unix()},
		}},
		Summary: codexUsageSummary{LifetimeTokens: &codexLifetime, PeakDailyTokens: &codexPeak, CurrentStreakDays: &codexStreak, LongestStreakDays: &codexBest},
		Daily: []codexDailyUsage{
			{StartDate: today, Tokens: 42_000}, {StartDate: yesterday, Tokens: 31_000}, {StartDate: threeDaysAgo, Tokens: 55_000},
		},
		DailyByModel: []codexDailyModelUsage{
			{StartDate: today, Model: "gpt-5", Tokens: 31_000}, {StartDate: today, Model: "gpt-5-mini", Tokens: 11_000},
			{StartDate: yesterday, Model: "gpt-5", Tokens: 31_000}, {StartDate: threeDaysAgo, Model: "gpt-5", Tokens: 55_000},
		},
		History: []codexHistoricalSession{
			{ID: "demo-codex-ember", Title: "Triage flaky tests", WorkDir: "/demo/ember-api", Model: "gpt-5", GitBranch: "main", Source: "cli", Tokens: 62_000, UpdatedAt: now.Add(-8 * time.Minute)},
			{ID: "demo-codex-atlas", Title: "Review release notes", WorkDir: "/demo/atlas-studio", Model: "gpt-5-mini", GitBranch: "launch", Source: "cli", Tokens: 28_000, UpdatedAt: now.Add(-3 * time.Hour)},
		},
	}
	m.codexUsageRefreshedAt = now

	m.copilotUsage = copilotUsageSnapshot{
		Plan:    copilotPlanDetails{Name: "Demo plan", Available: true, UsedCredits: 140, TotalCredits: 500, ResetAt: now.Add(12 * 24 * time.Hour)},
		Summary: copilotUsageSummary{Sessions: 8, ModelCalls: 132, UserRequests: 54, InputTokens: 340_000, OutputTokens: 170_000, LifetimeTokens: 510_000},
		Daily: []copilotDailyUsage{
			{StartDate: today, Tokens: 18_000, UserRequests: 8}, {StartDate: yesterday, Tokens: 24_000, UserRequests: 10}, {StartDate: threeDaysAgo, Tokens: 32_000, UserRequests: 12},
		},
		DailyByModel: []copilotDailyModelUsage{
			{StartDate: today, Model: "gpt-4.1", Tokens: 12_000}, {StartDate: today, Model: "claude-sonnet", Tokens: 6_000},
			{StartDate: yesterday, Model: "gpt-4.1", Tokens: 24_000}, {StartDate: threeDaysAgo, Model: "claude-sonnet", Tokens: 32_000},
		},
		Models: []copilotUsageModel{{Model: "gpt-4.1", Tokens: 320_000, ModelCalls: 76, UserRequests: 32}, {Model: "claude-sonnet", Tokens: 190_000, ModelCalls: 56, UserRequests: 22}},
		History: []copilotHistoricalSession{
			{ID: "demo-copilot-northwind", Title: "Write onboarding guide", WorkDir: "/demo/northwind-docs", Repository: "northwind-docs", GitBranch: "new-guide", Model: "gpt-4.1", HostType: "cli", LifetimeTokens: 31_000, InputTokens: 21_000, OutputTokens: 10_000, ModelCalls: 12, UserRequests: 5, UpdatedAt: now.Add(-20 * time.Minute)},
			{ID: "demo-copilot-blueprint", Title: "Draft CLI examples", WorkDir: "/demo/blueprint-cli", Repository: "blueprint-cli", GitBranch: "prototype", Model: "claude-sonnet", HostType: "cli", LifetimeTokens: 21_000, InputTokens: 15_000, OutputTokens: 6_000, ModelCalls: 8, UserRequests: 3, UpdatedAt: now.Add(-5 * time.Hour)},
		},
	}
	m.copilotUsageRefreshedAt = now

	m.kimiUsage = kimiUsageSnapshot{
		Summary: kimiUsageSummary{Sessions: 6, Turns: 84, InputTokens: 270_000, CacheRead: 610_000, CacheCreation: 85_000, OutputTokens: 135_000, LifetimeTokens: 1_100_000},
		Daily:   []kimiDailyUsage{{StartDate: today, Tokens: 39_000}, {StartDate: yesterday, Tokens: 48_000}, {StartDate: threeDaysAgo, Tokens: 51_000}},
		Models:  []kimiUsageModel{{Model: "kimi-k2", Tokens: 880_000}, {Model: "kimi-k2-thinking", Tokens: 220_000}},
		History: []kimiHistoricalSession{
			{ID: "demo-kimi-blueprint", Title: "Review command output", WorkDir: "/demo/blueprint-cli", Model: "kimi-k2", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-30 * time.Minute), InputTokens: 12_000, CacheRead: 32_000, CacheCreation: 2_000, OutputTokens: 8_000, LifetimeTokens: 54_000},
			{ID: "demo-kimi-atlas", Title: "Check layout variants", WorkDir: "/demo/atlas-studio", Model: "kimi-k2-thinking", CreatedAt: now.Add(-1 * 24 * time.Hour), UpdatedAt: now.Add(-1 * 24 * time.Hour), InputTokens: 9_000, CacheRead: 17_000, OutputTokens: 5_000, LifetimeTokens: 31_000},
		},
	}
	m.kimiUsageRefreshedAt = now

	m.claudeUsage = claudeUsageSnapshot{
		LimitsOn: true,
		Limits: claudelimits.Limits{
			FiveHour: &claudelimits.Window{UsedPercent: 64, ResetsAt: now.Add(2*time.Hour + 30*time.Minute)},
			SevenDay: &claudelimits.Window{UsedPercent: 27, ResetsAt: now.Add(4 * 24 * time.Hour)},
		},
		Summary: claudeUsageSummary{Sessions: 12, Messages: 188, InputTokens: 590_000, CacheRead: 1_720_000, CacheCreation: 220_000, OutputTokens: 370_000, LifetimeTokens: 2_900_000},
		Daily:   []claudeDailyUsage{{StartDate: today, Tokens: 87_000}, {StartDate: yesterday, Tokens: 75_000}, {StartDate: threeDaysAgo, Tokens: 93_000}},
		Models:  []claudeUsageModel{{Model: "claude-sonnet", Tokens: 2_200_000}, {Model: "claude-haiku", Tokens: 700_000}},
		History: []claudeHistoricalSession{
			{ID: "demo-claude-atlas", Title: "Shape the launch page", WorkDir: "/demo/atlas-studio", GitBranch: "launch", Model: "claude-sonnet", UpdatedAt: now.Add(-5 * time.Minute), InputTokens: 35_000, CacheRead: 72_000, CacheCreation: 9_000, OutputTokens: 19_000, LifetimeTokens: 135_000},
			{ID: "demo-claude-ember", Title: "Plan API migration", WorkDir: "/demo/ember-api", GitBranch: "main", Model: "claude-haiku", UpdatedAt: now.Add(-4 * time.Hour), InputTokens: 16_000, CacheRead: 29_000, OutputTokens: 8_000, LifetimeTokens: 53_000},
		},
	}
	m.claudeUsageRefreshedAt = now
}
