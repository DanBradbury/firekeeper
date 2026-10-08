package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Claude Code reads stay bounded: state and metadata come from the tail of a
// transcript, the start time from its head.
const (
	ClaudeTranscriptTailBytes = int64(4 << 20)
	ClaudeTranscriptHeadBytes = int64(64 << 10)
)

// claudeHelperCommands are Claude Code subcommands that run as background
// helpers or one-off tools rather than as a session.
var claudeHelperCommands = map[string]bool{
	"daemon": true, "mcp": true, "config": true, "update": true, "upgrade": true,
	"doctor": true, "install": true, "setup-token": true, "migrate-installer": true,
	"plugin": true, "auth": true,
}

// isClaudeCodeCommand reports whether command runs a Claude Code session. It
// looks at the executable, not substrings, so paths such as ~/.claude in other
// commands and the Claude desktop app do not match.
func (d *discoverer) isClaudeCodeCommand(command string) bool {
	args := d.claudeCodeArgs(command)
	if args == nil {
		return false
	}
	if len(args) > 0 && (claudeHelperCommands[args[0]] || strings.HasPrefix(args[0], "bg-")) {
		return false
	}
	return true
}

// claudeCodeArgs returns the arguments after the Claude Code executable, or
// nil when command is not Claude Code. Both the native binary and the npm
// package run through node are recognized.
func (d *discoverer) claudeCodeArgs(command string) []string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	switch base := strings.ToLower(filepath.Base(fields[0])); base {
	case "claude", "claude.exe":
		return append([]string{}, fields[1:]...)
	case "node", "node.exe", "bun", "bun.exe":
		if len(fields) > 1 && strings.Contains(filepath.ToSlash(fields[1]), "@anthropic-ai/claude-code/") {
			return append([]string{}, fields[2:]...)
		}
	}
	return nil
}

// claudeSessionIDFromCommand reads an explicit session id from --session-id,
// or from --resume when the session is not being forked to a new id.
func (d *discoverer) claudeSessionIDFromCommand(command string) (string, bool) {
	args := d.claudeCodeArgs(command)
	value := func(index int, field, flag string) (string, bool) {
		if strings.HasPrefix(field, flag+"=") {
			return strings.TrimPrefix(field, flag+"="), true
		}
		if field == flag && index+1 < len(args) {
			return args[index+1], true
		}
		return "", false
	}
	fork := false
	sessionID, resumeID := "", ""
	for index, field := range args {
		if field == "--fork-session" {
			fork = true
		}
		if v, ok := value(index, field, "--session-id"); ok {
			sessionID = strings.Trim(v, "'\"")
		}
		for _, flag := range []string{"--resume", "-r"} {
			if v, ok := value(index, field, flag); ok {
				// --resume takes an id or a transcript path.
				resumeID = strings.TrimSuffix(filepath.Base(strings.Trim(v, "'\"")), ".jsonl")
			}
		}
	}
	if d.validUUID(sessionID) {
		return sessionID, true
	}
	if !fork && d.validUUID(resumeID) {
		return resumeID, true
	}
	return "", false
}

// claudeProjectDirName encodes a working directory the way Claude Code names
// its project directories: every character other than an ASCII letter or
// digit becomes '-'.
func (d *discoverer) claudeProjectDirName(cwd string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
}

func (d *discoverer) enrichClaudeSessions(groups []ProcessGroup) error {
	var groupIndexes []int
	for index := range groups {
		if groups[index].Tool == "Claude" {
			groupIndexes = append(groupIndexes, index)
		}
	}
	if len(groupIndexes) == 0 {
		return nil
	}
	dir, err := d.claudeConfigDir()
	if err != nil {
		return err
	}
	projects := filepath.Join(dir, "projects")

	// Explicit ids from the command line win. Their transcript may not exist
	// yet; it appears once the first message is sent.
	paths := make(map[int]string, len(groupIndexes))
	claimed := make(map[string]bool)
	var unmapped []int
	for _, index := range groupIndexes {
		id, ok := d.claudeSessionIDFromGroupCommands(groups[index])
		if !ok {
			unmapped = append(unmapped, index)
			continue
		}
		claimed[strings.ToLower(id)] = true
		// Resuming from another directory can leave a copy per project
		// directory; the newest one is being written.
		matches, _ := filepath.Glob(filepath.Join(projects, "*", id+".jsonl"))
		var newest time.Time
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && (paths[index] == "" || info.ModTime().After(newest)) {
				paths[index], newest = match, info.ModTime()
			}
		}
	}

	// Otherwise map each runtime's working directory to its project
	// directory and take the most recently written transcripts there. When
	// several runtimes share a directory, newer processes (higher PIDs) get
	// newer transcripts. This is a guess; see NOTES.md.
	if len(unmapped) > 0 {
		workDirs := d.claudeWorkDirs(groups, unmapped)
		byDir := make(map[string][]int)
		for _, index := range unmapped {
			if cwd := workDirs[groups[index].Root.PID]; cwd != "" {
				byDir[cwd] = append(byDir[cwd], index)
			}
		}
		for cwd, indexes := range byDir {
			sort.Slice(indexes, func(i, j int) bool { return groups[indexes[i]].Root.PID > groups[indexes[j]].Root.PID })
			candidates := d.newestClaudeTranscripts(filepath.Join(projects, d.claudeProjectDirName(cwd)), claimed)
			for i, index := range indexes {
				if i < len(candidates) {
					paths[index] = candidates[i]
				}
			}
		}
	}

	missing := 0
	for _, index := range groupIndexes {
		path, ok := paths[index]
		if !ok {
			missing++
			continue
		}
		info, err := d.readClaudeTranscriptMetadata(path)
		if err != nil {
			missing++
			continue
		}
		groups[index].Sessions = []SessionInfo{info}
	}
	if missing > 0 {
		return fmt.Errorf("metadata unavailable for %d runtime(s)", missing)
	}
	return nil
}

