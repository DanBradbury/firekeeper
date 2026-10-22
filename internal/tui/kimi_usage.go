package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
