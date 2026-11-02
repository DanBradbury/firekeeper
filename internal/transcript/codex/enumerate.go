package codex

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
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/sqlitejson"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// storeTimeout bounds the state database queries.
const storeTimeout = 10 * time.Second

// threadRow is the metadata Enumerate takes from state_5.sqlite. Columns
// that hold conversation text (preview, first_user_message) are not read.
type threadRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	Model     string `json:"model"`
	Branch    string `json:"git_branch"`
	CreatedMS int64  `json:"created_ms"`
	UpdatedMS int64  `json:"updated_ms"`
	// Source is the thread's session source. For a subagent it is JSON
	// naming the parent thread, the agent's nickname and its role.
	Source string `json:"source"`
}

// Enumerate lists every rollout under sessions/*/*/*/ and archived_sessions/.
// Metadata comes from the threads table of state_5.sqlite, read with
// sqlite3 -readonly. Only when a row is missing the working directory,
// branch, model, or start time is the rollout opened, and then only its first
// reader.HeadBytes are read. A thread with rollouts in both places is
// listed once, from the most recently modified file.
func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	home, err := s.codexHome()
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id    string
		path  string
		size  int64
		mtime time.Time
	}
	byID := map[string]candidate{}
	for _, pattern := range []string{
		filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"),
		filepath.Join(home, "archived_sessions", "rollout-*.jsonl"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("codex: search rollouts: %w", err)
		}
		for _, path := range matches {
			id, ok := session.ThreadIDFromRolloutPath(path)
			if !ok {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
				continue
			}
			key := strings.ToLower(id)
			if prev, dup := byID[key]; dup && !info.ModTime().After(prev.mtime) {
				continue
			}
			byID[key] = candidate{id: id, path: path, size: info.Size(), mtime: info.ModTime()}
		}
	}
	candidates := make([]candidate, 0, len(byID))
	for _, c := range byID {
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var warnings []string
	var rows map[string]threadRow
	if len(candidates) > 0 {
		if rows, err = s.readThreads(ctx, home); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			warnings = append(warnings, err.Error())
		}
	}

	var metas []session.Meta
	mtimes := make(map[string]time.Time, len(candidates))
	bad := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.size == 0 {
			bad++
			continue
		}
		meta := session.Meta{ID: c.id, Provider: string(transcript.ProviderCodex), State: session.SessionStateEnded, RolloutPath: c.path}
		// A row whose source column is filled says authoritatively whether
		// the thread is a subagent; the rollout head is consulted only
		// without one.
		sourceKnown := false
		if row, ok := rows[strings.ToLower(c.id)]; ok {
			sourceKnown = strings.TrimSpace(row.Source) != ""
			meta.Title = clean(row.Title)
			meta.CWD = clean(row.CWD)
			meta.Model = clean(row.Model)
			meta.Branch = clean(row.Branch)
			meta.StartedAt = millis(row.CreatedMS)
			meta.LastActivityAt = millis(row.UpdatedMS)
			setSpawn(&meta, session.ParseCodexSpawn([]byte(row.Source)))
		}
		checked := false
		headRead, forkedFrom := false, ""
		if meta.CWD != "" && opts.Skip != nil {
			checked = true
			if opts.Skip(finish(meta)) {
				continue
			}
		}
		if meta.CWD == "" || meta.Branch == "" || meta.Model == "" || meta.StartedAt == nil {
			h, err := s.readHead(c.path, c.id)
			if err != nil {
				bad++
				continue
			}
			// A first line longer than the head (a large session_meta) leaves
			// no header; it is read on its own below.
			headRead = h.sawMeta
			if h.forkedKnown {
				forkedFrom = h.forkedFrom
			}
			if !sourceKnown {
				setSpawn(&meta, h.spawn)
			}
			meta.CWD = session.FirstNonEmpty(meta.CWD, clean(h.cwd))
			meta.Branch = session.FirstNonEmpty(meta.Branch, clean(h.branch))
			meta.Model = session.FirstNonEmpty(meta.Model, clean(h.model))
			if meta.StartedAt == nil && !h.started.IsZero() {
				started := h.started
				meta.StartedAt = &started
			}
		}
		// Only the rollout's own session_meta says whether the thread is a
		// fork; the copies a fork embeds name other forks. The database does
		// not record it, so a thread whose head was not read above has its
		// first line, where Codex writes its own session_meta, read here.
		if !headRead {
			if own, ok := s.readOwnMeta(c.path, c.id); ok {
				forkedFrom = own.ForkedFromID
			}
		}
		if forkedFrom != "" && forkedFrom != meta.ID {
			meta.ForkedFromID = clean(forkedFrom)
		}
		meta = finish(meta)
		if !checked && opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		mtimes[c.path] = c.mtime
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, mtimes)
	if bad > 0 {
		warnings = append(warnings, fmt.Sprintf("codex: skipped %d empty, unreadable, or corrupt rollout(s)", bad))
	}
	if len(warnings) > 0 {
		return metas, &session.Warning{Message: strings.Join(warnings, "; ")}
	}
	return metas, nil
}

