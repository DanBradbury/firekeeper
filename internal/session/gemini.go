package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
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

// Gemini CLI reads stay bounded: metadata comes from the first record and the
// tail of a session log. Legacy whole-document session files are read only
// up to GeminiLegacyMaxBytes.
const (
	GeminiTranscriptTailBytes = int64(4 << 20)
	GeminiLegacyMaxBytes      = int64(64 << 20)
	geminiHeadBytes           = int64(1 << 20)
	geminiRegistryMaxBytes    = int64(1 << 20)
	geminiMaxIDLength         = 128
)

// GeminiSource is the Source value for Gemini CLI sessions.
const GeminiSource = "Gemini CLI"

// geminiHelperCommands are Gemini CLI subcommands that manage configuration
// rather than run a session.
var geminiHelperCommands = map[string]bool{
	"mcp": true, "extensions": true, "extension": true, "skills": true, "skill": true,
	"hooks": true, "hook": true, "gemma": true,
}

// isGeminiCommand reports whether command runs a Gemini CLI session. Like
// Claude Code, it looks at the executable and script, not substrings, so
// model names such as gemini-2.5-pro in other harnesses' flags do not match.
func (d *discoverer) isGeminiCommand(command string) bool {
	args := d.geminiArgs(command)
	if args == nil {
		return false
	}
	return len(args) == 0 || !geminiHelperCommands[args[0]]
}

// geminiArgs returns the arguments after the Gemini CLI executable, or nil
// when command is not Gemini CLI. The npm package runs as a node script
// (bin/gemini, bundle/gemini.js, or a path inside @google/gemini-cli), and it
// relaunches itself as a node child with extra node flags.
func (d *discoverer) geminiArgs(command string) []string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	if strings.Contains(filepath.ToSlash(fields[0]), ".app/Contents/") {
		return nil
	}
	switch strings.ToLower(filepath.Base(fields[0])) {
	case "gemini", "gemini.exe":
		return append([]string{}, fields[1:]...)
	case "node", "node.exe", "nodejs", "bun", "bun.exe":
		for i := 1; i < len(fields); i++ {
			if strings.HasPrefix(fields[i], "-") {
				continue // node flags such as --max-old-space-size=8192
			}
			if isGeminiScript(fields[i]) {
				return append([]string{}, fields[i+1:]...)
			}
			return nil
		}
	}
	return nil
}

func isGeminiScript(path string) bool {
	switch filepath.Base(path) {
	case "gemini", "gemini.js", "gemini.mjs":
		return true
	}
	return strings.Contains(filepath.ToSlash(path), "/@google/gemini-cli/")
}

// geminiSessionIDFromCommand reads an explicit session id from --session-id,
// or from --resume when it names a session id rather than "latest" or a list
// index.
func (d *discoverer) geminiSessionIDFromCommand(command string) (string, bool) {
	args := d.geminiArgs(command)
	value := func(index int, field, flag string) (string, bool) {
		if strings.HasPrefix(field, flag+"=") {
			return strings.Trim(strings.TrimPrefix(field, flag+"="), "'\""), true
		}
		if field == flag && index+1 < len(args) {
			return strings.Trim(args[index+1], "'\""), true
		}
		return "", false
	}
	for index, field := range args {
		if v, ok := value(index, field, "--session-id"); ok && ValidGeminiSessionID(v) {
			return v, true
		}
		for _, flag := range []string{"--resume", "-r"} {
			if v, ok := value(index, field, flag); ok && d.validUUID(v) {
				return v, true
			}
		}
	}
	return "", false
}

func (d *discoverer) geminiSessionIDFromGroupCommands(group ProcessGroup) (string, bool) {
	for _, process := range append([]ProcessInfo{group.Root}, group.Processes...) {
		if id, ok := d.geminiSessionIDFromCommand(process.Command); ok {
			return id, true
		}
	}
	return "", false
}