func (d *discoverer) claudeSessionIDFromGroupCommands(group ProcessGroup) (string, bool) {
	for _, process := range append([]ProcessInfo{group.Root}, group.Processes...) {
		if id, ok := d.claudeSessionIDFromCommand(process.Command); ok {
			return id, true
		}
	}
	return "", false
}

// claudeWorkDirs returns the current directory of each group's root process.
func (d *discoverer) claudeWorkDirs(groups []ProcessGroup, indexes []int) map[int]string {
	pids := make([]string, len(indexes))
	for i, index := range indexes {
		pids[i] = strconv.Itoa(groups[index].Root.PID)
	}
	workDirs := make(map[int]string)
	output, err := d.command("lsof", "-a", "-d", "cwd", "-Fn", "-p", strings.Join(pids, ",")).Output()
	if err != nil && len(output) == 0 {
		return workDirs
	}
	pid := 0
	for _, line := range strings.Split(string(output), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid != 0 {
				workDirs[pid] = line[1:]
			}
		}
	}
	return workDirs
}

// newestClaudeTranscripts lists session transcripts in dir, newest first,
// skipping ids already claimed by another runtime.
func (d *discoverer) newestClaudeTranscripts(dir string, claimed map[string]bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		id, isJSONL := strings.CutSuffix(entry.Name(), ".jsonl")
		if entry.IsDir() || !isJSONL || !d.validUUID(id) || claimed[strings.ToLower(id)] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{filepath.Join(dir, entry.Name()), info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].path > candidates[j].path
		}
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		paths[i] = c.path
	}
	return paths
}

