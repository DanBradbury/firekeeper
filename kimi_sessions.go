package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type kimiSessionState struct {
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Title     string    `json:"title"`
	WorkDir   string    `json:"workDir"`
}

func enrichKimiSessions(groups []processGroup) error {
	var pids []string
	for _, group := range groups {
		if group.tool == "Kimi" {
			pids = append(pids, fmt.Sprint(group.root.pid))
		}
	}
	if len(pids) == 0 {
		return nil
	}
	home, err := kimiHome()
	if err != nil {
		return err
	}
	workDirs := make(map[int]string)
	output, err := exec.Command("lsof", "-a", "-d", "cwd", "-Fn", "-p", strings.Join(pids, ",")).Output()
	if err == nil {
		pid := 0
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "p") {
				fmt.Sscanf(line[1:], "%d", &pid)
			} else if strings.HasPrefix(line, "n") && pid != 0 {
				workDirs[pid] = line[1:]
			}
		}
	}
	latest, err := latestKimiSessions(filepath.Join(home, "sessions"))
	if err != nil {
		return err
	}
	for index := range groups {
		if groups[index].tool != "Kimi" {
			continue
		}
		state, ok := latest[workDirs[groups[index].root.pid]]
		if !ok {
			continue
		}
		groups[index].sessions = []sessionInfo{{
			id: filepath.Base(filepath.Dir(state.path)), name: emptyFallback(sanitizeProcessCommand(state.Title), "Kimi Code"), state: sessionStateActive,
			cwd: sanitizeProcessCommand(state.WorkDir), model: sanitizeProcessCommand(state.Model), source: "Kimi Code", updatedAt: state.UpdatedAt,
		}}
	}
	return nil
}

type kimiLatestSession struct {
	kimiSessionState
	Model string
	path  string
}

func latestKimiSessions(root string) (map[string]kimiLatestSession, error) {
	result := make(map[string]kimiLatestSession)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read Kimi sessions: %w", err)
	}
	for _, workEntry := range entries {
		if !workEntry.IsDir() {
			continue
		}
		sessionsRoot := filepath.Join(root, workEntry.Name())
		sessions, _ := os.ReadDir(sessionsRoot)
		for _, sessionEntry := range sessions {
			if !sessionEntry.IsDir() {
				continue
			}
			path := filepath.Join(sessionsRoot, sessionEntry.Name(), "state.json")
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			var state kimiSessionState
			if json.Unmarshal(body, &state) != nil || state.WorkDir == "" {
				continue
			}
			candidate := kimiLatestSession{kimiSessionState: state, Model: "", path: path}
			if previous, ok := result[state.WorkDir]; !ok || candidate.UpdatedAt.After(previous.UpdatedAt) {
				result[state.WorkDir] = candidate
			}
		}
	}
	return result, nil
}
