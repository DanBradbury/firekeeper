package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OpenCode keeps every session in one SQLite database, so a transcript path
// names the database and the session inside it: "<database>#<session id>".
// See internal/transcript/opencode/NOTES.md for the tables read.
const openCodePathSeparator = "#"

// openCodeHelperCommands are OpenCode subcommands that do not run a session
// of their own, such as the shared background server and account tools.
var openCodeHelperCommands = map[string]bool{
	"upgrade": true, "update": true, "uninstall": true, "acp": true, "api": true,
	"debug": true, "auth": true, "mcp": true, "plugin": true, "models": true,
	"stats": true, "session": true, "service": true, "reload": true, "pair": true,
	"serve": true, "web": true, "export": true, "import": true, "github": true,
	"agent": true,
}

// ValidOpenCodeSessionID reports whether id looks like an OpenCode session
// id: "ses" followed by letters, digits, '_' or '-'. Ids that pass are safe to
// quote into SQL.
func ValidOpenCodeSessionID(id string) bool {
	rest, ok := strings.CutPrefix(id, "ses")
	if !ok || rest == "" || len(id) > 128 {
		return false
	}
	for _, r := range rest {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// OpenCodeTranscriptPath names session id inside database.
func OpenCodeTranscriptPath(database, id string) string {
	return database + openCodePathSeparator + id
}

// SplitOpenCodeTranscriptPath returns the database and session id a path from
// OpenCodeTranscriptPath names.
func SplitOpenCodeTranscriptPath(path string) (database, id string, ok bool) {
	i := strings.LastIndex(path, openCodePathSeparator)
	if i <= 0 {
		return "", "", false
	}
	database, id = path[:i], path[i+len(openCodePathSeparator):]
	if !ValidOpenCodeSessionID(id) {
		return "", "", false
	}
	return database, id, true
}

// OpenCodeDatabase returns OpenCode's database path the way OpenCode
// resolves it: OPENCODE_DB, absolute or relative to the data directory, else
// opencode.db in $XDG_DATA_HOME/opencode (default ~/.local/share/opencode, on
// macOS too). It returns "" when OPENCODE_DB is ":memory:", since nothing is
// stored then. An empty home means the user's home directory.
func OpenCodeDatabase(home string) (string, error) {
	return (&discoverer{opts: Options{Home: home}}).openCodeDatabase()
}

func (d *discoverer) openCodeDatabase() (string, error) {
	if d.opts.OpenCodeDB != "" {
		return d.opts.OpenCodeDB, nil
	}
	data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if !filepath.IsAbs(data) {
		home, err := d.home()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		data = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(data, "opencode")
	switch db := strings.TrimSpace(os.Getenv("OPENCODE_DB")); {
	case db == ":memory:":
		return "", nil
	case db == "":
		return filepath.Join(dir, "opencode.db"), nil
	case filepath.IsAbs(db):
		return db, nil
	default:
		return filepath.Join(dir, db), nil
	}
}

// openCodeArgs returns the arguments after the OpenCode executable, or nil
// when command does not run it.
func (d *discoverer) openCodeArgs(command string) []string {
	fields := strings.Fields(command)
	for i, field := range fields {
		base := strings.ToLower(filepath.Base(field))
		if base == "opencode" || base == "opencode.exe" {
			return append([]string{}, fields[i+1:]...)
		}
	}
	return nil
}

// openCodeRunsSession reports whether command runs an OpenCode session: the
// interactive default command, run, or mini, but not a helper subcommand.
func (d *discoverer) openCodeRunsSession(command string) bool {
	args := d.openCodeArgs(command)
	if args == nil {
		return false
	}
	return len(args) == 0 || !openCodeHelperCommands[args[0]]
}

// openCodeSessionIDFromCommand reads an explicit --session/-s id. A forked
// continuation writes a new session, so --fork drops the id.
func (d *discoverer) openCodeSessionIDFromCommand(command string) (string, bool) {
	if !d.openCodeRunsSession(command) {
		return "", false
	}
	args := d.openCodeArgs(command)
	id, fork := "", false
	for i, field := range args {
		switch {
		case field == "--fork":
			fork = true
		case field == "--session" || field == "-s":
			if i+1 < len(args) {
				id = args[i+1]
			}
		case strings.HasPrefix(field, "--session="):
			id = strings.TrimPrefix(field, "--session=")
		case strings.HasPrefix(field, "-s="):
			id = strings.TrimPrefix(field, "-s=")
		}
	}
	id = strings.Trim(id, "'\"")
	if fork || !ValidOpenCodeSessionID(id) {
		return "", false
	}
	return id, true
}

func (d *discoverer) openCodeSessionIDFromGroupCommands(group ProcessGroup) (string, bool) {
	for _, process := range append([]ProcessInfo{group.Root}, group.Processes...) {
		if id, ok := d.openCodeSessionIDFromCommand(process.Command); ok {
			return id, true
		}
	}
	return "", false
}

func (d *discoverer) enrichOpenCodeSessions(groups []ProcessGroup) error {
	var groupIndexes []int
	for index := range groups {
		if groups[index].Tool == "OpenCode" && d.openCodeRunsSession(groups[index].Root.Command) {
			groupIndexes = append(groupIndexes, index)
		}
	}
	if len(groupIndexes) == 0 {
		return nil
	}
	db, err := d.openCodeDatabase()
	if err != nil || db == "" {
		return err
	}
	if _, err := os.Stat(db); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing has been stored yet
		}
		return errors.New("stat OpenCode database")
	}

	// Explicit ids from the command line win.
	ids := make(map[int]string, len(groupIndexes))
	claimed := make(map[string]bool)
	var unmapped []int
	for _, index := range groupIndexes {
		if id, ok := d.openCodeSessionIDFromGroupCommands(groups[index]); ok {
			ids[index], claimed[id] = id, true
			continue
		}
		unmapped = append(unmapped, index)
	}

	// Otherwise take the most recently updated top-level sessions in each
	// runtime's working directory. When several runtimes share a directory,
	// newer processes (higher PIDs) get newer sessions. This is a guess; see
	// NOTES.md.
	if len(unmapped) > 0 {
		workDirs := d.claudeWorkDirs(groups, unmapped)
		byDir := make(map[string][]int)
		for _, index := range unmapped {
			if cwd := workDirs[groups[index].Root.PID]; cwd != "" {
				byDir[cwd] = append(byDir[cwd], index)
			}
		}
		if len(byDir) > 0 {
			dirs := make([]string, 0, len(byDir))
			for dir := range byDir {
				dirs = append(dirs, dir)
			}
			recent, err := d.recentOpenCodeSessions(db, dirs)
			if err != nil {
				return err
			}
			for cwd, indexes := range byDir {
				sort.Slice(indexes, func(i, j int) bool { return groups[indexes[i]].Root.PID > groups[indexes[j]].Root.PID })
				next := 0
				for _, id := range recent[cwd] {
					if next == len(indexes) {
						break
					}
					if !claimed[id] {
						ids[indexes[next]], claimed[id] = id, true
						next++
					}
				}
			}
		}
	}

	wanted := make([]string, 0, len(ids))
	for _, id := range ids {
		wanted = append(wanted, id)
	}
	rows, err := d.readOpenCodeSessions(db, wanted)
	if err != nil {
		return err
	}
	missing := 0
	for _, index := range groupIndexes {
		row, ok := rows[ids[index]]
		if !ok {
			missing++
			continue
		}
		groups[index].Sessions = []Info{row.info(db)}
	}
	if missing > 0 {
		return fmt.Errorf("metadata unavailable for %d runtime(s)", missing)
	}
	return nil
}

// sqlQuote quotes value as an SQL string literal.
func sqlQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (d *discoverer) openCodeQuery(db, query string, rows any) error {
	output, err := d.command("sqlite3", "-readonly", "-json", db, query).Output()
	if err != nil {
		return fmt.Errorf("read OpenCode database: %w", err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil
	}
	if err := json.Unmarshal(output, rows); err != nil {
		return errors.New("decode OpenCode database rows")
	}
	return nil
}

// recentOpenCodeSessions returns top-level session ids per directory, most
// recently updated first.
func (d *discoverer) recentOpenCodeSessions(db string, dirs []string) (map[string][]string, error) {
	quoted := make([]string, len(dirs))
	for i, dir := range dirs {
		quoted[i] = sqlQuote(dir)
	}
	var rows []struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
	}
	query := fmt.Sprintf(`SELECT id, directory FROM session_v2
		WHERE parent_id IS NULL AND directory IN (%s)
		ORDER BY time_updated DESC, id DESC LIMIT 200`, strings.Join(quoted, ","))
	if err := d.openCodeQuery(db, query, &rows); err != nil {
		return nil, err
	}
	result := make(map[string][]string)
	for _, row := range rows {
		if ValidOpenCodeSessionID(row.ID) {
			result[row.Directory] = append(result[row.Directory], row.ID)
		}
	}
	return result, nil
}

// openCodeSessionRow is the session metadata discovery reads. No message
// text is selected; only the type of the latest message is, for state.
type openCodeSessionRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Directory   string `json:"directory"`
	Model       string `json:"model"`
	CreatedMS   int64  `json:"time_created"`
	UpdatedMS   int64  `json:"time_updated"`
	Input       int64  `json:"tokens_input"`
	Output      int64  `json:"tokens_output"`
	Reasoning   int64  `json:"tokens_reasoning"`
	CacheRead   int64  `json:"tokens_cache_read"`
	CacheWrite  int64  `json:"tokens_cache_write"`
	LastType    string `json:"last_type"`
	LastUpdated int64  `json:"last_updated"`
}