func (d *discoverer) claudeConfigDir() (string, error) {
	if d.opts.ClaudeHome != "" {
		return d.opts.ClaudeHome, nil
	}
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	home, err := d.home()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// claudeRecord holds the transcript fields discovery reads. Message text is
// only inspected for the interrupt marker and is never kept.
type claudeRecord struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	Entrypoint  string `json:"entrypoint"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	AITitle     string `json:"aiTitle"`
	AgentName   string `json:"agentName"`
	Message     *struct {
		Model      string          `json:"model"`
		StopReason *string         `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
	ModelUsage map[string]struct {
		Input         int64 `json:"inputTokens"`
		Output        int64 `json:"outputTokens"`
		CacheRead     int64 `json:"cacheReadInputTokens"`
		CacheCreation int64 `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
}

// claudeBlock holds the content block fields state tracking needs.
type claudeBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
}

// claudeInputTools wait on the user rather than running on their own.
var claudeInputTools = map[string]bool{"AskUserQuestion": true, "ExitPlanMode": true}

// claudeTranscriptMetadata is what discovery reads from one transcript.
type claudeTranscriptMetadata struct {
	ID         string
	Title      string
	CWD        string
	GitBranch  string
	Model      string
	Entrypoint string
	State      SessionState
	StartedAt  time.Time
	UpdatedAt  time.Time
	TokensUsed int64
}

func (d *discoverer) readClaudeTranscriptMetadata(path string) (SessionInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, errors.New("open Claude Code transcript")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return SessionInfo{}, errors.New("stat Claude Code transcript")
	}

	var startedAt time.Time
	start := max(info.Size()-ClaudeTranscriptTailBytes, 0)
	if start > 0 {
		head, _ := d.scanClaudeTranscript(io.LimitReader(file, ClaudeTranscriptHeadBytes), false)
		startedAt = head.StartedAt
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return SessionInfo{}, errors.New("seek Claude Code transcript")
		}
	}
	meta, err := d.scanClaudeTranscript(file, start > 0)
	if err != nil {
		return SessionInfo{}, errors.New("read Claude Code transcript")
	}
	if !startedAt.IsZero() {
		meta.StartedAt = startedAt
	}

	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !d.validUUID(id) {
		id = meta.ID
	}
	updated := d.newestTime(meta.UpdatedAt, info.ModTime())
	return SessionInfo{
		ID:          id,
		Name:        d.emptyFallback(d.sanitizeProcessCommand(meta.Title), "Claude Code"),
		State:       meta.State,
		CWD:         d.sanitizeProcessCommand(meta.CWD),
		Model:       d.sanitizeProcessCommand(meta.Model),
		Source:      d.emptyFallback(d.sanitizeProcessCommand(meta.Entrypoint), "Claude Code"),
		GitBranch:   d.sanitizeProcessCommand(meta.GitBranch),
		RolloutPath: path,
		StartedAt:   meta.StartedAt,
		UpdatedAt:   updated,
		TokensUsed:  meta.TokensUsed,
	}, nil
}

// scanClaudeTranscript reads metadata and the latest observable state from
// transcript lines. With skipPartial set, the first line is dropped because
// the reader starts mid-line.
func (d *discoverer) scanClaudeTranscript(reader io.Reader, skipPartial bool) (claudeTranscriptMetadata, error) {
	var meta claudeTranscriptMetadata
	state := SessionStateUnknown
	pending := make(map[string]string)
	buffered := bufio.NewReaderSize(reader, 256<<10)
	first := true
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 && !(first && skipPartial) {
			var rec claudeRecord
			if json.Unmarshal(line, &rec) == nil {
				d.trackClaudeRecord(&meta, rec)
				state = d.nextClaudeState(state, rec, pending)
			}
		}
		first = false
		if errors.Is(err, io.EOF) {
			meta.State = state
			return meta, nil
		}
		if err != nil {
			return meta, err
		}
	}
}

func (d *discoverer) trackClaudeRecord(meta *claudeTranscriptMetadata, rec claudeRecord) {
	if meta.ID == "" && d.validUUID(rec.SessionID) {
		meta.ID = rec.SessionID
	}
	if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
		if meta.StartedAt.IsZero() {
			meta.StartedAt = ts
		}
		meta.UpdatedAt = d.newestTime(meta.UpdatedAt, ts)
	}
	if rec.IsSidechain {
		return
	}
	if rec.CWD != "" {
		meta.CWD = rec.CWD
	}
	if rec.GitBranch != "" {
		meta.GitBranch = rec.GitBranch
	}
	if rec.Entrypoint != "" {
		meta.Entrypoint = rec.Entrypoint
	}
	switch rec.Type {
	case "ai-title":
		if rec.AITitle != "" {
			meta.Title = rec.AITitle
		}
	case "agent-name":
		if rec.AgentName != "" {
			meta.Title = rec.AgentName
		}
	case "assistant":
		if rec.Message != nil && rec.Message.Model != "" && rec.Message.Model != "<synthetic>" {
			meta.Model = rec.Message.Model
		}
	case "cost-state":
		// Cumulative totals Claude Code saves for the session; the latest wins.
		var total int64
		for _, usage := range rec.ModelUsage {
			total += usage.Input + usage.Output + usage.CacheRead + usage.CacheCreation
		}
		if total > 0 {
			meta.TokensUsed = total
		}
	}
}

// nextClaudeState follows the main conversation. A user prompt or tool
// result means the model is working; a pending tool call means a tool is
// running, or the user is being asked a question; the end of a turn means
// Claude Code is waiting for the next prompt. Permission prompts are not
// written to the transcript, so a tool waiting on approval reads as ACTIVE.
func (d *discoverer) nextClaudeState(state SessionState, rec claudeRecord, pending map[string]string) SessionState {
	if rec.IsSidechain {
		return state
	}
	switch rec.Type {
	case "user":
		if rec.IsMeta || rec.Message == nil {
			return state
		}
		var blocks []claudeBlock
		var text string
		if json.Unmarshal(rec.Message.Content, &text) == nil {
			blocks = []claudeBlock{{Type: "text", Text: text}}
		} else if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			return state
		}
		results := false
		for _, b := range blocks {
			if b.Type == "tool_result" {
				results = true
				delete(pending, b.ToolUseID)
			}
		}
		if results {
			return claudePendingState(pending)
		}
		// A new prompt, or the marker Claude Code writes when the user
		// interrupts a turn.
		clear(pending)
		for _, b := range blocks {
			if b.Type == "text" && strings.HasPrefix(b.Text, "[Request interrupted by user") {
				return SessionStateWaiting
			}
		}
		return SessionStateActive
	case "assistant":
		if rec.Message == nil {
			return state
		}
		var blocks []claudeBlock
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			return state
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" {
				pending[b.ID] = b.Name
			}
		}
		if len(pending) > 0 {
			return claudePendingState(pending)
		}
		if stop := rec.Message.StopReason; stop != nil && (*stop == "end_turn" || *stop == "stop_sequence") {
			return SessionStateWaiting
		}
		return SessionStateActive
	case "system":
		if rec.Subtype == "turn_duration" {
			clear(pending)
			return SessionStateWaiting
		}
	}
	return state
}

func claudePendingState(pending map[string]string) SessionState {
	for _, name := range pending {
		if claudeInputTools[name] {
			return SessionStateNeedsInput
		}
	}
	return SessionStateActive
}
