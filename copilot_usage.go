package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type copilotUsageSummary struct {
	Sessions       int64
	ModelCalls     int64
	InputTokens    int64
	OutputTokens   int64
	LifetimeTokens int64
	UserRequests   float64
}

type copilotDailyUsage struct {
	StartDate    string
	Tokens       int64
	UserRequests float64
}

type copilotUsageModel struct {
	Model        string
	Tokens       int64
	ModelCalls   int64
	UserRequests float64
}

type copilotPlanDetails struct {
	Name         string
	UsedCredits  float64
	TotalCredits float64
	Remaining    float64
	ResetAt      time.Time
	Available    bool
}

type copilotUsageSnapshot struct {
	Summary copilotUsageSummary
	Daily   []copilotDailyUsage
	Models  []copilotUsageModel
	Plan    copilotPlanDetails
	PlanErr string
}

type copilotUsageResultMsg struct {
	snapshot  copilotUsageSnapshot
	refreshed time.Time
	err       error
}

type copilotUsageRow struct {
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	Tokens       int64   `json:"tokens"`
	UserRequests float64 `json:"user_requests"`
	ModelCalls   int64   `json:"model_calls"`
	Sessions     int64   `json:"sessions"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

func refreshCopilotUsage() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := fetchCopilotUsage()
		return copilotUsageResultMsg{
			snapshot:  snapshot,
			refreshed: time.Now(),
			err:       err,
		}
	}
}

func fetchCopilotUsage() (copilotUsageSnapshot, error) {
	home, err := copilotHome()
	if err != nil {
		return copilotUsageSnapshot{}, err
	}
	path := filepath.Join(home, "session-store.db")
	if _, err := os.Stat(path); err != nil {
		return copilotUsageSnapshot{}, fmt.Errorf("find Copilot session store: %w", err)
	}
	query := `
		WITH usage AS (
			SELECT
				COALESCE(model, 'unknown') AS model,
				COALESCE(input_tokens, 0) AS input_tokens,
				COALESCE(output_tokens, 0) AS output_tokens,
				CASE
					WHEN initiator = 'user' THEN COALESCE(request_multiplier, 1.0)
					ELSE 0
				END AS user_requests,
				created_at
			FROM assistant_usage_events
		), rows AS (
			SELECT
				0 AS sort_group,
				'' AS sort_label,
				'summary' AS kind,
				'' AS label,
				COALESCE(SUM(input_tokens + output_tokens), 0) AS tokens,
				COALESCE(SUM(user_requests), 0) AS user_requests,
				COUNT(*) AS model_calls,
				(SELECT COUNT(*) FROM sessions) AS sessions,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens
			FROM usage
			UNION ALL
			SELECT
				1 AS sort_group,
				substr(created_at, 1, 10) AS sort_label,
				'daily' AS kind,
				substr(created_at, 1, 10) AS label,
				SUM(input_tokens + output_tokens) AS tokens,
				SUM(user_requests) AS user_requests,
				COUNT(*) AS model_calls,
				0 AS sessions,
				SUM(input_tokens) AS input_tokens,
				SUM(output_tokens) AS output_tokens
			FROM usage
			GROUP BY substr(created_at, 1, 10)
			UNION ALL
			SELECT
				2 AS sort_group,
				model AS sort_label,
				'model' AS kind,
				model AS label,
				SUM(input_tokens + output_tokens) AS tokens,
				SUM(user_requests) AS user_requests,
				COUNT(*) AS model_calls,
				0 AS sessions,
				SUM(input_tokens) AS input_tokens,
				SUM(output_tokens) AS output_tokens
			FROM usage
			GROUP BY model
		)
		SELECT
			kind,
			label,
			tokens,
			user_requests,
			model_calls,
			sessions,
			input_tokens,
			output_tokens
		FROM rows
		ORDER BY sort_group, sort_label
	`
	output, err := exec.Command("sqlite3", "-readonly", "-json", path, query).Output()
	if err != nil {
		return copilotUsageSnapshot{}, fmt.Errorf("read Copilot usage: %w", err)
	}
	snapshot, err := decodeCopilotUsage(output)
	if err != nil {
		return copilotUsageSnapshot{}, err
	}
	snapshot.Plan, snapshot.PlanErr = fetchCopilotPlan()
	return snapshot, nil
}

const copilotPlanURL = "https://api.github.com/copilot_internal/user"

func fetchCopilotPlan() (copilotPlanDetails, string) {
	token := copilotGitHubToken()
	if token == "" {
		return copilotPlanDetails{}, "Copilot token unavailable"
	}

	endpoint := copilotPlanURL
	if apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv("GITHUB_API_URL")), "/"); apiURL != "" {
		endpoint = apiURL + "/copilot_internal/user"
	}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return copilotPlanDetails{}, "create Copilot plan request: " + err.Error()
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return copilotPlanDetails{}, "read Copilot plan: " + sanitizeProcessCommand(err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return copilotPlanDetails{}, fmt.Sprintf("Copilot plan request returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return copilotPlanDetails{}, "read Copilot plan response: " + err.Error()
	}
	plan, err := decodeCopilotPlan(body)
	if err != nil {
		return copilotPlanDetails{}, err.Error()
	}
	return plan, ""
}

func copilotGitHubToken() string {
	for _, name := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	if gh, err := exec.LookPath("gh"); err == nil {
		if output, err := exec.Command(gh, "auth", "token").Output(); err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	return ""
}

type copilotPlanResponse struct {
	Plan           string                  `json:"copilot_plan"`
	PlanName       string                  `json:"plan"`
	ResetAt        string                  `json:"quota_reset_date_utc"`
	QuotaSnapshots map[string]copilotQuota `json:"quota_snapshots"`
}

type copilotQuota struct {
	Entitlement float64 `json:"entitlement"`
	Remaining   float64 `json:"remaining"`
	Used        float64 `json:"used"`
	Unlimited   bool    `json:"unlimited"`
}

func decodeCopilotPlan(body []byte) (copilotPlanDetails, error) {
	var response copilotPlanResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return copilotPlanDetails{}, fmt.Errorf("decode Copilot plan: %w", err)
	}
	quota, ok := response.QuotaSnapshots["ai_credits"]
	if !ok {
		quota, ok = response.QuotaSnapshots["premium_interactions"]
	}
	if !ok {
		return copilotPlanDetails{}, fmt.Errorf("decode Copilot plan: AI credit quota unavailable")
	}
	used := quota.Used
	if used == 0 && quota.Entitlement >= quota.Remaining {
		used = quota.Entitlement - quota.Remaining
	}
	name := response.Plan
	if name == "" {
		name = response.PlanName
	}
	resetAt, _ := time.Parse(time.RFC3339, response.ResetAt)
	return copilotPlanDetails{
		Name:         name,
		UsedCredits:  used,
		TotalCredits: quota.Entitlement,
		Remaining:    quota.Remaining,
		ResetAt:      resetAt,
		Available:    true,
	}, nil
}

func decodeCopilotUsage(output []byte) (copilotUsageSnapshot, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return copilotUsageSnapshot{}, nil
	}
	var rows []copilotUsageRow
	if err := json.Unmarshal(output, &rows); err != nil {
		return copilotUsageSnapshot{}, fmt.Errorf("decode Copilot usage: %w", err)
	}
	var snapshot copilotUsageSnapshot
	for _, row := range rows {
		switch row.Kind {
		case "summary":
			snapshot.Summary = copilotUsageSummary{
				Sessions:       row.Sessions,
				ModelCalls:     row.ModelCalls,
				InputTokens:    row.InputTokens,
				OutputTokens:   row.OutputTokens,
				LifetimeTokens: row.Tokens,
				UserRequests:   row.UserRequests,
			}
		case "daily":
			snapshot.Daily = append(snapshot.Daily, copilotDailyUsage{
				StartDate:    row.Label,
				Tokens:       row.Tokens,
				UserRequests: row.UserRequests,
			})
		case "model":
			snapshot.Models = append(snapshot.Models, copilotUsageModel{
				Model:        row.Label,
				Tokens:       row.Tokens,
				ModelCalls:   row.ModelCalls,
				UserRequests: row.UserRequests,
			})
		}
	}
	sort.Slice(snapshot.Models, func(i, j int) bool {
		if snapshot.Models[i].Tokens == snapshot.Models[j].Tokens {
			return snapshot.Models[i].Model < snapshot.Models[j].Model
		}
		return snapshot.Models[i].Tokens > snapshot.Models[j].Tokens
	})
	return snapshot, nil
}

func (m model) viewUsage(contentRows int) string {
	if contentRows < 1 {
		return ""
	}
	selector := m.usageProviderSelectorLine()
	if contentRows == 1 {
		return selector
	}
	bodyRows := contentRows - 1
	body := m.viewCodexUsage(bodyRows)
	if m.usageProvider == copilotProvider {
		body = m.viewCopilotUsage(bodyRows)
	} else if m.usageProvider == kimiProvider {
		body = m.viewKimiUsage(bodyRows)
	}
	return selector + "\n" + body
}

func (m model) usageProviderSelectorLine() string {
	codex := "  [ CODEX ]"
	copilot := "    COPILOT  "
	kimi := "    KIMI  "
	if m.usageProvider == copilotProvider {
		codex = "    CODEX  "
		copilot = "  [ COPILOT ]"
	} else if m.usageProvider == kimiProvider {
		codex = "    CODEX  "
		kimi = "  [ KIMI ]"
	}
	return usagePaintLine(codex+copilot+kimi+"    ←/→ provider", m.width, usageBrightColor, true, usageHighlightColor)
}

func (m model) viewCopilotUsage(contentRows int) string {
	lines := m.copilotUsageHeaderLines()
	if m.copilotUsageLoading && !m.copilotUsage.hasData() {
		lines = append(lines,
			usageDividerLine(m.width),
			usageSectionLine("LOADING", m.width),
			usagePaintLine("  Reading local Copilot CLI usage…", m.width, usageMutedColor, false, usagePanelColor),
		)
		return fillUsageLines(lines, contentRows, m.width)
	}
	if m.copilotUsageErr != "" && !m.copilotUsage.hasData() {
		lines = append(lines,
			usageDividerLine(m.width),
			usageSectionLine("UNAVAILABLE", m.width),
			usagePaintLine("  "+m.copilotUsageErr, m.width, usageMutedColor, false, usagePanelColor),
		)
		return fillUsageLines(lines, contentRows, m.width)
	}

	if m.copilotUsage.Plan.Available {
		plan := m.copilotUsage.Plan
		lines = append(lines, usagePaintLine(fmt.Sprintf("  AI Credits %s AIC  •  Plan %s / %s AIC", formatAICredits(plan.UsedCredits), formatAICredits(plan.UsedCredits), formatAICredits(plan.TotalCredits)), m.width, usageMutedColor, false, usagePanelColor))
	} else {
		message := "  Plan information unavailable"
		if m.copilotUsage.PlanErr != "" {
			message += ": " + m.copilotUsage.PlanErr
		}
		lines = append(lines, usagePaintLine(message, m.width, usageMutedColor, false, usagePanelColor))
	}
	lines = append(lines,
		usageDividerLine(m.width),
		usageSectionLine("LOCAL CLI HISTORY", m.width),
		usageSidesLine(
			fmt.Sprintf("  %d sessions  •  %s model calls", m.copilotUsage.Summary.Sessions, formatTokenCount(m.copilotUsage.Summary.ModelCalls)),
			formatRequestCount(m.copilotUsage.Summary.UserRequests)+" weighted requests  ",
			m.width,
			usageTextColor,
			usageBrightColor,
		),
		usagePaintLine("  Account allowance and remaining quota: GitHub billing", m.width, usageMutedColor, false, usagePanelColor),
		usageDividerLine(m.width),
		usageSectionLine("TOKENS BY DAY", m.width),
	)
	days := recentCopilotDailyUsage(m.copilotUsage.Daily, time.Now(), 7)
	peak := int64(0)
	for _, day := range days {
		peak = max(peak, day.Tokens)
	}
	for index, day := range days {
		label := dayLabel(day.StartDate, index == len(days)-1)
		lines = append(lines, usageMetricBarLine(label, day.Tokens, peak, m.width, index == len(days)-1))
	}

	lines = append(lines, usageDividerLine(m.width), usageSectionLine("TOKENS BY MODEL", m.width))
	if len(m.copilotUsage.Models) == 0 {
		lines = append(lines, usagePaintLine("  No local Copilot CLI model usage", m.width, usageMutedColor, false, usageHighlightColor))
	} else {
		for _, model := range m.copilotUsage.Models {
			lines = append(lines, usageModelLine(modelUsage{Model: model.Model, Tokens: model.Tokens}, m.width))
		}
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func (m model) copilotUsageHeaderLines() []string {
	details := "LOCAL CLI"
	if m.copilotUsage.Summary.LifetimeTokens > 0 {
		details += "  •  " + formatTokenCount(m.copilotUsage.Summary.LifetimeTokens) + " TOKENS"
	}
	return []string{
		usagePaintLine("  ╭────╮  GitHub Copilot", m.width, usageBrightColor, true, usagePanelColor),
		usagePaintLine("  │ ◖◗ │  "+details, m.width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────╯", m.width, usageMutedColor, false, usagePanelColor),
	}
}

func recentCopilotDailyUsage(buckets []copilotDailyUsage, now time.Time, count int) []copilotDailyUsage {
	byDate := make(map[string]copilotDailyUsage, len(buckets))
	for _, bucket := range buckets {
		day := byDate[bucket.StartDate]
		day.StartDate = bucket.StartDate
		day.Tokens += bucket.Tokens
		day.UserRequests += bucket.UserRequests
		byDate[bucket.StartDate] = day
	}
	result := make([]copilotDailyUsage, 0, count)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for offset := count - 1; offset >= 0; offset-- {
		date := today.AddDate(0, 0, -offset).Format("2006-01-02")
		day := byDate[date]
		day.StartDate = date
		result = append(result, day)
	}
	return result
}

func formatRequestCount(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%.1f", value)
}

func formatAICredits(value float64) string {
	if value == float64(int64(value)) {
		return formatCommaInt(int64(value))
	}
	return formatCommaInt(int64(value)) + fmt.Sprintf(".%d", int((value-float64(int64(value)))*10))
}

func formatCommaInt(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := strconv.FormatInt(value, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func (m model) usageFooter() (string, string) {
	help := "  ←/→ provider  •  r refresh  •  Tab switch  •  q quit"
	if m.usageProvider == copilotProvider {
		if m.copilotUsageLoading {
			return help, "  reading local Copilot CLI usage…"
		}
		if m.copilotUsageErr != "" {
			return help, "  Copilot usage refresh failed: " + m.copilotUsageErr
		}
		if m.copilotUsageRefreshedAt.IsZero() {
			return help, "  Copilot usage not loaded"
		}
		return help, "  Copilot refreshed " + m.copilotUsageRefreshedAt.Format("15:04:05")
	}
	if m.usageProvider == kimiProvider {
		if m.kimiHistoryOpen {
			return "  ↑/↓ session  •  Enter/Esc back  •  q quit", "  Kimi historical sessions"
		}
		if m.kimiUsageLoading {
			return help, "  reading local Kimi Code usage…"
		}
		if m.kimiUsageErr != "" {
			return help, "  Kimi usage refresh failed: " + m.kimiUsageErr
		}
		if m.kimiUsageRefreshedAt.IsZero() {
			return help, "  Kimi usage not loaded"
		}
		return help, "  Kimi refreshed " + m.kimiUsageRefreshedAt.Format("15:04:05")
	}
	if m.codexUsageLoading {
		return help, "  fetching Codex quotas and usage…"
	}
	if m.codexUsageErr != "" {
		return help, "  Codex usage refresh failed: " + m.codexUsageErr
	}
	if m.codexUsageRefreshedAt.IsZero() {
		return help, "  Codex usage not loaded"
	}
	return help, "  Codex refreshed " + m.codexUsageRefreshedAt.Format("15:04:05")
}

func (snapshot copilotUsageSnapshot) hasData() bool {
	return snapshot.Plan.Available || snapshot.Summary.Sessions > 0 || snapshot.Summary.LifetimeTokens > 0 || len(snapshot.Daily) > 0
}
