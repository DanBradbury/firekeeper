package session

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kimiTolerance allows for ps reporting elapsed time in whole seconds and
// for the gap between a process starting and Kimi Code writing its session.
const kimiTolerance = 5 * time.Second

// kimiNow is the clock Kimi linking compares process start times with.
// Tests replace it.
var kimiNow = time.Now

// enrichKimiSessions links Kimi Code session groups by working directory
// (a guess; Kimi Code keeps no session file open that Firekeeper reads yet).
// Servers (acp, web) get nothing without evidence (invariant 8); tools never
// reach Link. Within a directory each session is claimed once:
//   - kimi -c links the newest session that existed when the process started,
//     Kimi's documented rule for --continue, even if it was last updated
//     before then;
//   - plain kimi and kimi -p link only a session updated after the process
//     started, newest process first, so a week-old session isn't shown as
//     ACTIVE; a process whose start time can't be read is left unlinked.
//
// -c processes are placed first, so a newer plain kimi in the same
// directory isn't handed the session -c resumed.
func (d *discoverer) enrichKimiSessions(groups []ProcessGroup) error {
	var pids []string
	for _, group := range groups {
		if group.Tool == "Kimi" && group.Role == RoleSession {
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
	byDir, err := d.kimiSessionsByDir(filepath.Join(home, "sessions"))
	if err != nil {
		return err
	}
	now := kimiNow()
	type runtime struct {
		index     int
		started   time.Time
		known     bool
		continue_ bool
	}
	perDir := make(map[string][]runtime)
	for index := range groups {
		group := groups[index]
		if group.Tool != "Kimi" || group.Role != RoleSession {
			continue
		}
		dir := workDirs[group.Root.PID]
		if dir == "" {
			continue
		}
		elapsed, known := parseElapsed(group.Root.Elapsed)
		perDir[dir] = append(perDir[dir], runtime{
			index:     index,
			started:   now.Add(-elapsed),
			known:     known,
			continue_: hasFlag(group.Root.Invocation.Rest, kimiSignature.flags, "-c", "--continue"),
		})
	}
	for dir, runtimes := range perDir {
		sort.SliceStable(runtimes, func(i, j int) bool {
			if runtimes[i].continue_ != runtimes[j].continue_ {
				return runtimes[i].continue_
			}
			return runtimes[i].started.After(runtimes[j].started)
		})
		candidates := byDir[dir] // newest first
		claimed := make(map[string]bool)
		for _, rt := range runtimes {
			if !rt.known {
				continue
			}
			for _, candidate := range candidates {
				if claimed[candidate.Path] {
					continue
				}
				if rt.continue_ {
					if candidate.CreatedAt.After(rt.started.Add(kimiTolerance)) {
						continue // created after this process started
					}
				} else if candidate.UpdatedAt.Before(rt.started.Add(-kimiTolerance)) {
					continue // not touched since this process started
				}
				claimed[candidate.Path] = true
				groups[rt.index].Sessions = []Info{d.kimiInfo(candidate)}
				break
			}
		}
	}
	return nil
}

func (d *discoverer) kimiInfo(state KimiLatestSession) Info {
	sessionDir := filepath.Dir(state.Path)
	var rollout string
	if wire := KimiWirePath(sessionDir); fileExists(wire) {
		rollout = wire
	}
	return Info{
		ID: filepath.Base(sessionDir), Name: d.emptyFallback(d.sanitizeProcessCommand(state.Title), "Kimi Code"), State: SessionStateActive,
		CWD: d.sanitizeProcessCommand(state.WorkDir), Model: d.sanitizeProcessCommand(state.Model), Source: "Kimi Code", StartedAt: state.CreatedAt, UpdatedAt: state.UpdatedAt,
		RolloutPath: rollout,
	}
}

// latestKimiSessions returns the most recently updated session per working
// directory.
func (d *discoverer) latestKimiSessions(root string) (map[string]KimiLatestSession, error) {
	byDir, err := d.kimiSessionsByDir(root)
	if err != nil {
		return nil, err
	}
	result := make(map[string]KimiLatestSession, len(byDir))
	for dir, sessions := range byDir {
		result[dir] = sessions[0]
	}
	return result, nil
}

// kimiSessionsByDir returns every session per working directory, most
// recently updated first.
func (d *discoverer) kimiSessionsByDir(root string) (map[string][]KimiLatestSession, error) {
	result := make(map[string][]KimiLatestSession)
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
			result[state.WorkDir] = append(result[state.WorkDir], KimiLatestSession{KimiSessionState: state, Path: path})
		}
	}
	for _, sessions := range result {
		sort.SliceStable(sessions, func(i, j int) bool {
			if !sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
				return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
			}
			return sessions[i].Path < sessions[j].Path
		})
	}
	return result, nil
}

// parseElapsed reads ps's etime column, [[dd-]hh:]mm:ss.
func parseElapsed(value string) (time.Duration, bool) {
	days := 0
	if before, after, ok := strings.Cut(value, "-"); ok {
		n, err := strconv.Atoi(before)
		if err != nil || n < 0 {
			return 0, false
		}
		days, value = n, after
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n >= 60 && part != parts[0] {
			return 0, false
		}
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second, true
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
