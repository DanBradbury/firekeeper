package session

import (
	"encoding/json"

	"fmt"

	"os"
	"path/filepath"

	"strings"
)

func (d *discoverer) enrichKimiSessions(groups []ProcessGroup) error {
	var pids []string
	for _, group := range groups {
		if group.Tool == "Kimi" {
			pids = append(pids, fmt.Sprint(group.Root.PID))
		}
	}
	if len(pids) == 0 {
		return nil
	}
	home, err := d.kimiHome()
	if err != nil {
		return err
	}
	workDirs := make(map[int]string)
	output, err := d.command("lsof", "-a", "-d", "cwd", "-Fn", "-p", strings.Join(pids, ",")).Output()
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
	latest, err := d.latestKimiSessions(filepath.Join(home, "sessions"))
	if err != nil {
		return err
	}
	for index := range groups {
		if groups[index].Tool != "Kimi" {
			continue
		}
		state, ok := latest[workDirs[groups[index].Root.PID]]
		if !ok {
			continue
		}
		groups[index].Sessions = []SessionInfo{{
			ID: filepath.Base(filepath.Dir(state.Path)), Name: d.emptyFallback(d.sanitizeProcessCommand(state.Title), "Kimi Code"), State: SessionStateActive,
			CWD: d.sanitizeProcessCommand(state.WorkDir), Model: d.sanitizeProcessCommand(state.Model), Source: "Kimi Code", StartedAt: state.CreatedAt, UpdatedAt: state.UpdatedAt,
		}}
	}
	return nil
}

func (d *discoverer) latestKimiSessions(root string) (map[string]KimiLatestSession, error) {
	result := make(map[string]KimiLatestSession)
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
			var state KimiSessionState
			if json.Unmarshal(body, &state) != nil || state.WorkDir == "" {
				continue
			}
			candidate := KimiLatestSession{KimiSessionState: state, Model: "", Path: path}
			if previous, ok := result[state.WorkDir]; !ok || candidate.UpdatedAt.After(previous.UpdatedAt) {
				result[state.WorkDir] = candidate
			}
		}
	}
	return result, nil
}

func (d *discoverer) kimiHome() (string, error) {
	if d.opts.KimiHome != "" {
		return d.opts.KimiHome, nil
	}
	if home := os.Getenv("KIMI_CODE_HOME"); home != "" {
		return home, nil
	}
	if home := os.Getenv("KIMI_SHARE_DIR"); home != "" {
		return home, nil
	}
	home, err := d.home()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".kimi-code"), nil
}
