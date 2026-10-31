package tui

import (
	"bytes"
	"context"
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

	"github.com/DanBradbury/firekeeper/internal/sqlitejson"
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
	output, err := sqlitejson.Run(context.Background(), "-readonly", "-json", path, query)
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
	output, err := sqlitejson.Run(context.Background(), "-readonly", "-json", path, query)
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
	return selector + "\n" + m.viewUsagePage(contentRows-1)
}

func (m model) usageProviderName() string {
	switch m.usageProvider {
	case copilotProvider:
		return "copilot"
	case kimiProvider:
		return "kimi"
	case claudeProvider:
		return "claude"
	default:
		return "codex"
	}
}

// usageProviderSelectorLine mirrors the tab bar styling of the Processes tab:
// the active provider is a highlighted chip, the others are muted.
func (m model) usageProviderSelectorLine() string {
	line := dashPaint(" ", usageTextColor, false)
	for _, name := range []string{"codex", "copilot", "kimi", "claude"} {
		label := strings.ToUpper(name)
		if name == m.usageProviderName() {
			line += usagePaint("[ "+label+" ]", dashOrange, true, usageHighlightColor) + dashPaint(" ", usageTextColor, false)
		} else {
			line += dashPaint("  "+label+"   ", usageMutedColor, false)
		}
	}
	return dashFit(line, m.width, usagePanelColor)
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

func (m model) usageFooter() string {
	keys := []footerKey{{"←/→", "provider"}, {"r", "refresh"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"}}
	if m.usageSessionCount() > 0 {
		keys = append([]footerKey{{"↑↓", "session"}, {"enter", "chat"}}, keys...)
	}
	return renderFooterKeys(keys, m.width)
}

func (snapshot copilotUsageSnapshot) hasData() bool {
	return snapshot.Plan.Available || snapshot.Summary.Sessions > 0 || snapshot.Summary.LifetimeTokens > 0 || len(snapshot.Daily) > 0 || len(snapshot.DailyByModel) > 0
}
