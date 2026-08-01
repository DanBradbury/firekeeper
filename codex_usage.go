package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const codexUsageTimeout = 15 * time.Second

const (
	usagePanelColor     = "24;26;38"
	usageTextColor      = "174;185;220"
	usageBrightColor    = "205;214;244"
	usageMutedColor     = "103;114;151"
	usageTrackColor     = "48;53;72"
	usageDividerColor   = "42;46;63"
	usageHighlightColor = "55;60;78"
)

type codexQuotaWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	WindowDurationMin int64   `json:"windowDurationMins"`
	ResetsAt          int64   `json:"resetsAt"`
}

type codexQuotaCredits struct {
	HasCredits bool   `json:"hasCredits"`
	Unlimited  bool   `json:"unlimited"`
	Balance    string `json:"balance"`
}

type codexRateLimit struct {
	LimitID              string             `json:"limitId"`
	LimitName            string             `json:"limitName"`
	Primary              *codexQuotaWindow  `json:"primary"`
	Secondary            *codexQuotaWindow  `json:"secondary"`
	Credits              *codexQuotaCredits `json:"credits"`
	PlanType             string             `json:"planType"`
	RateLimitReachedType string             `json:"rateLimitReachedType"`
}

type codexRateLimitResetCredits struct {
	AvailableCount int `json:"availableCount"`
}

type codexUsageSummary struct {
	LifetimeTokens        *int64 `json:"lifetimeTokens"`
	PeakDailyTokens       *int64 `json:"peakDailyTokens"`
	LongestRunningTurnSec *int64 `json:"longestRunningTurnSec"`
	CurrentStreakDays     *int64 `json:"currentStreakDays"`
	LongestStreakDays     *int64 `json:"longestStreakDays"`
}

type codexDailyUsage struct {
	StartDate string `json:"startDate"`
	Tokens    int64  `json:"tokens"`
}

type codexUsageSnapshot struct {
	RateLimits   map[string]codexRateLimit
	ResetCredits int
	Summary      codexUsageSummary
	Daily        []codexDailyUsage
}

type codexUsageResultMsg struct {
	snapshot  codexUsageSnapshot
	refreshed time.Time
	err       error
}

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRPCResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *codexRPCError  `json:"error"`
}

type codexRateLimitsRPCResult struct {
	RateLimits          codexRateLimit              `json:"rateLimits"`
	RateLimitsByLimitID map[string]codexRateLimit   `json:"rateLimitsByLimitId"`
	ResetCredits        *codexRateLimitResetCredits `json:"rateLimitResetCredits"`
}

type codexUsageRPCResult struct {
	Summary           codexUsageSummary `json:"summary"`
	DailyUsageBuckets []codexDailyUsage `json:"dailyUsageBuckets"`
}

func refreshCodexUsage() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := fetchCodexUsage()
		return codexUsageResultMsg{
			snapshot:  snapshot,
			refreshed: time.Now(),
			err:       err,
		}
	}
}

func fetchCodexUsage() (codexUsageSnapshot, error) {
	executable, err := findCodexExecutable()
	if err != nil {
		return codexUsageSnapshot{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), codexUsageTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://")
	stdin, err := command.StdinPipe()
	if err != nil {
		return codexUsageSnapshot{}, fmt.Errorf("open Codex app-server input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return codexUsageSnapshot{}, fmt.Errorf("open Codex app-server output: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return codexUsageSnapshot{}, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	requests := []any{
		map[string]any{
			"method": "initialize",
			"id":     0,
			"params": map[string]any{
				"clientInfo": map[string]string{
					"name":    "firekeeper",
					"title":   "Firekeeper",
					"version": "0.1.0",
				},
			},
		},
		map[string]any{"method": "initialized", "params": map[string]any{}},
		map[string]any{"method": "account/rateLimits/read", "id": 1},
		map[string]any{"method": "account/usage/read", "id": 2},
	}
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			return codexUsageSnapshot{}, fmt.Errorf("write Codex app-server request: %w", err)
		}
	}

	snapshot, err := readCodexUsageResponses(stdout)
	if ctx.Err() == context.DeadlineExceeded {
		return codexUsageSnapshot{}, fmt.Errorf("Codex usage request timed out")
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return codexUsageSnapshot{}, fmt.Errorf("%w: %s", err, sanitizeProcessCommand(message))
		}
		return codexUsageSnapshot{}, err
	}
	return snapshot, nil
}