// ValidGeminiSessionID reports whether id can be a Gemini CLI session id:
// Gemini CLI generates UUIDs and accepts --session-id values made of letters,
// digits, '-' and '_'. Anything else could escape a directory or act as a glob
// pattern, so it is rejected. Project directory names follow the same rule.
func ValidGeminiSessionID(id string) bool {
	if id == "" || len(id) > geminiMaxIDLength {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func (d *discoverer) enrichGeminiSessions(groups []ProcessGroup) error {
	var groupIndexes []int
	for index := range groups {
		if groups[index].Tool == "Gemini" {
			groupIndexes = append(groupIndexes, index)
		}
	}
	if len(groupIndexes) == 0 {
		return nil
	}
	dirs, err := d.geminiDirs()
	if err != nil {
		return err
	}

	// Explicit ids from the command line win. Gemini CLI writes a session's
	// first record when it starts, so the log exists from the beginning.
	paths := make(map[int]string, len(groupIndexes))
	claimed := make(map[string]bool)
	var unmapped []int
	for _, index := range groupIndexes {
		if id, ok := d.geminiSessionIDFromGroupCommands(groups[index]); ok {
			if path := FindGeminiTranscript(dirs, id); path != "" {
				paths[index] = path
				claimed[path] = true
				continue
			}
		}
		unmapped = append(unmapped, index)
	}

	// Otherwise map each runtime's working directory to its project
	// directory and take the most recently written logs there. When several
	// runtimes share a directory, newer processes (higher PIDs) get newer
	// logs. This is a guess; see the transcript package's NOTES.md.
	workDirs := map[int]string{}
	if len(unmapped) > 0 {
		workDirs = d.claudeWorkDirs(groups, unmapped)
		byDir := make(map[string][]int)
		for _, index := range unmapped {
			if cwd := workDirs[groups[index].Root.PID]; cwd != "" {
				byDir[cwd] = append(byDir[cwd], index)
			}
		}
		for cwd, indexes := range byDir {
			sort.Slice(indexes, func(i, j int) bool { return groups[indexes[i]].Root.PID > groups[indexes[j]].Root.PID })
			candidates := newestGeminiTranscripts(dirs, cwd, claimed)
			for i, index := range indexes {
				if i < len(candidates) {
					paths[index] = candidates[i]
					claimed[candidates[i]] = true
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
		t, err := ReadGeminiTranscript(path)
		if err != nil || t.Kind == "subagent" {
			missing++
			continue
		}
		groups[index].Sessions = []Info{{
			ID:          t.SessionID,
			Name:        d.emptyFallback(d.sanitizeProcessCommand(t.Summary), GeminiSource),
			State:       t.State,
			CWD:         d.sanitizeProcessCommand(workDirs[groups[index].Root.PID]),
			Model:       d.sanitizeProcessCommand(t.Model),
			Source:      GeminiSource,
			StartedAt:   t.StartedAt,
			UpdatedAt:   t.UpdatedAt,
			RolloutPath: path,
		}}
	}
	if missing > 0 {
		return fmt.Errorf("metadata unavailable for %d runtime(s)", missing)
	}
	return nil
}

func (d *discoverer) geminiDirs() ([]string, error) {
	if d.opts.GeminiDir != "" {
		return []string{d.opts.GeminiDir}, nil
	}
	home := strings.TrimSpace(os.Getenv("GEMINI_CLI_HOME"))
	if home == "" {
		var err error
		if home, err = d.home(); err != nil {
			return nil, fmt.Errorf("find home directory: %w", err)
		}
	}
	// Under the macOS seatbelt sandbox Gemini CLI keeps its runtime state in
	// ~/.cache/.gemini instead.
	return []string{filepath.Join(home, ".gemini"), filepath.Join(home, ".cache", ".gemini")}, nil
}

// GeminiDirs returns the directories Gemini CLI keeps session state in:
// $GEMINI_CLI_HOME/.gemini (default ~/.gemini), then the ~/.cache/.gemini
// directory its macOS sandbox uses. Either may be missing.
func GeminiDirs() ([]string, error) { return standalone().geminiDirs() }

// GeminiProjectHash is the directory name older Gemini CLI releases gave a
// project, and the projectHash recorded in every session log.
func GeminiProjectHash(projectRoot string) string {
	sum := sha256.Sum256([]byte(projectRoot))
	return hex.EncodeToString(sum[:])
}

// readGeminiRegistry reads projects.json, which maps project roots to the
// short directory names under tmp/. A missing or invalid registry is empty.
func readGeminiRegistry(runtimeDir string) map[string]string {
	file, err := os.Open(filepath.Join(runtimeDir, "projects.json"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var registry struct {
		Projects map[string]string `json:"projects"`
	}
	if json.NewDecoder(io.LimitReader(file, geminiRegistryMaxBytes)).Decode(&registry) != nil {
		return nil
	}
	return registry.Projects
}

// GeminiProjectDirs returns the directories under runtimeDir/tmp that may
// hold sessions for the project rooted at cwd, most likely first: the one
// projects.json names, else any whose .project_root marker names cwd, then
// the sha256 directory older releases used.
func GeminiProjectDirs(runtimeDir, cwd string) []string {
	if cwd == "" {
		return nil
	}
	cwd = filepath.Clean(cwd)
	tmp := filepath.Join(runtimeDir, "tmp")
	var dirs []string
	add := func(name string) {
		if !ValidGeminiSessionID(name) {
			return
		}
		dir := filepath.Join(tmp, name)
		for _, existing := range dirs {
			if existing == dir {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	if slug := readGeminiRegistry(runtimeDir)[cwd]; slug != "" {
		add(slug)
	} else if entries, err := os.ReadDir(tmp); err == nil {
		// The registry can be missing or reset; the markers survive it.
		for _, entry := range entries {
			if entry.IsDir() && geminiProjectRootMarker(filepath.Join(tmp, entry.Name())) == cwd {
				add(entry.Name())
			}
		}
	}
	add(GeminiProjectHash(cwd))
	return dirs
}

func geminiProjectRootMarker(projectDir string) string {
	file, err := os.Open(filepath.Join(projectDir, ".project_root"))
	if err != nil {
		return ""
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(body))
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// GeminiProjectRoot returns the project root a tmp/<project> directory
// belongs to: its .project_root marker, else the projects.json entry naming
// it, else the projects.json entry whose hash matches projectHash. It returns
// "" when none is known.
func GeminiProjectRoot(runtimeDir, projectDir, projectHash string) string {
	if root := geminiProjectRootMarker(projectDir); root != "" {
		return root
	}
	name := filepath.Base(projectDir)
	registry := readGeminiRegistry(runtimeDir)
	roots := make([]string, 0, len(registry))
	for root := range registry {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if registry[root] == name {
			return root
		}
	}
	for _, root := range roots {
		hash := GeminiProjectHash(root)
		if hash == projectHash || hash == name {
			return root
		}
	}
	return ""
}

// isGeminiSessionFile reports whether name is a main session log: current
// releases write session-<time>-<id prefix>.jsonl, older ones .json.
func isGeminiSessionFile(name string) bool {
	return strings.HasPrefix(name, "session-") && (strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".json"))
}

// GeminiSessionFiles lists main session logs in a project directory's chats
// folder. Subagent logs live one level deeper and are not listed. A legacy
// .json file is left out when the .jsonl Gemini CLI migrated it to exists.
func GeminiSessionFiles(projectDir string) []string {
	chats := filepath.Join(projectDir, "chats")
	entries, err := os.ReadDir(chats)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !isGeminiSessionFile(name) {
			continue
		}
		if strings.HasSuffix(name, ".json") && fileExists(filepath.Join(chats, name+"l")) {
			continue
		}
		paths = append(paths, filepath.Join(chats, name))
	}
	return paths
}

// newestGeminiTranscripts lists the session logs of cwd's project, newest
// first, skipping logs another runtime already claimed.
func newestGeminiTranscripts(runtimeDirs []string, cwd string, claimed map[string]bool) []string {
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, runtimeDir := range runtimeDirs {
		for _, projectDir := range GeminiProjectDirs(runtimeDir, cwd) {
			for _, path := range GeminiSessionFiles(projectDir) {
				if claimed[path] {
					continue
				}
				if info, err := os.Stat(path); err == nil {
					candidates = append(candidates, candidate{path, info.ModTime()})
				}
			}
		}
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

// FindGeminiTranscript returns the main session log for id under any of
// runtimeDirs, or "" when there is none. File names carry only the first
// eight characters of the id, so each match's first record is checked. A
// .jsonl log wins over a legacy .json one, then the most recently written.
func FindGeminiTranscript(runtimeDirs []string, id string) string {
	if !ValidGeminiSessionID(id) {
		return ""
	}
	prefix := id
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	var best string
	var bestTime time.Time
	for _, runtimeDir := range runtimeDirs {
		for _, ext := range []string{".jsonl", ".json"} {
			matches, _ := filepath.Glob(filepath.Join(runtimeDir, "tmp", "*", "chats", "session-*-"+prefix+ext))
			for _, path := range matches {
				info, err := os.Stat(path)
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				if best != "" {
					isJSONL, bestJSONL := ext == ".jsonl", strings.HasSuffix(best, ".jsonl")
					if bestJSONL && !isJSONL || bestJSONL == isJSONL && !info.ModTime().After(bestTime) {
						continue
					}
				}
				if header, err := readGeminiHeader(path); err != nil || header.SessionID != id {
					continue
				}
				best, bestTime = path, info.ModTime()
			}
		}
	}
	return best
}

// GeminiTranscript is what discovery and enumeration read from a session log.
// No message text is kept.
type GeminiTranscript struct {
	SessionID   string
	ProjectHash string
	// Kind is "main", "subagent", or empty for logs that predate it.
	Kind      string
	Summary   string
	Model     string
	StartedAt time.Time
	UpdatedAt time.Time
	State     State
}

// geminiRecord holds the fields of a session log line that discovery reads.
// Lines are a metadata record, a message record, or a $set, $patch, or
// $rewindTo update.
type geminiRecord struct {
	SessionID   string          `json:"sessionId"`
	ProjectHash string          `json:"projectHash"`
	StartTime   string          `json:"startTime"`
	LastUpdated string          `json:"lastUpdated"`
	Summary     string          `json:"summary"`
	Kind        string          `json:"kind"`
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	Model       string          `json:"model"`
	Content     json.RawMessage `json:"content"`
	ToolCalls   []struct{}      `json:"toolCalls"`
	Set         *geminiRecord   `json:"$set"`
	Patch       json.RawMessage `json:"$patch"`
	RewindTo    *string         `json:"$rewindTo"`
}

// isMessage reports whether r is a message record, as Gemini CLI decides it:
// a string id and no $patch.
func (r geminiRecord) isMessage() bool { return r.ID != "" && r.Patch == nil && r.Set == nil }

func geminiTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// readGeminiHeader decodes the session metadata at the start of a log: the
// first line of a .jsonl log, or the top-level fields of a legacy .json one.
func readGeminiHeader(path string) (geminiRecord, error) {
	if strings.HasSuffix(path, ".json") {
		doc, err := readGeminiLegacy(path)
		if err != nil {
			return geminiRecord{}, err
		}
		return doc.geminiRecord, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return geminiRecord{}, err
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, geminiHeadBytes)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return geminiRecord{}, err
	}
	var header geminiRecord
	if json.Unmarshal(bytes.TrimSpace(line), &header) != nil || header.isMessage() || !ValidGeminiSessionID(header.SessionID) {
		return geminiRecord{}, errors.New("gemini: session log has no metadata record")
	}
	return header, nil
}

// geminiLegacyDocument is a whole session file from releases before the
// switch to JSONL logs in April 2026.
type geminiLegacyDocument struct {
	geminiRecord
	Messages []geminiRecord `json:"messages"`
}

func readGeminiLegacy(path string) (geminiLegacyDocument, error) {
	file, err := os.Open(path)
	if err != nil {
		return geminiLegacyDocument{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return geminiLegacyDocument{}, err
	}
	if info.Size() > GeminiLegacyMaxBytes {
		return geminiLegacyDocument{}, errors.New("gemini: legacy session file is too large")
	}
	var doc geminiLegacyDocument
	if err := json.NewDecoder(file).Decode(&doc); err != nil {
		return geminiLegacyDocument{}, errors.New("gemini: legacy session file is not valid JSON")
	}
	if !ValidGeminiSessionID(doc.SessionID) {
		return geminiLegacyDocument{}, errors.New("gemini: legacy session file has no session id")
	}
	return doc, nil
}

// ReadGeminiTranscript reads a session log's metadata and best-effort state
// from its first record and its last GeminiTranscriptTailBytes. UpdatedAt
// falls back to the file's modification time.
func ReadGeminiTranscript(path string) (GeminiTranscript, error) {
	info, err := os.Stat(path)
	if err != nil {
		return GeminiTranscript{}, err
	}
	var t GeminiTranscript
	track := func(rec geminiRecord) {
		switch {
		case rec.Set != nil:
			if ts := geminiTime(rec.Set.LastUpdated); ts.After(t.UpdatedAt) {
				t.UpdatedAt = ts
			}
			if rec.Set.Summary != "" {
				t.Summary = rec.Set.Summary
			}
		case rec.isMessage():
			if ts := geminiTime(rec.Timestamp); ts.After(t.UpdatedAt) {
				t.UpdatedAt = ts
			}
			if rec.Type == "gemini" && rec.Model != "" {
				t.Model = rec.Model
			}
		}
		t.State = nextGeminiState(t.State, rec)
	}
	if strings.HasSuffix(path, ".json") {
		doc, err := readGeminiLegacy(path)
		if err != nil {
			return GeminiTranscript{}, err
		}
		t.setHeader(doc.geminiRecord)
		for _, message := range doc.Messages {
			track(message)
		}
	} else {
		header, err := readGeminiHeader(path)
		if err != nil {
			return GeminiTranscript{}, err
		}
		t.setHeader(header)
		if err := scanGeminiTail(path, info.Size(), track); err != nil {
			return GeminiTranscript{}, err
		}
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = info.ModTime()
	}
	return t, nil
}

func (t *GeminiTranscript) setHeader(header geminiRecord) {
	t.SessionID = header.SessionID
	t.ProjectHash = header.ProjectHash
	t.Kind = header.Kind
	t.Summary = header.Summary
	t.StartedAt = geminiTime(header.StartTime)
	t.UpdatedAt = geminiTime(header.LastUpdated)
}

// scanGeminiTail decodes the complete lines in the last
// GeminiTranscriptTailBytes of a log, skipping a partial first line.
func scanGeminiTail(path string, size int64, track func(geminiRecord)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	start := max(size-GeminiTranscriptTailBytes, 0)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(io.LimitReader(file, size-start))
	if start > 0 {
		if _, err := reader.ReadBytes('\n'); err != nil {
			return nil // no complete line in the tail
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// A line without a newline is still being written.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var rec geminiRecord
		if json.Unmarshal(line, &rec) == nil {
			track(rec)
		}
	}
}

// nextGeminiState follows the latest observable record. Gemini CLI records a
// model reply when its stream ends and tool calls only once they complete, so
// a reply with text reads as WAITING even when tool calls follow it, and a
// tool call waiting for approval reads as ACTIVE.
func nextGeminiState(state State, rec geminiRecord) State {
	if rec.RewindTo != nil {
		return SessionStateWaiting
	}
	if !rec.isMessage() {
		return state
	}
	switch rec.Type {
	case "user":
		return SessionStateActive
	case "gemini":
		if len(rec.ToolCalls) > 0 || !geminiHasText(rec.Content) {
			return SessionStateActive
		}
		return SessionStateWaiting
	case "error":
		return SessionStateWaiting
	}
	return state
}

// geminiHasText reports whether message content, a string or a list of
// parts, holds text other than thoughts.
func geminiHasText(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var parts []struct {
		Text    string `json:"text"`
		Thought bool   `json:"thought"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		var part struct {
			Text    string `json:"text"`
			Thought bool   `json:"thought"`
		}
		if json.Unmarshal(raw, &part) != nil {
			return false
		}
		parts = append(parts, part)
	}
	for _, part := range parts {
		if !part.Thought && strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}
