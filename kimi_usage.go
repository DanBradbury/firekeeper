package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type kimiUsageSummary struct {
	Sessions       int64
	Turns          int64
	InputTokens    int64
	CacheRead      int64
	CacheCreation  int64
	OutputTokens   int64
	LifetimeTokens int64
}

type kimiDailyUsage struct {
	StartDate string
	Tokens    int64
}

type kimiUsageModel struct {
	Model  string
	Tokens int64
}

type kimiHistoricalSession struct {
	ID             string
	Title          string
	WorkDir        string
	Model          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	InputTokens    int64
	CacheRead      int64
	CacheCreation  int64
	OutputTokens   int64
	LifetimeTokens int64
}

type kimiUsageSnapshot struct {
	Summary kimiUsageSummary
	Daily   []kimiDailyUsage
	Models  []kimiUsageModel
	History []kimiHistoricalSession
}

type kimiUsageResultMsg struct {
	snapshot  kimiUsageSnapshot
	refreshed time.Time
	err       error
}

type kimiWireUsage struct {
	InputCacheCreation int64 `json:"inputCacheCreation"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputOther         int64 `json:"inputOther"`
	Output             int64 `json:"output"`
}

type kimiWireEvent struct {
	Type  string        `json:"type"`
	Model string        `json:"model"`
	Time  int64         `json:"time"`
	Usage kimiWireUsage `json:"usage"`
}

func refreshKimiUsage() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := fetchKimiUsage()
		return kimiUsageResultMsg{snapshot: snapshot, refreshed: time.Now(), err: err}
	}
}

func kimiHome() (string, error) {
	if home := os.Getenv("KIMI_CODE_HOME"); home != "" {
		return home, nil
	}
	if home := os.Getenv("KIMI_SHARE_DIR"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".kimi-code"), nil
}

func fetchKimiUsage() (kimiUsageSnapshot, error) {
	home, err := kimiHome()
	if err != nil {
		return kimiUsageSnapshot{}, err
	}
	root := filepath.Join(home, "sessions")
	if _, err := os.Stat(root); err != nil {
		return kimiUsageSnapshot{}, fmt.Errorf("find Kimi session history: %w", err)
	}

	snapshot := kimiUsageSnapshot{}
	models := make(map[string]int64)
	daily := make(map[string]int64)
	sessions := make(map[string]*kimiHistoricalSession)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "wire.jsonl" {
			return nil
		}
		sessionDir := filepath.Dir(filepath.Dir(filepath.Dir(path)))
		session := sessions[sessionDir]
		if session == nil {
			session = &kimiHistoricalSession{ID: filepath.Base(sessionDir)}
			if state, readErr := readKimiSessionState(filepath.Join(sessionDir, "state.json")); readErr == nil {
				session.Title = state.Title
				session.WorkDir = state.WorkDir
				session.CreatedAt = state.CreatedAt
				session.UpdatedAt = state.UpdatedAt
			}
			sessions[sessionDir] = session
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var event kimiWireEvent
			if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "usage.record" {
				continue
			}
			tokens := event.Usage.InputCacheCreation + event.Usage.InputCacheRead + event.Usage.InputOther + event.Usage.Output
			snapshot.Summary.Turns++
			snapshot.Summary.InputTokens += event.Usage.InputOther
			snapshot.Summary.CacheRead += event.Usage.InputCacheRead
			snapshot.Summary.CacheCreation += event.Usage.InputCacheCreation
			snapshot.Summary.OutputTokens += event.Usage.Output
			snapshot.Summary.LifetimeTokens += tokens
			session.InputTokens += event.Usage.InputOther
			session.CacheRead += event.Usage.InputCacheRead
			session.CacheCreation += event.Usage.InputCacheCreation
			session.OutputTokens += event.Usage.Output
			session.LifetimeTokens += tokens
			if event.Model != "" {
				session.Model = event.Model
			}
			if event.Time > 0 && time.UnixMilli(event.Time).After(session.UpdatedAt) {
				session.UpdatedAt = time.UnixMilli(event.Time)
			}
			models[event.Model] += tokens
			if event.Time > 0 {
				day := time.UnixMilli(event.Time).Format("2006-01-02")
				daily[day] += tokens
			}
		}
		return nil
	})
	if err != nil {
		return kimiUsageSnapshot{}, fmt.Errorf("read Kimi session history: %w", err)
	}
	snapshot.Summary.Sessions = int64(len(sessions))
	for _, session := range sessions {
		session.Title = emptyFallback(sanitizeProcessCommand(session.Title), "Kimi Code session")
		session.WorkDir = sanitizeProcessCommand(session.WorkDir)
		snapshot.History = append(snapshot.History, *session)
	}
	sort.Slice(snapshot.History, func(i, j int) bool { return snapshot.History[i].UpdatedAt.After(snapshot.History[j].UpdatedAt) })
	for day, tokens := range daily {
		snapshot.Daily = append(snapshot.Daily, kimiDailyUsage{StartDate: day, Tokens: tokens})
	}
	sort.Slice(snapshot.Daily, func(i, j int) bool { return snapshot.Daily[i].StartDate < snapshot.Daily[j].StartDate })
	for model, tokens := range models {
		if strings.TrimSpace(model) != "" {
			snapshot.Models = append(snapshot.Models, kimiUsageModel{Model: model, Tokens: tokens})
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

func (s kimiUsageSnapshot) hasData() bool { return s.Summary.Turns > 0 || s.Summary.Sessions > 0 }

func readKimiSessionState(path string) (kimiSessionState, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return kimiSessionState{}, err
	}
	var state kimiSessionState
	if err := json.Unmarshal(body, &state); err != nil {
		return kimiSessionState{}, err
	}
	return state, nil
}

func (m model) viewKimiUsage(contentRows int) string {
	lines := []string{
		usagePaintLine("  ╭────╮  Kimi Code", m.width, usageBrightColor, true, usagePanelColor),
		usagePaintLine("  │ ◈  │  LOCAL CLI HISTORY", m.width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────╯", m.width, usageMutedColor, false, usagePanelColor),
	}
	if m.kimiUsageLoading && !m.kimiUsage.hasData() {
		lines = append(lines, usageDividerLine(m.width), usageSectionLine("LOADING", m.width), usagePaintLine("  Reading local Kimi Code sessions…", m.width, usageMutedColor, false, usagePanelColor))
		return fillUsageLines(lines, contentRows, m.width)
	}
	if m.kimiUsageErr != "" && !m.kimiUsage.hasData() {
		lines = append(lines, usageDividerLine(m.width), usageSectionLine("UNAVAILABLE", m.width), usagePaintLine("  "+m.kimiUsageErr, m.width, usageMutedColor, false, usagePanelColor))
		return fillUsageLines(lines, contentRows, m.width)
	}
	s := m.kimiUsage.Summary
	if m.kimiHistoryOpen {
		return m.viewKimiHistory(contentRows)
	}
	lines = append(lines,
		usageSidesLine(fmt.Sprintf("  %d sessions  •  %d turns", s.Sessions, s.Turns), formatTokenCount(s.LifetimeTokens)+" tokens  ", m.width, usageTextColor, usageBrightColor),
		usagePaintLine(fmt.Sprintf("  Input %s  •  cache read %s  •  output %s", formatTokenCount(s.InputTokens), formatTokenCount(s.CacheRead), formatTokenCount(s.OutputTokens)), m.width, usageMutedColor, false, usagePanelColor),
		usagePaintLine("  Quota and reset: Kimi Code /usage; no supported local quota file", m.width, usageMutedColor, false, usagePanelColor),
		usageDividerLine(m.width), usageSectionLine("TOKENS BY DAY", m.width))
	days := recentKimiDailyUsage(m.kimiUsage.Daily, time.Now(), 7)
	peak := int64(0)
	for _, day := range days {
		peak = max(peak, day.Tokens)
	}
	for index, day := range days {
		lines = append(lines, usageMetricBarLine(dayLabel(day.StartDate, index == len(days)-1), day.Tokens, peak, m.width, index == len(days)-1))
	}
	lines = append(lines, usageDividerLine(m.width), usageSectionLine("TOKENS BY MODEL", m.width))
	if len(m.kimiUsage.Models) == 0 {
		lines = append(lines, usagePaintLine("  No local Kimi Code token records", m.width, usageMutedColor, false, usageHighlightColor))
	} else {
		for _, item := range m.kimiUsage.Models {
			lines = append(lines, usageModelLine(modelUsage{Model: item.Model, Tokens: item.Tokens}, m.width))
		}
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func (m model) viewKimiHistory(contentRows int) string {
	lines := []string{
		usagePaintLine("  ╭────╮  Kimi Code", m.width, usageBrightColor, true, usagePanelColor),
		usagePaintLine("  │ ◈  │  HISTORICAL SESSIONS  •  Enter/Esc back", m.width, usageTextColor, true, usagePanelColor),
		usagePaintLine("  ╰────╯", m.width, usageMutedColor, false, usagePanelColor),
		usageDividerLine(m.width),
	}
	for index, session := range m.kimiUsage.History {
		cursor := " "
		if index == m.kimiHistoryCursor {
			cursor = ">"
		}
		when := formatSessionTime(session.UpdatedAt)
		cwd := "unknown cwd"
		if session.WorkDir != "" {
			cwd = truncateKimiText(filepath.Base(session.WorkDir), 24)
		}
		lines = append(lines,
			usagePaintLine(fmt.Sprintf("%s %-24s %s", cursor, truncateKimiText(session.Title, 24), when), m.width, usageBrightColor, index == m.kimiHistoryCursor, usageHighlightColor),
			usagePaintLine(fmt.Sprintf("    %-18s %s  •  %s tokens", truncateKimiText(session.Model, 18), formatTokenCount(session.LifetimeTokens), cwd), m.width, usageMutedColor, false, usagePanelColor),
			usagePaintLine(fmt.Sprintf("    input %s  •  cache read %s  •  cache write %s  •  output %s", formatTokenCount(session.InputTokens), formatTokenCount(session.CacheRead), formatTokenCount(session.CacheCreation), formatTokenCount(session.OutputTokens)), m.width, usageMutedColor, false, usagePanelColor),
		)
	}
	if len(m.kimiUsage.History) == 0 {
		lines = append(lines, usagePaintLine("  No historical Kimi sessions", m.width, usageMutedColor, false, usagePanelColor))
	}
	return fillUsageLines(lines, contentRows, m.width)
}

func truncateKimiText(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func recentKimiDailyUsage(buckets []kimiDailyUsage, now time.Time, count int) []kimiDailyUsage {
	byDate := make(map[string]int64, len(buckets))
	for _, bucket := range buckets {
		byDate[bucket.StartDate] += bucket.Tokens
	}
	result := make([]kimiDailyUsage, 0, count)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for offset := count - 1; offset >= 0; offset-- {
		date := today.AddDate(0, 0, -offset).Format("2006-01-02")
		result = append(result, kimiDailyUsage{StartDate: date, Tokens: byDate[date]})
	}
	return result
}