func (d *discoverer) readOpenCodeSessions(db string, ids []string) (map[string]openCodeSessionRow, error) {
	result := make(map[string]openCodeSessionRow, len(ids))
	var quoted []string
	for _, id := range ids {
		if ValidOpenCodeSessionID(id) {
			quoted = append(quoted, sqlQuote(id))
		}
	}
	if len(quoted) == 0 {
		return result, nil
	}
	var rows []openCodeSessionRow
	query := fmt.Sprintf(`SELECT s.id, COALESCE(s.title, '') AS title, s.directory,
			COALESCE(s.model, '') AS model, s.time_created, s.time_updated,
			s.tokens_input, s.tokens_output, s.tokens_reasoning,
			s.tokens_cache_read, s.tokens_cache_write,
			COALESCE((SELECT m.type FROM session_message m WHERE m.session_id = s.id
				ORDER BY m.seq DESC LIMIT 1), '') AS last_type,
			COALESCE((SELECT MAX(m.time_updated) FROM session_message m WHERE m.session_id = s.id), 0) AS last_updated
		FROM session_v2 s WHERE s.id IN (%s)`, strings.Join(quoted, ","))
	if err := d.openCodeQuery(db, query, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

// OpenCodeModel returns the model id from a session or message model
// reference, {"id": ..., "providerID": ...}, or "" when it has none.
func OpenCodeModel(ref []byte) string {
	var model struct {
		ID string `json:"id"`
	}
	if len(ref) == 0 || json.Unmarshal(ref, &model) != nil {
		return ""
	}
	return strings.TrimSpace(model.ID)
}

func (row openCodeSessionRow) info(db string) Info {
	clean := func(value string) string { return strings.TrimSpace(SanitizeProcessCommand(value)) }
	tokens := TokenBreakdown{
		Input:      max(row.Input, 0),
		Output:     max(row.Output, 0) + max(row.Reasoning, 0),
		CacheRead:  max(row.CacheRead, 0),
		CacheWrite: max(row.CacheWrite, 0),
	}
	return Info{
		ID:          row.ID,
		Name:        clean(row.Title),
		State:       openCodeState(row.LastType),
		CWD:         clean(row.Directory),
		Model:       clean(OpenCodeModel([]byte(row.Model))),
		RolloutPath: OpenCodeTranscriptPath(db, row.ID),
		StartedAt:   unixMilli(row.CreatedMS),
		UpdatedAt:   unixMilli(max(row.UpdatedMS, row.LastUpdated)),
		TokensUsed:  tokens.Total(),
		Tokens:      tokens,
	}
}

// openCodeState maps the latest message type to a state. OpenCode writes an
// idle message when a turn ends, so anything after the last idle marker is a
// turn in progress. A session with no messages yet is waiting for its first
// prompt. Permission and question prompts are not visible here, so
// NEEDS INPUT is never reported.
func openCodeState(lastType string) State {
	switch lastType {
	case "", "idle":
		return SessionStateWaiting
	case "user", "assistant", "shell", "compaction", "synthetic", "system", "skill":
		return SessionStateActive
	default:
		return SessionStateUnknown
	}
}

func unixMilli(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
