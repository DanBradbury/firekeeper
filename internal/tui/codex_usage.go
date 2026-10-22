package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	codexUsageTimeout               = 15 * time.Second
	codexUsageHistoryDays           = 7
	codexUsageRolloutMaxBytes int64 = 8 << 20
	codexSessionHistoryLimit        = 500
)

const (
	usagePanelColor     = "24;26;38"
	usageTextColor      = "174;185;220"
	usageBrightColor    = "205;214;244"
	usageMutedColor     = "103;114;151"
	usageTrackColor     = "48;53;72"
	usageDividerColor   = "42;46;63"
	usageHighlightColor = "55;60;78"
)

var modelUsageColors = []string{
	"137;180;250", // blue
	"250;179;135", // orange
	"166;227;161", // green
	"243;139;168", // red
	"203;166;247", // purple
	"249;226;175", // yellow
	"148;226;213", // teal
	"245;194;231", // pink
	"137;220;235", // sky
	"180;190;254", // lavender
}

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

type codexDailyModelUsage struct {
	StartDate string
	Model     string
	Tokens    int64
}

type codexHistoricalSession struct {
	ID        string `json:"id"`
	Title     string `json:"display_name"`
	WorkDir   string `json:"cwd"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	GitBranch string `json:"git_branch"`
	UpdatedMS int64  `json:"updated_at_ms"`
	Tokens    int64  `json:"tokens_used"`
	Archived  int    `json:"archived"`
	UpdatedAt time.Time
}

type codexUsageSnapshot struct {
	RateLimits   map[string]codexRateLimit
	ResetCredits int
	Summary      codexUsageSummary
	Daily        []codexDailyUsage
	DailyByModel []codexDailyModelUsage
	History      []codexHistoricalSession
}

type codexUsageResultMsg struct {
	snapshot   codexUsageSnapshot
	refreshed  time.Time
	err        error
	historyErr error
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

type codexRolloutUsageEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type           string `json:"type"`
		ThreadSettings struct {
			Model string `json:"model"`
		} `json:"thread_settings"`
		Info struct {
			TotalTokenUsage struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func refreshCodexUsage() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := fetchCodexUsage()
		history, historyErr := readCodexHistoricalSessions()
		snapshot.History = history
		return codexUsageResultMsg{
			snapshot:   snapshot,
			refreshed:  time.Now(),
			err:        err,
			historyErr: historyErr,
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
	if dailyByModel, err := readCodexDailyModelUsage(time.Now(), codexUsageHistoryDays); err == nil {
		snapshot.DailyByModel = dailyByModel
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

func readCodexHistoricalSessions() ([]codexHistoricalSession, error) {
	statePath, err := codexStatePath()
	if err != nil {
		return nil, err
	}
	columnsOutput, err := exec.Command("sqlite3", "-readonly", "-json", statePath, "PRAGMA table_info(threads)").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect Codex state: %w", err)
	}
	var schema []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(columnsOutput, &schema); err != nil {
		return nil, fmt.Errorf("decode Codex state schema: %w", err)
	}
	columns := make(map[string]bool, len(schema))
	for _, column := range schema {
		columns[column.Name] = true
	}
	query, err := codexHistoryQuery(columns)
	if err != nil {
		return nil, err
	}
	output, err := exec.Command("sqlite3", "-readonly", "-json", statePath, query).Output()
	if err != nil {
		return nil, fmt.Errorf("read Codex session history: %w", err)
	}
	return decodeCodexHistoricalSessions(output)
}

func codexHistoryQuery(columns map[string]bool) (string, error) {
	if !columns["id"] {
		return "", fmt.Errorf("Codex state threads table has no id column")
	}
	title := codexHistoryTextExpression(columns, "id", "name", "title", "preview")
	cwd := codexHistoryTextExpression(columns, "''", "cwd")
	model := codexHistoryTextExpression(columns, "''", "model")
	source := codexHistoryTextExpression(columns, "''", "thread_source", "source")
	branch := codexHistoryTextExpression(columns, "''", "git_branch")
	updated := codexHistoryUpdatedExpression(columns)
	tokens := "0"
	if columns["tokens_used"] {
		tokens = "COALESCE(tokens_used, 0)"
	}
	archived := "0"
	if columns["archived"] {
		archived = "COALESCE(archived, 0)"
	}
	order := updated
	if columns["recency_at_ms"] {
		order = "COALESCE(NULLIF(recency_at_ms, 0), " + updated + ")"
	}
	return fmt.Sprintf(`
		SELECT
			id,
			%s AS display_name,
			%s AS cwd,
			%s AS model,
			%s AS source,
			%s AS git_branch,
			%s AS updated_at_ms,
			%s AS tokens_used,
			%s AS archived
		FROM threads
		ORDER BY %s DESC
		LIMIT %d
	`, title, cwd, model, source, branch, updated, tokens, archived, order, codexSessionHistoryLimit), nil
}

func codexHistoryTextExpression(columns map[string]bool, fallback string, candidates ...string) string {
	parts := make([]string, 0, len(candidates)+1)
	for _, candidate := range candidates {
		if columns[candidate] {
			parts = append(parts, "NULLIF("+candidate+", '')")
		}
	}
	parts = append(parts, fallback)
	if len(parts) == 1 {
		return parts[0]
	}
	return "COALESCE(" + strings.Join(parts, ", ") + ")"
}

func codexHistoryUpdatedExpression(columns map[string]bool) string {
	var parts []string
	if columns["updated_at_ms"] {
		parts = append(parts, "NULLIF(updated_at_ms, 0)")
	}
	if columns["updated_at"] {
		parts = append(parts, "NULLIF(updated_at, 0) * 1000")
	}
	parts = append(parts, "0")
	if len(parts) == 1 {
		return parts[0]
	}
	return "COALESCE(" + strings.Join(parts, ", ") + ")"
}

func decodeCodexHistoricalSessions(output []byte) ([]codexHistoricalSession, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, nil
	}
	var sessions []codexHistoricalSession
	if err := json.Unmarshal(output, &sessions); err != nil {
		return nil, fmt.Errorf("decode Codex session history: %w", err)
	}
	for index := range sessions {
		session := &sessions[index]
		session.ID = sanitizeProcessCommand(session.ID)
		session.Title = emptyFallback(sanitizeProcessCommand(session.Title), "Codex session")
		session.WorkDir = sanitizeProcessCommand(session.WorkDir)
		session.Model = sanitizeProcessCommand(session.Model)
		session.Source = sanitizeProcessCommand(session.Source)
		session.GitBranch = sanitizeProcessCommand(session.GitBranch)
		if session.UpdatedMS > 0 {
			session.UpdatedAt = time.UnixMilli(session.UpdatedMS)
		}
	}
	return sessions, nil
}

type modelUsage struct {
	Model  string
	Tokens int64
	Color  string
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

type codexDailyModelUsageBucket struct {
	StartDate string
	Models    []modelUsage
}

func readCodexDailyModelUsage(now time.Time, days int) ([]codexDailyModelUsage, error) {
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find Codex home: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	root := filepath.Join(codexHome, "sessions")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("read Codex sessions: %w", err)
	}

	usage := make(map[string]map[string]int64)
	cutoff := startOfDay(now).AddDate(0, 0, -(max(days, 1) - 1))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "rollout-") || filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}

		reader := io.Reader(file)
		skipInitial := false
		if info.Size() > codexUsageRolloutMaxBytes {
			if _, err := file.Seek(info.Size()-codexUsageRolloutMaxBytes, io.SeekStart); err != nil {
				return nil
			}
			buffered := bufio.NewReader(file)
			if _, err := buffered.ReadBytes('\n'); err != nil && err != io.EOF {
				return nil
			}
			reader = buffered
			skipInitial = true
		}
		entries, err := scanCodexRolloutModelUsage(reader, now, days, skipInitial)
		closeErr := file.Close()
		if err != nil {
			return nil
		}
		if closeErr != nil {
			return nil
		}
		for _, entry := range entries {
			if usage[entry.StartDate] == nil {
				usage[entry.StartDate] = make(map[string]int64)
			}
			usage[entry.StartDate][entry.Model] += entry.Tokens
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan Codex rollouts: %w", err)
	}

	result := make([]codexDailyModelUsage, 0)
	for date, models := range usage {
		for model, tokens := range models {
			if tokens > 0 {
				result = append(result, codexDailyModelUsage{StartDate: date, Model: model, Tokens: tokens})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartDate == result[j].StartDate {
			return result[i].Model < result[j].Model
		}
		return result[i].StartDate < result[j].StartDate
	})
	return result, nil
}

func scanCodexRolloutModelUsage(reader io.Reader, now time.Time, days int, skipInitial bool) ([]codexDailyModelUsage, error) {
	cutoff := startOfDay(now).AddDate(0, 0, -(max(days, 1) - 1))
	usage := make(map[string]map[string]int64)
	model := "unknown"
	var previousTotal int64
	hasPreviousTotal := false
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		var event codexRolloutUsageEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type != "event_msg" {
			continue
		}
		if event.Payload.Type == "thread_settings_applied" && strings.TrimSpace(event.Payload.ThreadSettings.Model) != "" {
			model = sanitizeProcessCommand(event.Payload.ThreadSettings.Model)
			if model == "" {
				model = "unknown"
			}
			continue
		}
		if event.Payload.Type != "token_count" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			continue
		}
		total := event.Payload.Info.TotalTokenUsage.TotalTokens
		if total < 0 {
			continue
		}
		if !hasPreviousTotal {
			previousTotal = total
			hasPreviousTotal = true
			if skipInitial {
				continue
			}
		} else if total >= previousTotal {
			total -= previousTotal
			previousTotal += total
		} else {
			previousTotal = total
			continue
		}
		localTime := timestamp.In(now.Location())
		if localTime.Before(cutoff) {
			continue
		}
		date := localTime.Format("2006-01-02")
		if usage[date] == nil {
			usage[date] = make(map[string]int64)
		}
		usage[date][model] += total
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	result := make([]codexDailyModelUsage, 0)
	for date, models := range usage {
		for model, tokens := range models {
			if tokens > 0 {
				result = append(result, codexDailyModelUsage{StartDate: date, Model: model, Tokens: tokens})
			}
		}
	}
	return result, nil
}

func recentCodexDailyModelUsage(entries []codexDailyModelUsage, now time.Time, count int) []codexDailyModelUsageBucket {
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

func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
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

func activeCodexModelUsage(groups []processGroup) []modelUsage {
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
	models := make([]modelUsage, 0, len(totals))
	for model, tokens := range totals {
		models = append(models, modelUsage{Model: model, Tokens: tokens})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Tokens == models[j].Tokens {
			return models[i].Model < models[j].Model
		}
		return models[i].Tokens > models[j].Tokens
	})
	return models
}

func colorModelUsage(models []modelUsage) []modelUsage {
	names := make([]string, len(models))
	for index, model := range models {
		names[index] = model.Model
	}
	return colorModelUsageWithAssignments(models, modelUsageColorAssignments(names))
}

func colorModelUsageWithAssignments(models []modelUsage, assignments map[string]string) []modelUsage {
	colored := append([]modelUsage(nil), models...)
	for index := range colored {
		colored[index].Color = assignments[modelUsageColorKey(colored[index].Model)]
		if colored[index].Color == "" {
			colored[index].Color = modelUsageColor(colored[index].Model)
		}
	}
	return colored
}

func modelUsageColorAssignments(models []string) map[string]string {
	unique := make(map[string]bool, len(models))
	keys := make([]string, 0, len(models))
	for _, model := range models {
		key := modelUsageColorKey(model)
		if unique[key] {
			continue
		}
		unique[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assignments := make(map[string]string, len(keys))
	used := make([]bool, len(modelUsageColors))
	for _, key := range keys {
		start := modelUsageColorIndex(key)
		colorIndex := start
		for offset := 0; offset < len(modelUsageColors); offset++ {
			candidate := (start + offset) % len(modelUsageColors)
			if !used[candidate] {
				colorIndex = candidate
				used[candidate] = true
				break
			}
		}
		assignments[key] = modelUsageColors[colorIndex]
	}
	return assignments
}

func modelUsageColorKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func modelUsageColor(model string) string {
	return modelUsageColors[modelUsageColorIndex(modelUsageColorKey(model))]
}

func modelUsageColorIndex(model string) int {
	hash := uint32(2166136261)
	for _, character := range model {
		hash ^= uint32(character)
		hash *= 16777619
	}
	return int(hash) % len(modelUsageColors)
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
	prefix := "  " + padText(label, labelWidth) + " "
	suffix := " " + fmt.Sprintf("%*s", valueWidth, formatTokenCount(value)) + " "
	return usagePaint(prefix, labelColor, emphasized, usagePanelColor) +
		usagePaint(strings.Repeat("━", filled), fillColor, true, usagePanelColor) +
		usagePaint(strings.Repeat("━", barWidth-filled), usageTrackColor, false, usagePanelColor) +
		usagePaint(suffix, labelColor, emphasized, usagePanelColor)
}

func usageModelStackBarLine(label string, models []modelUsage, maximum int64, width int, emphasized bool) string {
	labelWidth := min(8, max(width/8, 5))
	valueWidth := min(9, max(width/10, 6))
	barWidth := max(width-labelWidth-valueWidth-5, 1)
	total := int64(0)
	for _, model := range models {
		total += model.Tokens
	}
	filled := int(float64(barWidth)*float64(total)/float64(max(maximum, 1)) + 0.5)
	if total > 0 {
		filled = max(filled, 1)
	}
	filled = min(filled, barWidth)
	labelColor := usageMutedColor
	if emphasized {
		labelColor = usageBrightColor
	}
	prefix := "  " + padText(label, labelWidth) + " "
	suffix := " " + fmt.Sprintf("%*s", valueWidth, formatTokenCount(total)) + " "
	var bar strings.Builder
	remainingTokens := total
	remainingWidth := filled
	for index, model := range models {
		segment := 0
		if remainingTokens > 0 {
			segment = int(float64(remainingWidth)*float64(model.Tokens)/float64(remainingTokens) + 0.5)
		}
		if index == len(models)-1 {
			segment = remainingWidth
		}
		segment = min(segment, remainingWidth)
		if segment > 0 {
			bar.WriteString(usagePaint(strings.Repeat("━", segment), model.Color, true, usagePanelColor))
		}
		remainingTokens -= model.Tokens
		remainingWidth -= segment
	}
	if remainingWidth > 0 {
		bar.WriteString(usagePaint(strings.Repeat("━", remainingWidth), usageTrackColor, false, usagePanelColor))
	}
	bar.WriteString(usagePaint(strings.Repeat("━", barWidth-filled), usageTrackColor, false, usagePanelColor))
	return usagePaint(prefix, labelColor, emphasized, usagePanelColor) + bar.String() + usagePaint(suffix, labelColor, emphasized, usagePanelColor)
}

func usagePaint(value, foreground string, bold bool, background string) string {
	weight := "22"
	if bold {
		weight = "1"
	}
	return fmt.Sprintf("\x1b[%s;38;2;%s;48;2;%sm%s\x1b[0m", weight, foreground, background, value)
}

func (snapshot codexUsageSnapshot) hasData() bool {
	return len(snapshot.RateLimits) > 0 || len(snapshot.Daily) > 0 || len(snapshot.DailyByModel) > 0 || snapshot.Summary.LifetimeTokens != nil
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
