package session

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
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
		cwd := workDirs[groups[index].Root.PID]
		state, ok := latest[cwd]
		if !ok {
			// Kimi writes state.json only after the first prompt. Show the
			// runtime's working directory until then instead of no metadata.
			if cwd != "" {
				groups[index].Sessions = []Info{{
					Name: "Kimi Code", State: SessionStateUnknown,
					CWD: d.sanitizeProcessCommand(cwd), Source: "Kimi Code",
				}}
			}
			continue
		}
		sessionDir := filepath.Dir(state.Path)
		var rollout string
		if wire := KimiWirePath(sessionDir); fileExists(wire) {
			rollout = wire
		}
		groups[index].Sessions = []Info{{
			ID: filepath.Base(sessionDir), Name: d.emptyFallback(d.sanitizeProcessCommand(state.Title), "Kimi Code"), State: SessionStateActive,
			CWD: d.sanitizeProcessCommand(state.WorkDir), Model: d.sanitizeProcessCommand(state.Model), Source: "Kimi Code", StartedAt: state.CreatedAt, UpdatedAt: state.UpdatedAt,
			RolloutPath: rollout,
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

// KimiHome returns Kimi Code's data directory, honoring KIMI_CODE_HOME and then
// KIMI_SHARE_DIR (default ~/.kimi-code).
func KimiHome() (string, error) { return standalone().kimiHome() }

// KimiWirePath returns the main agent's wire log inside a Kimi Code session
// directory, sessions/<work dir key>/<session id>.
func KimiWirePath(sessionDir string) string {
	return filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
}

// UnmarshalJSON accepts both state.json layouts Kimi Code has written:
// current releases store createdAt and updatedAt as epoch milliseconds and
// the directory as cwd, while older ones used RFC 3339 strings and workDir.
// A timestamp in neither form is left zero rather than failing the decode.
func (s *KimiSessionState) UnmarshalJSON(body []byte) error {
	var raw struct {
		CreatedAt json.RawMessage `json:"createdAt"`
		UpdatedAt json.RawMessage `json:"updatedAt"`
		Title     string          `json:"title"`
		WorkDir   string          `json:"workDir"`
		CWD       string          `json:"cwd"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	*s = KimiSessionState{
		CreatedAt: kimiTime(raw.CreatedAt),
		UpdatedAt: kimiTime(raw.UpdatedAt),
		Title:     raw.Title,
		WorkDir:   raw.CWD,
	}
	if s.WorkDir == "" {
		s.WorkDir = raw.WorkDir
	}
	return nil
}

// kimiTime reads an epoch-millisecond number or an RFC 3339 string.
func kimiTime(raw json.RawMessage) time.Time {
	var ms float64
	if json.Unmarshal(raw, &ms) == nil {
		if ms <= 0 || math.IsInf(ms, 0) || math.IsNaN(ms) || ms > maxKimiMillis {
			return time.Time{}
		}
		return time.UnixMilli(int64(ms)).UTC()
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return t
		}
	}
	return time.Time{}
}

// maxKimiMillis bounds timestamps to year 9999 so bad input cannot overflow.
const maxKimiMillis = 253402300799999

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
