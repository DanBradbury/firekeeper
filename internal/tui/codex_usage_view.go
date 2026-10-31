package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// codexPage adapts the Codex snapshot to the shared Usage layout: quota
// limits, lifetime totals and the models of running sessions as cards.
func (m model) codexPage() usagePage {
	now := time.Now()
	colors := m.codexModelColors()
	return usagePage{
		Label:       "Codex",
		Provider:    transcript.ProviderCodex,
		Loading:     m.codexUsageLoading,
		LoadingText: "Fetching Codex account usage…",
		HasData:     m.codexUsage.hasData(),
		Err:         m.codexUsageErr,
		HistoryErr:  m.codexHistoryErr,
		Cards: []usageCard{
			{"limits", dashBlue, func(rows int) []string { return m.codexLimitLines(rows, now) }},
			{"lifetime", dashYellow, func(rows int) []string { return m.codexLifetimeLines() }},
			{"active models", dashPurple, func(rows int) []string {
				models := colorModelUsageWithAssignments(activeCodexModelUsage(m.processGroups), colors)
				return usageModelCardLines(models, rows, "No active model usage.")
			}},
		},
		Days: func(count, inner int) (string, []string) {
			if len(m.codexUsage.DailyByModel) > 0 {
				days := recentCodexDailyModelUsage(m.codexUsage.DailyByModel, time.Now(), count)
				return " · model", usageModelDays(days, colors, inner)
			}
			days := recentCodexDailyUsage(m.codexUsage.Daily, time.Now(), count)
			dates, tokens := make([]string, len(days)), make([]int64, len(days))
			for index, day := range days {
				dates[index], tokens[index] = day.StartDate, day.Tokens
			}
			return "", usageTotalDays(dates, tokens, inner)
		},
		Sessions: m.codexUsageSessions(),
		Cursor:   m.codexHistoryCursor,
	}
}

func (m model) codexUsageSessions() []usageSession {
	sessions := make([]usageSession, 0, len(m.codexUsage.History))
	for _, item := range m.codexUsage.History {
		source := item.Source
		if item.Archived != 0 {
			source = strings.TrimSpace(source + " archived")
		}
		sessions = append(sessions, usageSession{
			ID: item.ID, Title: item.Title, WorkDir: item.WorkDir, Branch: item.GitBranch,
			Model: item.Model, Tokens: item.Tokens, UpdatedAt: item.UpdatedAt,
			Details: []usageField{{Name: "Source", Value: source}},
		})
	}
	return sessions
}

// codexLimitLines lists each quota window as one line: name, percent used and
// time to reset. Percent is coloured by how close the window is to its limit.
func (m model) codexLimitLines(rows int, now time.Time) []string {
	limits := m.codexUsage.sortedRateLimits()
	var lines []string
	for _, limit := range limits {
		name := limit.LimitName
		if name == "" {
			name = limit.LimitID
		}
		if limit.Primary != nil {
			lines = append(lines, codexLimitLine(name, *limit.Primary, now))
		}
		if limit.Secondary != nil {
			lines = append(lines, codexLimitLine(name, *limit.Secondary, now))
		}
		if limit.RateLimitReachedType != "" {
			lines = append(lines, dashPaint("LIMIT REACHED "+sanitizeProcessCommand(limit.RateLimitReachedType), dashRed, true))
		}
	}
	if len(lines) == 0 {
		return []string{dashPaint("No ChatGPT-backed quota data", usageMutedColor, false)}
	}
	if len(lines) > rows && rows > 0 {
		extra := len(lines) - (rows - 1)
		lines = append(lines[:rows-1], dashPaint(fmt.Sprintf("+%d more", extra), usageMutedColor, false))
	}
	return lines
}

func codexLimitLine(name string, window codexQuotaWindow, now time.Time) string {
	title := quotaWindowTitle(name, window.WindowDurationMin)
	return dashPaint(dashCell(title, 10, false), usageTextColor, false) +
		dashPaint(fmt.Sprintf("%4.0f%% ", window.UsedPercent), usagePercentColor(window.UsedPercent), true) +
		dashPaint(codexResetText(window.ResetsAt, now), usageMutedColor, false)
}

// usageLimitLine is one quota row for providers whose windows are not Codex
// ones: name, percent used, and the reset countdown.
func usageLimitLine(name string, percent float64, resetAt, now time.Time) string {
	reset := "reset unknown"
	if !resetAt.IsZero() {
		reset = codexResetText(resetAt.Unix(), now)
	}
	return dashPaint(dashCell(name, 15, false), usageTextColor, false) +
		dashPaint(fmt.Sprintf("%4.0f%% ", percent), usagePercentColor(percent), true) +
		dashPaint(reset, usageMutedColor, false)
}

func usagePercentColor(percent float64) string {
	switch {
	case percent >= 80:
		return dashRed
	case percent >= 50:
		return dashYellow
	default:
		return dashGreen
	}
}

func (m model) codexLifetimeLines() []string {
	plan := ""
	for _, limit := range m.codexUsage.sortedRateLimits() {
		if limit.PlanType != "" {
			plan = strings.ToUpper(sanitizeProcessCommand(limit.PlanType))
			break
		}
	}
	summary := m.codexUsage.Summary
	stat := func(name, value string) string {
		return dashPaint(dashCell(name, 8, false), usageMutedColor, false) + dashPaint(value, usageBrightColor, true)
	}
	var lines []string
	if plan != "" {
		lines = append(lines, stat("Plan", plan))
	}
	if summary.LifetimeTokens != nil {
		lines = append(lines, dashPaint(dashCell("Tokens", 8, false), usageMutedColor, false)+
			dashPaint(formatTokenCount(*summary.LifetimeTokens), dashYellow, true))
	}
	if summary.PeakDailyTokens != nil {
		lines = append(lines, stat("Peak day", formatTokenCount(*summary.PeakDailyTokens)))
	}
	if summary.CurrentStreakDays != nil {
		streak := fmt.Sprintf("%dd", *summary.CurrentStreakDays)
		if summary.LongestStreakDays != nil {
			streak += fmt.Sprintf(" (best %dd)", *summary.LongestStreakDays)
		}
		lines = append(lines, stat("Streak", streak))
	}
	if m.codexUsage.ResetCredits > 0 {
		lines = append(lines, stat("Resets", fmt.Sprintf("%d available", m.codexUsage.ResetCredits)))
	}
	if len(lines) == 0 {
		return []string{dashPaint("No lifetime usage yet.", usageMutedColor, false)}
	}
	return lines
}

func (m model) codexModelColors() map[string]string {
	models := activeCodexModelUsage(m.processGroups)
	names := make([]string, 0, len(models)+len(m.codexUsage.DailyByModel))
	for _, model := range models {
		names = append(names, model.Model)
	}
	for _, entry := range m.codexUsage.DailyByModel {
		names = append(names, entry.Model)
	}
	return modelUsageColorAssignments(names)
}

// codexResetText is the compact reset countdown shown in a limits card.
func codexResetText(resetAt int64, now time.Time) string {
	text := formatResetCountdown(resetAt, now)
	if rest, ok := strings.CutPrefix(text, "Resets in "); ok {
		return "↻ " + rest
	}
	return text
}
