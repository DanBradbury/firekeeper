package tui

import (
	"fmt"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// usage_pages.go adapts the Copilot, Kimi and Claude snapshots to the shared
// Usage layout in usage_page.go. Codex lives in codex_usage_view.go.

// usageModels turns all-time model totals into coloured card rows.
func usageModels(models []modelUsage, colors map[string]string) []modelUsage {
	return colorModelUsageWithAssignments(models, colors)
}

func (m model) copilotPage() usagePage {
	snapshot := m.copilotUsage
	names := make([]string, 0, len(snapshot.Models)+len(snapshot.DailyByModel))
	models := make([]modelUsage, 0, len(snapshot.Models))
	for _, item := range snapshot.Models {
		names = append(names, item.Model)
		models = append(models, modelUsage{Model: item.Model, Tokens: item.Tokens})
	}
	for _, entry := range snapshot.DailyByModel {
		names = append(names, entry.Model)
	}
	colors := modelUsageColorAssignments(names)
	summary := snapshot.Summary

	sessions := make([]usageSession, 0, len(snapshot.History))
	for _, item := range snapshot.History {
		sessions = append(sessions, usageSession{
			ID: item.ID, Title: item.Title, WorkDir: item.WorkDir, Branch: item.GitBranch,
			Model: item.Model, Tokens: item.LifetimeTokens, UpdatedAt: item.UpdatedAt,
			Details: []usageField{
				{Name: "Repo", Value: item.Repository, Color: dashTeal},
				{Name: "Usage", Value: fmt.Sprintf("%s calls · %s requests", formatTokenCount(item.ModelCalls), formatRequestCount(item.UserRequests))},
				{Name: "Host", Value: item.HostType},
			},
		})
	}

	return usagePage{
		Label:       "Copilot",
		Provider:    transcript.ProviderCopilot,
		Loading:     m.copilotUsageLoading,
		LoadingText: "Reading local Copilot CLI usage…",
		HasData:     snapshot.hasData(),
		Err:         m.copilotUsageErr,
		HistoryErr:  m.copilotHistoryErr,
		Cards: []usageCard{
			{"plan", dashBlue, func(rows int) []string { return copilotPlanLines(snapshot) }},
			{"lifetime", dashYellow, func(rows int) []string {
				return []string{
					usageStat("Sessions", fmt.Sprintf("%d", summary.Sessions), ""),
					usageStat("Calls", formatTokenCount(summary.ModelCalls), ""),
					usageStat("Requests", formatRequestCount(summary.UserRequests), ""),
					usageStat("Tokens", formatTokenCount(summary.LifetimeTokens), dashYellow),
					usageStat("In / out", formatTokenCount(summary.InputTokens)+" / "+formatTokenCount(summary.OutputTokens), ""),
				}
			}},
			{"models", dashPurple, func(rows int) []string {
				return usageModelCardLines(usageModels(models, colors), rows, "No local Copilot CLI model usage.")
			}},
		},
		Days: func(count, inner int) (string, []string) {
			if len(snapshot.DailyByModel) > 0 {
				days := recentCopilotDailyModelUsage(snapshot.DailyByModel, time.Now(), count)
				return " · model", usageModelDays(days, colors, inner)
			}
			days := recentCopilotDailyUsage(snapshot.Daily, time.Now(), count)
			dates, tokens := make([]string, len(days)), make([]int64, len(days))
			for index, day := range days {
				dates[index], tokens[index] = day.StartDate, day.Tokens
			}
			return "", usageTotalDays(dates, tokens, inner)
		},
		Sessions: sessions,
		Cursor:   m.copilotHistoryCursor,
	}
}

// copilotPlanLines shows AI credit usage when the plan endpoint answered.
// Local history is not account-wide quota, so the card says where the real
// allowance lives.
func copilotPlanLines(snapshot copilotUsageSnapshot) []string {
	plan := snapshot.Plan
	if !plan.Available {
		message := "Plan information unavailable"
		if snapshot.PlanErr != "" {
			message += ": " + snapshot.PlanErr
		}
		return []string{usageNote(message), usageNote("Allowance: GitHub billing")}
	}
	lines := []string{}
	if plan.Name != "" {
		lines = append(lines, usageStat("Plan", plan.Name, ""))
	}
	percent := 0.0
	if plan.TotalCredits > 0 {
		percent = plan.UsedCredits / plan.TotalCredits * 100
	}
	lines = append(lines,
		dashPaint(dashCell("AI credits", 10, false), usageTextColor, false)+
			dashPaint(fmt.Sprintf("%3.0f%% ", percent), usagePercentColor(percent), true)+
			dashPaint(formatAICredits(plan.UsedCredits)+" / "+formatAICredits(plan.TotalCredits), usageMutedColor, false),
	)
	if !plan.ResetAt.IsZero() {
		lines = append(lines, usageNote(codexResetText(plan.ResetAt.Unix(), time.Now())))
	}
	return append(lines, usageNote("Allowance: GitHub billing"))
}

func (m model) kimiPage() usagePage {
	snapshot := m.kimiUsage
	summary := snapshot.Summary
	names := make([]string, 0, len(snapshot.Models))
	models := make([]modelUsage, 0, len(snapshot.Models))
	for _, item := range snapshot.Models {
		names = append(names, item.Model)
		models = append(models, modelUsage{Model: item.Model, Tokens: item.Tokens})
	}
	colors := modelUsageColorAssignments(names)

	sessions := make([]usageSession, 0, len(snapshot.History))
	for _, item := range snapshot.History {
		sessions = append(sessions, usageSession{
			ID: item.ID, Title: item.Title, WorkDir: item.WorkDir,
			Model: item.Model, Tokens: item.LifetimeTokens, UpdatedAt: item.UpdatedAt,
			Details: []usageField{
				{Name: "Input", Value: formatTokenCount(item.InputTokens) + " · cache read " + formatTokenCount(item.CacheRead)},
				{Name: "Output", Value: formatTokenCount(item.OutputTokens) + " · cache write " + formatTokenCount(item.CacheCreation)},
				{Name: "Created", Value: formatSessionTime(item.CreatedAt)},
			},
		})
	}

	return usagePage{
		Label:       "Kimi",
		Provider:    transcript.ProviderKimi,
		Loading:     m.kimiUsageLoading,
		LoadingText: "Reading local Kimi Code sessions…",
		HasData:     snapshot.hasData(),
		Err:         m.kimiUsageErr,
		Cards: []usageCard{
			{"lifetime", dashYellow, func(rows int) []string {
				return []string{
					usageStat("Sessions", fmt.Sprintf("%d", summary.Sessions), ""),
					usageStat("Turns", fmt.Sprintf("%d", summary.Turns), ""),
					usageStat("Tokens", formatTokenCount(summary.LifetimeTokens), dashYellow),
				}
			}},
			{"tokens", dashBlue, func(rows int) []string {
				return []string{
					usageStat("Input", formatTokenCount(summary.InputTokens), ""),
					usageStat("Cache read", formatTokenCount(summary.CacheRead), ""),
					usageStat("Cache write", formatTokenCount(summary.CacheCreation), ""),
					usageStat("Output", formatTokenCount(summary.OutputTokens), ""),
					usageNote("Quota: Kimi Code /usage"),
				}
			}},
			{"models", dashPurple, func(rows int) []string {
				return usageModelCardLines(usageModels(models, colors), rows, "No local Kimi Code token records.")
			}},
		},
		Days: func(count, inner int) (string, []string) {
			days := recentKimiDailyUsage(snapshot.Daily, time.Now(), count)
			dates, tokens := make([]string, len(days)), make([]int64, len(days))
			for index, day := range days {
				dates[index], tokens[index] = day.StartDate, day.Tokens
			}
			return "", usageTotalDays(dates, tokens, inner)
		},
		Sessions: sessions,
		Cursor:   m.kimiHistoryCursor,
	}
}

func (m model) claudePage() usagePage {
	snapshot := m.claudeUsage
	summary := snapshot.Summary
	names := make([]string, 0, len(snapshot.Models))
	models := make([]modelUsage, 0, len(snapshot.Models))
	for _, item := range snapshot.Models {
		names = append(names, item.Model)
		models = append(models, modelUsage{Model: item.Model, Tokens: item.Tokens})
	}
	colors := modelUsageColorAssignments(names)

	sessions := make([]usageSession, 0, len(snapshot.History))
	for _, item := range snapshot.History {
		details := []usageField{
			{Name: "Input", Value: formatTokenCount(item.InputTokens) + " · cache read " + formatTokenCount(item.CacheRead)},
			{Name: "Output", Value: formatTokenCount(item.OutputTokens) + " · cache write " + formatTokenCount(item.CacheCreation)},
		}
		if item.UndercountWarning != "" {
			details = append(details, usageField{Name: "Note", Value: item.UndercountWarning, Color: dashYellow})
		}
		sessions = append(sessions, usageSession{
			ID: item.ID, Title: item.Title, WorkDir: item.WorkDir, Branch: item.GitBranch,
			Model: item.Model, Tokens: item.LifetimeTokens, UpdatedAt: item.UpdatedAt,
			Details: details,
		})
	}

	return usagePage{
		Label:       "Claude",
		Provider:    transcript.ProviderClaude,
		Loading:     m.claudeUsageLoading,
		LoadingText: "Reading local Claude Code transcripts…",
		HasData:     snapshot.hasData(),
		Err:         m.claudeUsageErr,
		Cards: []usageCard{
			{"lifetime", dashYellow, func(rows int) []string {
				return []string{
					usageStat("Sessions", fmt.Sprintf("%d", summary.Sessions), ""),
					usageStat("Responses", fmt.Sprintf("%d", summary.Messages), ""),
					usageStat("Tokens", formatTokenCount(summary.LifetimeTokens), dashYellow),
				}
			}},
			{"tokens", dashBlue, func(rows int) []string {
				return []string{
					usageStat("Input", formatTokenCount(summary.InputTokens), ""),
					usageStat("Cache read", formatTokenCount(summary.CacheRead), ""),
					usageStat("Cache write", formatTokenCount(summary.CacheCreation), ""),
					usageStat("Output", formatTokenCount(summary.OutputTokens), ""),
					usageNote("Plan limits: Claude Code /usage"),
				}
			}},
			{"models", dashPurple, func(rows int) []string {
				return usageModelCardLines(usageModels(models, colors), rows, "No local Claude Code token records.")
			}},
		},
		Days: func(count, inner int) (string, []string) {
			days := recentClaudeDailyUsage(snapshot.Daily, time.Now(), count)
			dates, tokens := make([]string, len(days)), make([]int64, len(days))
			for index, day := range days {
				dates[index], tokens[index] = day.StartDate, day.Tokens
			}
			return "", usageTotalDays(dates, tokens, inner)
		},
		Sessions: sessions,
		Cursor:   m.claudeHistoryCursor,
	}
}
