package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Cursor CLI reads stay bounded: state comes from the tail of a transcript,
// and a chat's metadata row is rejected past cursorChatMetaMaxBytes.
const (
	CursorTranscriptTailBytes = int64(4 << 20)
	cursorChatMetaMaxBytes    = 1 << 20
	cursorWorkspaceMaxReads   = 256
)

// CursorSource is the Source value for Cursor CLI sessions.
const CursorSource = "Cursor CLI"

// cursorHelperCommands are Cursor CLI subcommands that manage the account or
// installation rather than run a session.
var cursorHelperCommands = map[string]bool{
	"login": true, "logout": true, "status": true, "whoami": true, "update": true,
	"upgrade": true, "mcp": true, "about": true, "help": true, "create-chat": true,
	"install-shell-integration": true, "uninstall-shell-integration": true,
}

// isCursorCommand reports whether command runs a Cursor CLI session. It looks
// at the executable and script, not substrings, so the Cursor editor and paths
// such as ~/.cursor in other commands do not match.
func (d *discoverer) isCursorCommand(command string) bool {
	args := d.cursorArgs(command)
	if args == nil {
		return false
	}
	return len(args) == 0 || !cursorHelperCommands[args[0]]
}

// cursorArgs returns the arguments after the Cursor CLI executable, or nil
// when command is not Cursor CLI. The installer puts a cursor-agent launcher
// script (also linked as agent) in ~/.local/bin, which runs a bundled node on
// index.js under ~/.local/share/cursor-agent/versions/<version>/. The bare
// name agent is too generic to match on its own, so only the launcher's
// cursor-agent name and the versioned install directory are recognized.
func (d *discoverer) cursorArgs(command string) []string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	if strings.Contains(filepath.ToSlash(fields[0]), ".app/Contents/") {
		return nil
	}
	switch strings.ToLower(filepath.Base(fields[0])) {
	case "cursor-agent", "cursor-agent.exe":
		return append([]string{}, fields[1:]...)
	case "node", "node.exe", "bun", "bun.exe", "sh", "bash", "zsh":
		for i := 1; i < len(fields); i++ {
			if strings.HasPrefix(fields[i], "-") {
				continue // interpreter flags such as --use-system-ca
			}
			if isCursorScript(fields[i]) {
				return append([]string{}, fields[i+1:]...)
			}
			return nil
		}
	}
	return nil
}

func isCursorScript(path string) bool {
	if filepath.Base(path) == "cursor-agent" {
		return true
	}
	return strings.Contains(filepath.ToSlash(path), "/cursor-agent/versions/")
}

// cursorSessionIDFromCommand reads the chat id from --resume. Bare --resume,
// and the resume subcommand, pick the latest chat and name none.
func (d *discoverer) cursorSessionIDFromCommand(command string) (string, bool) {
	args := d.cursorArgs(command)
	for index, field := range args {
		var value string
		switch {
		case strings.HasPrefix(field, "--resume="):
			value = strings.TrimPrefix(field, "--resume=")
		case field == "--resume" && index+1 < len(args):
			value = args[index+1]
		default:
			continue
		}
		if value = strings.Trim(value, "'\""); d.validUUID(value) {
			return value, true
		}
	}
	return "", false
}

func (d *discoverer) cursorSessionIDFromGroupCommands(group ProcessGroup) (string, bool) {
	for _, process := range append([]ProcessInfo{group.Root}, group.Processes...) {
		if id, ok := d.cursorSessionIDFromCommand(process.Command); ok {
			return id, true
		}
	}
	return "", false
}