func findCodexExecutable() (string, error) {
	if path, err := exec.LookPath("codex"); err == nil {
		return path, nil
	}
	if runtime.GOOS == "darwin" {
		path := "/Applications/Codex.app/Contents/Resources/codex"
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("Codex executable not found")
}

func readCodexUsageResponses(reader io.Reader) (codexUsageSnapshot, error) {
	snapshot := codexUsageSnapshot{RateLimits: make(map[string]codexRateLimit)}
	gotRateLimits := false
	gotUsage := false
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var response codexRPCResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			continue
		}
		var id int
		if len(response.ID) == 0 || json.Unmarshal(response.ID, &id) != nil {
			continue
		}
		if id != 1 && id != 2 {
			continue
		}
		if response.Error != nil {
			return codexUsageSnapshot{}, fmt.Errorf("Codex app-server request %d: %s", id, response.Error.Message)
		}
		switch id {
		case 1:
			var result codexRateLimitsRPCResult
			if err := json.Unmarshal(response.Result, &result); err != nil {
				return codexUsageSnapshot{}, fmt.Errorf("decode Codex rate limits: %w", err)
			}
			for key, limit := range result.RateLimitsByLimitID {
				snapshot.RateLimits[key] = limit
			}
			if len(snapshot.RateLimits) == 0 && result.RateLimits.LimitID != "" {
				snapshot.RateLimits[result.RateLimits.LimitID] = result.RateLimits
			}
			if result.ResetCredits != nil {
				snapshot.ResetCredits = result.ResetCredits.AvailableCount
			}
			gotRateLimits = true
		case 2:
			var result codexUsageRPCResult
			if err := json.Unmarshal(response.Result, &result); err != nil {
				return codexUsageSnapshot{}, fmt.Errorf("decode Codex usage: %w", err)
			}
			snapshot.Summary = result.Summary
			snapshot.Daily = result.DailyUsageBuckets
			gotUsage = true
		}
		if gotRateLimits && gotUsage {
			return snapshot, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return codexUsageSnapshot{}, fmt.Errorf("read Codex app-server response: %w", err)
	}
	return codexUsageSnapshot{}, fmt.Errorf("Codex app-server closed before returning usage data")
}

func (m model) viewCodexUsage(contentRows int) string {
	lines := m.codexUsageHeaderLines()
	if m.codexUsageLoading && !m.codexUsage.hasData() {
		lines = append(lines,
			usageDividerLine(m.width),
			usageSectionLine("LOADING", m.width),
			usagePaintLine("  Fetching Codex account usage…", m.width, usageMutedColor, false, usagePanelColor),
		)
		return fillCodexUsageLines(lines, contentRows, m.width)
	}
	if m.codexUsageErr != "" && !m.codexUsage.hasData() {
		lines = append(lines,
			usageDividerLine(m.width),
			usageSectionLine("UNAVAILABLE", m.width),
			usagePaintLine("  "+m.codexUsageErr, m.width, usageMutedColor, false, usagePanelColor),
		)
		return fillCodexUsageLines(lines, contentRows, m.width)
	}

	lines = append(lines, usageDividerLine(m.width), usageSectionLine("LIMITS", m.width))
	limits := m.codexUsage.sortedRateLimits()
	if len(limits) == 0 {
		lines = append(lines, usagePaintLine("  No ChatGPT-backed quota data", m.width, usageMutedColor, false, usagePanelColor))
	} else {
		for _, limit := range limits {
			name := limit.LimitName
			if name == "" {
				name = limit.LimitID
			}
			if limit.Primary != nil {
				lines = append(lines, codexQuotaBlock(name, *limit.Primary, m.width, time.Now())...)
			}
			if limit.Secondary != nil {
				lines = append(lines, codexQuotaBlock(name+" secondary", *limit.Secondary, m.width, time.Now())...)
			}
			if limit.RateLimitReachedType != "" {
				lines = append(lines, usagePaintLine("  LIMIT REACHED  "+limit.RateLimitReachedType, m.width, usageBrightColor, true, usagePanelColor))
			}
		}
	}

	lines = append(lines, usageDividerLine(m.width), usageSectionLine("TOKENS BY DAY", m.width))
	days := recentCodexDailyUsage(m.codexUsage.Daily, time.Now(), 7)
	peak := int64(0)
	for _, day := range days {
		peak = max(peak, day.Tokens)
	}
	for index, day := range days {
		label := dayLabel(day.StartDate, index == len(days)-1)
		lines = append(lines, usageMetricBarLine(label, day.Tokens, peak, m.width, index == len(days)-1))
	}

	lines = append(lines, usageDividerLine(m.width), usageSectionLine("ACTIVE TOKENS BY MODEL", m.width))
	models := activeCodexModelUsage(m.processGroups)
	if len(models) == 0 {
		lines = append(lines, usagePaintLine("  No active model usage", m.width, usageMutedColor, false, usageHighlightColor))
	} else {
		for _, model := range models {
			lines = append(lines, usageModelLine(model, m.width))
		}
	}
	return fillCodexUsageLines(lines, contentRows, m.width)
}

