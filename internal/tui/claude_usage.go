package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// claudeUsageHistoryLimit bounds the session browser, like the Codex and
// Copilot browsers.
const claudeUsageHistoryLimit = 500

type claudeUsageSummary struct {
	Sessions       int64
	Messages       int64
	InputTokens    int64
	CacheRead      int64
	CacheCreation  int64
	OutputTokens   int64
	LifetimeTokens int64
}

type claudeDailyUsage struct {
	StartDate string
	Tokens    int64
}

type claudeUsageModel struct {
	Model  string
	Tokens int64
}

type claudeHistoricalSession struct {
	ID             string
	Title          string
	WorkDir        string
	GitBranch      string
	Model          string
	UpdatedAt      time.Time
	InputTokens    int64
	CacheRead      int64
	CacheCreation  int64
	OutputTokens   int64
	LifetimeTokens int64
}

type claudeUsageSnapshot struct {
	Summary claudeUsageSummary
	Daily   []claudeDailyUsage
	Models  []claudeUsageModel
	History []claudeHistoricalSession
}

type claudeUsageResultMsg struct {
	snapshot  claudeUsageSnapshot
	refreshed time.Time
	err       error
}

// claudeUsageRecord holds the transcript fields the Usage tab reads. Message
// content is never decoded.
type claudeUsageRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	AITitle   string `json:"aiTitle"`
	AgentName string `json:"agentName"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func refreshClaudeUsage() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := fetchClaudeUsage()
		return claudeUsageResultMsg{snapshot: snapshot, refreshed: time.Now(), err: err}
	}
}

// fetchClaudeUsage sums assistant message usage from Claude Code transcripts
// under CLAUDE_CONFIG_DIR/projects. Claude Code repeats a response's usage on
// every content block line, so each message id is counted once, across all
// files, which also keeps messages copied into a resumed session from
// counting twice. Plan limits are not stored locally, so only token history
// is shown.
func fetchClaudeUsage() (claudeUsageSnapshot, error) {
	dir, err := claudeConfigDir()
	if err != nil {
		return claudeUsageSnapshot{}, err
	}
	root := filepath.Join(dir, "projects")
	if _, err := os.Stat(root); err != nil {
		return claudeUsageSnapshot{}, fmt.Errorf("find Claude Code session history: %w", err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return claudeUsageSnapshot{}, fmt.Errorf("list Claude Code session history: %w", err)
	}
	// Oldest first, so a message copied into a resumed session stays with the
	// session that produced it.
	modTimes := make(map[string]time.Time, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			modTimes[path] = info.ModTime()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool {
		if !modTimes[paths[i]].Equal(modTimes[paths[j]]) {
			return modTimes[paths[i]].Before(modTimes[paths[j]])
		}
		return paths[i] < paths[j]
	})

	snapshot := claudeUsageSnapshot{}
	models := make(map[string]int64)
	daily := make(map[string]int64)
	seen := make(map[string]bool)
	for _, path := range paths {
		session, ok := readClaudeUsageFile(path, &snapshot.Summary, models, daily, seen)
		if !ok {
			continue
		}
		snapshot.Summary.Sessions++
		snapshot.History = append(snapshot.History, session)
	}
	sort.Slice(snapshot.History, func(i, j int) bool { return snapshot.History[i].UpdatedAt.After(snapshot.History[j].UpdatedAt) })
	if len(snapshot.History) > claudeUsageHistoryLimit {
		snapshot.History = snapshot.History[:claudeUsageHistoryLimit]
	}
	for day, tokens := range daily {
		snapshot.Daily = append(snapshot.Daily, claudeDailyUsage{StartDate: day, Tokens: tokens})
	}
	sort.Slice(snapshot.Daily, func(i, j int) bool { return snapshot.Daily[i].StartDate < snapshot.Daily[j].StartDate })
	for model, tokens := range models {
		snapshot.Models = append(snapshot.Models, claudeUsageModel{Model: model, Tokens: tokens})
	}
	sort.Slice(snapshot.Models, func(i, j int) bool {
		if snapshot.Models[i].Tokens == snapshot.Models[j].Tokens {
			return snapshot.Models[i].Model < snapshot.Models[j].Model
		}
		return snapshot.Models[i].Tokens > snapshot.Models[j].Tokens
	})
	return snapshot, nil
}

// readClaudeUsageFile adds one transcript's usage to the running totals and
// returns its session row. A file that cannot be opened is skipped.
func readClaudeUsageFile(path string, summary *claudeUsageSummary, models, daily map[string]int64, seen map[string]bool) (claudeHistoricalSession, bool) {
	file, err := os.Open(path)
	if err != nil {
		return claudeHistoricalSession{}, false
	}
	defer file.Close()
	session := claudeHistoricalSession{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	if info, err := file.Stat(); err == nil {
		session.UpdatedAt = info.ModTime()
	}
	reader := bufio.NewReaderSize(file, 256<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trackClaudeUsageLine(line, &session, summary, models, daily, seen)
		}
		if err != nil {
			break
		}
	}
	session.Title = emptyFallback(sanitizeProcessCommand(session.Title), "Claude Code session")
	session.WorkDir = sanitizeProcessCommand(session.WorkDir)
	session.GitBranch = sanitizeProcessCommand(session.GitBranch)
	session.Model = sanitizeProcessCommand(session.Model)
	return session, true
}

func trackClaudeUsageLine(line []byte, session *claudeHistoricalSession, summary *claudeUsageSummary, models, daily map[string]int64, seen map[string]bool) {
	// Most lines are prompts and tool output; decode only the ones that can
	// carry usage, a title, or the working directory.
	if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`"aiTitle"`)) && !bytes.Contains(line, []byte(`"agentName"`)) &&
		(session.WorkDir != "" || !bytes.Contains(line, []byte(`"cwd"`))) {
		return
	}
	var rec claudeUsageRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if rec.CWD != "" {
		session.WorkDir = rec.CWD
	}
	if rec.GitBranch != "" {
		session.GitBranch = rec.GitBranch
	}
	switch rec.Type {
	case "ai-title":
		if rec.AITitle != "" {
			session.Title = rec.AITitle
		}
	case "agent-name":
		if rec.AgentName != "" {
			session.Title = rec.AgentName
		}
	case "assistant":
		if rec.Message == nil || rec.Message.Usage == nil || rec.Message.Model == "<synthetic>" {
			return
		}
		if rec.Message.ID != "" {
			if seen[rec.Message.ID] {
				return
			}
			seen[rec.Message.ID] = true
		}
		usage := rec.Message.Usage
		input, output := max(usage.Input, 0), max(usage.Output, 0)
		cacheRead, cacheCreation := max(usage.CacheRead, 0), max(usage.CacheCreation, 0)
		tokens := input + output + cacheRead + cacheCreation
		summary.Messages++
		summary.InputTokens += input
		summary.CacheRead += cacheRead
		summary.CacheCreation += cacheCreation
		summary.OutputTokens += output
		summary.LifetimeTokens += tokens
		session.InputTokens += input
		session.CacheRead += cacheRead
		session.CacheCreation += cacheCreation
		session.OutputTokens += output
		session.LifetimeTokens += tokens
		if model := strings.TrimSpace(rec.Message.Model); model != "" {
			session.Model = model
			models[model] += tokens
		}
		if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			daily[ts.Local().Format("2006-01-02")] += tokens
		}
	}
}

func (s claudeUsageSnapshot) hasData() bool { return s.Summary.Messages > 0 || s.Summary.Sessions > 0 }

func claudeUsageHeaderLines(width int, subtitle string) []string {
	return []string{
		usagePaintLine("  ╭────╮  Claude Code", width, usageBrightColor, true, usagePanelColor),
		usagePaintLine("  │ ✳  │  "+subtitle, width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────╯", width, usageMutedColor, false, usagePanelColor),
	}
}

func (m model) viewClaudeUsage(contentRows int) string {
	if m.claudeHistoryOpen {
		return m.viewClaudeHistory(contentRows)
	}
	lines := claudeUsageHeaderLines(m.width, "LOCAL CLI HISTORY")
	if m.claudeUsageLoading && !m.claudeUsage.hasData() {
		lines = append(lines, usageDividerLine(m.width), usageSectionLine("LOADING", m.width), usagePaintLine("  Reading local Claude Code transcripts…", m.width, usageMutedColor, false, usagePanelColor))
		return fillUsageLines(lines, contentRows, m.width)
	}
	if m.claudeUsageErr != "" && !m.claudeUsage.hasData() {
		lines = append(lines, usageDividerLine(m.width), usageSectionLine("UNAVAILABLE", m.width), usagePaintLine("  "+m.claudeUsageErr, m.width, usageMutedColor, false, usagePanelColor))
		return fillUsageLines(lines, contentRows, m.width)
	}
	s := m.claudeUsage.Summary
	lines = append(lines,
		usageSidesLine(fmt.Sprintf("  %d sessions  •  %d responses", s.Sessions, s.Messages), formatTokenCount(s.LifetimeTokens)+" tokens  ", m.width, usageTextColor, usageBrightColor),
		usagePaintLine(fmt.Sprintf("  Input %s  •  cache read %s  •  cache write %s  •  output %s", formatTokenCount(s.InputTokens), formatTokenCount(s.CacheRead), formatTokenCount(s.CacheCreation), formatTokenCount(s.OutputTokens)), m.width, usageMutedColor, false, usagePanelColor),
		usagePaintLine("  Plan limits and reset: Claude Code /usage; not stored locally", m.width, usageMutedColor, false, usagePanelColor),
		usageDividerLine(m.width), usageSectionLine("TOKENS BY DAY", m.width))
	days := recentClaudeDailyUsage(m.claudeUsage.Daily, time.Now(), 7)
	peak := int64(0)
	for _, day := range days {
		peak = max(peak, day.Tokens)
	}
	for index, day := range days {
		lines = append(lines, usageMetricBarLine(dayLabel(day.StartDate, index == len(days)-1), day.Tokens, peak, m.width, index == len(days)-1))
	}
	lines = append(lines, usageDividerLine(m.width), usageSectionLine("TOKENS BY MODEL", m.width))
	if len(m.claudeUsage.Models) == 0 {
		lines = append(lines, usagePaintLine("  No local Claude Code token records", m.width, usageMutedColor, false, usageHighlightColor))
	} else {
		models := make([]modelUsage, 0, len(m.claudeUsage.Models))
		for _, item := range m.claudeUsage.Models {
			models = append(models, modelUsage{Model: item.Model, Tokens: item.Tokens})
		}
		for _, model := range colorModelUsage(models) {
			lines = append(lines, usageModelLine(model, m.width))
		}
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func (m model) viewClaudeHistory(contentRows int) string {
	lines := claudeUsageHeaderLines(m.width, fmt.Sprintf("HISTORICAL SESSIONS (%d)  •  Enter/Esc back", len(m.claudeUsage.History)))
	lines = append(lines, usageDividerLine(m.width))
	start, end := codexHistoryWindow(len(m.claudeUsage.History), m.claudeHistoryCursor, contentRows-len(lines))
	for index := start; index < end; index++ {
		session := m.claudeUsage.History[index]
		cursor := " "
		if index == m.claudeHistoryCursor {
			cursor = ">"
		}
		cwd := "unknown cwd"
		if session.WorkDir != "" {
			cwd = truncateKimiText(filepath.Base(session.WorkDir), 24)
		}
		if session.GitBranch != "" {
			cwd += "  •  branch " + truncateKimiText(session.GitBranch, 20)
		}
		lines = append(lines,
			usagePaintLine(fmt.Sprintf("%s %-28s %s", cursor, truncateKimiText(session.Title, 28), formatSessionTime(session.UpdatedAt)), m.width, usageBrightColor, index == m.claudeHistoryCursor, usageHighlightColor),
			usagePaintLine(fmt.Sprintf("    %-20s %s tokens  •  %s", truncateKimiText(emptyFallback(session.Model, "unknown model"), 20), formatTokenCount(session.LifetimeTokens), cwd), m.width, usageMutedColor, false, usagePanelColor),
			usagePaintLine(fmt.Sprintf("    input %s  •  cache read %s  •  cache write %s  •  output %s", formatTokenCount(session.InputTokens), formatTokenCount(session.CacheRead), formatTokenCount(session.CacheCreation), formatTokenCount(session.OutputTokens)), m.width, usageMutedColor, false, usagePanelColor),
		)
	}
	if len(m.claudeUsage.History) == 0 {
		lines = append(lines, usagePaintLine("  No historical Claude Code sessions", m.width, usageMutedColor, false, usagePanelColor))
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func recentClaudeDailyUsage(buckets []claudeDailyUsage, now time.Time, count int) []claudeDailyUsage {
	byDate := make(map[string]int64, len(buckets))
	for _, bucket := range buckets {
		byDate[bucket.StartDate] += bucket.Tokens
	}
	result := make([]claudeDailyUsage, 0, count)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for offset := count - 1; offset >= 0; offset-- {
		date := today.AddDate(0, 0, -offset).Format("2006-01-02")
		result = append(result, claudeDailyUsage{StartDate: date, Tokens: byDate[date]})
	}
	return result
}