func (d *discoverer) enrichCursorSessions(groups []ProcessGroup) error {
	var groupIndexes []int
	for index := range groups {
		if groups[index].Tool == "Cursor" {
			groupIndexes = append(groupIndexes, index)
		}
	}
	if len(groupIndexes) == 0 {
		return nil
	}
	dir, err := d.cursorDir()
	if err != nil {
		return err
	}
	workDirs := d.claudeWorkDirs(groups, groupIndexes)

	// An id from --resume wins. Otherwise each runtime's working directory
	// picks its chats: the newest chats there go to the newest processes
	// (higher PIDs). This is a guess; see the transcript package's NOTES.md.
	ids := make(map[int]string, len(groupIndexes))
	claimed := make(map[string]bool)
	byDir := make(map[string][]int)
	for _, index := range groupIndexes {
		if id, ok := d.cursorSessionIDFromGroupCommands(groups[index]); ok {
			ids[index] = id
			claimed[id] = true
			continue
		}
		if cwd := workDirs[groups[index].Root.PID]; cwd != "" {
			byDir[cwd] = append(byDir[cwd], index)
		}
	}
	for cwd, indexes := range byDir {
		sort.Slice(indexes, func(i, j int) bool { return groups[indexes[i]].Root.PID > groups[indexes[j]].Root.PID })
		candidates := newestCursorSessions(dir, cwd, claimed)
		for i, index := range indexes {
			if i < len(candidates) {
				ids[index] = candidates[i]
				claimed[candidates[i]] = true
			}
		}
	}

	missing := 0
	for _, index := range groupIndexes {
		id, ok := ids[index]
		if !ok {
			missing++
			continue
		}
		cwd := workDirs[groups[index].Root.PID]
		info := Info{ID: id, CWD: d.sanitizeProcessCommand(cwd), Source: CursorSource, Name: CursorSource}
		transcriptPath := FindCursorTranscript(dir, id)
		chatPath := FindCursorChat(dir, cwd, id)
		if transcriptPath == "" && chatPath == "" {
			missing++
			continue
		}
		if transcriptPath != "" {
			if t, err := ReadCursorTranscript(transcriptPath); err == nil {
				info.State, info.Model, info.UpdatedAt = t.State, d.sanitizeProcessCommand(t.Model), t.UpdatedAt
				info.RolloutPath = transcriptPath
			}
		}
		if chatPath != "" {
			// Best effort: without sqlite3 the transcript still gives state.
			if chat, err := d.readCursorChat(chatPath); err == nil && chat.ID == id {
				info.Name = d.emptyFallback(d.sanitizeProcessCommand(chat.Name), CursorSource)
				info.Model = d.firstNonEmpty(info.Model, d.sanitizeProcessCommand(chat.Model))
				info.StartedAt = chat.CreatedAt
			}
			if modTime := cursorChatModTime(chatPath); modTime.After(info.UpdatedAt) {
				info.UpdatedAt = modTime
			}
		}
		groups[index].Sessions = []Info{info}
	}
	if missing > 0 {
		return fmt.Errorf("metadata unavailable for %d runtime(s)", missing)
	}
	return nil
}