type codexModelUsage struct {
	Model  string
	Tokens int64
}

func (m model) codexUsageHeaderLines() []string {
	plan := "CODEX"
	for _, limit := range m.codexUsage.sortedRateLimits() {
		if limit.PlanType != "" {
			plan = strings.ToUpper(limit.PlanType)
			break
		}
	}
	details := plan
	if m.codexUsage.Summary.LifetimeTokens != nil {
		details += "  •  " + formatTokenCount(*m.codexUsage.Summary.LifetimeTokens) + " TOKENS"
	}
	if m.codexUsage.ResetCredits > 0 {
		details += fmt.Sprintf("  •  %d RESET", m.codexUsage.ResetCredits)
	}
	return []string{
		usagePaintLine("  ╭────╮  Codex", m.width, usageBrightColor, true, usagePanelColor),
		usagePaintLine("  │ >_ │  "+details, m.width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────╯", m.width, usageMutedColor, false, usagePanelColor),
	}
}

func codexQuotaBlock(name string, window codexQuotaWindow, width int, now time.Time) []string {
	title := quotaWindowTitle(name, window.WindowDurationMin)
	percent := fmt.Sprintf("%.0f%%", window.UsedPercent)
	return []string{
		usageSidesLine("  "+title, percent+"  ", width, usageTextColor, usageBrightColor),
		usageQuotaBarLine(window.UsedPercent, width),
		usagePaintLine("  "+formatResetCountdown(window.ResetsAt, now), width, usageMutedColor, false, usagePanelColor),
	}
}

func quotaWindowTitle(name string, minutes int64) string {
	var window string
	switch {
	case minutes == 10080:
		window = "Weekly"
	case minutes == 1440:
		window = "Daily"
	case minutes >= 60 && minutes%60 == 0:
		window = fmt.Sprintf("%d-hour", minutes/60)
	default:
		window = fmt.Sprintf("%d-minute", minutes)
	}
	if name != "" && !strings.EqualFold(name, "codex") {
		return window + " · " + name
	}
	return window
}

func formatResetCountdown(resetAt int64, now time.Time) string {
	if resetAt <= 0 {
		return "Reset time unavailable"
	}
	remaining := time.Unix(resetAt, 0).Sub(now)
	if remaining <= 0 {
		return "Reset due"
	}
	remaining = remaining.Round(time.Minute)
	days := int(remaining / (24 * time.Hour))
	hours := int((remaining % (24 * time.Hour)) / time.Hour)
	minutes := int((remaining % time.Hour) / time.Minute)
	if days > 0 {
		return fmt.Sprintf("Resets in %dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("Resets in %dh %dm", hours, minutes)
	}
	return fmt.Sprintf("Resets in %dm", max(minutes, 1))
}

func recentCodexDailyUsage(buckets []codexDailyUsage, now time.Time, count int) []codexDailyUsage {
	byDate := make(map[string]int64, len(buckets))
	for _, bucket := range buckets {
		byDate[bucket.StartDate] += bucket.Tokens
	}
	result := make([]codexDailyUsage, 0, count)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for offset := count - 1; offset >= 0; offset-- {
		date := today.AddDate(0, 0, -offset).Format("2006-01-02")
		result = append(result, codexDailyUsage{StartDate: date, Tokens: byDate[date]})
	}
	return result
}

func dayLabel(date string, today bool) string {
	if today {
		return "Today"
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return parsed.Format("Mon")
}

func activeCodexModelUsage(groups []processGroup) []codexModelUsage {
	totals := make(map[string]int64)
	seen := make(map[string]bool)
	for _, group := range groups {
		if group.tool != "Codex" {
			continue
		}
		for _, session := range group.sessions {
			key := session.id
			if key == "" {
				key = session.rolloutPath
			}
			if key != "" && seen[key] {
				continue
			}
			seen[key] = true
			model := session.model
			if model == "" {
				model = "unknown"
			}
			totals[model] += session.tokensUsed
		}
	}
	models := make([]codexModelUsage, 0, len(totals))
	for model, tokens := range totals {
		models = append(models, codexModelUsage{Model: model, Tokens: tokens})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Tokens == models[j].Tokens {
			return models[i].Model < models[j].Model
		}
		return models[i].Tokens > models[j].Tokens
	})
	return models
}

func usageSectionLine(title string, width int) string {
	return usagePaintLine("  "+title, width, usageMutedColor, true, usagePanelColor)
}

func usageDividerLine(width int) string {
	line := ""
	if width > 4 {
		line = "  " + strings.Repeat("─", width-4) + "  "
	}
	return usagePaintLine(line, width, usageDividerColor, false, usagePanelColor)
}

func usageSidesLine(left, right string, width int, leftColor, rightColor string) string {
	space := max(width-len([]rune(left))-len([]rune(right)), 1)
	leftText := left + strings.Repeat(" ", space)
	return usagePaint(leftText, leftColor, true, usagePanelColor) + usagePaint(right, rightColor, true, usagePanelColor)
}

func usageQuotaBarLine(percent float64, width int) string {
	barWidth := max(width-4, 1)
	filled := int(float64(barWidth)*min(max(percent, 0), 100)/100 + 0.5)
	if percent > 0 {
		filled = max(filled, 1)
	}
	filled = min(filled, barWidth)
	return usagePaint("  ", usagePanelColor, false, usagePanelColor) +
		usagePaint(strings.Repeat("━", filled), usageBrightColor, true, usagePanelColor) +
		usagePaint(strings.Repeat("━", barWidth-filled), usageTrackColor, false, usagePanelColor) +
		usagePaint("  ", usagePanelColor, false, usagePanelColor)
}

func usageMetricBarLine(label string, value, maximum int64, width int, emphasized bool) string {
	labelWidth := min(8, max(width/8, 5))
	valueWidth := min(9, max(width/10, 6))
	barWidth := max(width-labelWidth-valueWidth-5, 1)
	ratio := float64(0)
	if maximum > 0 {
		ratio = float64(value) / float64(maximum)
	}
	filled := int(float64(barWidth)*ratio + 0.5)
	if value > 0 {
		filled = max(filled, 1)
	}
	filled = min(filled, barWidth)
	labelColor := usageMutedColor
	fillColor := usageTextColor
	if emphasized {
		labelColor = usageBrightColor
		fillColor = usageBrightColor
	}
	prefix := "  " + padRPGMenuText(label, labelWidth) + " "
	suffix := " " + fmt.Sprintf("%*s", valueWidth, formatTokenCount(value)) + " "
	return usagePaint(prefix, labelColor, emphasized, usagePanelColor) +
		usagePaint(strings.Repeat("━", filled), fillColor, true, usagePanelColor) +
		usagePaint(strings.Repeat("━", barWidth-filled), usageTrackColor, false, usagePanelColor) +
		usagePaint(suffix, labelColor, emphasized, usagePanelColor)
}

func usageModelLine(model codexModelUsage, width int) string {
	name := strings.ReplaceAll(strings.ToUpper(model.Model), "-", " ")
	value := formatTokenCount(model.Tokens)
	contentWidth := max(width-4, 1)
	space := max(contentWidth-len([]rune(name))-len([]rune(value)), 1)
	line := "  " + name + strings.Repeat(" ", space) + value + "  "
	return usagePaintLine(line, width, usageTextColor, false, usageHighlightColor)
}

func usagePaintLine(value string, width int, foreground string, bold bool, background string) string {
	return usagePaint(fitLine(value, width), foreground, bold, background)
}

func usagePaint(value, foreground string, bold bool, background string) string {
	weight := "22"
	if bold {
		weight = "1"
	}
	return fmt.Sprintf("\x1b[%s;38;2;%s;48;2;%sm%s\x1b[0m", weight, foreground, background, value)
}

func (m model) codexUsageFooter() (string, string) {
	help := "  r refresh  •  Tab switch  •  q quit"
	if m.codexUsageLoading {
		return help, "  fetching Codex quotas and usage…"
	}
	if m.codexUsageErr != "" {
		return help, "  Codex usage refresh failed: " + m.codexUsageErr
	}
	if m.codexUsageRefreshedAt.IsZero() {
		return help, "  Codex usage not loaded"
	}
	return help, "  refreshed " + m.codexUsageRefreshedAt.Format("15:04:05")
}

func (snapshot codexUsageSnapshot) hasData() bool {
	return len(snapshot.RateLimits) > 0 || len(snapshot.Daily) > 0 || snapshot.Summary.LifetimeTokens != nil
}

func (snapshot codexUsageSnapshot) sortedRateLimits() []codexRateLimit {
	keys := make([]string, 0, len(snapshot.RateLimits))
	for key := range snapshot.RateLimits {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	limits := make([]codexRateLimit, 0, len(keys))
	for _, key := range keys {
		limits = append(limits, snapshot.RateLimits[key])
	}
	return limits
}

func formatTokenCount(value int64) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func fillCodexUsageLines(lines []string, height, width int) string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, usagePaintLine("", width, usageTextColor, false, usagePanelColor))
	}
	return strings.Join(lines, "\n")
}
