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

const copilotSessionHistoryLimit = 500

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

type copilotDailyModelUsage struct {
	StartDate string
	Model     string
	Tokens    int64
}

type copilotUsageModel struct {
	Model        string
	Tokens       int64
	ModelCalls   int64
	UserRequests float64
}

type copilotHistoricalSession struct {
	ID             string  `json:"id"`
	Title          string  `json:"summary"`
	WorkDir        string  `json:"cwd"`
	Repository     string  `json:"repository"`
	HostType       string  `json:"host_type"`
	GitBranch      string  `json:"branch"`
	Created        string  `json:"created_at"`
	Updated        string  `json:"updated_at"`
	Model          string  `json:"model"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	LifetimeTokens int64   `json:"tokens"`
	ModelCalls     int64   `json:"model_calls"`
	UserRequests   float64 `json:"user_requests"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	Summary      copilotUsageSummary
	Daily        []copilotDailyUsage
	DailyByModel []copilotDailyModelUsage
	Models       []copilotUsageModel
	Plan         copilotPlanDetails
	PlanErr      string
	History      []copilotHistoricalSession
}

type copilotUsageResultMsg struct {
	snapshot   copilotUsageSnapshot
	refreshed  time.Time
	err        error
	historyErr error
}

type copilotUsageRow struct {
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	Model        string  `json:"model"`
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
		history, historyErr := readCopilotHistoricalSessions()
		snapshot.History = history
		return copilotUsageResultMsg{
			snapshot:   snapshot,
			refreshed:  time.Now(),
			err:        err,
			historyErr: historyErr,
		}
	}
}

func readCopilotHistoricalSessions() ([]copilotHistoricalSession, error) {
	home, err := copilotHome()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, "session-store.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("find Copilot session store: %w", err)
	}
	query := fmt.Sprintf(`
		SELECT
			s.id,
			COALESCE(s.summary, '') AS summary,
			COALESCE(s.cwd, '') AS cwd,
			COALESCE(s.repository, '') AS repository,
			COALESCE(s.host_type, '') AS host_type,
			COALESCE(s.branch, '') AS branch,
			COALESCE(s.created_at, '') AS created_at,
			CASE
				WHEN COALESCE(MAX(a.created_at), '') > COALESCE(s.updated_at, '') THEN MAX(a.created_at)
				ELSE COALESCE(s.updated_at, '')
			END AS updated_at,
			COALESCE((
				SELECT latest.model
				FROM assistant_usage_events latest
				WHERE latest.session_id = s.id
				ORDER BY latest.id DESC
				LIMIT 1
			), '') AS model,
			COALESCE(SUM(a.input_tokens), 0) AS input_tokens,
			COALESCE(SUM(a.output_tokens), 0) AS output_tokens,
			COALESCE(SUM(COALESCE(a.input_tokens, 0) + COALESCE(a.output_tokens, 0)), 0) AS tokens,
			COUNT(a.id) AS model_calls,
			COALESCE(SUM(CASE WHEN a.initiator = 'user' THEN COALESCE(a.request_multiplier, 1.0) ELSE 0 END), 0) AS user_requests
		FROM sessions s
		LEFT JOIN assistant_usage_events a ON a.session_id = s.id
		GROUP BY s.id
		ORDER BY updated_at DESC
		LIMIT %d
	`, copilotSessionHistoryLimit)
	output, err := exec.Command("sqlite3", "-readonly", "-json", path, query).Output()
	if err != nil {
		return nil, fmt.Errorf("read Copilot session history: %w", err)
	}
	return decodeCopilotHistoricalSessions(output)
}