func (d *discoverer) cursorDir() (string, error) {
	if d.opts.CursorDir != "" {
		return d.opts.CursorDir, nil
	}
	if dir := strings.TrimSpace(os.Getenv("CURSOR_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	home, err := d.home()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".cursor"), nil
}

// CursorDir returns the directory Cursor CLI keeps chats and transcripts in,
// honoring CURSOR_CONFIG_DIR (default ~/.cursor).
func CursorDir() (string, error) { return standalone().cursorDir() }

// CursorProjectKey reduces a working directory, or the name Cursor gave its
// project directory, to a comparable form: lowercase ASCII letters and digits
// in runs separated by single '-'. Cursor names a project directory after
// its workspace path with separators replaced by '-' (/Users/me/app becomes
// Users-me-app), so both sides reduce to the same key. The key is lossy:
// my-app and my/app collide.
func CursorProjectKey(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// CursorProjectDirs returns the project directories under dir/projects whose
// name matches cwd's key.
func CursorProjectDirs(dir, cwd string) []string {
	key := CursorProjectKey(cwd)
	if key == "" {
		return nil
	}
	root := filepath.Join(dir, "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && CursorProjectKey(entry.Name()) == key {
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	return dirs
}

// CursorWorkspace returns the existing directory a project directory name
// encodes, or "" when none is found. The encoding is lossy, so this walks the
// filesystem from the root and accepts only a directory whose own key equals
// the name's key; it never guesses a path that does not exist.
func CursorWorkspace(projectDirName string) string {
	return cursorWorkspaceFrom(string(filepath.Separator), projectDirName)
}

func cursorWorkspaceFrom(root, projectDirName string) string {
	want := CursorProjectKey(projectDirName)
	if want == "" {
		return ""
	}
	budget := cursorWorkspaceMaxReads
	var walk func(dir, rest string) string
	walk = func(dir, rest string) string {
		if budget == 0 {
			return ""
		}
		budget--
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			key := CursorProjectKey(entry.Name())
			if key == "" || (key != rest && !strings.HasPrefix(rest, key+"-")) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				continue
			}
			if key == rest {
				return path
			}
			if found := walk(path, rest[len(key)+1:]); found != "" {
				return found
			}
		}
		return ""
	}
	return walk(root, want)
}

// CursorChatsDir is the directory holding cwd's chat databases:
// dir/chats/<md5 of cwd>.
func CursorChatsDir(dir, cwd string) string {
	sum := md5.Sum([]byte(cwd))
	return filepath.Join(dir, "chats", hex.EncodeToString(sum[:]))
}

// FindCursorChat returns the chat database for id, looking first under
// cwd's chats directory when cwd is known, then under every workspace's. It
// returns "" when there is none.
func FindCursorChat(dir, cwd, id string) string {
	if !ValidUUID(id) {
		return ""
	}
	if cwd != "" {
		if path := filepath.Join(CursorChatsDir(dir, cwd), id, "store.db"); fileExists(path) {
			return path
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "chats", "*", id, "store.db"))
	sort.Strings(matches)
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// cursorChatModTime is when a chat database last changed, counting its
// write-ahead log.
func cursorChatModTime(path string) time.Time {
	var newest time.Time
	for _, p := range []string{path, path + "-wal"} {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// CursorTranscriptID returns the session id of a main transcript, named
// agent-transcripts/<id>/<id>.jsonl or agent-transcripts/<id>.jsonl, and ""
// for anything else, including subagent transcripts under <id>/subagents/.
func CursorTranscriptID(path string) string {
	id, ok := strings.CutSuffix(filepath.Base(path), ".jsonl")
	if !ok || !ValidUUID(id) {
		return ""
	}
	switch parent := filepath.Base(filepath.Dir(path)); {
	case parent == "agent-transcripts":
		return id
	case strings.EqualFold(parent, id) && filepath.Base(filepath.Dir(filepath.Dir(path))) == "agent-transcripts":
		return id
	}
	return ""
}

// CursorSessionFiles lists the main transcripts in a project directory.
func CursorSessionFiles(projectDir string) []string {
	root := filepath.Join(projectDir, "agent-transcripts")
	nested, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	flat, _ := filepath.Glob(filepath.Join(root, "*.jsonl"))
	var paths []string
	for _, path := range append(nested, flat...) {
		if CursorTranscriptID(path) == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

// FindCursorTranscript returns the main transcript for id in any project
// directory, the most recently written when there are several, or "".
func FindCursorTranscript(dir, id string) string {
	if !ValidUUID(id) {
		return ""
	}
	projects := filepath.Join(dir, "projects", "*", "agent-transcripts")
	nested, _ := filepath.Glob(filepath.Join(projects, id, id+".jsonl"))
	flat, _ := filepath.Glob(filepath.Join(projects, id+".jsonl"))
	var best string
	var bestTime time.Time
	for _, path := range append(nested, flat...) {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = path, info.ModTime()
		}
	}
	return best
}

// newestCursorSessions lists the ids of cwd's chats, newest first, skipping
// claimed ones. A chat database appears when a session starts and a
// transcript once it has messages, so both are counted.
func newestCursorSessions(dir, cwd string, claimed map[string]bool) []string {
	newest := map[string]time.Time{}
	note := func(id string, modTime time.Time) {
		if id == "" || claimed[id] {
			return
		}
		if previous, ok := newest[id]; !ok || modTime.After(previous) {
			newest[id] = modTime
		}
	}
	for _, projectDir := range CursorProjectDirs(dir, cwd) {
		for _, path := range CursorSessionFiles(projectDir) {
			if info, err := os.Stat(path); err == nil {
				note(CursorTranscriptID(path), info.ModTime())
			}
		}
	}
	chats := CursorChatsDir(dir, cwd)
	if entries, err := os.ReadDir(chats); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !ValidUUID(entry.Name()) {
				continue
			}
			if modTime := cursorChatModTime(filepath.Join(chats, entry.Name(), "store.db")); !modTime.IsZero() {
				note(entry.Name(), modTime)
			}
		}
	}
	ids := make([]string, 0, len(newest))
	for id := range newest {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if a, b := newest[ids[i]], newest[ids[j]]; !a.Equal(b) {
			return a.After(b)
		}
		return ids[i] > ids[j]
	})
	return ids
}

// CursorChat is the metadata row of a chat database. No message is read.
type CursorChat struct {
	ID        string
	Name      string
	Model     string
	CreatedAt time.Time
}

// ReadCursorChat reads a chat database's metadata row with sqlite3.
func ReadCursorChat(ctx context.Context, path string) (CursorChat, error) {
	return (&discoverer{ctx: ctx}).readCursorChat(path)
}

// readCursorChat decodes the meta table's row "0": JSON, stored hex-encoded,
// with agentId, name, createdAt in Unix milliseconds, and lastUsedModel.
// The model "default" means Cursor's automatic choice and is left out.
func (d *discoverer) readCursorChat(path string) (CursorChat, error) {
	output, err := d.command("sqlite3", "-readonly", "-json", path, "SELECT CAST(value AS TEXT) AS value FROM meta WHERE key = '0' LIMIT 1").Output()
	if err != nil {
		return CursorChat{}, errors.New("read Cursor chat database")
	}
	var rows []struct {
		Value string `json:"value"`
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return CursorChat{}, errors.New("cursor: chat database has no metadata")
	}
	if json.Unmarshal(output, &rows) != nil || len(rows) == 0 {
		return CursorChat{}, errors.New("cursor: decode chat database rows")
	}
	return parseCursorChatMeta(rows[0].Value)
}

func parseCursorChatMeta(value string) (CursorChat, error) {
	value = strings.TrimSpace(value)
	if len(value) > cursorChatMetaMaxBytes {
		return CursorChat{}, errors.New("cursor: chat metadata is too large")
	}
	raw := []byte(value)
	if decoded, err := hex.DecodeString(value); err == nil {
		raw = decoded
	}
	var meta struct {
		AgentID       string          `json:"agentId"`
		Name          string          `json:"name"`
		CreatedAt     json.RawMessage `json:"createdAt"`
		LastUsedModel string          `json:"lastUsedModel"`
	}
	if json.Unmarshal(raw, &meta) != nil || !ValidUUID(meta.AgentID) {
		return CursorChat{}, errors.New("cursor: chat metadata has no agent id")
	}
	chat := CursorChat{ID: meta.AgentID, Name: strings.TrimSpace(meta.Name), Model: strings.TrimSpace(meta.LastUsedModel)}
	if chat.Model == "default" {
		chat.Model = ""
	}
	var ms float64
	if json.Unmarshal(meta.CreatedAt, &ms) == nil && ms > 0 {
		chat.CreatedAt = time.UnixMilli(int64(ms))
	}
	return chat, nil
}

// CursorTranscript is what discovery reads from a transcript. No message
// text is kept.
type CursorTranscript struct {
	Model string
	// UpdatedAt is the file's modification time: records carry no
	// timestamps.
	UpdatedAt time.Time
	State     State
}

// cursorRecord holds the transcript fields discovery reads. Lines are
// {role, message: {content}} with Anthropic-style content blocks, or a
// {type: "turn_ended"} marker.
type cursorRecord struct {
	Role    string `json:"role"`
	Type    string `json:"type"`
	Model   string `json:"model"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type cursorBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ReadCursorTranscript reads best-effort state and the model, when one is
// recorded, from the last CursorTranscriptTailBytes of a transcript.
func ReadCursorTranscript(path string) (CursorTranscript, error) {
	file, err := os.Open(path)
	if err != nil {
		return CursorTranscript{}, errors.New("cursor: open transcript")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return CursorTranscript{}, errors.New("cursor: stat transcript")
	}
	t := CursorTranscript{UpdatedAt: info.ModTime()}
	start := max(info.Size()-CursorTranscriptTailBytes, 0)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return CursorTranscript{}, errors.New("cursor: read transcript")
	}
	reader := bufio.NewReader(io.LimitReader(file, info.Size()-start))
	if start > 0 {
		if _, err := reader.ReadBytes('\n'); err != nil {
			return t, nil // no complete line in the tail
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// A line without a newline is still being written.
			if errors.Is(err, io.EOF) {
				return t, nil
			}
			return CursorTranscript{}, errors.New("cursor: read transcript")
		}
		var rec cursorRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Model != "" {
			t.Model = rec.Model
		}
		t.State = nextCursorState(t.State, rec)
	}
}

// nextCursorState follows the latest observable record. A prompt or tool
// result means the model is working, a reply ending in a tool call means a
// tool is running, and a reply ending in text, or the end of a turn, means
// Cursor CLI is waiting. Approval prompts are not recorded, so a tool waiting
// on approval reads as ACTIVE, and NEEDS_INPUT is never reported.
func nextCursorState(state State, rec cursorRecord) State {
	if rec.Type == "turn_ended" {
		return SessionStateWaiting
	}
	switch strings.ToLower(rec.Role) {
	case "user":
		return SessionStateActive
	case "assistant":
		if rec.Message == nil {
			return state
		}
		var blocks []cursorBlock
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			var text string
			if json.Unmarshal(rec.Message.Content, &text) == nil && strings.TrimSpace(text) != "" {
				return SessionStateWaiting
			}
			return state
		}
		for i := len(blocks) - 1; i >= 0; i-- {
			switch strings.ToLower(blocks[i].Type) {
			case "tool_use", "tool-use", "tool_call", "tool-call":
				return SessionStateActive
			case "text":
				if strings.TrimSpace(blocks[i].Text) != "" {
					return SessionStateWaiting
				}
			}
		}
		// Only thinking so far.
		return SessionStateActive
	}
	return state
}
