package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript/claudecloud"
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
	// UndercountWarning is set when the session has subagent transcripts.
	// It is not part of LifetimeTokens.
	UndercountWarning string
}

type claudeUsageSnapshot struct {
	// Limits are the 5-hour and weekly windows. They are fetched unless the
	// user ran `firekeeper claude-limits disable`: LimitsOn says it is on, and
	// LimitsErr why the windows are missing when it is (a missing sign-in is
	// not an error).
	Limits    claudelimits.Limits
	LimitsOn  bool
	LimitsErr string
	// CloudSyncWarn is the daemon's last Claude Code on the web sync
	// problem, set only while `[claude_cloud] enabled = true`.
	CloudSyncWarn string

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

// claudeLimitsSource reports whether limits are enabled and, if so, fetches
// them. It is a variable so tests do not touch the config file, the Keychain
// or the network.
var claudeLimitsSource = func() (limits claudelimits.Limits, enabled bool, err error) {
	cfg, loadErr := config.Load(config.Options{Getenv: os.Getenv})
	if loadErr != nil || !cfg.ClaudeLimits {
		return claudelimits.Limits{}, false, nil
	}
	limits, err = claudelimits.Client{}.Fetch(context.Background())
	return limits, true, err
}

// claudeCloudStatusSource returns the daemon's last cloud sync warning, or
// "" when cloud sync is off or clean. It reads only Firekeeper's own status
// file, never the Claude Code login, and is a variable for tests.
var claudeCloudStatusSource = func() string {
	cfg, err := config.Load(config.Options{Getenv: os.Getenv})
	if err != nil || !cfg.ClaudeCloud {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	status, ok := claudecloud.ReadStatus(filepath.Join(home, ".firekeeper", claudecloud.StatusFile))
	if !ok {
		return ""
	}
	return sanitizeProcessCommand(status.Warning)
}

func refreshClaudeUsage() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := fetchClaudeUsage()
		if err == nil {
			limits, enabled, limitsErr := claudeLimitsSource()
			snapshot.LimitsOn = enabled
			// No sign-in just means Claude Code is not logged in here: show
			// nothing rather than an error on every machine without one.
			if limitsErr != nil && !errors.Is(limitsErr, claudelimits.ErrNoToken) {
				snapshot.LimitsErr = limitsErr.Error()
			} else {
				snapshot.Limits = limits
			}
		}
		snapshot.CloudSyncWarn = claudeCloudStatusSource()
		return claudeUsageResultMsg{snapshot: snapshot, refreshed: time.Now(), err: err}
	}
}

// fetchClaudeUsage sums assistant message usage from Claude Code transcripts
// under CLAUDE_CONFIG_DIR/projects. Claude Code repeats a response's usage on
// every content block line, so each message id is counted once, across all
// files, which also keeps messages copied into a resumed session from
// counting twice. Plan limits are not stored locally; they come from
// claudeLimitsSource when the user opted in.
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
	session.UndercountWarning = claudeUndercountWarning(path)
	return session, true
}

// claudeUndercountWarning is package-level so it can name the session package.
// readClaudeUsageFile's session variable would shadow that import.
func claudeUndercountWarning(path string) string {
	return session.ClaudeUndercountWarningFor(path)
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

// claudeLimitWindow is one named window for display.
type claudeLimitWindow struct {
	Label       string
	UsedPercent float64
	ResetsAt    time.Time
}

// claudeLimitWindows lists the reported windows, shortest first.
func claudeLimitWindows(limits claudelimits.Limits) []claudeLimitWindow {
	var windows []claudeLimitWindow
	for _, item := range []struct {
		label  string
		window *claudelimits.Window
	}{
		{"5-hour", limits.FiveHour},
		{"Weekly", limits.SevenDay},
		{"Weekly · Opus", limits.SevenDayOpus},
		{"Weekly · Sonnet", limits.SevenDaySonnet},
	} {
		if item.window != nil {
			windows = append(windows, claudeLimitWindow{item.label, item.window.UsedPercent, item.window.ResetsAt})
		}
	}
	return windows
}

// claudeLimitsNote is the muted line shown where limits would be.
func claudeLimitsNote(snapshot claudeUsageSnapshot) string {
	switch {
	case !snapshot.LimitsOn:
		return "firekeeper claude-limits enable" // short: it must fit a card
	case snapshot.LimitsErr != "":
		return "Limits unavailable: " + snapshot.LimitsErr
	default:
		return "Plan limits: Claude Code /usage"
	}
}