// setSpawn records who spawned a subagent thread. A thread that names
// itself as its parent is treated as top-level. Ids are compared exactly,
// as the upload contract and the reporter compare them.
func setSpawn(meta *session.Meta, spawn session.CodexSpawn) {
	if spawn.ParentID == "" || spawn.ParentID == meta.ID {
		return
	}
	meta.ParentID = clean(spawn.ParentID)
	meta.AgentName = clean(spawn.AgentName)
	meta.AgentRole = clean(spawn.AgentRole)
}

// finish fills the fields derived from the others, matching Discover.
func finish(meta session.Meta) session.Meta {
	meta.Project = reader.ProjectName(meta.CWD)
	meta.Name = meta.Title
	meta.GitBranch = meta.Branch
	if meta.LastActivityAt != nil {
		meta.UpdatedAt = *meta.LastActivityAt
	}
	return meta
}

func clean(value string) string {
	return strings.TrimSpace(session.SanitizeProcessCommand(value))
}

func millis(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// readThreads returns threads rows keyed by lower-case id. A missing
// database is not an error; Enumerate falls back to rollout heads.
func (s *Source) readThreads(ctx context.Context, home string) (map[string]threadRow, error) {
	path := filepath.Join(home, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, errors.New("codex: state database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	output, err := s.sqlite(ctx, path, "PRAGMA table_info(threads)")
	if err != nil {
		return nil, errors.New("codex: inspect state database failed")
	}
	var schema []struct {
		Name string `json:"name"`
	}
	if len(bytes.TrimSpace(output)) > 0 {
		if err := json.Unmarshal(output, &schema); err != nil {
			return nil, errors.New("codex: decode state database schema failed")
		}
	}
	columns := make(map[string]bool, len(schema))
	for _, column := range schema {
		columns[column.Name] = true
	}
	if !columns["id"] {
		return nil, errors.New("codex: state database has no threads table")
	}
	query := fmt.Sprintf(`SELECT id, %s AS title, %s AS cwd, %s AS model, %s AS git_branch, %s AS created_ms, %s AS updated_ms, %s AS source FROM threads`,
		textColumn(columns, "name", "title"), textColumn(columns, "cwd"), textColumn(columns, "model"),
		textColumn(columns, "git_branch"), timeColumn(columns, "created"), timeColumn(columns, "updated"), textColumn(columns, "source"))
	output, err = s.sqlite(ctx, path, query)
	if err != nil {
		return nil, errors.New("codex: read state database failed")
	}
	var rows []threadRow
	if len(bytes.TrimSpace(output)) > 0 {
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, errors.New("codex: decode state database failed")
		}
	}
	result := make(map[string]threadRow, len(rows))
	for _, row := range rows {
		result[strings.ToLower(row.ID)] = row
	}
	return result, nil
}

func textColumn(columns map[string]bool, candidates ...string) string {
	parts := []string{}
	for _, c := range candidates {
		if columns[c] {
			parts = append(parts, "NULLIF("+c+", '')")
		}
	}
	return "COALESCE(" + strings.Join(append(parts, "''"), ", ") + ")"
}

// timeColumn reads <prefix>_at_ms, falling back to <prefix>_at in seconds.
func timeColumn(columns map[string]bool, prefix string) string {
	parts := []string{}
	if columns[prefix+"_at_ms"] {
		parts = append(parts, "NULLIF("+prefix+"_at_ms, 0)")
	}
	if columns[prefix+"_at"] {
		parts = append(parts, "NULLIF("+prefix+"_at, 0) * 1000")
	}
	return "COALESCE(" + strings.Join(append(parts, "0"), ", ") + ")"
}

func (s *Source) sqlite(ctx context.Context, path, query string) ([]byte, error) {
	args := []string{"-readonly", "-json", path, query}
	if s.run != nil {
		return s.run(ctx, "sqlite3", args...)
	}
	return sqlitejson.Run(ctx, args...)
}

// readOwnMeta reads the thread's own session_meta from the first line of
// its rollout.
func (s *Source) readOwnMeta(path, id string) (session.CodexSessionMeta, bool) {
	open := s.open
	if open == nil {
		open = func(name string) (io.ReadCloser, error) { return os.Open(name) }
	}
	file, err := open(path)
	if err != nil {
		return session.CodexSessionMeta{}, false
	}
	defer file.Close()
	return session.ReadCodexSessionMeta(file, id)
}

type head struct {
	cwd, branch, model string
	started            time.Time
	spawn              session.CodexSpawn
	// forkedFrom is the own header's forked_from_id; forkedKnown says the
	// own header was found.
	forkedFrom  string
	forkedKnown bool
	// sawMeta says the head held a session_meta at all.
	sawMeta bool
}

// readHead decodes the first reader.HeadBytes of a rollout. It is
// corrupt when no complete line there is a JSON object.
//
// A forked rollout embeds copies of the source thread's session_meta lines
// after its own, so the session_meta whose id is the thread's describes this
// thread and the others are ignored, as Codex's own state extraction does.
// Without one, the first session_meta is used.
func (s *Source) readHead(path, id string) (head, error) {
	open := s.open
	if open == nil {
		open = func(name string) (io.ReadCloser, error) { return os.Open(name) }
	}
	file, err := open(path)
	if err != nil {
		return head{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, reader.HeadBytes))
	scanner.Buffer(make([]byte, 0, 64<<10), reader.HeadBytes)
	type metaHead struct {
		cwd, branch string
		started     time.Time
		spawn       session.CodexSpawn
		forkedFrom  string
		noID, ok    bool
	}
	var h head
	var first, own metaHead
	var turnCWD string
	var lineTime time.Time
	decoded := false
	for scanner.Scan() {
		var r struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				ID        string `json:"id"`
				Timestamp string `json:"timestamp"`
				CWD       string `json:"cwd"`
				Model     string `json:"model"`
				Git       *struct {
					Branch string `json:"branch"`
				} `json:"git"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		decoded = true
		switch r.Type {
		case "session_meta":
			var raw struct {
				Payload json.RawMessage `json:"payload"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &raw)
			parsed := session.ParseCodexSessionMeta(raw.Payload)
			m := metaHead{cwd: r.Payload.CWD, started: parseTime(r.Payload.Timestamp), spawn: parsed.Spawn, forkedFrom: parsed.ForkedFromID, noID: parsed.ID == "", ok: true}
			if r.Payload.Git != nil {
				m.branch = r.Payload.Git.Branch
			}
			if !first.ok {
				first = m
			}
			if !own.ok && id != "" && strings.EqualFold(r.Payload.ID, id) {
				own = m
			}
		case "turn_context":
			turnCWD = session.FirstNonEmpty(turnCWD, r.Payload.CWD)
			if r.Payload.Model != "" {
				h.model = r.Payload.Model
			}
		}
		if lineTime.IsZero() {
			lineTime = parseTime(r.Timestamp)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return head{}, err
	}
	if !decoded {
		return head{}, errors.New("no decodable record")
	}
	h.sawMeta = first.ok
	m := first
	if own.ok {
		m = own
	}
	h.cwd = session.FirstNonEmpty(m.cwd, turnCWD)
	h.branch = m.branch
	h.spawn = m.spawn
	// A fork is known only from the thread's own header (or one without an
	// id); a copied header names the source's fork, not this thread's.
	if own.ok || m.noID {
		h.forkedFrom, h.forkedKnown = m.forkedFrom, true
	}
	h.started = m.started
	if h.started.IsZero() {
		h.started = lineTime
	}
	return h, nil
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
