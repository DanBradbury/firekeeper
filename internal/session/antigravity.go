package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/antigravityusage"
	"github.com/DanBradbury/firekeeper/internal/fsutil"
)

const (
	AntigravitySource                    = "Antigravity CLI"
	AntigravityTranscriptTailBytes int64 = 4 << 20
	antigravityHeadBytes           int64 = 64 << 10
	antigravityCacheBytes          int64 = 1 << 20
)

// AntigravityRecord describes the observed CLI JSONL envelope. Additional
// fields remain in the original record, not in this metadata projection.
type AntigravityRecord struct {
	StepIndex *int64  `json:"step_index"`
	Source    string  `json:"source"`
	Type      string  `json:"type"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	Content   *string `json:"content"`
	Thinking  string  `json:"thinking"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	TruncatedFields []string `json:"truncated_fields"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CacheReadTokens int64    `json:"cache_read_tokens"`
}

type AntigravityTranscript struct {
	SessionID string
	Title     string
	Model     string
	State     State
	StartedAt time.Time
	UpdatedAt time.Time
}

func antigravityArgs(command string) []string {
	fields := commandFields(command)
	if len(fields) == 0 || strings.Contains(slashPath(fields[0]), ".app/Contents/") {
		return nil
	}
	switch strings.ToLower(baseName(fields[0])) {
	case "agy", "agy.exe":
		return append([]string{}, fields[1:]...)
	default:
		return nil
	}
}

func (d *discoverer) isAntigravityCommand(command string) bool {
	args := antigravityArgs(command)
	if args == nil {
		return false
	}
	if len(args) > 0 {
		switch args[0] {
		case "login", "logout", "auth", "help", "version", "update", "upgrade", "mcp", "extensions", "completion", "models", "agents":
			return false
		case "--help", "-h", "--version", "-v":
			return false
		}
	}
	return true
}

func (d *discoverer) antigravitySessionIDFromCommand(command string) (string, bool) {
	args := antigravityArgs(command)
	for i, arg := range args {
		var id string
		switch {
		case strings.HasPrefix(arg, "--conversation="):
			id = strings.TrimPrefix(arg, "--conversation=")
		case arg == "--conversation" && i+1 < len(args):
			id = args[i+1]
		}
		id = strings.Trim(id, "'\"")
		if ValidAntigravitySessionID(id) {
			return id, true
		}
	}
	return "", false
}

// ValidAntigravitySessionID rejects paths and glob patterns. The CLI allocates
// conversation IDs; accepting safe opaque IDs also supports synthetic fixtures.
func ValidAntigravitySessionID(id string) bool { return ValidGeminiSessionID(id) }

func (d *discoverer) antigravityDir() (string, error) {
	dirs, err := d.geminiDirs()
	if err != nil || len(dirs) == 0 {
		return "", errors.New("find Antigravity data directory")
	}
	return filepath.Join(dirs[0], "antigravity-cli"), nil
}

// AntigravityDir returns <Gemini runtime directory>/antigravity-cli. As with
// GeminiDir, GEMINI_CLI_HOME names the parent of the .gemini runtime directory.
func AntigravityDir() (string, error) { return standalone().antigravityDir() }

// AntigravityTranscriptID accepts only the main CLI transcript layout. The
// transcript_full.jsonl duplicate and legacy desktop markdown are excluded.
func AntigravityTranscriptID(path string) (string, bool) {
	parts := strings.Split(strings.TrimRight(slashPath(path), "/"), "/")
	if len(parts) < 5 {
		return "", false
	}
	n := len(parts)
	if parts[n-5] != "brain" || parts[n-3] != ".system_generated" ||
		parts[n-2] != "logs" || parts[n-1] != "transcript.jsonl" ||
		!ValidAntigravitySessionID(parts[n-4]) {
		return "", false
	}
	return parts[n-4], true
}

func FindAntigravityTranscript(dir, id string) string {
	if !ValidAntigravitySessionID(id) {
		return ""
	}
	path := filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript.jsonl")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

// AntigravityWorkDirs reads the documented workspace-keyed resume cache, not
// transcript contents. Ambiguous IDs mapped to multiple workspaces get no cwd.
// Cache layout: https://antigravity.google/docs/cli/commands/resume.
func AntigravityWorkDirs(dir string) (map[string]string, error) {
	file, err := fsutil.Open(filepath.Join(dir, "cache", "last_conversations.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, errors.New("antigravity: read workspace cache")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, antigravityCacheBytes+1))
	if err != nil || int64(len(data)) > antigravityCacheBytes {
		return nil, errors.New("antigravity: workspace cache is unreadable or too large")
	}
	var cache map[string]string
	if json.Unmarshal(data, &cache) != nil {
		return nil, errors.New("antigravity: invalid workspace cache")
	}
	cwds := map[string]string{}
	ambiguous := map[string]bool{}
	for cwd, id := range cache {
		absolute := filepath.IsAbs(cwd) || strings.HasPrefix(cwd, `\\`) ||
			(len(cwd) > 2 && cwd[1] == ':' && (cwd[2] == '\\' || cwd[2] == '/'))
		if !ValidAntigravitySessionID(id) || !absolute {
			continue
		}
		if previous, ok := cwds[id]; ok && !sameDirectory(previous, cwd) {
			ambiguous[id] = true
		}
		cwds[id] = cwd
	}
	for id := range ambiguous {
		delete(cwds, id)
	}
	if len(ambiguous) > 0 {
		return cwds, errors.New("antigravity: ambiguous workspace cache")
	}
	return cwds, nil
}

func UnwrapAntigravityUserRequest(content string) string {
	if _, after, ok := strings.Cut(content, "<USER_REQUEST>"); ok {
		if text, _, closed := strings.Cut(after, "</USER_REQUEST>"); closed {
			return strings.TrimSpace(text)
		}
	}
	for _, tag := range []string{"<ADDITIONAL_METADATA>", "<USER_SETTINGS_CHANGE>"} {
		if before, _, found := strings.Cut(content, tag); found {
			content = before
		}
	}
	return strings.TrimSpace(content)
}

// AntigravityModelFromUserInput reads only the last settings block, never a
// model name mentioned in the user's request. Version dots remain intact.
func AntigravityModelFromUserInput(content string) string {
	const open = "<USER_SETTINGS_CHANGE>"
	i := strings.LastIndex(content, open)
	if i < 0 {
		return ""
	}
	block := content[i+len(open):]
	if before, _, found := strings.Cut(block, "</USER_SETTINGS_CHANGE>"); found {
		block = before
	}
	_, change, found := strings.Cut(block, "`Model Selection`")
	if !found {
		return ""
	}
	change = strings.TrimSpace(change)
	if !strings.HasPrefix(change, "from ") {
		return ""
	}
	_, model, found := strings.Cut(change, " to ")
	if !found {
		return ""
	}
	model = strings.TrimSpace(model)
	for i, r := range model {
		if r == '\n' || r == '<' || (r == '.' && (i+1 == len(model) || strings.ContainsRune(" \t\r\n<", rune(model[i+1])))) {
			return strings.TrimSpace(model[:i])
		}
	}
	return model
}

func antigravityRecordState(r AntigravityRecord, previous State) State {
	if r.Status != "DONE" {
		return SessionStateUnknown
	}
	switch r.Type {
	case "USER_INPUT":
		return SessionStateActive
	case "PLANNER_RESPONSE":
		if len(r.ToolCalls) > 0 {
			return SessionStateActive
		}
		if r.Content != nil && strings.TrimSpace(*r.Content) != "" {
			return SessionStateWaiting
		}
		return SessionStateUnknown
	case "RUN_COMMAND", "VIEW_FILE", "LIST_DIRECTORY", "CODE_ACTION", "SEARCH_WEB", "GREP_SEARCH", "GENERIC":
		return SessionStateActive
	case "CONVERSATION_HISTORY", "CHECKPOINT", "SYSTEM_MESSAGE":
		return previous
	default:
		return SessionStateUnknown
	}
}

// ReadAntigravityTranscript reads bounded complete records from the head and
// tail. Unknown or malformed latest steps do not imply a known runtime state.
func ReadAntigravityTranscript(path string) (AntigravityTranscript, error) {
	id, ok := AntigravityTranscriptID(path)
	if !ok {
		return AntigravityTranscript{}, errors.New("antigravity: invalid transcript layout")
	}
	t := AntigravityTranscript{SessionID: id}
	file, err := fsutil.Open(path)
	if err != nil {
		return t, errors.New("antigravity: open transcript")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return t, errors.New("antigravity: stat transcript")
	}
	valid := false
	consume := func(data []byte, head bool) {
		lines := bytes.Split(data, []byte{'\n'})
		for _, line := range lines[:len(lines)-1] {
			var r AntigravityRecord
			if json.Unmarshal(line, &r) != nil || r.StepIndex == nil || *r.StepIndex < 0 || r.Type == "" {
				t.State = SessionStateUnknown
				continue
			}
			valid = true
			if ts, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
				if head && t.StartedAt.IsZero() {
					t.StartedAt = ts
				}
				if ts.After(t.UpdatedAt) {
					t.UpdatedAt = ts
				}
			}
			if r.Type == "USER_INPUT" && r.Content != nil {
				if head && t.Title == "" {
					t.Title = strings.SplitN(UnwrapAntigravityUserRequest(*r.Content), "\n", 2)[0]
				}
				if model := AntigravityModelFromUserInput(*r.Content); model != "" {
					t.Model = model
				}
			}
			t.State = antigravityRecordState(r, t.State)
		}
	}
	head, err := io.ReadAll(io.LimitReader(file, antigravityHeadBytes))
	if err != nil {
		return t, errors.New("antigravity: read transcript head")
	}
	consume(head, true)
	if info.Size() > antigravityHeadBytes {
		start := max(int64(0), info.Size()-AntigravityTranscriptTailBytes)
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return t, errors.New("antigravity: seek transcript tail")
		}
		tail, err := io.ReadAll(io.LimitReader(file, AntigravityTranscriptTailBytes))
		if err != nil {
			return t, errors.New("antigravity: read transcript tail")
		}
		if start > 0 {
			_, tail, _ = bytes.Cut(tail, []byte{'\n'})
		}
		// The head's state is not a valid predecessor across an omitted region.
		t.State = SessionStateUnknown
		consume(tail, start == 0)
	}
	if !valid {
		return t, errors.New("antigravity: empty or unrecognized transcript")
	}
	return t, nil
}

func (d *discoverer) enrichAntigravitySessions(groups []ProcessGroup) error {
	var indexes []int
	for i := range groups {
		if groups[i].Tool == "Antigravity" {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nil
	}
	dir, err := d.antigravityDir()
	if err != nil {
		return err
	}
	workDirs := d.claudeWorkDirs(groups, indexes)
	cache, cacheErr := AntigravityWorkDirs(dir)
	var pids []string
	roots := map[int]int{}
	for _, i := range indexes {
		for _, p := range append([]ProcessInfo{groups[i].Root}, groups[i].Processes...) {
			pids = append(pids, strconv.Itoa(p.PID))
			roots[p.PID] = i
		}
	}
	openPaths := map[int][]string{}
	output, _ := d.command("lsof", "-Fn", "-p", strings.Join(pids, ",")).Output()
	pid := 0
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(line[1:])
		} else if strings.HasPrefix(line, "n") {
			if i, ok := roots[pid]; ok {
				path := line[1:]
				if _, valid := AntigravityTranscriptID(path); valid {
					rel, err := filepath.Rel(dir, path)
					if err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
						openPaths[i] = append(openPaths[i], path)
					}
				}
			}
		}
	}
	missing, incompleteUsage := 0, 0
	for _, i := range indexes {
		path := ""
		explicit := false
		for _, p := range append([]ProcessInfo{groups[i].Root}, groups[i].Processes...) {
			if id, ok := d.antigravitySessionIDFromCommand(p.Command); ok {
				path, explicit = FindAntigravityTranscript(dir, id), true
				break
			}
		}
		if !explicit {
			unique := map[string]bool{}
			for _, p := range openPaths[i] {
				unique[p] = true
			}
			if path == "" && len(unique) == 0 {
				cwd := workDirs[groups[i].Root.PID]
				matches, runtimes := 0, 0
				if cwd != "" {
					for _, other := range indexes {
						if sameDirectory(cwd, workDirs[groups[other].Root.PID]) {
							runtimes++
						}
					}
					for id, cachedCWD := range cache {
						if sameDirectory(cwd, cachedCWD) {
							path = FindAntigravityTranscript(dir, id)
							matches++
						}
					}
				}
				if matches != 1 || runtimes != 1 {
					path = ""
				}
			}
			if len(unique) == 1 {
				for p := range unique {
					path = p
				}
			}
		}
		t, err := ReadAntigravityTranscript(path)
		if err != nil {
			missing++
			continue
		}
		cwd := workDirs[groups[i].Root.PID]
		if cwd == "" {
			cwd = cache[t.SessionID]
		}
		clean := d.sanitizeProcessCommand
		info := Info{ID: t.SessionID, Name: clean(t.Title), Source: AntigravitySource,
			CWD: clean(cwd), Model: clean(t.Model), State: t.State, RolloutPath: path,
			StartedAt: t.StartedAt, UpdatedAt: t.UpdatedAt}
		ctx := d.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		database := filepath.Join(dir, "conversations", t.SessionID+".db")
		if usage, err := antigravityusage.Read(ctx, database); err == nil {
			info.Tokens = TokenBreakdown{Input: usage.Input, Output: usage.Output, CacheRead: usage.Cache}
			info.TokensUsed = info.Tokens.Total()
			if usage.Model != "" {
				info.Model = clean(usage.Model)
			}
			if usage.Skipped > 0 {
				incompleteUsage++
			}
		} else if _, err := os.Stat(database); err == nil {
			incompleteUsage++
		}
		groups[i].Sessions = []Info{info}
	}
	if missing > 0 || incompleteUsage > 0 {
		return errors.Join(cacheErr, fmt.Errorf("metadata unavailable for %d runtime(s); usage incomplete for %d session(s)", missing, incompleteUsage))
	}
	return cacheErr
}