func decodeCopilotHistoricalSessions(output []byte) ([]copilotHistoricalSession, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, nil
	}
	var sessions []copilotHistoricalSession
	if err := json.Unmarshal(output, &sessions); err != nil {
		return nil, fmt.Errorf("decode Copilot session history: %w", err)
	}
	for index := range sessions {
		session := &sessions[index]
		session.ID = sanitizeProcessCommand(session.ID)
		session.WorkDir = sanitizeProcessCommand(session.WorkDir)
		session.Repository = sanitizeProcessCommand(session.Repository)
		session.HostType = sanitizeProcessCommand(session.HostType)
		session.GitBranch = sanitizeProcessCommand(session.GitBranch)
		session.Model = sanitizeProcessCommand(session.Model)
		session.Title = emptyFallback(sanitizeProcessCommand(session.Title), firstNonEmpty(filepath.Base(session.WorkDir), session.Repository, "Copilot session"))
		session.CreatedAt = parseCopilotTime(session.Created)
		session.UpdatedAt = parseCopilotTime(session.Updated)
	}
	return sessions, nil
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
				'' AS model,
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
				'' AS model,
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
				substr(created_at, 1, 10) || char(0) || model AS sort_label,
				'daily_model' AS kind,
				substr(created_at, 1, 10) AS label,
				model AS model,
				SUM(input_tokens + output_tokens) AS tokens,
				SUM(user_requests) AS user_requests,
				COUNT(*) AS model_calls,
				0 AS sessions,
				SUM(input_tokens) AS input_tokens,
				SUM(output_tokens) AS output_tokens
			FROM usage
			GROUP BY substr(created_at, 1, 10), model
			UNION ALL
			SELECT
				3 AS sort_group,
				model AS sort_label,
				'model' AS kind,
				model AS label,
				'' AS model,
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
		model,
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
		case "daily_model":
			snapshot.DailyByModel = append(snapshot.DailyByModel, copilotDailyModelUsage{
				StartDate: row.Label,
				Model:     row.Model,
				Tokens:    row.Tokens,
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
	if m.copilotHistoryOpen {
		return m.viewCopilotHistory(contentRows)
	}
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
	)

	lines = append(lines, usageDividerLine(m.width), usageSectionLine("TOKENS BY MODEL", m.width))
	modelNames := make([]string, 0, len(m.copilotUsage.Models)+len(m.copilotUsage.DailyByModel))
	for _, model := range m.copilotUsage.Models {
		modelNames = append(modelNames, model.Model)
	}
	for _, entry := range m.copilotUsage.DailyByModel {
		modelNames = append(modelNames, entry.Model)
	}
	modelColors := modelUsageColorAssignments(modelNames)
	if len(m.copilotUsage.Models) == 0 {
		lines = append(lines, usagePaintLine("  No local Copilot CLI model usage", m.width, usageMutedColor, false, usageHighlightColor))
	} else {
		models := make([]modelUsage, 0, len(m.copilotUsage.Models))
		peak := int64(0)
		for _, model := range m.copilotUsage.Models {
			models = append(models, modelUsage{Model: model.Model, Tokens: model.Tokens})
			peak = max(peak, model.Tokens)
		}
		for _, model := range colorModelUsageWithAssignments(models, modelColors) {
			lines = append(lines, usageModelBarLine(model, peak, m.width))
		}
	}

	title := "TOKENS BY DAY"
	if len(m.copilotUsage.DailyByModel) > 0 {
		title += " · MODEL"
	}
	dayCount := min(7, max(contentRows-len(lines)-2, 1))
	lines = append(lines, usageDividerLine(m.width), usageSectionLine(title, m.width))
	if len(m.copilotUsage.DailyByModel) > 0 {
		days := recentCopilotDailyModelUsage(m.copilotUsage.DailyByModel, time.Now(), dayCount)
		for index := range days {
			days[index].Models = colorModelUsageWithAssignments(days[index].Models, modelColors)
		}
		peak := int64(0)
		for _, day := range days {
			total := int64(0)
			for _, model := range day.Models {
				total += model.Tokens
			}
			peak = max(peak, total)
		}
		for index, day := range days {
			label := dayLabel(day.StartDate, index == len(days)-1)
			lines = append(lines, usageModelStackBarLine(label, day.Models, peak, m.width, index == len(days)-1))
		}
	} else {
		days := recentCopilotDailyUsage(m.copilotUsage.Daily, time.Now(), dayCount)
		peak := int64(0)
		for _, day := range days {
			peak = max(peak, day.Tokens)
		}
		for index, day := range days {
			label := dayLabel(day.StartDate, index == len(days)-1)
			lines = append(lines, usageMetricBarLine(label, day.Tokens, peak, m.width, index == len(days)-1))
		}
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func (m model) viewCopilotHistory(contentRows int) string {
	lines := []string{
		usagePaintLine("  ╭────╮  GitHub Copilot", m.width, usageBrightColor, true, usagePanelColor),
		usagePaintLine(fmt.Sprintf("  │ ◖◗ │  HISTORICAL SESSIONS (%d)  •  Enter/Esc back", len(m.copilotUsage.History)), m.width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────╯", m.width, usageMutedColor, false, usagePanelColor),
		usageDividerLine(m.width),
	}
	start, end := codexHistoryWindow(len(m.copilotUsage.History), m.copilotHistoryCursor, contentRows-len(lines))
	for index := start; index < end; index++ {
		session := m.copilotUsage.History[index]
		cursor := " "
		if index == m.copilotHistoryCursor {
			cursor = ">"
		}
		cwd := "unknown cwd"
		if session.WorkDir != "" {
			cwd = truncateKimiText(filepath.Base(session.WorkDir), 24)
		}
		model := emptyFallback(session.Model, "unknown model")
		metadata := make([]string, 0, 3)
		if session.Repository != "" {
			metadata = append(metadata, truncateKimiText(session.Repository, 28))
		}
		if session.GitBranch != "" {
			metadata = append(metadata, "branch "+truncateKimiText(session.GitBranch, 20))
		}
		if session.HostType != "" {
			metadata = append(metadata, session.HostType)
		}
		if len(metadata) == 0 {
			metadata = append(metadata, "local session")
		}
		lines = append(lines,
			usagePaintLine(fmt.Sprintf("%s %-28s %s", cursor, truncateKimiText(session.Title, 28), formatSessionTime(session.UpdatedAt)), m.width, usageBrightColor, index == m.copilotHistoryCursor, usageHighlightColor),
			usagePaintLine(fmt.Sprintf("    %-20s %s  •  %s tokens", truncateKimiText(model, 20), cwd, formatTokenCount(session.LifetimeTokens)), m.width, usageMutedColor, false, usagePanelColor),
			usagePaintLine(fmt.Sprintf("    %s  •  %s calls  •  %s requests", strings.Join(metadata, "  •  "), formatTokenCount(session.ModelCalls), formatRequestCount(session.UserRequests)), m.width, usageMutedColor, false, usagePanelColor),
		)
	}
	if len(m.copilotUsage.History) == 0 {
		message := "  No historical Copilot sessions"
		if m.copilotHistoryErr != "" {
			message = "  Session history unavailable: " + m.copilotHistoryErr
		} else if m.copilotUsageLoading {
			message = "  Loading historical Copilot sessions…"
		}
		lines = append(lines, usagePaintLine(message, m.width, usageMutedColor, false, usagePanelColor))
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

func recentCopilotDailyModelUsage(entries []copilotDailyModelUsage, now time.Time, count int) []codexDailyModelUsageBucket {
	byDate := make(map[string]map[string]int64)
	for _, entry := range entries {
		if byDate[entry.StartDate] == nil {
			byDate[entry.StartDate] = make(map[string]int64)
		}
		byDate[entry.StartDate][entry.Model] += entry.Tokens
	}
	result := make([]codexDailyModelUsageBucket, 0, count)
	today := startOfDay(now)
	for offset := count - 1; offset >= 0; offset-- {
		date := today.AddDate(0, 0, -offset).Format("2006-01-02")
		models := make([]modelUsage, 0, len(byDate[date]))
		for model, tokens := range byDate[date] {
			models = append(models, modelUsage{Model: model, Tokens: tokens})
		}
		sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
		result = append(result, codexDailyModelUsageBucket{StartDate: date, Models: models})
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
		if m.copilotHistoryOpen {
			return "  ↑/↓ session  •  Enter/Esc back  •  q quit", "  Copilot historical sessions"
		}
		help = "  Enter sessions  •  ←/→ provider  •  r refresh  •  Tab switch  •  q quit"
		if m.copilotUsageLoading {
			return help, "  reading local Copilot CLI usage…"
		}
		if m.copilotUsageErr != "" {
			return help, "  Copilot usage refresh failed: " + m.copilotUsageErr
		}
		if m.copilotHistoryErr != "" {
			return help, "  Copilot session history unavailable: " + m.copilotHistoryErr
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
	if m.codexHistoryOpen {
		return "  ↑/↓ session  •  Enter/Esc back  •  q quit", "  Codex historical sessions"
	}
	if len(m.codexUsage.History) > 0 {
		help = "  Enter sessions  •  ←/→ provider  •  r refresh  •  Tab switch  •  q quit"
	}
	if m.codexUsageLoading {
		return help, "  fetching Codex quotas and usage…"
	}
	if m.codexUsageErr != "" {
		return help, "  Codex usage refresh failed: " + m.codexUsageErr
	}
	if m.codexHistoryErr != "" {
		return help, "  Codex session history unavailable: " + m.codexHistoryErr
	}
	if m.codexUsageRefreshedAt.IsZero() {
		return help, "  Codex usage not loaded"
	}
	return help, "  Codex refreshed " + m.codexUsageRefreshedAt.Format("15:04:05")
}

func (snapshot copilotUsageSnapshot) hasData() bool {
	return snapshot.Plan.Available || snapshot.Summary.Sessions > 0 || snapshot.Summary.LifetimeTokens > 0 || len(snapshot.Daily) > 0 || len(snapshot.DailyByModel) > 0
}
